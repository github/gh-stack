// Package submitview implements the interactive single-screen TUI used by
// `gh stack submit` to create a stack of pull requests. A left-hand timeline
// lists the stack and lets the user choose which branches without a PR should
// become PRs; the right-hand editor drafts each PR's title, description, and
// ready/draft state before a single batch submit.
//
// The package builds on the shared Charm components in internal/tui/shared and
// reuses the branch display data loaded by internal/tui/stackview. The submit
// command supplies the per-PR overrides this package produces; the underlying
// push/create/relink engine is unchanged.
package submitview

import (
	"github.com/github/gh-stack/internal/tui/stackview"
)

// BranchState classifies a branch by the status of its pull request. The state
// determines whether a branch is selectable and editable in the TUI, and which
// badge color it renders with.
type BranchState int

const (
	// StateNew is a branch with no PR yet. It is the only interactive state:
	// selectable (default on) and editable.
	StateNew BranchState = iota
	// StateOpen is a branch with an open (non-draft) PR. Locked, shown for context.
	StateOpen
	// StateDraft is a branch with a draft PR. Locked, shown for context.
	StateDraft
	// StateQueued is a branch whose PR is in a merge queue. Locked.
	StateQueued
	// StateMerged is a branch whose PR has merged. Locked, historical context.
	StateMerged
	// StateClosed is a branch with a closed PR. It blocks the stack and is
	// neither selectable nor editable.
	StateClosed
)

// Selectable reports whether a branch in this state can be toggled for
// inclusion. Only NEW branches are selectable.
func (s BranchState) Selectable() bool { return s == StateNew }

// Editable reports whether a branch in this state opens the editor. Only NEW
// branches are editable.
func (s BranchState) Editable() bool { return s == StateNew }

// Locked reports whether a branch is shown for context only (open, draft,
// queued, or merged). Closed is handled separately because it blocks the stack.
func (s BranchState) Locked() bool {
	switch s {
	case StateOpen, StateDraft, StateQueued, StateMerged:
		return true
	default:
		return false
	}
}

// Blocks reports whether a branch in this state blocks the stack. Only closed
// PRs block.
func (s BranchState) Blocks() bool { return s == StateClosed }

// SubmitNode wraps a stackview.BranchNode with the per-branch UI state used by
// the submit TUI: its derived state, inclusion, the in-progress title and
// description draft, and the draft toggle. Prefill snapshots are retained so
// the model can detect unsaved edits for the quit confirmation.
type SubmitNode struct {
	stackview.BranchNode

	// State is the derived PR state for this branch.
	State BranchState

	// Included reports whether a PR should be created for this branch on
	// submit. Only meaningful for StateNew branches; defaults to true.
	Included bool

	// Title and Description hold the in-progress PR draft, prefilled from the
	// branch's commits and the repo PR template (see data.go).
	Title       string
	Description string

	// Draft is the per-PR "Open as draft" toggle.
	Draft bool

	// Submitted is set once this branch's PR has been created during the
	// current session. It drives the inline "✓ready" tag in the stack map.
	Submitted bool

	// prefill snapshots used to detect user edits.
	titlePrefill string
	descPrefill  string
	draftPrefill bool
}

// Edited reports whether the user has changed any field of this NEW branch from
// its prefilled defaults: title, description, draft toggle, or inclusion.
// Non-NEW branches are never editable and so are never considered edited.
func (n SubmitNode) Edited() bool {
	if n.State != StateNew {
		return false
	}
	return n.Title != n.titlePrefill ||
		n.Description != n.descPrefill ||
		n.Draft != n.draftPrefill ||
		!n.Included
}

// PRDraft is the per-branch override the TUI hands back to the submit command.
// The command's create path consumes these instead of auto-generating titles
// and bodies. The attribution footer is appended by the command at submit time,
// so Body holds only the user-authored description.
type PRDraft struct {
	// Branch is the head branch the PR will be created from.
	Branch string
	// Include reports whether to create a PR for this branch. Deselected NEW
	// branches are still pushed for stack consistency but get no PR.
	Include bool
	// Title is the PR title.
	Title string
	// Body is the user-authored PR description, without the attribution footer.
	Body string
	// Draft reports whether the PR should be created as a draft.
	Draft bool
}

// ToDraft converts a SubmitNode into the command-layer PRDraft override.
func (n SubmitNode) ToDraft() PRDraft {
	return PRDraft{
		Branch:  n.Ref.Branch,
		Include: n.Included,
		Title:   n.Title,
		Body:    n.Description,
		Draft:   n.Draft,
	}
}

// BuildDrafts returns the per-branch overrides for every NEW branch, keyed by
// branch name. Non-NEW branches are omitted because their PRs are never
// modified by the TUI.
func BuildDrafts(nodes []SubmitNode) map[string]*PRDraft {
	drafts := make(map[string]*PRDraft)
	for _, n := range nodes {
		if n.State != StateNew {
			continue
		}
		d := n.ToDraft()
		drafts[n.Ref.Branch] = &d
	}
	return drafts
}
