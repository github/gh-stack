package modify

import (
	"fmt"
	"slices"

	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
)

func removesNode(n modifyview.ModifyBranchNode) bool {
	if n.Removed {
		return true
	}
	return n.PendingAction != nil && (n.PendingAction.Type == modifyview.ActionDrop ||
		n.PendingAction.Type == modifyview.ActionFoldDown || n.PendingAction.Type == modifyview.ActionFoldUp)
}

// Resolve action targets before touching Git. The persisted execution order
// also lets continuation finish structural actions after a fold conflict.
func compileActions(s *stack.Stack, nodes []modifyview.ModifyBranchNode) ([]Action, []string, error) {
	preview := *s
	preview.Branches = append([]stack.BranchRef{}, s.Branches...)
	renames := make(map[string]string)
	seen := make(map[string]bool)
	originalOrder := make([]string, 0, len(s.Branches))
	hasStructure, hasReorder := false, false
	for _, n := range nodes {
		if seen[n.Ref.Branch] {
			return nil, nil, fmt.Errorf("branch %s appears more than once in the modify plan", n.Ref.Branch)
		}
		seen[n.Ref.Branch] = true
		if n.IsInserted {
			if n.PendingAction == nil || (n.PendingAction.Type != modifyview.ActionInsertAbove && n.PendingAction.Type != modifyview.ActionInsertBelow) ||
				n.PendingAction.NewName != n.Ref.Branch || removesNode(n) {
				return nil, nil, fmt.Errorf("invalid insertion plan for %s", n.Ref.Branch)
			}
		} else {
			originalOrder = append(originalOrder, n.Ref.Branch)
			index := s.IndexOf(n.Ref.Branch)
			if index < 0 {
				return nil, nil, fmt.Errorf("branch %s is not in the stack being modified", n.Ref.Branch)
			}
			if s.Branches[index].IsMerged() && (n.PendingAction != nil || n.Removed) {
				return nil, nil, fmt.Errorf("cannot modify merged branch %s", n.Ref.Branch)
			}
			if n.Removed && n.PendingAction == nil {
				return nil, nil, fmt.Errorf("removed branch %s has no modify action", n.Ref.Branch)
			}
		}
		if n.PendingAction != nil {
			switch n.PendingAction.Type {
			case modifyview.ActionRename, modifyview.ActionInsertAbove, modifyview.ActionInsertBelow:
				hasStructure = true
				name := n.PendingAction.NewName
				if err := git.ValidateRefName(name); err != nil {
					return nil, nil, fmt.Errorf("invalid branch name %q: %w", name, err)
				}
				exists, err := git.BranchExists(name)
				if err != nil {
					return nil, nil, fmt.Errorf("checking planned branch %s: %w", name, err)
				}
				if exists || s.Contains(name) {
					return nil, nil, fmt.Errorf("branch %s already exists", name)
				}
			case modifyview.ActionDrop, modifyview.ActionFoldDown, modifyview.ActionFoldUp:
				hasStructure = true
			case modifyview.ActionMove:
				hasReorder = true
			default:
				return nil, nil, fmt.Errorf("unknown modify action %q", n.PendingAction.Type)
			}
		}
	}
	for _, branch := range s.Branches {
		if !seen[branch.Branch] {
			return nil, nil, fmt.Errorf("branch %s is missing from the modify plan", branch.Branch)
		}
	}
	// Compare original branch order, including removed nodes but excluding
	// insertions. TUI OriginalPosition uses the opposite display orientation.
	if hasStructure && (hasReorder || !slices.Equal(originalOrder, s.BranchNames())) {
		return nil, nil, fmt.Errorf("cannot mix reordering with drops, folds, inserts, or renames in one modify session; apply them separately")
	}

	var execution []Action
	for _, n := range nodes {
		if n.PendingAction == nil || n.PendingAction.Type != modifyview.ActionRename {
			continue
		}
		name := n.PendingAction.NewName
		if preview.Contains(name) {
			return nil, nil, fmt.Errorf("branch %s is used by multiple modify actions", name)
		}
		renames[n.Ref.Branch] = name
		preview.Branches[preview.IndexOf(n.Ref.Branch)].Branch = name
		execution = append(execution, Action{Type: "rename", Branch: n.Ref.Branch, NewName: name})
	}
	resolved := func(name string) string {
		if renamed := renames[name]; renamed != "" {
			return renamed
		}
		return name
	}
	desired := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if !removesNode(n) && !n.Ref.IsMerged() {
			desired = append(desired, resolved(n.Ref.Branch))
		}
	}
	for _, n := range nodes {
		if !n.IsInserted {
			continue
		}
		name := n.PendingAction.NewName
		if preview.Contains(name) {
			return nil, nil, fmt.Errorf("branch %s is used by multiple modify actions", name)
		}
		position := slices.Index(desired, name)
		if position < 0 {
			return nil, nil, fmt.Errorf("inserted branch %s has no position in the stack", name)
		}
		parent := s.Trunk.Branch
		if position > 0 {
			parent = desired[position-1]
		}
		index := 0
		if parent != s.Trunk.Branch {
			index = preview.IndexOf(parent) + 1
			if index == 0 {
				return nil, nil, fmt.Errorf("insertion parent %s is missing from the stack", parent)
			}
		}
		preview.Branches = slices.Insert(preview.Branches, index, stack.BranchRef{Branch: name})
		execution = append(execution, Action{
			Type: string(n.PendingAction.Type), Branch: name, NewName: name, Target: parent, NewPosition: index,
		})
	}
	normalizedDrops := make(map[string]map[string]bool)
	for i, n := range nodes {
		if n.PendingAction == nil || (n.PendingAction.Type != modifyview.ActionFoldDown && n.PendingAction.Type != modifyview.ActionFoldUp) {
			continue
		}
		direction := -1
		if n.PendingAction.Type == modifyview.ActionFoldUp {
			direction = 1
		}
		target := ""
		targetPosition := -1
		for j := i + direction; j >= 0 && j < len(nodes); j += direction {
			candidate := nodes[j]
			if removesNode(candidate) || candidate.Ref.IsMerged() {
				continue
			}
			if candidate.IsInserted {
				return nil, nil, fmt.Errorf("cannot fold %s into an inserted branch", n.Ref.Branch)
			}
			target = resolved(candidate.Ref.Branch)
			targetPosition = j
			break
		}
		if target == "" {
			return nil, nil, fmt.Errorf("no surviving branch to fold %s into", n.Ref.Branch)
		}
		if n.PendingAction.Type == modifyview.ActionFoldUp {
			if normalizedDrops[target] == nil {
				normalizedDrops[target] = make(map[string]bool)
			}
			// Remove upper dropped ranges first so lower original cutoffs
			// remain ancestors while the receiver is normalized.
			for j := targetPosition - 1; j > i; j-- {
				dropped := nodes[j]
				if dropped.PendingAction == nil || dropped.PendingAction.Type != modifyview.ActionDrop {
					continue
				}
				name := resolved(dropped.Ref.Branch)
				if !normalizedDrops[target][name] {
					execution = append(execution, Action{Type: "fold_rebase", Branch: target, Target: name})
					normalizedDrops[target][name] = true
				}
			}
		}
		execution = append(execution, Action{Type: string(n.PendingAction.Type), Branch: resolved(n.Ref.Branch), Target: target})
	}
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if n.PendingAction != nil && n.PendingAction.Type == modifyview.ActionDrop {
			execution = append(execution, Action{Type: "drop", Branch: resolved(n.Ref.Branch)})
		}
	}
	return execution, desired, nil
}

func applyOrder(s *stack.Stack, desired []string) error {
	branches := make(map[string]stack.BranchRef)
	for _, branch := range s.Branches {
		if !branch.IsMerged() {
			branches[branch.Branch] = branch
		}
	}
	if len(branches) != len(desired) {
		return fmt.Errorf("the modify plan no longer matches the stack's active branches")
	}
	next := 0
	for i, branch := range s.Branches {
		if branch.IsMerged() {
			continue
		}
		replacement, ok := branches[desired[next]]
		if !ok {
			return fmt.Errorf("branch %s is missing from the planned stack order", desired[next])
		}
		delete(branches, desired[next])
		s.Branches[i] = replacement
		next++
	}
	return nil
}

func plannedRef(state *StateFile, name string) string {
	for _, action := range state.Plan {
		if action.Type == "rename" && action.NewName == name && state.RenamedBranches[action.Branch] == "" {
			return action.Branch
		}
	}
	if renamed := state.RenamedBranches[name]; renamed != "" {
		return renamed
	}
	return name
}

func remainingTargets(state *StateFile) []string {
	var branches []string
	if state.DesiredOrder != nil && state.NextAction < len(state.Execution) {
		for _, action := range state.Execution[state.NextAction:] {
			switch action.Type {
			case "rename", "fold_rebase":
				branches = append(branches, action.Branch)
			case "fold_down":
				branches = append(branches, action.Target)
			}
		}
		branches = append(branches, state.DesiredOrder...)
	} else if state.ConflictType == "" && state.DesiredOrder != nil {
		branches = append(branches, state.DesiredOrder...)
	} else {
		branches = append(branches, state.RemainingBranches...)
		if state.ConflictType == "rebase_start" || state.ConflictType == "rebase" || state.ConflictType == "" {
			if state.ConflictBranch != "" {
				branches = append(branches, state.ConflictBranch)
			}
		}
	}
	for i, name := range branches {
		branches[i] = plannedRef(state, name)
	}
	return branches
}
