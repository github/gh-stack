package modify

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
)

func findStack(state *StateFile, sf *stack.StackFile) (*stack.Stack, error) {
	var target *stack.Stack
	for i := range sf.Stacks {
		if MatchesStack(state, &sf.Stacks[i]) {
			if target != nil {
				return nil, fmt.Errorf("modify recovery matches multiple stacks; recovery state was retained")
			}
			target = &sf.Stacks[i]
		}
	}
	if target == nil {
		return nil, fmt.Errorf("the stack recorded by modify was not found; recovery state was retained")
	}
	return target, nil
}

func recoveryBranches(state *StateFile) []string {
	names := append([]string{}, state.StackBranches...)
	for _, branch := range state.Snapshot.Branches {
		names = append(names, branch.Name)
	}
	for _, action := range state.Plan {
		names = append(names, action.Branch)
		if action.NewName != "" {
			names = append(names, action.NewName)
		}
	}
	return names
}

func recoveryBranchAvailability(state *StateFile, ops git.Ops) (map[string]bool, error) {
	names := recoveryBranches(state)
	for oldName, newName := range state.RenamedBranches {
		names = append(names, oldName, newName)
	}
	for name := range state.CreatedBranches {
		names = append(names, name)
	}
	if state.PendingAction != nil {
		names = append(names, state.PendingAction.Branch, state.PendingAction.NewName)
	}
	existing := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, checked := existing[name]; checked {
			continue
		}
		exists, err := ops.BranchExists(name)
		if err != nil {
			return nil, fmt.Errorf("checking branch %s before modify recovery: %w", name, err)
		}
		existing[name] = exists
	}
	return existing, nil
}

func recoveryContext(dir string, state *StateFile) (*worktree.Context, error) {
	ctx := state.Worktrees
	if ctx == nil {
		var err error
		ctx, err = worktree.New()
		if err != nil {
			return nil, err
		}
		ops, err := ctx.OriginOps()
		if err != nil {
			return nil, err
		}
		nativeDir, err := ops.GitDir()
		if err != nil {
			return nil, err
		}
		if !worktree.SamePath(nativeDir, dir) {
			return nil, fmt.Errorf("legacy modify recovery must run in its original worktree before shared-state migration")
		}
	}
	if err := checkSingleWorktree(ctx, recoveryBranches(state)); err != nil {
		return nil, err
	}
	return ctx, nil
}

func adoptLegacyContext(state *StateFile, ctx *worktree.Context, ops git.Ops, existing map[string]bool) error {
	if err := checkLegacyRemainingRefs(state, ops); err != nil {
		return err
	}
	for _, branch := range state.Snapshot.Branches {
		name := branch.Name
		for _, action := range state.Plan {
			if action.Type == "rename" && action.Branch == name && !existing[name] && existing[action.NewName] {
				if state.RenamedBranches == nil {
					state.RenamedBranches = make(map[string]string)
				}
				state.RenamedBranches[name] = action.NewName
				ctx.Rename(name, action.NewName)
				name = action.NewName
			}
		}
		sha, err := ops.RevParse(name)
		if err != nil {
			return fmt.Errorf("reading legacy recovery branch %s: %w", name, err)
		}
		if sha != branch.TipSHA || name != branch.Name {
			ctx.Touched[name] = sha
		}
	}
	for _, action := range state.Plan {
		if action.Type != "insert_below" && action.Type != "insert_above" {
			continue
		}
		if !existing[action.NewName] {
			return fmt.Errorf("legacy inserted branch %s is missing; recovery state was retained", action.NewName)
		}
		sha, err := ops.RevParse(action.NewName)
		if err != nil {
			return fmt.Errorf("reading legacy inserted branch %s: %w", action.NewName, err)
		}
		if state.CreatedBranches == nil {
			state.CreatedBranches = make(map[string]string)
		}
		state.CreatedBranches[action.NewName] = sha
	}
	state.Worktrees = ctx
	return nil
}

// Do not seed Touched from a future branch's changed tip: that would authorize
// abort to discard a commit made by someone else while the operation was paused.
func checkLegacyRemainingRefs(state *StateFile, ops git.Ops) error {
	expected := originalTips(state)
	for _, action := range state.Plan {
		if action.Type == "rename" {
			if sha, ok := expected[action.Branch]; ok {
				expected[action.NewName] = sha
				delete(expected, action.Branch)
			}
		}
	}
	remaining := append([]string{}, state.RemainingBranches...)
	active := state.ConflictBranch
	if state.ConflictType == "cherry_pick" {
		active = state.FoldTarget
	} else if state.ConflictType == "rebase_start" {
		remaining = append(remaining, state.ConflictBranch)
		active = ""
	}
	for _, branch := range remaining {
		before, known := expected[branch]
		if !known || branch == active {
			continue
		}
		current, err := ops.RevParse(branch)
		if err != nil {
			return fmt.Errorf("checking legacy remaining branch %s: %w", branch, err)
		}
		if current != before {
			return fmt.Errorf("remaining branch %s changed since the legacy snapshot; cannot safely attribute that change to modify, recovery state was retained", branch)
		}
	}
	return nil
}

// Publish the recovery identity before the catalog so an interrupted write
// remains identifiable on either side of the short catalog save.
func saveProgress(dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile) error {
	state.RecordStack(s)
	if err := SaveState(dir, state); err != nil {
		return fmt.Errorf("saving modify recovery state: %w", err)
	}
	if err := stack.Save(dir, sf); err != nil {
		return fmt.Errorf("saving stack metadata (modify recovery state retained): %w", err)
	}
	return nil
}

func originalTips(state *StateFile) map[string]string {
	refs := make(map[string]string, len(state.Snapshot.Branches)+len(state.CreatedBranches))
	for _, branch := range state.Snapshot.Branches {
		refs[branch.Name] = branch.TipSHA
	}
	for name, sha := range state.CreatedBranches {
		refs[name] = sha
	}
	return refs
}

func prepareMutation(state *StateFile) (git.Ops, error) {
	if err := checkSingleWorktree(state.Worktrees, recoveryBranches(state)); err != nil {
		return nil, err
	}
	ops, err := state.Worktrees.OriginOps()
	if err != nil {
		return nil, err
	}
	if err := worktree.CheckClean(ops, state.Worktrees.Origin.Path); err != nil {
		return nil, err
	}
	return ops, nil
}

func startRefMutation(dir string, state *StateFile, branch string) (git.Ops, error) {
	ops, err := prepareMutation(state)
	if err != nil {
		return nil, err
	}
	expected := state.Worktrees.Touched[branch]
	if expected == "" {
		expected = originalTips(state)[branch]
	}
	if expected == "" {
		err = state.Worktrees.Start(branch)
	} else {
		err = state.Worktrees.Start(branch, expected)
	}
	if err != nil {
		return nil, err
	}
	if err := SaveState(dir, state); err != nil {
		return nil, err
	}
	return ops, nil
}

func finishApply(dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, needsSubmit bool) error {
	state.Phase = PhaseApplying
	state.ConflictBranch, state.ConflictType = "", ""
	state.RemainingBranches = nil
	if err := saveProgress(dir, state, s, sf); err != nil {
		return err
	}
	if needsSubmit {
		state.Phase = PhasePendingSubmit
		state.PreviousStackBranches = nil
		state.RecordStack(s)
		return SaveState(dir, state)
	}
	return ClearState(dir)
}

func unwindState(cfg *config.Config, dir string, state *StateFile, sf *stack.StackFile) error {
	if state.Phase != PhaseApplying && state.Phase != PhaseConflict {
		return fmt.Errorf("cannot unwind modify in phase %q; recovery state was retained", state.Phase)
	}
	target, err := findStack(state, sf)
	if err != nil {
		return err
	}
	var restored stack.Stack
	if err := json.Unmarshal(state.Snapshot.StackMetadata, &restored); err != nil {
		return fmt.Errorf("restoring stack metadata: %w", err)
	}
	if restored.Trunk.Branch == "" {
		return fmt.Errorf("modify snapshot has no stack identity; recovery state was retained")
	}
	ctx, err := recoveryContext(dir, state)
	if err != nil {
		return err
	}
	ops, err := ctx.OriginOps()
	if err != nil {
		return err
	}
	if state.Worktrees == nil {
		if err := checkLegacyRemainingRefs(state, ops); err != nil {
			return err
		}
	}
	rebasing, err := ops.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state before unwind: %w", err)
	}
	picking, err := ops.IsCherryPickInProgress()
	if err != nil {
		return fmt.Errorf("checking cherry-pick state before unwind: %w", err)
	}
	existing, err := recoveryBranchAvailability(state, ops)
	if err != nil {
		return err
	}
	state.Phase = PhaseApplying
	if err := SaveState(dir, state); err != nil {
		return err
	}
	retain := func(err error) error {
		return errors.Join(err, SaveState(dir, state))
	}

	if rebasing {
		if state.Worktrees != nil && state.ConflictType != "rebase" && state.ConflictType != "rebase_start" {
			return retain(fmt.Errorf("an unrelated rebase is active in %s; recovery state was retained", ctx.Origin.Path))
		}
		if err := ops.RebaseAbort(); err != nil {
			return retain(fmt.Errorf("aborting rebase in %s: %w", ctx.Origin.Path, err))
		}
	}
	if picking {
		if state.Worktrees != nil && state.ConflictType != "cherry_pick" {
			return retain(fmt.Errorf("an unrelated cherry-pick is active in %s; recovery state was retained", ctx.Origin.Path))
		}
		if err := ops.CherryPickAbort(); err != nil {
			return retain(fmt.Errorf("aborting cherry-pick in %s: %w", ctx.Origin.Path, err))
		}
	}
	if err := worktree.CheckClean(ops, ctx.Origin.Path); err != nil {
		return retain(err)
	}

	originalBranch := state.OriginalBranch
	if originalBranch == "" && len(state.Snapshot.Branches) > 0 {
		originalBranch = state.Snapshot.Branches[0].Name
	}
	if state.Worktrees == nil {
		if err := restoreLegacy(ops, state, originalBranch, existing); err != nil {
			return retain(err)
		}
	} else {
		if err := recoverPendingAction(state, ops, existing); err != nil {
			return retain(err)
		}
		for i := len(state.Plan) - 1; i >= 0; i-- {
			action := state.Plan[i]
			newName, renamed := state.RenamedBranches[action.Branch]
			if action.Type != "rename" || !renamed {
				continue
			}
			if existing[newName] {
				sha, err := ops.RevParse(newName)
				if err != nil {
					return retain(err)
				}
				if sha != ctx.Touched[newName] || existing[action.Branch] {
					return retain(fmt.Errorf("renamed branch %s changed after modify; leaving it untouched", newName))
				}
				if err := ops.RenameBranch(newName, action.Branch); err != nil {
					return retain(fmt.Errorf("restoring branch name %s: %w", action.Branch, err))
				}
				existing[newName], existing[action.Branch] = false, true
			} else {
				sha, err := ops.RevParse(action.Branch)
				if err != nil || sha != ctx.Touched[newName] {
					return retain(fmt.Errorf("cannot identify renamed branch %s; recovery state was retained", newName))
				}
			}
			ctx.Rename(newName, action.Branch)
			delete(state.RenamedBranches, action.Branch)
			if err := SaveState(dir, state); err != nil {
				return err
			}
		}
		if err := ctx.Restore(originalTips(state)); err != nil {
			return retain(err)
		}
		if originalBranch != "" {
			if err := ctx.RestoreOrigin(originalBranch); err != nil {
				return retain(err)
			}
		}
		for name, original := range state.CreatedBranches {
			if existing[name] {
				sha, err := ops.RevParse(name)
				if err != nil || sha != original {
					return retain(fmt.Errorf("inserted branch %s changed after modify; leaving it untouched", name))
				}
				if err := ops.DeleteBranch(name, true); err != nil {
					return retain(fmt.Errorf("removing inserted branch %s: %w", name, err))
				}
			}
			delete(state.CreatedBranches, name)
		}
	}

	*target = restored
	if err := saveProgress(dir, state, target, sf); err != nil {
		return err
	}
	if err := ClearState(dir); err != nil {
		return err
	}
	cfg.Successf("Stack restored to pre-modify state")
	return nil
}

func recoverPendingAction(state *StateFile, ops git.Ops, existing map[string]bool) error {
	action := state.PendingAction
	if action == nil {
		return nil
	}
	switch action.Type {
	case "rename":
		if existing[action.NewName] && !existing[action.Branch] {
			sha, err := ops.RevParse(action.NewName)
			if err != nil || sha != state.Worktrees.PendingBefore {
				return fmt.Errorf("cannot identify interrupted rename to %s; recovery state was retained", action.NewName)
			}
			if state.RenamedBranches == nil {
				state.RenamedBranches = make(map[string]string)
			}
			state.RenamedBranches[action.Branch] = action.NewName
			state.Worktrees.Rename(action.Branch, action.NewName)
			if err := state.Worktrees.Record(action.NewName); err != nil {
				return err
			}
		} else if !existing[action.Branch] || existing[action.NewName] {
			return fmt.Errorf("cannot identify interrupted rename of %s; recovery state was retained", action.Branch)
		}
	case "insert_below", "insert_above":
		if existing[action.NewName] && state.CreatedBranches[action.NewName] == "" {
			return fmt.Errorf("cannot prove branch %s was created by the interrupted insert; recovery state was retained", action.NewName)
		}
	}
	state.PendingAction = nil
	return nil
}

// Old journals have no last-written refs. Their original-worktree-only
// compatibility path retains the historical snapshot restoration semantics.
func restoreLegacy(ops git.Ops, state *StateFile, originalBranch string, existing map[string]bool) error {
	for i := len(state.Plan) - 1; i >= 0; i-- {
		action := state.Plan[i]
		if action.Type == "rename" && !existing[action.Branch] && existing[action.NewName] {
			if err := ops.RenameBranch(action.NewName, action.Branch); err != nil {
				return fmt.Errorf("restoring renamed branch %s: %w", action.Branch, err)
			}
			existing[action.NewName], existing[action.Branch] = false, true
		}
	}
	for _, branch := range state.Snapshot.Branches {
		if !existing[branch.Name] {
			if err := ops.CreateBranch(branch.Name, branch.TipSHA); err != nil {
				return fmt.Errorf("restoring branch %s: %w", branch.Name, err)
			}
			existing[branch.Name] = true
			continue
		}
		if err := ops.CheckoutBranch(branch.Name); err != nil {
			return fmt.Errorf("checking out %s for recovery: %w", branch.Name, err)
		}
		if err := ops.ResetHard(branch.TipSHA); err != nil {
			return fmt.Errorf("restoring branch %s: %w", branch.Name, err)
		}
	}
	if originalBranch != "" {
		if err := ops.CheckoutBranch(originalBranch); err != nil {
			return fmt.Errorf("restoring original checkout %s: %w", originalBranch, err)
		}
	}
	original := originalTips(state)
	for _, action := range state.Plan {
		if action.NewName != "" && original[action.NewName] == "" &&
			(action.Type == "rename" || action.Type == "insert_below" || action.Type == "insert_above") &&
			existing[action.NewName] {
			if err := ops.DeleteBranch(action.NewName, true); err != nil {
				return fmt.Errorf("removing branch %s created by modify: %w", action.NewName, err)
			}
		}
	}
	return nil
}
