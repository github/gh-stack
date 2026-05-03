package modifyview

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeNode creates a test ModifyBranchNode with sensible defaults.
func makeNode(branch string, isCurrent bool, pos int) ModifyBranchNode {
	return ModifyBranchNode{
		BranchNode: stackview.BranchNode{
			Ref:       stack.BranchRef{Branch: branch},
			IsCurrent: isCurrent,
			IsLinear:  true,
		},
		OriginalPosition: pos,
	}
}

// makeMergedNode creates a merged test node (IsMerged() returns true).
func makeMergedNode(branch string, pos int) ModifyBranchNode {
	return ModifyBranchNode{
		BranchNode: stackview.BranchNode{
			Ref: stack.BranchRef{
				Branch:      branch,
				PullRequest: &stack.PullRequestRef{Number: 1, Merged: true},
			},
			IsLinear: true,
		},
		OriginalPosition: pos,
	}
}

var testTrunk = stack.BranchRef{Branch: "main"}

// sendKey sends a key message to the model and returns the updated Model.
func sendKey(t *testing.T, m Model, msg tea.KeyMsg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// --- New() constructor tests ---

func TestNew_CursorDefaultsToCurrentBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("b0", false, 0),
		makeNode("b1", false, 1),
		makeNode("b2", true, 2),
		makeNode("b3", false, 3),
	}
	m := New(nodes, testTrunk, "1.0.0")
	assert.Equal(t, 2, m.cursor, "cursor should be on the IsCurrent node")
}

func TestNew_CursorFallsBackToFirstNonMerged(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeMergedNode("merged0", 0),
		makeNode("active1", false, 1),
		makeNode("active2", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	assert.Equal(t, 1, m.cursor, "cursor should skip merged node and land on first non-merged")
}

// --- Drop toggle tests ---

func TestDropToggle(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 1, m.cursor)

	// Press 'x' → drop
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionDrop, m.nodes[1].PendingAction.Type)
	assert.True(t, m.nodes[1].Removed)

	// Press 'x' again → undo drop
	m = sendKey(t, m, runeKey('x'))
	assert.Nil(t, m.nodes[1].PendingAction)
	assert.False(t, m.nodes[1].Removed)
}

// --- Fold toggle tests ---

func TestFoldToggle(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 1, m.cursor) // cursor on b

	// 'd' → fold down
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[1].PendingAction.Type)
	assert.True(t, m.nodes[1].Removed)

	// 'd' again → toggle off
	m = sendKey(t, m, runeKey('d'))
	assert.Nil(t, m.nodes[1].PendingAction)
	assert.False(t, m.nodes[1].Removed)

	// 'u' → fold up
	m = sendKey(t, m, runeKey('u'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldUp, m.nodes[1].PendingAction.Type)
	assert.True(t, m.nodes[1].Removed)

	// 'u' again → toggle off
	m = sendKey(t, m, runeKey('u'))
	assert.Nil(t, m.nodes[1].PendingAction)
	assert.False(t, m.nodes[1].Removed)
}

// --- Fold replace tests ---

func TestFoldReplace(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// 'd' → fold down
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[1].PendingAction.Type)

	// 'u' → should replace fold-down with fold-up
	m = sendKey(t, m, runeKey('u'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldUp, m.nodes[1].PendingAction.Type)
	assert.True(t, m.nodes[1].Removed)
}

// --- Last branch guard tests ---

func TestCannotDropLastBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Drop first node
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[0].PendingAction)
	assert.Equal(t, ActionDrop, m.nodes[0].PendingAction.Type)

	// Move cursor to second node
	m = sendKey(t, m, runeKey('j'))
	require.Equal(t, 1, m.cursor)

	// Try to drop second node → should be refused
	m = sendKey(t, m, runeKey('x'))
	assert.Nil(t, m.nodes[1].PendingAction, "second node should NOT be dropped")
	assert.False(t, m.nodes[1].Removed)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "last branch")
}

func TestCannotFoldLastBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Drop first node
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[0].PendingAction)

	// Move cursor to second node
	m = sendKey(t, m, runeKey('j'))
	require.Equal(t, 1, m.cursor)

	// Try to fold second node down → should fail (no branch below)
	m = sendKey(t, m, runeKey('d'))
	assert.Nil(t, m.nodes[1].PendingAction, "second node should NOT be folded")
	assert.True(t, m.statusIsError)

	// Try to fold second node up → should fail (only target above is removed)
	m = sendKey(t, m, runeKey('u'))
	assert.Nil(t, m.nodes[1].PendingAction, "second node should NOT be folded")
	assert.True(t, m.statusIsError)
}

// --- Mutual fold test ---

func TestMutualFoldBlocked(t *testing.T) {
	// With 3 nodes A(0), B(1), C(2): fold B down into C, then try
	// to fold C up. Since B is removed the target search for fold-up
	// skips B and finds A. The fold into A would leave only 1 active
	// branch (A) so it IS allowed (active >= 1). Verify the behavior.
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold B down into C
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[1].PendingAction.Type)
	assert.True(t, m.nodes[1].Removed)

	// Move cursor to C
	m = sendKey(t, m, runeKey('j'))
	require.Equal(t, 2, m.cursor)

	// Try fold C up — B is removed so target becomes A.
	// With only 2 nodes removed (B, C), A is the only active → active=1 (passes guard).
	m = sendKey(t, m, runeKey('u'))
	require.NotNil(t, m.nodes[2].PendingAction, "C should fold up into A since B is skipped")
	assert.Equal(t, ActionFoldUp, m.nodes[2].PendingAction.Type)
	assert.True(t, m.nodes[2].Removed)

	// Verify only A remains active
	active := 0
	for _, n := range m.nodes {
		if !n.Removed {
			active++
		}
	}
	assert.Equal(t, 1, active, "only A should remain active")
}

// --- Mode exclusivity tests ---

func TestModeExclusivity_ReorderBlocksStructure(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 0, m.cursor)

	// Move top node down (shift+down = 'J') → reorder mode
	m = sendKey(t, m, runeKey('J'))
	assert.Equal(t, 1, m.cursor, "cursor should move with the swapped node")
	assert.Equal(t, modeReorder, m.currentMode())

	// Try 'x' (drop) → should show error
	m = sendKey(t, m, runeKey('x'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")

	// Try 'd' (fold) → should show error
	m = sendKey(t, m, runeKey('d'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")
}

func TestModeExclusivity_StructureBlocksReorder(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 1, m.cursor)

	// Drop middle node → structure mode
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, modeStructure, m.currentMode())

	// Move cursor to a non-removed node
	m = sendKey(t, m, runeKey('j'))
	require.Equal(t, 2, m.cursor)

	// Try shift+down (move) → should show error
	m = sendKey(t, m, runeKey('J'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")

	// Try shift+up (move) → should show error
	m = sendKey(t, m, runeKey('K'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")
}

// --- Apply validation tests ---

func TestApplyRefusedWhenNoPendingChanges(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// ctrl+s with no changes
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	assert.False(t, m.applyRequested)
	assert.Equal(t, "No pending changes to apply", m.statusMessage)
	assert.False(t, m.statusIsError)
}

func TestApplySucceedsWithPendingChanges(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Drop a node
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[0].PendingAction)

	// ctrl+s → apply should be requested
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	assert.True(t, m.applyRequested)
}

// --- Quit / cancel tests ---

func TestQuitSetsCancelled(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
	}
	m := New(nodes, testTrunk, "1.0.0")

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	assert.True(t, m.Cancelled())
}

// --- Cursor navigation tests ---

func TestCursorNavigation(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 0, m.cursor)

	// Move down
	m = sendKey(t, m, runeKey('j'))
	assert.Equal(t, 1, m.cursor)

	// Move down again
	m = sendKey(t, m, runeKey('j'))
	assert.Equal(t, 2, m.cursor)

	// Move down at bottom → stays at bottom
	m = sendKey(t, m, runeKey('j'))
	assert.Equal(t, 2, m.cursor)

	// Move up
	m = sendKey(t, m, runeKey('k'))
	assert.Equal(t, 1, m.cursor)

	// Move up to top
	m = sendKey(t, m, runeKey('k'))
	assert.Equal(t, 0, m.cursor)

	// Move up at top → stays at top
	m = sendKey(t, m, runeKey('k'))
	assert.Equal(t, 0, m.cursor)
}

// --- Undo tests ---

func TestUndoDrop(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Drop node
	m = sendKey(t, m, runeKey('x'))
	require.True(t, m.nodes[0].Removed)

	// Undo
	m = sendKey(t, m, runeKey('z'))
	assert.Nil(t, m.nodes[0].PendingAction)
	assert.False(t, m.nodes[0].Removed)
}

func TestUndoMove(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Move a down (shift+down)
	m = sendKey(t, m, runeKey('J'))
	assert.Equal(t, "b", m.nodes[0].Ref.Branch, "b should now be at index 0")
	assert.Equal(t, "a", m.nodes[1].Ref.Branch, "a should now be at index 1")
	assert.Equal(t, 1, m.cursor)

	// Undo → should swap back
	m = sendKey(t, m, runeKey('z'))
	assert.Equal(t, "a", m.nodes[0].Ref.Branch, "a should be back at index 0")
	assert.Equal(t, "b", m.nodes[1].Ref.Branch, "b should be back at index 1")
	assert.Equal(t, 0, m.cursor, "cursor should return to original position")
}

func TestUndoNothingShowsMessage(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Undo with empty stack
	m = sendKey(t, m, runeKey('z'))
	assert.Equal(t, "Nothing to undo", m.statusMessage)
	assert.False(t, m.statusIsError)
}

// --- Getters tests ---

func TestGetters(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	assert.False(t, m.Applied())
	assert.False(t, m.Cancelled())
	assert.False(t, m.ApplyRequested())
	assert.Len(t, m.Nodes(), 2)
	assert.Equal(t, "a", m.Nodes()[0].Ref.Branch)
	assert.Equal(t, "b", m.Nodes()[1].Ref.Branch)
}

// --- Merged branch guard tests ---

func TestCannotDropMergedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeMergedNode("merged", 0),
		makeNode("active", true, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Cursor on merged node
	m.cursor = 0
	m = sendKey(t, m, runeKey('x'))
	assert.Nil(t, m.nodes[0].PendingAction)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "merged")
}

func TestCannotFoldMergedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeMergedNode("merged", 0),
		makeNode("active", true, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	assert.Nil(t, m.nodes[0].PendingAction)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "merged")
}

// --- Drop target protected by fold ---

func TestCannotDropFoldTarget(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold b down into c
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[1].PendingAction.Type)

	// Move cursor to c (fold target)
	m = sendKey(t, m, runeKey('j'))
	require.Equal(t, 2, m.cursor)

	// Try to drop c → should be refused because b is folding into c
	m = sendKey(t, m, runeKey('x'))
	assert.Nil(t, m.nodes[2].PendingAction, "fold target should not be droppable")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding into")
}

// --- Help toggle ---

func TestHelpToggle(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Open help
	m = sendKey(t, m, runeKey('?'))
	assert.True(t, m.showHelp)

	// Close help with '?'
	m = sendKey(t, m, runeKey('?'))
	assert.False(t, m.showHelp)

	// Open and close with Escape
	m = sendKey(t, m, runeKey('?'))
	assert.True(t, m.showHelp)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	assert.False(t, m.showHelp)
}

// --- Status message cleared on keypress ---

func TestStatusMessageClearedOnKeypress(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Generate an error
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotEmpty(t, m.statusMessage)

	// Any key press clears the message
	m = sendKey(t, m, runeKey('j'))
	assert.Empty(t, m.statusMessage)
	assert.False(t, m.statusIsError)
}

func TestPendingChangeSummary(t *testing.T) {
	t.Run("no changes returns empty", func(t *testing.T) {
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "", result)
	})

	t.Run("one drop", func(t *testing.T) {
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &PendingAction{Type: ActionDrop},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 1 drop", result)
	})

	t.Run("multiple drops uses plural", func(t *testing.T) {
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &PendingAction{Type: ActionDrop},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
				PendingAction:    &PendingAction{Type: ActionDrop},
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 2 drops", result)
	})

	t.Run("mixed actions", func(t *testing.T) {
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &PendingAction{Type: ActionDrop},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
				PendingAction:    &PendingAction{Type: ActionFoldDown},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b3"}},
				OriginalPosition: 2,
				PendingAction:    &PendingAction{Type: ActionFoldUp},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b4"}},
				OriginalPosition: 3,
				PendingAction:    &PendingAction{Type: ActionRename, NewName: "b4-new"},
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 1 drop, 2 folds, 1 rename", result)
	})

	t.Run("position change counts as move", func(t *testing.T) {
		// b2 moved to position 0, b1 moved to position 1
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1, // moved from 1 to 0
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0, // moved from 0 to 1
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 2 moves", result)
	})

	t.Run("removed nodes with position change not counted as move", func(t *testing.T) {
		// A removed node with a different position should not add to moves
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 1,
				Removed:          true,
				PendingAction:    &PendingAction{Type: ActionDrop},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 1 drop", result)
	})

	t.Run("one rename singular", func(t *testing.T) {
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &PendingAction{Type: ActionRename, NewName: "feature"},
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 1 rename", result)
	})

	t.Run("one fold singular", func(t *testing.T) {
		nodes := []ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &PendingAction{Type: ActionFoldDown},
			},
		}

		result := pendingChangeSummary(nodes)
		assert.Equal(t, "Pending: 1 fold", result)
	})
}

func TestPluralize(t *testing.T) {
	assert.Equal(t, "drop", pluralize(1, "drop", "drops"))
	assert.Equal(t, "drops", pluralize(2, "drop", "drops"))
	assert.Equal(t, "drops", pluralize(0, "drop", "drops"))
}
