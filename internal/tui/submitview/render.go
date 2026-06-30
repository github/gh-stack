package submitview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/tui/shared"
)

// Chrome styles shared across the submit views.
var (
	stackInfoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// headerHeight returns the number of screen rows the shared header occupies, or
// 0 when the terminal is too small to show it.
func (m Model) headerHeight() int {
	if shared.ShouldShowHeader(m.width, m.height) {
		return shared.HeaderHeightFor(m.buildHeaderConfig())
	}
	return 0
}

// renderHeader renders the shared gh-stack header (GitHub art, title, stack
// info, and keyboard shortcuts), matching `gh stack view` and `gh stack
// modify`. It returns "" when the terminal is too small for the header.
func (m Model) renderHeader() string {
	if !shared.ShouldShowHeader(m.width, m.height) {
		return ""
	}
	var b strings.Builder
	shared.RenderHeader(&b, m.buildHeaderConfig(), m.width, m.height)
	return strings.TrimSuffix(b.String(), "\n")
}

// buildHeaderConfig assembles the shared header configuration: title, stack
// info lines (including the consequence summary), and keyboard shortcuts.
func (m Model) buildHeaderConfig() shared.HeaderConfig {
	repo := m.repoLabel
	if repo == "" {
		repo = "unknown"
	}

	infoLines := []shared.HeaderInfoLine{
		{Icon: "○", Label: "Repo: " + repo},
		{Icon: "◆", Label: "Base: " + m.trunk.Branch},
	}

	// Third line mirrors the modify header's pending line: a solid yellow square
	// with the count when PRs will be created, or an empty square otherwise.
	newCount := 0
	for _, n := range m.nodes {
		if n.State == StateNew && n.Included {
			newCount++
		}
	}
	if newCount > 0 {
		yellowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
		prWord := "PRs"
		if newCount == 1 {
			prWord = "PR"
		}
		infoLines = append(infoLines, shared.HeaderInfoLine{
			Icon:      "■",
			Label:     fmt.Sprintf("Creating %d %s", newCount, prWord),
			IconStyle: &yellowStyle,
		})
	} else {
		infoLines = append(infoLines, shared.HeaderInfoLine{Icon: "□", Label: "No pending PRs"})
	}

	return shared.HeaderConfig{
		ShowArt:         true,
		Title:           "Submit Stack",
		Subtitle:        "v" + m.version,
		InfoLines:       infoLines,
		ShortcutColumns: 1,
		Shortcuts:       m.headerShortcuts(),
	}
}

// headerShortcuts returns the six primary single-screen keyboard shortcuts shown
// in the header (the help overlay lists the full set).
func (m Model) headerShortcuts() []shared.ShortcutEntry {
	return []shared.ShortcutEntry{
		{Key: "↑↓", Desc: "select branch"},
		{Key: "tab", Desc: "cycle field"},
		{Key: "^x", Desc: "skip/include"},
		{Key: "^s", Desc: "submit PRs"},
		{Key: "^h", Desc: "help"},
		{Key: "esc", Desc: "quit"},
	}
}

// renderClosedBanner renders a slim full-width red banner directly under the
// header when the stack contains a closed PR, or "" otherwise.
func (m Model) renderClosedBanner() string {
	closed := ClosedBranches(m.nodes)
	if len(closed) == 0 {
		return ""
	}
	verb := "branch has"
	if len(closed) > 1 {
		verb = "branches have"
	}
	msg := fmt.Sprintf(" %s %d %s a closed PR (%s) — reopen it, or unstack and recreate.",
		RenderDot(StateClosed), len(closed), verb, strings.Join(closed, ", "))
	if lipgloss.Width(msg) > m.width {
		msg = truncateVisible(msg, m.width)
	}
	return calloutErrorStyle.Render(msg)
}

// composeLR places left and right content on one line of the given width with
// the right content flush to the right edge.
func composeLR(left, right string, width int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
		// Truncate the left side so the right stays visible.
		if lw > width-rw-1 {
			left = truncateVisible(left, width-rw-1)
		}
	}
	return left + strings.Repeat(" ", gap) + right
}

// truncateVisible truncates a possibly-styled string to at most maxWidth
// visible columns, appending an ellipsis when it had to cut. It resets styling
// at the cut so trailing ANSI does not leak.
func truncateVisible(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	var b strings.Builder
	width := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
		}
		if inEscape {
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if width >= maxWidth-1 {
			b.WriteString("…")
			b.WriteString("\x1b[0m")
			break
		}
		b.WriteRune(r)
		width++
	}
	return b.String()
}

// contentHeight returns the number of lines available for the two panels,
// between the header and the reserved status line.
func (m Model) contentHeight() int {
	h := m.height - m.headerHeight() - 1 // status line
	if h < 1 {
		h = 1
	}
	return h
}
