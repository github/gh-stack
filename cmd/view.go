package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/browser"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type viewOptions struct {
	short bool
	web   bool
}

func ViewCmd(cfg *config.Config) *cobra.Command {
	opts := &viewOptions{}

	cmd := &cobra.Command{
		Use:   "view",
		Short: "View the current stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runView(cfg, opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.short, "short", "s", false, "Show compact output")
	cmd.Flags().BoolVarP(&opts.web, "web", "w", false, "Open PRs in the browser")

	return cmd
}

func runView(cfg *config.Config, opts *viewOptions) error {
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
		cfg.Printf("Checkout an existing stack using %s or create a new stack using %s", cfg.ColorCyan("gh stack checkout"), cfg.ColorCyan("gh stack init"))
		return nil
	}

	// Re-read current branch in case disambiguation caused a checkout
	currentBranch, err = git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	// Sync PR state
	syncStackPRs(cfg, s)
	_ = stack.Save(gitDir, sf)

	if opts.web {
		return viewWeb(cfg, s)
	}

	if opts.short {
		return viewShort(cfg, s, currentBranch)
	}

	return viewFull(cfg, s, currentBranch)
}

func viewShort(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	var repoOwner, repoName string
	if repo, err := cfg.Repo(); err == nil {
		repoOwner = repo.Owner
		repoName = repo.Name
	}

	for i := len(s.Branches) - 1; i >= 0; i-- {
		b := s.Branches[i]
		merged := b.PullRequest != nil && b.PullRequest.Merged
		indicator := branchStatusIndicator(cfg, s, b)
		prSuffix := shortPRSuffix(cfg, b, repoOwner, repoName)
		if b.Branch == currentBranch {
			cfg.Outf("» %s%s%s %s\n", cfg.ColorBold(b.Branch), indicator, prSuffix, cfg.ColorCyan("(current)"))
		} else if merged {
			cfg.Outf("│ %s%s%s\n", cfg.ColorGray(b.Branch), indicator, prSuffix)
		} else {
			cfg.Outf("├ %s%s%s\n", b.Branch, indicator, prSuffix)
		}
	}
	cfg.Outf("└ %s\n", s.Trunk.Branch)
	return nil
}

// branchStatusIndicator returns a colored status icon for a branch:
//   - ✓ (purple) if the PR has been merged
//   - ⚠ (yellow) if the branch needs rebasing (non-linear history)
//   - ○ (green) if there is an open PR
func branchStatusIndicator(cfg *config.Config, s *stack.Stack, b stack.BranchRef) string {
	if b.PullRequest != nil && b.PullRequest.Merged {
		return " " + cfg.ColorMagenta("✓")
	}

	baseBranch := s.BaseBranch(b.Branch)
	if needsRebase, err := git.IsAncestor(baseBranch, b.Branch); err == nil && !needsRebase {
		return " " + cfg.ColorWarning("⚠")
	}

	if b.PullRequest != nil && b.PullRequest.Number != 0 {
		return " " + cfg.ColorSuccess("○")
	}

	return ""
}

func shortPRSuffix(cfg *config.Config, b stack.BranchRef, owner, repo string) string {
	if b.PullRequest == nil || b.PullRequest.Number == 0 {
		return ""
	}
	prNum := fmt.Sprintf("#%d", b.PullRequest.Number)
	if owner != "" && repo != "" {
		url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, b.PullRequest.Number)
		prNum = fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", url, prNum)
	}
	colorFn := cfg.ColorSuccess // green for open
	if b.PullRequest.Merged {
		colorFn = cfg.ColorMagenta // purple for merged
	}
	return fmt.Sprintf(" %s", colorFn(prNum))
}

func viewFull(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	client, clientErr := cfg.GitHubClient()

	var repoOwner, repoName string
	repo, repoErr := cfg.Repo()
	if repoErr == nil {
		repoOwner = repo.Owner
		repoName = repo.Name
	}

	var buf bytes.Buffer

	for i := len(s.Branches) - 1; i >= 0; i-- {
		b := s.Branches[i]
		isCurrent := b.Branch == currentBranch

		bullet := "○"
		if isCurrent {
			bullet = "●"
		}

		indicator := branchStatusIndicator(cfg, s, b)

		prInfo := ""
		if b.PullRequest != nil {
			if url := b.PullRequest.URL; url != "" {
				prInfo = "  " + url
			}
		} else if clientErr == nil && repoErr == nil {
			pr, err := client.FindPRForBranch(b.Branch)
			if err == nil && pr != nil {
				prInfo = fmt.Sprintf("  https://github.com/%s/%s/pull/%d", repoOwner, repoName, pr.Number)
			}
		}

		branchName := cfg.ColorMagenta(b.Branch)
		if isCurrent {
			branchName = cfg.ColorCyan(b.Branch + " (current)")
		}

		fmt.Fprintf(&buf, "%s %s %s%s\n", bullet, branchName, indicator, prInfo)

		commits, err := git.Log(b.Branch, 1)
		if err == nil && len(commits) > 0 {
			c := commits[0]
			short := c.SHA
			if len(short) > 7 {
				short = short[:7]
			}
			fmt.Fprintf(&buf, "│ %s %s\n", short, cfg.ColorGray("· "+timeAgo(c.Time)))
			fmt.Fprintf(&buf, "│ %s\n", cfg.ColorGray(c.Subject))
		}

		fmt.Fprintf(&buf, "│\n")
	}

	fmt.Fprintf(&buf, "└ %s\n", s.Trunk.Branch)

	return runPager(cfg, buf.String())
}

func runPager(cfg *config.Config, content string) error {
	if !cfg.IsInteractive() {
		_, err := fmt.Fprint(cfg.Out, content)
		return err
	}

	pagerCmd := os.Getenv("GIT_PAGER")
	if pagerCmd == "" {
		pagerCmd = os.Getenv("PAGER")
	}
	if pagerCmd == "" {
		pagerCmd = "less"
	}

	args := strings.Fields(pagerCmd)
	if len(args) == 0 {
		_, err := fmt.Fprint(cfg.Out, content)
		return err
	}
	if args[0] == "less" {
		hasR := false
		for _, a := range args[1:] {
			if strings.Contains(a, "R") {
				hasR = true
				break
			}
		}
		if !hasR {
			args = append(args, "-R")
		}
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = cfg.Out
	cmd.Stderr = cfg.Err
	cmd.Stdin = strings.NewReader(content)

	return cmd.Run()
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		secs := int(d.Seconds())
		if secs == 1 {
			return "1 second ago"
		}
		return fmt.Sprintf("%d seconds ago", secs)
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months <= 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}

func viewWeb(cfg *config.Config, s *stack.Stack) error {
	client, err := cfg.GitHubClient()
	if err != nil {
		return err
	}

	repo, err := cfg.Repo()
	if err != nil {
		return err
	}

	b := browser.New("", cfg.Out, cfg.Err)

	opened := 0
	for _, br := range s.Branches {
		var url string
		if br.PullRequest != nil && br.PullRequest.URL != "" {
			url = br.PullRequest.URL
		} else {
			pr, err := client.FindPRForBranch(br.Branch)
			if err != nil || pr == nil {
				continue
			}
			url = fmt.Sprintf("https://github.com/%s/%s/pull/%d", repo.Owner, repo.Name, pr.Number)
		}
		if err := b.Browse(url); err != nil {
			cfg.Warningf("failed to open %s: %v", url, err)
		} else {
			opened++
		}
	}

	if opened == 0 {
		cfg.Printf("No PRs found to open in browser.")
	} else {
		cfg.Successf("Opened %d PRs in browser", opened)
	}

	return nil
}
