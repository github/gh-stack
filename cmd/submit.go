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
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type submitOptions struct {
	auto   bool
	draft  bool
	remote string
}

func SubmitCmd(cfg *config.Config) *cobra.Command {
	opts := &submitOptions{}

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Create a stack of PRs on GitHub",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubmit(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.auto, "auto", false, "Use auto-generated PR titles without prompting")
	cmd.Flags().BoolVar(&opts.draft, "draft", false, "Create PRs as drafts")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to push to (defaults to auto-detected remote)")

	return cmd
}

func runSubmit(cfg *config.Config, opts *submitOptions) error {
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

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return ErrNotInStack
	}

	// Find the stack for the current branch without switching branches.
	// Submit should never change the user's checked-out branch.
	stacks := sf.FindAllStacksForBranch(currentBranch)
	if len(stacks) == 0 {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return ErrNotInStack
	}
	if len(stacks) > 1 {
		cfg.Errorf("branch %q belongs to multiple stacks; checkout a non-trunk branch first", currentBranch)
		return ErrDisambiguate
	}
	s := stacks[0]

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}

	// Verify that the repository has stacked PRs enabled.
	stacksAvailable := s.ID != ""
	if !stacksAvailable {
		if _, err := client.ListStacks(); err != nil {
			cfg.Warningf("Stacked PRs are not enabled for this repository")
			if cfg.IsInteractive() {
				p := prompter.New(cfg.In, cfg.Out, cfg.Err)
				proceed, promptErr := p.Confirm("Would you still like to create regular PRs?", false)
				if promptErr != nil {
					if isInterruptError(promptErr) {
						printInterrupt(cfg)
						return ErrSilent
					}
					return ErrStacksUnavailable
				}
				if !proceed {
					return ErrStacksUnavailable
				}
			} else {
				return ErrStacksUnavailable
			}
		} else {
			stacksAvailable = true
		}
	}

	// Sync PR state to detect merged/queued PRs before pushing.
	syncStackPRs(cfg, s)

	// Resolve remote for pushing
	remote, err := pickRemote(cfg, currentBranch, opts.remote)
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return ErrSilent
	}
	merged := s.MergedBranches()
	if len(merged) > 0 {
		cfg.Printf("Skipping %d merged %s", len(merged), plural(len(merged), "branch", "branches"))
	}
	queued := s.QueuedBranches()
	if len(queued) > 0 {
		cfg.Printf("Skipping %d queued %s", len(queued), plural(len(queued), "branch", "branches"))
	}
	activeBranches := activeBranchNames(s)
	if len(activeBranches) == 0 {
		cfg.Printf("All branches are merged or queued, nothing to submit")
		return nil
	}

	// If a modification is pending, delete the old remote stack first so that
	// PR base updates are allowed and force-pushes don't trigger auto-merges.
	if stacksAvailable {
		if err := handlePendingModify(cfg, client, s, gitDir); err != nil {
			if errors.Is(err, errInterrupt) {
				return ErrSilent
			}
			// DeleteStack or other failure — don't continue with stale state
			return ErrSilent
		}
	}

	// Fetch the active branches from the remote so that the local tracking refs
	// are up-to-date before pushing. This ensures --force-with-lease has accurate
	// remote state even in shallow clones.
	if err := git.FetchBranches(remote, activeBranches); err != nil {
		cfg.Warningf("Failed to fetch branches from %s: %v", remote, err)
	}

	// Push each branch and create/update its PR in stack order (bottom to top).
	// Sequential pushing ensures each branch's base is up-to-date on the
	// remote before the next branch is pushed, preventing race conditions.
	cfg.Printf("Pushing to %s...", remote)
	for i, b := range s.Branches {
		if s.Branches[i].IsMerged() || s.Branches[i].IsQueued() {
			continue
		}

		// Push this branch
		if err := git.Push(remote, []string{b.Branch}, true, false); err != nil {
			cfg.Errorf("failed to push %s: %s", b.Branch, err)
			return ErrSilent
		}

		// Find or create PR, and fix base if needed
		baseBranch := s.ActiveBaseBranch(b.Branch)
		if err := ensurePR(cfg, client, s, i, baseBranch, opts); err != nil {
			if errors.Is(err, errInterrupt) {
				printInterrupt(cfg)
				return ErrSilent
			}
			// Non-fatal — continue with remaining branches
		}
	}

	// Create or update the stack on GitHub
	if stacksAvailable {
		syncStack(cfg, client, s)
		clearPendingModifyState(cfg, gitDir)
	}

	// Update base commit hashes and sync PR state
	updateBaseSHAs(s)
	syncStackPRs(cfg, s)

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	cfg.Successf("Pushed and synced %d branches", len(s.ActiveBranches()))
	return nil
}

// ensurePR finds or creates a PR for the branch at index i, and updates
// its base branch if needed. This is the single place where PR state is
// reconciled during submit.
func ensurePR(cfg *config.Config, client github.ClientOps, s *stack.Stack, i int, baseBranch string, opts *submitOptions) error {
	b := s.Branches[i]

	pr, err := client.FindPRForBranch(b.Branch)
	if err != nil {
		cfg.Warningf("failed to check PR for %s: %v", b.Branch, err)
		return nil
	}

	if pr == nil {
		return createPR(cfg, client, s, i, baseBranch, opts)
	}

	// PR exists — record it and fix base if needed.
	if s.Branches[i].PullRequest == nil {
		s.Branches[i].PullRequest = &stack.PullRequestRef{
			Number: pr.Number,
			ID:     pr.ID,
			URL:    pr.URL,
		}
	}

	if pr.BaseRefName != baseBranch {
		if s.ID != "" {
			// Stack API owns base relationships — can't update directly.
			cfg.Warningf("PR %s has base %q (expected %q) but cannot update while stacked",
				cfg.PRLink(pr.Number, pr.URL), pr.BaseRefName, baseBranch)
		} else {
			if err := client.UpdatePRBase(pr.Number, baseBranch); err != nil {
				cfg.Warningf("failed to update base branch for PR %s: %v",
					cfg.PRLink(pr.Number, pr.URL), err)
			} else {
				cfg.Successf("Updated base branch for PR %s to %s",
					cfg.PRLink(pr.Number, pr.URL), baseBranch)
			}
		}
	} else {
		cfg.Printf("PR %s for %s is up to date", cfg.PRLink(pr.Number, pr.URL), b.Branch)
	}

	return nil
}

// createPR creates a new PR for the branch at index i.
func createPR(cfg *config.Config, client github.ClientOps, s *stack.Stack, i int, baseBranch string, opts *submitOptions) error {
	b := s.Branches[i]

	title, commitBody := defaultPRTitleBody(baseBranch, b.Branch)
	originalTitle := title
	if !opts.auto && cfg.IsInteractive() {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		input, err := p.Input(fmt.Sprintf("Title for PR (branch %s):", b.Branch), title)
		if err != nil {
			if isInterruptError(err) {
				return errInterrupt
			}
			// Non-interrupt error: keep the auto-generated title.
		} else if input != "" {
			title = input
		}
	}

	prBody := commitBody
	if title != originalTitle && commitBody != "" {
		prBody = originalTitle + "\n\n" + commitBody
	}
	body := generatePRBody(prBody)

	newPR, createErr := client.CreatePR(baseBranch, b.Branch, title, body, opts.draft)
	if createErr != nil {
		cfg.Warningf("failed to create PR for %s: %v", b.Branch, createErr)
		return nil
	}
	cfg.Successf("Created PR %s for %s", cfg.PRLink(newPR.Number, newPR.URL), b.Branch)
	s.Branches[i].PullRequest = &stack.PullRequestRef{
		Number: newPR.Number,
		ID:     newPR.ID,
		URL:    newPR.URL,
	}
	return nil
}

// defaultPRTitleBody generates a PR title and body from the branch's commits.
// If there is exactly one commit, use its subject as the title and its body
// (if any) as the PR body. Otherwise, humanize the branch name for the title.
func defaultPRTitleBody(base, head string) (string, string) {
	commits, err := git.LogRange(base, head)
	if err == nil && len(commits) == 1 {
		return commits[0].Subject, strings.TrimSpace(commits[0].Body)
	}
	return humanize(head), ""
}

// generatePRBody builds a PR description from the commit body (if any)
// and a footer linking to the CLI and feedback form.
func generatePRBody(commitBody string) string {
	var parts []string

	if commitBody != "" {
		parts = append(parts, commitBody)
	}

	footer := fmt.Sprintf(
		"<sub>Stack created with <a href=\"https://github.com/github/gh-stack\">GitHub Stacks CLI</a> • <a href=\"%s\">Give Feedback 💬</a></sub>",
		feedbackURL,
	)
	parts = append(parts, footer)

	return strings.Join(parts, "\n\n---\n\n")
}

// humanize replaces hyphens and underscores with spaces.
func humanize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, s)
}

// handlePendingModify handles the stack recreation after a modify operation.
// It deletes the old remote stack and clears s.ID so syncStack creates a new
// one. The state file is NOT cleared here — it is cleared after syncStack
// succeeds, ensuring retry safety.
func handlePendingModify(cfg *config.Config, client github.ClientOps, s *stack.Stack, gitDir string) error {
	state, err := modify.LoadState(gitDir)
	if err != nil || state == nil {
		return nil // No modify state — nothing to do
	}
	if state.Phase != modify.PhasePendingSubmit {
		return nil // Not in pending_submit phase
	}

	// Prompt for confirmation before overwriting the remote stack
	if cfg.IsInteractive() {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		proceed, promptErr := p.Confirm("The local stack has been modified. Overwrite the existing stack on GitHub?", true)
		if promptErr != nil {
			if isInterruptError(promptErr) {
				printInterrupt(cfg)
				return errInterrupt
			}
			return promptErr
		}
		if !proceed {
			cfg.Printf("Skipping stack recreation — run `%s` when ready",
				cfg.ColorCyan("gh stack submit"))
			return errInterrupt
		}
	}

	// Delete the old remote stack
	if state.PriorRemoteStackID != "" {
		if err := client.DeleteStack(state.PriorRemoteStackID); err != nil {
			var httpErr *api.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
				cfg.Printf("Previous stack already deleted on GitHub")
			} else {
				cfg.Warningf("Failed to delete existing stack: %v", err)
				cfg.Printf("Run `%s` again to retry", cfg.ColorCyan("gh stack submit"))
				return err
			}
		} else {
			cfg.Successf("Cleared existing stack on GitHub")
		}
		// Clear the old stack ID so syncStack creates a new one
		s.ID = ""
	}

	return nil
}

// clearPendingModifyState clears the modify state file after a successful submit.
// Called after syncStack succeeds to ensure retry safety.
func clearPendingModifyState(cfg *config.Config, gitDir string) {
	if !modify.StateExists(gitDir) {
		return
	}
	modify.ClearState(gitDir)
	cfg.Successf("Stack recreated on GitHub to match local state")
}

// syncStack creates or updates a stack on GitHub from the active PRs.
// If the stack already exists (s.ID is set), it calls the PUT endpoint with
// the full list of PRs to keep the remote stack in sync. If no stack exists
// yet, it calls POST to create one.
// This is a best-effort operation: failures are reported as warnings but do
// not cause the submit command to fail (the PRs are already created).
func syncStack(cfg *config.Config, client github.ClientOps, s *stack.Stack) {
	// Collect PR numbers in stack order (bottom to top).
	var prNumbers []int
	for _, b := range s.Branches {
		if b.IsMerged() {
			continue
		}
		if b.PullRequest != nil {
			prNumbers = append(prNumbers, b.PullRequest.Number)
		}
	}

	// The API requires at least 2 PRs to form a stack.
	if len(prNumbers) < 2 {
		return
	}

	if s.ID != "" {
		updateStack(cfg, client, s, prNumbers)
	} else {
		createNewStack(cfg, client, s, prNumbers)
	}
}

// updateStack calls the PUT endpoint to sync the full PR list for an existing stack.
// If the remote stack was deleted (404), it clears the local ID and falls through
// to createNewStack so the user doesn't need to re-run the command.
func updateStack(cfg *config.Config, client github.ClientOps, s *stack.Stack, prNumbers []int) {
	if err := client.UpdateStack(s.ID, prNumbers); err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				// Stack was deleted on GitHub — clear the stale ID and
				// immediately try to re-create it.
				s.ID = ""
				createNewStack(cfg, client, s, prNumbers)
			default:
				cfg.Warningf("Failed to update stack on GitHub: %s", httpErr.Message)
			}
		} else {
			cfg.Warningf("Failed to update stack on GitHub: %v", err)
		}
		return
	}
	cfg.Successf("Stack updated on GitHub with %d PRs", len(prNumbers))
}

// createNewStack calls the POST endpoint to create a new stack, handling the
// three types of 422 errors the API may return.
func createNewStack(cfg *config.Config, client github.ClientOps, s *stack.Stack, prNumbers []int) {
	stackID, err := client.CreateStack(prNumbers)
	if err == nil {
		s.ID = strconv.Itoa(stackID)
		cfg.Successf("Stack created on GitHub with %d PRs", len(prNumbers))
		return
	}

	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		cfg.Warningf("Failed to create stack on GitHub: %v", err)
		return
	}

	switch httpErr.StatusCode {
	case 422:
		handleCreate422(cfg, httpErr, prNumbers)
	case 404:
		cfg.Warningf("Stacked PRs are not enabled for this repository")
	default:
		cfg.Warningf("Failed to create stack on GitHub: %s", httpErr.Message)
	}
}

// handleCreate422 handles 422 errors from the create stack endpoint.
// The three known error messages are:
//   - "Stack must contain at least two pull requests"
//   - "Pull requests must form a stack, where each PR's base ref is the previous PR's head ref"
//   - "Pull requests #123, #124, #125 are already stacked"
func handleCreate422(cfg *config.Config, httpErr *api.HTTPError, prNumbers []int) {
	msg := httpErr.Message

	if strings.Contains(msg, "already stacked") {
		// Check if the error lists exactly the same PRs we're trying to
		// stack. If so, they're already in a stack together — nothing to do.
		// If only a subset matches, the PRs are in a different stack.
		if allPRsInMessage(msg, prNumbers) {
			cfg.Successf("Stack with %d PRs is up to date", len(prNumbers))
			return
		}
		cfg.Warningf("One or more PRs are already part of a different stack on GitHub")
		cfg.Printf("  To fix this, unstack the PRs from the web, then `%s`",
			cfg.ColorCyan("gh stack submit"))
		return
	}

	if strings.Contains(msg, "must form a stack") {
		cfg.Warningf("Cannot create stack: %s", msg)
		cfg.Printf("  Each PR's base branch must match the previous PR's head branch.")
		return
	}

	// "at least two" or any other validation error
	cfg.Warningf("Could not create stack: %s", msg)
}

// allPRsInMessage checks whether every PR number in prNumbers appears
// in the error message (e.g. as "#65"). This distinguishes "our PRs are
// already stacked together" from "some PRs are in a different stack."
func allPRsInMessage(msg string, prNumbers []int) bool {
	for _, n := range prNumbers {
		if !strings.Contains(msg, fmt.Sprintf("#%d", n)) {
			return false
		}
	}
	return true
}
