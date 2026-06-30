package modifyview

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/github/gh-stack/internal/tui/shared"
)

// Colors come from the background-aware palette in internal/tui/shared so the
// modify view reads well on both dark and light terminals.
var (
	// Action annotation styles (modify-specific)
	dropBadge   = lipgloss.NewStyle().Foreground(shared.ColorRed)    // drop
	foldBadge   = lipgloss.NewStyle().Foreground(shared.ColorYellow) // fold
	renameBadge = lipgloss.NewStyle().Foreground(shared.ColorAccent) // rename
	moveBadge   = lipgloss.NewStyle().Foreground(shared.ColorPurple) // move
	insertBadge = lipgloss.NewStyle().Foreground(shared.ColorGreen)  // insert

	// Branch name overrides for drop/fold/insert
	dropBranchStyle   = lipgloss.NewStyle().Foreground(shared.ColorRed).Strikethrough(true)
	foldBranchStyle   = lipgloss.NewStyle().Foreground(shared.ColorYellow).Strikethrough(true)
	insertBranchStyle = lipgloss.NewStyle().Foreground(shared.ColorGreen)

	// Connector color overrides for drop/fold/move/insert
	dropConnectorStyle   = lipgloss.NewStyle().Foreground(shared.ColorRed)
	foldConnectorStyle   = lipgloss.NewStyle().Foreground(shared.ColorYellow)
	movedConnectorStyle  = lipgloss.NewStyle().Foreground(shared.ColorPurple)
	insertConnectorStyle = lipgloss.NewStyle().Foreground(shared.ColorGreen)

	// Status line styles
	statusBarStyle   = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
	statusCountStyle = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true)
	statusKeyStyle   = lipgloss.NewStyle().Foreground(shared.ColorAccent)
	statusDescStyle  = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)

	// Help overlay styles
	helpOverlayStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(shared.ColorBorder).
				Padding(1, 2)
	helpKeyStyle   = lipgloss.NewStyle().Foreground(shared.ColorAccent).Bold(true)
	helpDescStyle  = lipgloss.NewStyle().Foreground(shared.ColorText)
	helpTitleStyle = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true).Underline(true)

	// Transient message styles
	transientErrorStyle = lipgloss.NewStyle().Foreground(shared.ColorRed)
	transientInfoStyle  = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
)
