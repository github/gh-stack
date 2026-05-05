package cmd

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/spf13/cobra"
)

type linkOptions struct {
	base   string
	open   bool
	remote string
}

func LinkCmd(cfg *config.Config) *cobra.Command {
	opts := &linkOptions{}

	cmd := &cobra.Command{
		Use:   "link <branch-or-pr> <branch-or-pr> [<branch-or-pr>...]",
		Short: "Link PRs into a stack on GitHub without local tracking",
		Long: `Create or update a stack on GitHub from branch names or PR numbers.

This command does not rely on gh-stack local tracking state. It is
designed for users who manage branches with external tools (e.g. jj,
Sapling, ghstack, git-town, etc...) and want to use GitHub stacked
PRs without adopting local stack tracking.

Arguments are provided in stack order (bottom to top). Each argument
can be a branch name or a PR number. For numeric arguments, the
command first checks if a PR with that number exists; if not, it
treats the argument as a branch name.

Branch arguments are automatically pushed to the remote before
creating or looking up PRs. For branches that already have open PRs,
those PRs are used. For branches without PRs, new PRs are created
automatically with the correct base branch chaining.

If the PRs are not yet in a stack, a new stack is created. If some of
the PRs are already in a stack, the existing stack is updated to include
the new PRs (existing PRs are never removed).`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
	branch   string // head branch name
	prNumber int    // PR number
	prURL    string // PR URL (for display)
	created  bool   // true if we created this PR (skip base-fix re-fetch)
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

	// Phase 1: Push branch args to the remote so PRs can be found/created
	if err := pushBranchArgs(cfg, opts, args); err != nil {
		return err
	}

	// Phase 2: Find existing PRs for all args (don't create yet)
	cfg.Printf("Looking up PRs for %d %s...", len(args), plural(len(args), "branch", "branches"))
	found, err := findExistingPRs(cfg, client, args)
	if err != nil {
		return err
	}

	// Phase 3: Pre-validate the stack — check that adding these PRs won't
	// conflict with existing stacks before creating any new PRs.
	// Also fetches stacks for reuse in the upsert phase.
	knownPRNumbers := make([]int, 0, len(found))
	for _, r := range found {
		if r != nil {
			knownPRNumbers = append(knownPRNumbers, r.prNumber)
		}
	}
	cfg.Printf("Checking existing stacks...")
	stacks, err := listStacksSafe(cfg, client)
	if err != nil {
		return err
	}
	if len(knownPRNumbers) > 0 {
		if err := prevalidateStack(cfg, stacks, knownPRNumbers); err != nil {
			return err
		}
	}

	// Phase 4: Create PRs for branches that don't have one yet
	needsCreation := 0
	for _, r := range found {
		if r == nil {
			needsCreation++
		}
	}
	if needsCreation > 0 {
		cfg.Printf("Creating %d %s...", needsCreation, plural(needsCreation, "PR", "PRs"))
	}
	resolved, err := createMissingPRs(cfg, client, opts, args, found)
	if err != nil {
		return err
	}

	// Phase 5: Fix base branches for existing PRs with wrong bases
	fixBaseBranches(cfg, client, opts, resolved)

	// Phase 6: Upsert the stack (reuse stacks from phase 3)
	prNumbers := make([]int, len(resolved))
	for i, r := range resolved {
		prNumbers[i] = r.prNumber
	}

	return upsertStack(cfg, client, stacks, prNumbers)
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

	if err := git.FetchBranches(remote, branches); err != nil {
		cfg.Warningf("Failed to fetch branches from %s: %v", remote, err)
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
		}, nil
	}

	return nil, nil // needs PR creation
}

// listStacksSafe fetches all stacks, handling the 404 "not enabled" case.
func listStacksSafe(cfg *config.Config, client github.ClientOps) ([]github.RemoteStack, error) {
	stacks, err := client.ListStacks()
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			cfg.Warningf("Stacked PRs are not enabled for this repository")
			return nil, ErrStacksUnavailable
		}
		cfg.Errorf("failed to list stacks: %v", err)
		return nil, ErrAPIFailure
	}
	return stacks, nil
}

// prevalidateStack checks whether the known PRs would conflict with
// existing stacks. This runs before creating new PRs so we can fail
// early without leaving orphaned PRs.
func prevalidateStack(cfg *config.Config, stacks []github.RemoteStack, knownPRNumbers []int) error {
	matchedStack, err := findMatchingStack(stacks, knownPRNumbers)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrDisambiguate
	}

	if matchedStack != nil {
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
		for _, n := range matchedStack.PullRequests {
			if !knownSet[n] {
				dropped = append(dropped, n)
			}
		}

		if len(dropped) > 0 {
			cfg.Errorf("Cannot update stack: this would remove %s from the stack",
				formatPRList(dropped))
			cfg.Printf("Current stack: %s", formatPRList(matchedStack.PullRequests))
			cfg.Printf("Include all existing PRs in the command to update the stack")
			return ErrInvalidArgs
		}
	}

	return nil
}

// createMissingPRs creates PRs for branches that don't have one yet.
// Returns the fully resolved list with all branches mapped to PRs.
func createMissingPRs(cfg *config.Config, client github.ClientOps, opts *linkOptions, args []string, found []*resolvedArg) ([]resolvedArg, error) {
	resolved := make([]resolvedArg, len(args))

	for i, arg := range args {
		if found[i] != nil {
			resolved[i] = *found[i]
			continue
		}

		// Determine the base branch for this PR
		baseBranch := opts.base
		if i > 0 {
			baseBranch = resolved[i-1].branch
		}

		title := humanize(arg)
		body := generatePRBody("")

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
// expected stack chain. The first PR should have base = opts.base,
// each subsequent PR should have base = previous PR's head branch.
// Newly created PRs (created=true) are skipped since they already have
// the correct base from creation.
func fixBaseBranches(cfg *config.Config, client github.ClientOps, opts *linkOptions, resolved []resolvedArg) {
	for i, r := range resolved {
		if r.created {
			continue
		}

		expectedBase := opts.base
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
		for _, n := range stacks[i].PullRequests {
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
	_, err := client.CreateStack(prNumbers)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 422:
				cfg.Errorf("Cannot create stack: %s", httpErr.Message)
				return ErrAPIFailure
			case 404:
				cfg.Warningf("Stacked PRs are not enabled for this repository")
				return ErrStacksUnavailable
			default:
				cfg.Errorf("Failed to create stack (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to create stack: %v", err)
		return ErrAPIFailure
	}

	cfg.Successf("Created stack with %d PRs", len(prNumbers))
	return nil
}

// updateLink updates an existing stack with the given PR numbers.
// The update is additive-only: it errors if any existing PRs would be removed.
func updateLink(cfg *config.Config, client github.ClientOps, existing *github.RemoteStack, prNumbers []int) error {
	// Check if the input exactly matches the existing stack.
	if slicesEqual(existing.PullRequests, prNumbers) {
		cfg.Successf("Stack with %d PRs is already up to date", len(prNumbers))
		return nil
	}

	// Check that no existing PRs would be removed (additive-only).
	newSet := make(map[int]bool, len(prNumbers))
	for _, n := range prNumbers {
		newSet[n] = true
	}

	var dropped []int
	for _, n := range existing.PullRequests {
		if !newSet[n] {
			dropped = append(dropped, n)
		}
	}

	if len(dropped) > 0 {
		cfg.Errorf("Cannot update stack: this would remove %s from the stack",
			formatPRList(dropped))
		cfg.Printf("Current stack: %s", formatPRList(existing.PullRequests))
		cfg.Printf("Include all existing PRs in the command to update the stack")
		return ErrInvalidArgs
	}

	stackID := strconv.Itoa(existing.ID)
	if err := client.UpdateStack(stackID, prNumbers); err != nil {
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

	cfg.Successf("Updated stack to %d PRs", len(prNumbers))
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
