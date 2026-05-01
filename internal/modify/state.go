package modify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stateFileName = "gh-stack-modify-state"

// StateFile holds the state of an in-progress or pending-submit modify operation.
// It is stored at .git/gh-stack-modify-state.
type StateFile struct {
	SchemaVersion      int            `json:"schema_version"`
	StackName          string         `json:"stack_name"`
	StackIndex         int            `json:"stack_index"` // index in StackFile.Stacks at modify start
	StartedAt          time.Time      `json:"started_at"`
	Phase              string         `json:"phase"` // "applying", "conflict", or "pending_submit"
	PriorRemoteStackID string         `json:"prior_remote_stack_id,omitempty"`
	Snapshot           Snapshot       `json:"snapshot"`
	Plan               []Action       `json:"plan"`

	// Conflict state — populated when phase is "conflict"
	ConflictBranch    string            `json:"conflict_branch,omitempty"`
	RemainingBranches []string          `json:"remaining_branches,omitempty"`
	OriginalBranch    string            `json:"original_branch,omitempty"`
	OriginalRefs      map[string]string `json:"original_refs,omitempty"`
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
	data, err := os.ReadFile(StatePath(gitDir))
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
	return &state, nil
}

// SaveState writes the modify state file atomically (write to temp, then rename).
func SaveState(gitDir string, state *StateFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling modify state: %w", err)
	}
	target := StatePath(gitDir)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing modify state: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("committing modify state: %w", err)
	}
	return nil
}

// ClearState removes the modify state file.
func ClearState(gitDir string) {
	_ = os.Remove(StatePath(gitDir))
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
		return nil // ignore read errors
	}
	if state == nil {
		return nil
	}
	if state.Phase == "applying" {
		return fmt.Errorf("a modify session was interrupted — run `gh stack modify --recover` to restore your stack")
	}
	if state.Phase == "conflict" {
		return fmt.Errorf("a modify has unresolved conflicts — run `gh stack modify --continue` or `gh stack modify --recover`")
	}
	return nil
}
