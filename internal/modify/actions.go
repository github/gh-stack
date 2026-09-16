package modify

import (
	"errors"
	"fmt"
	"slices"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
)

func affectsBranch(s *stack.Stack, name string) bool {
	for _, branch := range s.Branches {
		if !branch.IsMerged() && branch.PullRequest != nil &&
			(branch.Branch == name || s.ActiveBaseBranch(branch.Branch) == name) {
			return true
		}
	}
	return false
}

func runActions(cfg *config.Config, dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, result *modifyview.ApplyResult) (*modifyview.ConflictInfo, error) {
	if state.NextAction < 0 || state.NextAction > len(state.Execution) {
		return nil, fmt.Errorf("invalid modify action progress; recovery state was retained")
	}
	for state.NextAction < len(state.Execution) {
		action := state.Execution[state.NextAction]
		switch action.Type {
		case "rename":
			if err := applyRename(cfg, dir, state, s, sf, action, result); err != nil {
				return nil, err
			}
		case "insert_above", "insert_below":
			if err := applyInsert(cfg, dir, state, s, sf, action, result); err != nil {
				return nil, err
			}
		case "fold_down", "fold_up":
			conflict, err := applyFold(cfg, dir, state, s, sf, action)
			if err != nil {
				return conflict, err
			}
		case "fold_rebase":
			if conflict, err := normalizeFoldReceiver(dir, state, s, sf, action); err != nil {
				return conflict, err
			}
			cfg.Successf("Rebased %s to exclude dropped branch %s", action.Branch, action.Target)
		case "drop":
			index := s.IndexOf(action.Branch)
			if index < 0 || s.Branches[index].IsMerged() {
				return nil, fmt.Errorf("cannot drop %s from the recorded stack", action.Branch)
			}
			state.AffectsPRs = state.AffectsPRs || affectsBranch(s, action.Branch)
			if pr := s.Branches[index].PullRequest; pr != nil {
				result.DroppedPRs = append(result.DroppedPRs, modifyview.DroppedPR{Branch: action.Branch, PRNumber: pr.Number})
			}
			s.Branches = slices.Delete(s.Branches, index, index+1)
			cfg.Successf("Dropped %s from stack", action.Branch)
		default:
			return nil, fmt.Errorf("unknown modify execution action %q", action.Type)
		}
		state.PendingAction = nil
		state.PendingHead = ""
		state.NextAction++
		state.ConflictBranch, state.ConflictType = "", "actions"
		if err := saveProgress(dir, state, s, sf); err != nil {
			return nil, err
		}
	}
	if err := applyOrder(s, state.DesiredOrder); err != nil {
		return nil, err
	}
	state.ConflictBranch, state.ConflictType = "", "cascade"
	state.RemainingBranches = s.BranchNames()
	return nil, saveProgress(dir, state, s, sf)
}

func normalizeFoldReceiver(dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, action Action) (*modifyview.ConflictInfo, error) {
	index := s.IndexOf(action.Branch)
	if index < 0 || s.Branches[index].IsSkipped() {
		return nil, fmt.Errorf("cannot normalize fold receiver %s", action.Branch)
	}
	cutoff := originalTips(state)[action.Target]
	base := state.OriginalRefs[action.Target]
	if cutoff == "" || base == "" {
		return nil, fmt.Errorf("no original range recorded for dropped branch %s", action.Target)
	}
	if err := checkExpectedRef(state, action.Branch); err != nil {
		return nil, err
	}
	ops, err := branchOps(state.Worktrees, action.Branch)
	if err != nil {
		return nil, err
	}
	ancestor, err := ops.IsAncestor(cutoff, action.Branch)
	if err != nil {
		return nil, err
	}
	if !ancestor {
		return nil, fmt.Errorf("dropped range %s is no longer identifiable in %s; recovery state was retained", action.Target, action.Branch)
	}
	state.PendingAction = &action
	state.AffectsPRs = state.AffectsPRs || s.Branches[index].PullRequest != nil
	return rebaseInOwner(dir, state, s, sf, action.Branch, base, cutoff)
}

func applyRename(cfg *config.Config, dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, action Action, result *modifyview.ApplyResult) error {
	index := s.IndexOf(action.Branch)
	if index < 0 || s.Branches[index].IsMerged() {
		return fmt.Errorf("cannot rename %s in the recorded stack", action.Branch)
	}
	ops, err := prepareMutation(state, action.Branch)
	if err != nil {
		return err
	}
	exists, err := ops.BranchExists(action.NewName)
	if err != nil {
		return fmt.Errorf("checking rename target %s: %w", action.NewName, err)
	}
	if exists {
		return fmt.Errorf("cannot rename %s to %s: branch already exists", action.Branch, action.NewName)
	}
	state.PendingAction = &action
	ops, err = startRefMutation(dir, state, action.Branch)
	if err != nil {
		return err
	}
	if err := ops.RenameBranch(action.Branch, action.NewName); err != nil {
		return errors.Join(fmt.Errorf("renaming %s to %s: %w", action.Branch, action.NewName, err), unwindState(cfg, dir, state, sf))
	}
	state.Worktrees.Rename(action.Branch, action.NewName)
	if state.RenamedBranches == nil {
		state.RenamedBranches = make(map[string]string)
	}
	state.RenamedBranches[action.Branch] = action.NewName
	if err := state.Worktrees.Record(action.NewName); err != nil {
		return err
	}
	state.AffectsPRs = state.AffectsPRs || affectsBranch(s, action.Branch)
	s.Branches[index].Branch = action.NewName
	state.OriginalRefs[action.NewName] = state.OriginalRefs[action.Branch]
	delete(state.OriginalRefs, action.Branch)
	result.RenamedBranches = append(result.RenamedBranches, modifyview.RenamedBranch{OldName: action.Branch, NewName: action.NewName})
	cfg.Successf("Renamed %s → %s", action.Branch, action.NewName)
	return nil
}

func applyInsert(cfg *config.Config, dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, action Action, result *modifyview.ApplyResult) error {
	if action.NewPosition < 0 || action.NewPosition > len(s.Branches) || !s.Contains(action.Target) {
		return fmt.Errorf("invalid insertion position for %s", action.NewName)
	}
	ops, err := prepareMutation(state, action.NewName)
	if err != nil {
		return err
	}
	exists, err := ops.BranchExists(action.NewName)
	if err != nil {
		return fmt.Errorf("checking insertion target %s: %w", action.NewName, err)
	}
	if exists {
		return fmt.Errorf("cannot insert %s: branch already exists", action.NewName)
	}
	if err := checkExpectedRef(state, action.Target); err != nil {
		return err
	}
	parentSHA, err := ops.RevParse(action.Target)
	if err != nil {
		return fmt.Errorf("resolving insert parent %s: %w", action.Target, err)
	}
	state.PendingAction = &action
	if err := SaveState(dir, state); err != nil {
		return err
	}
	if err := ops.CreateBranch(action.NewName, parentSHA); err != nil {
		return errors.Join(fmt.Errorf("creating %s from %s: %w", action.NewName, action.Target, err), unwindState(cfg, dir, state, sf))
	}
	if state.CreatedBranches == nil {
		state.CreatedBranches = make(map[string]string)
	}
	state.CreatedBranches[action.NewName] = parentSHA
	state.OriginalRefs[action.NewName] = parentSHA
	if err := state.Worktrees.Record(action.NewName); err != nil {
		return err
	}
	s.Branches = slices.Insert(s.Branches, action.NewPosition, stack.BranchRef{Branch: action.NewName})
	state.AffectsPRs = state.AffectsPRs || affectsBranch(s, action.NewName)
	result.InsertedBranches = append(result.InsertedBranches, action.NewName)
	cfg.Successf("Inserted %s after %s", action.NewName, action.Target)
	return nil
}

func applyFold(cfg *config.Config, dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, action Action) (*modifyview.ConflictInfo, error) {
	index, target := s.IndexOf(action.Branch), s.IndexOf(action.Target)
	if index < 0 || target < 0 || index == target || s.Branches[index].IsMerged() || s.Branches[target].IsMerged() {
		return nil, fmt.Errorf("cannot fold %s into %s in the recorded stack", action.Branch, action.Target)
	}
	if err := checkExpectedRef(state, action.Branch); err != nil {
		return nil, err
	}
	cutoff := state.OriginalRefs[action.Branch]
	if cutoff == "" {
		return nil, fmt.Errorf("no original parent cutoff recorded for %s", action.Branch)
	}
	state.AffectsPRs = state.AffectsPRs || affectsBranch(s, action.Branch) || s.Branches[target].PullRequest != nil
	if action.Type == "fold_up" {
		// Multiple sources can fold into one receiver. Keep the earliest
		// cutoff so a later fold cannot exclude an earlier source's commits.
		current := state.OriginalRefs[action.Target]
		previousFold := false
		for _, completed := range state.Execution[:state.NextAction] {
			if completed.Type == "fold_up" && completed.Target == action.Target {
				previousFold = true
				break
			}
		}
		useCutoff := !previousFold || current == ""
		if !useCutoff {
			var err error
			useCutoff, err = git.IsAncestor(cutoff, current)
			if err != nil {
				return nil, fmt.Errorf("comparing fold cutoffs for %s: %w", action.Target, err)
			}
		}
		if useCutoff {
			state.OriginalRefs[action.Target] = cutoff
		}
		cfg.Successf("Folded %s into %s", action.Branch, action.Target)
	} else {
		commits, err := git.LogRange(cutoff, action.Branch)
		if err != nil {
			return nil, fmt.Errorf("reading commits to fold from %s: %w", action.Branch, err)
		}
		if len(commits) > 0 {
			state.PendingAction = &action
			state.ConflictBranch, state.ConflictType = action.Branch, "cherry_pick"
			state.FoldBranch, state.FoldTarget = action.Branch, action.Target
			state.RemainingBranches = append([]string{}, state.DesiredOrder...)
			_, err := startRefMutation(dir, state, action.Target)
			if err != nil {
				return nil, err
			}
			ops, err := state.Worktrees.Prepare(action.Target)
			if err != nil {
				return nil, err
			}
			shas := make([]string, len(commits))
			for i, commit := range commits {
				shas[len(commits)-1-i] = commit.SHA
			}
			if err := ops.CherryPick(shas); err != nil {
				files, fileErr := ops.ConflictedFiles()
				picking, stateErr := ops.IsCherryPickInProgress()
				if stateErr != nil {
					return nil, errors.Join(err, fmt.Errorf("checking cherry-pick state in %s: %w", state.Worktrees.Location(action.Target).Path, stateErr))
				}
				if fileErr == nil && len(files) == 0 && !picking {
					return nil, errors.Join(fmt.Errorf("cherry-picking %s into %s: %w", action.Branch, action.Target, err), unwindState(cfg, dir, state, sf))
				}
				if saveErr := saveNativeConflict(dir, state, s, sf, ops); saveErr != nil {
					return nil, errors.Join(err, saveErr)
				}
				if fileErr != nil {
					return nil, errors.Join(err, fileErr)
				}
				return &modifyview.ConflictInfo{Branch: action.Branch, ConflictedFiles: files},
					fmt.Errorf("cherry-pick conflict folding %s into %s in %s", action.Branch, action.Target, state.Worktrees.Location(action.Target).Path)
			}
			if err := state.Worktrees.Record(action.Target); err != nil {
				return nil, err
			}
			cfg.Successf("Folded %s into %s (%d commits)", action.Branch, action.Target, len(commits))
		} else {
			cfg.Printf("No commits to fold from %s", action.Branch)
		}
	}
	s.Branches = slices.Delete(s.Branches, s.IndexOf(action.Branch), s.IndexOf(action.Branch)+1)
	return nil, nil
}
