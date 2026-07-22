package mergeview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/theme"
)

var (
	titleStyle   = lipgloss.NewStyle().Foreground(theme.ColorText).Bold(true)
	mutedStyle   = lipgloss.NewStyle().Foreground(theme.ColorTextMuted)
	faintStyle   = lipgloss.NewStyle().Foreground(theme.ColorTextFaint)
	accentStyle  = lipgloss.NewStyle().Foreground(theme.ColorAccent)
	numberStyle  = lipgloss.NewStyle().Foreground(theme.ColorAccent).Bold(true)
	checkedStyle = lipgloss.NewStyle().Foreground(theme.ColorGreen)
	textStyle    = lipgloss.NewStyle().Foreground(theme.ColorText)
	successStyle = lipgloss.NewStyle().Foreground(theme.ColorGreen).Bold(true)
	failureStyle = lipgloss.NewStyle().Foreground(theme.ColorRed).Bold(true)

	// Wizard stepper.
	stepActiveStyle   = lipgloss.NewStyle().Foreground(theme.ColorText).Background(theme.ColorRowShade).Bold(true).Padding(0, 1)
	stepDoneStyle     = lipgloss.NewStyle().Foreground(theme.ColorAccent).Padding(0, 1)
	stepUpcomingStyle = lipgloss.NewStyle().Foreground(theme.ColorTextFaint).Padding(0, 1)
	stepArrowStyle    = lipgloss.NewStyle().Foreground(theme.ColorBorder)

	shortcutKey   = lipgloss.NewStyle().Foreground(theme.ColorText)
	shortcutLabel = lipgloss.NewStyle().Foreground(theme.ColorTextMuted)
)

var wizardSteps = []string{"Select PRs", "Select Merge Method", "Confirm"}

// View implements tea.Model.
func (m Model) View() string {
	switch m.step {
	case StepSelectPRs:
		return m.banner() + m.viewSelect()
	case StepMethod:
		return m.banner() + m.viewMethod()
	case StepConfirm:
		return m.banner() + m.viewConfirm()
	case StepProgress:
		return m.banner() + m.viewProgress()
	default:
		return m.banner() + m.viewDone()
	}
}

// banner renders the persistent title and wizard stepper shown at the top of
// every step.
func (m Model) banner() string {
	return titleStyle.Render("Merge stack") + "\n" + m.stepper() + "\n\n"
}

func (m Model) stepper() string {
	cur := m.wizardIndex()
	parts := make([]string, len(wizardSteps))
	for i, label := range wizardSteps {
		switch {
		case i < cur:
			parts[i] = stepDoneStyle.Render("✓ " + label)
		case i == cur:
			parts[i] = stepActiveStyle.Render(label)
		default:
			parts[i] = stepUpcomingStyle.Render(label)
		}
	}
	return strings.Join(parts, stepArrowStyle.Render("▸"))
}

// wizardIndex maps the current step to its position in the stepper. Progress and
// done are past the last selectable step, so all three read as complete.
func (m Model) wizardIndex() int {
	switch m.step {
	case StepSelectPRs:
		return 0
	case StepMethod:
		return 1
	case StepConfirm:
		return 2
	default:
		return len(wizardSteps)
	}
}

func (m Model) viewSelect() string {
	var b strings.Builder
	b.WriteString(mutedStyle.Render("Select how far up the stack to merge (everything up to your choice merges).") + "\n\n")

	// Render top of stack first so the layout matches the CLI.
	for i := len(m.opts.PRs) - 1; i >= 0; i-- {
		pr := m.opts.PRs[i]
		cursor := "  "
		if i == m.cursor {
			cursor = accentStyle.Render("❯ ")
		}
		box := "[ ]"
		if i <= m.topIndex {
			box = checkedStyle.Render("[x]")
		}
		num := numberStyle.Render(fmt.Sprintf("#%d", pr.Number))
		title := truncate(pr.Title, 60)
		titleStyled := mutedStyle.Render(title)
		if i <= m.topIndex {
			titleStyled = textStyle.Render(title)
		}
		b.WriteString(fmt.Sprintf("%s%s %s  %s\n", cursor, box, num, titleStyled))
	}

	b.WriteString("\n")
	if m.topIndex >= 0 {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("Merging %s into %s.", prCount(m.topIndex+1), m.opts.BaseRef)))
	} else {
		b.WriteString(faintStyle.Render("Select at least one pull request."))
	}
	b.WriteString("\n\n")
	b.WriteString(shortcuts(
		[2]string{"↑/↓", "move"},
		[2]string{"space", "toggle"},
		[2]string{"tab/enter", "next"},
		[2]string{"esc", "cancel"},
	))
	return b.String()
}

func (m Model) viewMethod() string {
	var b strings.Builder
	b.WriteString(mutedStyle.Render("Choose a merge method.") + "\n\n")

	for i, method := range m.opts.AllowedMethods {
		cursor := "  "
		if i == m.methodCursor {
			cursor = accentStyle.Render("❯ ")
		}
		radio := "( )"
		label := mutedStyle.Render(methodLabel(method))
		if i == m.methodCursor {
			radio = checkedStyle.Render("(•)")
			label = textStyle.Render(methodLabel(method))
		}
		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, radio, label))
	}

	b.WriteString("\n")
	b.WriteString(shortcuts(
		[2]string{"↑/↓", "move"},
		[2]string{"tab/enter", "next"},
		[2]string{"shift+tab", "back"},
		[2]string{"esc", "cancel"},
	))
	return b.String()
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	nums := m.selectedNumbers()

	b.WriteString(fmt.Sprintf("%s into %s via %s.\n",
		titleStyle.Render("Merge "+prCount(len(nums))),
		accentStyle.Render(m.opts.BaseRef),
		accentStyle.Render(methodLabel(m.method)),
	))
	b.WriteString(numberStyle.Render(prNumberList(nums)) + "\n\n")
	b.WriteString(shortcuts(
		[2]string{"enter", "merge"},
		[2]string{"shift+tab", "back"},
		[2]string{"esc", "cancel"},
	))
	return b.String()
}

func (m Model) viewProgress() string {
	var b strings.Builder
	nums := m.selectedNumbers()

	b.WriteString(fmt.Sprintf("%s Merging %s into %s via %s…\n",
		m.spinner.View(),
		numberStyle.Render(prNumberList(nums)),
		accentStyle.Render(m.opts.BaseRef),
		accentStyle.Render(methodLabel(m.method)),
	))
	if m.message != "" {
		b.WriteString(faintStyle.Render(m.message) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(faintStyle.Render("ctrl+c: stop watching (the merge keeps running on GitHub)"))
	return b.String()
}

func (m Model) viewDone() string {
	var b strings.Builder
	nums := m.selectedNumbers()

	switch {
	case m.merged:
		b.WriteString(successStyle.Render("✓ Merged") + " ")
		b.WriteString(fmt.Sprintf("%s into %s.\n", numberStyle.Render(prNumberList(nums)), m.opts.BaseRef))
		if m.status.SHA != "" {
			b.WriteString(faintStyle.Render("Merge commit "+shortSHA(m.status.SHA)) + "\n")
		}
	case m.failed:
		b.WriteString(failureStyle.Render("✗ Merge failed") + "\n")
		if m.message != "" {
			b.WriteString(mutedStyle.Render(m.message) + "\n")
		}
		b.WriteString(faintStyle.Render("The stack is atomic, so nothing was merged.") + "\n")
	case m.cancelled:
		b.WriteString(mutedStyle.Render("Merge cancelled.") + "\n")
	default:
		if m.message != "" {
			b.WriteString(mutedStyle.Render(m.message) + "\n")
		}
	}
	return b.String()
}

func shortcuts(entries ...[2]string) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, shortcutKey.Render(e[0])+" "+shortcutLabel.Render(e[1]))
	}
	return strings.Join(parts, faintStyle.Render("  ·  "))
}

// prCount renders a pull-request count with correct pluralization: "1 PR" or
// "N PRs".
func prCount(n int) string {
	if n == 1 {
		return "1 PR"
	}
	return fmt.Sprintf("%d PRs", n)
}

func methodLabel(method string) string {
	switch method {
	case "merge":
		return "Create a merge commit"
	case "squash":
		return "Squash and merge"
	case "rebase":
		return "Rebase and merge"
	default:
		return method
	}
}

func prNumberList(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
