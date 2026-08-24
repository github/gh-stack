package cmd

import (
	"fmt"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/spf13/cobra"
)

func SwitchCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "switch",
		Short: "Interactively switch to another branch in the stack",
		Long: `Show an interactive picker listing all branches in the current stack
and switch to the selected one.

Branches are displayed from top (furthest from trunk) to bottom (closest to
trunk) with their position number. Use the down/up arrow keys or j/k to
navigate and Enter to select.

To move one branch down or up without an interactive picker, use
'gh stack down' or 'gh stack up' instead.`,
		Example: `  # Open the branch picker for the current stack
  $ gh stack switch`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSwitch(cfg)
		},
	}
}

func runSwitch(cfg *config.Config) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	s := result.Stack

	if len(s.Branches) == 0 {
		cfg.Errorf("stack has no branches")
		return ErrNotInStack
	}

	if !cfg.IsInteractive() {
		cfg.Errorf("switch requires an interactive terminal")
		return ErrSilent
	}

	// Build options in reverse order (top of stack first) with 1-based numbering.
	n := len(s.Branches)
	options := make([]string, n)
	currentBranch := result.CurrentBranch
	var defaultOpt string
	for i := 0; i < n; i++ {
		branchIdx := n - 1 - i
		options[i] = fmt.Sprintf("%d. %s", branchIdx+1, s.Branches[branchIdx].Branch)
		if s.Branches[branchIdx].Branch == currentBranch {
			defaultOpt = options[i]
		}
	}

	selected, err := selectSwitchBranch(cfg, "Select a branch in the stack to switch to:", defaultOpt, options)
	if err != nil {
		if isInterruptError(err) {
			clearSelectPrompt(cfg, len(options))
			printInterrupt(cfg)
			return errInterrupt
		}
		cfg.Errorf("failed to select branch: %v", err)
		return ErrSilent
	}

	if selected < 0 || selected >= n {
		cfg.Errorf("invalid selection")
		return ErrSilent
	}

	// Map selection back: index 0 in options = branch at n-1, etc.
	branchIdx := n - 1 - selected
	targetBranch := s.Branches[branchIdx].Branch
	if targetBranch == currentBranch {
		cfg.Infof("Already on %s", targetBranch)
		return nil
	}

	if err := git.CheckoutBranch(targetBranch); err != nil {
		cfg.Errorf("failed to checkout %s: %v", targetBranch, err)
		return ErrSilent
	}

	cfg.Successf("Switched to %s", targetBranch)
	return nil
}

func selectSwitchBranch(cfg *config.Config, prompt, defaultValue string, options []string) (int, error) {
	if cfg.SelectFn != nil {
		return cfg.SelectFn(prompt, defaultValue, options)
	}

	var selected int
	err := survey.AskOne(
		newSwitchSelect(prompt, defaultValue, options),
		&selected,
		survey.WithStdio(cfg.In, cfg.Out, cfg.Err),
	)
	if err != nil {
		return 0, fmt.Errorf("could not prompt: %w", err)
	}
	return selected, nil
}

func newSwitchSelect(prompt, defaultValue string, options []string) *survey.Select {
	selectPrompt := &survey.Select{
		Message:  prompt,
		Options:  options,
		PageSize: selectPromptPageSize,
		VimMode:  true,
		Filter:   switchSelectFilter,
	}
	if defaultValue != "" {
		for _, option := range options {
			if option == defaultValue {
				selectPrompt.Default = defaultValue
				break
			}
		}
	}
	return selectPrompt
}

func switchSelectFilter(filter, value string, _ int) bool {
	filter = strings.ToLower(filter)
	value = strings.ToLower(value)
	return strings.Contains(value, filter) ||
		strings.Contains(text.RemoveDiacritics(value), filter)
}
