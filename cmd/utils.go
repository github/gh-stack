package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
)

// ErrSilent indicates the error has already been printed to the user.
// Execute() will exit with code 1 but will not print the error again.
var ErrSilent = &ExitError{Code: 1}

// Typed exit errors for programmatic detection by scripts and agents.
var (
	ErrNotInStack        = &ExitError{Code: 2} // branch/stack not found
	ErrConflict          = &ExitError{Code: 3} // rebase conflict
	ErrAPIFailure        = &ExitError{Code: 4} // GitHub API error
	ErrInvalidArgs       = &ExitError{Code: 5} // invalid arguments or flags
	ErrDisambiguate      = &ExitError{Code: 6} // multiple stacks/remotes, can't auto-select
	ErrRebaseActive      = &ExitError{Code: 7} // rebase already in progress
	ErrLockFailed        = &ExitError{Code: 8} // could not acquire stack file lock
	ErrStacksUnavailable = &ExitError{Code: 9} // stacked PRs not available for this repository
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
// For each branch, it queries GitHub for the most recent PR and updates the
// PullRequestRef including merge status. Branches with already-merged PRs are skipped.
// The transient Queued flag is also populated from the API response.
func syncStackPRs(cfg *config.Config, s *stack.Stack) {
	client, err := cfg.GitHubClient()
	if err != nil {
		return
	}

	for i := range s.Branches {
		b := &s.Branches[i]

		if b.IsMerged() {
			continue
		}

		pr, err := client.FindAnyPRForBranch(b.Branch)
		if err != nil || pr == nil {
			continue
		}

		b.PullRequest = &stack.PullRequestRef{
			Number: pr.Number,
			ID:     pr.ID,
			URL:    pr.URL,
			Merged: pr.Merged,
		}
		b.Queued = pr.IsQueued()
	}
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
