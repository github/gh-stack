package submitview

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.showHelp || m.confirmingQuit {
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			m.showHelp = false
		}
		return m, nil
	}

	switch msg.Action {
	case tea.MouseActionPress:
		leftW, _ := m.panelWidths()
		overLeft := msg.X >= 0 && msg.X < leftW
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if overLeft {
				m.scrollLeft(-leftScrollStep)
			} else if m.isDescScrollable() {
				m.scrollDesc(-descScrollStep)
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if overLeft {
				m.scrollLeft(leftScrollStep)
			} else if m.isDescScrollable() {
				m.scrollDesc(descScrollStep)
			}
			return m, nil
		case tea.MouseButtonLeft:
			return m.handleClick(msg.X, msg.Y)
		}
	}
	return m, nil
}

// leftScrollStep is the number of rows the left timeline scrolls per wheel notch.
const leftScrollStep = 2

// descScrollStep is the number of rows the description scrolls per wheel notch.
const descScrollStep = 3

// scrollDesc moves the description's scroll offset by delta rows, clamped to the
// scrollable range. In edit mode the first scroll pins the offset to the current
// cursor view for continuity; in preview mode the offset is absolute.
func (m *Model) scrollDesc(delta int) {
	_, rightW := m.panelWidths()
	innerW := rightW - 4
	// In the editor's edit mode the first scroll pins the offset to the cursor
	// view; the locked preview (and edit-preview) use an absolute offset.
	n := m.currentNode()
	editing := n != nil && n.State == StateNew && !m.descPreview
	if editing && !m.descScrollPinned {
		m.descScroll = cursorViewTop(m.descCursorRow(innerW), m.descAreaHeight())
		m.descScrollPinned = true
	}
	next := m.descScroll + delta
	if next < 0 {
		next = 0
	}
	if max := m.maxDescScroll(innerW); next > max {
		next = max
	}
	m.descScroll = next
}

// isDescScrollable reports whether the wheel should scroll the description: the
// focused description of an included NEW branch, or a locked PR's read-only
// description preview.
func (m Model) isDescScrollable() bool {
	n := m.currentNode()
	if n == nil {
		return false
	}
	if n.State == StateNew {
		return n.Included && m.focusedField == fieldDescription
	}
	return n.State.Locked()
}

// panelTopRow returns the screen row of the first inner line of the panels
// (past the header, the optional closed-PR banner, and the panel top border).
func (m Model) panelTopRow() int {
	row := m.headerHeight()
	if m.renderClosedBanner() != "" {
		row++
	}
	return row + 1
}

// branchRowAt maps screen coordinates to a left-panel branch index, or -1. A
// click anywhere on a branch's node, wrapped-name, or meta lines resolves to it.
func (m Model) branchRowAt(x, y int) int {
	leftW, _ := m.panelWidths()
	if x < 0 || x >= leftW {
		return -1
	}
	rows := m.buildLeftRows(leftW - 2)
	visH := m.leftVisibleHeight()
	scroll := clampScroll(m.leftScroll, len(rows), visH)
	vis := y - m.panelTopRow()
	if vis < 0 || vis >= visH {
		return -1
	}
	off := vis + scroll
	if off < 0 || off >= len(rows) {
		return -1
	}
	return rows[off].branch
}

// leftCheckboxHit reports whether (x,y) lands on a NEW branch's right-edge
// include checkbox in the left panel.
func (m Model) leftCheckboxHit(x, y int) bool {
	leftW, _ := m.panelWidths()
	rows := m.buildLeftRows(leftW - 2)
	visH := m.leftVisibleHeight()
	scroll := clampScroll(m.leftScroll, len(rows), visH)
	vis := y - m.panelTopRow()
	if vis < 0 || vis >= visH {
		return false
	}
	off := vis + scroll
	if off < 0 || off >= len(rows) {
		return false
	}
	r := rows[off]
	if r.branch < 0 || !r.nodeLine || m.nodes[r.branch].State != StateNew {
		return false
	}
	// The checkbox sits one cell in from the right border: cols [leftW-5, leftW-2).
	return x >= leftW-5 && x < leftW-2
}

// handleClick routes a left click to a branch row (left map) or an editor
// element (right panel).
func (m Model) handleClick(x, y int) (tea.Model, tea.Cmd) {
	m.statusMessage = ""
	m.statusIsError = false

	leftW, rightW := m.panelWidths()

	// Left panel: focus a branch, toggling include when its checkbox is clicked.
	if idx := m.branchRowAt(x, y); idx != -1 {
		onCheckbox := m.leftCheckboxHit(x, y) && m.nodes[idx].State == StateNew
		m.saveEditor()
		m.cursor = idx
		m.scrollLeftToCursor()
		m.loadEditor()
		if onCheckbox {
			excluding := m.nodes[idx].Included
			extra := m.applyIncludeCascade(idx)
			m.setCascadeStatus(excluding, extra)
			cmd := m.focusFirstField()
			return m, cmd
		}
		cmd := m.focusFirstField()
		return m, cmd
	}

	// Right panel.
	if x < leftW {
		return m, nil
	}
	n := m.currentNode()
	if n == nil {
		return m, nil
	}

	// Mode 3 (locked): the existing PR opens only from the PR number or the
	// "↗ Open on GitHub" button on the header row, not from clicking the card body.
	if n.State.Locked() || n.State.Blocks() {
		if lockedURL(*n) != "" && y-m.panelTopRow() == 0 {
			numStart, numEnd, btnStart, btnEnd := m.lockedHeaderTargets(*n)
			onNumber := numEnd > numStart && x >= numStart && x < numEnd
			onButton := btnEnd > btnStart && x >= btnStart && x < btnEnd
			if onNumber || onButton {
				m.openFocusedPR()
			}
		}
		return m, nil
	}

	rel := y - m.panelTopRow()
	titleLine, descLabel, descTop, descBot, draftLine := m.rightZones()
	rightEdge := leftW + 1 + rightW

	// Include chip (header row 0, right side) toggles inclusion.
	if rel == 0 {
		if x >= rightEdge-2-lipgloss.Width(m.renderIncludeChip(*n)) {
			cmd := m.toggleInclude()
			return m, cmd
		}
		return m, nil
	}

	// Mode 2 (skipped): the body is non-interactive (the footer shows an inert
	// "SKIPPED"); only the chip toggles.
	if n.State == StateNew && !n.Included {
		return m, nil
	}

	// Footer bottom-right action (NEXT BRANCH / SUBMIT) for an included branch.
	// The footer sits two rows below the CREATE AS line.
	if rel == draftLine+2 {
		if btn := m.footerRightButton(); btn != "" && x >= rightEdge-2-lipgloss.Width(btn) {
			if next := m.nextEditableIndex(); next != -1 {
				cmd := m.moveCursor(next - m.cursor)
				return m, cmd
			}
			return m.requestSubmit()
		}
		return m, nil
	}

	// Mode 1 (included editor).
	switch {
	case rel == descLabel:
		// The edit/preview sub-toggle is right-aligned on the DESCRIPTION line.
		if x >= rightEdge-2-lipgloss.Width(m.renderDescToggle()) {
			cmd := m.togglePreview()
			return m, cmd
		}
		return m, nil
	case rel >= titleLine-1 && rel <= titleLine+1:
		cmd := m.focusField(fieldTitle)
		return m, cmd
	case rel >= descTop && rel <= descBot:
		// Resolve the current top visible row, then map the clicked screen row to
		// an absolute row in the wrapped text.
		innerW := rightW - 4
		scroll := m.descScroll
		if !m.descScrollPinned {
			scroll = cursorViewTop(m.descCursorRow(innerW), m.descAreaHeight())
		}
		cmd := m.focusField(fieldDescription)
		if !m.descPreview {
			m.positionDescCursor(scroll+rel-(descTop+1), x-(leftW+5))
			// Keep the clicked view fixed so the cursor lands where it was clicked.
			m.descScroll = scroll
			m.descScrollPinned = true
		}
		return m, cmd
	case rel == draftLine:
		// Only clicks inside the [ Ready | Draft ] brackets change the value; the
		// "CREATE AS" label and the rest of the line are inert. The half the click
		// lands in selects that option (left = Ready, right = Draft).
		segStart, dividerX, segEnd := m.draftSegmentBounds()
		if x >= segStart && x < segEnd {
			_ = m.focusField(fieldDraft)
			n.Draft = x >= dividerX
		}
		return m, nil
	}
	return m, nil
}

// rightZones returns the right-panel line offsets (from panelTopRow) of the
// interactive editor elements for the included-NEW layout.
func (m Model) rightZones() (titleLine, descLabel, descTop, descBot, draftLine int) {
	descH := m.descAreaHeight()
	titleLine = 4 // title box content (header 0, rule 1, label 2, box 3..5)
	descLabel = 6
	descTop = 7 // description box top border
	descBot = 8 + descH
	draftLine = 9 + descH
	return
}

// positionDescCursor moves the description textarea's cursor to the clicked
// visual row and column. It resets to the top of the buffer and walks down, so
// it is exact when the text is not scrolled (the common case now that the box
// fills the panel) and approximate otherwise. CursorUp/CursorDown move by
// visual line, so soft-wrapped lines are handled correctly.
func (m *Model) positionDescCursor(visRow, col int) {
	if visRow < 0 {
		visRow = 0
	}
	if col < 0 {
		col = 0
	}
	for guard := 0; guard < 1000; guard++ {
		row, off := m.descArea.Line(), m.descArea.LineInfo().RowOffset
		m.descArea.CursorUp()
		if m.descArea.Line() == row && m.descArea.LineInfo().RowOffset == off {
			break // reached the top-left of the buffer
		}
	}
	for i := 0; i < visRow; i++ {
		m.descArea.CursorDown()
	}
	m.descArea.SetCursor(m.descArea.LineInfo().StartColumn + col)
}
