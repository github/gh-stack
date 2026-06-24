package cmd

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/spf13/cobra"
)

func WatchCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Interactively browse the stack and switch branches in place",
		Long: `Watch opens an interactive view of the current stack.

It works like 'gh stack view', but the view stays open after you act on it.
Pressing enter on a branch checks it out in place and updates the current
branch marker without closing the view, so you can keep navigating.

Press 'r' to refresh PR and stack state in place, or 'p' to push the whole
stack to the remote (after a confirmation prompt).

Status icons:
  ✓  PR merged
  ◎  PR queued
  ○  PR open
  ⚠  Needs rebase`,
		Example: `  # Open the interactive watch view
  $ gh stack watch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cfg)
		},
	}

	return cmd
}

func runWatch(cfg *config.Config) error {
	if !cfg.IsInteractive() {
		cfg.Errorf("watch requires an interactive terminal; use 'gh stack view' instead")
		return ErrSilent
	}

	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	fmt.Fprintf(cfg.Err, "Loading stack...")

	// Sync PR state and save (best-effort).
	prDetails := syncStackPRs(cfg, s)
	stack.SaveNonBlocking(gitDir, sf)

	fmt.Fprintf(cfg.Err, "\r\033[2K")

	// Load enriched data for all branches.
	nodes := stackview.LoadBranchNodes(cfg, s, currentBranch, prDetails)

	// Reverse nodes so index 0 = top of stack (matches visual order).
	reversed := make([]stackview.BranchNode, len(nodes))
	for i, n := range nodes {
		reversed[len(nodes)-1-i] = n
	}

	// refresh re-syncs PR/stack state and returns fresh nodes in top-down order.
	refresh := func() ([]stackview.BranchNode, error) {
		details := syncStackPRs(cfg, s)
		stack.SaveNonBlocking(gitDir, sf)
		cur, err := git.CurrentBranch()
		if err != nil {
			return nil, err
		}
		fresh := stackview.LoadBranchNodes(cfg, s, cur, details)
		out := make([]stackview.BranchNode, len(fresh))
		for i, n := range fresh {
			out[len(fresh)-1-i] = n
		}
		return out, nil
	}

	// push pushes all active branches in the stack to the remote.
	push := func() error {
		cur, err := git.CurrentBranch()
		if err != nil {
			return err
		}
		remote, err := git.ResolveRemote(cur)
		if err != nil {
			return err
		}
		_ = syncStackPRs(cfg, s)
		active := activeBranchNames(s)
		if len(active) == 0 {
			return fmt.Errorf("no active branches to push (all merged or queued)")
		}
		_ = git.FetchBranches(remote, active)
		if err := git.Push(remote, active, true, false); err != nil {
			return err
		}
		updateBaseSHAs(s)
		return stack.Save(gitDir, sf)
	}

	model := stackview.NewInteractive(reversed, s.Trunk, Version, stackview.InteractiveActions{
		Checkout: git.CheckoutBranch,
		Refresh:  refresh,
		Push:     push,
	})

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}

	return nil
}
