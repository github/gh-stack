package submitview

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestStateLabel(t *testing.T) {
	cases := map[BranchState]string{
		StateNew:    "NEW",
		StateOpen:   "OPEN",
		StateDraft:  "DRAFT",
		StateQueued: "QUEUED",
		StateMerged: "MERGED",
		StateClosed: "CLOSED",
	}
	for state, want := range cases {
		assert.Equal(t, want, state.Label())
	}
}

func TestStateColorAndDot(t *testing.T) {
	for _, s := range []BranchState{StateNew, StateOpen, StateDraft, StateQueued, StateMerged, StateClosed} {
		assert.NotEqual(t, lipgloss.Color(""), s.Color(), "state %s should have a color", s.Label())
		assert.NotEmpty(t, s.Dot(), "state %s should have a dot", s.Label())
	}
}

func TestRenderBadgeContainsLabel(t *testing.T) {
	// The rendered badge may contain ANSI escapes; assert the label text is
	// present regardless of styling.
	for _, s := range []BranchState{StateNew, StateOpen, StateDraft, StateQueued, StateMerged, StateClosed} {
		out := RenderBadge(s)
		assert.Contains(t, out, s.Label())
	}
}

func TestRenderDotContainsGlyph(t *testing.T) {
	assert.Contains(t, RenderDot(StateNew), StateNew.Dot())
}
