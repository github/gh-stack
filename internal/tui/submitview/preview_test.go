package submitview

import (
	"os"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTogglePreview(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	require.Equal(t, fieldDescription, m.focusedField)
	require.True(t, m.descArea.Focused())

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	assert.True(t, m.descPreview)
	assert.False(t, m.descArea.Focused(), "textarea blurs in preview")

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	assert.False(t, m.descPreview)
	assert.True(t, m.descArea.Focused(), "textarea refocuses on returning to edit")
}

func TestResolveEditor(t *testing.T) {
	t.Setenv("GH_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	assert.Equal(t, "", resolveEditor())

	t.Setenv("EDITOR", "nano")
	assert.Equal(t, "nano", resolveEditor())
	t.Setenv("VISUAL", "vim")
	assert.Equal(t, "vim", resolveEditor())
	t.Setenv("GH_EDITOR", "code --wait")
	assert.Equal(t, "code --wait", resolveEditor())
}

func TestOpenEditor_NoEditorSet(t *testing.T) {
	t.Setenv("GH_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	m := testModel(t, newNodes())
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(Model)
	assert.Nil(t, cmd)
	assert.True(t, m.statusIsError)
	assert.Contains(t, m.statusMessage, "EDITOR")
}

func TestHandleEditorFinished_UpdatesDescription(t *testing.T) {
	m := testModel(t, newNodes())
	f, err := os.CreateTemp(t.TempDir(), "ed-*.md")
	require.NoError(t, err)
	_, _ = f.WriteString("Edited externally\n")
	require.NoError(t, f.Close())

	updated, _ := m.Update(editorFinishedMsg{path: f.Name()})
	m = updated.(Model)
	assert.Equal(t, "Edited externally", m.nodes[1].Description)

	_, statErr := os.Stat(f.Name())
	assert.True(t, os.IsNotExist(statErr))
}

// cmdMessages flattens the message(s) a command (possibly a tea.Batch) emits.
func cmdMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, cmdMessages(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

func containsType(msgs []tea.Msg, want tea.Msg) bool {
	wt := reflect.TypeOf(want)
	for _, m := range msgs {
		if reflect.TypeOf(m) == wt {
			return true
		}
	}
	return false
}

func TestEditorFinished_ReenablesMouse(t *testing.T) {
	// Bubble Tea's RestoreTerminal (run after the external editor exits) does not
	// re-enable mouse tracking, so the editor-finished handler must re-arm it or
	// the mouse stops working after the editor closes.
	want := tea.EnableMouseCellMotion()

	t.Run("success", func(t *testing.T) {
		m := testModel(t, newNodes())
		f, err := os.CreateTemp(t.TempDir(), "ed-*.md")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		_, cmd := m.Update(editorFinishedMsg{path: f.Name()})
		assert.True(t, containsType(cmdMessages(cmd), want), "mouse tracking is re-enabled after a clean editor exit")
	})

	t.Run("editor error", func(t *testing.T) {
		m := testModel(t, newNodes())
		_, cmd := m.Update(editorFinishedMsg{path: "/no/such/file", err: assert.AnError})
		assert.True(t, containsType(cmdMessages(cmd), want), "mouse tracking is re-enabled even when the editor errored")
	})
}

func TestRenderMarkdown(t *testing.T) {
	assert.Contains(t, renderMarkdown("", 40), "no description")
	// Glamour styles each word as a separate ANSI span, so assert on the words
	// individually rather than the literal "Hello World".
	out := renderMarkdown("# Hello World", 40)
	assert.Contains(t, out, "Hello")
	assert.Contains(t, out, "World")
}

func TestPreview_RendersWithoutHanging(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description (saves prefill)
	// Set the description after focusing so the Tab's saveEditor doesn't clobber it.
	m.nodes[1].Description = "# Heading\n\nBody text here."
	m.descArea.SetValue("# Heading\n\nBody text here.")
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlP}) // switch to preview
	require.True(t, m.descPreview)
	// View() exercises the Glamour render path; with a fixed style it must not
	// query the terminal (which would block) and must show the content.
	out := m.View()
	assert.Contains(t, out, "Heading")
}
