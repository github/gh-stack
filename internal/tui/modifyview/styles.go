package modifyview

import "github.com/charmbracelet/lipgloss"

var (
	// Action annotation styles (modify-specific)
	dropBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))  // red
	foldBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow
	renameBadge = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	moveBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))  // magenta/purple

	// Branch name overrides for drop/fold
	dropBranchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Strikethrough(true) // red strikethrough
	foldBranchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Strikethrough(true) // yellow strikethrough

	// Connector color overrides for drop/fold/move
	dropConnectorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))   // red
	foldConnectorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))   // yellow
	movedConnectorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))   // magenta/purple

	// Status line styles
	statusBarStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	statusCountStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	statusKeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	statusDescStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Help overlay styles
	helpOverlayStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(1, 2)
	helpKeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	helpDescStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	helpTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Underline(true)

	// Transient message styles
	transientErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	transientInfoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // gray
)
