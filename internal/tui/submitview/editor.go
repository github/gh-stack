package submitview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
)

// currentNode returns a pointer to the focused node, or nil.
func (m Model) currentNode() *SubmitNode {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return nil
	}
	return &m.nodes[m.cursor]
}

// renderIncludedEditor renders Mode 1: the editor for an included NEW branch —
// the header (with the "Creating PR" chip), a separator rule, the title and
// description fields and ready/draft toggle, and the footer strip.
func (m Model) renderIncludedEditor(n SubmitNode, innerW int) string {
	var b strings.Builder
	b.WriteString(m.renderRightHeader(n, innerW))
	b.WriteString("\n")
	b.WriteString(rule(innerW))
	b.WriteString("\n")
	b.WriteString(m.renderEditBody(innerW))
	b.WriteString("\n")
	b.WriteString(rule(innerW))
	b.WriteString("\n")
	b.WriteString(m.renderRightFooter(n, innerW))
	return b.String()
}

// renderSkippedCard renders Mode 2: a NEW branch the user opted out of. The
// editor body is shown dimmed and non-interactive so it is clear what would be
// created, with the header chip reading "Skipped".
func (m Model) renderSkippedCard(n SubmitNode, innerW int) string {
	var b strings.Builder
	b.WriteString(m.renderRightHeader(n, innerW))
	b.WriteString("\n")
	b.WriteString(rule(innerW))
	b.WriteString("\n")
	b.WriteString(dimBodyStyle.Render(stripANSI(m.renderEditBody(innerW))))
	b.WriteString("\n")
	b.WriteString(rule(innerW))
	b.WriteString("\n")
	b.WriteString(m.renderRightFooter(n, innerW))
	return b.String()
}

// renderLockedCard renders Mode 3: a read-only card for a branch that already
// has a PR. It shows the PR title and a scrollable markdown preview of the
// description, with an "Open on GitHub" button in the top-right. A closed PR
// blocks the stack, so it shows a short callout instead.
func (m Model) renderLockedCard(n SubmitNode, innerW int) string {
	var b strings.Builder
	b.WriteString(composeLR(m.renderRightHeader(n, innerW), m.renderOpenButton(n), innerW))
	b.WriteString("\n")

	if n.State.Blocks() {
		b.WriteString("\n")
		b.WriteString(calloutErrorStyle.Render("  This branch has a closed pull request, which blocks the stack."))
		b.WriteString("\n")
		b.WriteString(hintStyle.Render("  Reopen it, or unstack and recreate to remove it."))
		return b.String()
	}

	b.WriteString(rule(innerW))
	b.WriteString("\n")
	b.WriteString(sectionLabelStyle.Render("TITLE"))
	b.WriteString("\n")
	b.WriteString(fieldBox(lockedTitleStyle.Render(truncateVisible(n.Title, innerW-4)), innerW, false))
	b.WriteString("\n")
	b.WriteString(sectionLabelStyle.Render("DESCRIPTION"))
	b.WriteString("\n")
	b.WriteString(descBox(m.lockedDescContent(innerW), innerW, false))
	return b.String()
}

// renderOpenButton renders the "↗ Open on GitHub  (^o)" button for a locked
// PR's card, or "" when there is no PR URL to open. "↗ Open on GitHub" is one
// underlined white link; "(^o)" is a dim, non-link keyboard hint.
func (m Model) renderOpenButton(n SubmitNode) string {
	if lockedURL(n) == "" {
		return ""
	}
	return openLinkStyle.Render("↗ Open on GitHub") + " " + hintStyle.Render("(^o)")
}

// lockedHeaderTargets returns the screen-x half-open ranges [start,end) of the
// two click targets on a locked card's header row: the existing-PR number and
// the "Open on GitHub" button. A zero-width range (start == end) means that
// target is absent. handleClick and tests share this so the click regions stay
// in sync with what renderLockedCard draws.
func (m Model) lockedHeaderTargets(n SubmitNode) (numStart, numEnd, btnStart, btnEnd int) {
	leftW, rightW := m.panelWidths()
	contentLeft := leftW + 3                   // left gap + panel border + panel padding
	contentRight := contentLeft + (rightW - 4) // exclusive: panel content right edge

	if num := prNumber(n); num != 0 {
		lead := headerBranchStyle.Render(n.Ref.Branch) + "  " + RenderBadge(n.State)
		numStart = contentLeft + lipgloss.Width(lead)
		numEnd = numStart + lipgloss.Width(fmt.Sprintf("  #%d", num))
	}
	if btn := m.renderOpenButton(n); btn != "" {
		btnEnd = contentRight
		btnStart = contentRight - lipgloss.Width(btn)
	}
	return numStart, numEnd, btnStart, btnEnd
}

// lockedDescContent renders the focused locked branch's description as a
// scrollable markdown preview with a scrollbar, matching the editor's preview.
func (m Model) lockedDescContent(innerW int) string {
	textWidth := descTextWidth(innerW)
	height := m.lockedDescHeight()
	lines := m.descPreviewLines(innerW)
	scroll := clampScroll(m.descScroll, len(lines), height)
	rows := clipScrollRows(lines, scroll, height)
	return addScrollbar(rows, scroll, len(lines), height, textWidth)
}

// renderRightHeader renders the right-panel heading: the focused branch name
// (white), its state badge, and — for a NEW branch — a "CREATE PR" switch on the
// right (on when the branch will become a PR, off when skipped).
func (m Model) renderRightHeader(n SubmitNode, innerW int) string {
	left := headerBranchStyle.Render(n.Ref.Branch) + "  " + RenderBadge(n.State)
	if n.State != StateNew {
		if num := prNumber(n); num != 0 {
			left += "  " + prNumberStyle.Render(fmt.Sprintf("#%d", num))
		}
		return left
	}
	return composeLR(left, m.renderIncludeChip(n), innerW)
}

// renderIncludeChip renders the "CREATE PR" toggle: the label in the shared
// section-heading style, a dim Ctrl+X hint, then the two-state switch.
func (m Model) renderIncludeChip(n SubmitNode) string {
	return sectionLabelStyle.Render("CREATE PR") + " " + hintStyle.Render("(^x)") + " " + renderSwitch(n.Included)
}

// renderSwitch draws a two-state pill toggle with a square knob that slides
// between the ends, kept one cell off each border so it never touches the edge:
// "   ■ " (green track, black knob right) when on, " ■   " (light track, dark
// knob left) when off.
func renderSwitch(on bool) string {
	if on {
		return switchOnStyle.Render("   ") + switchOnStyle.Foreground(switchOnKnob).Render("■") + switchOnStyle.Render(" ")
	}
	return switchOffStyle.Render(" ") + switchOffStyle.Foreground(switchOffKnob).Render("■") + switchOffStyle.Render("   ")
}

// renderEditBody renders the editable fields in web-create-PR order: the title
// input, the description input with an edit/preview sub-toggle, and the
// ready/draft segmented toggle. The skip and editor shortcuts are shown inline
// (the CREATE PR switch and the CREATE AS row), not in the footer.
func (m Model) renderEditBody(innerW int) string {
	var b strings.Builder

	// TITLE
	b.WriteString(sectionLabelFor("TITLE", m.focusedField == fieldTitle))
	b.WriteString("\n")
	b.WriteString(fieldBox(m.titleContent(), innerW, m.focusedField == fieldTitle))
	b.WriteString("\n")

	// DESCRIPTION with edit/preview sub-toggle on the right.
	b.WriteString(composeLR(sectionLabelFor("DESCRIPTION", m.focusedField == fieldDescription), m.renderDescToggle(), innerW))
	b.WriteString("\n")
	b.WriteString(descBox(m.descContent(innerW), innerW, m.focusedField == fieldDescription))
	b.WriteString("\n")

	// CREATE AS segmented toggle (with the Open in Editor hint on the right).
	b.WriteString(m.renderDraftToggle(innerW))

	return b.String()
}

// rule renders a full-width horizontal divider.
func rule(width int) string {
	if width < 1 {
		width = 1
	}
	return ruleStyle.Render(strings.Repeat("─", width))
}

// renderRightFooter renders the thin footer strip at the bottom of the right
// panel: the PR progress on the left and a bottom-right action — "NEXT BRANCH"
// or, on the last PR, "SUBMIT N PRs" — right-aligned.
func (m Model) renderRightFooter(n SubmitNode, innerW int) string {
	return composeLR(m.renderProgress(n), m.footerRightButton(), innerW)
}

// footerRightButton renders the bottom-right footer action: "NEXT BRANCH" (white)
// when another PR remains up the stack, or a prominent "SUBMIT N PRs" button on
// the last PR. A skipped branch shows an inert gray "SKIPPED" instead. The
// keyboard hint sits to the LEFT so the label/button stays anchored to the right
// edge. The "(tab)" hint shows only while the CREATE AS row is focused — the
// point at which Tab actually advances to the next branch. The NEXT BRANCH /
// SUBMIT actions are clickable (handleClick); empty when there is nothing to do.
func (m Model) footerRightButton() string {
	if n := m.currentNode(); n != nil && n.State == StateNew && !n.Included {
		return sectionLabelStyle.Render("SKIPPED")
	}
	if m.nextEditableIndex() != -1 {
		label := nextBranchStyle.Render("NEXT BRANCH")
		if m.focusedField == fieldDraft {
			return hintStyle.Render("(tab) ") + label
		}
		return label
	}
	if _, total := m.prProgress(); total > 0 {
		noun := "PRs"
		if total == 1 {
			noun = "PR"
		}
		return hintStyle.Render("(^s) ") + submitButtonStyle.Render(fmt.Sprintf("SUBMIT %d %s", total, noun))
	}
	return ""
}

// renderProgress renders the "●○ PR k of m" progress affordance for the new-PR
// set. When the focused branch is not an included NEW branch, only the count is
// shown.
func (m Model) renderProgress(n SubmitNode) string {
	pos, total := m.prProgress()
	if total == 0 {
		return ""
	}
	var dots strings.Builder
	for i := 1; i <= total; i++ {
		if i == pos {
			dots.WriteString(footerKeyStyle.Render("●"))
		} else {
			dots.WriteString(hintStyle.Render("○"))
		}
	}
	if pos == 0 {
		word := "PRs"
		if total == 1 {
			word = "PR"
		}
		return dots.String() + hintStyle.Render(fmt.Sprintf(" %d %s", total, word))
	}
	return dots.String() + hintStyle.Render(fmt.Sprintf(" PR %d of %d", pos, total))
}

// scrollbarReserve is the number of columns reserved on the right of the
// description box for a 1-column margin plus the 1-column scrollbar.
const scrollbarReserve = 2

// descTextWidth returns the wrap width for the description text. The description
// box uses left-only padding (descBox) so its content area is innerW-3; the
// scrollbar and its margin take scrollbarReserve more.
func descTextWidth(innerW int) int {
	w := innerW - 3 - scrollbarReserve
	if w < 10 {
		w = 10
	}
	return w
}

// descBox wraps the description content in a rounded box with left-only padding,
// so the scrollbar (the content's last column) sits flush against the right
// border with no extra margin.
func descBox(content string, width int, focused bool) string {
	bc := lipgloss.Color("8")
	if focused {
		bc = lipgloss.Color("14")
	}
	w := width - 2
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bc).
		Padding(0, 0, 0, 1).
		Width(w).
		Render(content)
}

// descContent returns the description body to display: the rendered markdown
// preview or the editable text, both shown through a scrollable viewport with a
// scrollbar on the right.
func (m Model) descContent(innerW int) string {
	textWidth := descTextWidth(innerW)
	height := m.descAreaHeight()

	if m.descPreview {
		lines := m.descPreviewLines(innerW)
		scroll := clampScroll(m.descScroll, len(lines), height)
		rows := clipScrollRows(lines, scroll, height)
		return addScrollbar(rows, scroll, len(lines), height, textWidth)
	}

	lines := m.descFullLines(innerW)
	cursorRow := m.descCursorRow(innerW)

	// When not free-scrolling, the viewport follows the cursor; otherwise the
	// user's absolute scroll offset is used.
	scroll := m.descScroll
	if !m.descScrollPinned {
		scroll = cursorViewTop(cursorRow, height)
	}
	scroll = clampScroll(scroll, len(lines), height)

	// Overlay the block cursor when the description is focused and the cursor is
	// within the visible window.
	if m.descArea.Focused() && cursorRow >= scroll && cursorRow < scroll+height && cursorRow < len(lines) {
		lines = overlayCursor(lines, cursorRow, m.descArea.LineInfo().ColumnOffset)
	}
	rows := clipScrollRows(lines, scroll, height)
	return addScrollbar(rows, scroll, len(lines), height, textWidth)
}

// titleContent renders the editable title as soft-wrapped visual rows with the
// block cursor overlaid, mirroring descContent but without a scrollbar. The
// title is a single logical line, so its cursor row is the wrapped row offset.
func (m Model) titleContent() string {
	width := m.titleTextWidth()
	h := m.titleAreaHeight()
	lines := wrapDescLines(m.titleArea.Value(), width)
	cursorRow := m.titleArea.LineInfo().RowOffset
	scroll := clampScroll(cursorViewTop(cursorRow, h), len(lines), h)
	if m.focusedField == fieldTitle && m.titleArea.Focused() &&
		cursorRow >= scroll && cursorRow < scroll+h && cursorRow < len(lines) {
		lines = overlayCursor(lines, cursorRow, m.titleArea.LineInfo().ColumnOffset)
	}
	rows := clipScrollRows(lines, scroll, h)
	for len(rows) < h {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

// clampScroll bounds a scroll offset to [0, total-height].
func clampScroll(scroll, total, height int) int {
	if max := total - height; scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

// descPreviewLines renders the description markdown to visual rows for the
// scrollable preview.
func (m Model) descPreviewLines(innerW int) []string {
	desc := ""
	if n := m.currentNode(); n != nil {
		desc = n.Description
	}
	return strings.Split(renderMarkdown(desc, descTextWidth(innerW)), "\n")
}

// cursorViewTop returns the scroll offset that keeps the cursor's row visible at
// the bottom of the viewport once the text grows past it (a normal editor feel).
func cursorViewTop(cursorRow, height int) int {
	if cursorRow >= height {
		return cursorRow - height + 1
	}
	return 0
}

// descFullLines renders the whole description, wrapped exactly like the editing
// textarea (same width, same component) but with no cursor, as visual rows. It
// reads the live textarea value so it always matches descCursorRow.
func (m Model) descFullLines(innerW int) []string {
	return wrapDescLines(m.descArea.Value(), descTextWidth(innerW))
}

// descCursorRow returns the cursor's absolute visual row in the wrapped
// description, accounting for soft-wrapped lines above it.
func (m Model) descCursorRow(innerW int) int {
	li := m.descArea.LineInfo()
	bufRow := m.descArea.Line()
	if bufRow <= 0 {
		return li.RowOffset
	}
	bufLines := strings.Split(m.descArea.Value(), "\n")
	if bufRow > len(bufLines) {
		bufRow = len(bufLines)
	}
	// A zero-width sentinel keeps a trailing empty line from being trimmed away,
	// so the rows above the cursor are counted correctly.
	before := strings.Join(bufLines[:bufRow], "\n") + "\u200b"
	return len(wrapDescLines(before, descTextWidth(innerW))) + li.RowOffset
}

// clipScrollRows returns height rows of lines starting at offset, padding with
// blank rows when the offset runs past the end.
func clipScrollRows(lines []string, offset, height int) []string {
	out := make([]string, 0, height)
	for i := offset; i < offset+height; i++ {
		if i >= 0 && i < len(lines) {
			out = append(out, lines[i])
		} else {
			out = append(out, "")
		}
	}
	return out
}

// addScrollbar appends a 1-column margin and a vertical scrollbar to each row.
// The thumb size and position reflect the visible window over the total rows;
// when everything fits, only blank margin is added so the layout is stable.
func addScrollbar(rows []string, scroll, total, height, textWidth int) string {
	start, size := scrollbarThumb(scroll, total, height)
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		if gap := textWidth - lipgloss.Width(row); gap > 0 {
			row += strings.Repeat(" ", gap)
		}
		b.WriteString(row)
		b.WriteByte(' ') // margin between text and bar
		switch {
		case total <= height:
			b.WriteByte(' ') // content fits: no bar
		case i >= start && i < start+size:
			b.WriteString(scrollThumbStyle.Render("┃"))
		default:
			b.WriteString(scrollTrackStyle.Render("│"))
		}
	}
	return b.String()
}

// scrollbarThumb returns the thumb's start row and size for a viewport of the
// given height over total rows scrolled to offset.
func scrollbarThumb(scroll, total, height int) (start, size int) {
	if total <= height || height <= 0 {
		return 0, 0
	}
	size = height * height / total
	if size < 1 {
		size = 1
	}
	if size > height {
		size = height
	}
	if denom := total - height; denom > 0 {
		start = scroll * (height - size) / denom
	}
	if start < 0 {
		start = 0
	}
	if start > height-size {
		start = height - size
	}
	return start, size
}

// wrapDescLines wraps text to width using a throwaway textarea so the wrapping
// matches the editor exactly, returning the visual rows as plain text (ANSI
// stripped) with trailing blank rows trimmed. Stripping ANSI is essential: in a
// real terminal the textarea pads blank rows with styled spaces, which would
// otherwise defeat the trailing-blank trim (inflating the scroll range) and the
// cursor-overlay column math.
func wrapDescLines(text string, width int) []string {
	if width < 10 {
		width = 10
	}
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.MaxHeight = 0
	ta.SetWidth(width)
	ta.SetHeight(strings.Count(text, "\n") + 1 + len([]rune(text))/width + 5)
	ta.SetValue(text)
	lines := strings.Split(ta.View(), "\n")
	for i := range lines {
		lines[i] = stripANSI(lines[i])
	}
	for len(lines) > 1 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// overlayCursor renders a block cursor at (row, col) within the given lines.
func overlayCursor(lines []string, row, col int) []string {
	if row < 0 || row >= len(lines) || col < 0 {
		return lines
	}
	r := []rune(lines[row])
	for len(r) <= col {
		r = append(r, ' ')
	}
	lines[row] = string(r[:col]) + descCursorStyle.Render(string(r[col])) + string(r[col+1:])
	return lines
}

// renderDescToggle renders the inline edit/preview sub-toggle with a dim Ctrl+P
// hint to its right.
func (m Model) renderDescToggle() string {
	var toggle string
	if m.descPreview {
		toggle = tabInactiveStyle.Render("edit") + stackInfoStyle.Render(" · ") + tabActiveStyle.Render("preview")
	} else {
		toggle = tabActiveStyle.Render("edit") + stackInfoStyle.Render(" · ") + tabInactiveStyle.Render("preview")
	}
	return toggle + " " + hintStyle.Render("(^p)")
}

// renderDraftToggle renders the ready-for-review ↔ draft choice as a single
// segmented control "[ Ready | Draft ]" under a "CREATE AS" label, with the
// "Open in Editor (^e)" hint right-aligned on the same line. The selected
// segment is filled green; the other is dim. New PRs default to ready for review.
func (m Model) renderDraftToggle(innerW int) string {
	n := m.currentNode()
	draft := n != nil && n.Draft

	ready := segOffStyle.Render("Ready")
	draftOpt := segOffStyle.Render("Draft")
	if draft {
		draftOpt = segOnStyle.Render("Draft")
	} else {
		ready = segOnStyle.Render("Ready")
	}
	seg := segFrameStyle.Render("[") + ready + segFrameStyle.Render("|") + draftOpt + segFrameStyle.Render("]")

	left := sectionLabelFor("CREATE AS", m.focusedField == fieldDraft) + "  " + seg
	return composeLR(left, hintStyle.Render("open in $EDITOR (^e)"), innerW)
}

// sectionLabelFor renders a field's section label, turning it cyan when the
// field is focused (matching the focused branch name and CREATE AS treatment).
func sectionLabelFor(text string, focused bool) string {
	if focused {
		return focusNameStyle.Render(text)
	}
	return sectionLabelStyle.Render(text)
}

// draftSegmentBounds returns the screen-x coordinates of the CREATE AS segmented
// control on the draft line: segStart (the "[" column), dividerX (the "|"
// column), and segEnd (exclusive, just past "]"). handleClick uses these so only
// clicks inside the brackets change the value — clicks on the "CREATE AS" label or
// elsewhere on the line are ignored — and the half a click lands in selects that
// option (left of the divider = Ready, right = Draft).
func (m Model) draftSegmentBounds() (segStart, dividerX, segEnd int) {
	leftW, _ := m.panelWidths()
	contentLeft := leftW + 3                                 // left gap + panel border + panel padding
	segStart = contentLeft + lipgloss.Width("CREATE AS") + 2 // label + two-space gap
	dividerX = segStart + lipgloss.Width("[") + lipgloss.Width(segOffStyle.Render("Ready"))
	segEnd = dividerX + lipgloss.Width("|") + lipgloss.Width(segOffStyle.Render("Draft")) + lipgloss.Width("]")
	return segStart, dividerX, segEnd
}

// fieldBox wraps a field's content in a rounded box whose border highlights when
// focused. width is the desired outer width.
func fieldBox(content string, width int, focused bool) string {
	bc := lipgloss.Color("8")
	if focused {
		bc = lipgloss.Color("14")
	}
	w := width - 2
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bc).
		Padding(0, 1).
		Width(w).
		Render(content)
}

// lockedURL returns the web URL for a locked branch's PR, or "".
func lockedURL(n SubmitNode) string {
	if n.PR != nil && n.PR.URL != "" {
		return n.PR.URL
	}
	if n.Ref.PullRequest != nil {
		return n.Ref.PullRequest.URL
	}
	return ""
}
