package stackview

import "github.com/charmbracelet/lipgloss"

var (
	// Branch name styles
	currentBranchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)  // cyan bold
	normalBranchStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))             // white
	mergedBranchStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))              // gray
	trunkStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true) // gray italic

	// Focus indicator — reserved for future use

	// Status indicators
	mergedIcon  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Render("✓") // magenta
	warningIcon = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("⚠") // yellow
	openIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("○") // green
	queuedIcon  = lipgloss.NewStyle().Foreground(lipgloss.Color("130")).Render("◎") // brown

	// PR status
	prOpenStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	prMergedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // magenta
	prClosedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	prDraftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // gray
	prQueuedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("130")) // brown

	// Diff stats
	additionsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	deletionsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red

	// Commit lines
	commitSHAStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow
	commitSubjectStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // white
	commitTimeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray

	// Connector lines
	connectorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray
	connectorDashedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow (non-linear)
	connectorFocusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // white (focused)
	connectorCurrentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan (current branch focused)
	connectorMergedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))  // magenta (merged branch focused)
	connectorQueuedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("130"))  // brown (queued branch focused)

	// Dim text (separators, secondary labels)
	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Header styles
	headerBorderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray box-drawing chars
	headerTitleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true) // white bold
	headerInfoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	headerInfoLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray
	headerShortcutKey    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // white
	headerShortcutDesc   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // gray

	// Expand/collapse toggle
	expandedIcon  = "▾"
	collapsedIcon = "▸"
)
