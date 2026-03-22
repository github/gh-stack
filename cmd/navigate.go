package cmd

import (
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/spf13/cobra"
)

func UpCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "up [n]",
		Short: "Check out a branch further up in the stack (further from the trunk)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 1
			if len(args) > 0 {
				fmt.Sscanf(args[0], "%d", &n)
			}
			return runNavigate(cfg, n)
		},
	}
}

func DownCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "down [n]",
		Short: "Check out a branch further down in the stack (closer to the trunk)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 1
			if len(args) > 0 {
				fmt.Sscanf(args[0], "%d", &n)
			}
			return runNavigate(cfg, -n)
		},
	}
}

func TopCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "top",
		Short: "Check out the top branch of the stack (furthest from the trunk)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNavigateToEnd(cfg, true)
		},
	}
}

func BottomCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "bottom",
		Short: "Check out the bottom branch of the stack (closest to the trunk)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNavigateToEnd(cfg, false)
		},
	}
}

func runNavigate(cfg *config.Config, delta int) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return nil
	}
	s := result.Stack
	currentBranch := result.CurrentBranch

	idx := s.IndexOf(currentBranch)
	if idx < 0 {
		// Current branch is the trunk (not in s.Branches).
		// loadStack guarantees the branch is part of the stack.
		if delta > 0 && len(s.Branches) > 0 {
			targetIdx := s.FirstActiveBranchIndex()
			if targetIdx < 0 {
				targetIdx = len(s.Branches) - 1
				cfg.Warningf("Warning: all branches in this stack have been merged")
			}
			target := s.Branches[targetIdx].Branch
			if err := git.CheckoutBranch(target); err != nil {
				return err
			}
			cfg.Successf("Switched to %s", target)
			return nil
		}
		cfg.Printf("Already at the bottom of the stack")
		return nil
	}

	onMerged := s.Branches[idx].IsMerged()
	if onMerged {
		cfg.Warningf("Warning: you are on merged branch %q", currentBranch)
	}

	var newIdx int
	var skipped int

	if onMerged {
		// Navigate relative to current position among ALL branches
		newIdx = idx + delta
		if newIdx < 0 {
			newIdx = 0
		}
		if newIdx >= len(s.Branches) {
			newIdx = len(s.Branches) - 1
		}
	} else {
		// Build list of active (non-merged) branch indices
		activeIndices := s.ActiveBranchIndices()

		// Find current position in active list
		activePos := -1
		for i, ai := range activeIndices {
			if ai == idx {
				activePos = i
				break
			}
		}

		newActivePos := activePos + delta
		if newActivePos < 0 {
			newActivePos = 0
		}
		if newActivePos >= len(activeIndices) {
			newActivePos = len(activeIndices) - 1
		}

		newIdx = activeIndices[newActivePos]

		// Count how many merged branches were skipped
		if newIdx > idx {
			for i := idx + 1; i < newIdx; i++ {
				if s.Branches[i].IsMerged() {
					skipped++
				}
			}
		} else if newIdx < idx {
			for i := newIdx + 1; i < idx; i++ {
				if s.Branches[i].IsMerged() {
					skipped++
				}
			}
		}
	}

	if newIdx == idx {
		if delta > 0 {
			cfg.Printf("Already at the top of the stack")
		} else {
			cfg.Printf("Already at the bottom of the stack")
		}
		return nil
	}

	target := s.Branches[newIdx].Branch
	if err := git.CheckoutBranch(target); err != nil {
		return err
	}

	if skipped > 0 {
		cfg.Printf("Skipped %d merged %s", skipped, plural(skipped, "branch", "branches"))
	}

	moved := newIdx - idx
	direction := "up"
	if moved < 0 {
		direction = "down"
		moved = -moved
	}

	cfg.Successf("Checked out %s, %d %s %s", target, moved, plural(moved, "branch", "branches"), direction)
	return nil
}

func runNavigateToEnd(cfg *config.Config, top bool) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return nil
	}
	s := result.Stack
	currentBranch := result.CurrentBranch

	if len(s.Branches) == 0 {
		cfg.Errorf("stack has no branches")
		return nil
	}

	var targetIdx int
	if top {
		targetIdx = len(s.Branches) - 1
	} else {
		targetIdx = s.FirstActiveBranchIndex()
		if targetIdx < 0 {
			// All merged — fall back to first branch with warning
			targetIdx = 0
			cfg.Warningf("Warning: all branches in this stack have been merged")
		}
	}

	target := s.Branches[targetIdx].Branch
	if target == currentBranch {
		if top {
			cfg.Printf("Already at the top of the stack")
		} else {
			cfg.Printf("Already at the bottom of the stack")
		}
		return nil
	}

	if err := git.CheckoutBranch(target); err != nil {
		return err
	}

	if s.Branches[targetIdx].IsMerged() {
		cfg.Warningf("Warning: you are on merged branch %q", target)
	}

	cfg.Successf("Switched to %s", target)
	return nil
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
