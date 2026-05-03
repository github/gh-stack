package modifyview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderHelpOverlay renders a centered help overlay with a guide to modify operations.
func renderHelpOverlay(width, height int) string {
	var b strings.Builder

	title := helpTitleStyle.Render("Modify Stack")
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(helpDescStyle.Render("Restructure your stack by dropping, folding, renaming, or reordering branches."))
	b.WriteString("\n")

	sections := []struct {
		heading string
		body    string
	}{
		{
			"Drop (x)",
			"Remove a branch and its commits from the stack.\nThe local branch is preserved; the PR stays open on GitHub.",
		},
		{
			"Fold up / down (u / d)",
			"Merge a branch's commits into an adjacent branch.\nFold up absorbs into the branch above; fold down into the branch below.",
		},
		{
			"Rename (r)",
			"Rename a branch locally. The new name is pushed on submit.",
		},
		{
			"Reorder (Shift+↑/↓)",
			"Move a branch up or down in the stack.\nA cascading rebase adjusts all affected branches.",
		},
	}

	for _, s := range sections {
		b.WriteString("\n")
		b.WriteString(helpKeyStyle.Render(s.heading))
		b.WriteString("\n")
		for _, line := range strings.Split(s.body, "\n") {
			b.WriteString(helpDescStyle.Render(line))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(helpKeyStyle.Render("Applying changes"))
	b.WriteString("\n")
	b.WriteString(helpDescStyle.Render("Press " + helpKeyStyle.Render("Ctrl+S") + " to apply all staged changes. Nothing is modified until you save."))
	b.WriteString("\n")
	b.WriteString(helpDescStyle.Render("If you have open PRs, run ") + helpKeyStyle.Render("gh stack submit") + helpDescStyle.Render(" afterwards to push the updated"))
	b.WriteString("\n")
	b.WriteString(helpDescStyle.Render("branches and recreate the stack of PRs on GitHub."))

	b.WriteString("\n\n")
	b.WriteString(statusBarStyle.Render("Press ? or Esc to close"))

	content := b.String()

	// Apply the overlay style and center it
	styled := helpOverlayStyle.Render(content)

	// Center vertically and horizontally
	styledLines := strings.Split(styled, "\n")
	styledHeight := len(styledLines)
	styledWidth := 0
	for _, line := range styledLines {
		w := lipgloss.Width(line)
		if w > styledWidth {
			styledWidth = w
		}
	}

	topPad := (height - styledHeight) / 2
	if topPad < 0 {
		topPad = 0
	}
	leftPad := (width - styledWidth) / 2
	if leftPad < 0 {
		leftPad = 0
	}

	var result strings.Builder
	for i := 0; i < topPad; i++ {
		result.WriteString("\n")
	}
	for _, line := range styledLines {
		result.WriteString(strings.Repeat(" ", leftPad))
		result.WriteString(line)
		result.WriteString("\n")
	}

	return result.String()
}
