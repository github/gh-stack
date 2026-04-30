package modifyview

import (
	"testing"

	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/stretchr/testify/assert"
)

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
