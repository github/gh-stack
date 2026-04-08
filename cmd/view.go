package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/spf13/cobra"
)

type viewOptions struct {
	short  bool
	asJSON bool
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
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "Output stack data as JSON")

	return cmd
}

func runView(cfg *config.Config, opts *viewOptions) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	// Sync PR state and save (best-effort).
	syncStackPRs(cfg, s)
	stack.SaveNonBlocking(gitDir, sf)

	if opts.asJSON {
		return viewJSON(cfg, s, currentBranch)
	}

	if opts.short {
		return viewShort(cfg, s, currentBranch)
	}

	return viewFull(cfg, s, currentBranch)
}

func viewShort(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	var repoHost, repoOwner, repoName string
	if repo, err := cfg.Repo(); err == nil {
		repoHost = repo.Host
		repoOwner = repo.Owner
		repoName = repo.Name
	}

	for i := len(s.Branches) - 1; i >= 0; i-- {
		b := s.Branches[i]
		merged := b.IsMerged()
		queued := b.IsQueued()

		// Insert separator when transitioning from active to queued section
		if queued && !merged && (i == len(s.Branches)-1 || (!s.Branches[i+1].IsQueued() && !s.Branches[i+1].IsMerged())) {
			cfg.Outf("├─── %s ────\n", cfg.ColorWarning("queued"))
		}

		// Insert separator when transitioning from active/queued to merged section
		if merged && (i == len(s.Branches)-1 || !s.Branches[i+1].IsMerged()) {
			cfg.Outf("├─── %s ────\n", cfg.ColorMagenta("merged"))
		}

		indicator := branchStatusIndicator(cfg, s, b)
		prSuffix := shortPRSuffix(cfg, b, repoHost, repoOwner, repoName)
		if b.Branch == currentBranch {
			cfg.Outf("» %s%s%s %s\n", cfg.ColorBold(b.Branch), indicator, prSuffix, cfg.ColorCyan("(current)"))
		} else if merged {
			cfg.Outf("│ %s%s%s\n", cfg.ColorGray(b.Branch), indicator, prSuffix)
		} else if queued {
			cfg.Outf("│ %s%s%s\n", cfg.ColorWarning(b.Branch), indicator, prSuffix)
		} else {
			cfg.Outf("├ %s%s%s\n", b.Branch, indicator, prSuffix)
		}
	}
	cfg.Outf("└ %s\n", s.Trunk.Branch)
	return nil
}

// branchStatusIndicator returns a colored status icon for a branch:
//   - ✓ (purple) if the PR has been merged
//   - ◎ (yellow) if the PR is queued in a merge queue
//   - ⚠ (yellow) if the branch needs rebasing (non-linear history)
//   - ○ (green) if there is an open PR
func branchStatusIndicator(cfg *config.Config, s *stack.Stack, b stack.BranchRef) string {
	if b.IsMerged() {
		return " " + cfg.ColorMagenta("✓")
	}

	if b.IsQueued() {
		return " " + cfg.ColorWarning("◎")
	}

	baseBranch := s.ActiveBaseBranch(b.Branch)
	if needsRebase, err := git.IsAncestor(baseBranch, b.Branch); err == nil && !needsRebase {
		return " " + cfg.ColorWarning("⚠")
	}

	if b.PullRequest != nil && b.PullRequest.Number != 0 {
		return " " + cfg.ColorSuccess("○")
	}

	return ""
}

// JSON output types for gh stack view --json.
type viewJSONOutput struct {
	Trunk         string           `json:"trunk"`
	Prefix        string           `json:"prefix,omitempty"`
	CurrentBranch string           `json:"currentBranch"`
	Branches      []viewJSONBranch `json:"branches"`
}

type viewJSONBranch struct {
	Name        string      `json:"name"`
	Head        string      `json:"head,omitempty"`
	Base        string      `json:"base,omitempty"`
	IsCurrent   bool        `json:"isCurrent"`
	IsMerged    bool        `json:"isMerged"`
	IsQueued    bool        `json:"isQueued"`
	NeedsRebase bool        `json:"needsRebase"`
	PR          *viewJSONPR `json:"pr,omitempty"`
}

type viewJSONPR struct {
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state"`
}

func viewJSON(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	out := viewJSONOutput{
		Trunk:         s.Trunk.Branch,
		Prefix:        s.Prefix,
		CurrentBranch: currentBranch,
		Branches:      make([]viewJSONBranch, 0, len(s.Branches)),
	}

	for _, b := range s.Branches {
		jb := viewJSONBranch{
			Name:      b.Branch,
			Head:      b.Head,
			Base:      b.Base,
			IsCurrent: b.Branch == currentBranch,
			IsMerged:  b.IsMerged(),
			IsQueued:  b.IsQueued(),
		}

		// Check if the branch needs rebasing (base not ancestor of branch).
		if !jb.IsMerged {
			baseBranch := s.ActiveBaseBranch(b.Branch)
			if isAnc, err := git.IsAncestor(baseBranch, b.Branch); err == nil && !isAnc {
				jb.NeedsRebase = true
			}
		}

		if b.PullRequest != nil && b.PullRequest.Number != 0 {
			state := "OPEN"
			if b.PullRequest.Merged {
				state = "MERGED"
			} else if b.IsQueued() {
				state = "QUEUED"
			}
			jb.PR = &viewJSONPR{
				Number: b.PullRequest.Number,
				URL:    b.PullRequest.URL,
				State:  state,
			}
		}

		out.Branches = append(out.Branches, jb)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling JSON: %w", err)
	}
	_, err = fmt.Fprintf(cfg.Out, "%s\n", data)
	return err
}

func shortPRSuffix(cfg *config.Config, b stack.BranchRef, host, owner, repo string) string {
	if b.PullRequest == nil || b.PullRequest.Number == 0 {
		return ""
	}
	url := b.PullRequest.URL
	if url == "" && owner != "" && repo != "" {
		url = ghapi.PRURL(host, owner, repo, b.PullRequest.Number)
	}
	prNum := cfg.PRLink(b.PullRequest.Number, url)
	colorFn := cfg.ColorSuccess // green for open
	if b.PullRequest.Merged {
		colorFn = cfg.ColorMagenta // purple for merged
	}
	return fmt.Sprintf(" %s", colorFn(prNum))
}

func viewFull(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	if !cfg.IsInteractive() {
		return viewFullStatic(cfg, s, currentBranch)
	}

	return viewFullTUI(cfg, s, currentBranch)
}

func viewFullTUI(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	// Load enriched data for all branches
	nodes := stackview.LoadBranchNodes(cfg, s, currentBranch)

	// Reverse nodes so index 0 = top of stack (matches visual order)
	reversed := make([]stackview.BranchNode, len(nodes))
	for i, n := range nodes {
		reversed[len(nodes)-1-i] = n
	}

	model := stackview.New(reversed, s.Trunk, Version)

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}

	// Checkout branch if user requested it
	if m, ok := finalModel.(stackview.Model); ok {
		if branch := m.CheckoutBranch(); branch != "" {
			if err := git.CheckoutBranch(branch); err != nil {
				cfg.Errorf("failed to checkout %s: %v", branch, err)
			} else {
				cfg.Successf("Switched to %s", branch)
			}
		}
	}

	return nil
}

func viewFullStatic(cfg *config.Config, s *stack.Stack, currentBranch string) error {
	client, clientErr := cfg.GitHubClient()

	var repoHost, repoOwner, repoName string
	repo, repoErr := cfg.Repo()
	if repoErr == nil {
		repoHost = repo.Host
		repoOwner = repo.Owner
		repoName = repo.Name
	}

	var buf bytes.Buffer

	for i := len(s.Branches) - 1; i >= 0; i-- {
		b := s.Branches[i]

		// Insert separator when transitioning from active to merged section
		if b.IsMerged() && (i == len(s.Branches)-1 || !s.Branches[i+1].IsMerged()) {
			fmt.Fprintf(&buf, "╌╌╌ %s ╌╌╌\n", cfg.ColorGray("merged"))
		}

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
				prInfo = "  " + ghapi.PRURL(repoHost, repoOwner, repoName, pr.Number)
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
