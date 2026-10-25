package submitview

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/github/gh-stack/internal/tui/shared"
)

// State foreground colors, matching how GitHub.com colors these PR states. Each
// is background-aware (see internal/tui/shared/theme.go).
var stateColors = map[BranchState]lipgloss.TerminalColor{
	StateNew:    shared.ColorBlue,
	StateOpen:   shared.ColorGreen,
	StateDraft:  shared.ColorGray,
	StateQueued: shared.ColorYellow,
	StateMerged: shared.ColorPurple,
	StateClosed: shared.ColorRed,
}

// State pill badges: each state's color on a faint wash of the same hue, so the
// badge reads as a low-opacity tint of its foreground color in either mode.
var stateBadges = map[BranchState]shared.Badge{
	StateNew:    shared.BadgeBlue,
	StateOpen:   shared.BadgeGreen,
	StateDraft:  shared.BadgeGray,
	StateQueued: shared.BadgeYellow,
	StateMerged: shared.BadgePurple,
	StateClosed: shared.BadgeRed,
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
func (s BranchState) Color() lipgloss.TerminalColor { return stateColors[s] }

// Dot returns the compact status glyph for a state.
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
	badge := stateBadges[s]
	return lipgloss.NewStyle().
		Foreground(badge.Fg).
		Background(badge.Bg).
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
// language. Colors come from the background-aware palette in internal/tui/shared.
var (
	focusNameStyle = lipgloss.NewStyle().Foreground(shared.ColorAccent).Bold(true) // focused label
	// headerBranchStyle renders the focused branch name in the right-panel card
	// header in primary ink (the left-panel cursor name uses the accent color).
	headerBranchStyle = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true)

	// rowShadeColor tints the focused (currently-viewed) branch row in the left
	// timeline, reading as a subtle highlight on either background.
	rowShadeColor = shared.ColorRowShade

	// Panel border shared by both panels (focus is shown on the active input
	// field, not the panel frame).
	panelBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(shared.ColorBorder).
				Padding(0, 1)

	// Section labels (e.g. STACK, EDITING, TITLE, DESCRIPTION).
	sectionLabelStyle = lipgloss.NewStyle().Foreground(shared.ColorTextMuted).Bold(true)

	// Tab strip styles.
	tabActiveStyle   = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)

	// Footer / status styles.
	footerKeyStyle = lipgloss.NewStyle().Foreground(shared.ColorAccent)

	// openLinkStyle renders the underlined "↗ Open on GitHub" link (arrow
	// included) in a locked PR's read-only card header; lockedTitleStyle renders
	// that PR's title.
	openLinkStyle    = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true).Underline(true)
	lockedTitleStyle = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true)

	// Footer bottom-right actions: nextBranchStyle is the "NEXT BRANCH" label;
	// submitButtonStyle is the prominent primary (green) "SUBMIT N PRs" button
	// shown on the last PR.
	nextBranchStyle   = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true)
	submitButtonStyle = lipgloss.NewStyle().Foreground(shared.ColorOnPrimary).Background(shared.ColorPrimaryFill).Bold(true).Padding(0, 1)
	// prNumberStyle renders a clickable existing-PR number as an underlined link.
	prNumberStyle = lipgloss.NewStyle().Foreground(shared.ColorText).Underline(true)

	// Tree spine + horizontal rules (dim chrome).
	spineStyle = lipgloss.NewStyle().Foreground(shared.ColorBorder)
	ruleStyle  = lipgloss.NewStyle().Foreground(shared.ColorBorder)

	// CREATE PR switch in the right-panel header. On: a primary (green) pill
	// matching the CREATE AS selected segment, with a white knob inset on the
	// right. Off: a muted track with a darker knob inset on the left.
	switchOnStyle  = lipgloss.NewStyle().Background(shared.ColorPrimaryFill)
	switchOffStyle = lipgloss.NewStyle().Background(shared.ColorToggleOffTrack)
	switchOnKnob   = shared.ColorOnPrimary
	switchOffKnob  = shared.ColorToggleOffKnob

	// Segmented Ready/Draft control: the selected segment is the primary
	// (green) fill; the other is muted. Brackets/divider are dim chrome.
	segOnStyle = lipgloss.NewStyle().
			Foreground(shared.ColorOnPrimary).
			Background(shared.ColorPrimaryFill).
			Bold(true).
			Padding(0, 1)
	segOffStyle   = lipgloss.NewStyle().Foreground(shared.ColorTextMuted).Padding(0, 1)
	segFrameStyle = lipgloss.NewStyle().Foreground(shared.ColorBorder)

	// dimBodyStyle renders the skipped branch's body as muted, non-interactive
	// chrome.
	dimBodyStyle = lipgloss.NewStyle().Foreground(shared.ColorTextFaint)

	// descCursorStyle renders the block cursor overlaid on the scrollable
	// description view.
	descCursorStyle = lipgloss.NewStyle().Reverse(true)

	// Description scrollbar (track + thumb), drawn inside the box.
	scrollTrackStyle = lipgloss.NewStyle().Foreground(shared.ColorBorder)
	scrollThumbStyle = lipgloss.NewStyle().Foreground(shared.ColorText)

	// Callouts.
	calloutErrorStyle = lipgloss.NewStyle().Foreground(shared.ColorRed)
	hintStyle         = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
)
