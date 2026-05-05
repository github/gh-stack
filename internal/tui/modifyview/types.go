package modifyview

import (
	"github.com/github/gh-stack/internal/tui/stackview"
)

// ActionType represents the type of modification action.
type ActionType string

const (
	ActionDrop     ActionType = "drop"
	ActionFoldDown ActionType = "fold_down"
	ActionFoldUp   ActionType = "fold_up"
	ActionMove     ActionType = "move"
	ActionRename   ActionType = "rename"
)

// PendingAction represents a staged modification on a branch.
type PendingAction struct {
	Type    ActionType
	NewName string // for rename
}

// StagedAction records a single staged action in the undo stack.
// It stores enough information to reverse the action.
type StagedAction struct {
	Type             ActionType
	BranchName       string // the branch affected
	OriginalPosition int    // for move: the position before the move
	NewPosition      int    // for move: the position after the move
	OriginalName     string // for rename: the name before rename
	NewName          string // for rename: the name after rename
	FoldTarget       string // for fold: the branch that received the commits
}

// ModifyBranchNode wraps a BranchNode with modification state.
type ModifyBranchNode struct {
	stackview.BranchNode
	PendingAction    *PendingAction
	OriginalPosition int
	Removed          bool // true if this branch has been dropped or folded
}

// ApplyResult holds the outcome of applying modifications.
type ApplyResult struct {
	Success         bool
	DroppedPRs      []DroppedPR
	RenamedBranches []RenamedBranch
	MovedBranches   int
	Message         string
}

// DroppedPR records a PR that was dropped from the stack.
type DroppedPR struct {
	Branch   string
	PRNumber int
}

// RenamedBranch records a branch rename.
type RenamedBranch struct {
	OldName string
	NewName string
}

// ConflictInfo holds information about a rebase conflict during apply.
type ConflictInfo struct {
	Branch          string
	ConflictedFiles []string
}
