package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type checkoutOptions struct {
	target string
}

func CheckoutCmd(cfg *config.Config) *cobra.Command {
	opts := &checkoutOptions{}

	cmd := &cobra.Command{
		Use:   "checkout [<pr-number> | <branch>]",
		Short: "Checkout a stack from a PR number or branch name",
		Long: `Check out a stack from a pull request number or branch name.

When a PR number is provided (e.g. 123), the command first checks
local tracking. If the PR is not tracked locally, it queries the
GitHub API to discover the stack, fetches the branches, and sets up
the stack locally. If the stack already exists locally and matches,
it simply switches to the branch.

When a branch name is provided, the command resolves it against
locally tracked stacks only.

When run without arguments, shows a menu of all locally available
stacks to choose from.`,
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
// Non-numeric targets use local resolution only.
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
		// Interactive picker mode
		s, err = interactiveStackPicker(cfg, sf)
		if err != nil {
			if !errors.Is(err, errInterrupt) {
				cfg.Errorf("%s", err)
			}
			return ErrSilent
		}
		if s == nil {
			return nil
		}
		targetBranch = s.Branches[len(s.Branches)-1].Branch
	} else if prNumber, parseErr := strconv.Atoi(opts.target); parseErr == nil && prNumber > 0 {
		// Target is a pure integer — try local PR, then remote API, then branch name
		s, targetBranch, err = resolveNumericTarget(cfg, sf, gitDir, prNumber, opts.target)
		if err != nil {
			return err
		}
	} else {
		// Non-numeric target — resolve against local stacks only
		var br *stack.BranchRef
		s, br, err = resolvePR(sf, opts.target)
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrNotInStack
		}
		targetBranch = br.Branch
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
	return nil
}

// resolveNumericTarget handles the case where the user passes a pure integer.
// It tries, in order:
//  1. Local stack lookup by PR number
//  2. Remote API discovery (ListStacks → find → import)
//  3. Local stack lookup by branch name (for numeric branch names like "123")
func resolveNumericTarget(cfg *config.Config, sf *stack.StackFile, gitDir string, prNumber int, raw string) (*stack.Stack, string, error) {
	// 1. Try local PR number lookup
	if s, br := sf.FindStackByPRNumber(prNumber); s != nil && br != nil {
		return s, br.Branch, nil
	}

	// 2. Try remote API
	s, targetBranch, err := checkoutRemoteStack(cfg, sf, gitDir, prNumber)
	if err == nil {
		return s, targetBranch, nil
	}
	// If the API returned a definitive "not in a stack" or a real error,
	// fall through to the branch-name attempt only for "not in stack".
	// For API failures (404, network errors), still fall through —
	// the user might have a numeric branch name.
	remoteErr := err

	// 3. Fall back to branch name lookup (handles numeric branch names)
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

	// Nothing worked — return the remote error which has the most
	// informative message for a numeric input
	return nil, "", remoteErr
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

	// Step 1: List stacks and find one containing the target PR
	remoteStack, err := findRemoteStackForPR(client, prNumber)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			cfg.Errorf("Stacked PRs are not enabled for this repository")
			return nil, "", ErrAPIFailure
		}
		cfg.Errorf("failed to list stacks: %v", err)
		return nil, "", ErrAPIFailure
	}
	if remoteStack == nil {
		cfg.Errorf("PR #%d is not part of a stack on GitHub", prNumber)
		return nil, "", ErrNotInStack
	}

	// Step 2: Fetch PR details for every PR in the remote stack
	prs, err := fetchStackPRDetails(client, remoteStack.PullRequests)
	if err != nil {
		cfg.Errorf("failed to fetch PR details: %v", err)
		return nil, "", ErrAPIFailure
	}

	// Determine trunk (base branch of the first PR) and the target branch
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

	remoteStackID := strconv.Itoa(remoteStack.ID)

	// Step 3: Check if the target branch is already in a local stack
	localStack := findLocalStackForRemotePRs(sf, prs)

	if localStack != nil {
		// Case A: branch is in a local stack — check composition
		if stackCompositionMatches(localStack, remoteStack.PullRequests) {
			// Composition matches — sync PR state and checkout
			syncRemotePRState(localStack, prs)
			if localStack.ID == "" {
				localStack.ID = remoteStackID
			}
			if err := stack.Save(gitDir, sf); err != nil {
				return nil, "", handleSaveError(cfg, err)
			}
			cfg.Successf("Local stack matches remote — switching to branch")
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

	s, err := importRemoteStack(cfg, sf, gitDir, remote, trunk, prs, remoteStackID)
	if err != nil {
		return nil, "", err
	}

	if err := stack.Save(gitDir, sf); err != nil {
		return nil, "", handleSaveError(cfg, err)
	}

	return s, targetBranch, nil
}

// findRemoteStackForPR queries the list stacks API and returns the stack
// containing the given PR number, or nil if no stack contains it.
func findRemoteStackForPR(client github.ClientOps, prNumber int) (*github.RemoteStack, error) {
	stacks, err := client.ListStacks()
	if err != nil {
		return nil, err
	}
	for i := range stacks {
		for _, n := range stacks[i].PullRequests {
			if n == prNumber {
				return &stacks[i], nil
			}
		}
	}
	return nil, nil
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

		s, importErr := importRemoteStack(cfg, sf, gitDir, remote, trunk, prs, remoteStackID)
		if importErr != nil {
			return nil, importErr
		}
		if err := stack.Save(gitDir, sf); err != nil {
			return nil, handleSaveError(cfg, err)
		}
		cfg.Successf("Local stack replaced with remote version")
		return s, nil

	case 1:
		// Delete remote stack, keep local
		if err := client.DeleteStack(remoteStackID); err != nil {
			var httpErr *api.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
				cfg.Warningf("Remote stack already deleted")
			} else {
				cfg.Errorf("failed to delete remote stack: %v", err)
				return nil, ErrAPIFailure
			}
		} else {
			cfg.Successf("Remote stack deleted")
		}
		localStack.ID = ""
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
) (*stack.Stack, error) {
	// Fetch latest refs from remote
	if err := git.Fetch(remote); err != nil {
		cfg.Warningf("failed to fetch from %s: %v", remote, err)
	}

	// Ensure trunk exists locally
	if !git.BranchExists(trunk) {
		remoteTrunk := remote + "/" + trunk
		if err := git.CreateBranch(trunk, remoteTrunk); err != nil {
			cfg.Warningf("could not pull trunk branch %s: %v", trunk, err)
		}
	}

	// Create local branches for each PR's head branch
	for _, pr := range prs {
		branch := pr.HeadRefName
		if git.BranchExists(branch) {
			continue
		}
		remoteRef := remote + "/" + branch
		if err := git.CreateBranch(branch, remoteRef); err != nil {
			cfg.Errorf("failed to pull branch %s from %s: %v", branch, remoteRef, err)
			return nil, ErrSilent
		}
		_ = git.SetUpstreamTracking(branch, remote)
		cfg.Successf("Pulled branch %s", branch)
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
		ID: remoteStackID,
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

	cfg.Successf("Imported stack with %d branches from GitHub", len(prs))
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

// interactiveStackPicker shows a menu of all locally tracked stacks and returns
// the one the user selects. Returns nil, nil if the user has no stacks.
func interactiveStackPicker(cfg *config.Config, sf *stack.StackFile) (*stack.Stack, error) {
	if !cfg.IsInteractive() {
		return nil, fmt.Errorf("no target specified; provide a branch name or PR number, or run interactively to select a stack")
	}

	if len(sf.Stacks) == 0 {
		cfg.Infof("No locally tracked stacks found")
		cfg.Printf("Create a stack with `%s` or check out a remote stack with `%s`",
			cfg.ColorCyan("gh stack init"),
			cfg.ColorCyan("gh stack checkout 123"))
		return nil, nil
	}

	options := make([]string, len(sf.Stacks))
	for i := range sf.Stacks {
		options[i] = sf.Stacks[i].DisplayChain()
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selected, err := p.Select(
		"Select a stack to check out (showing locally tracked stacks only)",
		"",
		options,
	)
	if err != nil {
		if isInterruptError(err) {
			printInterrupt(cfg)
			return nil, errInterrupt
		}
		return nil, fmt.Errorf("stack selection: %w", err)
	}

	return &sf.Stacks[selected], nil
}
