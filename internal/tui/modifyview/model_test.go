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

func TestFoldTargetOfFoldBlocked(t *testing.T) {
	// With 3 nodes A(0), B(1), C(2): fold B down into C, then try
	// to fold C up. C is the target of B's fold, so C should not
	// be allowed to fold.
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

	// Try fold C up — C is the target of B's fold-down, so blocked
	m = sendKey(t, m, runeKey('u'))
	assert.Nil(t, m.nodes[2].PendingAction, "C should not fold because B is folding into it")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding into this branch")
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

func TestUndoRename(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Simulate a rename on node at index 1 (bypass git validation)
	m.nodes[1].PendingAction = &PendingAction{Type: ActionRename, NewName: "b-renamed"}
	m.actionStack = append(m.actionStack, StagedAction{
		Type:         ActionRename,
		BranchName:   "b",
		OriginalName: "b",
		NewName:      "b-renamed",
	})

	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionRename, m.nodes[1].PendingAction.Type)
	assert.Equal(t, "b-renamed", m.nodes[1].PendingAction.NewName)

	// Undo
	m = sendKey(t, m, runeKey('z'))
	assert.Nil(t, m.nodes[1].PendingAction, "PendingAction should be cleared after undo")
}

func TestUndoRename_DoesNotAffectOtherRenames(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Simulate rename on node 0 (first rename)
	m.nodes[0].PendingAction = &PendingAction{Type: ActionRename, NewName: "a-renamed"}
	m.actionStack = append(m.actionStack, StagedAction{
		Type:         ActionRename,
		BranchName:   "a",
		OriginalName: "a",
		NewName:      "a-renamed",
	})

	// Simulate rename on node 2 (second rename)
	m.nodes[2].PendingAction = &PendingAction{Type: ActionRename, NewName: "c-renamed"}
	m.actionStack = append(m.actionStack, StagedAction{
		Type:         ActionRename,
		BranchName:   "c",
		OriginalName: "c",
		NewName:      "c-renamed",
	})

	// Undo the second rename
	m = sendKey(t, m, runeKey('z'))
	assert.Nil(t, m.nodes[2].PendingAction, "second rename should be undone")
	require.NotNil(t, m.nodes[0].PendingAction, "first rename should still be intact")
	assert.Equal(t, "a-renamed", m.nodes[0].PendingAction.NewName, "first rename new name should be unchanged")
}

func TestCursorNavigation_SkipsMergedBranches(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeMergedNode("merged", 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 0, m.cursor, "cursor should start on first non-merged")

	// Move down should skip merged and land on c
	m = sendKey(t, m, runeKey('j'))
	assert.Equal(t, 2, m.cursor, "down should skip merged branch")

	// Move up should skip merged and land back on a
	m = sendKey(t, m, runeKey('k'))
	assert.Equal(t, 0, m.cursor, "up should skip merged branch")
}

// --- Insert tests ---

// simulateInsert enters insert mode, types a branch name, and presses Enter.
func simulateInsert(t *testing.T, m Model, name string) Model {
	t.Helper()
	require.True(t, m.insertMode, "expected insert mode to be active")
	for _, r := range name {
		m = sendKey(t, m, runeKey(r))
	}
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	return m
}

func TestInsertBelow(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 0, m.cursor) // cursor on a

	// Press 'i' to insert below
	m = sendKey(t, m, runeKey('i'))
	require.True(t, m.insertMode, "should enter insert mode")
	assert.Equal(t, ActionInsertBelow, m.insertDirection)

	// Type a branch name and confirm
	m = simulateInsert(t, m, "new-branch")
	require.False(t, m.insertMode, "should exit insert mode")

	// New node should be at index 1 (below cursor at 0)
	require.Len(t, m.nodes, 4)
	assert.Equal(t, "a", m.nodes[0].Ref.Branch)
	assert.Equal(t, "new-branch", m.nodes[1].Ref.Branch)
	assert.True(t, m.nodes[1].IsInserted)
	assert.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionInsertBelow, m.nodes[1].PendingAction.Type)
	assert.Equal(t, "new-branch", m.nodes[1].PendingAction.NewName)
	assert.Equal(t, "b", m.nodes[2].Ref.Branch)
	assert.Equal(t, "c", m.nodes[3].Ref.Branch)

	// Cursor should be on the new node
	assert.Equal(t, 1, m.cursor)
}

func TestInsertAbove(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 1, m.cursor) // cursor on b

	// Press 'I' (shift+i) to insert above
	m = sendKey(t, m, runeKey('I'))
	require.True(t, m.insertMode, "should enter insert mode")
	assert.Equal(t, ActionInsertAbove, m.insertDirection)

	// Type a branch name and confirm
	m = simulateInsert(t, m, "above-b")
	require.False(t, m.insertMode, "should exit insert mode")

	// New node should be at index 1 (above cursor which was at 1)
	require.Len(t, m.nodes, 4)
	assert.Equal(t, "a", m.nodes[0].Ref.Branch)
	assert.Equal(t, "above-b", m.nodes[1].Ref.Branch)
	assert.True(t, m.nodes[1].IsInserted)
	assert.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionInsertAbove, m.nodes[1].PendingAction.Type)
	assert.Equal(t, "b", m.nodes[2].Ref.Branch)
	assert.Equal(t, "c", m.nodes[3].Ref.Branch)

	// Cursor should be on the new node
	assert.Equal(t, 1, m.cursor)
}

func TestInsertAtTop(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")
	require.Equal(t, 0, m.cursor)

	// Insert above the top — new node becomes index 0
	m = sendKey(t, m, runeKey('I'))
	m = simulateInsert(t, m, "top")

	require.Len(t, m.nodes, 3)
	assert.Equal(t, "top", m.nodes[0].Ref.Branch)
	assert.True(t, m.nodes[0].IsInserted)
	assert.Equal(t, "a", m.nodes[1].Ref.Branch)
	assert.Equal(t, "b", m.nodes[2].Ref.Branch)
	assert.Equal(t, 0, m.cursor)
}

func TestInsertAtBottom(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")
	// Move cursor to bottom
	m = sendKey(t, m, runeKey('j'))
	// cursor already starts at 1 (b is current)
	require.Equal(t, 1, m.cursor)

	// Insert below the bottom — new node appended
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "bottom")

	require.Len(t, m.nodes, 3)
	assert.Equal(t, "a", m.nodes[0].Ref.Branch)
	assert.Equal(t, "b", m.nodes[1].Ref.Branch)
	assert.Equal(t, "bottom", m.nodes[2].Ref.Branch)
	assert.True(t, m.nodes[2].IsInserted)
	assert.Equal(t, 2, m.cursor)
}

func TestUndoInsert(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert below a
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	require.Len(t, m.nodes, 4)
	assert.Equal(t, 1, m.cursor)

	// Undo
	m = sendKey(t, m, runeKey('z'))
	require.Len(t, m.nodes, 3, "inserted node should be removed")
	assert.Equal(t, "a", m.nodes[0].Ref.Branch)
	assert.Equal(t, "b", m.nodes[1].Ref.Branch)
	assert.Equal(t, "c", m.nodes[2].Ref.Branch)
}

func TestInsertBlockedInReorderMode(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Reorder: move a down
	m = sendKey(t, m, runeKey('J'))
	assert.Equal(t, modeReorder, m.currentMode())

	// Try insert below → should be blocked
	m = sendKey(t, m, runeKey('i'))
	assert.False(t, m.insertMode)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")

	// Try insert above → should be blocked
	m = sendKey(t, m, runeKey('I'))
	assert.False(t, m.insertMode)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")
}

func TestReorderBlockedAfterInsert(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	assert.Equal(t, modeStructure, m.currentMode())

	// Try reorder → should be blocked
	m = sendKey(t, m, runeKey('J'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "undo")
}

func TestInsertCannotInsertOnMergedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeMergedNode("merged", 0),
		makeNode("active", true, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Force cursor to merged node
	m.cursor = 0
	m = sendKey(t, m, runeKey('i'))
	assert.False(t, m.insertMode)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "merged")
}

func TestInsertCancelledWithEscape(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Start insert
	m = sendKey(t, m, runeKey('i'))
	require.True(t, m.insertMode)

	// Cancel with Escape
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	assert.False(t, m.insertMode)
	assert.Len(t, m.nodes, 2, "no node should be added on cancel")
}

func TestInsertEmptyNameCancels(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Start insert, press Enter with empty input
	m = sendKey(t, m, runeKey('i'))
	require.True(t, m.insertMode)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, m.insertMode)
	assert.Len(t, m.nodes, 2, "no node should be added on empty input")
}

func TestInsertDuplicateNameBlocked(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Try to insert with existing branch name "b"
	m = sendKey(t, m, runeKey('i'))
	require.True(t, m.insertMode)
	m = simulateInsert(t, m, "b")
	// Should still be in insert mode with an error
	assert.True(t, m.insertMode, "should stay in insert mode on validation error")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "already used")
}

func TestInsertCountedInPendingSummary(t *testing.T) {
	nodes := []ModifyBranchNode{
		{
			BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "new"}},
			OriginalPosition: -1,
			IsInserted:       true,
			PendingAction:    &PendingAction{Type: ActionInsertBelow, NewName: "new"},
		},
		{
			BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b"}},
			OriginalPosition: 0,
		},
	}

	result := pendingChangeSummary(nodes)
	assert.Equal(t, "Pending: 1 insert", result)
}

func TestInsertPluralInPendingSummary(t *testing.T) {
	nodes := []ModifyBranchNode{
		{
			BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "new1"}},
			OriginalPosition: -1,
			IsInserted:       true,
			PendingAction:    &PendingAction{Type: ActionInsertBelow, NewName: "new1"},
		},
		{
			BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b"}},
			OriginalPosition: 0,
		},
		{
			BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "new2"}},
			OriginalPosition: -1,
			IsInserted:       true,
			PendingAction:    &PendingAction{Type: ActionInsertAbove, NewName: "new2"},
		},
	}

	result := pendingChangeSummary(nodes)
	assert.Equal(t, "Pending: 2 inserts", result)
}

func TestInsertMixedWithOtherActions(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Drop a
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[0].PendingAction)
	assert.Equal(t, ActionDrop, m.nodes[0].PendingAction.Type)

	// Move cursor to b and insert below it
	m = sendKey(t, m, runeKey('j'))
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")

	// Should now have 4 nodes: a(dropped), b, new-branch, c
	require.Len(t, m.nodes, 4)
	assert.Equal(t, modeStructure, m.currentMode())
	assert.Equal(t, ActionDrop, m.nodes[0].PendingAction.Type)
	assert.Equal(t, "new-branch", m.nodes[2].Ref.Branch)
	assert.True(t, m.nodes[2].IsInserted)
}

func TestInsertOnRemovedBranchDoesNothing(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Drop a
	m = sendKey(t, m, runeKey('x'))
	require.True(t, m.nodes[0].Removed)

	// Force cursor back to removed node and try to insert
	m.cursor = 0
	prevLen := len(m.nodes)
	m = sendKey(t, m, runeKey('i'))
	assert.False(t, m.insertMode, "should not enter insert mode on removed branch")
	assert.Len(t, m.nodes, prevLen)
}

func TestCurrentModeStructureAfterInsert(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")
	assert.Equal(t, modeNone, m.currentMode())

	// Insert
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	assert.Equal(t, modeStructure, m.currentMode())
}

func TestApplyWithInsert(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert a branch
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")

	// Apply should be accepted
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	assert.True(t, m.applyRequested)
}

func TestInsertAnnotation(t *testing.T) {
	node := ModifyBranchNode{
		BranchNode: stackview.BranchNode{
			Ref: stack.BranchRef{Branch: "new-branch"},
		},
		PendingAction:    &PendingAction{Type: ActionInsertBelow, NewName: "new-branch"},
		OriginalPosition: -1,
		IsInserted:       true,
	}

	annotation := nodeAnnotation(node, 0)
	require.NotNil(t, annotation)
	assert.Equal(t, "✚ insert", annotation.Text)
}

// --- Bug fix tests: insert should not cause false "moved" annotations ---

func TestInsertDoesNotShowMovedAnnotation(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert below b (cursor at 1)
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")

	// Nodes are now: a(0), b(1), new-branch(inserted), c(2)
	// Verify c does NOT have a "moved" annotation
	require.Len(t, m.nodes, 4)
	assert.Equal(t, "c", m.nodes[3].Ref.Branch)

	// Use effectiveIdx=2 for c (skipping the inserted node)
	annotation := nodeAnnotation(m.nodes[3], 2)
	assert.Nil(t, annotation, "c should not show a moved annotation after insert")

	// Also verify a and b have no annotation
	assert.Nil(t, nodeAnnotation(m.nodes[0], 0), "a should have no annotation")
	assert.Nil(t, nodeAnnotation(m.nodes[1], 1), "b should have no annotation")
}

// --- Bug fix tests: header branch count excludes inserts ---

func TestBranchCountExcludesInserts(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")
	m.width = 120
	m.height = 40

	// Branch count should be 3
	cfg := m.buildHeaderConfig()
	assert.Contains(t, cfg.InfoLines[1].Label, "3 branches")

	// Insert a branch
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")

	// Branch count should still be 3 (not 4)
	cfg = m.buildHeaderConfig()
	assert.Contains(t, cfg.InfoLines[1].Label, "3 branches")
}

// --- Bug fix tests: operations blocked on inserted nodes ---

func TestCannotRenameInsertedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert a branch and cursor lands on it
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	require.Equal(t, 1, m.cursor)
	require.True(t, m.nodes[1].IsInserted)

	// Rename the inserted branch — should update the insert name directly
	m = sendKey(t, m, runeKey('r'))
	require.True(t, m.renameMode, "should enter rename mode on inserted branch")

	// Clear existing text and type new name
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlU}) // clear line
	for _, r := range "better-name" {
		m = sendKey(t, m, runeKey(r))
	}
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, m.renameMode)

	// The node's branch name and insert action should be updated
	assert.Equal(t, "better-name", m.nodes[1].Ref.Branch)
	assert.True(t, m.nodes[1].IsInserted, "should still be marked as inserted")
	assert.Equal(t, ActionInsertBelow, m.nodes[1].PendingAction.Type, "action type should still be insert")
	assert.Equal(t, "better-name", m.nodes[1].PendingAction.NewName)

	// No separate rename action in the undo stack — only the original insert
	for _, a := range m.actionStack {
		assert.NotEqual(t, ActionRename, a.Type, "should not have a rename action in undo stack")
	}
}

func TestCannotFoldInsertedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert a branch
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	require.True(t, m.nodes[1].IsInserted)

	// Try fold down → should be blocked
	m = sendKey(t, m, runeKey('d'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "inserted")

	// Try fold up → should be blocked
	m = sendKey(t, m, runeKey('u'))
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "inserted")
}

func TestDropInsertedBranchRemovesIt(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert a branch below a
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	require.Len(t, m.nodes, 4)
	require.True(t, m.nodes[1].IsInserted)

	// Drop (x) the inserted branch → should remove it entirely
	m = sendKey(t, m, runeKey('x'))
	assert.Len(t, m.nodes, 3, "inserted node should be removed")
	assert.Equal(t, "a", m.nodes[0].Ref.Branch)
	assert.Equal(t, "b", m.nodes[1].Ref.Branch)
	assert.Equal(t, "c", m.nodes[2].Ref.Branch)
}

func TestDropInsertedBranchCanBeUndone(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert, then drop it
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	require.Len(t, m.nodes, 3)

	m = sendKey(t, m, runeKey('x'))
	require.Len(t, m.nodes, 2)
	require.Empty(t, m.actionStack, "undo stack should be empty after dropping an insert")

	// Dropping an inserted branch cancels the insert — undo stack should
	// no longer contain the insert action. Pressing z undoes whatever came
	// before the insert (i.e., nothing), not the drop itself.
	m = sendKey(t, m, runeKey('z'))
	assert.Len(t, m.nodes, 2, "undo should not re-insert — the drop cancelled the insert")
	assert.Equal(t, "Nothing to undo", m.statusMessage)
}

func TestCannotDropAllOriginalBranchesWithInsert(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert a new branch
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	require.Len(t, m.nodes, 3)

	// Drop a
	m.cursor = 0
	m = sendKey(t, m, runeKey('x'))
	require.NotNil(t, m.nodes[0].PendingAction)
	assert.Equal(t, ActionDrop, m.nodes[0].PendingAction.Type)

	// Move to b and try to drop it — should be refused since only
	// the inserted branch would remain
	m = sendKey(t, m, runeKey('j'))
	m = sendKey(t, m, runeKey('j'))
	require.Equal(t, "b", m.nodes[2].Ref.Branch)
	m = sendKey(t, m, runeKey('x'))
	assert.Nil(t, m.nodes[2].PendingAction, "should not be able to drop the last original branch")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "last branch")
}

func TestCannotFoldIntoInsertedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", true, 0),
		makeNode("b", false, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert below a
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	// nodes: a(0), new-branch(1, inserted), b(2), c(3)
	require.True(t, m.nodes[1].IsInserted)

	// Move cursor to a and try fold down — target would be the inserted branch
	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	assert.Nil(t, m.nodes[0].PendingAction, "should not fold into an inserted branch")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "inserted")
}

func TestCannotFoldUpIntoInsertedBranch(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Insert above b (at index 1)
	m = sendKey(t, m, runeKey('I'))
	m = simulateInsert(t, m, "new-branch")
	// nodes: a(0), new-branch(1, inserted), b(2), c(3)
	require.True(t, m.nodes[1].IsInserted)

	// Move cursor to b and try fold up — target would be the inserted branch
	m.cursor = 2
	m = sendKey(t, m, runeKey('u'))
	assert.Nil(t, m.nodes[2].PendingAction, "should not fold up into an inserted branch")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "inserted")
}

func TestDropBlockedWhenFoldWouldRetargetToInserted(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold a down into b
	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[0].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[0].PendingAction.Type)

	// Insert between b and c
	m.cursor = 1
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	// nodes: a(0, fold-down), b(1), new-branch(2, inserted), c(3)

	// Try to drop b — fold-down on a currently targets b.
	// If b is dropped, fold target shifts to new-branch (inserted) → block
	m.cursor = 1
	m = sendKey(t, m, runeKey('x'))
	assert.Nil(t, m.nodes[1].PendingAction, "drop should be blocked")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "fold")
}

func TestCannotMutualFold(t *testing.T) {
	// Scenario: a folds down into b, then try to fold b up.
	// Since a is Removed after folding, b's fold-up target skips a.
	// But b is the target of a's fold — so b should not be allowed to fold.
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold a down (target = b)
	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[0].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[0].PendingAction.Type)

	// Move to b — b is the target of a's fold.
	// Try to fold b up — should be blocked because a is folding into b.
	m.cursor = 1
	m = sendKey(t, m, runeKey('u'))
	assert.Nil(t, m.nodes[1].PendingAction, "should not fold a branch that is receiving a fold")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding into this branch")
}

func TestCannotMutualFold_FourNodes(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", false, 1),
		makeNode("c", true, 2),
		makeNode("d", false, 3),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold c down into d
	m.cursor = 2
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[2].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[2].PendingAction.Type)

	// Now try fold d up — d is c's fold target, so d can't fold
	m.cursor = 3
	m = sendKey(t, m, runeKey('u'))
	assert.Nil(t, m.nodes[3].PendingAction, "should not fold a branch that is receiving a fold")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding into this branch")

	// But folding d down should still work (it's not mutual)
	m = sendKey(t, m, runeKey('d'))
	// d has no branch below, so this should fail with "No branch below"
	assert.True(t, m.statusIsError)
}

func TestCannotFoldTargetOfFold(t *testing.T) {
	// b folds up into a, then try to fold a down — a is receiving b's fold
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold b up into a
	m.cursor = 1
	m = sendKey(t, m, runeKey('u'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldUp, m.nodes[1].PendingAction.Type)

	// Try to fold a down — a is b's fold target, should be blocked
	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	assert.Nil(t, m.nodes[0].PendingAction, "should not fold a branch that is receiving a fold")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding into this branch")
}

func TestInsertBlockedBetweenFoldDownAndTarget(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold a down into b
	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[0].PendingAction)
	assert.Equal(t, ActionFoldDown, m.nodes[0].PendingAction.Type)

	// Try inserting above b (between a and b) — should be blocked immediately
	m.cursor = 1
	m = sendKey(t, m, runeKey('I'))
	assert.False(t, m.insertMode, "should not enter insert mode")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding")
}

func TestInsertBlockedBetweenFoldUpAndTarget(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold b up into a
	m.cursor = 1
	m = sendKey(t, m, runeKey('u'))
	require.NotNil(t, m.nodes[1].PendingAction)
	assert.Equal(t, ActionFoldUp, m.nodes[1].PendingAction.Type)

	// Try inserting below a (between a and b) — should be blocked immediately
	m.cursor = 0
	m = sendKey(t, m, runeKey('i'))
	assert.False(t, m.insertMode, "should not enter insert mode")
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "folding")
}

func TestInsertAllowedOutsideFoldRange(t *testing.T) {
	nodes := []ModifyBranchNode{
		makeNode("a", false, 0),
		makeNode("b", true, 1),
		makeNode("c", false, 2),
		makeNode("d", false, 3),
	}
	m := New(nodes, testTrunk, "1.0.0")

	// Fold a down into b
	m.cursor = 0
	m = sendKey(t, m, runeKey('d'))
	require.NotNil(t, m.nodes[0].PendingAction)

	// Inserting below c (between c and d) should be fine — outside fold range
	m.cursor = 2
	m = sendKey(t, m, runeKey('i'))
	m = simulateInsert(t, m, "new-branch")
	assert.False(t, m.insertMode, "should succeed")
	assert.Len(t, m.nodes, 5)
	assert.Equal(t, "new-branch", m.nodes[3].Ref.Branch)
}
