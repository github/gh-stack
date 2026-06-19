package submitview

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmit_EmptyTitleGuard(t *testing.T) {
	m := testModel(t, newNodes())
	m.titleInput.SetValue("") // blank the focused branch's title

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(Model)
	assert.False(t, m.submitRequested)
	assert.True(t, m.statusIsError)
	assert.Nil(t, cmd)
	assert.Equal(t, fieldTitle, m.focusedField)
	assert.Equal(t, 1, m.cursor)
}

func TestSubmit_SucceedsWhenTitlesPresent(t *testing.T) {
	m := testModel(t, newNodes())
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(Model)
	assert.True(t, m.submitRequested)
	assert.NotNil(t, cmd)
}

func TestSubmit_SkippedBranchWithoutTitleDoesNotBlock(t *testing.T) {
	m := testModel(t, newNodes())
	// Blank the first branch's title, then skip it; submit should proceed.
	m.titleInput.SetValue("")
	m.saveEditor()
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip branch 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(Model)
	assert.True(t, m.submitRequested, "a skipped branch's empty title does not block submit")
}

func TestBuildDrafts_FromModel(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp}) // focus the top branch (0)
	require.Equal(t, 0, m.cursor)
	m.nodes[1].Title = "Custom title"
	// Skip the top branch (branch 0); the branch below it stays included.
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	require.False(t, m.nodes[0].Included)
	require.True(t, m.nodes[1].Included)

	drafts := BuildDrafts(m.Nodes())
	require.NotNil(t, drafts["feat/auth/tests"])
	assert.False(t, drafts["feat/auth/tests"].Include, "skipped branch")
	require.NotNil(t, drafts["feat/auth/middleware"])
	assert.True(t, drafts["feat/auth/middleware"].Include, "still included")
	assert.Equal(t, "Custom title", drafts["feat/auth/middleware"].Title)
	// Locked branches never produce drafts.
	assert.Nil(t, drafts["feat/auth/handlers"])
}
