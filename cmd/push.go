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
				if err == nil && input != "" {
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
