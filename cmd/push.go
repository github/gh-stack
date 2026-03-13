package cmd

import (
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type pushOptions struct {
	auto  bool
	draft bool
	noPRs bool
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
	cmd.Flags().BoolVar(&opts.noPRs, "no-prs", false, "Push branches without creating or updating PRs")

	return cmd
}

func runPush(cfg *config.Config, opts *pushOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	s, err := resolveStack(sf, currentBranch, cfg)
	if err != nil {
		cfg.Errorf("%s", err)
		return nil
	}
	if s == nil {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return nil
	}

	// Re-read current branch in case disambiguation caused a checkout
	currentBranch, err = git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return nil
	}

	// Push all branches
	merged := s.MergedBranches()
	if len(merged) > 0 {
		cfg.Printf("Skipping %d merged %s", len(merged), plural(len(merged), "branch", "branches"))
	}
	for _, b := range s.ActiveBranches() {
		cfg.Printf("Pushing %s...", b.Branch)
		if err := git.Push("origin", []string{b.Branch}, true, false); err != nil {
			cfg.Errorf("failed to push %s: %s", b.Branch, err)
			return nil
		}
	}

	if opts.noPRs {
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
			title := defaultPRTitle(baseBranchForDiff, b.Branch)
			if !opts.auto && cfg.IsInteractive() {
				p := prompter.New(cfg.In, cfg.Out, cfg.Err)
				input, err := p.Input(fmt.Sprintf("Title for PR (branch %s):", b.Branch), title)
				if err == nil && input != "" {
					title = input
				}
			}
			body := generatePRBody(s, b.Branch)

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
			// Update base if needed
			if pr.BaseRefName != baseBranch {
				if err := client.UpdatePRBase(pr.ID, baseBranch); err != nil {
					cfg.Warningf("failed to update PR %s base: %v", cfg.PRLink(pr.Number, pr.URL), err)
				} else {
					cfg.Successf("Updated PR %s base to %s", cfg.PRLink(pr.Number, pr.URL), baseBranch)
				}
			} else {
				cfg.Printf("PR %s for %s is up to date", cfg.PRLink(pr.Number, pr.URL), b.Branch)
			}
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
	for i := range s.Branches {
		if s.Branches[i].IsMerged() {
			continue
		}
		parent := s.ActiveBaseBranch(s.Branches[i].Branch)
		if base, err := git.HeadSHA(parent); err == nil {
			s.Branches[i].Base = base
		}
		if head, err := git.HeadSHA(s.Branches[i].Branch); err == nil {
			s.Branches[i].Head = head
		}
	}
	syncStackPRs(cfg, s)

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return nil
	}

	cfg.Successf("Pushed and synced %d branches", len(s.ActiveBranches()))
	return nil
}

// defaultPRTitle generates a PR title from the branch's commits.
// If there is exactly one commit, use its subject. Otherwise, humanize the
// branch name (replace hyphens/underscores with spaces).
func defaultPRTitle(base, head string) string {
	commits, err := git.LogRange(base, head)
	if err == nil && len(commits) == 1 {
		return commits[0].Subject
	}
	return humanize(head)
}

// generatePRBody builds a rich PR description showing the downstack branches,
// the current branch, and a footer with links to the CLI and feedback form.
func generatePRBody(s *stack.Stack, currentBranch string) string {
	var lines []string

	// Current branch entry (always first)
	lines = append(lines, fmt.Sprintf("- `%s` ← *this PR*", currentBranch))

	// Walk downstack from just below current to the bottom, skipping merged branches
	found := false
	for i := len(s.Branches) - 1; i >= 0; i-- {
		b := s.Branches[i]
		if b.Branch == currentBranch {
			found = true
			continue
		}
		if !found {
			continue
		}
		if b.IsMerged() {
			continue
		}
		if b.PullRequest != nil && b.PullRequest.URL != "" {
			lines = append(lines, fmt.Sprintf("- `%s` %s", b.Branch, b.PullRequest.URL))
		} else {
			lines = append(lines, fmt.Sprintf("- `%s`", b.Branch))
		}
	}

	// Trunk entry
	lines = append(lines, fmt.Sprintf("- `%s` (base)", s.Trunk.Branch))

	body := "---\n\n**Stacked Pull Requests**\n" + strings.Join(lines, "\n")
	body += fmt.Sprintf(
		"\n\n<sub>Stack created with <a href=\"https://github.com/github/gh-stack\">GitHub Stacks CLI</a> • <a href=\"%s\">Give Feedback 💬</a></sub>",
		feedbackBaseURL,
	)

	return body
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
