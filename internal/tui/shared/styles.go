package shared

import "github.com/charmbracelet/lipgloss"

var (
	// Branch name styles
	CurrentBranchStyle = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	NormalBranchStyle  = lipgloss.NewStyle().Foreground(ColorText)
	MergedBranchStyle  = lipgloss.NewStyle().Foreground(ColorTextMuted)
	TrunkStyle         = lipgloss.NewStyle().Foreground(ColorTextMuted).Italic(true)

	// Status indicator glyphs. These are rendered at use-time (see StatusIcon)
	// with the styles below so their adaptive colors resolve against the detected
	// terminal background rather than being baked in at package-init time.
	mergedGlyph  = "✓"
	warningGlyph = "⚠"
	openGlyph    = "○"
	queuedGlyph  = "◎"

	mergedIconStyle  = lipgloss.NewStyle().Foreground(ColorPurple)
	warningIconStyle = lipgloss.NewStyle().Foreground(ColorYellow)
	openIconStyle    = lipgloss.NewStyle().Foreground(ColorGreen)
	queuedIconStyle  = lipgloss.NewStyle().Foreground(ColorYellow)

	// PR status styles
	PRLinkStyle   = lipgloss.NewStyle().Foreground(ColorText).Underline(true)
	PROpenStyle   = lipgloss.NewStyle().Foreground(ColorGreen)
	PRMergedStyle = lipgloss.NewStyle().Foreground(ColorPurple)
	PRClosedStyle = lipgloss.NewStyle().Foreground(ColorRed)
	PRDraftStyle  = lipgloss.NewStyle().Foreground(ColorGray)
	PRQueuedStyle = lipgloss.NewStyle().Foreground(ColorYellow)

	// Diff stats
	AdditionsStyle = lipgloss.NewStyle().Foreground(ColorGreen)
	DeletionsStyle = lipgloss.NewStyle().Foreground(ColorRed)

	// Commit lines
	CommitSHAStyle     = lipgloss.NewStyle().Foreground(ColorYellow)
	CommitSubjectStyle = lipgloss.NewStyle().Foreground(ColorText)
	CommitTimeStyle    = lipgloss.NewStyle().Foreground(ColorTextMuted)

	// Connector lines
	ConnectorStyle        = lipgloss.NewStyle().Foreground(ColorBorder)
	ConnectorDashedStyle  = lipgloss.NewStyle().Foreground(ColorYellow)
	ConnectorFocusedStyle = lipgloss.NewStyle().Foreground(ColorText)
	ConnectorCurrentStyle = lipgloss.NewStyle().Foreground(ColorAccent)
	ConnectorMergedStyle  = lipgloss.NewStyle().Foreground(ColorPurple)
	ConnectorQueuedStyle  = lipgloss.NewStyle().Foreground(ColorYellow)

	// Dim text
	DimStyle = lipgloss.NewStyle().Foreground(ColorTextFaint)

	// Header styles
	HeaderBorderStyle    = lipgloss.NewStyle().Foreground(ColorBorder)
	HeaderTitleStyle     = lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	HeaderInfoStyle      = lipgloss.NewStyle().Foreground(ColorAccent)
	HeaderInfoLabelStyle = lipgloss.NewStyle().Foreground(ColorTextMuted)
	HeaderShortcutKey    = lipgloss.NewStyle().Foreground(ColorText)
	HeaderShortcutDesc   = lipgloss.NewStyle().Foreground(ColorTextMuted)

	// Expand/collapse icons
	ExpandedIcon  = "▾"
	CollapsedIcon = "▸"
)
