package submitview

import (
	"testing"

	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/stretchr/testify/assert"
)

func TestBranchStatePredicates(t *testing.T) {
	tests := []struct {
		state      BranchState
		selectable bool
		editable   bool
		locked     bool
		blocks     bool
	}{
		{StateNew, true, true, false, false},
		{StateOpen, false, false, true, false},
		{StateDraft, false, false, true, false},
		{StateQueued, false, false, true, false},
		{StateMerged, false, false, true, false},
		{StateClosed, false, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.state.Label(), func(t *testing.T) {
			assert.Equal(t, tt.selectable, tt.state.Selectable())
			assert.Equal(t, tt.editable, tt.state.Editable())
			assert.Equal(t, tt.locked, tt.state.Locked())
			assert.Equal(t, tt.blocks, tt.state.Blocks())
		})
	}
}

func TestSubmitNodeEdited(t *testing.T) {
	base := SubmitNode{
		BranchNode:   stackview.BranchNode{Ref: stack.BranchRef{Branch: "feat/a"}},
		State:        StateNew,
		Included:     true,
		Title:        "Title",
		Description:  "Desc",
		Draft:        false,
		titlePrefill: "Title",
		descPrefill:  "Desc",
		draftPrefill: false,
	}

	t.Run("unchanged is not edited", func(t *testing.T) {
		assert.False(t, base.Edited())
	})

	t.Run("title change is edited", func(t *testing.T) {
		n := base
		n.Title = "New title"
		assert.True(t, n.Edited())
	})

	t.Run("description change is edited", func(t *testing.T) {
		n := base
		n.Description = "New desc"
		assert.True(t, n.Edited())
	})

	t.Run("draft toggle is edited", func(t *testing.T) {
		n := base
		n.Draft = true
		assert.True(t, n.Edited())
	})

	t.Run("deselection is edited", func(t *testing.T) {
		n := base
		n.Included = false
		assert.True(t, n.Edited())
	})

	t.Run("non-new node is never edited", func(t *testing.T) {
		n := base
		n.State = StateOpen
		n.Title = "changed"
		assert.False(t, n.Edited())
	})
}

func TestToDraft(t *testing.T) {
	n := SubmitNode{
		BranchNode:  stackview.BranchNode{Ref: stack.BranchRef{Branch: "feat/a"}},
		State:       StateNew,
		Included:    true,
		Title:       "Add feature",
		Description: "Body",
		Draft:       true,
	}
	got := n.ToDraft()
	assert.Equal(t, PRDraft{
		Branch:  "feat/a",
		Include: true,
		Title:   "Add feature",
		Body:    "Body",
		Draft:   true,
	}, got)
}

func TestBuildDrafts(t *testing.T) {
	nodes := []SubmitNode{
		{State: StateNew, Included: true, Title: "A", BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "feat/a"}}},
		{State: StateNew, Included: false, Title: "B", BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "feat/b"}}},
		{State: StateOpen, BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "feat/c"}}},
	}

	drafts := BuildDrafts(nodes)

	assert.Len(t, drafts, 2, "only NEW branches produce drafts")
	assert.NotNil(t, drafts["feat/a"])
	assert.True(t, drafts["feat/a"].Include)
	assert.NotNil(t, drafts["feat/b"])
	assert.False(t, drafts["feat/b"].Include)
	assert.Nil(t, drafts["feat/c"], "non-new branch has no draft")
}
