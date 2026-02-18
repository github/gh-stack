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

	s := sf.FindStackForBranch(currentBranch)
	if s == nil {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		cfg.Printf("Checkout an existing stack using %s or create a new stack using %s", cfg.ColorCyan("gh stack checkout"), cfg.ColorCyan("gh stack init"))
		return nil
	}

	if opts.web {
		return viewWeb(cfg, s)
	}

	if opts.short {
		return viewShort(cfg, s, currentBranch)
	}

	return viewFull(cfg, s, currentBranch)
}

func viewShort(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	for i := len(s.Branches) - 1; i >= 0; i-- {
		b := s.Branches[i]
		if b.Branch == currentBranch {
			cfg.Outf("● %s %s\n", cfg.ColorBold(b.Branch), cfg.ColorCyan("(current)"))
		} else {
			cfg.Outf("○ %s\n", b.Branch)
		}
	}
	cfg.Outf("└ %s\n", s.Trunk.Branch)
	return nil
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

		prInfo := ""
		if clientErr == nil && repoErr == nil {
			pr, err := client.FindPRForBranch(b.Branch)
			if err == nil && pr != nil {
				prInfo = fmt.Sprintf("  https://github.com/%s/%s/pull/%d", repoOwner, repoName, pr.Number)
			}
		}

		branchName := cfg.ColorMagenta(b.Branch)
		if isCurrent {
			branchName = cfg.ColorCyan(b.Branch + " (current)")
		}

		fmt.Fprintf(&buf, "%s %s%s\n", bullet, branchName, prInfo)

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
		pr, err := client.FindPRForBranch(br.Branch)
		if err != nil || pr == nil {
			continue
		}
		url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", repo.Owner, repo.Name, pr.Number)
		if err := b.Browse(url); err != nil {
			cfg.Warningf("failed to open %s: %v\n", url, err)
		} else {
			opened++
		}
	}

	if opened == 0 {
		cfg.Printf("No PRs found to open in browser.\n")
	} else {
		cfg.Successf("Opened %d PRs in browser\n", opened)
	}

	return nil
}
