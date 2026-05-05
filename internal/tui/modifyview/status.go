package modifyview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// pendingChangeSummary returns a summary string of all pending changes.
// E.g. "Pending: 1 drop, 1 fold, 2 moves, 1 rename"
func pendingChangeSummary(nodes []ModifyBranchNode) string {
	var drops, foldDowns, foldUps, moves, renames int

	for _, n := range nodes {
		if n.PendingAction == nil {
			continue
		}
		switch n.PendingAction.Type {
		case ActionDrop:
			drops++
		case ActionFoldDown:
			foldDowns++
		case ActionFoldUp:
			foldUps++
		case ActionMove:
			moves++
		case ActionRename:
			renames++
		}
	}

	// Also count nodes that have moved from their original position
	for i, n := range nodes {
		if !n.Removed && n.PendingAction == nil && n.OriginalPosition != i {
			moves++
		}
	}

	if drops == 0 && foldDowns == 0 && foldUps == 0 && moves == 0 && renames == 0 {
		return ""
	}

	var parts []string
	if drops > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", drops, pluralize(drops, "drop", "drops")))
	}
	folds := foldDowns + foldUps
	if folds > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", folds, pluralize(folds, "fold", "folds")))
	}
	if moves > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", moves, pluralize(moves, "move", "moves")))
	}
	if renames > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", renames, pluralize(renames, "rename", "renames")))
	}

	return "Pending: " + strings.Join(parts, ", ")
}

// pluralize returns singular or plural form based on count.
func pluralize(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

// renderStatusLine renders the bottom status bar with pending changes and key hints.
func renderStatusLine(nodes []ModifyBranchNode, width int) string {
	summary := pendingChangeSummary(nodes)

	hints := statusKeyStyle.Render("ctrl+s") + statusDescStyle.Render(" apply  ") +
		statusKeyStyle.Render("q") + statusDescStyle.Render(" cancel  ") +
		statusKeyStyle.Render("?") + statusDescStyle.Render(" help")

	if summary == "" {
		summary = statusBarStyle.Render("No pending changes")
	} else {
		summary = statusCountStyle.Render(summary)
	}

	// Lay out: summary on left, hints on right
	summaryWidth := lipgloss.Width(summary)
	hintsWidth := lipgloss.Width(hints)
	gap := width - summaryWidth - hintsWidth
	if gap < 2 {
		gap = 2
	}

	return summary + strings.Repeat(" ", gap) + hints
}
