package mergeview

import (
	"fmt"
	"os"
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
	// selectedTitleStyle makes the selected PR's title stand out a touch more
	// than the others while staying white/black.
	selectedTitleStyle = lipgloss.NewStyle().Foreground(theme.ColorText).Bold(true)

	shortcutKey   = lipgloss.NewStyle().Foreground(theme.ColorText)
	shortcutLabel = lipgloss.NewStyle().Foreground(theme.ColorTextMuted)
)

// stepArrow is the Powerline right-triangle separator, rendered in the current
// segment's background color over the next segment's background so the arrow
// blends seamlessly into the shading.
const stepArrow = "\ue0b0"

var wizardSteps = []string{"Select PRs", "Select Merge Method", "Confirm"}

// View implements tea.Model.
func (m Model) View() string {
	var s string
	switch m.step {
	case StepSelectPRs:
		s = m.banner() + m.viewSelect()
	case StepMethod:
		s = m.banner() + m.viewMethod()
	case StepConfirm:
		s = m.banner() + m.viewConfirm()
	case StepProgress:
		// Once the merge is submitted, hide the header/wizard and just show
		// live progress.
		s = m.viewProgress()
	default:
		// StepDone: render nothing so the inline TUI clears itself on exit; the
		// command prints the final outcome.
		return ""
	}
	// Ensure no rendered line exceeds the terminal width; otherwise a line wraps,
	// the inline renderer miscounts its height, and repainting (e.g. on resize)
	// leaves duplicated header lines behind.
	return clampToWidth(s, m.width)
}

// banner renders the persistent title and wizard stepper shown at the top of
// every step, followed by a single blank line of spacing.
func (m Model) banner() string {
	title := "Merge stack"
	if m.opts.StackNumber > 0 {
		title = fmt.Sprintf("Merge stack #%d", m.opts.StackNumber)
	}
	return titleStyle.Render(title) + "\n" + m.stepper() + "\n\n"
}

// stepBg returns the background color for the step at index i given the current
// active step: completed steps are green, the active step is the brightest
// (near-white on dark, near-black on light), and upcoming steps are a dim gray.
func stepBg(i, cur int) lipgloss.TerminalColor {
	switch {
	case i < cur:
		return theme.ColorGreen
	case i == cur:
		return theme.ColorText
	default:
		return theme.ColorBorder
	}
}

// stepFg returns the foreground color for the step at index i: dark text on the
// bright/green segments, and a dim muted text on the upcoming gray segments.
func stepFg(i, cur int) lipgloss.TerminalColor {
	if i > cur {
		return theme.ColorTextMuted
	}
	return theme.ColorOnFill
}

func (m Model) stepper() string {
	cur := m.wizardIndex()
	var b strings.Builder
	n := len(wizardSteps)
	for i, label := range wizardSteps {
		bg := stepBg(i, cur)
		icon := "•"
		if i < cur {
			icon = "✓"
		}
		seg := lipgloss.NewStyle().Background(bg).Foreground(stepFg(i, cur)).Bold(i == cur).Padding(0, 1)
		b.WriteString(seg.Render(icon + " " + label))

		if m.usePowerline {
			// Powerline separator: the current background color, over the next
			// segment's background (or the terminal default after the last step).
			arrow := lipgloss.NewStyle().Foreground(bg)
			if i < n-1 {
				arrow = arrow.Background(stepBg(i+1, cur))
			}
			b.WriteString(arrow.Render(stepArrow))
		}
		// Fallback: segments abut directly, so their background colors form a
		// seamless segmented bar without any Powerline glyph.
	}
	return b.String()
}

// powerlineEnabled reports whether the terminal is known to render Powerline
// glyphs (U+E0Bx). Most terminals need a patched/Nerd font, so this defaults to
// off and only opts in for terminals with built-in Powerline glyph support,
// avoiding the missing-glyph box seen in e.g. Apple Terminal. Set
// GH_STACK_POWERLINE=1/0 to override.
func powerlineEnabled() bool {
	switch strings.ToLower(os.Getenv("GH_STACK_POWERLINE")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "ghostty", "WezTerm":
		return true
	}
	switch os.Getenv("TERM") {
	case "xterm-ghostty", "xterm-kitty":
		return true
	}
	return os.Getenv("KITTY_WINDOW_ID") != ""
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

	n := len(m.opts.PRs)
	h := m.visibleItems()
	start := m.scrollOffset
	if start > n-h {
		start = n - h
	}
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > n {
		end = n
	}

	// Reserve the indicator lines at all times (blank when nothing is hidden) so
	// the list doesn't shift as the ↑/↓ hints appear and disappear while scrolling.
	if start > 0 {
		b.WriteString(faintStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
	} else {
		b.WriteString("\n")
	}
	// Render top of stack first so the layout matches the CLI.
	for r := start; r < end; r++ {
		i := n - 1 - r
		pr := m.opts.PRs[i]
		selected := i <= m.topIndex

		cursorMark := "  "
		if i == m.cursor {
			cursorMark = accentStyle.Render("❯ ")
		}
		box := mutedStyle.Render("[ ]")
		if selected {
			box = checkedStyle.Render("[x]")
		}
		// Title: white/black for all, a touch bolder when selected.
		titleField := textStyle
		// Number + branch: gray for all, fainter when deselected.
		metaField := faintStyle
		if selected {
			titleField = selectedTitleStyle
			metaField = mutedStyle
		}
		title := pr.Title
		if title == "" {
			title = pr.Branch
		}
		b.WriteString(fmt.Sprintf("%s%s %s\n", cursorMark, box, titleField.Render(title)))
		b.WriteString("      " + metaField.Render(fmt.Sprintf("#%d • %s", pr.Number, pr.Branch)) + "\n")
	}
	if end < n {
		b.WriteString(faintStyle.Render(fmt.Sprintf("  ↓ %d more", n-end)) + "\n")
	} else {
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.topIndex >= 0 {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("Will merge %s into %s.", prCount(m.topIndex+1), m.opts.BaseRef)))
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

	b.WriteString(fmt.Sprintf("%s into %s with %s.\n",
		titleStyle.Render("Merge "+prCount(len(nums))),
		accentStyle.Render(m.opts.BaseRef),
		accentStyle.Render(methodLabel(m.method)),
	))
	// Wrap the PR list so a long stack isn't cut off at the screen edge.
	listStyle := numberStyle
	if m.width > 0 {
		listStyle = listStyle.Width(m.width)
	}
	b.WriteString(listStyle.Render(prNumberList(nums)) + "\n\n")
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

	b.WriteString(fmt.Sprintf("%s Merging %s into %s via %s\n",
		m.spinner.View(),
		numberStyle.Render(prNumberList(nums)),
		accentStyle.Render(m.opts.BaseRef),
		accentStyle.Render(methodLabel(m.method)),
	))
	// Always render a status line so it doesn't pop in later and shift the view.
	b.WriteString(faintStyle.Render(progressStatus(m.message)) + "\n")
	b.WriteString("\n")
	b.WriteString(faintStyle.Render("ctrl+c: stop watching (the merge keeps running on GitHub)"))
	return b.String()
}

// progressStatus normalizes an async-merge status message for display: a blank
// message shows an initial "Submitting…" line, and messages end in an ellipsis
// rather than a period.
func progressStatus(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "Submitting merge request..."
	}
	return strings.TrimRight(msg, ". ") + "..."
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

// clampToWidth truncates every line of s to at most width cells so nothing
// wraps.
func clampToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if lipgloss.Width(ln) > width {
			lines[i] = truncate(ln, width)
		}
	}
	return strings.Join(lines, "\n")
}

// truncate shortens s to at most width display cells, appending an ellipsis and
// resetting styling. It skips ANSI escape sequences when counting width.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
		}
		if inEscape {
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			b.WriteString("…")
			b.WriteString("\x1b[0m")
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}
