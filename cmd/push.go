package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type pushOptions struct {
	auto    bool
	draft   bool
	skipPRs bool
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

	return cmd
}

func runPush(cfg *config.Config, opts *pushOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrSilent
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrSilent
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return ErrSilent
	}

	// Find the stack for the current branch without switching branches.
	// Push should never change the user's checked-out branch.
	stacks := sf.FindAllStacksForBranch(currentBranch)
	if len(stacks) == 0 {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return ErrSilent
	}
	if len(stacks) > 1 {
		cfg.Errorf("branch %q belongs to multiple stacks; checkout a non-trunk branch first", currentBranch)
		return ErrSilent
	}
	s := stacks[0]

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrSilent
	}

	// Push all active branches atomically
	remote, err := pickRemote(cfg, currentBranch)
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

	// TODO: Add PRs to a stack
	//
	// We can call an API after all the individual PRs are created/updated to create the stack at once,
	// or we can add a flag to the existing PR API to incrementally build the stack.
	//
	// For now, the PRs are pushed and created individually but are NOT linked as a formal stack on GitHub.
	cfg.Warningf("Stacked PRs is not yet implemented — PRs were created individually.")
	fmt.Fprintf(cfg.Err, "  Once the GitHub Stacks API is available, PRs will be automatically\n")
	fmt.Fprintf(cfg.Err, "  grouped into a Stack.\n")

	// Update base commit hashes and sync PR state
	updateBaseSHAs(s)
	syncStackPRs(cfg, s)

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return ErrSilent
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

// pickRemote determines which remote to push to. It delegates to
// git.ResolveRemote for config-based resolution and remote listing.
// If multiple remotes exist with no configured default, the user is
// prompted to select one interactively.
func pickRemote(cfg *config.Config, branch string) (string, error) {
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
