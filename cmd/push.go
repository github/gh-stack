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

type pushOptions struct {
	auto    bool
	draft   bool
	skipPRs bool
	remote  string
}

func PushCmd(cfg *config.Config) *cobra.Command {
	opts := &pushOptions{}

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push all branches in the current stack and create/update PRs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.auto, "auto", false, "Use auto-generated PR titles without prompting")
	cmd.Flags().BoolVar(&opts.draft, "draft", false, "Create PRs as drafts")
	cmd.Flags().BoolVar(&opts.skipPRs, "skip-prs", false, "Push branches without creating or updating PRs")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to push to (defaults to auto-detected remote)")

	return cmd
}

func runPush(cfg *config.Config, opts *pushOptions) error {
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
	// Push should never change the user's checked-out branch.
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

	// Push all active branches atomically
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
	activeBranches := activeBranchNames(s)
	cfg.Printf("Pushing %d %s to %s...", len(activeBranches), plural(len(activeBranches), "branch", "branches"), remote)
	if err := git.Push(remote, activeBranches, true, true); err != nil {
		cfg.Errorf("failed to push: %s", err)
		return ErrSilent
	}

	if opts.skipPRs {
		cfg.Successf("Pushed %d branches (PR creation skipped)", len(s.ActiveBranches()))
		return nil
	}

	// Create or update PRs
	for i, b := range s.Branches {
		if s.Branches[i].IsMerged() {
			continue
		}
		baseBranch := s.ActiveBaseBranch(b.Branch)

		pr, err := client.FindPRForBranch(b.Branch)
		if err != nil {
			cfg.Warningf("failed to check PR for %s: %v", b.Branch, err)
			continue
		}

		if pr == nil {
			// Create new PR — auto-generate title from commits/branch name,
			// then prompt interactively unless --auto or non-interactive.
			baseBranchForDiff := s.ActiveBaseBranch(b.Branch)
			title, commitBody := defaultPRTitleBody(baseBranchForDiff, b.Branch)
			originalTitle := title
			if !opts.auto && cfg.IsInteractive() {
				p := prompter.New(cfg.In, cfg.Out, cfg.Err)
				input, err := p.Input(fmt.Sprintf("Title for PR (branch %s):", b.Branch), title)
				if err != nil {
					if isInterruptError(err) {
						printInterrupt(cfg)
						return ErrSilent
					}
					// Non-interrupt error: keep the auto-generated title.
				} else if input != "" {
					title = input
				}
			}

			// If the user changed the title and the commit had a multi-line
			// message, put the full commit message in the PR body so no
			// content is lost.
			prBody := commitBody
			if title != originalTitle && commitBody != "" {
				prBody = originalTitle + "\n\n" + commitBody
			}
			body := generatePRBody(prBody)

			newPR, createErr := client.CreatePR(baseBranch, b.Branch, title, body, opts.draft)
			if createErr != nil {
				cfg.Warningf("failed to create PR for %s: %v", b.Branch, createErr)
				continue
			}
			cfg.Successf("Created PR %s for %s", cfg.PRLink(newPR.Number, newPR.URL), b.Branch)
			s.Branches[i].PullRequest = &stack.PullRequestRef{
				Number: newPR.Number,
				ID:     newPR.ID,
				URL:    newPR.URL,
			}
		} else {
			cfg.Printf("PR %s for %s is up to date", cfg.PRLink(pr.Number, pr.URL), b.Branch)
			if s.Branches[i].PullRequest == nil {
				s.Branches[i].PullRequest = &stack.PullRequestRef{
					Number: pr.Number,
					ID:     pr.ID,
					URL:    pr.URL,
				}
			}
		}
	}

	// Create or update the stack on GitHub
	syncStack(cfg, client, s)

	// Update base commit hashes and sync PR state
	updateBaseSHAs(s)
	syncStackPRs(cfg, s)

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	cfg.Successf("Pushed and synced %d branches", len(s.ActiveBranches()))
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
		feedbackBaseURL,
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

// pickRemote determines which remote to push to. If remoteOverride is
// non-empty, it is returned directly. Otherwise it delegates to
// git.ResolveRemote for config-based resolution and remote listing.
// If multiple remotes exist with no configured default, the user is
// prompted to select one interactively.
func pickRemote(cfg *config.Config, branch, remoteOverride string) (string, error) {
	if remoteOverride != "" {
		return remoteOverride, nil
	}

	remote, err := git.ResolveRemote(branch)
	if err == nil {
		return remote, nil
	}

	var multi *git.ErrMultipleRemotes
	if !errors.As(err, &multi) {
		return "", err
	}

	if !cfg.IsInteractive() {
		return "", fmt.Errorf("multiple remotes configured; set remote.pushDefault or use an interactive terminal")
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selected, promptErr := p.Select("Multiple remotes found. Which remote should be used?", "", multi.Remotes)
	if promptErr != nil {
		if isInterruptError(promptErr) {
			printInterrupt(cfg)
			return "", errInterrupt
		}
		return "", fmt.Errorf("remote selection: %w", promptErr)
	}
	return multi.Remotes[selected], nil
}

// syncStack creates or updates a stack on GitHub from the active PRs.
// If the stack already exists (s.ID is set), it calls the PUT endpoint with
// the full list of PRs to keep the remote stack in sync. If no stack exists
// yet, it calls POST to create one.
// This is a best-effort operation: failures are reported as warnings but do
// not cause the push command to fail (the PRs are already created).
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
func updateStack(cfg *config.Config, client github.ClientOps, s *stack.Stack, prNumbers []int) {
	if err := client.UpdateStack(s.ID, prNumbers); err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				cfg.Warningf("Stack not found on GitHub (ID %s) — it may have been deleted", s.ID)
				cfg.Printf("  Run %s to re-create the stack on your next push.",
					cfg.ColorCyan("gh stack push"))
				s.ID = "" // Clear stale ID so the next push creates a new stack
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
		handleCreate422(cfg, httpErr)
	case 404:
		cfg.Warningf("Stacked PRs are not yet available for this repository")
	default:
		cfg.Warningf("Failed to create stack on GitHub: %s", httpErr.Message)
	}
}

// handleCreate422 handles 422 errors from the create stack endpoint.
// The three known error messages are:
//   - "Stack must contain at least two pull requests"
//   - "Pull requests must form a stack, where each PR's base ref is the previous PR's head ref"
//   - "Pull requests #123, #124, #125 are already stacked"
func handleCreate422(cfg *config.Config, httpErr *api.HTTPError) {
	msg := httpErr.Message

	if strings.Contains(msg, "already stacked") {
		cfg.Warningf("%s", msg)
		cfg.Printf("  The stack already exists on GitHub. Updating existing stacks will be supported in an upcoming release.")
		return
	}

	if strings.Contains(msg, "must form a stack") {
		cfg.Errorf("Cannot create stack: %s", msg)
		cfg.Printf("  Each PR's base branch must match the previous PR's head branch.")
		return
	}

	// "at least two" or any other validation error
	cfg.Warningf("Could not create stack: %s", msg)
}
