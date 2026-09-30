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
		if err := checkSingleWorktree(ctx, recoveryBranches(state)); err != nil {
			return nil, err
		}
	}
	if _, err := originOps(ctx); err != nil {
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

func expectedHead(state *StateFile, branch string) string {
	if sha := state.Worktrees.Touched[branch]; sha != "" {
		return sha
	}
	if sha := state.CreatedBranches[branch]; sha != "" {
		return sha
	}
	for _, original := range state.Snapshot.Branches {
		name := original.Name
		if renamed := state.RenamedBranches[name]; renamed != "" {
			name = renamed
		}
		if name == branch {
			return original.TipSHA
		}
	}
	if branch == state.StackName {
		return state.TrunkSHA
	}
	return ""
}

func checkExpectedRef(state *StateFile, branch string) error {
	expected := expectedHead(state, branch)
	if expected == "" {
		return nil // Legacy records and not-yet-created insertion targets.
	}
	current, err := git.RevParse(branch)
	if err != nil {
		return fmt.Errorf("reading expected head for %s: %w", branch, err)
	}
	if current != expected {
		return fmt.Errorf("%s changed since this operation's snapshot; leaving it untouched", branch)
	}
	return nil
}

func checkResultRefs(state *StateFile, s *stack.Stack) error {
	for _, branch := range s.Branches {
		if !branch.IsMerged() {
			if err := checkExpectedRef(state, branch.Branch); err != nil {
				return err
			}
		}
	}
	return checkExpectedRef(state, s.Trunk.Branch)
}

func prepareMutation(state *StateFile, branch string) (git.Ops, error) {
	if err := preflightBranches(state, []string{branch}, ""); err != nil {
		return nil, err
	}
	return branchOps(state.Worktrees, branch)
}

func startRefMutation(dir string, state *StateFile, branch string) (git.Ops, error) {
	ops, err := prepareMutation(state, branch)
	if err != nil {
		return nil, err
	}
	expected := expectedHead(state, branch)
	if expected == "" {
		if state.DesiredOrder != nil {
			return nil, fmt.Errorf("no original head recorded for %s; recovery state was retained", branch)
		}
		err = state.Worktrees.Start(branch)
	} else {
		err = state.Worktrees.Start(branch, expected)
	}
	if err != nil {
		return nil, err
	}
	state.PendingHead = state.Worktrees.PendingBefore
	if err := SaveState(dir, state); err != nil {
		return nil, err
	}
	return ops, nil
}

func nativeBranch(state *StateFile) string {
	if state.Worktrees != nil && state.Worktrees.Pending != "" {
		return state.Worktrees.Pending
	}
	if state.ConflictType == "cherry_pick" {
		return state.FoldTarget
	}
	return state.ConflictBranch
}

// ConflictOps resolves the native operation's worktree, which can differ from
// both the invoking worktree and the source branch of a fold.
func ConflictOps(state *StateFile) (git.Ops, string, error) {
	if state == nil || state.Worktrees == nil || nativeBranch(state) == "" {
		return nil, "", fmt.Errorf("modify conflict has no recorded worktree")
	}
	branch := nativeBranch(state)
	ops, err := branchOps(state.Worktrees, branch)
	if err != nil {
		return nil, "", err
	}
	return ops, state.Worktrees.Location(branch).Path, nil
}

func checkPendingHead(state *StateFile, ops git.Ops) error {
	expected := state.PendingHead
	if expected == "" {
		expected = state.Worktrees.PendingBefore
	}
	if expected == "" {
		return nil
	}
	branch := nativeBranch(state)
	sha, err := ops.RevParse(branch)
	if err != nil {
		return err
	}
	if sha != expected {
		return fmt.Errorf("%s changed after modify paused; leaving it untouched in %s", branch, state.Worktrees.Location(branch).Path)
	}
	return nil
}

func saveNativeConflict(dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, ops git.Ops) error {
	sha, err := ops.RevParse(nativeBranch(state))
	if err != nil {
		return fmt.Errorf("recording paused modify head: %w", err)
	}
	state.PendingHead = sha
	state.Phase = PhaseConflict
	return saveProgress(dir, state, s, sf)
}

func finishApply(dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, needsSubmit bool) error {
	state.Phase = PhaseApplying
	state.ConflictBranch, state.ConflictType = "", ""
	state.RemainingBranches = nil
	state.PendingHead = ""
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

func foreignCheckoutOwner(ctx *worktree.Context, branch string) (string, error) {
	trees, err := git.Worktrees()
	if err != nil {
		return "", err
	}
	owner := ""
	for _, tree := range trees {
		if tree.Bare || tree.Branch != branch {
			continue
		}
		if owner != "" && !worktree.SamePath(owner, tree.Path) {
			return "", fmt.Errorf("branch %s is checked out in multiple worktrees", branch)
		}
		if tree.Prunable {
			return "", fmt.Errorf("worktree holding %s is unavailable at %s", branch, tree.Path)
		}
		ops, err := git.ForWorktree(tree.Path)
		if err != nil {
			return "", err
		}
		if err := validateWorktreeRoot(ops, tree.Path); err != nil {
			return "", err
		}
		owner = tree.Path
	}
	if owner != "" && !worktree.SamePath(owner, ctx.Origin.Path) {
		return owner, nil
	}
	return "", nil
}

func restoreCheckout(cfg *config.Config, state *StateFile, s *stack.Stack, aborting bool) error {
	ctx := state.Worktrees
	if _, err := originOps(ctx); err != nil {
		return err
	}
	original := state.OriginalBranch
	if renamed := state.RenamedBranches[original]; renamed != "" {
		original = renamed
	}
	if original == "" {
		return nil
	}
	target := original
	if !aborting {
		plan := state.Execution
		if plan == nil {
			plan = state.Plan
		}
		target = resolveCheckoutBranch(state.OriginalBranch, plan, state.Snapshot, s)
	}
	owner, err := foreignCheckoutOwner(ctx, target)
	if err != nil {
		return err
	}
	if owner != "" {
		if aborting {
			return fmt.Errorf("cannot restore original checkout %s in %s: it is now owned by %s; recovery state was retained", target, ctx.Origin.Path, owner)
		}
		if originalOwner, err := foreignCheckoutOwner(ctx, original); err != nil {
			return err
		} else if originalOwner != "" {
			return fmt.Errorf("cannot retain original branch %s in %s: it is now owned by %s", original, ctx.Origin.Path, originalOwner)
		}
		if err := ctx.RestoreOrigin(original); err != nil {
			return err
		}
		cfg.Infof("Kept %s in %s; surviving branch %s is checked out in worktree %s", original, ctx.Origin.Path, target, owner)
		return nil
	}
	if err := ctx.RestoreOrigin(target); err != nil {
		return err
	}
	if target != original {
		cfg.Printf("Switched to %s in %s (original branch %s is no longer in the stack)", target, ctx.Origin.Path, state.OriginalBranch)
	}
	return nil
}

func abortNative(state *StateFile) error {
	ctx := state.Worktrees
	branch := nativeBranch(state)
	if branch == "" {
		return nil
	}
	ops, err := branchOps(ctx, branch)
	if err != nil {
		return err
	}
	trees, err := git.Worktrees()
	if err != nil {
		return err
	}
	if err := validateOwner(ctx, branch, trees); err != nil {
		return err
	}
	rebasing, err := ops.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state before abort: %w", err)
	}
	picking, err := ops.IsCherryPickInProgress()
	if err != nil {
		return fmt.Errorf("checking cherry-pick state before abort: %w", err)
	}
	if !rebasing && !picking && ctx.Pending != "" {
		sha, err := ops.RevParse(branch)
		if err != nil {
			return err
		}
		if sha == ctx.PendingBefore {
			ctx.Pending, ctx.PendingBefore = "", ""
			state.PendingHead, state.PendingAction = "", nil
			return nil
		}
	}
	if err := checkPendingHead(state, ops); err != nil {
		return err
	}
	path := ctx.Location(branch).Path
	if rebasing {
		if state.ConflictType != "rebase" && state.ConflictType != "rebase_start" {
			return fmt.Errorf("an unrelated rebase is active in %s; recovery state was retained", path)
		}
		if err := ops.RebaseAbort(); err != nil {
			return fmt.Errorf("aborting rebase in %s: %w", path, err)
		}
	}
	if picking {
		if state.ConflictType != "cherry_pick" {
			return fmt.Errorf("an unrelated cherry-pick is active in %s; recovery state was retained", path)
		}
		if err := ops.CherryPickAbort(); err != nil {
			return fmt.Errorf("aborting cherry-pick in %s: %w", path, err)
		}
	}
	if ctx.Pending != "" {
		sha, err := ops.RevParse(branch)
		if err != nil {
			return err
		}
		if sha == ctx.PendingBefore {
			ctx.Pending, ctx.PendingBefore = "", ""
		} else if !rebasing && !picking && state.PendingHead != "" && sha == state.PendingHead {
			if err := ctx.Record(branch); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("cannot prove the native operation on %s was restored safely; recovery state was retained", branch)
		}
	}
	state.PendingHead, state.PendingAction = "", nil
	state.ConflictBranch, state.ConflictType = "", ""
	return nil
}

func preflightRestore(state *StateFile) error {
	original := originalTips(state)
	trees, err := git.Worktrees()
	if err != nil {
		return err
	}
	for branch, expected := range state.Worktrees.Touched {
		ops, err := branchOps(state.Worktrees, branch)
		if err != nil {
			return err
		}
		sha, err := ops.RevParse(branch)
		if err != nil {
			return err
		}
		if sha == original[branch] {
			continue // A previous attempt restored it before a journal save failed.
		}
		if sha != expected {
			return fmt.Errorf("%s changed after this operation; leaving it untouched", branch)
		}
		if err := validateOwner(state.Worktrees, branch, trees); err != nil {
			return err
		}
		if err := worktree.CheckClean(ops, state.Worktrees.Location(branch).Path); err != nil {
			return err
		}
	}
	return nil
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
	ops, err := originOps(ctx)
	if err != nil {
		return err
	}
	if state.Worktrees == nil {
		if err := checkLegacyRemainingRefs(state, ops); err != nil {
			return err
		}
	}
	nativeOps := ops
	if state.Worktrees != nil && nativeBranch(state) != "" {
		nativeOps, err = branchOps(ctx, nativeBranch(state))
		if err != nil {
			return err
		}
	}
	rebasing, err := nativeOps.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state before unwind: %w", err)
	}
	picking, err := nativeOps.IsCherryPickInProgress()
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

	originalBranch := state.OriginalBranch
	if originalBranch == "" && len(state.Snapshot.Branches) > 0 {
		originalBranch = state.Snapshot.Branches[0].Name
	}
	if state.Worktrees == nil {
		if rebasing {
			if err := ops.RebaseAbort(); err != nil {
				return retain(err)
			}
		}
		if picking {
			if err := ops.CherryPickAbort(); err != nil {
				return retain(err)
			}
		}
		if err := worktree.CheckClean(ops, ctx.Origin.Path); err != nil {
			return retain(err)
		}
		if err := restoreLegacy(ops, state, originalBranch, existing); err != nil {
			return retain(err)
		}
	} else {
		if err := recoverPendingAction(state, ops, existing); err != nil {
			return retain(err)
		}
		if err := abortNative(state); err != nil {
			return retain(err)
		}
		if err := SaveState(dir, state); err != nil {
			return err
		}
		for i := len(state.Plan) - 1; i >= 0; i-- {
			action := state.Plan[i]
			newName, renamed := state.RenamedBranches[action.Branch]
			if action.Type != "rename" || !renamed {
				continue
			}
			ops, err := branchOps(ctx, newName)
			if err != nil {
				return retain(err)
			}
			if existing[newName] {
				sha, err := ops.RevParse(newName)
				if err != nil {
					return retain(err)
				}
				if sha != ctx.Touched[newName] || existing[action.Branch] {
					return retain(fmt.Errorf("renamed branch %s changed after modify; leaving it untouched", newName))
				}
				state.PendingAction = &Action{Type: "undo_rename", Branch: newName, NewName: action.Branch}
				ops, err = startRefMutation(dir, state, newName)
				if err != nil {
					return retain(err)
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
			if err := ctx.Record(action.Branch); err != nil {
				return retain(err)
			}
			delete(state.RenamedBranches, action.Branch)
			state.PendingAction, state.PendingHead = nil, ""
			if err := SaveState(dir, state); err != nil {
				return err
			}
		}
		if err := preflightRestore(state); err != nil {
			return retain(err)
		}
		if err := ctx.Restore(originalTips(state)); err != nil {
			return retain(err)
		}
		if err := SaveState(dir, state); err != nil {
			return err
		}
		state.OriginalBranch = originalBranch
		if err := restoreCheckout(cfg, state, &restored, true); err != nil {
			return retain(err)
		}
		for name, original := range state.CreatedBranches {
			ops, err := branchOps(ctx, name)
			if err != nil {
				return retain(err)
			}
			if existing[name] {
				sha, err := ops.RevParse(name)
				if err != nil || sha != original {
					return retain(fmt.Errorf("inserted branch %s changed after modify; leaving it untouched", name))
				}
				trees, err := git.Worktrees()
				if err != nil {
					return retain(err)
				}
				if err := validateOwner(ctx, name, trees); err != nil {
					return retain(err)
				}
				if err := ops.DeleteBranch(name, true); err != nil {
					return retain(fmt.Errorf("removing inserted branch %s: %w", name, err))
				}
				existing[name] = false
			}
			delete(state.CreatedBranches, name)
			if err := SaveState(dir, state); err != nil {
				return err
			}
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
	case "rename", "undo_rename":
		var err error
		ops, err = branchOps(state.Worktrees, action.Branch)
		if err != nil {
			return err
		}
		if existing[action.NewName] && !existing[action.Branch] {
			sha, err := ops.RevParse(action.NewName)
			if err != nil || sha != state.Worktrees.PendingBefore {
				return fmt.Errorf("cannot identify interrupted rename to %s; recovery state was retained", action.NewName)
			}
			if action.Type == "rename" {
				if state.RenamedBranches == nil {
					state.RenamedBranches = make(map[string]string)
				}
				state.RenamedBranches[action.Branch] = action.NewName
			} else {
				delete(state.RenamedBranches, action.NewName)
			}
			state.Worktrees.Rename(action.Branch, action.NewName)
			trees, err := git.Worktrees()
			if err != nil {
				return err
			}
			if err := validateOwner(state.Worktrees, action.NewName, trees); err != nil {
				return err
			}
			if err := state.Worktrees.Record(action.NewName); err != nil {
				return err
			}
			state.PendingHead = ""
		} else if !existing[action.Branch] || existing[action.NewName] {
			return fmt.Errorf("cannot identify interrupted rename of %s; recovery state was retained", action.Branch)
		}
	case "insert_below", "insert_above":
		if existing[action.NewName] && state.CreatedBranches[action.NewName] == "" {
			return fmt.Errorf("cannot prove branch %s was created by the interrupted insert; recovery state was retained", action.NewName)
		}
	case "fold_down", "fold_rebase":
		return nil // The native operation must be aborted in its receiver.
	default:
		return fmt.Errorf("unknown pending modify action %q; recovery state was retained", action.Type)
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
