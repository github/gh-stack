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
	}
	stateFile.RecordStack(s)

	result := &modifyview.ApplyResult{Success: true}

	// Track whether any action affects a branch with a PR.
	affectsPRs := false

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
	if err := SaveState(gitDir, stateFile); err != nil {
		return nil, nil, fmt.Errorf("saving modify state: %w", err)
	}
	rollback := func(cause error) error {
		return errors.Join(cause, unwindState(cfg, gitDir, stateFile, sf))
	}

	// Step 1: Renames
	for i, n := range nodes {
		if n.PendingAction != nil && n.PendingAction.Type == modifyview.ActionRename {
			oldName := n.Ref.Branch
			newName := n.PendingAction.NewName
			ops, err := prepareMutation(stateFile)
			if err != nil {
				return nil, nil, err
			}
			exists, err := ops.BranchExists(newName)
			if err != nil {
				return nil, nil, fmt.Errorf("checking rename target %s: %w", newName, err)
			}
			if exists {
				return nil, nil, rollback(fmt.Errorf("cannot rename %s to %s: branch already exists", oldName, newName))
			}
			stateFile.PendingAction = &Action{Type: "rename", Branch: oldName, NewName: newName}
			ops, err = startRefMutation(gitDir, stateFile, oldName)
			if err != nil {
				return nil, nil, err
			}
			if err := ops.RenameBranch(oldName, newName); err != nil {
				return nil, nil, rollback(fmt.Errorf("renaming %s to %s: %w", oldName, newName, err))
			}
			ctx.Rename(oldName, newName)
			stateFile.RenamedBranches[oldName] = newName
			stateFile.PendingAction = nil
			if err := ctx.Record(newName); err != nil {
				return nil, nil, err
			}

			// Update in-memory state
			idx := s.IndexOf(oldName)
			if idx >= 0 {
				// Update originalRefs key
				if sha, ok := originalRefs[oldName]; ok {
					originalRefs[newName] = sha
					delete(originalRefs, oldName)
				}
				// Update originalParentTips key
				if sha, ok := originalParentTips[oldName]; ok {
					originalParentTips[newName] = sha
					delete(originalParentTips, oldName)
				}
				s.Branches[idx].Branch = newName
			}
			// Update the node's ref for later steps
			nodes[i].Ref.Branch = newName

			result.RenamedBranches = append(result.RenamedBranches, modifyview.RenamedBranch{
				OldName: oldName,
				NewName: newName,
			})
			if n.Ref.PullRequest != nil {
				affectsPRs = true
			}
			stateFile.AffectsPRs = affectsPRs
			if err := saveProgress(gitDir, stateFile, s, sf); err != nil {
				return nil, nil, err
			}
			cfg.Successf("Renamed %s → %s", oldName, newName)
		}
	}

	// Step 2: Inserts — create new branches and add to stack metadata.
	// Process in order so positions are stable. The node's position in the
	// non-removed list determines the parent branch.
	for _, n := range nodes {
		if n.PendingAction == nil {
			continue
		}
		if n.PendingAction.Type != modifyview.ActionInsertBelow && n.PendingAction.Type != modifyview.ActionInsertAbove {
			continue
		}

		newName := n.PendingAction.NewName

		// Determine the parent branch: find the position of this node among
		// the non-removed, non-merged nodes in the apply-order list, then
		// look at the branch just before it (toward trunk).
		var parentBranch string
		insertPos := -1

		// Determine where in s.Branches the new branch should go.
		// Walk the non-removed nodes to find the relative position.
		nonRemovedPos := 0
		for _, other := range nodes {
			if other.Removed || other.Ref.IsMerged() {
				continue
			}
			if other.Ref.Branch == newName {
				insertPos = nonRemovedPos
				break
			}
			nonRemovedPos++
		}

		if insertPos <= 0 {
			parentBranch = s.Trunk.Branch
		} else {
			// Find the branch at insertPos-1 among active branches
			activeCount := 0
			for _, b := range s.Branches {
				if b.IsMerged() {
					continue
				}
				if activeCount == insertPos-1 {
					parentBranch = b.Branch
					break
				}
				activeCount++
			}
			if parentBranch == "" {
				parentBranch = s.Trunk.Branch
			}
		}

		ops, err := prepareMutation(stateFile)
		if err != nil {
			return nil, nil, err
		}
		exists, err := ops.BranchExists(newName)
		if err != nil {
			return nil, nil, fmt.Errorf("checking insert target %s: %w", newName, err)
		}
		if exists {
			return nil, nil, rollback(fmt.Errorf("cannot insert %s: branch already exists", newName))
		}
		parentSHA, err := ops.RevParse(parentBranch)
		if err != nil {
			return nil, nil, rollback(fmt.Errorf("resolving insert parent %s: %w", parentBranch, err))
		}
		stateFile.PendingAction = &Action{Type: string(n.PendingAction.Type), Branch: n.Ref.Branch, NewName: newName}
		if err := SaveState(gitDir, stateFile); err != nil {
			return nil, nil, err
		}
		if err := ops.CreateBranch(newName, parentSHA); err != nil {
			return nil, nil, rollback(fmt.Errorf("creating branch %s from %s: %w", newName, parentBranch, err))
		}
		stateFile.CreatedBranches[newName] = parentSHA
		stateFile.PendingAction = nil
		if err := ctx.Record(newName); err != nil {
			return nil, nil, err
		}

		// Insert BranchRef into s.Branches at the correct position
		newRef := stack.BranchRef{Branch: newName}
		targetIdx := len(s.Branches) // default: append at end
		if insertPos >= 0 {
			// Map the active position back to s.Branches index
			activeCount := 0
			for j, b := range s.Branches {
				if b.IsMerged() {
					continue
				}
				if activeCount == insertPos {
					targetIdx = j
					break
				}
				activeCount++
			}
		}
		s.Branches = append(s.Branches, stack.BranchRef{})
		copy(s.Branches[targetIdx+1:], s.Branches[targetIdx:])
		s.Branches[targetIdx] = newRef

		// Check if the branch above the insertion point has a PR —
		// its base changes, so we need a submit
		if targetIdx < len(s.Branches)-1 {
			above := s.Branches[targetIdx+1]
			if above.PullRequest != nil {
				affectsPRs = true
			}
		}

		result.InsertedBranches = append(result.InsertedBranches, newName)
		stateFile.AffectsPRs = affectsPRs
		if err := saveProgress(gitDir, stateFile, s, sf); err != nil {
			return nil, nil, err
		}
		cfg.Successf("Inserted %s after %s", newName, parentBranch)
	}

	// Step 3: Folds — absorb one branch's commits into an adjacent branch.
	//
	// Fold-down: cherry-pick the folded branch's commits onto the target below.
	//   The target is below in the stack (closer to trunk), so it doesn't
	//   contain the folded branch's commits. Cherry-pick adds them.
	//
	// Fold-up: the target (above) already contains the folded branch's commits
	//   in its ancestry (it's stacked on top). Instead of cherry-picking, we
	//   adjust originalParentTips so the cascading rebase replays both the
	//   folded branch's commits AND the target's own commits when rebasing
	//   the target onto the folded branch's base.
	for _, n := range nodes {
		if n.PendingAction == nil {
			continue
		}
		if n.PendingAction.Type != modifyview.ActionFoldDown && n.PendingAction.Type != modifyview.ActionFoldUp {
			continue
		}

		foldBranch := n.Ref.Branch

		// Determine target branch
		var targetBranch string
		foldIdx := s.IndexOf(foldBranch)
		if foldIdx < 0 {
			continue
		}

		if n.PendingAction.Type == modifyview.ActionFoldDown {
			// Target is the branch below (toward trunk)
			if foldIdx == 0 {
				continue
			}
			targetBranch = s.Branches[foldIdx-1].Branch
		} else {
			// Target is the branch above (away from trunk)
			if foldIdx >= len(s.Branches)-1 {
				continue
			}
			targetBranch = s.Branches[foldIdx+1].Branch
		}

		baseBranch := s.ActiveBaseBranch(foldBranch)

		// Check if fold source or target has a PR
		if n.Ref.PullRequest != nil {
			affectsPRs = true
		}
		targetIdx := s.IndexOf(targetBranch)
		if targetIdx >= 0 && s.Branches[targetIdx].PullRequest != nil {
			affectsPRs = true
		}

		if n.PendingAction.Type == modifyview.ActionFoldDown {
			// Fold-down: cherry-pick the folded branch's commits onto the target.
			commits, err := git.LogRange(baseBranch, foldBranch)
			if err != nil {
				return nil, nil, rollback(fmt.Errorf("reading commits to fold from %s: %w", foldBranch, err))
			}
			if len(commits) == 0 {
				cfg.Printf("No commits to fold from %s", foldBranch)
			} else {
				stateFile.ConflictBranch = foldBranch
				stateFile.ConflictType = "cherry_pick"
				stateFile.FoldBranch, stateFile.FoldTarget = foldBranch, targetBranch
				stateFile.AffectsPRs = affectsPRs
				ops, err := startRefMutation(gitDir, stateFile, targetBranch)
				if err != nil {
					return nil, nil, err
				}
				if err := ops.CheckoutBranch(targetBranch); err != nil {
					return nil, nil, rollback(fmt.Errorf("checking out %s for fold: %w", targetBranch, err))
				}

				shas := make([]string, len(commits))
				for i, c := range commits {
					shas[len(commits)-1-i] = c.SHA
				}

				if err := ops.CherryPick(shas); err != nil {
					conflict := &modifyview.ConflictInfo{Branch: foldBranch}
					files, fileErr := ops.ConflictedFiles()
					conflict.ConflictedFiles = files

					// Compute remaining branches for cascading rebase after cherry-pick resumes.
					// Since folds happen before cascading rebase (Step 5), all non-merged, non-folded
					// branches need rebasing.
					remaining := make([]string, 0)
					for _, br := range s.Branches {
						if !br.IsMerged() && br.Branch != foldBranch {
							remaining = append(remaining, br.Branch)
						}
					}

					// Save conflict state so --continue can resume the cherry-pick
					stateFile.Phase = PhaseConflict
					stateFile.ConflictBranch = foldBranch
					stateFile.ConflictType = "cherry_pick"
					stateFile.FoldBranch = foldBranch
					stateFile.FoldTarget = targetBranch
					stateFile.RemainingBranches = remaining
					stateFile.AffectsPRs = affectsPRs
					if saveErr := saveProgress(gitDir, stateFile, s, sf); saveErr != nil {
						return nil, nil, errors.Join(err, saveErr)
					}
					if fileErr != nil {
						return nil, nil, errors.Join(err, fmt.Errorf("reading conflicts in %s: %w", ctx.Origin.Path, fileErr))
					}

					return nil, conflict, fmt.Errorf("cherry-pick conflict folding %s into %s in %s", foldBranch, targetBranch, ctx.Origin.Path)
				}
				if err := ctx.Record(targetBranch); err != nil {
					return nil, nil, err
				}

				cfg.Successf("Folded %s into %s (%d commits)", foldBranch, targetBranch, len(commits))
			}
		} else {
			// Fold-up: the target (above) already has the folded branch's
			// commits in its history. We adjust originalParentTips so the
			// cascading rebase uses the folded branch's BASE as the cutoff,
			// replaying both the folded branch's commits and the target's
			// own commits onto the new parent.
			originalParentTips[targetBranch] = originalParentTips[foldBranch]
			cfg.Successf("Folded %s into %s", foldBranch, targetBranch)
		}

		// Remove folded branch from stack metadata
		foldIdx = s.IndexOf(foldBranch) // re-resolve in case earlier folds shifted indices
		if foldIdx >= 0 && foldIdx < len(s.Branches) {
			s.Branches = append(s.Branches[:foldIdx], s.Branches[foldIdx+1:]...)
		}
		stateFile.AffectsPRs = affectsPRs
		if err := saveProgress(gitDir, stateFile, s, sf); err != nil {
			return nil, nil, err
		}
	}

	// Step 4: Drops — remove from stack metadata
	// Process in reverse order to preserve indices
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if n.PendingAction == nil || n.PendingAction.Type != modifyview.ActionDrop {
			continue
		}

		dropBranch := n.Ref.Branch
		dropIdx := s.IndexOf(dropBranch)
		if dropIdx < 0 {
			continue
		}

		if n.Ref.PullRequest != nil && n.Ref.PullRequest.Number > 0 {
			result.DroppedPRs = append(result.DroppedPRs, modifyview.DroppedPR{
				Branch:   dropBranch,
				PRNumber: n.Ref.PullRequest.Number,
			})
			affectsPRs = true
		}

		s.Branches = append(s.Branches[:dropIdx], s.Branches[dropIdx+1:]...)
		stateFile.AffectsPRs = affectsPRs
		if err := saveProgress(gitDir, stateFile, s, sf); err != nil {
			return nil, nil, err
		}
		cfg.Successf("Dropped %s from stack", dropBranch)
	}

	// Step 5: Reorder — build the desired branch order from the remaining nodes
	desiredOrder := make([]string, 0)
	for _, n := range nodes {
		if n.Removed {
			continue
		}
		if n.PendingAction != nil && (n.PendingAction.Type == modifyview.ActionDrop ||
			n.PendingAction.Type == modifyview.ActionFoldDown ||
			n.PendingAction.Type == modifyview.ActionFoldUp) {
			continue
		}
		if n.Ref.IsMerged() {
			continue // Merged branches keep their position
		}
		desiredOrder = append(desiredOrder, n.Ref.Branch)
	}

	// Check if reorder is needed by comparing with current stack order
	currentOrder := make([]string, 0)
	for _, b := range s.Branches {
		if !b.IsMerged() {
			currentOrder = append(currentOrder, b.Branch)
		}
	}

	needsReorder := false
	if len(desiredOrder) == len(currentOrder) {
		for i := range desiredOrder {
			if desiredOrder[i] != currentOrder[i] {
				needsReorder = true
				break
			}
		}
	} else {
		needsReorder = true
	}

	// Rebuild s.Branches in the desired order, preserving merged branches
	// at their original positions.
	if needsReorder {
		// Build a queue of active branches in the desired order
		desiredIdx := 0
		branchMap := make(map[string]stack.BranchRef)
		for _, b := range s.Branches {
			branchMap[b.Branch] = b
		}

		newBranches := make([]stack.BranchRef, 0, len(s.Branches))
		for _, b := range s.Branches {
			if b.IsMerged() {
				// Merged branches stay at their original position
				newBranches = append(newBranches, b)
			} else {
				// Substitute the next active branch from the desired order
				if desiredIdx < len(desiredOrder) {
					if sub, ok := branchMap[desiredOrder[desiredIdx]]; ok {
						newBranches = append(newBranches, sub)
					}
					desiredIdx++
				}
			}
		}

		s.Branches = newBranches
		if err := saveProgress(gitDir, stateFile, s, sf); err != nil {
			return nil, nil, err
		}
	}

	// Step 6: Replay each active branch's original commit range onto its new parent.
	moved, conflict, err := rebaseRemaining(cfg, gitDir, stateFile, s, sf, s.BranchNames())
	if err != nil {
		return nil, conflict, err
	}
	result.MovedBranches = moved

	// Check out the best branch — the original if it's still in the stack,
	// otherwise the nearest surviving branch.
	targetBranch := resolveCheckoutBranch(currentBranch, plan, snapshot, s)
	if err := ctx.RestoreOrigin(targetBranch); err != nil {
		return nil, nil, err
	}
	if targetBranch != currentBranch {
		cfg.Printf("Switched to %s (original branch %s is no longer in the stack)", targetBranch, currentBranch)
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
		ops, err := state.Worktrees.OriginOps()
		if err != nil {
			return moved, nil, err
		}
		newBase := s.ActiveBaseBranch(name)
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
				continue
			}
		}
		state.ConflictBranch = name
		state.ConflictType = "rebase"
		state.RemainingBranches = append([]string{}, branches[i+1:]...)
		state.AffectsPRs = state.AffectsPRs || branch.PullRequest != nil
		ops, err = startRefMutation(dir, state, name)
		if err != nil {
			return moved, nil, err
		}
		if err := ops.RebaseOnto(newBase, oldBase, name, git.RebaseOpts{}); err != nil {
			state.Phase = PhaseConflict
			if git.IsRebaseStartError(err) {
				state.ConflictType = "rebase_start"
			}
			if saveErr := saveProgress(dir, state, s, sf); saveErr != nil {
				return moved, nil, errors.Join(err, saveErr)
			}
			if git.IsRebaseStartError(err) {
				return moved, nil, fmt.Errorf("could not start rebase of %s onto %s in %s: %w", name, newBase, state.Worktrees.Origin.Path, err)
			}
			files, fileErr := ops.ConflictedFiles()
			if fileErr != nil {
				return moved, nil, errors.Join(err, fmt.Errorf("reading conflicts in %s: %w", state.Worktrees.Origin.Path, fileErr))
			}
			return moved, &modifyview.ConflictInfo{Branch: name, ConflictedFiles: files},
				fmt.Errorf("rebase conflict on %s in %s", name, state.Worktrees.Origin.Path)
		}
		if err := state.Worktrees.Record(name); err != nil {
			return moved, nil, err
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

		case "fold_down":
			// Fold-down merges into the branch below in the original order.
			if target := adjacentSnapshotBranch(snapshot, originalBranch, -1); target != "" {
				resolved := resolvedName(target)
				if s.IndexOf(resolved) >= 0 {
					return resolved
				}
			}

		case "fold_up":
			// Fold-up merges into the branch above in the original order.
			if target := adjacentSnapshotBranch(snapshot, originalBranch, +1); target != "" {
				resolved := resolvedName(target)
				if s.IndexOf(resolved) >= 0 {
					return resolved
				}
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

// adjacentSnapshotBranch returns the branch adjacent to target in the snapshot.
// direction -1 means below (toward trunk), +1 means above (away from trunk).
func adjacentSnapshotBranch(snapshot Snapshot, target string, direction int) string {
	for i, bs := range snapshot.Branches {
		if bs.Name == target {
			adj := i + direction
			if adj >= 0 && adj < len(snapshot.Branches) {
				return snapshot.Branches[adj].Name
			}
			return ""
		}
	}
	return ""
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
	ctx, err := recoveryContext(gitDir, state)
	if err != nil {
		return err
	}
	ops, err := ctx.OriginOps()
	if err != nil {
		return err
	}
	inProgress, err := ops.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state before continuation: %w", err)
	}
	picking, err := ops.IsCherryPickInProgress()
	if err != nil {
		return fmt.Errorf("checking cherry-pick state before continuation: %w", err)
	}
	switch state.ConflictType {
	case "", "rebase":
		if !inProgress {
			return fmt.Errorf("the rebase recorded by modify is no longer in progress in %s; recovery state was retained, run `gh stack modify --abort` to recover", ctx.Origin.Path)
		}
	case "cherry_pick":
		if !picking {
			return fmt.Errorf("the cherry-pick recorded by modify is no longer in progress in %s; recovery state was retained, run `gh stack modify --abort` to recover", ctx.Origin.Path)
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
			return fmt.Errorf("cherry-pick continue failed in %s — resolve remaining conflicts and try again: %w", ctx.Origin.Path, err)
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
	case "", "rebase":
		// Rebase conflict
		if err := ops.RebaseContinue(git.RebaseOpts{}); err != nil {
			return fmt.Errorf("rebase continue failed in %s — resolve remaining conflicts and try again: %w", ctx.Origin.Path, err)
		}
		if err := ctx.Record(state.ConflictBranch); err != nil {
			return err
		}
		cfg.Successf("Rebased %s", state.ConflictBranch)
	case "rebase_start":
		remainingBranches = append([]string{state.ConflictBranch}, remainingBranches...)
	case "cascade":
	default:
		return fmt.Errorf("unknown modify conflict type %q", state.ConflictType)
	}

	state.ConflictBranch, state.ConflictType = "", "cascade"
	state.RemainingBranches = remainingBranches
	if err := saveProgress(gitDir, state, s, sf); err != nil {
		return err
	}
	if _, conflict, err := rebaseRemaining(cfg, gitDir, state, s, sf, remainingBranches); err != nil {
		if conflict != nil {
			cfg.Warningf("Conflict rebasing %s in %s", conflict.Branch, ctx.Origin.Path)
			for _, file := range conflict.ConflictedFiles {
				cfg.Printf("  %s", file)
			}
			cfg.Printf("")
			cfg.Printf("Resolve the conflicts in %s, stage with `%s`, then run `%s`",
				ctx.Origin.Path,
				cfg.ColorCyan("git add <file>"),
				cfg.ColorCyan("gh stack modify --continue"))
			cfg.Printf("Or restore the stack with `%s`",
				cfg.ColorCyan("gh stack modify --abort"))
		}
		return err
	}
	// All rebases done — check out the best branch
	if state.OriginalBranch != "" {
		targetBranch := resolveCheckoutBranch(state.OriginalBranch, state.Plan, state.Snapshot, s)
		if err := ctx.RestoreOrigin(targetBranch); err != nil {
			return err
		}
		if targetBranch != state.OriginalBranch {
			cfg.Printf("Switched to %s (original branch %s is no longer in the stack)", targetBranch, state.OriginalBranch)
		}
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
