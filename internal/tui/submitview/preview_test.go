package submitview

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/github/gh-stack/internal/tui/shared"
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
	t.Setenv("PATH", "")
	assert.Equal(t, "", resolveEditor())

	t.Setenv("EDITOR", "nano")
	assert.Equal(t, "nano", resolveEditor())
	t.Setenv("VISUAL", "vim")
	assert.Equal(t, "vim", resolveEditor())
	t.Setenv("GH_EDITOR", "code --wait")
	assert.Equal(t, "code --wait", resolveEditor())

	t.Setenv("GH_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	binDir := t.TempDir()
	viName := "vi"
	if runtime.GOOS == "windows" {
		viName += ".exe"
	}
	require.NoError(t, os.WriteFile(filepath.Join(binDir, viName), nil, 0o755))
	t.Setenv("PATH", binDir)
	assert.Equal(t, "vi", resolveEditor())
	t.Setenv("EDITOR", "nano")
	assert.Equal(t, "nano", resolveEditor())
}

func TestOpenEditor_NoEditorSet(t *testing.T) {
	t.Setenv("GH_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", "")

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

func TestMarkdownStyle(t *testing.T) {
	for _, dark := range []bool{true, false} {
		pick := func(c lipgloss.AdaptiveColor) string {
			if dark {
				return c.Dark
			}
			return c.Light
		}
		style := markdownStyle(dark)

		require.NotNil(t, style.Document.Margin)
		assert.Zero(t, *style.Document.Margin, "flush with the edit-mode textarea")
		assert.Equal(t, pick(shared.ColorText), *style.Document.Color)
		assert.Equal(t, pick(shared.ColorText), *style.Heading.Color)
		assert.Equal(t, "# ", style.H1.Prefix, "H1 is marked like the other heading levels")
		assert.Nil(t, style.H1.BackgroundColor, "no colored H1 pill")
		assert.Equal(t, pick(shared.ColorAccent), *style.Link.Color)
		assert.Equal(t, pick(shared.ColorAccent), *style.LinkText.Color)
		assert.Equal(t, pick(shared.ColorCodeBg), *style.Code.BackgroundColor)
		assert.Nil(t, style.CodeBlock.Chroma, "code blocks use a named Chroma theme")
	}
	assert.Equal(t, "github-dark", markdownStyle(true).CodeBlock.Theme)
	assert.Equal(t, "github", markdownStyle(false).CodeBlock.Theme)

	// The base styles are copied, never modified.
	assert.Equal(t, " ", styles.DarkStyleConfig.H1.Prefix)
	assert.NotNil(t, styles.DarkStyleConfig.H1.BackgroundColor)
	assert.NotNil(t, styles.LightStyleConfig.CodeBlock.Chroma)
}

func TestChromaFormatter(t *testing.T) {
	assert.Equal(t, "terminal16m", chromaFormatter(termenv.TrueColor))
	assert.Equal(t, "terminal256", chromaFormatter(termenv.ANSI256))
	assert.Equal(t, "terminal16", chromaFormatter(termenv.ANSI))
	assert.Equal(t, "noop", chromaFormatter(termenv.Ascii))
}

// TestRenderMarkdownUsesPalette renders on a truecolor terminal and checks the
// links use the accent color and code blocks use GitHub's syntax theme.
func TestRenderMarkdownUsesPalette(t *testing.T) {
	forceTrueColor(t, true)
	out := renderMarkdown("See [docs](https://example.com).\n\n```go\nfunc main() {}\n```", 60)
	assert.Contains(t, out, sgr(shared.ColorAccent.Dark, false), "accent link")
	assert.Contains(t, out, "\x1b[38;2;255;123;114mfunc", "github-dark keyword color")
}

func TestRenderMarkdownWithoutColor(t *testing.T) {
	profile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	lipgloss.SetColorProfile(termenv.Ascii)

	out := renderMarkdown("# Title\n\n```go\nfunc main() {}\n```", 60)
	assert.NotContains(t, out, "38;", "no foreground colors when color is disabled")
	assert.NotContains(t, out, "48;", "no background colors when color is disabled")
	assert.Contains(t, out, "Title")
	assert.Contains(t, out, "func main() {}", "code blocks are not highlighted")
}

func TestEditorCommand(t *testing.T) {
	cmd := editorCommand("code --wait", "/tmp/desc.md")
	assert.Equal(t, []string{"code", "--wait", "/tmp/desc.md"}, cmd.Args)
	assert.Same(t, os.Stdout, cmd.Stdout, "the editor writes to the terminal, not the program output")
	assert.Nil(t, cmd.Stdin, "Bubble Tea supplies its own terminal input")
}
