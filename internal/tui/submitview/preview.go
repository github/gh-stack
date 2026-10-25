package submitview

import (
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/github/gh-stack/internal/tui/shared"
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

	return m, tea.ExecProcess(editorCommand(editor, path), func(err error) tea.Msg {
		return editorFinishedMsg{path: path, err: err}
	})
}

// editorCommand builds the $EDITOR invocation for path. Its stdout is pinned
// to the real terminal: tea.ExecProcess would otherwise hand the editor the
// program's output, which is the canvas-painting wrapper (theme.CanvasOutput)
// when the canvas is on, so the editor would see a pipe instead of a terminal.
// Stdin is left unset so Bubble Tea passes its own terminal input.
func editorCommand(editor, path string) *exec.Cmd {
	fields := strings.Fields(editor)
	args := append(fields[1:], path)
	cmd := exec.Command(fields[0], args...)
	cmd.Stdout = os.Stdout
	return cmd
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
// VISUAL, then EDITOR. If none are set, it falls back to vi when available.
func resolveEditor() string {
	for _, key := range []string{"GH_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	if _, err := exec.LookPath("vi"); err == nil {
		return "vi"
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

// renderMarkdown renders markdown to styled terminal output using Glamour, with
// the gh-stack palette (see markdownStyle). It selects the light or dark style
// from the already-detected terminal background (lipgloss.HasDarkBackground,
// cached at startup) rather than glamour.WithAutoStyle(): auto-style probes the
// terminal with an OSC query whose response is consumed by Bubble Tea's own
// input reader, so the query blocks forever and freezes the UI. On any error it
// falls back to the raw markdown so the user still sees their content.
func renderMarkdown(md string, width int) string {
	if strings.TrimSpace(md) == "" {
		return hintStyle.Render("(no description)")
	}
	if width < 10 {
		width = 10
	}
	profile := lipgloss.ColorProfile()
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle(lipgloss.HasDarkBackground())),
		glamour.WithColorProfile(profile),
		glamour.WithChromaFormatter(chromaFormatter(profile)),
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

// markdownStyle returns Glamour's light or dark base style recolored to the
// gh-stack palette, so the preview reads like a description rendered on GitHub:
// primary ink for body text and headings, accent links, inline code on a
// neutral chip, muted quotes and rules, and GitHub's own syntax highlighting
// for code blocks. The base style is copied, so the shared package-level
// Glamour styles stay untouched.
func markdownStyle(dark bool) gansi.StyleConfig {
	style := styles.DarkStyleConfig
	codeTheme := "github-dark"
	if !dark {
		style = styles.LightStyleConfig
		codeTheme = "github"
	}
	pick := func(c lipgloss.AdaptiveColor) *string {
		v := c.Light
		if dark {
			v = c.Dark
		}
		return &v
	}
	text, muted, accent := pick(shared.ColorText), pick(shared.ColorTextMuted), pick(shared.ColorAccent)

	// Drop the document block's default 2-column margin so the preview text
	// aligns flush-left with the edit-mode textarea instead of being indented.
	var noMargin uint
	style.Document.Margin = &noMargin
	style.Document.Color = text

	// Headings in bold primary ink, with H1 marked like the other levels rather
	// than Glamour's colored pill. GitHub renders H6 in muted ink.
	style.Heading.Color = text
	style.H1.Prefix, style.H1.Suffix = "# ", ""
	style.H1.Color, style.H1.BackgroundColor = nil, nil
	style.H6.Color = muted

	style.Link.Color = accent
	style.LinkText.Color = accent
	style.Image.Color = accent
	style.ImageText.Color = muted
	style.BlockQuote.Color = muted
	style.HorizontalRule.Color = pick(shared.ColorBorder)

	style.Code.Color = text
	style.Code.BackgroundColor = pick(shared.ColorCodeBg)
	style.CodeBlock.Chroma = nil
	style.CodeBlock.Theme = codeTheme
	return style
}

// chromaFormatter returns the Chroma formatter that matches the terminal's
// color profile, so highlighted code blocks use exact GitHub syntax colors on
// truecolor terminals and no color at all when color is disabled.
func chromaFormatter(profile termenv.Profile) string {
	switch profile {
	case termenv.TrueColor:
		return "terminal16m"
	case termenv.ANSI256:
		return "terminal256"
	case termenv.ANSI:
		return "terminal16"
	default:
		return "noop"
	}
}
