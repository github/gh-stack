package modify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
)

const stateFileName = "gh-stack-modify-state"

const (
	PhaseApplying      = "applying"
	PhaseConflict      = "conflict"
	PhasePendingSubmit = "pending_submit"
)

// StateFile holds the state of an in-progress or pending-submit modify operation.
// It is stored at <common-dir>/gh-stack-modify-state.
type StateFile struct {
	SchemaVersion         int               `json:"schema_version"`
	StackName             string            `json:"stack_name"`
	StackIndex            int               `json:"stack_index"` // legacy hint, never an identity
	StartedAt             time.Time         `json:"started_at"`
	Phase                 string            `json:"phase"`
	PriorRemoteStackID    string            `json:"prior_remote_stack_id,omitempty"`
	Snapshot              Snapshot          `json:"snapshot"`
	Plan                  []Action          `json:"plan"`
	Worktrees             *worktree.Context `json:"worktrees,omitempty"`
	StackBranches         []string          `json:"stack_branches"`
	PreviousStackBranches []string          `json:"previous_stack_branches"`
	RenamedBranches       map[string]string `json:"renamed_branches,omitempty"`
	CreatedBranches       map[string]string `json:"created_branches,omitempty"`
	PendingAction         *Action           `json:"pending_action,omitempty"`

	// Conflict state — populated when phase is "conflict"
	ConflictBranch    string            `json:"conflict_branch,omitempty"`
	ConflictType      string            `json:"conflict_type,omitempty"` // "rebase" or "cherry_pick"
	RemainingBranches []string          `json:"remaining_branches,omitempty"`
	OriginalBranch    string            `json:"original_branch,omitempty"`
	OriginalRefs      map[string]string `json:"original_refs,omitempty"`

	// Cherry-pick conflict context — which fold was in progress
	FoldBranch string `json:"fold_branch,omitempty"` // branch being folded
	FoldTarget string `json:"fold_target,omitempty"` // branch receiving the cherry-pick

	// AffectsPRs records whether any action so far has affected a branch with
	// a PR.  Persisted across conflict boundaries so ContinueApply can combine
	// it with checks on remaining branches.
	AffectsPRs bool `json:"affects_prs,omitempty"`
}

// Snapshot captures the pre-modify state for unwind/recovery.
type Snapshot struct {
	Branches      []BranchSnapshot `json:"branches"`
	StackMetadata json.RawMessage  `json:"stack_metadata"`
}

// BranchSnapshot stores the state of a single branch before modification.
type BranchSnapshot struct {
	Name     string `json:"name"`
	TipSHA   string `json:"tip_sha"`
	Position int    `json:"position"`
}

// Action represents a single staged action from the TUI.
type Action struct {
	Type        string `json:"type"` // "drop", "fold_down", "fold_up", "move", "rename"
	Branch      string `json:"branch"`
	NewPosition int    `json:"new_position,omitempty"` // for "move"
	NewName     string `json:"new_name,omitempty"`     // for "rename"
}

// StatePath returns the full path to the modify state file.
func StatePath(gitDir string) string {
	return filepath.Join(gitDir, stateFileName)
}

// LoadState reads the modify state file from the git directory.
// Returns nil, nil if the file does not exist.
func LoadState(gitDir string) (*StateFile, error) {
	data, err := stack.ReadStateFile(StatePath(gitDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading modify state: %w", err)
	}

	var state StateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing modify state: %w", err)
	}
	if state.SchemaVersion > 1 {
		return nil, fmt.Errorf("modify state uses unsupported schema version %d; upgrade gh-stack before recovery", state.SchemaVersion)
	}
	return &state, nil
}

// SaveState writes the modify state file atomically (write to temp, then rename).
func SaveState(gitDir string, state *StateFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling modify state: %w", err)
	}
	if err := stack.WriteAtomic(StatePath(gitDir), data); err != nil {
		return fmt.Errorf("writing modify state: %w", err)
	}
	return nil
}

func (s *StateFile) RecordStack(target *stack.Stack) {
	names := target.BranchNames()
	if s.StackBranches != nil && !slices.Equal(s.StackBranches, names) {
		s.PreviousStackBranches = s.StackBranches
	}
	s.StackName = target.Trunk.Branch
	s.StackBranches = append([]string{}, names...)
}

// MatchesStack never uses the catalog position as an identity. Older records
// can be identified by their remote ID or the original snapshot.
func MatchesStack(state *StateFile, target *stack.Stack) bool {
	if state == nil || target == nil {
		return false
	}
	if state.PriorRemoteStackID != "" && target.ID != "" {
		return state.PriorRemoteStackID == target.ID
	}
	if state.StackBranches != nil {
		if state.StackName != target.Trunk.Branch {
			return false
		}
		if slices.Equal(state.StackBranches, target.BranchNames()) {
			return true
		}
		// The journal is published before the catalog. An interrupted save
		// may leave either definition on disk, but submit must match only
		// the completed definition.
		return state.Phase != PhasePendingSubmit && state.PreviousStackBranches != nil &&
			slices.Equal(state.PreviousStackBranches, target.BranchNames())
	}
	var original stack.Stack
	if err := json.Unmarshal(state.Snapshot.StackMetadata, &original); err != nil {
		return false
	}
	if original.Trunk.Branch == "" || original.Trunk.Branch != target.Trunk.Branch {
		return false
	}
	if slices.Equal(original.BranchNames(), target.BranchNames()) {
		return true
	}
	names := original.BranchNames()
	for _, action := range state.Plan {
		switch action.Type {
		case "rename":
			for i, name := range names {
				if name == action.Branch {
					names[i] = action.NewName
				}
			}
		case "drop", "fold_down", "fold_up":
			names = slices.DeleteFunc(names, func(name string) bool { return name == action.Branch })
		case "insert_below", "insert_above":
			if action.NewPosition < 0 || action.NewPosition > len(names) || action.NewName == "" {
				return false
			}
			names = slices.Insert(names, action.NewPosition, action.NewName)
		case "move":
			index := slices.Index(names, action.Branch)
			if index < 0 || action.NewPosition < 0 || action.NewPosition >= len(names) {
				return false
			}
			names = slices.Delete(names, index, index+1)
			names = slices.Insert(names, action.NewPosition, action.Branch)
		}
	}
	return slices.Equal(names, target.BranchNames())
}

// ClearState removes the modify state file.
func ClearState(gitDir string) error {
	if err := os.Remove(StatePath(gitDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing modify state: %w", err)
	}
	return nil
}

// StateExists returns true if a modify state file exists.
func StateExists(gitDir string) bool {
	_, err := os.Stat(StatePath(gitDir))
	return err == nil
}

// CheckStateGuard checks if a modify state file exists with phase "applying"
// and returns an error if so. This is used as a guard at the top of commands that
// should not run while a modify is in progress.
func CheckStateGuard(gitDir string) error {
	state, err := LoadState(gitDir)
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	if state.Phase == PhaseApplying {
		return fmt.Errorf("a modify session was interrupted — run `gh stack modify --abort` to restore your stack")
	}
	if state.Phase == PhaseConflict {
		return fmt.Errorf("a modify has unresolved conflicts — run `gh stack modify --continue` or `gh stack modify --abort`")
	}
	if state.Phase != PhasePendingSubmit {
		return fmt.Errorf("unrecognized modify state phase %q; recovery state was retained", state.Phase)
	}
	return nil
}
