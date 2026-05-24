package modify

import (
	"encoding/json"
	"fmt"
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

		if n.Removed {
			continue // Removed nodes are handled by their pending action
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

		if !n.IsInserted && n.OriginalPosition != i && n.PendingAction == nil {
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
	// Build the snapshot before any changes
	snapshot, err := BuildSnapshot(s)
	if err != nil {
		return nil, nil, fmt.Errorf("building snapshot: %w", err)
	}

	// Acquire the stack lock before making any changes
	lock, err := stack.Lock(gitDir)
	if err != nil {
		return nil, nil, fmt.Errorf("acquiring stack lock: %w", err)
	}
	defer lock.Unlock()

	plan := BuildPlan(nodes)

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
	}
	if err := SaveState(gitDir, stateFile); err != nil {
		return nil, nil, fmt.Errorf("saving modify state: %w", err)
	}

	result := &modifyview.ApplyResult{Success: true}

	// Track whether any action affects a branch with a PR.
	affectsPRs := false
	// Collect original refs for rebase --onto, including trunk
	branchNames := make([]string, 0, len(s.Branches)+1)
	branchNames = append(branchNames, s.Trunk.Branch)
	for _, b := range s.Branches {
		if !b.IsMerged() && git.BranchExists(b.Branch) {
			branchNames = append(branchNames, b.Branch)
		}
	}
	originalRefs, err := git.RevParseMap(branchNames)
	if err != nil {
		// Unwind on failure
		unwindErr := Unwind(cfg, gitDir, snapshot, stackIndex, sf, plan)
		if unwindErr != nil {
			return nil, nil, fmt.Errorf("failed to resolve refs (%v) and unwind failed (%v)", err, unwindErr)
		}
		return nil, nil, fmt.Errorf("failed to resolve branch SHAs: %w", err)
	}

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

	// Step 1: Renames
	for i, n := range nodes {
		if n.PendingAction != nil && n.PendingAction.Type == modifyview.ActionRename {
			oldName := n.Ref.Branch
			newName := n.PendingAction.NewName
			if err := git.RenameBranch(oldName, newName); err != nil {
				unwindErr := Unwind(cfg, gitDir, snapshot, stackIndex, sf, plan)
				if unwindErr != nil {
					return nil, nil, fmt.Errorf("rename failed (%v) and unwind failed (%v)", err, unwindErr)
				}
				return nil, nil, fmt.Errorf("renaming %s to %s: %w", oldName, newName, err)
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

		// Create the git branch at the parent's tip
		if err := git.CreateBranch(newName, parentBranch); err != nil {
			unwindErr := Unwind(cfg, gitDir, snapshot, stackIndex, sf, plan)
			if unwindErr != nil {
				return nil, nil, fmt.Errorf("creating branch %s failed (%v) and unwind failed (%v)", newName, err, unwindErr)
			}
			return nil, nil, fmt.Errorf("creating branch %s from %s: %w", newName, parentBranch, err)
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
			if err != nil || len(commits) == 0 {
				cfg.Printf("No commits to fold from %s", foldBranch)
			} else {
				if err := git.CheckoutBranch(targetBranch); err != nil {
					unwindErr := Unwind(cfg, gitDir, snapshot, stackIndex, sf, plan)
					if unwindErr != nil {
						return nil, nil, fmt.Errorf("checkout failed (%v) and unwind failed (%v)", err, unwindErr)
					}
					return nil, nil, fmt.Errorf("checking out %s for fold: %w", targetBranch, err)
				}

				shas := make([]string, len(commits))
				for i, c := range commits {
					shas[len(commits)-1-i] = c.SHA
				}

				git.CherryPickAbort()

				if err := git.CherryPick(shas); err != nil {
					conflict := &modifyview.ConflictInfo{Branch: foldBranch}
					if files, ferr := git.ConflictedFiles(); ferr == nil {
						conflict.ConflictedFiles = files
					}

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
					stateFile.OriginalBranch = currentBranch
					stateFile.OriginalRefs = originalParentTips
					stateFile.AffectsPRs = affectsPRs
					if saveErr := SaveState(gitDir, stateFile); saveErr != nil {
						cfg.Warningf("failed to save conflict state: %v", saveErr)
					}

					// Save stack metadata so far
					if saveErr := stack.SaveWithLock(gitDir, sf, lock); saveErr != nil {
						cfg.Warningf("failed to save stack metadata: %v", saveErr)
					}

					return nil, conflict, fmt.Errorf("cherry-pick conflict folding %s into %s", foldBranch, targetBranch)
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
	}

	// Step 6: Cascading rebase — rebase each active branch onto its new parent.
	// Use the original parent tip SHA as the oldBase for --onto, so that only
	// the branch's own commits are replayed onto the new parent.
	for i, b := range s.Branches {
		if b.IsMerged() {
			continue
		}

		var newBase string
		if i == 0 {
			newBase = s.Trunk.Branch
		} else {
			newBase = s.ActiveBaseBranch(b.Branch)
		}

		// Use the branch's original parent tip as the oldBase for --onto.
		// This ensures we replay only this branch's unique commits.
		oldBase, hasOldBase := originalParentTips[b.Branch]
		if !hasOldBase {
			// No original parent recorded — try merge-base as fallback
			if mb, mberr := git.MergeBase(newBase, b.Branch); mberr == nil {
				oldBase = mb
			} else {
				continue
			}
		}

		// Check if rebase is actually needed
		isAnc, ancErr := git.IsAncestor(newBase, b.Branch)
		if ancErr == nil && isAnc {
			if mb, mberr := git.MergeBase(newBase, b.Branch); mberr == nil && mb == oldBase {
				continue // No rebase needed
			}
		}

		if err := git.RebaseOnto(newBase, oldBase, b.Branch, git.RebaseOpts{}); err != nil {
			conflict := &modifyview.ConflictInfo{
				Branch: b.Branch,
			}
			if files, ferr := git.ConflictedFiles(); ferr == nil {
				conflict.ConflictedFiles = files
			}

			if b.PullRequest != nil {
				affectsPRs = true
			}

			// Save conflict state so --continue can resume
			remaining := make([]string, 0)
			for j := i + 1; j < len(s.Branches); j++ {
				if !s.Branches[j].IsMerged() {
					remaining = append(remaining, s.Branches[j].Branch)
				}
			}
			stateFile.Phase = PhaseConflict
			stateFile.ConflictBranch = b.Branch
			stateFile.ConflictType = "rebase"
			stateFile.RemainingBranches = remaining
			stateFile.OriginalBranch = currentBranch
			stateFile.OriginalRefs = originalParentTips
			stateFile.AffectsPRs = affectsPRs
			if saveErr := SaveState(gitDir, stateFile); saveErr != nil {
				cfg.Warningf("failed to save conflict state: %v", saveErr)
			}

			// Save stack metadata so far (renames, folds, drops already applied)
			if saveErr := stack.SaveWithLock(gitDir, sf, lock); saveErr != nil {
				cfg.Warningf("failed to save stack metadata: %v", saveErr)
			}

			return nil, conflict, fmt.Errorf("rebase conflict on %s", b.Branch)
		}

		cfg.Successf("Rebased %s onto %s", b.Branch, newBase)
		if b.PullRequest != nil {
			affectsPRs = true
		}
		result.MovedBranches++
	}

	// Check out the best branch — the original if it's still in the stack,
	// otherwise the nearest surviving branch.
	targetBranch := resolveCheckoutBranch(currentBranch, plan, snapshot, s)
	if err := git.CheckoutBranch(targetBranch); err == nil {
		if targetBranch != currentBranch {
			cfg.Printf("Switched to %s (original branch %s is no longer in the stack)", targetBranch, currentBranch)
		}
	}

	// Update base SHAs
	updateBaseSHAs(s)

	// Update state file phase — only require submit when PRs are affected
	result.NeedsSubmit = s.ID != "" && affectsPRs
	if result.NeedsSubmit {
		stateFile.Phase = PhasePendingSubmit
		if err := SaveState(gitDir, stateFile); err != nil {
			cfg.Warningf("failed to update modify state: %s", err)
		}
	}

	// Save stack metadata — this must succeed since git refs have been rewritten
	if err := stack.SaveWithLock(gitDir, sf, lock); err != nil {
		return nil, nil, fmt.Errorf("saving stack metadata: %w", err)
	}

	// Clear state after metadata save succeeds to preserve --abort recovery
	if !result.NeedsSubmit {
		ClearState(gitDir)
	}

	return result, nil, nil
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
	pos := -1
	for i, bs := range snapshot.Branches {
		if bs.Name == dropped {
			pos = i
			break
		}
	}
	if pos < 0 {
		return ""
	}

	// Search above first (higher indices = away from trunk)
	for i := pos + 1; i < len(snapshot.Branches); i++ {
		name := resolvedName(snapshot.Branches[i].Name)
		if s.IndexOf(name) >= 0 {
			return name
		}
	}
	// Then below (lower indices = toward trunk)
	for i := pos - 1; i >= 0; i-- {
		name := resolvedName(snapshot.Branches[i].Name)
		if s.IndexOf(name) >= 0 {
			return name
		}
	}
	return ""
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

	// Acquire lock for the duration of the operation
	lock, err := stack.Lock(gitDir)
	if err != nil {
		return fmt.Errorf("acquiring stack lock: %w", err)
	}
	defer lock.Unlock()

	// Find the stack using the saved index for reliable identification.
	var s *stack.Stack
	if state.StackIndex >= 0 && state.StackIndex < len(sf.Stacks) {
		s = &sf.Stacks[state.StackIndex]
	}
	if s == nil {
		return fmt.Errorf("stack at index %d not found (stack file may have changed)", state.StackIndex)
	}

	// Carry forward whether any prior actions already affected PRs
	affectsPRs := state.AffectsPRs

	// Check the conflict branch itself
	if idx := s.IndexOf(state.ConflictBranch); idx >= 0 && s.Branches[idx].PullRequest != nil {
		affectsPRs = true
	}

	// Finish the in-progress git operation (rebase or cherry-pick)
	if state.ConflictType == "cherry_pick" {
		if err := git.CherryPickContinue(); err != nil {
			return fmt.Errorf("cherry-pick continue failed — resolve remaining conflicts and try again: %w", err)
		}
		cfg.Successf("Folded %s into %s", state.FoldBranch, state.FoldTarget)

		// Remove the folded branch from stack metadata
		foldIdx := s.IndexOf(state.FoldBranch)
		if foldIdx >= 0 && foldIdx < len(s.Branches) {
			s.Branches = append(s.Branches[:foldIdx], s.Branches[foldIdx+1:]...)
		}
	} else {
		// Rebase conflict
		if git.IsRebaseInProgress() {
			if err := git.RebaseContinue(git.RebaseOpts{}); err != nil {
				return fmt.Errorf("rebase continue failed — resolve remaining conflicts and try again: %w", err)
			}
		}
		cfg.Successf("Rebased %s", state.ConflictBranch)
	}

	// Continue cascading rebase for remaining branches
	for _, branchName := range state.RemainingBranches {
		idx := s.IndexOf(branchName)
		if idx < 0 {
			cfg.Warningf("branch %s no longer in stack, skipping", branchName)
			continue
		}
		b := s.Branches[idx]
		if b.IsMerged() {
			continue
		}

		var newBase string
		if idx == 0 {
			newBase = s.Trunk.Branch
		} else {
			newBase = s.ActiveBaseBranch(b.Branch)
		}

		// Use original parent tip or merge-base as oldBase
		oldBase := ""
		if state.OriginalRefs != nil {
			oldBase = state.OriginalRefs[b.Branch]
		}
		if oldBase == "" {
			if mb, mberr := git.MergeBase(newBase, b.Branch); mberr == nil {
				oldBase = mb
			} else {
				continue
			}
		}

		// Check if rebase is needed
		isAnc, ancErr := git.IsAncestor(newBase, b.Branch)
		if ancErr == nil && isAnc {
			if mb, mberr := git.MergeBase(newBase, b.Branch); mberr == nil && mb == oldBase {
				continue
			}
		}

		if err := git.RebaseOnto(newBase, oldBase, b.Branch, git.RebaseOpts{}); err != nil {
			// Another conflict — update state and bail
			remaining := make([]string, 0)
			foundCurrent := false
			for _, rn := range state.RemainingBranches {
				if rn == branchName {
					foundCurrent = true
					continue
				}
				if foundCurrent {
					remaining = append(remaining, rn)
				}
			}
			state.ConflictBranch = branchName
			state.RemainingBranches = remaining
			state.AffectsPRs = affectsPRs
			_ = SaveState(gitDir, state)

			cfg.Warningf("Conflict rebasing %s", branchName)
			if files, ferr := git.ConflictedFiles(); ferr == nil {
				for _, f := range files {
					cfg.Printf("  %s", f)
				}
			}
			cfg.Printf("")
			cfg.Printf("Resolve the conflicts, stage with `%s`, then run `%s`",
				cfg.ColorCyan("git add <file>"),
				cfg.ColorCyan("gh stack modify --continue"))
			cfg.Printf("Or restore the stack with `%s`",
				cfg.ColorCyan("gh stack modify --abort"))
			return fmt.Errorf("rebase conflict on %s", branchName)
		}

		cfg.Successf("Rebased %s onto %s", branchName, newBase)
		if b.PullRequest != nil {
			affectsPRs = true
		}
	}
	// All rebases done — check out the best branch
	if state.OriginalBranch != "" {
		targetBranch := resolveCheckoutBranch(state.OriginalBranch, state.Plan, state.Snapshot, s)
		if err := git.CheckoutBranch(targetBranch); err == nil {
			if targetBranch != state.OriginalBranch {
				cfg.Printf("Switched to %s (original branch %s is no longer in the stack)", targetBranch, state.OriginalBranch)
			}
		}
	}

	// Update base SHAs
	updateBaseSHAs(s)

	// Transition to pending_submit only when PRs are affected
	needsSubmit := s.ID != "" && affectsPRs
	if needsSubmit {
		state.Phase = PhasePendingSubmit
		state.ConflictBranch = ""
		state.RemainingBranches = nil
		state.OriginalRefs = nil
		if err := SaveState(gitDir, state); err != nil {
			cfg.Warningf("failed to update modify state: %s", err)
		}
	}

	// Save stack metadata
	if err := stack.SaveWithLock(gitDir, sf, lock); err != nil {
		cfg.Warningf("failed to save stack: %v", err)
	}

	// Clear state after metadata save succeeds to preserve --abort recovery
	if !needsSubmit {
		ClearState(gitDir)
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
// stackIndex is the index of the stack in sf.Stacks at modify start time.
func Unwind(cfg *config.Config, gitDir string, snapshot Snapshot, stackIndex int, sf *stack.StackFile, plan []Action) error {
	// Abort any in-progress rebase
	if git.IsRebaseInProgress() {
		_ = git.RebaseAbort()
	}

	// Restore branch tips
	snapshotNames := make(map[string]bool, len(snapshot.Branches))
	for _, bs := range snapshot.Branches {
		snapshotNames[bs.Name] = true
		if !git.BranchExists(bs.Name) {
			// Branch was renamed — try to find it by SHA and recreate
			if err := git.CreateBranch(bs.Name, bs.TipSHA); err != nil {
				cfg.Warningf("failed to restore branch %s: %v", bs.Name, err)
				continue
			}
		} else {
			if err := git.CheckoutBranch(bs.Name); err != nil {
				cfg.Warningf("failed to checkout %s for unwind: %v", bs.Name, err)
				continue
			}
			if err := git.ResetHard(bs.TipSHA); err != nil {
				cfg.Warningf("failed to reset %s to %s: %v", bs.Name, bs.TipSHA[:7], err)
				continue
			}
		}
	}

	// Clean up branches created by renames or inserts during the partial apply
	for _, action := range plan {
		if action.NewName != "" && (action.Type == "rename" || action.Type == "insert_below" || action.Type == "insert_above") {
			if !snapshotNames[action.NewName] && git.BranchExists(action.NewName) {
				_ = git.DeleteBranch(action.NewName, true)
			}
		}
	}

	// Restore stack metadata from snapshot
	var restoredStack stack.Stack
	if err := json.Unmarshal(snapshot.StackMetadata, &restoredStack); err != nil {
		return fmt.Errorf("restoring stack metadata: %w", err)
	}

	// Replace the stack at the saved index
	if stackIndex >= 0 && stackIndex < len(sf.Stacks) {
		sf.Stacks[stackIndex] = restoredStack
	}

	// Save restored stack
	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Warningf("failed to save restored stack: %v", err)
	}

	// Clear state file
	ClearState(gitDir)

	// Checkout the first snapshot branch
	if len(snapshot.Branches) > 0 {
		_ = git.CheckoutBranch(snapshot.Branches[0].Name)
	}

	cfg.Successf("Stack restored to pre-modify state")
	return nil
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

	return Unwind(cfg, gitDir, state.Snapshot, state.StackIndex, sf, state.Plan)
}
