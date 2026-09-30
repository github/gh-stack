package modify

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
)

// BuildSnapshot captures the current state of the stack for unwind/recovery.
func BuildSnapshot(s *stack.Stack) (Snapshot, error) {
	// Collect all branch names
	names := make([]string, len(s.Branches))
	for i, b := range s.Branches {
		names[i] = b.Branch
	}

	// Resolve all SHAs
	shaMap, err := git.RevParseMap(names)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolving branch SHAs: %w", err)
	}

	// Build branch snapshots
	branches := make([]BranchSnapshot, len(s.Branches))
	for i, b := range s.Branches {
		branches[i] = BranchSnapshot{
			Name:     b.Branch,
			TipSHA:   shaMap[b.Branch],
			Position: i,
		}
	}

	// Serialize stack metadata
	stackJSON, err := json.Marshal(s)
	if err != nil {
		return Snapshot{}, fmt.Errorf("serializing stack metadata: %w", err)
	}

	return Snapshot{
		Branches:      branches,
		StackMetadata: stackJSON,
	}, nil
}

// BuildPlan converts the TUI's staged actions into a list of Actions
// suitable for storage in the state file.
func BuildPlan(nodes []modifyview.ModifyBranchNode) []Action {
	var plan []Action

	// When computing move detection, skip inserted nodes since they
	// shift the indices of existing nodes.
	effectiveIdx := 0
	for i, n := range nodes {
		if n.IsInserted {
			// Inserted nodes always have a PendingAction — handle below
		} else {
			if n.PendingAction == nil && n.OriginalPosition == effectiveIdx && !n.Removed {
				effectiveIdx++
				continue
			}
			effectiveIdx++
		}

		if n.Removed && n.PendingAction == nil {
			continue
		}

		if n.PendingAction != nil {
			action := Action{
				Type:   string(n.PendingAction.Type),
				Branch: n.Ref.Branch,
			}
			if n.PendingAction.Type == modifyview.ActionRename {
				action.NewName = n.PendingAction.NewName
			}
			if n.PendingAction.Type == modifyview.ActionInsertBelow || n.PendingAction.Type == modifyview.ActionInsertAbove {
				action.NewName = n.PendingAction.NewName
				action.NewPosition = i
			}
			plan = append(plan, action)
		}

		if !n.Removed && !n.IsInserted && n.OriginalPosition != i && n.PendingAction == nil {
			plan = append(plan, Action{
				Type:        "move",
				Branch:      n.Ref.Branch,
				NewPosition: i,
			})
		}
	}

	return plan
}

// ApplyPlan executes the staged modifications on the stack.
// updateBaseSHAs is called after rebasing to refresh branch SHAs in the stack metadata.
// It returns an ApplyResult on success or a ConflictInfo if a rebase conflict occurs.
func ApplyPlan(
	cfg *config.Config,
	gitDir string,
	s *stack.Stack,
	sf *stack.StackFile,
	nodes []modifyview.ModifyBranchNode,
	currentBranch string,
	updateBaseSHAs func(*stack.Stack),
) (*modifyview.ApplyResult, *modifyview.ConflictInfo, error) {
	existing, err := LoadState(gitDir)
	if err != nil {
		return nil, nil, err
	}
	if existing != nil {
		return nil, nil, fmt.Errorf("a modify journal already exists; finish or abort that operation before applying another plan")
	}
	ctx, err := CheckWorktrees(s)
	if err != nil {
		return nil, nil, err
	}
	if err := CheckNoMergeQueuePRs(cfg, s); err != nil {
		return nil, nil, err
	}
	execution, desiredOrder, err := compileActions(s, nodes)
	if err != nil {
		return nil, nil, err
	}

	// Build the snapshot before any changes
	snapshot, err := BuildSnapshot(s)
	if err != nil {
		return nil, nil, fmt.Errorf("building snapshot: %w", err)
	}

	// Check branch availability before writing recovery state or changing refs.
	branchNames := make([]string, 0, len(s.Branches)+1)
	branchNames = append(branchNames, s.Trunk.Branch)
	for _, b := range s.Branches {
		if b.IsMerged() {
			continue
		}
		exists, err := git.BranchExists(b.Branch)
		if err != nil {
			return nil, nil, fmt.Errorf("checking branch %s: %w", b.Branch, err)
		}
		if exists {
			branchNames = append(branchNames, b.Branch)
		}
	}
	originalRefs, err := git.RevParseMap(branchNames)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve branch SHAs: %w", err)
	}

	plan := BuildPlan(nodes)
	for _, action := range plan {
		if action.NewName != "" {
			if _, err := git.BranchExists(action.NewName); err != nil {
				return nil, nil, fmt.Errorf("checking target branch %s: %w", action.NewName, err)
			}
		}
	}

	// Find the index of this stack in the stack file for reliable identification
	stackIndex := -1
	for i := range sf.Stacks {
		if &sf.Stacks[i] == s {
			stackIndex = i
			break
		}
	}

	// Write state file with phase "applying"
	stateFile := &StateFile{
		SchemaVersion:      1,
		StackName:          s.Trunk.Branch,
		StackIndex:         stackIndex,
		StartedAt:          time.Now().UTC(),
		Phase:              PhaseApplying,
		PriorRemoteStackID: s.ID,
		Snapshot:           snapshot,
		Plan:               plan,
		OriginalBranch:     currentBranch,
		Worktrees:          ctx,
		RenamedBranches:    make(map[string]string),
		CreatedBranches:    make(map[string]string),
		Execution:          execution,
		DesiredOrder:       desiredOrder,
	}
	stateFile.RecordStack(s)

	result := &modifyview.ApplyResult{Success: true}

	// Build a map of each branch's original parent tip SHA for accurate --onto rebase
	originalParentTips := make(map[string]string)
	for i, b := range s.Branches {
		if b.IsMerged() {
			continue
		}
		var parentName string
		if i == 0 {
			parentName = s.Trunk.Branch
		} else {
			parentName = s.ActiveBaseBranch(b.Branch)
		}
		if sha, ok := originalRefs[parentName]; ok {
			originalParentTips[b.Branch] = sha
		}
	}
	stateFile.OriginalRefs = originalParentTips
	stateFile.TrunkSHA = originalRefs[s.Trunk.Branch]
	if err := preflightBranches(stateFile, remainingTargets(stateFile), ""); err != nil {
		return nil, nil, err
	}
	if err := SaveState(gitDir, stateFile); err != nil {
		return nil, nil, fmt.Errorf("saving modify state: %w", err)
	}
	if conflict, err := runActions(cfg, gitDir, stateFile, s, sf, result); err != nil {
		return nil, conflict, err
	}

	// Step 6: Replay each active branch's original commit range onto its new parent.
	moved, conflict, err := rebaseRemaining(cfg, gitDir, stateFile, s, sf, stateFile.RemainingBranches)
	if err != nil {
		return nil, conflict, err
	}
	result.MovedBranches = moved

	if err := checkResultRefs(stateFile, s); err != nil {
		return nil, nil, err
	}
	if err := restoreCheckout(cfg, stateFile, s, false); err != nil {
		return nil, nil, err
	}

	// Update base SHAs
	updateBaseSHAs(s)

	// Update state file phase — only require submit when PRs are affected
	result.NeedsSubmit = s.ID != "" && stateFile.AffectsPRs
	if err := finishApply(gitDir, stateFile, s, sf, result.NeedsSubmit); err != nil {
		return nil, nil, err
	}

	return result, nil, nil
}

func rebaseRemaining(cfg *config.Config, dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, branches []string) (int, *modifyview.ConflictInfo, error) {
	moved := 0
	for i, name := range branches {
		index := s.IndexOf(name)
		if index < 0 {
			return moved, nil, fmt.Errorf("branch %s is missing from the recorded stack; recovery state was retained", name)
		}
		branch := s.Branches[index]
		if branch.IsMerged() {
			continue
		}
		if branch.IsQueued() {
			return moved, nil, fmt.Errorf("cannot rebase queued branch %s during modify", name)
		}
		if err := checkExpectedRef(state, name); err != nil {
			return moved, nil, err
		}
		ops, err := branchOps(state.Worktrees, name)
		if err != nil {
			return moved, nil, err
		}
		newBase := s.ActiveBaseBranch(name)
		if err := checkExpectedRef(state, newBase); err != nil {
			return moved, nil, err
		}
		oldBase := state.OriginalRefs[name]
		if oldBase == "" {
			oldBase, err = ops.MergeBase(newBase, name)
			if err != nil {
				return moved, nil, fmt.Errorf("finding original base for %s: %w", name, err)
			}
		}
		ancestor, err := ops.IsAncestor(newBase, name)
		if err != nil {
			return moved, nil, fmt.Errorf("checking ancestry of %s: %w", name, err)
		}
		if ancestor {
			base, err := ops.MergeBase(newBase, name)
			if err != nil {
				return moved, nil, fmt.Errorf("finding merge base for %s: %w", name, err)
			}
			if base == oldBase {
				state.RemainingBranches = append([]string{}, branches[i+1:]...)
				continue
			}
		}
		state.RemainingBranches = append([]string{}, branches[i+1:]...)
		state.AffectsPRs = state.AffectsPRs || branch.PullRequest != nil
		if conflict, err := rebaseInOwner(dir, state, s, sf, name, newBase, oldBase); err != nil {
			return moved, conflict, err
		}
		state.ConflictBranch, state.ConflictType = "", "cascade"
		if err := saveProgress(dir, state, s, sf); err != nil {
			return moved, nil, err
		}
		cfg.Successf("Rebased %s onto %s", name, newBase)
		moved++
	}
	return moved, nil, nil
}

func rebaseInOwner(dir string, state *StateFile, s *stack.Stack, sf *stack.StackFile, name, newBase, oldBase string) (*modifyview.ConflictInfo, error) {
	state.ConflictBranch, state.ConflictType = name, "rebase"
	_, err := startRefMutation(dir, state, name)
	if err != nil {
		return nil, err
	}
	ops, err := state.Worktrees.Prepare(name)
	if err != nil {
		state.Phase, state.ConflictType = PhaseConflict, "rebase_start"
		return nil, errors.Join(err, saveProgress(dir, state, s, sf))
	}
	if err := ops.RebaseOnto(newBase, oldBase, name, git.RebaseOpts{}); err != nil {
		state.Phase = PhaseConflict
		if git.IsRebaseStartError(err) {
			state.ConflictType = "rebase_start"
		}
		if saveErr := saveNativeConflict(dir, state, s, sf, ops); saveErr != nil {
			return nil, errors.Join(err, saveErr)
		}
		if git.IsRebaseStartError(err) {
			return nil, fmt.Errorf("could not start rebase of %s onto %s in %s: %w", name, newBase, state.Worktrees.Location(name).Path, err)
		}
		files, fileErr := ops.ConflictedFiles()
		if fileErr != nil {
			return nil, errors.Join(err, fmt.Errorf("reading conflicts in %s: %w", state.Worktrees.Location(name).Path, fileErr))
		}
		return &modifyview.ConflictInfo{Branch: name, ConflictedFiles: files},
			fmt.Errorf("rebase conflict on %s in %s", name, state.Worktrees.Location(name).Path)
	}
	if err := state.Worktrees.Record(name); err != nil {
		return nil, err
	}
	state.PendingHead = ""
	return nil, nil
}

// resolveCheckoutBranch determines which branch to check out after a modify
// operation completes. If the user's original branch was dropped, folded, or
// renamed, this returns the most appropriate surviving branch.
func resolveCheckoutBranch(originalBranch string, plan []Action, snapshot Snapshot, s *stack.Stack) string {
	// Check if the original branch is still in the stack — quick exit.
	if s.IndexOf(originalBranch) >= 0 {
		return originalBranch
	}

	// Build a rename map (old name → new name) so we can translate snapshot
	// neighbor names that may have been renamed in the same modify operation.
	renames := make(map[string]string)
	for _, a := range plan {
		if a.Type == "rename" && a.NewName != "" {
			renames[a.Branch] = a.NewName
		}
	}

	// resolvedName returns the post-rename name for a branch, or the
	// original name if it wasn't renamed.
	resolvedName := func(name string) string {
		if newName, ok := renames[name]; ok {
			return newName
		}
		return name
	}

	// Scan the plan for an action that targeted the original branch.
	for _, a := range plan {
		if a.Branch != originalBranch {
			continue
		}

		switch a.Type {
		case "rename":
			if a.NewName != "" && s.IndexOf(a.NewName) >= 0 {
				return a.NewName
			}

		case "fold_down", "fold_up":
			if target := resolvedName(a.Target); target != "" && s.IndexOf(target) >= 0 {
				return target
			}
			// Legacy plans have no compiled receiver. Skip removed neighbors
			// in the fold direction rather than selecting the topmost branch.
			direction := -1
			if a.Type == "fold_up" {
				direction = 1
			}
			for i, branch := range snapshot.Branches {
				if branch.Name != originalBranch {
					continue
				}
				for j := i + direction; j >= 0 && j < len(snapshot.Branches); j += direction {
					target := resolvedName(snapshot.Branches[j].Name)
					if index := s.IndexOf(target); index >= 0 && !s.Branches[index].IsMerged() {
						return target
					}
				}
				break
			}

		case "drop":
			// Prefer the branch that was directly above in the original order,
			// then fall back to the one below.
			if nearest := nearestSurvivingBranch(snapshot, originalBranch, s, resolvedName); nearest != "" {
				return nearest
			}
		}
	}

	// Fallback: topmost branch in the stack.
	if len(s.Branches) > 0 {
		return s.Branches[len(s.Branches)-1].Branch
	}
	return originalBranch
}

// nearestSurvivingBranch finds the closest branch to the dropped branch that
// still exists in the stack. Prefers the branch above (higher index), then below.
// resolvedName translates snapshot names through any renames from the same operation.
func nearestSurvivingBranch(snapshot Snapshot, dropped string, s *stack.Stack, resolvedName func(string) string) string {
	order := make([]string, len(snapshot.Branches))
	for i, bs := range snapshot.Branches {
		order[i] = bs.Name
	}
	raw := stack.NearestSurvivingBranch(order, dropped, func(name string) bool {
		return s.IndexOf(resolvedName(name)) >= 0
	})
	if raw == "" {
		return ""
	}
	return resolvedName(raw)
}

// ContinueApply resumes a modify operation after the user resolves a rebase conflict.
// It finishes the in-progress git rebase, then continues the cascading rebase for
// remaining branches stored in the state file.
func ContinueApply(
	cfg *config.Config,
	gitDir string,
	updateBaseSHAs func(*stack.Stack),
) error {
	state, err := LoadState(gitDir)
	if err != nil {
		return fmt.Errorf("loading modify state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no modify state file found")
	}
	if state.Phase != PhaseConflict {
		return fmt.Errorf("no modify conflict in progress (phase: %s)", state.Phase)
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		return fmt.Errorf("loading stack: %w", err)
	}

	s, err := findStack(state, sf)
	if err != nil {
		return err
	}
	if state.StackBranches != nil && (state.StackName != s.Trunk.Branch || !slices.Equal(state.StackBranches, s.BranchNames())) {
		return fmt.Errorf("the modify catalog update did not complete or the stack changed; run `gh stack modify --abort` to recover")
	}
	for _, branch := range []string{state.ConflictBranch, nativeBranch(state)} {
		if index := s.IndexOf(branch); index >= 0 && s.Branches[index].IsSkipped() {
			return fmt.Errorf("cannot continue modify on merged or queued branch %s; run `gh stack modify --abort`", branch)
		}
	}
	ctx, err := recoveryContext(gitDir, state)
	if err != nil {
		return err
	}
	ops, err := originOps(ctx)
	if err != nil {
		return err
	}
	conflictType := state.ConflictType
	path := ctx.Origin.Path
	if conflictType == "" || conflictType == "rebase" || conflictType == "cherry_pick" {
		nativeOps := ops
		if state.Worktrees != nil {
			nativeOps, path, err = ConflictOps(state)
			if err != nil {
				return err
			}
		}
		inProgress, err := nativeOps.IsRebaseInProgress()
		if err != nil {
			return fmt.Errorf("checking rebase state before continuation: %w", err)
		}
		picking, err := nativeOps.IsCherryPickInProgress()
		if err != nil {
			return fmt.Errorf("checking cherry-pick state before continuation: %w", err)
		}
		if conflictType == "cherry_pick" {
			if !picking {
				return fmt.Errorf("the cherry-pick recorded by modify is no longer in progress in %s; recovery state was retained, run `gh stack modify --abort` to recover", path)
			}
		} else if !inProgress {
			return fmt.Errorf("the rebase recorded by modify is no longer in progress in %s; recovery state was retained, run `gh stack modify --abort` to recover", path)
		}
	}
	existing, err := recoveryBranchAvailability(state, ops)
	if err != nil {
		return err
	}
	if state.Worktrees == nil {
		if err := adoptLegacyContext(state, ctx, ops, existing); err != nil {
			return err
		}
	}
	normalizingFold := state.PendingAction != nil && state.PendingAction.Type == "fold_rebase"
	if state.PendingAction != nil && state.PendingAction.Type != "fold_down" && !normalizingFold {
		return fmt.Errorf("modify was interrupted during %s; run `gh stack modify --abort` to recover", state.PendingAction.Type)
	}
	if normalizingFold {
		if state.DesiredOrder == nil || state.NextAction < 0 || state.NextAction >= len(state.Execution) ||
			state.Execution[state.NextAction] != *state.PendingAction ||
			(conflictType != "rebase" && conflictType != "rebase_start") ||
			state.PendingAction.Branch != nativeBranch(state) {
			return fmt.Errorf("fold normalization does not match the pending action; recovery state was retained")
		}
	}
	resumeActions := state.DesiredOrder != nil && (conflictType == "cherry_pick" || conflictType == "actions" || normalizingFold)
	pending := ""
	if conflictType == "cherry_pick" || conflictType == "rebase" || conflictType == "" {
		ops, path, err = ConflictOps(state)
		if err != nil {
			return err
		}
		if conflictType == "cherry_pick" {
			if state.DesiredOrder != nil {
				if state.NextAction < 0 || state.NextAction >= len(state.Execution) {
					return fmt.Errorf("fold conflict has invalid action progress; recovery state was retained")
				}
				action := state.Execution[state.NextAction]
				if action.Type != "fold_down" || action.Branch != state.FoldBranch || action.Target != state.FoldTarget {
					return fmt.Errorf("fold conflict does not match the pending action; recovery state was retained")
				}
			}
		}
		pending = nativeBranch(state)
		if err := checkPendingHead(state, ops); err != nil {
			return err
		}
		if index := s.IndexOf(nativeBranch(state)); index >= 0 && !normalizingFold {
			if err := checkExpectedRef(state, s.ActiveBaseBranch(nativeBranch(state))); err != nil {
				return err
			}
		}
		if conflictType == "cherry_pick" {
			if err := checkExpectedRef(state, state.FoldBranch); err != nil {
				return err
			}
		}
	}
	if err := preflightBranches(state, remainingTargets(state), pending); err != nil {
		return err
	}
	state.RecordStack(s)
	if err := SaveState(gitDir, state); err != nil {
		return err
	}

	// Check the conflict branch itself
	if idx := s.IndexOf(state.ConflictBranch); idx >= 0 && s.Branches[idx].PullRequest != nil {
		state.AffectsPRs = true
	}

	remainingBranches := append([]string{}, state.RemainingBranches...)

	// Finish the in-progress git operation, or resume at a rebase that was
	// previously refused before it could start.
	switch state.ConflictType {
	case "cherry_pick":
		if err := ops.CherryPickContinue(); err != nil {
			return errors.Join(fmt.Errorf("cherry-pick continue failed in %s — resolve remaining conflicts and try again: %w", path, err),
				saveNativeConflict(gitDir, state, s, sf, ops))
		}
		if err := ctx.Record(state.FoldTarget); err != nil {
			return err
		}
		cfg.Successf("Folded %s into %s", state.FoldBranch, state.FoldTarget)

		// Remove the folded branch from stack metadata
		foldIdx := s.IndexOf(state.FoldBranch)
		if foldIdx >= 0 && foldIdx < len(s.Branches) {
			s.Branches = append(s.Branches[:foldIdx], s.Branches[foldIdx+1:]...)
		}
		if state.DesiredOrder != nil {
			state.NextAction++
		}
	case "", "rebase":
		// Rebase conflict
		if err := ops.RebaseContinue(git.RebaseOpts{}); err != nil {
			return errors.Join(fmt.Errorf("rebase continue failed in %s — resolve remaining conflicts and try again: %w", path, err),
				saveNativeConflict(gitDir, state, s, sf, ops))
		}
		if err := ctx.Record(state.ConflictBranch); err != nil {
			return err
		}
		if normalizingFold {
			state.NextAction++
		}
		cfg.Successf("Rebased %s", state.ConflictBranch)
	case "rebase_start":
		if !normalizingFold {
			remainingBranches = append([]string{state.ConflictBranch}, remainingBranches...)
		}
	case "actions", "cascade":
	default:
		return fmt.Errorf("unknown modify conflict type %q", state.ConflictType)
	}

	state.ConflictBranch, state.ConflictType = "", "cascade"
	state.PendingAction, state.PendingHead = nil, ""
	state.RemainingBranches = remainingBranches
	if resumeActions {
		state.ConflictType = "actions"
	}
	if err := saveProgress(gitDir, state, s, sf); err != nil {
		return err
	}
	if resumeActions {
		result := &modifyview.ApplyResult{Success: true}
		if conflict, err := runActions(cfg, gitDir, state, s, sf, result); err != nil {
			printContinueConflict(cfg, state, conflict)
			return err
		}
		remainingBranches = state.RemainingBranches
	}
	if _, conflict, err := rebaseRemaining(cfg, gitDir, state, s, sf, remainingBranches); err != nil {
		printContinueConflict(cfg, state, conflict)
		return err
	}
	if err := checkResultRefs(state, s); err != nil {
		return err
	}
	if err := restoreCheckout(cfg, state, s, false); err != nil {
		return err
	}

	// Update base SHAs
	updateBaseSHAs(s)

	// Transition to pending_submit only when PRs are affected
	needsSubmit := s.ID != "" && state.AffectsPRs
	if err := finishApply(gitDir, state, s, sf, needsSubmit); err != nil {
		return err
	}

	cfg.Successf("Stack modified successfully")
	if needsSubmit {
		cfg.Printf("")
		cfg.Printf("Run `%s` to push your changes and update the stack of PRs on GitHub",
			cfg.ColorCyan("gh stack submit"))
	}
	return nil
}

func printContinueConflict(cfg *config.Config, state *StateFile, conflict *modifyview.ConflictInfo) {
	if conflict == nil {
		return
	}
	path := state.Worktrees.Location(nativeBranch(state)).Path
	cfg.Warningf("Conflict applying %s in %s", conflict.Branch, path)
	for _, file := range conflict.ConflictedFiles {
		cfg.Printf("  %s", file)
	}
	cfg.Printf("")
	cfg.Printf("Resolve the conflicts in %s, stage with `%s`, then run `%s`",
		path, cfg.ColorCyan("git add <file>"), cfg.ColorCyan("gh stack modify --continue"))
	cfg.Printf("Or restore the stack with `%s`", cfg.ColorCyan("gh stack modify --abort"))
}

// Unwind restores the stack to its pre-modify state using the snapshot.
// stackIndex is retained for legacy callers, but is never used as an identity.
func Unwind(cfg *config.Config, gitDir string, snapshot Snapshot, stackIndex int, sf *stack.StackFile, plan []Action) error {
	state, err := LoadState(gitDir)
	if err != nil {
		return err
	}
	if state == nil {
		state = &StateFile{
			SchemaVersion: 1, StackIndex: stackIndex, Phase: PhaseApplying,
			Snapshot: snapshot, Plan: plan,
		}
	} else {
		var original stack.Stack
		if err := json.Unmarshal(snapshot.StackMetadata, &original); err != nil {
			return fmt.Errorf("reading recovery snapshot: %w", err)
		}
		if !MatchesStack(&StateFile{Snapshot: state.Snapshot}, &original) {
			return fmt.Errorf("modify journal belongs to a different stack; recovery state was retained")
		}
	}
	return unwindState(cfg, gitDir, state, sf)
}

// UnwindFromStateFile restores the stack from a modify state file (for --abort).
func UnwindFromStateFile(cfg *config.Config, gitDir string) error {
	state, err := LoadState(gitDir)
	if err != nil {
		return fmt.Errorf("loading modify state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("no modify state file found")
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		return fmt.Errorf("loading stack: %w", err)
	}

	return unwindState(cfg, gitDir, state, sf)
}
