package modifyview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type helpEntry struct {
	keys string
	desc string
}

var helpEntries = []helpEntry{
	{"↑/↓, k/j", "Select branch"},
	{"f", "View files changed"},
	{"c", "View commits"},
	{"x", "Drop branch from stack"},
	{"r", "Rename branch"},
	{"u", "Fold up (merge into branch above)"},
	{"d", "Fold down (merge into branch below)"},
	{"shift+↑/↓, K/J", "Reorder branch up/down"},
	{"z", "Undo last action"},
	{"ctrl+s", "Apply all changes"},
	{"q/esc", "Cancel and exit (abandon changes)"},
}

// renderHelpOverlay renders a centered help overlay.
func renderHelpOverlay(width, height int) string {
	var b strings.Builder

	title := helpTitleStyle.Render("Keyboard Shortcuts")
	b.WriteString(title)
	b.WriteString("\n\n")

	maxKeyWidth := 0
	for _, e := range helpEntries {
		w := lipgloss.Width(e.keys)
		if w > maxKeyWidth {
			maxKeyWidth = w
		}
	}

	for _, e := range helpEntries {
		keyVisWidth := lipgloss.Width(e.keys)
		keyPad := strings.Repeat(" ", maxKeyWidth-keyVisWidth+2)
		b.WriteString(helpKeyStyle.Render(e.keys))
		b.WriteString(keyPad)
		b.WriteString(helpDescStyle.Render(e.desc))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(statusBarStyle.Render("Press ? or Esc to close"))

	content := b.String()

	// Apply the overlay style and center it
	styled := helpOverlayStyle.Render(content)

	// Center vertically and horizontally
	styledLines := strings.Split(styled, "\n")
	styledHeight := len(styledLines)
	styledWidth := 0
	for _, line := range styledLines {
		w := lipgloss.Width(line)
		if w > styledWidth {
			styledWidth = w
		}
	}

	topPad := (height - styledHeight) / 2
	if topPad < 0 {
		topPad = 0
	}
	leftPad := (width - styledWidth) / 2
	if leftPad < 0 {
		leftPad = 0
	}

	var result strings.Builder
	for i := 0; i < topPad; i++ {
		result.WriteString("\n")
	}
	for _, line := range styledLines {
		result.WriteString(strings.Repeat(" ", leftPad))
		result.WriteString(line)
		result.WriteString("\n")
	}

	return result.String()
}
