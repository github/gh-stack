package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/checkoutview"
	"github.com/spf13/cobra"
)

type checkoutOptions struct {
	target string
}

func CheckoutCmd(cfg *config.Config) *cobra.Command {
	opts := &checkoutOptions{}

	cmd := &cobra.Command{
		Use:   "checkout [<stack-number> | <pr-number> | <pr-url> | <branch>]",
		Short: "Checkout a stack by stack number, PR number, PR URL, or branch name",
		Long: `Check out a stack by stack number, pull request number, PR URL, or branch name.

A bare number is interpreted first as a stack number (the identifier shown in
the GitHub stack UI). If no stack has that number, it is then tried as a
locally tracked PR number, then a PR number whose stack is discovered from
GitHub, and finally a branch name.

When a PR number or PR URL is provided (e.g. 123 or
https://github.com/owner/repo/pull/123), the command first checks
local tracking. If the PR is not tracked locally, it queries the
GitHub API to discover the stack, fetches the branches, and sets up
the stack locally. If the stack already exists locally and matches,
it simply switches to the branch.

When a branch name is provided, the command first checks locally tracked
stacks. If the branch is not tracked locally, it looks for the branch on
remote stacks and pulls down the matching stack.

When run without arguments, first checks whether the current branch belongs
to a stack on remote that is not tracked locally, and offers to check
it out. Otherwise, it opens an interactive picker listing every stack available
to you — both the stacks tracked locally and the stacks that exist only on
GitHub — so you can search, filter, and check one out. Fully merged stacks are
omitted.`,
		Example: `  # Check out a stack by its stack number
  $ gh stack checkout 7

  # Check out a stack by PR number
  $ gh stack checkout 42

  # Check out a stack by PR URL
  $ gh stack checkout https://github.com/owner/repo/pull/42

  # Check out a stack by branch name
  $ gh stack checkout feat/api-routes

  # Open the interactive picker of all available stacks (local and remote)
  $ gh stack checkout`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.target = args[0]
			}
			return runCheckout(cfg, opts)
		},
	}

	return cmd
}

// runCheckout resolves a stack and checks out the target branch.
// For numeric targets, it tries local lookup first, then falls back to
// the GitHub API to discover remote stacks, then tries as a branch name.
// Branch names resolve locally first and then against stacks on GitHub.
func runCheckout(cfg *config.Config, opts *checkoutOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	var s *stack.Stack
	var targetBranch string

	if opts.target == "" {
		// Interactive picker mode (local + remote stacks).
		s, targetBranch, err = interactiveCheckout(cfg, sf, gitDir)
		if err != nil {
			var exitErr *ExitError
			if errors.As(err, &exitErr) {
				// The callee already printed a message and chose an exit code.
				return err
			}
			cfg.Errorf("%s", err)
			return ErrSilent
		}
		if s == nil {
			// No stacks available, or the user cancelled.
			return nil
		}
	} else if prNumber, ok := parsePRURL(opts.target); ok {
		// Target is a PR URL — extract number and resolve like a numeric target
		s, targetBranch, err = resolveNumericTarget(cfg, sf, gitDir, prNumber, opts.target)
		if err != nil {
			return err
		}
	} else if prNumber, parseErr := strconv.Atoi(opts.target); parseErr == nil && prNumber > 0 {
		// Target is a pure integer — try stack number, then PR, then branch name
		s, targetBranch, err = resolveNumericTarget(cfg, sf, gitDir, prNumber, opts.target)
		if err != nil {
			return err
		}
	} else {
		// Non-numeric target — resolve locally before checking GitHub.
		var br *stack.BranchRef
		s, br, err = resolvePR(cfg, sf, opts.target)
		if err == nil {
			targetBranch = br.Branch
		} else {
			s, targetBranch, err = checkoutRemoteStackByBranch(cfg, sf, gitDir, opts.target)
			if errors.Is(err, errRemoteBranchNotFound) {
				cfg.Errorf("no local or remote stack found for %q", opts.target)
				cfg.Printf("Try a stack or PR number with `%s`",
					cfg.ColorCyan("gh stack checkout <number>"))
				return ErrNotInStack
			}
			if err != nil {
				return err
			}
		}
	}

	currentBranch, _ := git.CurrentBranch()
	if targetBranch == currentBranch {
		cfg.Infof("Already on %s", targetBranch)
		cfg.Printf("Stack: %s", s.DisplayChain())
		return nil
	}

	if err := git.CheckoutBranch(targetBranch); err != nil {
		cfg.Errorf("failed to checkout %s: %v", targetBranch, err)
		return ErrSilent
	}

	cfg.Successf("Switched to %s", targetBranch)
	cfg.Printf("Stack: %s", s.DisplayChain())
	cfg.Printf("Run `%s` to see the full stack",
		cfg.ColorCyan("gh stack view"))
	return nil
}

// resolveNumericTarget handles the case where the user passes a pure integer or
// a PR URL. The number is interpreted as, in order:
//  1. A stack number (the primary identifier)
//  2. A locally tracked PR number
//  3. A PR number whose stack is discovered from GitHub
//  4. A branch name (for numeric branch names like "123")
//
// Stack, PR, and issue numbers share a single repo-scoped numberspace,
// so a given number is only ever one object type; a number that is not a stack
// simply misses at step 1 and resolves at a later step.
func resolveNumericTarget(cfg *config.Config, sf *stack.StackFile, gitDir string, number int, raw string) (*stack.Stack, string, error) {
	// 1. Try as a stack number (the primary identifier).
	if s, targetBranch, err := checkoutStackByNumber(cfg, sf, gitDir, number); err == nil {
		return s, targetBranch, nil
	} else if !errors.Is(err, errStackNumberNotFound) {
		// A real error during import/reconcile (composition conflict, interrupted
		// import, etc.) — surface it rather than trying other interpretations.
		return nil, "", err
	}

	// 2. Try a locally tracked PR number.
	if s, br := sf.FindStackByPRNumber(number); s != nil && br != nil {
		return s, br.Branch, nil
	}

	// 3. Try a PR number whose stack is on GitHub.
	s, targetBranch, err := checkoutRemoteStack(cfg, sf, gitDir, number)
	if err == nil {
		return s, targetBranch, nil
	}
	// For API failures or "not in a stack", still fall through to the branch-name
	// attempt — the user might have a numeric branch name.
	remoteErr := err

	// 4. Fall back to local branch name lookup (handles numeric branch names).
	stacks := sf.FindAllStacksForBranch(raw)
	if len(stacks) > 0 {
		s := stacks[0]
		idx := s.IndexOf(raw)
		if idx >= 0 {
			return s, s.Branches[idx].Branch, nil
		}
		// Matched as trunk
		if len(s.Branches) > 0 {
			return s, s.Branches[0].Branch, nil
		}
	}

	// Only a definitive "not in a stack" result can be reinterpreted as a
	// remote branch name. Preserve API, conflict, and other actionable errors.
	if !errors.Is(remoteErr, ErrNotInStack) {
		return nil, "", remoteErr
	}

	// Finally, try the numeric input as a branch in a stack on GitHub.
	s, targetBranch, err = checkoutRemoteStackByBranch(cfg, sf, gitDir, raw)
	if err == nil {
		return s, targetBranch, nil
	}
	if !errors.Is(err, errRemoteBranchNotFound) {
		return nil, "", err
	}

	// Nothing worked — return the remote error which has the most
	// informative message for a numeric input
	cfg.Errorf("PR #%d is not part of a stack on GitHub", number)
	return nil, "", remoteErr
}

var errRemoteBranchNotFound = errors.New("remote branch not found in a stack")

// checkoutRemoteStackByBranch finds the unique active stack on GitHub that
// contains branch, then imports it through the existing PR checkout path.
func checkoutRemoteStackByBranch(cfg *config.Config, sf *stack.StackFile, gitDir, branch string) (*stack.Stack, string, error) {
	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return nil, "", ErrAPIFailure
	}

	remoteStacks, err := client.ListStacks()
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			warnStacksUnavailable(cfg)
			return nil, "", ErrStacksUnavailable
		}
		cfg.Errorf("failed to list stacks: %v", err)
		return nil, "", ErrAPIFailure
	}

	matches := matchingRemoteStacksForBranch(remoteStacks, branch)
	if len(matches) == 0 {
		return nil, "", errRemoteBranchNotFound
	}
	if len(matches) > 1 {
		stackNumbers := make([]string, len(matches))
		for i, match := range matches {
			stackNumbers[i] = strconv.Itoa(match.Number)
		}
		cfg.Errorf("branch %q belongs to multiple stacks on GitHub (%s)",
			branch, strings.Join(stackNumbers, ", "))
		cfg.Printf("Use `%s` with a stack or PR number to choose one",
			cfg.ColorCyan("gh stack checkout <number>"))
		return nil, "", ErrDisambiguate
	}

	for _, pr := range matches[0].PRDetails {
		if pr.Head.Ref == branch && pr.Number > 0 {
			s, targetBranch, err := checkoutRemoteStack(cfg, sf, gitDir, pr.Number)
			if errors.Is(err, ErrNotInStack) {
				cfg.Errorf("PR #%d is not part of a stack on GitHub", pr.Number)
			}
			return s, targetBranch, err
		}
	}
	return nil, "", errRemoteBranchNotFound
}

// checkoutRemoteStack discovers a stack from GitHub for the given PR number,
// reconciles it with any local state, and returns the resolved stack and
// target branch name. The stack file is saved before returning.
func checkoutRemoteStack(cfg *config.Config, sf *stack.StackFile, gitDir string, prNumber int) (*stack.Stack, string, error) {
	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return nil, "", ErrAPIFailure
	}

	// Step 1: Find the stack containing the target PR via the list endpoint's
	// server-side pull_request filter.
	remoteStack, err := client.FindStackForPR(prNumber)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			warnStacksUnavailable(cfg)
			return nil, "", ErrStacksUnavailable
		}
		cfg.Errorf("failed to list stacks: %v", err)
		return nil, "", ErrAPIFailure
	}
	if remoteStack == nil {
		return nil, "", ErrNotInStack
	}

	// Step 2: Fetch PR details for every PR in the remote stack
	prs, err := fetchStackPRDetails(client, remoteStack.PRNumbers())
	if err != nil {
		cfg.Errorf("failed to fetch PR details: %v", err)
		return nil, "", ErrAPIFailure
	}

	// Determine trunk (base branch of the first PR) and the target branch (the
	// branch for the requested PR).
	trunk := prs[0].BaseRefName
	var targetBranch string
	for _, pr := range prs {
		if pr.Number == prNumber {
			targetBranch = pr.HeadRefName
			break
		}
	}
	if targetBranch == "" {
		cfg.Errorf("could not determine branch for PR #%d", prNumber)
		return nil, "", ErrAPIFailure
	}

	return reconcileAndImportRemoteStack(cfg, client, sf, gitDir, remoteStack, prs, trunk, targetBranch)
}

// errStackNumberNotFound is returned by checkoutStackByNumber when a numeric
// argument does not resolve to a stack (no such stack, stacks unavailable, or
// any lookup failure), signalling the caller to try interpreting the argument
// as a PR number or branch name instead.
var errStackNumberNotFound = errors.New("stack number not found")

// checkoutStackByNumber discovers a stack from GitHub by its stack number,
// reconciles it with any local state, and checks out the top-most unmerged
// branch. It returns errStackNumberNotFound when the number does not resolve to
// a stack so the caller can fall back to other interpretations. Because stack,
// PR, and issue numbers share one repo-scoped numberspace, a number that
// belongs to a PR (or nothing) simply misses here and is resolved by the
// caller's later steps.
func checkoutStackByNumber(cfg *config.Config, sf *stack.StackFile, gitDir string, stackNumber int) (*stack.Stack, string, error) {
	client, err := cfg.GitHubClient()
	if err != nil {
		return nil, "", errStackNumberNotFound
	}

	remoteStack, err := client.GetStack(stackNumber)
	if err != nil || remoteStack == nil || len(remoteStack.PullRequests) == 0 {
		// No such stack, stacks unavailable, or a transient failure — let the
		// caller try the number as a PR number or branch name.
		return nil, "", errStackNumberNotFound
	}

	prs, err := fetchStackPRDetails(client, remoteStack.PRNumbers())
	if err != nil {
		cfg.Errorf("failed to fetch PR details: %v", err)
		return nil, "", ErrAPIFailure
	}

	trunk := prs[0].BaseRefName
	// Target the top-most unmerged branch, falling back to the very top.
	targetBranch := prs[len(prs)-1].HeadRefName
	for i := len(prs) - 1; i >= 0; i-- {
		if !prs[i].Merged {
			targetBranch = prs[i].HeadRefName
			break
		}
	}

	return reconcileAndImportRemoteStack(cfg, client, sf, gitDir, remoteStack, prs, trunk, targetBranch)
}

// reconcileAndImportRemoteStack reconciles a resolved remote stack with local
// state — adopting a matching local stack, resolving composition conflicts, or
// importing the stack from the remote — and returns the resolved local stack
// and the branch to check out.
func reconcileAndImportRemoteStack(cfg *config.Config, client github.ClientOps, sf *stack.StackFile, gitDir string, remoteStack *github.RemoteStack, prs []*github.PullRequest, trunk, targetBranch string) (*stack.Stack, string, error) {
	allMerged := true
	for _, pr := range prs {
		if !pr.Merged {
			allMerged = false
			break
		}
	}
	if allMerged {
		cfg.Infof("All PRs in this stack have been merged")
		cfg.Printf("To start a new stack, use `%s`", cfg.ColorCyan("gh stack init"))
		return nil, "", ErrSilent
	}

	remoteStackID := strconv.Itoa(remoteStack.ID)

	// Check if the target branch is already in a local stack.
	localStack := findLocalStackForRemotePRs(sf, prs)

	if localStack != nil {
		// Sync remote PR metadata before comparing composition so locally
		// tracked stacks with incomplete PR refs don't appear to conflict.
		syncRemotePRState(localStack, prs)

		// Case A: branch is in a local stack — check composition
		if stackCompositionMatches(localStack, remoteStack.PRNumbers()) {
			// Composition matches — checkout
			// remoteStack is authoritative for both identifiers here, so
			// refresh them together. Updating only one (e.g. the number while
			// keeping a stale ID) breaks later ID-based discovery when the old
			// remote stack was replaced by a new one holding the same PRs.
			localStack.ID = remoteStackID
			localStack.Number = remoteStack.Number
			if err := stack.Save(gitDir, sf); err != nil {
				return nil, "", handleSaveError(cfg, err)
			}
			cfg.Successf("Local stack matches remote — switching to branch%s", stackLabel(remoteStack.Number))
			return localStack, targetBranch, nil
		}

		// Composition mismatch — prompt for resolution
		resolved, resolveErr := handleCompositionConflict(cfg, client, sf, localStack, remoteStack, prs, gitDir, trunk)
		if resolveErr != nil {
			return nil, "", resolveErr
		}
		return resolved, targetBranch, nil
	}

	// Case B/C: no matching local stack — import from remote
	remote, err := pickRemote(cfg, trunk, "")
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return nil, "", ErrSilent
	}

	s, err := importRemoteStack(cfg, sf, gitDir, remote, trunk, prs, remoteStackID, remoteStack.Number)
	if err != nil {
		return nil, "", err
	}

	if err := stack.Save(gitDir, sf); err != nil {
		return nil, "", handleSaveError(cfg, err)
	}

	return s, targetBranch, nil
}

// fetchStackPRDetails fetches PR details for each number in the stack.
// Returns PRs in the same order as the input numbers.
func fetchStackPRDetails(client github.ClientOps, prNumbers []int) ([]*github.PullRequest, error) {
	prs := make([]*github.PullRequest, 0, len(prNumbers))
	for _, n := range prNumbers {
		pr, err := client.FindPRByNumber(n)
		if err != nil {
			return nil, fmt.Errorf("fetching PR #%d: %w", n, err)
		}
		if pr == nil {
			return nil, fmt.Errorf("PR #%d not found", n)
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// findLocalStackForRemotePRs checks if any PR's branch is already tracked
// in a local stack and returns that stack (first match).
func findLocalStackForRemotePRs(sf *stack.StackFile, prs []*github.PullRequest) *stack.Stack {
	for _, pr := range prs {
		stacks := sf.FindAllStacksForBranch(pr.HeadRefName)
		for _, s := range stacks {
			if s.IndexOf(pr.HeadRefName) >= 0 {
				return s
			}
		}
	}
	return nil
}

// stackCompositionMatches checks if a local stack's PR numbers match
// the remote stack's PR numbers in the same order.
func stackCompositionMatches(localStack *stack.Stack, remotePRNumbers []int) bool {
	var localPRNumbers []int
	for _, b := range localStack.Branches {
		if b.PullRequest != nil {
			localPRNumbers = append(localPRNumbers, b.PullRequest.Number)
		}
	}
	if len(localPRNumbers) != len(remotePRNumbers) {
		return false
	}
	for i := range localPRNumbers {
		if localPRNumbers[i] != remotePRNumbers[i] {
			return false
		}
	}
	return true
}

// handleCompositionConflict prompts the user to resolve a mismatch between
// local and remote stack composition. Returns the resolved stack.
func handleCompositionConflict(
	cfg *config.Config,
	client github.ClientOps,
	sf *stack.StackFile,
	localStack *stack.Stack,
	remoteStack *github.RemoteStack,
	prs []*github.PullRequest,
	gitDir string,
	trunk string,
) (*stack.Stack, error) {
	if !cfg.IsInteractive() {
		cfg.Errorf("local stack composition differs from remote")
		cfg.Printf("  Local:  %s", localStack.DisplayChain())
		remoteBranches := make([]string, len(prs))
		for i, pr := range prs {
			remoteBranches[i] = pr.HeadRefName
		}
		cfg.Printf("  Remote: (%s) <- %s", trunk, strings.Join(remoteBranches, " <- "))
		cfg.Printf("  Unstack on remote or use `%s` to unstack locally",
			cfg.ColorCyan("gh stack unstack --local"))
		return nil, ErrConflict
	}

	cfg.Warningf("Local stack differs from remote stack")
	cfg.Printf("  Local:  %s", localStack.DisplayChain())
	remoteBranches := make([]string, len(prs))
	for i, pr := range prs {
		remoteBranches[i] = pr.HeadRefName
	}
	cfg.Printf("  Remote: (%s) <- %s", trunk, strings.Join(remoteBranches, " <- "))

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	options := []string{
		"Replace local stack with remote version",
		"Delete remote stack and keep local version",
		"Cancel",
	}
	selected, err := p.Select("How would you like to resolve this?", "", options)
	if err != nil {
		if isInterruptError(err) {
			clearSelectPrompt(cfg, len(options))
			printInterrupt(cfg)
			return nil, errInterrupt
		}
		return nil, ErrSilent
	}

	remoteStackID := strconv.Itoa(remoteStack.ID)

	switch selected {
	case 0:
		// Replace local with remote
		removeLocalStack(sf, localStack)

		remote, remoteErr := pickRemote(cfg, trunk, "")
		if remoteErr != nil {
			if !errors.Is(remoteErr, errInterrupt) {
				cfg.Errorf("%s", remoteErr)
			}
			return nil, ErrSilent
		}

		s, importErr := importRemoteStack(cfg, sf, gitDir, remote, trunk, prs, remoteStackID, remoteStack.Number)
		if importErr != nil {
			return nil, importErr
		}
		if err := stack.Save(gitDir, sf); err != nil {
			return nil, handleSaveError(cfg, err)
		}
		cfg.Successf("Local stack replaced with remote version")
		return s, nil

	case 1:
		// Unstack the remote stack, keep local
		_, dissolved, err := client.Unstack(remoteStack.Number)
		if err != nil {
			var httpErr *api.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
				cfg.Warningf("Remote stack already removed")
			} else if errors.As(err, &httpErr) && httpErr.StatusCode == 422 {
				cfg.Errorf("Cannot unstack remote stack: %s", httpErr.Message)
				return nil, ErrAPIFailure
			} else {
				cfg.Errorf("failed to unstack remote stack: %v", err)
				return nil, ErrAPIFailure
			}
		} else if dissolved {
			cfg.Successf("Remote stack removed")
		} else {
			cfg.Warningf("Some pull requests could not be unstacked and remain on GitHub")
		}
		localStack.ID = ""
		localStack.Number = 0
		if err := stack.Save(gitDir, sf); err != nil {
			return nil, handleSaveError(cfg, err)
		}
		return localStack, nil

	default:
		// Cancel
		cfg.Infof("Checkout cancelled")
		return nil, ErrSilent
	}
}

// removeLocalStack removes a stack from the stack file by pointer identity.
func removeLocalStack(sf *stack.StackFile, target *stack.Stack) {
	for i := range sf.Stacks {
		if &sf.Stacks[i] == target {
			sf.RemoveStack(i)
			return
		}
	}
}

// importRemoteStack fetches branches from the remote, creates any that are
// missing locally, builds a Stack from the PR data, and adds it to the
// StackFile. Returns the newly created stack.
func importRemoteStack(
	cfg *config.Config,
	sf *stack.StackFile,
	gitDir string,
	remote string,
	trunk string,
	prs []*github.PullRequest,
	remoteStackID string,
	remoteStackNumber int,
) (*stack.Stack, error) {
	// Fetch latest refs from remote
	if err := git.Fetch(remote); err != nil {
		cfg.Warningf("failed to fetch from %s: %v", remote, err)
	}

	// Ensure trunk exists locally
	if err := ensureLocalTrunk(cfg, trunk, remote); err != nil {
		cfg.Errorf("%s", err)
		return nil, ErrSilent
	}

	// Create local branches for each PR's head branch.
	// Skip merged PRs whose branches were deleted from the remote —
	// these no longer exist upstream and can't be created locally.
	for _, pr := range prs {
		if _, err := ensureLocalBranchFromRemote(cfg, remote, pr); err != nil {
			return nil, err
		}
	}

	// Build the stack
	branchRefs := make([]stack.BranchRef, len(prs))
	for i, pr := range prs {
		branchRefs[i] = stack.BranchRef{
			Branch: pr.HeadRefName,
			PullRequest: &stack.PullRequestRef{
				Number: pr.Number,
				ID:     pr.ID,
				URL:    pr.URL,
				Merged: pr.Merged,
			},
		}
	}

	trunkSHA, _ := git.RevParse(trunk)
	newStack := stack.Stack{
		ID:     remoteStackID,
		Number: remoteStackNumber,
		Trunk: stack.BranchRef{
			Branch: trunk,
			Head:   trunkSHA,
		},
		Branches: branchRefs,
	}

	sf.AddStack(newStack)
	s := &sf.Stacks[len(sf.Stacks)-1]

	// Update base SHAs from actual local refs
	updateBaseSHAs(s)

	cfg.Successf("Imported stack with %d branches from GitHub%s", len(prs), stackLabel(remoteStackNumber))
	return s, nil
}

// syncRemotePRState updates a local stack's PR metadata from fetched PR data.
func syncRemotePRState(s *stack.Stack, prs []*github.PullRequest) {
	prMap := make(map[string]*github.PullRequest, len(prs))
	for _, pr := range prs {
		prMap[pr.HeadRefName] = pr
	}
	for i := range s.Branches {
		pr, ok := prMap[s.Branches[i].Branch]
		if !ok {
			continue
		}
		s.Branches[i].PullRequest = &stack.PullRequestRef{
			Number: pr.Number,
			ID:     pr.ID,
			URL:    pr.URL,
			Merged: pr.Merged,
		}
		s.Branches[i].Queued = pr.IsQueued()
	}
}

// interactiveCheckout opens the interactive stack picker, which lists every
// stack available to the user (locally tracked and remote-only, reconciled and
// with fully-merged stacks filtered out), and resolves the user's choice to a
// stack and the branch to check out. It returns (nil, "", nil) when the user
// cancels or has no stacks. Remote-only selections are cloned down through the
// same path as `gh stack checkout <number>`.
func interactiveCheckout(cfg *config.Config, sf *stack.StackFile, gitDir string) (*stack.Stack, string, error) {
	if !cfg.IsInteractive() {
		return nil, "", fmt.Errorf("no target specified; provide a branch name or PR number, or run interactively to select a stack")
	}

	rows, remoteStacks := gatherCheckoutRows(cfg, sf)
	if currentBranch, branchErr := git.CurrentBranch(); branchErr == nil {
		stackNumber, confirmed, confirmErr := offerRemoteStackForBranch(cfg, sf, remoteStacks, currentBranch)
		if confirmErr != nil {
			// The confirmation helper only returns an error for an explicit
			// interrupt and has already printed the friendly message.
			return nil, "", ErrSilent
		}
		if confirmed {
			return resolveCheckoutSelection(cfg, sf, gitDir, checkoutview.StackRow{
				Number: stackNumber,
				Type:   checkoutview.TypeRemote,
			})
		}
	}

	if len(rows) == 0 {
		cfg.Infof("No stacks available to check out")
		cfg.Printf("Create a stack with `%s` or check out a stack by number with `%s`",
			cfg.ColorCyan("gh stack init"),
			cfg.ColorCyan("gh stack checkout <number>"))
		return nil, "", nil
	}

	selected, ok, err := launchCheckoutPicker(rows)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		// The user dismissed the picker without selecting.
		return nil, "", nil
	}

	return resolveCheckoutSelection(cfg, sf, gitDir, selected)
}

// gatherCheckoutRows fetches the remote stacks (best-effort) and reconciles them
// with the local stacks into the picker's rows. Any GitHub failure (stacks not
// enabled for the repo, no auth, network error) gracefully degrades to a
// local-only list.
func gatherCheckoutRows(cfg *config.Config, sf *stack.StackFile) ([]checkoutview.StackRow, []github.RemoteStack) {
	var remote []github.RemoteStack
	if client, err := cfg.GitHubClient(); err == nil {
		if stacks, err := client.ListStacks(); err == nil {
			remote = stacks
		}
	}
	return checkoutview.BuildRows(sf.Stacks, remote), remote
}

// offerRemoteStackForBranch asks to check out the unique active remote stack
// containing branch when the branch is not already associated with a local
// stack. Every outcome except confirmation or Ctrl+C falls through to the
// existing picker.
func offerRemoteStackForBranch(cfg *config.Config, sf *stack.StackFile, remote []github.RemoteStack, branch string) (int, bool, error) {
	if branch == "" || len(sf.FindAllStacksForBranch(branch)) > 0 {
		return 0, false, nil
	}

	matches := matchingRemoteStacksForBranch(remote, branch)
	if len(matches) != 1 {
		return 0, false, nil
	}

	stackNumber := matches[0].Number
	prompt := fmt.Sprintf("Found stack #%d that includes branch %q. Check out stack #%d?", stackNumber, branch, stackNumber)
	confirmed, err := confirmRemoteStackCheckout(cfg, prompt)
	if err != nil {
		if errors.Is(err, errInterrupt) {
			return 0, false, err
		}
		return 0, false, nil
	}
	if !confirmed {
		return 0, false, nil
	}
	return stackNumber, true, nil
}

// matchingRemoteStacksForBranch returns picker-eligible remote stacks that
// contain branch exactly once per stack. Empty and fully merged stacks are not
// actionable and are omitted, matching the picker.
func matchingRemoteStacksForBranch(remote []github.RemoteStack, branch string) []*github.RemoteStack {
	var matches []*github.RemoteStack
	for i := range remote {
		rs := &remote[i]
		if rs.Number <= 0 || len(rs.PRDetails) == 0 {
			continue
		}

		containsBranch := false
		hasUnmergedPR := false
		for _, pr := range rs.PRDetails {
			if pr.Head.Ref == branch {
				containsBranch = true
			}
			if !pr.IsMerged() {
				hasUnmergedPR = true
			}
		}
		if containsBranch && hasUnmergedPR {
			matches = append(matches, rs)
		}
	}
	return matches
}

func confirmRemoteStackCheckout(cfg *config.Config, prompt string) (bool, error) {
	var (
		confirmed bool
		err       error
	)
	if cfg.ConfirmFn != nil {
		confirmed, err = cfg.ConfirmFn(prompt, true)
	} else {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		confirmed, err = p.Confirm(prompt, true)
	}
	if isInterruptError(err) {
		printInterrupt(cfg)
		return false, errInterrupt
	}
	return confirmed, err
}

// resolveCheckoutSelection resolves a picker selection to a local stack and the
// branch to check out. Locally available stacks are used directly; remote-only
// stacks are cloned down by stack number through the same import/reconcile flow
// as `gh stack checkout <number>`.
func resolveCheckoutSelection(cfg *config.Config, sf *stack.StackFile, gitDir string, selected checkoutview.StackRow) (*stack.Stack, string, error) {
	if selected.Type == checkoutview.TypeLocal && selected.LocalStack != nil {
		return selected.LocalStack, topUnmergedBranch(selected.LocalStack), nil
	}

	s, targetBranch, err := checkoutStackByNumber(cfg, sf, gitDir, selected.Number)
	if err != nil {
		if errors.Is(err, errStackNumberNotFound) {
			cfg.Errorf("stack #%d could not be loaded from GitHub", selected.Number)
			return nil, "", ErrAPIFailure
		}
		return nil, "", err
	}
	return s, targetBranch, nil
}

// launchCheckoutPicker runs the Bubble Tea stack picker and returns the selected
// row (and whether one was chosen). It renders inline (no alt-screen) so the
// picker occupies only a few lines and leaves the surrounding terminal output
// intact. Mouse motion is intentionally not enabled so the search field never
// receives stray mouse bytes.
func launchCheckoutPicker(rows []checkoutview.StackRow) (checkoutview.StackRow, bool, error) {
	p := tea.NewProgram(checkoutview.New(rows))
	finalModel, err := p.Run()
	if err != nil {
		return checkoutview.StackRow{}, false, fmt.Errorf("running stack picker: %w", err)
	}
	m, ok := finalModel.(checkoutview.Model)
	if !ok {
		return checkoutview.StackRow{}, false, nil
	}
	row, selected := m.Result()
	return row, selected, nil
}

// topUnmergedBranch returns the top-most branch of a local stack that has not
// been merged, falling back to the very top branch when every branch is merged.
func topUnmergedBranch(s *stack.Stack) string {
	if len(s.Branches) == 0 {
		return ""
	}
	for i := len(s.Branches) - 1; i >= 0; i-- {
		if !s.Branches[i].IsMerged() {
			return s.Branches[i].Branch
		}
	}
	return s.Branches[len(s.Branches)-1].Branch
}
