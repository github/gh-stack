package cmd

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/pr"
	"github.com/spf13/cobra"
)

type linkOptions struct {
	base        string
	open        bool
	remote      string
	baseChanged bool
}

func LinkCmd(cfg *config.Config) *cobra.Command {
	opts := &linkOptions{}

	cmd := &cobra.Command{
		Use:   "link <stack-number | branch-or-pr> <branch-or-pr> [<branch-or-pr>...]",
		Short: "Link PRs into a stack on GitHub without local tracking",
		Long: `Create or update a stack on GitHub from branch names, PR numbers, or PR URLs.

This command does not rely on gh-stack local tracking state. It is
designed for users who manage branches with external tools (e.g. jj,
Sapling, ghstack, git-town, etc...) and want to use GitHub stacked
PRs without adopting local stack tracking.

Arguments are provided in stack order (bottom to top). Each argument
can be a branch name, a PR number, or a PR URL (e.g.
https://github.com/owner/repo/pull/123). For numeric arguments, the
command first checks if a PR with that number exists; if not, it
treats the argument as a branch name. PR URLs are always resolved
as pull requests (never as branch names).

Branch arguments are automatically pushed to the remote before
creating or looking up PRs. For branches that already have open PRs,
those PRs are used. For branches without PRs, new PRs are created
automatically with the correct base branch chaining.

If the PRs are not yet in a stack, a new stack is created. If some of
the PRs are already in a stack, the existing stack is updated to include
the new PRs (existing PRs are never removed).

As a shortcut for growing an existing stack, pass a stack number as the
first argument (the number shown in the GitHub stack UI). The remaining
arguments are appended to the top of that stack, so you don't have to
re-list its current PRs. Arguments already in the stack are skipped;
arguments that belong to a different stack are rejected. Because stack and
PR numbers never overlap, a numeric first argument is treated as a stack
only when it matches an existing stack.`,
		Example: `  # Link branches into a stack (bottom to top)
  $ gh stack link auth-layer api-routes ui-components

  # Link existing PRs by number
  $ gh stack link 41 42 43

  # Link existing PRs by URL
  $ gh stack link https://github.com/owner/repo/pull/41 https://github.com/owner/repo/pull/42

  # Add PRs to the top of an existing stack (7 is a stack number)
  $ gh stack link 7 48 ui-polish

  # Specify a custom base branch for stack
  $ gh stack link --base develop auth-layer api-routes`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.baseChanged = cmd.Flags().Changed("base")
			return runLink(cfg, opts, args)
		},
	}

	cmd.Flags().StringVar(&opts.base, "base", "main", "Base branch for the bottom of the stack")
	cmd.Flags().BoolVar(&opts.open, "open", false, "Mark new and existing PRs as ready for review")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to push to (defaults to auto-detected remote)")

	return cmd
}

// resolvedArg holds the result of resolving a single CLI argument to a PR.
type resolvedArg struct {
	branch   string              // head branch name
	prNumber int                 // PR number
	prURL    string              // PR URL (for display)
	created  bool                // true if we created this PR (skip base-fix re-fetch)
	pr       *github.PullRequest // full PR data (only set for existing PRs)
}

func runLink(cfg *config.Config, opts *linkOptions, args []string) error {
	if err := validateArgs(args); err != nil {
		cfg.Errorf("%s", err)
		return ErrInvalidArgs
	}

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}

	// Fetch existing stacks up front. They are needed to detect whether the
	// first argument names an existing stack (add mode) and, in the
	// create/update path, to find the stack the PRs already belong to.
	cfg.Printf("Checking existing stacks...")
	stacks, err := listStacksSafe(cfg, client)
	if err != nil {
		return err
	}

	// Detect "add mode": when the first argument is a bare stack number that
	// matches an existing stack, the remaining arguments are appended to the
	// top of that stack. Stack, PR, and issue numbers share one repo-scoped
	// numberspace, so a number that names a stack never also names a PR.
	targetStack, prArgs := detectAddMode(args, stacks)

	// Phase 1: Push branch args to the remote so PRs can be found/created.
	if err := pushBranchArgs(cfg, opts, prArgs); err != nil {
		return err
	}

	// Phase 2: Find existing PRs for all PR args (don't create yet).
	cfg.Printf("Looking up PRs for %d %s...", len(prArgs), plural(len(prArgs), "branch", "branches"))
	found, err := findExistingPRs(cfg, client, prArgs)
	if err != nil {
		return err
	}

	// Look up the repository's PR template (best-effort; skip if not in a repo).
	var templateContent string
	if repoRoot, tlErr := git.RootDir(); tlErr == nil {
		templateContent = pr.FindTemplate(repoRoot)
	}

	// Add mode: append the PR args to the top of the named stack.
	if targetStack != nil {
		return runLinkAdd(cfg, client, opts, targetStack, stacks, prArgs, found, templateContent)
	}

	// Create/update mode: create a new stack or additively update the stack
	// the PRs already belong to.
	return runLinkCreateOrUpdate(cfg, client, opts, stacks, prArgs, found, templateContent)
}

// detectAddMode reports whether the first argument names an existing stack,
// enabling "add mode" in which the remaining arguments are appended to the top
// of that stack. It returns the matched stack (or nil) and the PR arguments to
// resolve — args[1:] in add mode, or all args otherwise.
//
// Only a bare positive integer can name a stack. Because stack, PR, and issue
// numbers share one repo-scoped numberspace, a number that matches a stack can
// never also be a PR number, so this never shadows a create/update invocation.
func detectAddMode(args []string, stacks []github.RemoteStack) (*github.RemoteStack, []string) {
	if len(args) < 2 {
		return nil, args
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		return nil, args
	}
	for i := range stacks {
		if stacks[i].Number == n {
			return &stacks[i], args[1:]
		}
	}
	return nil, args
}

// runLinkCreateOrUpdate creates a new stack from the resolved PR args, or
// additively updates the single stack the PRs already belong to. This is the
// original link behavior, where every PR in the stack must be listed.
func runLinkCreateOrUpdate(cfg *config.Config, client github.ClientOps, opts *linkOptions, stacks []github.RemoteStack, prArgs []string, found []*resolvedArg, templateContent string) error {
	knownPRNumbers := make([]int, 0, len(found))
	for _, r := range found {
		if r != nil {
			knownPRNumbers = append(knownPRNumbers, r.prNumber)
		}
	}

	// Determine the stack these PRs already belong to (if any). PRs that are
	// already members of this stack are exempt from the eligibility checks
	// below, since they are not being added — they are already present.
	targetStack, err := findMatchingStack(stacks, knownPRNumbers)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrDisambiguate
	}

	// Validate that all found PRs are eligible to be added to a stack. Only
	// open/draft PRs without auto-merge enabled are allowed, except for PRs
	// already in the target stack.
	if err := validatePREligibility(cfg, found, targetStack); err != nil {
		return err
	}

	// Pre-validate the stack — check that adding these PRs won't drop existing
	// PRs from the target stack before creating any new PRs, so we can fail
	// early without leaving orphaned PRs.
	if targetStack != nil {
		if err := prevalidateStack(cfg, targetStack, knownPRNumbers); err != nil {
			return err
		}
	}

	// Create PRs for branches that don't have one yet.
	needsCreation := 0
	for _, r := range found {
		if r == nil {
			needsCreation++
		}
	}
	if needsCreation > 0 {
		cfg.Printf("Creating %d %s...", needsCreation, plural(needsCreation, "PR", "PRs"))
	}
	resolved, err := createMissingPRs(cfg, client, opts, prArgs, found, templateContent, opts.base)
	if err != nil {
		return err
	}

	// Fix base branches for existing PRs with wrong bases.
	fixBaseBranches(cfg, client, opts, resolved, opts.base)

	// Upsert the stack (reuse the stacks fetched above).
	prNumbers := make([]int, len(resolved))
	for i, r := range resolved {
		prNumbers[i] = r.prNumber
	}

	return upsertStack(cfg, client, stacks, prNumbers)
}

// runLinkAdd appends the resolved PR args to the top of an existing stack. PRs
// already in the target stack are skipped (idempotent); PRs that belong to a
// different stack are rejected. Branch args without a PR get one created and
// chained on top of the stack's current top branch.
func runLinkAdd(cfg *config.Config, client github.ClientOps, opts *linkOptions, target *github.RemoteStack, stacks []github.RemoteStack, prArgs []string, found []*resolvedArg, templateContent string) error {
	if opts.baseChanged {
		cfg.Warningf("--base is ignored when adding to stack #%d (its base is fixed by the existing stack)", target.Number)
	}

	inTarget := make(map[int]bool, len(target.PRNumbers()))
	for _, n := range target.PRNumbers() {
		inTarget[n] = true
	}

	// Partition the args into those already in the target stack (skipped) and
	// those to append. found is parallel to prArgs; a nil entry is a branch
	// with no PR yet, which is always new.
	var appendArgs []string
	var appendFound []*resolvedArg
	for i, arg := range prArgs {
		r := found[i]
		if r != nil && inTarget[r.prNumber] {
			cfg.Infof("PR %s is already in stack #%d — skipping",
				cfg.PRLink(r.prNumber, r.prURL), target.Number)
			continue
		}
		appendArgs = append(appendArgs, arg)
		appendFound = append(appendFound, r)
	}

	// Enforce the one-stack constraint: none of the PRs being appended may
	// belong to a different stack.
	if err := ensureNotInOtherStack(cfg, stacks, target.Number, appendFound); err != nil {
		return err
	}

	if len(appendArgs) == 0 {
		cfg.Successf("Stack #%d is already up to date", target.Number)
		return nil
	}

	// Validate eligibility of the PRs being appended. Target members were
	// filtered out above, so every remaining PR faces the full checks.
	if err := validatePREligibility(cfg, appendFound, nil); err != nil {
		return err
	}

	// New PRs chain on top of the stack's current top branch. The stack list
	// response should carry per-PR head refs, but fall back to fetching the
	// full stack if the top branch can't be resolved from the listed stack.
	topBranch, err := stackTopBranch(target)
	if err != nil {
		if full, gerr := client.GetStack(target.Number); gerr == nil && full != nil {
			topBranch, err = stackTopBranch(full)
		}
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrAPIFailure
		}
	}

	needsCreation := 0
	for _, r := range appendFound {
		if r == nil {
			needsCreation++
		}
	}
	if needsCreation > 0 {
		cfg.Printf("Creating %d %s...", needsCreation, plural(needsCreation, "PR", "PRs"))
	}
	resolved, err := createMissingPRs(cfg, client, opts, appendArgs, appendFound, templateContent, topBranch)
	if err != nil {
		return err
	}

	// Correct base branches so the appended PRs chain on top of the stack.
	fixBaseBranches(cfg, client, opts, resolved, topBranch)

	delta := make([]int, len(resolved))
	for i, r := range resolved {
		delta[i] = r.prNumber
	}

	return addToStack(cfg, client, target.Number, delta)
}

// ensureNotInOtherStack verifies that none of the resolved PRs belong to a
// stack other than the target. PRs in no stack are allowed. Reports every
// offender before returning an error.
func ensureNotInOtherStack(cfg *config.Config, stacks []github.RemoteStack, targetNumber int, found []*resolvedArg) error {
	owner := make(map[int]int)
	for i := range stacks {
		for _, n := range stacks[i].PRNumbers() {
			owner[n] = stacks[i].Number
		}
	}

	invalid := 0
	for _, r := range found {
		if r == nil {
			continue
		}
		if sn, ok := owner[r.prNumber]; ok && sn != targetNumber {
			cfg.Errorf("PR %s already belongs to stack #%d — unstack it first",
				cfg.PRLink(r.prNumber, r.prURL), sn)
			invalid++
		}
	}
	if invalid > 0 {
		return ErrInvalidArgs
	}
	return nil
}

// stackTopBranch returns the head branch of the pull request at the top of the
// stack — the base for the first PR appended on top of it.
func stackTopBranch(s *github.RemoteStack) (string, error) {
	if len(s.PRDetails) == 0 {
		return "", fmt.Errorf("stack #%d has no pull requests to append to", s.Number)
	}
	top := s.PRDetails[len(s.PRDetails)-1]
	if top.Head.Ref == "" {
		return "", fmt.Errorf("could not determine the top branch of stack #%d", s.Number)
	}
	return top.Head.Ref, nil
}

// addToStack appends the delta PR numbers to the top of the target stack and
// reports the result, translating API errors into typed exit codes.
func addToStack(cfg *config.Config, client github.ClientOps, stackNumber int, delta []int) error {
	if _, err := client.AddToStack(stackNumber, delta); err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				cfg.Errorf("Stack #%d no longer exists", stackNumber)
				return ErrNotInStack
			case 422:
				cfg.Errorf("Cannot add to stack: %s", httpErr.Message)
				return ErrAPIFailure
			default:
				cfg.Errorf("Failed to add to stack (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to add to stack: %v", err)
		return ErrAPIFailure
	}

	cfg.Successf("Added %d %s to stack #%d", len(delta), plural(len(delta), "PR", "PRs"), stackNumber)
	return nil
}

// pushBranchArgs pushes all arguments that correspond to local branches
// to the remote. This ensures branches exist on the server before we try
// to create or look up PRs. Args that are pure PR numbers (not local
// branch names) are skipped.
func pushBranchArgs(cfg *config.Config, opts *linkOptions, args []string) error {
	var branches []string
	for _, arg := range args {
		if git.BranchExists(arg) {
			branches = append(branches, arg)
		}
	}

	if len(branches) == 0 {
		return nil
	}

	// Resolve the remote using the first branch as context
	remote, err := pickRemote(cfg, branches[0], opts.remote)
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return ErrSilent
	}

	cfg.Printf("Pushing %d %s to %s...", len(branches), plural(len(branches), "branch", "branches"), remote)
	if err := git.Push(remote, branches, false, true); err != nil {
		cfg.Errorf("failed to push branches: %s", err)
		return ErrSilent
	}

	return nil
}

// validateArgs checks for duplicates in the arg list.
func validateArgs(args []string) error {
	seen := make(map[string]bool, len(args))
	for _, arg := range args {
		if seen[arg] {
			return fmt.Errorf("duplicate argument: %q", arg)
		}
		seen[arg] = true
	}
	return nil
}

// findExistingPRs looks up existing PRs for each arg without creating any.
// Returns a slice parallel to args where each entry is either a resolved PR
// or nil (meaning the branch has no PR yet and one needs to be created).
func findExistingPRs(cfg *config.Config, client github.ClientOps, args []string) ([]*resolvedArg, error) {
	found := make([]*resolvedArg, len(args))

	for i, arg := range args {
		r, err := findExistingPR(cfg, client, arg)
		if err != nil {
			return nil, err
		}
		if r != nil {
			// Check for duplicate PR numbers
			for j := 0; j < i; j++ {
				if found[j] != nil && found[j].prNumber == r.prNumber {
					cfg.Errorf("arguments %q and %q resolve to the same PR #%d", found[j].branch, r.branch, r.prNumber)
					return nil, ErrInvalidArgs
				}
			}
		}
		found[i] = r
	}

	return found, nil
}

// findExistingPR looks up an existing PR for a single arg.
// Returns nil if the arg is a branch with no open PR.
func findExistingPR(cfg *config.Config, client github.ClientOps, arg string) (*resolvedArg, error) {
	// If the arg is a PR URL, extract the number and look it up.
	// Unlike numeric args, a URL can never be a valid branch name,
	// so we error instead of falling through to branch lookup.
	if n, ok := parsePRURL(arg); ok {
		pr, err := client.FindPRByNumber(n)
		if err != nil {
			cfg.Errorf("failed to look up PR #%d: %v", n, err)
			return nil, ErrAPIFailure
		}
		if pr == nil {
			cfg.Errorf("PR #%d not found", n)
			return nil, ErrInvalidArgs
		}
		return &resolvedArg{
			branch:   pr.HeadRefName,
			prNumber: pr.Number,
			prURL:    pr.URL,
			pr:       pr,
		}, nil
	}

	// If numeric, try as PR number first
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		pr, err := client.FindPRByNumber(n)
		if err != nil {
			cfg.Errorf("failed to look up PR #%d: %v", n, err)
			return nil, ErrAPIFailure
		}
		if pr != nil {
			return &resolvedArg{
				branch:   pr.HeadRefName,
				prNumber: pr.Number,
				prURL:    pr.URL,
				pr:       pr,
			}, nil
		}
		// PR doesn't exist — fall through to branch name lookup
	}

	// Treat as branch name: look for an open PR
	pr, err := client.FindPRForBranch(arg)
	if err != nil {
		cfg.Errorf("failed to look up PR for branch %s: %v", arg, err)
		return nil, ErrAPIFailure
	}
	if pr != nil {
		cfg.Printf("Found PR %s for branch %s", cfg.PRLink(pr.Number, pr.URL), arg)
		return &resolvedArg{
			branch:   arg,
			prNumber: pr.Number,
			prURL:    pr.URL,
			pr:       pr,
		}, nil
	}

	return nil, nil // needs PR creation
}

// validatePREligibility checks that all found PRs are eligible to be added
// to a stack. Only open or draft PRs without auto-merge enabled are allowed.
// Merged, closed, queued, and auto-merge-enabled PRs are rejected.
//
// PRs that are already members of targetStack are exempt from these checks:
// they are not being added (they are already present), so re-including them —
// as an additive update requires — must not fail the operation. Reports all
// invalid PRs at once before returning.
func validatePREligibility(cfg *config.Config, found []*resolvedArg, targetStack *github.RemoteStack) error {
	inTargetStack := make(map[int]bool)
	if targetStack != nil {
		for _, n := range targetStack.PRNumbers() {
			inTargetStack[n] = true
		}
	}

	invalid := 0
	for _, r := range found {
		if r == nil || r.pr == nil {
			continue
		}
		// PRs already in the target stack are not being added, so the
		// eligibility checks below do not apply to them.
		if inTargetStack[r.prNumber] {
			continue
		}
		pr := r.pr
		reason := ""
		switch {
		case pr.State == "MERGED":
			reason = "it has been merged"
		case pr.State == "CLOSED":
			reason = "it is closed"
		case pr.IsQueued():
			reason = "it is queued for merge"
		case pr.IsAutoMergeEnabled():
			reason = "it has auto-merge enabled"
		}
		if reason != "" {
			cfg.Errorf("PR %s cannot be added to a stack: %s",
				cfg.PRLink(r.prNumber, r.prURL), reason)
			invalid++
		}
	}
	if invalid > 0 {
		return ErrInvalidArgs
	}
	return nil
}

// listStacksSafe fetches all stacks, handling the 404 "not enabled" case.
func listStacksSafe(cfg *config.Config, client github.ClientOps) ([]github.RemoteStack, error) {
	stacks, err := client.ListStacks()
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			warnStacksUnavailable(cfg)
			return nil, ErrStacksUnavailable
		}
		cfg.Errorf("failed to list stacks: %v", err)
		return nil, ErrAPIFailure
	}
	return stacks, nil
}

// prevalidateStack checks whether adding the known PRs to the matched target
// stack would remove any of the stack's existing PRs. This runs before creating
// new PRs so we can fail early without leaving orphaned PRs. The caller is
// responsible for passing a non-nil matchedStack (the result of
// findMatchingStack); when no stack matches there is nothing to pre-validate.
func prevalidateStack(cfg *config.Config, matchedStack *github.RemoteStack, knownPRNumbers []int) error {
	// Check that we won't be removing PRs from the existing stack.
	// At this point we only have the known PR numbers (existing PRs).
	// New PRs will be created later and added. Since new PRs can't
	// match existing stack PRs (they don't exist yet), we just need
	// to check that all existing stack PRs are in the known set.
	knownSet := make(map[int]bool, len(knownPRNumbers))
	for _, n := range knownPRNumbers {
		knownSet[n] = true
	}

	var dropped []int
	for _, n := range matchedStack.PRNumbers() {
		if !knownSet[n] {
			dropped = append(dropped, n)
		}
	}

	if len(dropped) > 0 {
		cfg.Errorf("Cannot update stack: this would remove %s from the stack",
			formatPRList(dropped))
		cfg.Printf("Current stack: %s", formatPRList(matchedStack.PRNumbers()))
		cfg.Printf("Include all existing PRs in the command to update the stack")
		return ErrInvalidArgs
	}

	return nil
}

// createMissingPRs creates PRs for branches that don't have one yet.
// Returns the fully resolved list with all branches mapped to PRs. bottomBase
// is the base branch for the first PR in the chain; each subsequent PR bases
// off the previous PR's head branch.
func createMissingPRs(cfg *config.Config, client github.ClientOps, opts *linkOptions, args []string, found []*resolvedArg, templateContent, bottomBase string) ([]resolvedArg, error) {
	resolved := make([]resolvedArg, len(args))

	for i, arg := range args {
		if found[i] != nil {
			resolved[i] = *found[i]
			continue
		}

		// Determine the base branch for this PR
		baseBranch := bottomBase
		if i > 0 {
			baseBranch = resolved[i-1].branch
		}

		title := humanize(arg)
		body := generatePRBody("", templateContent)

		newPR, err := client.CreatePR(baseBranch, arg, title, body, !opts.open)
		if err != nil {
			cfg.Errorf("failed to create PR for branch %s: %v", arg, err)
			return nil, ErrAPIFailure
		}

		cfg.Successf("Created PR %s for %s (base: %s)", cfg.PRLink(newPR.Number, newPR.URL), arg, baseBranch)
		resolved[i] = resolvedArg{
			branch:   arg,
			prNumber: newPR.Number,
			prURL:    newPR.URL,
			created:  true,
		}
	}

	return resolved, nil
}

// fixBaseBranches updates the base branch of existing PRs to match the
// expected stack chain. The first PR should have base = bottomBase,
// each subsequent PR should have base = previous PR's head branch.
// Newly created PRs (created=true) are skipped since they already have
// the correct base from creation.
func fixBaseBranches(cfg *config.Config, client github.ClientOps, opts *linkOptions, resolved []resolvedArg, bottomBase string) {
	for i, r := range resolved {
		if r.created {
			continue
		}

		expectedBase := bottomBase
		if i > 0 {
			expectedBase = resolved[i-1].branch
		}

		// Look up the PR to check its current base
		pr, err := client.FindPRByNumber(r.prNumber)
		if err != nil {
			cfg.Warningf("could not verify base branch for PR %s: %v",
				cfg.PRLink(r.prNumber, r.prURL), err)
			continue
		}
		if pr == nil {
			continue
		}

		if pr.BaseRefName != expectedBase {
			if err := client.UpdatePRBase(r.prNumber, expectedBase); err != nil {
				cfg.Warningf("failed to update base branch for PR %s to %s: %s",
					cfg.PRLink(r.prNumber, r.prURL), expectedBase, formatAPIError(err))
			} else {
				cfg.Successf("Updated base branch for PR %s to %s",
					cfg.PRLink(r.prNumber, r.prURL), expectedBase)
			}
		}

		// Convert draft PR to ready for review when --open is set.
		if opts.open && pr.IsDraft {
			if err := client.MarkPRReadyForReview(pr.ID); err != nil {
				cfg.Warningf("failed to mark PR %s as ready for review: %v",
					cfg.PRLink(r.prNumber, r.prURL), err)
			} else {
				cfg.Successf("Marked PR %s as ready for review",
					cfg.PRLink(r.prNumber, r.prURL))
			}
		}
	}
}

// formatAPIError extracts a clean error message from an API error.
// For HTTP errors, returns just the status and message without the raw URL.
func formatAPIError(err error) string {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Message != "" {
			return fmt.Sprintf("HTTP %d: %s", httpErr.StatusCode, httpErr.Message)
		}
		return fmt.Sprintf("HTTP %d", httpErr.StatusCode)
	}
	return err.Error()
}

// upsertStack uses the pre-fetched stacks to create or update as needed.
func upsertStack(cfg *config.Config, client github.ClientOps, stacks []github.RemoteStack, prNumbers []int) error {
	matchedStack, err := findMatchingStack(stacks, prNumbers)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrDisambiguate
	}

	if matchedStack == nil {
		return createLink(cfg, client, prNumbers)
	}

	return updateLink(cfg, client, matchedStack, prNumbers)
}

// findMatchingStack finds a single stack that contains any of the given PR numbers.
// Returns nil if no stack matches. Returns an error if PRs span multiple stacks.
func findMatchingStack(stacks []github.RemoteStack, prNumbers []int) (*github.RemoteStack, error) {
	prSet := make(map[int]bool, len(prNumbers))
	for _, n := range prNumbers {
		prSet[n] = true
	}

	var matched *github.RemoteStack
	for i := range stacks {
		for _, n := range stacks[i].PRNumbers() {
			if prSet[n] {
				if matched != nil && matched.ID != stacks[i].ID {
					return nil, fmt.Errorf("PRs belong to multiple stacks — unstack them first, then re-link")
				}
				matched = &stacks[i]
				break
			}
		}
	}

	return matched, nil
}

// createLink creates a new stack with the given PR numbers.
func createLink(cfg *config.Config, client github.ClientOps, prNumbers []int) error {
	rs, err := client.CreateStack(prNumbers)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 422:
				cfg.Errorf("Cannot create stack: %s", httpErr.Message)
				return ErrAPIFailure
			case 404:
				warnStacksUnavailable(cfg)
				return ErrStacksUnavailable
			default:
				cfg.Errorf("Failed to create stack (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to create stack: %v", err)
		return ErrAPIFailure
	}

	cfg.Successf("Created stack with %d PRs%s", len(prNumbers), stackLabel(rs.Number))
	return nil
}

// updateLink updates an existing stack with the given PR numbers.
// The update is additive-only: it errors if any existing PRs would be removed,
// and (because the add endpoint appends to the top) if the existing PRs are not
// an ordered prefix of the desired list.
func updateLink(cfg *config.Config, client github.ClientOps, existing *github.RemoteStack, prNumbers []int) error {
	current := existing.PRNumbers()

	// Check if the input exactly matches the existing stack.
	if slicesEqual(current, prNumbers) {
		cfg.Successf("Stack with %d PRs is already up to date", len(prNumbers))
		return nil
	}

	// Check that no existing PRs would be removed (additive-only).
	newSet := make(map[int]bool, len(prNumbers))
	for _, n := range prNumbers {
		newSet[n] = true
	}

	var dropped []int
	for _, n := range current {
		if !newSet[n] {
			dropped = append(dropped, n)
		}
	}

	if len(dropped) > 0 {
		cfg.Errorf("Cannot update stack: this would remove %s from the stack",
			formatPRList(dropped))
		cfg.Printf("Current stack: %s", formatPRList(current))
		cfg.Printf("Include all existing PRs in the command to update the stack")
		return ErrInvalidArgs
	}

	// The add endpoint appends to the top of the stack, so the existing PRs
	// must be an ordered prefix of the desired list.
	delta, ok := appendDelta(current, prNumbers)
	if !ok {
		cfg.Errorf("Cannot update stack: new PRs must be added to the top of the existing stack")
		cfg.Printf("Current stack: %s", formatPRList(current))
		return ErrInvalidArgs
	}

	rs, err := client.AddToStack(existing.Number, delta)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				// Stack was deleted between list and update — try creating instead.
				cfg.Warningf("Stack was deleted — creating a new one")
				return createLink(cfg, client, prNumbers)
			case 422:
				cfg.Errorf("Cannot update stack: %s", httpErr.Message)
				return ErrAPIFailure
			default:
				cfg.Errorf("Failed to update stack (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to update stack: %v", err)
		return ErrAPIFailure
	}

	cfg.Successf("Updated stack to %d PRs%s", len(prNumbers), stackLabel(rs.Number))
	return nil
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// appendDelta returns the PR numbers that must be appended to current to reach
// desired, with ok=true, when current is an exact ordered prefix of desired.
// When desired diverges from current (a reorder or removal), ok is false. This
// mirrors the Stacks add endpoint, which only appends to the top of a stack.
func appendDelta(current, desired []int) (delta []int, ok bool) {
	if len(current) > len(desired) {
		return nil, false
	}
	for i, n := range current {
		if desired[i] != n {
			return nil, false
		}
	}
	return desired[len(current):], true
}

func formatPRList(numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	s := fmt.Sprintf("#%d", numbers[0])
	for _, n := range numbers[1:] {
		s += fmt.Sprintf(", #%d", n)
	}
	return s
}
