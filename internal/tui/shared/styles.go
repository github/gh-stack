package shared

import "github.com/charmbracelet/lipgloss"

var (
	// Branch name styles
	CurrentBranchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	NormalBranchStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	MergedBranchStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	TrunkStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)

	// Status indicators
	MergedIcon  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Render("✓")
	WarningIcon = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("⚠")
	OpenIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("○")
	QueuedIcon  = lipgloss.NewStyle().Foreground(lipgloss.Color("130")).Render("◎")

	// PR status styles
	PRLinkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Underline(true)
	PROpenStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	PRMergedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	PRClosedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	PRDraftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	PRQueuedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("130"))

	// Diff stats
	AdditionsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	DeletionsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))

	// Commit lines
	CommitSHAStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	CommitSubjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	CommitTimeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Connector lines
	ConnectorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	ConnectorDashedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	ConnectorFocusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	ConnectorCurrentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	ConnectorMergedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	ConnectorQueuedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("130"))

	// Dim text
	DimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Header styles
	HeaderBorderStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	HeaderTitleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	HeaderInfoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	HeaderInfoLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	HeaderShortcutKey    = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	HeaderShortcutDesc   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Expand/collapse icons
	ExpandedIcon  = "▾"
	CollapsedIcon = "▸"
)
