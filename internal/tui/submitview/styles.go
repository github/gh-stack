package submitview

import "github.com/charmbracelet/lipgloss"

// State foreground colors, aligned with the gh stack view / modify palette.
var stateColors = map[BranchState]lipgloss.Color{
	StateNew:    lipgloss.Color("2"),   // green
	StateOpen:   lipgloss.Color("4"),   // blue
	StateDraft:  lipgloss.Color("3"),   // amber
	StateQueued: lipgloss.Color("130"), // orange
	StateMerged: lipgloss.Color("5"),   // purple
	StateClosed: lipgloss.Color("1"),   // red
}

// State background tints for pill badges (dark 256-color shades that read as a
// low-opacity wash of the foreground color across most terminal themes).
var stateBgColors = map[BranchState]lipgloss.Color{
	StateNew:    lipgloss.Color("22"), // dark green
	StateOpen:   lipgloss.Color("18"), // dark blue
	StateDraft:  lipgloss.Color("58"), // dark amber
	StateQueued: lipgloss.Color("52"), // dark orange/red
	StateMerged: lipgloss.Color("53"), // dark purple
	StateClosed: lipgloss.Color("52"), // dark red
}

// Label returns the uppercase badge text for a state (e.g. "NEW").
func (s BranchState) Label() string {
	switch s {
	case StateNew:
		return "NEW"
	case StateOpen:
		return "OPEN"
	case StateDraft:
		return "DRAFT"
	case StateQueued:
		return "QUEUED"
	case StateMerged:
		return "MERGED"
	case StateClosed:
		return "CLOSED"
	default:
		return ""
	}
}

// Color returns the foreground color associated with a state.
func (s BranchState) Color() lipgloss.Color { return stateColors[s] }

// Dot returns the compact legend glyph for a state, used in the Step 2 stack
// map and legend.
func (s BranchState) Dot() string {
	switch s {
	case StateNew:
		return "●"
	case StateOpen:
		return "○"
	case StateDraft:
		return "◐"
	case StateQueued:
		return "◌"
	case StateMerged:
		return "◍"
	case StateClosed:
		return "✗"
	default:
		return "·"
	}
}

// RenderBadge renders a state as a pill badge: the uppercase label in the state
// color on a tinted background with single-column horizontal padding.
func RenderBadge(s BranchState) string {
	return lipgloss.NewStyle().
		Foreground(s.Color()).
		Background(stateBgColors[s]).
		Bold(true).
		Padding(0, 1).
		Render(s.Label())
}

// RenderDot renders the state's legend glyph in the state color.
func RenderDot(s BranchState) string {
	return lipgloss.NewStyle().Foreground(s.Color()).Render(s.Dot())
}

// Shared submit-view styles. These are intentionally centralized so Step 1,
// Step 2, the editor, and the diff tab render with a consistent visual
// language.
var (
	// FocusAccent is the left accent bar that marks the focused row/branch.
	FocusAccent = "▌"

	focusAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	focusNameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	normalNameStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	// lockedStyle dims locked rows (~45% opacity feel).
	lockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Checkbox styles for Step 1.
	checkboxCheckedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	checkboxUncheckedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	checkboxLockedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Panel borders for the Step 2 two-panel layout.
	panelBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)
	panelFocusedBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("14")).
				Padding(0, 1)

	// Section labels (e.g. STACK, EDITING, TITLE, DESCRIPTION).
	sectionLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)

	// Tab strip styles.
	tabActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Footer / status styles.
	footerKeyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	footerDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Callouts.
	calloutErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hintStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	readyTagStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	editTagStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)
