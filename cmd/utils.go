package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/mgutz/ansi"
)

// ErrSilent indicates the error has already been printed to the user.
// Execute() will exit with code 1 but will not print the error again.
var ErrSilent = &ExitError{Code: 1}

// Typed exit errors for programmatic detection by scripts and agents.
var (
	ErrNotInStack        = &ExitError{Code: 2}  // branch/stack not found
	ErrConflict          = &ExitError{Code: 3}  // rebase conflict
	ErrAPIFailure        = &ExitError{Code: 4}  // GitHub API error
	ErrInvalidArgs       = &ExitError{Code: 5}  // invalid arguments or flags
	ErrDisambiguate      = &ExitError{Code: 6}  // multiple stacks/remotes, can't auto-select
	ErrRebaseActive      = &ExitError{Code: 7}  // rebase already in progress
	ErrLockFailed        = &ExitError{Code: 8}  // could not acquire stack file lock
	ErrStacksUnavailable = &ExitError{Code: 9}  // stacked PRs not available for this repository
	ErrModifyRecovery    = &ExitError{Code: 10} // modify session interrupted, recovery required
)

// ExitError is returned by commands to indicate a specific exit code.
// Execute() extracts the code and passes it to os.Exit.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) Is(target error) bool {
	t, ok := target.(*ExitError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// errInterrupt is a sentinel returned when a prompt is cancelled via Ctrl+C.
// Callers should exit silently (the friendly message is already printed).
var errInterrupt = errors.New("interrupt")

// isInterruptError reports whether err is (or wraps) the survey interrupt,
// which is raised when the user presses Ctrl+C during a prompt.
func isInterruptError(err error) bool {
	return errors.Is(err, terminal.InterruptErr)
}

// printInterrupt prints a friendly message and should be called exactly once
// per interrupted operation.  The leading newline ensures the message starts
// on its own line even if the cursor was mid-prompt.
func printInterrupt(cfg *config.Config) {
	fmt.Fprintln(cfg.Err)
	cfg.Infof("Received interrupt, aborting operation")
}

// warnStacksUnavailableOrPAT prints an appropriate warning when a stacks API
// call returns 404. If the token is a PAT the message focuses on the auth
// issue; otherwise it falls back to the generic "not enabled" message.
func warnStacksUnavailableOrPAT(cfg *config.Config) {
	if cfg.WarnIfPAT() {
		return
	}
	cfg.Warningf("Stacked PRs are not enabled for this repository")
}

// inputWithPrefill prompts the user for text input with the given prefill
// already editable in the input field. Unlike survey.Input's Default (which
// shows in parentheses), this places the prefill text directly in the
// editable line so the user can append to or modify it. The user's input
// is rendered in cyan for visual distinction from the prompt message.
func inputWithPrefill(cfg *config.Config, prompt, prefill string) (string, error) {
	if cfg.InputFn != nil {
		return cfg.InputFn(prompt, prefill)
	}

	stdio := terminal.Stdio{In: cfg.In, Out: cfg.Out, Err: cfg.Err}
	rr := terminal.NewRuneReader(stdio)
	if err := rr.SetTermMode(); err != nil {
		return "", fmt.Errorf("failed to set terminal mode: %w", err)
	}
	defer func() { _ = rr.RestoreTermMode() }()

	// Render the prompt in survey style: green bold "?" + message
	icon := "?"
	useColor := cfg.Terminal.IsColorEnabled()
	if useColor {
		icon = ansi.Color("?", "green+hb")
	}
	fmt.Fprintf(cfg.Out, "%s %s ", icon, prompt)

	// Set cyan color for the user's input text
	if useColor {
		fmt.Fprint(cfg.Out, ansi.ColorCode("cyan"))
	}

	line, err := rr.ReadLineWithDefault(0, []rune(prefill))

	// Reset color after input
	if useColor {
		fmt.Fprint(cfg.Out, ansi.ColorCode("reset"))
	}

	if err != nil {
		return "", err
	}
	return string(line), nil
}

// selectPromptPageSize matches the PageSize used by the go-gh prompter.
const selectPromptPageSize = 20

// clearSelectPrompt erases the rendered Select prompt from the terminal.
// survey/v2 does not call Cleanup on interrupt, leaving the question and
// option lines visible. This function moves the cursor up past those lines
// and clears to the end of the screen.
func clearSelectPrompt(cfg *config.Config, numOptions int) {
	visible := numOptions
	if visible > selectPromptPageSize {
		visible = selectPromptPageSize
	}
	// 1 line for the question/filter + visible option lines
	lines := 1 + visible
	fmt.Fprintf(cfg.Out, "\033[%dA\033[J", lines)
}

// loadStackResult holds everything returned by loadStack.
type loadStackResult struct {
	GitDir        string
	StackFile     *stack.StackFile
	Stack         *stack.Stack
	CurrentBranch string
	PRDetails     map[string]*github.PRDetails
}

// loadStack is the standard way to obtain a Stack for the current (or given)
// branch.  It resolves the git directory, loads the stack file, determines the
// branch, calls resolveStack (which may prompt for disambiguation), checks for
// a nil stack, and re-reads the current branch (in case disambiguation caused
// a checkout).  Errors are printed via cfg and returned.
//
// loadStack does NOT acquire the stack file lock.  The lock is acquired
// automatically by stack.Save() when writing.
func loadStack(cfg *config.Config, branch string) (*loadStackResult, error) {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil, fmt.Errorf("not a git repository")
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil, fmt.Errorf("failed to load stack state: %w", err)
	}

	branchFromArg := branch != ""
	if branch == "" {
		branch, err = git.CurrentBranch()
		if err != nil {
			cfg.Errorf("failed to get current branch: %s", err)
			return nil, fmt.Errorf("failed to get current branch: %w", err)
		}
	}

	s, err := resolveStack(sf, branch, cfg)
	if err != nil {
		if errors.Is(err, errInterrupt) {
			return nil, errInterrupt
		}
		cfg.Errorf("%s", err)
		return nil, err
	}
	if s == nil {
		if branchFromArg {
			cfg.Errorf("branch %q is not part of a stack", branch)
		} else {
			cfg.Errorf("current branch %q is not part of a stack", branch)
		}
		cfg.Printf("Checkout an existing stack using `%s` or create a new stack using `%s`",
			cfg.ColorCyan("gh stack checkout"), cfg.ColorCyan("gh stack init"))
		return nil, fmt.Errorf("branch %q is not part of a stack", branch)
	}

	// Re-read current branch in case disambiguation caused a checkout.
	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}

	return &loadStackResult{
		GitDir:        gitDir,
		StackFile:     sf,
		Stack:         s,
		CurrentBranch: currentBranch,
	}, nil
}

// handleSaveError translates a stack.Save error into the appropriate user
// message and exit error.  Lock contention and stale-file detection both
// return ErrLockFailed (exit 8); other write failures return ErrSilent (exit 1).
func handleSaveError(cfg *config.Config, err error) error {
	var lockErr *stack.LockError
	if errors.As(err, &lockErr) {
		cfg.Errorf("another process is currently editing the stack — try again later")
		return ErrLockFailed
	}
	var staleErr *stack.StaleError
	if errors.As(err, &staleErr) {
		cfg.Errorf("stack file was modified by another process — please re-run the command")
		return ErrLockFailed
	}
	cfg.Errorf("failed to save stack state: %s", err)
	return ErrSilent
}

// resolveStack finds the stack for the given branch, handling ambiguity when
// a branch (typically a trunk) belongs to multiple stacks. If exactly one
// stack matches, it is returned directly. If multiple stacks match, the user
// is prompted to select one and the working tree is switched to the top branch
// of the selected stack. Returns nil with no error if no stack contains the
// branch.
func resolveStack(sf *stack.StackFile, branch string, cfg *config.Config) (*stack.Stack, error) {
	stacks := sf.FindAllStacksForBranch(branch)

	switch len(stacks) {
	case 0:
		return nil, nil
	case 1:
		return stacks[0], nil
	}

	if !cfg.IsInteractive() {
		return nil, fmt.Errorf("branch %q belongs to multiple stacks; use an interactive terminal to select one", branch)
	}

	cfg.Warningf("Branch %q is the trunk of multiple stacks", branch)

	options := make([]string, len(stacks))
	for i, s := range stacks {
		options[i] = s.DisplayChain()
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selected, err := p.Select("Which stack would you like to use?", "", options)
	if err != nil {
		if isInterruptError(err) {
			clearSelectPrompt(cfg, len(options))
			printInterrupt(cfg)
			return nil, errInterrupt
		}
		return nil, fmt.Errorf("stack selection: %w", err)
	}

	s := stacks[selected]

	if len(s.Branches) == 0 {
		return nil, fmt.Errorf("selected stack %q has no branches", s.DisplayChain())
	}

	// Switch to the top branch of the selected stack so future commands
	// resolve unambiguously.
	topBranch := s.Branches[len(s.Branches)-1].Branch
	if topBranch != branch {
		if err := git.CheckoutBranch(topBranch); err != nil {
			return nil, fmt.Errorf("failed to checkout branch %s: %w", topBranch, err)
		}
		cfg.Successf("Switched to %s", topBranch)
	}

	return s, nil
}

// syncStackPRs discovers and updates pull request metadata for branches in a stack.
// It also collects PRDetails for each branch, returned as a map keyed by branch name.
// The returned map is consumed by LoadBranchNodes to avoid redundant API calls.
//
// When the stack has a remote ID, the stack API is the source of truth: the
// authoritative PR list is fetched from the server and matched to local
// branches by head branch name. PRs remain associated even if closed.
//
// When no remote stack exists, branch-name-based discovery is used:
//
//  1. No tracked PR — look for an OPEN PR by head branch name.
//  2. Tracked PR (not merged) — refresh status by number; if closed,
//     clear the association and fall through to path 1.
//  3. Tracked PR (merged) — skip; the merged state is final.
//
// The transient Queued flag is also populated from the API response.
//
// API calls for different branches are made concurrently to reduce latency.
func syncStackPRs(cfg *config.Config, s *stack.Stack) map[string]*github.PRDetails {
	client, err := cfg.GitHubClient()
	if err != nil {
		return nil
	}

	// When the stack has a remote ID, the stack API is the source of truth.
	if s.ID != "" {
		if details, ok := syncStackPRsFromRemote(client, s); ok {
			return details
		}
	}

	// No remote stack (or remote sync failed) — local discovery.
	// Each branch is processed concurrently; results are collected and applied sequentially.
	type branchResult struct {
		index       int
		pullRequest *stack.PullRequestRef
		queued      bool
		details     *github.PRDetails
		skip        bool // true means keep existing data, don't update
	}

	results := make([]branchResult, len(s.Branches))

	// Fetch PR data for all branches concurrently using a WaitGroup for
	// completion and a semaphore channel to cap the number of in-flight
	// API requests (see maxAPIConcurrency).
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxAPIConcurrency)

	for i := range s.Branches {
		b := s.Branches[i]

		if b.IsMerged() {
			results[i] = branchResult{index: i, skip: true}
			// Provide PRDetails for merged branches from existing tracked PR
			if b.PullRequest != nil && b.PullRequest.Number != 0 {
				results[i].details = &github.PRDetails{
					Number: b.PullRequest.Number,
					State:  "MERGED",
					URL:    b.PullRequest.URL,
					Merged: true,
				}
			}
			continue
		}

		wg.Add(1)
		go func(idx int, branch stack.BranchRef) {
			defer wg.Done()

			// Acquire a semaphore slot to limit concurrent API calls.
			sem <- struct{}{}
			defer func() { <-sem }()

			res := branchResult{index: idx}

			trackedResolved := false
			if branch.PullRequest != nil && branch.PullRequest.Number != 0 {
				// Tracked PR — refresh its state.
				pr, err := client.FindPRByNumber(branch.PullRequest.Number)
				if err != nil {
					// API error — keep existing tracked PR
					res.skip = true
					res.details = prDetailsFromTracked(branch.PullRequest)
					results[idx] = res
					return
				}
				if pr != nil && pr.State != "CLOSED" {
					// PR is open or merged — keep it
					res.pullRequest = &stack.PullRequestRef{
						Number: pr.Number,
						ID:     pr.ID,
						URL:    pr.URL,
						Merged: pr.Merged,
					}
					res.queued = pr.IsQueued()
					res.details = prDetailsFromPR(pr)
					results[idx] = res
					trackedResolved = true
				}
				// Otherwise PR not found or closed — fall through to open-PR lookup
			}

			if trackedResolved {
				return
			}

			// No tracked PR (or cleared) — only adopt OPEN PRs.
			pr, err := client.FindPRForBranch(branch.Branch)
			if err != nil || pr == nil {
				results[idx] = res
				return
			}
			res.pullRequest = &stack.PullRequestRef{
				Number: pr.Number,
				ID:     pr.ID,
				URL:    pr.URL,
			}
			res.queued = pr.IsQueued()
			// FindPRForBranch only returns OPEN PRs
			res.details = &github.PRDetails{
				Number:   pr.Number,
				State:    "OPEN",
				URL:      pr.URL,
				IsDraft:  pr.IsDraft,
				Merged:   false,
				IsQueued: pr.IsQueued(),
			}
			results[idx] = res
		}(i, b)
	}
	wg.Wait()

	// Apply results sequentially to preserve deterministic behavior.
	details := make(map[string]*github.PRDetails)
	for _, res := range results {
		if res.details != nil {
			details[s.Branches[res.index].Branch] = res.details
		}
		if res.skip {
			continue
		}
		b := &s.Branches[res.index]
		if res.pullRequest != nil {
			b.PullRequest = res.pullRequest
			b.Queued = res.queued
		} else if !b.IsMerged() {
			// Clear if we didn't find anything (and original was cleared during discovery)
			if b.PullRequest != nil && res.pullRequest == nil {
				b.PullRequest = nil
				b.Queued = false
			}
		}
	}

	return details
}

// maxAPIConcurrency limits the number of concurrent API calls to avoid hitting secondary rate limits.
const maxAPIConcurrency = 6

// prDetailsFromPR builds PRDetails from a PullRequest returned by FindPRByNumber.
func prDetailsFromPR(pr *github.PullRequest) *github.PRDetails {
	if pr == nil {
		return nil
	}
	return &github.PRDetails{
		Number:   pr.Number,
		State:    pr.State,
		URL:      pr.URL,
		IsDraft:  pr.IsDraft,
		Merged:   pr.Merged,
		IsQueued: pr.IsQueued(),
	}
}

// prDetailsFromTracked builds minimal PRDetails from a tracked PullRequestRef.
func prDetailsFromTracked(ref *stack.PullRequestRef) *github.PRDetails {
	if ref == nil {
		return nil
	}
	state := "OPEN"
	if ref.Merged {
		state = "MERGED"
	}
	return &github.PRDetails{
		Number: ref.Number,
		State:  state,
		URL:    ref.URL,
		Merged: ref.Merged,
	}
}

// syncStackPRsFromRemote uses the stack API to sync PR state. The remote
// stack's PR list is the source of truth — PRs stay associated even if
// closed. Returns the PRDetails map and true if sync succeeded, or nil and
// false if we should fall back to local discovery.
func syncStackPRsFromRemote(client github.ClientOps, s *stack.Stack) (map[string]*github.PRDetails, bool) {
	stacks, err := client.ListStacks()
	if err != nil {
		return nil, false
	}

	// Find our stack in the remote list.
	var remotePRNumbers []int
	for _, rs := range stacks {
		if strconv.Itoa(rs.ID) == s.ID {
			remotePRNumbers = rs.PullRequests
			break
		}
	}
	if remotePRNumbers == nil {
		return nil, false
	}

	// Fetch each remote PR concurrently. Results are written to an ordered
	// slice (one slot per PR number) so that when we build the branch map
	// below, later entries win on duplicate HeadRefNames — matching the
	// sequential behavior of the old code.
	prResults := make([]*github.PullRequest, len(remotePRNumbers))

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxAPIConcurrency) // limits concurrent API calls

	for i, num := range remotePRNumbers {
		wg.Add(1)
		go func(idx, prNum int) {
			defer wg.Done()

			// Acquire a semaphore slot to limit concurrent API calls.
			sem <- struct{}{}
			defer func() { <-sem }()

			pr, err := client.FindPRByNumber(prNum)
			if err != nil || pr == nil {
				return
			}
			// Each goroutine writes to its own index — no lock needed.
			prResults[idx] = pr
		}(i, num)
	}
	wg.Wait()

	// Build map sequentially to preserve order semantics.
	prByBranch := make(map[string]*github.PullRequest, len(remotePRNumbers))
	for _, pr := range prResults {
		if pr != nil {
			prByBranch[pr.HeadRefName] = pr
		}
	}

	// Match remote PRs to local branches and collect PRDetails.
	details := make(map[string]*github.PRDetails)
	for i := range s.Branches {
		b := &s.Branches[i]
		pr, ok := prByBranch[b.Branch]
		if !ok {
			continue
		}
		b.PullRequest = &stack.PullRequestRef{
			Number: pr.Number,
			ID:     pr.ID,
			URL:    pr.URL,
			Merged: pr.Merged,
		}
		b.Queued = pr.IsQueued()
		details[b.Branch] = prDetailsFromPR(pr)
	}

	return details, true
}

// updateBaseSHAs refreshes the Base and Head SHAs for all active branches
// in a stack. Call this after any operation that may have moved branch refs
// (rebase, push, etc.).
func updateBaseSHAs(s *stack.Stack) {
	// Collect all refs we need to resolve, then batch into one git call.
	var refs []string
	type refPair struct {
		index  int
		parent string
		branch string
	}
	var pairs []refPair
	seen := make(map[string]bool)
	for i := range s.Branches {
		if s.Branches[i].IsMerged() {
			continue
		}
		parent := s.ActiveBaseBranch(s.Branches[i].Branch)
		branch := s.Branches[i].Branch
		pairs = append(pairs, refPair{i, parent, branch})
		if !seen[parent] {
			refs = append(refs, parent)
			seen[parent] = true
		}
		if !seen[branch] {
			refs = append(refs, branch)
			seen[branch] = true
		}
	}
	if len(refs) == 0 {
		return
	}
	shaMap, err := git.RevParseMap(refs)
	if err != nil {
		return
	}
	for _, p := range pairs {
		if base, ok := shaMap[p.parent]; ok {
			s.Branches[p.index].Base = base
		}
		if head, ok := shaMap[p.branch]; ok {
			s.Branches[p.index].Head = head
		}
	}
}

// activeBranchNames returns the branch names for all non-merged branches in a stack.
func activeBranchNames(s *stack.Stack) []string {
	active := s.ActiveBranches()
	names := make([]string, len(active))
	for i, b := range active {
		names[i] = b.Branch
	}
	return names
}

// fastForwardBranches fast-forwards each active stack branch to its remote
// tracking branch when the local branch is strictly behind. Returns the names
// of branches that were updated. Branches that are up-to-date, diverged, or
// have no remote tracking branch are silently skipped.
func fastForwardBranches(cfg *config.Config, s *stack.Stack, remote, currentBranch string) []string {
	var updated []string
	for _, br := range s.Branches {
		if br.IsSkipped() {
			continue
		}

		remoteRef := remote + "/" + br.Branch
		refs, err := git.RevParseMulti([]string{br.Branch, remoteRef})
		if err != nil {
			// Remote tracking branch doesn't exist — skip.
			continue
		}
		localSHA, remoteSHA := refs[0], refs[1]

		if localSHA == remoteSHA {
			continue
		}

		isAncestor, err := git.IsAncestor(localSHA, remoteSHA)
		if err != nil || !isAncestor {
			// Diverged or error — skip. This commonly happens after a
			// local rebase and is handled by the push step.
			continue
		}

		// Local is behind remote — fast-forward.
		if currentBranch == br.Branch {
			if err := git.MergeFF(remoteRef); err != nil {
				cfg.Warningf("Failed to fast-forward %s from remote: %v", br.Branch, err)
				continue
			}
		} else {
			if err := git.UpdateBranchRef(br.Branch, remoteSHA); err != nil {
				cfg.Warningf("Failed to fast-forward %s from remote: %v", br.Branch, err)
				continue
			}
		}

		cfg.Successf("Fast-forwarded %s to %s", br.Branch, short(remoteSHA))
		updated = append(updated, br.Branch)
	}
	return updated
}

// resolveOriginalRefs builds a map from branch name to current SHA for all
// branches in the stack. Merged branches that no longer exist locally are
// backfilled from the stack metadata. This map is used as the "before" state
// for cascade rebases and conflict recovery.
func resolveOriginalRefs(s *stack.Stack) (map[string]string, error) {
	branchNames := make([]string, 0, len(s.Branches))
	for _, b := range s.Branches {
		if b.IsMerged() && !git.BranchExists(b.Branch) {
			continue
		}
		branchNames = append(branchNames, b.Branch)
	}
	originalRefs, err := git.RevParseMap(branchNames)
	if err != nil {
		return nil, fmt.Errorf("resolving branch SHAs: %w", err)
	}

	// Backfill merged branches that were deleted locally.
	for _, b := range s.Branches {
		if b.IsMerged() && !git.BranchExists(b.Branch) {
			if b.Head != "" {
				originalRefs[b.Branch] = b.Head
			}
		}
	}
	return originalRefs, nil
}

// fastForwardTrunk fast-forwards the trunk branch to match its remote tracking
// branch. Returns true if trunk was updated.
func fastForwardTrunk(cfg *config.Config, trunk, remote, currentBranch string) bool {
	// If the local trunk branch doesn't exist, there's nothing to
	// fast-forward. The remote tracking ref is sufficient for rebasing.
	if !git.BranchExists(trunk) {
		return false
	}

	localSHA, remoteSHA := "", ""
	trunkRefs, trunkErr := git.RevParseMulti([]string{trunk, remote + "/" + trunk})
	if trunkErr == nil {
		localSHA, remoteSHA = trunkRefs[0], trunkRefs[1]
	}

	if trunkErr != nil {
		cfg.Warningf("Could not compare trunk %s with remote — skipping trunk update", trunk)
		return false
	}

	if localSHA == remoteSHA {
		cfg.Successf("Trunk %s is already up to date", trunk)
		return false
	}

	isAncestor, err := git.IsAncestor(localSHA, remoteSHA)
	if err != nil {
		cfg.Warningf("Could not determine fast-forward status for %s: %v", trunk, err)
		return false
	}
	if !isAncestor {
		cfg.Warningf("Trunk %s has diverged from %s — skipping trunk update", trunk, remote)
		cfg.Printf("  Local and remote %s have diverged. Resolve manually.", trunk)
		return false
	}

	if currentBranch == trunk {
		if err := git.MergeFF(remote + "/" + trunk); err != nil {
			cfg.Warningf("Failed to fast-forward %s: %v", trunk, err)
			return false
		}
	} else {
		if err := git.UpdateBranchRef(trunk, remoteSHA); err != nil {
			cfg.Warningf("Failed to fast-forward %s: %v", trunk, err)
			return false
		}
	}

	cfg.Successf("Trunk %s fast-forwarded to %s", trunk, short(remoteSHA))
	return true
}

// cascadeRebaseOpts holds parameters for a cascade rebase across a range of
// stack branches.
type cascadeRebaseOpts struct {
	Cfg                       *config.Config
	Stack                     *stack.Stack
	Branches                  []stack.BranchRef // the range of branches to rebase
	StartAbsIdx               int               // index of Branches[0] in Stack.Branches
	OriginalRefs              map[string]string
	NeedsOnto                 bool
	OntoOldBase               string
	CommitterDateIsAuthorDate bool
}

// cascadeRebaseResult describes the outcome of a cascade rebase.
type cascadeRebaseResult struct {
	Rebased        bool     // at least one branch was successfully rebased
	Conflicted     bool     // a rebase conflict was detected (recoverable via --continue)
	Err            error    // a fatal error occurred (not recoverable via --continue)
	ConflictIdx    int      // absolute index in Stack.Branches of the conflicting branch
	ConflictBranch string   // name of the conflicting branch
	ConflictBase   string   // base branch we were rebasing onto
	Remaining      []string // branch names after the conflict point
	NeedsOnto      bool     // --onto state at the conflict point (for --continue)
	OntoOldBase    string   // ontoOldBase at the conflict point (for --continue)
}

// cascadeRebase performs a cascade rebase across the given branch range. It
// stops at the first conflict and returns a result describing what happened.
// The caller is responsible for conflict recovery (abort+restore or save state).
func cascadeRebase(opts cascadeRebaseOpts) cascadeRebaseResult {
	s := opts.Stack
	cfg := opts.Cfg
	needsOnto := opts.NeedsOnto
	ontoOldBase := opts.OntoOldBase
	originalRefs := opts.OriginalRefs
	result := cascadeRebaseResult{}
	rebaseOpts := git.RebaseOpts{CommitterDateIsAuthorDate: opts.CommitterDateIsAuthorDate}

	for i, br := range opts.Branches {
		absIdx := opts.StartAbsIdx + i

		var base string
		if absIdx == 0 {
			base = s.Trunk.Branch
		} else {
			base = s.Branches[absIdx-1].Branch
		}

		// Skip merged and queued branches.
		if br.IsSkipped() {
			ontoOldBase = originalRefs[br.Branch]
			needsOnto = true
			if br.IsMerged() {
				cfg.Successf("Skipping %s (PR %s merged)", br.Branch, cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
			} else if br.IsQueued() {
				cfg.Successf("Skipping %s (PR %s queued)", br.Branch, cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
			}
			continue
		}

		if needsOnto {
			// Find --onto target: first non-skipped ancestor, or trunk.
			newBase := s.Trunk.Branch
			for j := absIdx - 1; j >= 0; j-- {
				if !s.Branches[j].IsSkipped() {
					newBase = s.Branches[j].Branch
					break
				}
			}

			// If ontoOldBase is stale (not an ancestor of the branch), the
			// branch was already rebased past it. Fall back to
			// merge-base(newBase, branch) to avoid replaying already-applied
			// commits.
			actualOldBase := ontoOldBase
			if isAnc, err := git.IsAncestor(ontoOldBase, br.Branch); err == nil && !isAnc {
				if mb, err := git.MergeBase(newBase, br.Branch); err == nil {
					actualOldBase = mb
				}
			}

			if err := git.RebaseOnto(newBase, actualOldBase, br.Branch, rebaseOpts); err != nil {
				remaining := make([]string, 0, len(opts.Branches)-i-1)
				for j := i + 1; j < len(opts.Branches); j++ {
					remaining = append(remaining, opts.Branches[j].Branch)
				}
				return cascadeRebaseResult{
					Rebased:        result.Rebased,
					Conflicted:     true,
					ConflictIdx:    absIdx,
					ConflictBranch: br.Branch,
					ConflictBase:   newBase,
					Remaining:      remaining,
					NeedsOnto:      true,
					OntoOldBase:    originalRefs[br.Branch],
				}
			}

			cfg.Successf("Rebased %s onto %s (adjusted for merged PR)", br.Branch, newBase)
			result.Rebased = true
			ontoOldBase = originalRefs[br.Branch]
		} else {
			var rebaseErr error
			if absIdx > 0 {
				rebaseErr = git.RebaseOnto(base, originalRefs[base], br.Branch, rebaseOpts)
			} else {
				if err := git.CheckoutBranch(br.Branch); err != nil {
					return cascadeRebaseResult{
						Rebased: result.Rebased,
						Err:     fmt.Errorf("checking out %s: %w", br.Branch, err),
					}
				}
				rebaseErr = git.Rebase(base, rebaseOpts)
			}

			if rebaseErr != nil {
				remaining := make([]string, 0, len(opts.Branches)-i-1)
				for j := i + 1; j < len(opts.Branches); j++ {
					remaining = append(remaining, opts.Branches[j].Branch)
				}
				return cascadeRebaseResult{
					Rebased:        result.Rebased,
					Conflicted:     true,
					ConflictIdx:    absIdx,
					ConflictBranch: br.Branch,
					ConflictBase:   base,
					Remaining:      remaining,
					NeedsOnto:      false,
					OntoOldBase:    originalRefs[br.Branch],
				}
			}

			cfg.Successf("Rebased %s onto %s", br.Branch, base)
			result.Rebased = true
		}
	}

	return result
}

// stackNeedsRebase returns true if any active branch in the stack is not based
// on its parent's current tip. This detects when the stack needs rebasing even
// if trunk was not updated in the current run.
func stackNeedsRebase(s *stack.Stack) bool {
	trunk := s.Trunk.Branch
	for i, br := range s.Branches {
		if br.IsSkipped() {
			continue
		}
		// Find the nearest non-skipped parent.
		parent := trunk
		for j := i - 1; j >= 0; j-- {
			if !s.Branches[j].IsSkipped() {
				parent = s.Branches[j].Branch
				break
			}
		}
		isAnc, err := git.IsAncestor(parent, br.Branch)
		if err != nil || !isAnc {
			return true
		}
	}
	return false
}

// resolvePR resolves a user-provided target to a stack and branch using
// waterfall logic: PR URL → PR number → branch name.
func resolvePR(cfg *config.Config, sf *stack.StackFile, target string) (*stack.Stack, *stack.BranchRef, error) {
	// Try parsing as a GitHub PR URL (e.g. https://github.com/owner/repo/pull/42).
	if prNumber, ok := parsePRURL(target); ok {
		s, b := sf.FindStackByPRNumber(prNumber)
		if s != nil && b != nil {
			return s, b, nil
		}
	}

	// Try parsing as a PR number.
	if prNumber, err := strconv.Atoi(target); err == nil && prNumber > 0 {
		s, b := sf.FindStackByPRNumber(prNumber)
		if s != nil && b != nil {
			return s, b, nil
		}
	}

	// Try matching as a branch name.
	stacks := sf.FindAllStacksForBranch(target)
	if len(stacks) > 0 {
		s := stacks[0]
		idx := s.IndexOf(target)
		if idx >= 0 {
			return s, &s.Branches[idx], nil
		}
		// Target matched as trunk — return the first active branch.
		if len(s.Branches) > 0 {
			return s, &s.Branches[0], nil
		}
	}

	return nil, nil, fmt.Errorf(
		"no locally tracked stack found for %q\n"+
			"To pull down a stack from remote, use the PR number: `%s`",
		target,
		cfg.ColorCyan("gh stack checkout <pr-number>"),
	)
}

// parsePRURL extracts a PR number from a GitHub pull request URL.
// Returns the number and true if the URL matches, or 0 and false otherwise.
func parsePRURL(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return 0, false
	}

	// Match paths like /owner/repo/pull/123
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return 0, false
	}

	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ensureRerere checks whether git rerere is enabled and, if not, prompts the
// user for permission before enabling it.  If the user previously declined,
// the prompt is suppressed.  In non-interactive sessions the function is a
// no-op so commands can still run in CI/scripting.
//
// Returns errInterrupt if the user pressed Ctrl+C during the prompt.
func ensureRerere(cfg *config.Config) error {
	enabled, err := git.IsRerereEnabled()
	if err != nil || enabled {
		return nil
	}

	declined, _ := git.IsRerereDeclined()
	if declined {
		return nil
	}

	if !cfg.IsInteractive() {
		return nil
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	ok, err := p.Confirm("Enable git rerere to remember conflict resolutions?", true)
	if err != nil {
		if isInterruptError(err) {
			printInterrupt(cfg)
			return errInterrupt
		}
		return nil
	}

	if ok {
		_ = git.EnableRerere()
	} else {
		_ = git.SaveRerereDeclined()
	}
	return nil
}
