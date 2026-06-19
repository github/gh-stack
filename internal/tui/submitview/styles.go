package submitview

import "github.com/charmbracelet/lipgloss"

// State foreground colors, matching how GitHub.com colors these PR states.
var stateColors = map[BranchState]lipgloss.Color{
	StateNew:    lipgloss.Color("4"),   // blue
	StateOpen:   lipgloss.Color("2"),   // green
	StateDraft:  lipgloss.Color("250"), // gray
	StateQueued: lipgloss.Color("137"), // brown
	StateMerged: lipgloss.Color("5"),   // purple
	StateClosed: lipgloss.Color("1"),   // red
}

// State background tints for pill badges (dark 256-color shades that read as a
// low-opacity wash of the foreground color across most terminal themes).
var stateBgColors = map[BranchState]lipgloss.Color{
	StateNew:    lipgloss.Color("18"),  // dark blue
	StateOpen:   lipgloss.Color("22"),  // dark green
	StateDraft:  lipgloss.Color("238"), // dark gray
	StateQueued: lipgloss.Color("58"),  // dark brown
	StateMerged: lipgloss.Color("53"),  // dark purple
	StateClosed: lipgloss.Color("52"),  // dark red
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

// Shared submit-view styles. These are intentionally centralized so the left
// stack tree, the editor, and the chrome render with a consistent visual
// language.
var (
	focusNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true) // cyan focused label
	// headerBranchStyle renders the focused branch name in the right-panel card
	// header in white (the left-panel cursor name stays cyan).
	headerBranchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)

	// rowShadeColor tints the focused (currently-viewed) branch row in the left
	// timeline. A neutral cool gray (truecolor, so it doesn't pick up a warm tint
	// from a themed 256-color palette) reading as a translucent-white highlight.
	rowShadeColor = lipgloss.Color("#3b3e46")

	// Panel border shared by both panels (focus is shown on the active input
	// field, not the panel frame).
	panelBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)

	// Section labels (e.g. STACK, EDITING, TITLE, DESCRIPTION).
	sectionLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)

	// Tab strip styles.
	tabActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Footer / status styles.
	footerKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

	// openLinkStyle renders the underlined white "↗ Open on GitHub" link (arrow
	// included) in a locked PR's read-only card header; lockedTitleStyle renders
	// that PR's title.
	openLinkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Underline(true)
	lockedTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)

	// Footer bottom-right actions: nextBranchStyle is the white "NEXT BRANCH"
	// label; submitButtonStyle is the prominent solid-white "SUBMIT N PRs" button
	// (dark text) shown on the last PR.
	nextBranchStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	submitButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("15")).Bold(true).Padding(0, 1)
	// prNumberStyle renders a clickable existing-PR number as an underlined
	// white link.
	prNumberStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Underline(true)

	// Tree spine + horizontal rules (dim chrome).
	spineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	ruleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// CREATE PR switch in the right-panel header. On: a green pill (matching the
	// CREATE AS selected color) with a black square knob inset on the right. Off:
	// the colors invert to a light-gray pill with a darker square inset on the
	// left. The "CREATE PR" label uses the shared section-heading style.
	switchOnStyle  = lipgloss.NewStyle().Background(lipgloss.Color("2"))
	switchOffStyle = lipgloss.NewStyle().Background(lipgloss.Color("245"))
	switchOnKnob   = lipgloss.Color("0")   // black knob (matches CREATE AS selected text)
	switchOffKnob  = lipgloss.Color("236") // dark square on a lighter track

	// Segmented Ready/Draft control: the selected segment is filled green; the
	// other is dim. Brackets/divider are dim chrome.
	segOnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("2")).
			Bold(true).
			Padding(0, 1)
	segOffStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	segFrameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// dimBodyStyle renders the skipped branch's body as muted, non-interactive
	// chrome.
	dimBodyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// descCursorStyle renders the block cursor overlaid on the scrollable
	// description view.
	descCursorStyle = lipgloss.NewStyle().Reverse(true)

	// Description scrollbar (track + thumb), drawn inside the box.
	scrollTrackStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	scrollThumbStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	// Callouts.
	calloutErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hintStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)
