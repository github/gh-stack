package submitview

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/tui/shared"
)

// --- Update ---

// updateScreen handles all key input on the single submit screen.
func (m Model) updateScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys, handled regardless of focus.
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEsc:
		m.saveEditor()
		return m.quit()
	case tea.KeyCtrlS:
		m.saveEditor()
		return m.requestSubmit()
	case tea.KeyCtrlX:
		cmd := m.toggleInclude()
		return m, cmd
	case tea.KeyCtrlO:
		m.openFocusedPR()
		return m, nil
	case tea.KeyCtrlH:
		// Global help so it works even while editing a field (where ? is typed).
		m.showHelp = true
		return m, nil
	}

	node := m.currentNode()
	editable := node != nil && node.State == StateNew && node.Included

	// Field cycling and editor controls.
	if editable {
		switch msg.Type {
		case tea.KeyCtrlP:
			cmd := m.togglePreview()
			return m, cmd
		case tea.KeyCtrlE:
			return m.openEditor()
		case tea.KeyTab:
			m.saveEditor()
			cmd := m.advanceField()
			return m, cmd
		case tea.KeyShiftTab:
			m.saveEditor()
			cmd := m.retreatField()
			return m, cmd
		}
	} else {
		// Skipped NEW or locked: tab just moves between branches.
		switch msg.Type {
		case tea.KeyTab:
			cmd := m.moveCursor(1)
			return m, cmd
		case tea.KeyShiftTab:
			cmd := m.moveCursor(-1)
			return m, cmd
		}
	}

	// Up/down move between branches, except while actively editing the
	// multi-line description text.
	editingDesc := editable && m.focusedField == fieldDescription && !m.descPreview
	if !editingDesc {
		switch msg.Type {
		case tea.KeyUp:
			cmd := m.moveCursor(-1)
			return m, cmd
		case tea.KeyDown:
			cmd := m.moveCursor(1)
			return m, cmd
		}
	}

	// Locked branch (Mode 3): read-only card.
	if node != nil && (node.State.Locked() || node.State.Blocks()) {
		switch msg.String() {
		case "?":
			m.showHelp = true
			return m, nil
		case "q":
			return m.quit()
		}
		return m, nil
	}

	// Skipped NEW branch (Mode 2): only the Create-PR toggle is actionable.
	if node != nil && node.State == StateNew && !node.Included {
		switch {
		case isSpaceKey(msg) || msg.Type == tea.KeyEnter:
			cmd := m.toggleInclude()
			return m, cmd
		case msg.String() == "?":
			m.showHelp = true
			return m, nil
		case msg.String() == "q":
			return m.quit()
		}
		return m, nil
	}

	// Included NEW branch (Mode 1): field-specific handling.
	switch m.focusedField {
	case fieldDraft:
		switch msg.Type {
		case tea.KeyLeft:
			if node != nil {
				node.Draft = false
			}
			return m, nil
		case tea.KeyRight:
			if node != nil {
				node.Draft = true
			}
			return m, nil
		}
		switch {
		case isSpaceKey(msg) || msg.Type == tea.KeyEnter:
			if node != nil {
				node.Draft = !node.Draft
			}
			return m, nil
		case msg.String() == "?":
			m.showHelp = true
			return m, nil
		case msg.String() == "q":
			return m.quit()
		}
		return m, nil

	case fieldDescription:
		if m.descPreview {
			return m, nil
		}
		m.descScrollPinned = false // a keystroke snaps the view back to the cursor
		var cmd tea.Cmd
		m.descArea, cmd = m.descArea.Update(msg)
		if node != nil {
			node.Description = m.descArea.Value()
		}
		return m, cmd

	case fieldTitle:
		var cmd tea.Cmd
		m.titleInput, cmd = m.titleInput.Update(msg)
		if node != nil {
			node.Title = m.titleInput.Value()
		}
		return m, cmd
	}

	return m, nil
}

// --- Navigation & focus ---

// moveCursor moves the focused branch by delta (clamped), saving and reloading
// the editor and focusing the new branch's first field.
func (m *Model) moveCursor(delta int) tea.Cmd {
	next := m.cursor + delta
	if next < 0 || next >= len(m.nodes) {
		return nil
	}
	m.saveEditor()
	m.cursor = next
	m.scrollLeftToCursor()
	m.loadEditor()
	return m.focusFirstField()
}

// moveCursorFocusLast is moveCursor but focuses the new branch's last field
// (used by shift+tab onto the previous branch).
func (m *Model) moveCursorFocusLast(delta int) tea.Cmd {
	next := m.cursor + delta
	if next < 0 || next >= len(m.nodes) {
		return nil
	}
	m.saveEditor()
	m.cursor = next
	m.scrollLeftToCursor()
	m.loadEditor()
	return m.focusLastField()
}

// focusFirstField focuses the first focusable element of the current branch:
// the title for an included NEW branch, nothing (a neutral state) otherwise.
func (m *Model) focusFirstField() tea.Cmd {
	n := m.currentNode()
	if n != nil && n.State == StateNew && n.Included {
		return m.focusField(fieldTitle)
	}
	m.titleInput.Blur()
	m.descArea.Blur()
	m.focusedField = fieldTitle
	return nil
}

// focusLastField focuses the last focusable element of the current branch.
func (m *Model) focusLastField() tea.Cmd {
	n := m.currentNode()
	if n != nil && n.State == StateNew && n.Included {
		return m.focusField(fieldDraft)
	}
	m.titleInput.Blur()
	m.descArea.Blur()
	m.focusedField = fieldTitle
	return nil
}

// toggleInclude flips the included state of the focused NEW branch, cascading
// the stack dependency to dependent branches, and keeps the Create-PR toggle
// focused.
func (m *Model) toggleInclude() tea.Cmd {
	n := m.currentNode()
	if n == nil || n.State != StateNew {
		return nil
	}
	m.saveEditor()
	excluding := n.Included
	extra := m.applyIncludeCascade(m.cursor)
	m.setCascadeStatus(excluding, extra)
	return m.focusFirstField()
}

// applyIncludeCascade flips the included state of the NEW branch at idx and
// propagates the stack dependency: because each PR builds on the branch below
// it, excluding a branch also excludes every branch stacked above it, while
// including a branch also includes every branch below it that it depends on.
// The cascade stops at the first non-NEW (locked) branch, which already
// provides a base. It returns the number of OTHER branches whose inclusion
// changed, for the status hint.
func (m *Model) applyIncludeCascade(idx int) int {
	if idx < 0 || idx >= len(m.nodes) || m.nodes[idx].State != StateNew {
		return 0
	}
	if m.nodes[idx].Included {
		return m.excludeFrom(idx)
	}
	return m.includeFrom(idx)
}

// excludeFrom skips the NEW branch at idx and every NEW branch stacked above it
// (lower indices), stopping at the first locked branch. It returns the count of
// branches other than idx that changed.
func (m *Model) excludeFrom(idx int) int {
	changed := 0
	for i := idx; i >= 0; i-- {
		n := &m.nodes[i]
		if n.State != StateNew {
			break
		}
		if n.Included {
			n.Included = false
			if i != idx {
				changed++
			}
		}
	}
	return changed
}

// includeFrom includes the NEW branch at idx and every NEW branch below it
// (higher indices) that it depends on, stopping at the first locked branch. It
// returns the count of branches other than idx that changed.
func (m *Model) includeFrom(idx int) int {
	changed := 0
	for i := idx; i < len(m.nodes); i++ {
		n := &m.nodes[i]
		if n.State != StateNew {
			break
		}
		if !n.Included {
			n.Included = true
			if i != idx {
				changed++
			}
		}
	}
	return changed
}

// setCascadeStatus surfaces a transient hint when toggling a branch's inclusion
// also changed other branches because of the stack dependency.
func (m *Model) setCascadeStatus(excluded bool, extra int) {
	if extra <= 0 {
		return
	}
	noun := "branch"
	if extra > 1 {
		noun = "branches"
	}
	m.statusIsError = false
	if excluded {
		m.statusMessage = fmt.Sprintf("Also skipped %d %s stacked above — a PR builds on the branch below it", extra, noun)
		return
	}
	m.statusMessage = fmt.Sprintf("Also included %d %s below that this PR depends on", extra, noun)
}

// openFocusedPR opens the focused locked branch's PR in the browser.
func (m *Model) openFocusedPR() {
	n := m.currentNode()
	if n == nil {
		return
	}
	url := ""
	if n.PR != nil {
		url = n.PR.URL
	} else if n.Ref.PullRequest != nil {
		url = n.Ref.PullRequest.URL
	}
	if url == "" {
		return
	}
	if m.openURL != nil {
		m.openURL(url)
		return
	}
	shared.OpenBrowserInBackground(url)
}

// advanceField moves focus to the next field on an included NEW branch, flowing
// onto the next PR (the next included NEW branch up the stack) after the
// ready/draft toggle.
func (m *Model) advanceField() tea.Cmd {
	switch m.focusedField {
	case fieldTitle:
		return m.focusField(fieldDescription)
	case fieldDescription:
		return m.focusField(fieldDraft)
	case fieldDraft:
		if idx := m.nextEditableIndex(); idx != -1 {
			return m.moveCursor(idx - m.cursor)
		}
	}
	return nil
}

// retreatField moves focus to the previous field, flowing onto the previous PR
// (the next included NEW branch down the stack) before the title.
func (m *Model) retreatField() tea.Cmd {
	switch m.focusedField {
	case fieldDraft:
		return m.focusField(fieldDescription)
	case fieldDescription:
		return m.focusField(fieldTitle)
	case fieldTitle:
		if idx := m.prevEditableIndex(); idx != -1 {
			return m.moveCursorFocusLast(idx - m.cursor)
		}
	}
	return nil
}

// --- Editor field/content management ---

// focusField blurs all text components and focuses the given field, returning
// any cursor-blink command. The Create-PR and draft toggles are not text inputs.
func (m *Model) focusField(f editField) tea.Cmd {
	m.focusedField = f
	m.titleInput.Blur()
	m.descArea.Blur()
	switch f {
	case fieldTitle:
		return m.titleInput.Focus()
	case fieldDescription:
		if !m.descPreview {
			return m.descArea.Focus()
		}
	}
	return nil
}

// maxDescScroll is the largest valid description scroll offset: the number of
// wrapped rows beyond what the box can show at once.
func (m Model) maxDescScroll(innerW int) int {
	if n := m.currentNode(); n != nil && n.State.Locked() {
		max := len(m.descPreviewLines(innerW)) - m.lockedDescHeight()
		if max < 0 {
			return 0
		}
		return max
	}
	total := len(m.descFullLines(innerW))
	if m.descPreview {
		total = len(m.descPreviewLines(innerW))
	}
	max := total - m.descAreaHeight()
	if max < 0 {
		return 0
	}
	return max
}

// loadEditor loads the focused branch's draft into the input components.
func (m *Model) loadEditor() {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	n := m.nodes[m.cursor]
	m.titleInput.SetValue(n.Title)
	m.descArea.SetValue(n.Description)
	// Start at the top: SetValue leaves the cursor at the end, so move it back to
	// the first line and reset the scroll. This keeps a prefilled (e.g. template)
	// description scrolled to the top with the cursor on the first line.
	m.descArea.CursorStart()
	for i := 0; i < 100000 && m.descArea.Line() > 0; i++ {
		m.descArea.CursorUp()
	}
	m.descArea.CursorStart()
	m.descScroll = 0
	m.descScrollPinned = false
}

// saveEditor writes the input components back to the focused branch.
func (m *Model) saveEditor() {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	n := &m.nodes[m.cursor]
	if n.State != StateNew {
		return
	}
	n.Title = m.titleInput.Value()
	n.Description = m.descArea.Value()
}

// resizeEditor sizes the text components to the right panel's inner width.
func (m *Model) resizeEditor() {
	_, rightW := m.panelWidths()
	innerW := rightW - 4 // border + padding
	if innerW < 10 {
		innerW = 10
	}
	// Field boxes add a border (2) and horizontal padding (2). The description
	// also reserves columns for its scrollbar so its wrap matches descContent.
	m.titleInput.Width = innerW - 4
	m.descArea.SetWidth(descTextWidth(innerW))
	m.descArea.SetHeight(m.descAreaHeight())
}

// nextEditableIndex returns the index of the next PR up the stack — the nearest
// included NEW branch above the cursor (lower index) — or -1. PRs are created
// bottom-up, so "up the stack" is the next PR in the flow.
func (m Model) nextEditableIndex() int {
	for i := m.cursor - 1; i >= 0; i-- {
		if m.nodes[i].State == StateNew && m.nodes[i].Included {
			return i
		}
	}
	return -1
}

// prevEditableIndex returns the index of the previous PR — the nearest included
// NEW branch below the cursor (higher index) — or -1.
func (m Model) prevEditableIndex() int {
	for i := m.cursor + 1; i < len(m.nodes); i++ {
		if m.nodes[i].State == StateNew && m.nodes[i].Included {
			return i
		}
	}
	return -1
}

// prProgress returns the focused branch's position in the new-PR set and the
// total number of PRs to create. PRs are numbered bottom-up (the branch closest
// to trunk is PR 1). pos is 0 when the focused branch is not an included NEW
// branch.
func (m Model) prProgress() (pos, total int) {
	for i, n := range m.nodes {
		if n.State == StateNew && n.Included {
			total++
			if i >= m.cursor {
				pos++
			}
		}
	}
	if cur := m.currentNode(); cur == nil || cur.State != StateNew || !cur.Included {
		pos = 0
	}
	return pos, total
}

// requestSubmit validates the included PRs and, if they are complete, marks the
// batch submit as requested and exits. If any included branch has an empty
// title, it focuses that branch's title field and surfaces a hint instead.
func (m Model) requestSubmit() (tea.Model, tea.Cmd) {
	m.saveEditor()
	if idx := m.firstEmptyTitleIndex(); idx != -1 {
		m.cursor = idx
		m.loadEditor()
		_ = m.focusField(fieldTitle)
		m.statusMessage = "A title is required — fill it in for " + m.nodes[idx].Ref.Branch
		m.statusIsError = true
		return m, nil
	}
	m.submitRequested = true
	return m, tea.Quit
}

// firstEmptyTitleIndex returns the index of the first included NEW branch with a
// blank title, or -1.
func (m Model) firstEmptyTitleIndex() int {
	for i, n := range m.nodes {
		if n.State == StateNew && n.Included && strings.TrimSpace(n.Title) == "" {
			return i
		}
	}
	return -1
}

// --- View ---

// panelWidths returns the left and right panel outer widths.
func (m Model) panelWidths() (left, right int) {
	left = m.width * 30 / 100
	if left < 22 {
		left = 22
	}
	if left > 34 {
		left = 34
	}
	right = m.width - left - 1 // 1-column gap
	if right < 20 {
		right = 20
	}
	return left, right
}

// descAreaHeight returns the number of text rows for the description textarea,
// sized so the description box fills the right panel's remaining vertical space.
func (m Model) descAreaHeight() int {
	// Right-panel rows excluding the description text: header (1), separator
	// rule (1), TITLE label + box (4), DESCRIPTION label (1), description box
	// borders (2), OPEN-AS toggle (1), footer rule (1), footer strip (1) = 12.
	overhead := 12
	// contentHeight() already excludes the slim header and bottom bar; subtract
	// the panel border (2) and the chrome so the textarea fills the rest.
	h := m.contentHeight() - 2 - overhead
	if h < 3 {
		h = 3
	}
	return h
}

// lockedDescHeight returns the rows for the read-only description preview in a
// locked PR's card, sized to fill the panel. Its chrome is lighter than the
// editor's: header(1), rule(1), TITLE label(1) + title box(3), DESCRIPTION
// label(1), and the description box borders(2) = 9, plus the panel border (2).
func (m Model) lockedDescHeight() int {
	h := m.contentHeight() - 2 - 9
	if h < 3 {
		h = 3
	}
	return h
}

func (m Model) viewScreen() string {
	status := m.renderStatusLine()
	banner := m.renderClosedBanner()

	panelH := m.contentHeight()
	if banner != "" {
		panelH-- // the banner occupies one content row
	}
	leftW, rightW := m.panelWidths()

	left := m.renderLeftPanel(leftW, panelH)
	right := m.renderRightPanel(rightW, panelH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)

	var out strings.Builder
	if header := m.renderHeader(); header != "" {
		out.WriteString(header)
		out.WriteString("\n")
	} else {
		// The header (and its inline-image logo) is hidden; clear any logo that
		// was previously drawn so it does not linger in the graphics layer.
		out.WriteString(shared.ClearLogo())
	}
	if banner != "" {
		out.WriteString(banner)
		out.WriteString("\n")
	}
	out.WriteString(body)
	out.WriteString("\n")
	out.WriteString(status)
	return out.String()
}

// renderStatusLine renders the transient status/hint line at the bottom.
func (m Model) renderStatusLine() string {
	if m.statusMessage == "" {
		return ""
	}
	if m.statusIsError {
		return " " + calloutErrorStyle.Render("✗ "+m.statusMessage)
	}
	return " " + hintStyle.Render(m.statusMessage)
}

// leftRow is one rendered line of the left stack panel plus the metadata the
// mouse layer needs to map a click back to a branch.
type leftRow struct {
	text     string // rendered content (without the panel frame)
	branch   int    // owning branch index, or -1 for chrome (header/connector/trunk)
	nodeLine bool   // the branch's first line, where the right-edge checkbox sits
}

// renderLeftPanel renders the stack as a vertical timeline: a circle node per
// branch — filled cyan when it will become a PR, a dotted gray ring when skipped,
// a state-colored ring for an existing PR — joined by a spine down to the trunk.
// Existing PRs show "state · #num" on a second line. The focused branch's full
// row (and a gap above/below) is shaded edge to edge. Long names wrap, and the
// content scrolls vertically when it is taller than the panel.
func (m Model) renderLeftPanel(width, height int) string {
	fullW := width - 2 // border only; rows manage their own gutters
	if fullW < 6 {
		fullW = 6
	}
	rows := m.buildLeftRows(fullW)
	visH := height - 2
	if visH < 1 {
		visH = 1
	}
	scroll := clampScroll(m.leftScroll, len(rows), visH)
	end := scroll + visH
	if end > len(rows) {
		end = len(rows)
	}

	var b strings.Builder
	for i, r := range rows[scroll:end] {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.text)
	}
	return leftPanelBox(b.String(), width, height)
}

// leftPanelBox frames the left panel with the shared rounded border but no inner
// horizontal padding, so a focused row's shade can span the full inner width.
func leftPanelBox(content string, width, height int) string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Width(innerW).
		Height(innerH).
		MaxHeight(height).
		Render(content)
}

// buildLeftRows lays out the whole left panel: the STACK header, each branch's
// node/name/meta lines, a spine gap between branches (shaded as padding around
// the focused branch), and the trunk. It is deterministic given the
// nodes/cursor/width, so the mouse layer can recompute it to resolve clicks.
func (m Model) buildLeftRows(fullW int) []leftRow {
	cur := m.cursor
	rows := []leftRow{
		{text: pad(1, false) + sectionLabelStyle.Render("STACK"), branch: -1},
		{text: m.gapRow(fullW, false, cur == 0), branch: -1}, // blank under STACK; top pad for branch 0
	}
	for i := range m.nodes {
		rows = append(rows, m.branchRows(i, fullW)...)
		// Spine gap below branch i (to the next branch or the trunk). Shaded when
		// it borders the focused branch, giving the highlight vertical padding.
		rows = append(rows, leftRow{text: m.gapRow(fullW, true, i == cur || i+1 == cur), branch: -1})
	}
	rows = append(rows, leftRow{
		text:   pad(1, false) + spineStyle.Render("└─ ") + shared.TrunkStyle.Render(m.trunk.Branch),
		branch: -1,
	})
	return rows
}

// gapRow renders a full-width spacer row: a 1-col gutter, an optional spine, then
// shaded fill. Shaded gaps form the focused branch's vertical padding.
func (m Model) gapRow(fullW int, withSpine, shaded bool) string {
	lead := pad(1, shaded)
	if withSpine {
		lead += bgIf(spineStyle, shaded).Render("│")
	}
	return lead + pad(fullW-lipgloss.Width(lead), shaded)
}

// branchRows renders one branch: the node line (circle + name, plus a right-edge
// include checkbox for a NEW branch), any wrapped name lines (spine + name), and
// — for an existing PR — a "state · #num" line below the name. The focused
// branch is shaded edge to edge.
func (m Model) branchRows(idx, fullW int) []leftRow {
	n := m.nodes[idx]
	focused := idx == m.cursor
	nameStyle := m.branchNameStyle(n, focused)

	// Lead is gutter(1) + glyph(1) + gap(2) = 4; the trailing gutter is 1. A NEW
	// branch also reserves its checkbox plus a separating space on the node line.
	contWidth := fullW - 4 - 1
	firstWidth := contWidth
	var checkbox string
	if n.State == StateNew {
		checkbox = m.branchCheckbox(n, focused)
		firstWidth = fullW - 4 - lipgloss.Width(checkbox) - 1 - 1
	}
	if firstWidth < 4 {
		firstWidth = 4
	}
	if contWidth < 4 {
		contWidth = 4
	}

	parts := wrapName(n.Ref.Branch, firstWidth, contWidth)
	rows := make([]leftRow, 0, len(parts)+1)
	for li, part := range parts {
		glyph := bgIf(spineStyle, focused).Render("│")
		trailing := ""
		if li == 0 {
			glyph = m.branchCircle(n, focused)
			trailing = checkbox // "" for existing PRs
		}
		rows = append(rows, leftRow{
			text:     leftLine(glyph, nameStyle.Render(part), trailing, focused, fullW),
			branch:   idx,
			nodeLine: li == 0,
		})
	}
	if n.State != StateNew {
		rows = append(rows, leftRow{
			text:   leftLine(bgIf(spineStyle, focused).Render("│"), m.branchMetaLine(n, focused), "", focused, fullW),
			branch: idx,
		})
	}
	return rows
}

// leftLine assembles a full-width (fullW) timeline row: a 1-col gutter, the
// timeline glyph, a 2-col gap, the body, an optional right-aligned trailing
// element before a 1-col gutter, with the remainder filled. Pre-styled pieces
// already carry the focus shade; the gutters/fill add it via pad.
func leftLine(glyph, body, trailing string, focused bool, fullW int) string {
	lead := pad(1, focused) + glyph + pad(2, focused)
	if trailing != "" {
		gap := fullW - lipgloss.Width(lead) - lipgloss.Width(body) - lipgloss.Width(trailing) - 1
		if gap < 1 {
			gap = 1
		}
		return lead + body + pad(gap, focused) + trailing + pad(1, focused)
	}
	fill := fullW - lipgloss.Width(lead) - lipgloss.Width(body)
	if fill < 0 {
		fill = 0
	}
	return lead + body + pad(fill, focused)
}

// branchCircle renders the timeline node for a branch: a filled cyan circle when
// it will become a PR, a dotted gray ring when skipped, or a state-colored open
// ring for an existing PR.
func (m Model) branchCircle(n SubmitNode, focused bool) string {
	glyph, color := "○", n.State.Color()
	switch {
	case n.State == StateNew && n.Included:
		glyph, color = "●", lipgloss.Color("14") // filled cyan
	case n.State == StateNew:
		glyph, color = "◌", lipgloss.Color("245") // dotted ring: skipped
	}
	return bgIf(lipgloss.NewStyle().Foreground(color), focused).Render(glyph)
}

// branchNameStyle returns the full-name style: white and bold for a branch that
// will become a PR, muted gray for skipped or existing-PR branches.
func (m Model) branchNameStyle(n SubmitNode, focused bool) lipgloss.Style {
	st := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	if n.State == StateNew && n.Included {
		st = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	}
	return bgIf(st, focused)
}

// branchCheckbox renders a NEW branch's include checkbox: cyan [x] when included,
// gray [ ] when skipped.
func (m Model) branchCheckbox(n SubmitNode, focused bool) string {
	if n.Included {
		return bgIf(lipgloss.NewStyle().Foreground(lipgloss.Color("14")), focused).Render("[x]")
	}
	return bgIf(lipgloss.NewStyle().Foreground(lipgloss.Color("8")), focused).Render("[ ]")
}

// branchMetaLine renders an existing PR's "state · #num" line, the state word in
// its color and the separator/number in dim gray.
func (m Model) branchMetaLine(n SubmitNode, focused bool) string {
	state := bgIf(lipgloss.NewStyle().Foreground(n.State.Color()), focused).Render(strings.ToLower(n.State.Label()))
	if num := prNumber(n); num != 0 {
		return state + bgIf(stackInfoStyle, focused).Render(fmt.Sprintf(" · #%d", num))
	}
	return state
}

// bgIf returns s with the focused row shade applied when focused, else s.
func bgIf(s lipgloss.Style, focused bool) lipgloss.Style {
	if focused {
		return s.Background(rowShadeColor)
	}
	return s
}

// pad renders n spaces, shaded when focused, used to fill a focused row's
// background across the panel's content width.
func pad(n int, focused bool) string {
	if n <= 0 {
		return ""
	}
	return bgIf(lipgloss.NewStyle(), focused).Render(strings.Repeat(" ", n))
}

// leftVisibleHeight is the number of timeline rows the left panel can show.
func (m Model) leftVisibleHeight() int {
	h := m.contentHeight() - 2 // panel border
	if h < 1 {
		h = 1
	}
	return h
}

// scrollLeftToCursor adjusts leftScroll so the focused branch's rows are visible.
func (m *Model) scrollLeftToCursor() {
	leftW, _ := m.panelWidths()
	rows := m.buildLeftRows(leftW - 2)
	visH := m.leftVisibleHeight()
	first, lastRow := -1, -1
	for i, r := range rows {
		if r.branch == m.cursor {
			if first < 0 {
				first = i
			}
			lastRow = i
		}
	}
	if first < 0 {
		return
	}
	if m.leftScroll > first {
		m.leftScroll = first
	}
	if m.leftScroll < lastRow-visH+1 {
		m.leftScroll = lastRow - visH + 1
	}
	m.leftScroll = clampScroll(m.leftScroll, len(rows), visH)
}

// scrollLeft moves the left timeline's scroll offset by delta rows, clamped.
func (m *Model) scrollLeft(delta int) {
	leftW, _ := m.panelWidths()
	rows := m.buildLeftRows(leftW - 2)
	m.leftScroll = clampScroll(m.leftScroll+delta, len(rows), m.leftVisibleHeight())
}

// wrapName splits a branch name into lines, the first at most firstWidth runes
// (the node line, which also carries the right-edge checkbox) and the rest at
// most contWidth runes. The full name is always shown — long names wrap rather
// than truncate.
func wrapName(s string, firstWidth, contWidth int) []string {
	if firstWidth < 1 {
		firstWidth = 1
	}
	if contWidth < 1 {
		contWidth = 1
	}
	r := []rune(s)
	if len(r) == 0 {
		return []string{""}
	}
	var lines []string
	w := firstWidth
	for len(r) > w {
		lines = append(lines, string(r[:w]))
		r = r[w:]
		w = contWidth
	}
	return append(lines, string(r))
}

// renderRightPanel renders the editor panel for the focused branch in one of
// three modes: included editor, skipped placeholder, or locked read-only card.
func (m Model) renderRightPanel(width, height int) string {
	innerW := width - 4
	if innerW < 8 {
		innerW = 8
	}

	n := m.currentNode()
	var b strings.Builder

	switch {
	case n == nil:
		// Nothing focused.
	case n.State.Locked() || n.State.Blocks():
		b.WriteString(m.renderLockedCard(*n, innerW))
	case n.State == StateNew && !n.Included:
		b.WriteString(m.renderSkippedCard(*n, innerW))
	default:
		b.WriteString(m.renderIncludedEditor(*n, innerW))
	}

	return panelBox(b.String(), width, height)
}

// panelBox wraps content in a rounded panel of the given outer dimensions. Both
// panels use the same muted border; focus is conveyed by the highlighted input
// field inside, not the panel frame.
func panelBox(content string, width, height int) string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	return panelBorderStyle.Width(innerW).Height(innerH).MaxHeight(height).Render(content)
}

// isSpaceKey reports whether a key message represents the space bar.
func isSpaceKey(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeySpace {
		return true
	}
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' '
}

// prNumber returns the PR number associated with a node, preferring fresh
// details, or 0 if none.
func prNumber(n SubmitNode) int {
	if n.PR != nil && n.PR.Number != 0 {
		return n.PR.Number
	}
	if n.Ref.PullRequest != nil {
		return n.Ref.PullRequest.Number
	}
	return 0
}
