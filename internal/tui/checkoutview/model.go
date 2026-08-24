package checkoutview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/github/gh-stack/internal/tui/shared"
)

// tab identifies the active filter tab.
type tab int

const (
	tabAll tab = iota
	tabLocal
	tabRemote
)

func (t tab) String() string {
	switch t {
	case tabLocal:
		return "Local"
	case tabRemote:
		return "Remote"
	default:
		return "All"
	}
}

// keyMap holds the picker's key bindings.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	VimUp   key.Binding
	VimDown key.Binding
	Select  key.Binding
	NextTab key.Binding
	PrevTab key.Binding
	Search  key.Binding
	Quit    key.Binding
}

var keys = keyMap{
	Up:      key.NewBinding(key.WithKeys("up", "ctrl+p")),
	Down:    key.NewBinding(key.WithKeys("down", "ctrl+n")),
	VimUp:   key.NewBinding(key.WithKeys("k")),
	VimDown: key.NewBinding(key.WithKeys("j")),
	Select:  key.NewBinding(key.WithKeys("enter")),
	NextTab: key.NewBinding(key.WithKeys("tab", "right")),
	PrevTab: key.NewBinding(key.WithKeys("shift+tab", "left")),
	Search:  key.NewBinding(key.WithKeys("/")),
	Quit:    key.NewBinding(key.WithKeys("esc", "q")),
}

// Model is the Bubble Tea model for the interactive checkout stack picker.
type Model struct {
	rows     []StackRow // all reconciled rows (stable order)
	filtered []StackRow // rows matching the active tab + search query

	tab       tab
	query     string
	searching bool

	cursor       int
	scrollOffset int
	width        int
	height       int

	result    StackRow
	hasResult bool
	cancelled bool
}

// New creates a picker model over the given reconciled rows.
func New(rows []StackRow) Model {
	m := Model{rows: rows}
	m.applyFilter()
	return m
}

// Result returns the selected row and whether one was chosen (Enter). It is
// false when the user cancelled or nothing was selectable.
func (m Model) Result() (StackRow, bool) {
	return m.result, m.hasResult
}

// Cancelled reports whether the user dismissed the picker without selecting.
func (m Model) Cancelled() bool {
	return m.cancelled
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureVisible()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		m.cancelled = true
		return m, tea.Quit

	case key.Matches(msg, keys.Up) || (!m.searching && key.Matches(msg, keys.VimUp)):
		m.moveCursor(-1)
		return m, nil

	case key.Matches(msg, keys.Down) || (!m.searching && key.Matches(msg, keys.VimDown)):
		m.moveCursor(1)
		return m, nil

	case key.Matches(msg, keys.Select):
		if row, ok := m.current(); ok {
			m.result = row
			m.hasResult = true
			return m, tea.Quit
		}
		return m, nil

	case key.Matches(msg, keys.NextTab):
		m.cycleTab(1)
		return m, nil

	case key.Matches(msg, keys.PrevTab):
		m.cycleTab(-1)
		return m, nil

	case key.Matches(msg, keys.Search) && !m.searching:
		m.searching = true
		// Entering search adds the search line, shrinking the body; keep the
		// cursor visible so Enter can't select an off-screen row.
		m.ensureVisible()
		return m, nil

	case !m.searching && key.Matches(msg, keys.Quit):
		m.cancelled = true
		return m, tea.Quit
	}

	if m.searching {
		return m.updateSearch(msg)
	}
	return m, nil
}

// updateSearch handles text entry while the search field is focused.
func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.searching = false
		m.query = ""
		m.applyFilter()
	case tea.KeyBackspace:
		if r := []rune(m.query); len(r) > 0 {
			m.query = string(r[:len(r)-1])
			m.applyFilter()
		}
	case tea.KeySpace:
		m.query += " "
		m.applyFilter()
	case tea.KeyRunes:
		m.query += string(msg.Runes)
		m.applyFilter()
	}
	return m, nil
}

// cycleTab moves the active tab by delta and resets the cursor.
func (m *Model) cycleTab(delta int) {
	m.tab = tab((int(m.tab) + delta + 3) % 3)
	m.cursor = 0
	m.scrollOffset = 0
	m.applyFilter()
}

// applyFilter recomputes the visible rows from the active tab and query, then
// clamps the cursor and scroll.
func (m *Model) applyFilter() {
	m.filtered = m.filtered[:0]
	for _, r := range m.rows {
		if matchesTab(r, m.tab) && matchesQuery(r, m.query) {
			m.filtered = append(m.filtered, r)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.ensureVisible()
}

func matchesTab(r StackRow, t tab) bool {
	switch t {
	case tabLocal:
		return r.Type == TypeLocal
	case tabRemote:
		return r.Type == TypeRemote
	default:
		return true
	}
}

func matchesQuery(r StackRow, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	// Search the stack number, base, type, and every branch name — including
	// mid-stack branches that are not shown in the Branches column.
	parts := make([]string, 0, len(r.Branches)+5)
	parts = append(parts, r.NumberDisplay(), r.Base, r.Type.String(), r.BottomBranch, r.TopBranch)
	parts = append(parts, r.Branches...)
	hay := strings.ToLower(strings.Join(parts, " "))
	return strings.Contains(hay, q)
}

// current returns the row under the cursor, if any.
func (m Model) current() (StackRow, bool) {
	if m.cursor >= 0 && m.cursor < len(m.filtered) {
		return m.filtered[m.cursor], true
	}
	return StackRow{}, false
}

// moveCursor moves the selection by delta within the filtered rows.
func (m *Model) moveCursor(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	m.ensureVisible()
}

// maxVisibleRows caps how many stack rows the inline picker shows at once. The
// rest are reached by scrolling, so the picker never takes over the screen.
const maxVisibleRows = 10

// bodyHeight returns the number of table rows shown at once: the row count
// capped at maxVisibleRows, and further shrunk to fit a short terminal. It never
// returns less than 1 (a line is reserved for the empty-state message).
func (m Model) bodyHeight() int {
	rows := len(m.filtered)
	if rows < 1 {
		rows = 1
	}
	limit := maxVisibleRows
	if m.height > 0 {
		if avail := m.height - m.chromeHeight(); avail < limit {
			limit = avail
		}
	}
	if limit < 1 {
		limit = 1
	}
	if rows > limit {
		rows = limit
	}
	return rows
}

// chromeHeight is the number of lines reserved around the table body when
// fitting the picker to a short terminal: title, tabs, blank, header, footer
// (5), plus one line of breathing room so the inline picker never exactly fills
// the terminal (which would make it scroll). The search line adds one more.
func (m Model) chromeHeight() int {
	chrome := 6
	if m.searching {
		chrome++
	}
	return chrome
}

// ensureVisible adjusts the scroll offset so the cursor stays on screen.
func (m *Model) ensureVisible() {
	m.scrollOffset = shared.EnsureVisible(m.cursor, m.cursor+1, m.scrollOffset, m.bodyHeight())
}

// --- layout ---

type layout struct {
	num, summary, base, status, typ, created int
}

const colSep = "  "

// computeLayout derives column widths from all rows (stable across filtering)
// and the terminal width.
func (m Model) computeLayout() layout {
	l := layout{
		num:     lipgloss.Width("#"),
		base:    lipgloss.Width("Base"),
		status:  lipgloss.Width("Status"),
		typ:     lipgloss.Width("Remote"),
		created: lipgloss.Width("Created"),
	}
	for i := range m.rows {
		r := &m.rows[i]
		l.num = maxInt(l.num, lipgloss.Width(r.NumberDisplay()))
		l.base = maxInt(l.base, lipgloss.Width(r.Base))
		l.status = maxInt(l.status, statusWidth(r.Status))
		l.created = maxInt(l.created, lipgloss.Width(r.CreatedDisplay()))
	}
	if l.base > 24 {
		l.base = 24
	}

	sep := lipgloss.Width(colSep)
	// selector(2) + num + base + status + typ + created + 5 separators between
	// the six data columns.
	fixed := 2 + l.num + l.base + l.status + l.typ + l.created + sep*5
	l.summary = m.width - fixed
	if l.summary < 10 {
		l.summary = 10
	}
	return l
}

// statusWidth returns the visible width of a status bar for the given counts.
func statusWidth(c StatusCounts) int {
	t := c.Total()
	if t == 0 {
		return 1
	}
	if t > maxStatusBoxes {
		return maxStatusBoxes
	}
	return t
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- view ---

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	// On exit, render nothing so the inline picker clears itself; the caller
	// prints the outcome (the switched-to branch, or nothing on cancel).
	if m.hasResult || m.cancelled {
		return ""
	}

	lay := m.computeLayout()
	var b strings.Builder

	b.WriteString(m.renderTitle())
	b.WriteString("\n")
	b.WriteString(m.renderTabs())
	b.WriteString("\n\n")
	b.WriteString(m.renderHeaderRow(lay))
	b.WriteString("\n")
	b.WriteString(m.renderBody(lay))
	b.WriteString("\n")
	if m.searching {
		b.WriteString(m.renderSearch())
		b.WriteString("\n")
	}
	b.WriteString(m.renderFooter())

	// Guarantee no rendered line exceeds the terminal width. Otherwise a line
	// wraps, the inline renderer's line count is off, and the bounded/clear
	// behavior breaks on narrow terminals.
	return clampToWidth(b.String(), m.width)
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

func (m Model) renderTitle() string {
	left := titleStyle.Render("Checkout a stack")
	right := m.positionIndicator()
	if right == "" {
		return left
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// positionIndicator returns a right-aligned "start–end of total" hint when the
// list is scrolled (more rows than fit at once), or "" when everything fits.
func (m Model) positionIndicator() string {
	total := len(m.filtered)
	vis := m.bodyHeight()
	if total == 0 || total <= vis {
		return ""
	}
	start := m.scrollOffset + 1
	end := m.scrollOffset + vis
	if end > total {
		end = total
	}
	return dimStyle.Render(fmt.Sprintf("%d–%d of %d", start, end, total))
}

func (m Model) renderTabs() string {
	labels := []tab{tabAll, tabLocal, tabRemote}
	parts := make([]string, 0, len(labels))
	for _, t := range labels {
		if t == m.tab {
			parts = append(parts, tabActiveStyle.Render(t.String()))
		} else {
			parts = append(parts, tabInactiveStyle.Render(t.String()))
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) renderHeaderRow(lay layout) string {
	var b strings.Builder
	b.WriteString("  ") // selector column
	b.WriteString(headerStyle.Render(padRight("#", lay.num)))
	b.WriteString(colSep)
	b.WriteString(headerStyle.Render(padRight("Branches", lay.summary)))
	b.WriteString(colSep)
	b.WriteString(headerStyle.Render(padRight("Base", lay.base)))
	b.WriteString(colSep)
	b.WriteString(headerStyle.Render(padRight("Status", lay.status)))
	b.WriteString(colSep)
	b.WriteString(headerStyle.Render(padRight("Type", lay.typ)))
	b.WriteString(colSep)
	b.WriteString(headerStyle.Render(padRight("Created", lay.created)))
	return b.String()
}

func (m Model) renderBody(lay layout) string {
	bodyH := m.bodyHeight()
	if len(m.filtered) == 0 {
		return m.padBody(emptyStyle.Render("  No stacks match."), bodyH)
	}

	var lines []string
	end := m.scrollOffset + bodyH
	if end > len(m.filtered) {
		end = len(m.filtered)
	}
	for i := m.scrollOffset; i < end; i++ {
		lines = append(lines, m.renderRow(m.filtered[i], i == m.cursor, lay))
	}
	return m.padBody(strings.Join(lines, "\n"), bodyH)
}

// padBody pads content with blank lines so the body always occupies bodyH rows,
// keeping the footer anchored.
func (m Model) padBody(content string, bodyH int) string {
	lines := strings.Count(content, "\n") + 1
	if content == "" {
		lines = 0
	}
	if lines >= bodyH {
		return content
	}
	pad := strings.Repeat("\n", bodyH-lines)
	if content == "" {
		return strings.TrimPrefix(pad, "\n")
	}
	return content + pad
}

func (m Model) renderRow(r StackRow, selected bool, lay layout) string {
	var b strings.Builder

	// Selector.
	selector := "  "
	if selected {
		selector = styleBg(selectorStyle, true).Render("›") + styleBg(lipgloss.NewStyle(), true).Render(" ")
	}
	b.WriteString(selector)

	// #, Branches, Base, Status, Type, Created. A missing stack number is
	// rendered faint so it stays unobtrusive.
	numStyle := numberStyle
	if r.Number == 0 {
		numStyle = dimStyle
	}
	b.WriteString(renderCell(r.NumberDisplay(), numStyle, lay.num, selected))
	b.WriteString(sepCell(selected))
	b.WriteString(renderSummaryCell(r, lay.summary, selected))
	b.WriteString(sepCell(selected))
	b.WriteString(renderCell(r.Base, baseStyle, lay.base, selected))
	b.WriteString(sepCell(selected))
	b.WriteString(renderStatusCell(r.Status, lay.status, selected))
	b.WriteString(sepCell(selected))
	b.WriteString(renderCell(r.Type.String(), typeStyle(r.Type), lay.typ, selected))
	b.WriteString(sepCell(selected))
	b.WriteString(renderCell(r.CreatedDisplay(), createdStyle, lay.created, selected))

	line := b.String()
	// Extend the selected-row background to the right edge.
	if selected {
		gap := m.width - lipgloss.Width(line)
		if gap > 0 {
			line += styleBg(lipgloss.NewStyle(), true).Render(strings.Repeat(" ", gap))
		}
	}
	return line
}

// renderSummaryCell renders "bottom...top" with a dimmed separator, each part
// carrying the selected-row background so styling stays continuous. When the
// text is too wide it falls back to a single truncated segment.
func renderSummaryCell(r StackRow, width int, selected bool) string {
	if lipgloss.Width(r.Summary()) > width {
		return renderCell(r.Summary(), summaryStyle, width, selected)
	}
	var seg string
	if r.BottomBranch != "" && r.TopBranch != "" && r.BottomBranch != r.TopBranch {
		seg = styleBg(summaryStyle, selected).Render(r.BottomBranch) +
			styleBg(dotsStyle, selected).Render(branchSep) +
			styleBg(summaryStyle, selected).Render(r.TopBranch)
	} else {
		seg = styleBg(summaryStyle, selected).Render(r.Summary())
	}
	if pad := width - lipgloss.Width(seg); pad > 0 {
		seg += styleBg(lipgloss.NewStyle(), selected).Render(strings.Repeat(" ", pad))
	}
	return seg
}

func (m Model) renderSearch() string {
	return searchLabelStyle.Render("/ ") + searchTextStyle.Render(m.query) + dimStyle.Render("▏")
}

func (m Model) renderFooter() string {
	var pairs [][2]string
	if m.searching {
		pairs = [][2]string{
			{"↓↑", "navigate"},
			{"enter", "select"},
			{"esc", "clear search"},
		}
	} else {
		pairs = [][2]string{
			{"↓↑/jk", "navigate"},
			{"←→", "tabs"},
			{"/", "search"},
			{"enter", "select"},
			{"esc", "quit"},
		}
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, footerKeyStyle.Render(p[0])+" "+footerDescStyle.Render(p[1]))
	}
	return strings.Join(parts, footerSepStyle.Render(" · "))
}

// --- cell rendering helpers ---

// styleBg adds the selected-row background to a style when selected.
func styleBg(st lipgloss.Style, selected bool) lipgloss.Style {
	if selected {
		return st.Background(shared.ColorRowShade)
	}
	return st
}

// renderCell renders text in st, truncated/padded to width, applying the
// selected-row background to text and padding when selected.
func renderCell(text string, st lipgloss.Style, width int, selected bool) string {
	text = truncate(text, width)
	rendered := styleBg(st, selected).Render(text)
	if pad := width - lipgloss.Width(rendered); pad > 0 {
		rendered += styleBg(lipgloss.NewStyle(), selected).Render(strings.Repeat(" ", pad))
	}
	return rendered
}

// renderStatusCell renders the status bar padded to width, shaded when selected.
func renderStatusCell(c StatusCounts, width int, selected bool) string {
	bar := statusBar(c, selected)
	if pad := width - lipgloss.Width(bar); pad > 0 {
		bar += styleBg(lipgloss.NewStyle(), selected).Render(strings.Repeat(" ", pad))
	}
	return bar
}

// sepCell renders the inter-column separator, shaded when selected.
func sepCell(selected bool) string {
	if selected {
		return styleBg(lipgloss.NewStyle(), true).Render(colSep)
	}
	return colSep
}
