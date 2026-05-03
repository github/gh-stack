package cmd

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/tui/modifyview"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/spf13/cobra"
)

type modifyOptions struct {
	abort bool
	cont  bool
}

func ModifyCmd(cfg *config.Config) *cobra.Command {
	opts := &modifyOptions{}

	cmd := &cobra.Command{
		Use:   "modify",
		Short: "Interactively restructure a stack",
		Long: `Open an interactive TUI to restructure the current stack.

Operations available:
  • Drop branches from the stack
  • Fold branches into adjacent branches
  • Reorder branches
  • Rename branches

All changes are staged in the TUI and applied together when you press Ctrl+S.
After applying, run 'gh stack submit' to push changes and recreate the stack on GitHub.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.abort {
				return runModifyAbort(cfg)
			}
			if opts.cont {
				return runModifyContinue(cfg)
			}
			return runModify(cfg)
		},
	}

	cmd.Flags().BoolVar(&opts.abort, "abort", false, "Abort the modify session and restore the stack to its pre-modify state")
	cmd.Flags().BoolVar(&opts.cont, "continue", false, "Continue after resolving conflicts")

	return cmd
}

func runModify(cfg *config.Config) error {
	// Run all precondition checks
	result, err := checkModifyPreconditions(cfg)
	if err != nil {
		return err
	}

	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	// Load branch data for the TUI
	viewNodes := stackview.LoadBranchNodes(cfg, s, currentBranch)

	// Reverse so index 0 = top of stack (matching visual order)
	reversed := make([]stackview.BranchNode, len(viewNodes))
	for i, n := range viewNodes {
		reversed[len(viewNodes)-1-i] = n
	}

	// Convert to ModifyBranchNodes
	modifyNodes := make([]modifyview.ModifyBranchNode, len(reversed))
	for i, n := range reversed {
		modifyNodes[i] = modifyview.ModifyBranchNode{
			BranchNode:       n,
			OriginalPosition: i,
		}
	}

	// Run the TUI
	model := modifyview.New(modifyNodes, s.Trunk, Version)

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}

	m, ok := finalModel.(modifyview.Model)
	if !ok {
		return fmt.Errorf("unexpected model type")
	}

	// Handle TUI result
	if m.Cancelled() {
		return nil
	}

	if !m.ApplyRequested() {
		return nil
	}

	// Apply the staged changes
	// Re-reverse nodes back to stack order (bottom to top) for the apply engine
	applyNodes := m.Nodes()
	reordered := make([]modifyview.ModifyBranchNode, len(applyNodes))
	for i, n := range applyNodes {
		reordered[len(applyNodes)-1-i] = n
	}

	applyResult, conflict, applyErr := modify.ApplyPlan(cfg, gitDir, s, sf, reordered, currentBranch, updateBaseSHAs)

	if conflict != nil {
		cfg.Warningf("Rebasing %s — conflict", conflict.Branch)

		printConflictDetailsWithContinue(cfg, conflict.Branch, "gh stack modify --continue")
		cfg.Printf("")

		cfg.Printf("Or restore the stack to its pre-modify state with `%s`",
			cfg.ColorCyan("gh stack modify --abort"))
		return ErrConflict
	}

	if applyErr != nil {
		cfg.Errorf("failed to apply modifications: %s", applyErr)
		return ErrSilent
	}

	// Print success summary
	printModifySuccess(cfg, applyResult, s.ID != "")

	return nil
}

// printModifySuccess prints a summary of what was applied.
func printModifySuccess(cfg *config.Config, result *modifyview.ApplyResult, hasRemoteStack bool) {
	if result == nil {
		return
	}

	cfg.Printf("")
	cfg.Successf("Stack modified successfully")

	for _, r := range result.RenamedBranches {
		cfg.Printf("  Renamed: %s → %s", r.OldName, r.NewName)
	}

	for _, d := range result.DroppedPRs {
		cfg.Printf("  Dropped: %s (PR #%d remains open — close with `%s`)",
			d.Branch, d.PRNumber, cfg.ColorCyan(fmt.Sprintf("gh pr close %d", d.PRNumber)))
	}

	if result.MovedBranches > 0 {
		cfg.Printf("  Rebased %d %s", result.MovedBranches,
			plural(result.MovedBranches, "branch", "branches"))
	}

	cfg.Printf("")
	if hasRemoteStack {
		cfg.Printf("Run `%s` to push your changes and update the stack of PRs on GitHub",
			cfg.ColorCyan("gh stack submit"))
	}
}

// runModifyAbort handles recovery to a pre-modify state.
func runModifyAbort(cfg *config.Config) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	state, err := modify.LoadState(gitDir)
	if err != nil {
		cfg.Errorf("failed to read modify state: %s", err)
		return ErrSilent
	}

	if state == nil {
		cfg.Printf("No modify session to abort")
		return nil
	}

	switch state.Phase {
	case "applying":
		cfg.Printf("A modify session was interrupted during the apply phase")
		cfg.Printf("Restoring stack to pre-modify state...")
		if err := modify.UnwindFromStateFile(cfg, gitDir); err != nil {
			cfg.Errorf("recovery failed: %s", err)
			return ErrSilent
		}
		cfg.Successf("Stack restored successfully")
		return nil

	case "pending_submit":
		cfg.Printf("A modify completed but the stack has not been submitted")
		cfg.Printf("Run `%s` to push changes and recreate the stack on GitHub",
			cfg.ColorCyan("gh stack submit"))
		return nil

	default:
		cfg.Errorf("unexpected modify state phase: %s", state.Phase)
		cfg.Printf("Clearing invalid state file...")
		modify.ClearState(gitDir)
		return nil
	}
}

// runModifyContinue continues applying after the user resolves a rebase conflict.
func runModifyContinue(cfg *config.Config) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	if err := modify.ContinueApply(cfg, gitDir, updateBaseSHAs); err != nil {
		cfg.Errorf("%s", err)
		return ErrConflict
	}

	return nil
}

// ---------------------------------------------------------------------------
// Preconditions
// ---------------------------------------------------------------------------

// checkModifyPreconditions runs all precondition checks for the modify command.
func checkModifyPreconditions(cfg *config.Config) (*loadStackResult, error) {
	if !cfg.IsInteractive() {
		cfg.Errorf("modify requires an interactive terminal")
		return nil, ErrSilent
	}

	result, err := loadStack(cfg, "")
	if err != nil {
		return nil, ErrNotInStack
	}

	gitDir := result.GitDir
	s := result.Stack

	// No existing modify state file
	if err := checkNoModifyInProgress(cfg, gitDir); err != nil {
		return nil, err
	}

	// No rebase in progress
	if git.IsRebaseInProgress() {
		cfg.Errorf("a rebase is currently in progress")
		cfg.Printf("Complete the rebase with `%s` or abort with `%s`",
			cfg.ColorCyan("gh stack rebase --continue"),
			cfg.ColorCyan("gh stack rebase --abort"))
		return nil, ErrRebaseActive
	}

	// Clean working tree
	if dirty, err := git.HasUncommittedChanges(); err != nil {
		cfg.Errorf("failed to check working tree status: %s", err)
		return nil, ErrSilent
	} else if dirty {
		cfg.Errorf("uncommitted changes in working tree")
		cfg.Printf("Commit or stash your changes before running modify")
		return nil, ErrSilent
	}

	// Sync PR state and check merge queue
	syncStackPRs(cfg, s)
	if err := modify.CheckNoMergeQueuePRs(cfg, s); err != nil {
		return nil, ErrSilent
	}

	// Stack linearity check
	if err := modify.CheckStackLinearity(cfg, s); err != nil {
		return nil, ErrSilent
	}

	return result, nil
}

// checkNoModifyInProgress checks if a modify state file already exists.
func checkNoModifyInProgress(cfg *config.Config, gitDir string) error {
	state, err := modify.LoadState(gitDir)
	if err != nil {
		return nil
	}
	if state == nil {
		return nil
	}

	switch state.Phase {
	case "applying":
		cfg.Errorf("a previous modify session was interrupted")
		cfg.Printf("Run `%s` to restore your stack",
			cfg.ColorCyan("gh stack modify --abort"))
		return ErrModifyRecovery
	case "conflict":
		cfg.Errorf("a modify has unresolved conflicts")
		cfg.Printf("Run `%s` to continue, or `%s` to restore your stack",
			cfg.ColorCyan("gh stack modify --continue"),
			cfg.ColorCyan("gh stack modify --abort"))
		return ErrSilent
	case "pending_submit":
		cfg.Errorf("a modify was completed but the stack has not been submitted yet")
		cfg.Printf("Run `%s` to push changes and recreate the stack on GitHub",
			cfg.ColorCyan("gh stack submit"))
		return ErrSilent
	default:
		cfg.Errorf("unexpected modify state phase: %s", state.Phase)
		return ErrSilent
	}
}
