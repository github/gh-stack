package checkoutview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/github/gh-stack/internal/tui/shared"
)

// maxStatusBoxes caps the number of cells in the status bar. The bar is a
// high-level summary, not one cell per PR: larger stacks are downsampled
// proportionally across these cells so a 3-branch and a 100-PR stack both stay
// compact.
const maxStatusBoxes = 5

// statusBox is the glyph used for each cell in the status bar. A lower
// three-quarters block is full width (so cells read as one continuous bar, like
// a full block) but only ~75% of the cell height, leaving an empty strip at the
// top of every cell. That keeps a consistent vertical gap between rows across
// terminals — a full block █ has no gap and merges vertically on terminals with
// no line spacing (e.g. Ghostty), while ■ renders rounded on some terminals.
const statusBox = "▆"

// Muted status-bar colors: each is a blend of the corresponding vivid PR-state
// color toward the faint text gray, so the bar stays legible but understated in
// both light and dark terminals.
var (
	statusMergedColor = lipgloss.AdaptiveColor{Dark: "#957dc1", Light: "#816abf"} // muted purple
	statusOpenColor   = lipgloss.AdaptiveColor{Dark: "#509661", Light: "#488462"} // muted green
	statusClosedColor = lipgloss.AdaptiveColor{Dark: "#b55d5d", Light: "#ab515d"} // muted red
)

var (
	// Title and tab strip.
	titleStyle       = lipgloss.NewStyle().Foreground(shared.ColorText).Bold(true)
	tabActiveStyle   = lipgloss.NewStyle().Foreground(shared.ColorText).Background(shared.ColorRowShade).Bold(true).Padding(0, 1)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(shared.ColorTextMuted).Padding(0, 1)

	// Table chrome.
	headerStyle   = lipgloss.NewStyle().Foreground(shared.ColorTextMuted).Bold(true)
	selectorStyle = lipgloss.NewStyle().Foreground(shared.ColorAccent).Bold(true)

	// Columns.
	numberStyle     = lipgloss.NewStyle().Foreground(shared.ColorText)
	summaryStyle    = lipgloss.NewStyle().Foreground(shared.ColorText)
	dotsStyle       = lipgloss.NewStyle().Foreground(shared.ColorTextFaint)
	baseStyle       = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
	localTypeStyle  = lipgloss.NewStyle().Foreground(shared.ColorText)
	remoteTypeStyle = lipgloss.NewStyle().Foreground(shared.ColorPurple)
	createdStyle    = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
	dimStyle        = lipgloss.NewStyle().Foreground(shared.ColorTextFaint)

	// Status squares use muted, desaturated variants of the PR-state colors so
	// the bar reads as a subtle progress indicator and does not pull focus from
	// the leading columns. Unpushed uses the faint text color (least prominent).
	statusMergedStyle   = lipgloss.NewStyle().Foreground(statusMergedColor)
	statusOpenStyle     = lipgloss.NewStyle().Foreground(statusOpenColor)
	statusClosedStyle   = lipgloss.NewStyle().Foreground(statusClosedColor)
	statusUnpushedStyle = lipgloss.NewStyle().Foreground(shared.ColorTextFaint)

	// Search + footer.
	searchLabelStyle = lipgloss.NewStyle().Foreground(shared.ColorAccent).Bold(true)
	searchTextStyle  = lipgloss.NewStyle().Foreground(shared.ColorText)
	footerKeyStyle   = lipgloss.NewStyle().Foreground(shared.ColorAccent)
	footerDescStyle  = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
	footerSepStyle   = lipgloss.NewStyle().Foreground(shared.ColorBorder)
	emptyStyle       = lipgloss.NewStyle().Foreground(shared.ColorTextMuted)
)

// typeStyle returns the style for a stack's Type column.
func typeStyle(t StackType) lipgloss.Style {
	if t == TypeRemote {
		return remoteTypeStyle
	}
	return localTypeStyle
}

// statusBar renders a stack's composition as colored squares: purple (merged),
// green (open), red (closed), gray (unpushed), left to right. It returns "—"
// for an empty stack. Stacks with more than maxStatusBoxes branches are
// downsampled proportionally. When selected, each square carries the
// selected-row background so the shade is continuous.
func statusBar(c StatusCounts, selected bool) string {
	if c.Total() == 0 {
		return styleBg(dimStyle, selected).Render("—")
	}
	boxes := distribute(c.boxOrder(), maxStatusBoxes)
	styles := []lipgloss.Style{statusMergedStyle, statusOpenStyle, statusClosedStyle, statusUnpushedStyle}
	var b strings.Builder
	for i, n := range boxes {
		if n <= 0 {
			continue
		}
		b.WriteString(styleBg(styles[i], selected).Render(strings.Repeat(statusBox, n)))
	}
	return b.String()
}

// boxOrder returns the counts in status-bar render order.
func (c StatusCounts) boxOrder() []int {
	return []int{c.Merged, c.Open, c.Closed, c.Unpushed}
}

// distribute scales counts so they sum to at most max, using the largest-
// remainder method when the total exceeds max. When the total already fits, the
// counts are returned unchanged (one square per branch).
func distribute(counts []int, max int) []int {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total <= max {
		out := make([]int, len(counts))
		copy(out, counts)
		return out
	}

	out := make([]int, len(counts))
	rem := make([]float64, len(counts))
	assigned := 0
	for i, n := range counts {
		q := float64(n) * float64(max) / float64(total)
		out[i] = int(q)
		rem[i] = q - float64(out[i])
		assigned += out[i]
	}
	for assigned < max {
		best, bestI := -1.0, -1
		for i := range counts {
			if rem[i] > best {
				best, bestI = rem[i], i
			}
		}
		if bestI < 0 {
			break
		}
		out[bestI]++
		rem[bestI] = -1
		assigned++
	}
	return out
}

// padRight pads s with spaces to at least width visible columns.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncate shortens s to at most width visible columns, appending an ellipsis
// when it had to cut. It resets styling at the cut so trailing ANSI does not
// leak.
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
		if w >= width-1 {
			b.WriteString("…")
			b.WriteString("\x1b[0m")
			break
		}
		b.WriteRune(r)
		w++
	}
	return b.String()
}
