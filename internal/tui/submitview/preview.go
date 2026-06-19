package submitview

import (
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// editorFinishedMsg is delivered after the external $EDITOR process exits.
type editorFinishedMsg struct {
	path string
	err  error
}

// togglePreview flips the description between edit and preview, blurring or
// focusing the textarea accordingly. It returns any focus command.
func (m *Model) togglePreview() tea.Cmd {
	// Edit and preview use different line coordinates, so reset the scroll.
	m.descScroll = 0
	m.descScrollPinned = false
	m.descPreview = !m.descPreview
	if m.descPreview {
		m.descArea.Blur()
		return nil
	}
	if m.focusedField == fieldDescription {
		return m.descArea.Focus()
	}
	return nil
}

// openEditor launches $EDITOR on the focused branch's description, returning the
// ExecProcess command. If no editor is configured it surfaces a brief error and
// leaves the in-TUI textarea editable.
func (m Model) openEditor() (tea.Model, tea.Cmd) {
	n := m.currentNode()
	if n == nil || n.State != StateNew {
		return m, nil
	}
	m.saveEditor()

	editor := resolveEditor()
	if editor == "" {
		m.statusMessage = "$EDITOR is not set — edit inline or set $EDITOR"
		m.statusIsError = true
		return m, nil
	}

	path, err := writeTempDescription(m.nodes[m.cursor].Description)
	if err != nil {
		m.statusMessage = "Could not open editor: " + err.Error()
		m.statusIsError = true
		return m, nil
	}

	fields := strings.Fields(editor)
	args := append(fields[1:], path)
	cmd := exec.Command(fields[0], args...)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorFinishedMsg{path: path, err: err}
	})
}

// handleEditorFinished reloads the description from the temp file after the
// editor exits, then removes the file.
func (m Model) handleEditorFinished(msg editorFinishedMsg) (tea.Model, tea.Cmd) {
	defer func() { _ = os.Remove(msg.path) }()

	if msg.err != nil {
		m.statusMessage = "Editor exited with an error — your inline edits are kept"
		m.statusIsError = true
		return m, nil
	}

	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.statusMessage = "Could not read the editor's output"
		m.statusIsError = true
		return m, nil
	}

	content := strings.TrimRight(string(data), "\n")
	if m.cursor >= 0 && m.cursor < len(m.nodes) {
		m.nodes[m.cursor].Description = content
		m.descArea.SetValue(content)
	}
	return m, nil
}

// resolveEditor returns the configured editor command, checking GH_EDITOR,
// VISUAL, then EDITOR. It returns "" when none are set.
func resolveEditor() string {
	for _, key := range []string{"GH_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// writeTempDescription writes content to a temporary markdown file and returns
// its path.
func writeTempDescription(content string) (string, error) {
	f, err := os.CreateTemp("", "gh-stack-pr-*.md")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// renderMarkdown renders markdown to styled terminal output using Glamour. It
// uses a fixed dark style rather than glamour.WithAutoStyle(): auto-style probes
// the terminal background with an OSC query whose response is consumed by Bubble
// Tea's own input reader, so the query blocks forever and freezes the UI. On any
// error it falls back to the raw markdown so the user still sees their content.
func renderMarkdown(md string, width int) string {
	if strings.TrimSpace(md) == "" {
		return hintStyle.Render("(no description)")
	}
	if width < 10 {
		width = 10
	}
	// Use glamour's "dark" style but drop the document block's default 2-column
	// margin so the preview text aligns flush-left with the edit-mode textarea
	// instead of being indented. Copying the struct and replacing the Margin
	// pointer leaves the shared package-level style untouched.
	style := styles.DarkStyleConfig
	var noMargin uint
	style.Document.Margin = &noMargin
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}
