package submitview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	helpOverlayStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(1, 2)
	helpTitleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Underline(true)
	helpSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	helpKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	helpDescStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// helpEntry is a single key/description pair in the help overlay.
type helpEntry struct {
	key  string
	desc string
}

// helpSection groups related key bindings under a heading.
type helpSection struct {
	heading string
	entries []helpEntry
}

var helpSections = []helpSection{
	{
		heading: "Choose which branches become PRs",
		entries: []helpEntry{
			{"↑ / ↓ (or k / j)", "move between branches"},
			{"^x / space", "skip / include the focused branch (works while typing)"},
			{"", "skipping also skips the branches above; including adds those below"},
		},
	},
	{
		heading: "Edit PR details",
		entries: []helpEntry{
			{"tab / shift+tab", "cycle fields (title, description, ready/draft) and PRs"},
			{"space / ← / →", "flip the ready ↔ draft toggle"},
			{"^p", "toggle description preview"},
			{"^e", "open $EDITOR for the description"},
		},
	},
	{
		heading: "Existing PRs",
		entries: []helpEntry{
			{"^o", "open the focused branch's PR on the web"},
		},
	},
	{
		heading: "Anywhere",
		entries: []helpEntry{
			{"^s", "submit all included PRs"},
			{"? / ^h", "toggle this help (^h also works while editing a field)"},
			{"q / esc", "quit (confirm if edits exist)"},
			{"mouse", "click rows, checkboxes, the include switch, toggles, and fields"},
		},
	},
}

// renderHelpOverlay renders a centered, full-screen help modal listing every
// key binding.
func renderHelpOverlay(width, height int) string {
	var b strings.Builder
	b.WriteString(helpTitleStyle.Render("gh stack submit — keyboard & mouse"))
	b.WriteString("\n")

	// Compute the key-column width for alignment.
	keyW := 0
	for _, s := range helpSections {
		for _, e := range s.entries {
			if w := lipgloss.Width(e.key); w > keyW {
				keyW = w
			}
		}
	}

	for _, s := range helpSections {
		b.WriteString("\n")
		b.WriteString(helpSectionStyle.Render(s.heading))
		b.WriteString("\n")
		for _, e := range s.entries {
			pad := keyW - lipgloss.Width(e.key)
			if pad < 0 {
				pad = 0
			}
			b.WriteString("  ")
			b.WriteString(helpKeyStyle.Render(e.key))
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString("  ")
			b.WriteString(helpDescStyle.Render(e.desc))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(helpDescStyle.Render("Press ?, ^h, or Esc to close"))

	styled := helpOverlayStyle.Render(b.String())
	return centerOverlay(styled, width, height)
}

// renderQuitConfirm renders the centered discard-edits confirmation modal.
func renderQuitConfirm(width, height int) string {
	body := helpTitleStyle.Render("Discard edits?") + "\n\n" +
		helpDescStyle.Render("You have unsaved changes. Quit without submitting?") + "\n\n" +
		helpKeyStyle.Render("y") + helpDescStyle.Render(" quit   ") +
		helpKeyStyle.Render("n") + helpDescStyle.Render(" keep editing")
	styled := helpOverlayStyle.Render(body)
	return centerOverlay(styled, width, height)
}

// centerOverlay positions a styled block in the center of the viewport.
func centerOverlay(block string, width, height int) string {
	blockLines := strings.Split(block, "\n")
	blockHeight := len(blockLines)
	blockWidth := 0
	for _, l := range blockLines {
		if w := lipgloss.Width(l); w > blockWidth {
			blockWidth = w
		}
	}

	topPad := (height - blockHeight) / 2
	if topPad < 0 {
		topPad = 0
	}
	leftPad := (width - blockWidth) / 2
	if leftPad < 0 {
		leftPad = 0
	}

	var b strings.Builder
	for i := 0; i < topPad; i++ {
		b.WriteString("\n")
	}
	for i, l := range blockLines {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat(" ", leftPad))
		b.WriteString(l)
	}
	return b.String()
}
