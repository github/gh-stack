package submitview

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFieldCycling_IncludedBranch(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, fieldTitle, m.focusedField)

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, fieldDescription, m.focusedField)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, fieldDraft, m.focusedField)

	// shift+tab back up through the fields to the title.
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	assert.Equal(t, fieldDescription, m.focusedField)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	assert.Equal(t, fieldTitle, m.focusedField)
}

func TestFieldCycling_TabFromDraftMovesToNextPR(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, 1, m.cursor)                   // bottom-most NEW (PR 1)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // desc
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // draft
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // next PR (up the stack)
	assert.Equal(t, 0, m.cursor)
	assert.Equal(t, fieldTitle, m.focusedField)
}

func TestTypingEditsTitle(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, runeKey('!'))
	assert.Contains(t, m.nodes[1].Title, "!")
}

func TestDraftToggle(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // desc
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // draft
	require.Equal(t, fieldDraft, m.focusedField)
	before := m.nodes[1].Draft
	m = sendKey(t, m, runeKey(' '))
	assert.Equal(t, !before, m.nodes[1].Draft)
}

func TestDraftToggle_DefaultsToReady(t *testing.T) {
	m := testModel(t, newNodes())
	assert.False(t, m.nodes[1].Draft, "new PRs default to ready for review, not draft")
}

func TestDraftToggle_ArrowKeysSelectOption(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // desc
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // draft
	require.Equal(t, fieldDraft, m.focusedField)

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRight})
	assert.True(t, m.nodes[1].Draft, "→ selects draft")
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRight})
	assert.True(t, m.nodes[1].Draft, "→ is idempotent")
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	assert.False(t, m.nodes[1].Draft, "← selects ready for review")
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	assert.False(t, m.nodes[1].Draft, "← is idempotent")
}

func TestDraftToggle_RendersBothOptions(t *testing.T) {
	m := testModel(t, newNodes())
	out := m.View()
	assert.Contains(t, out, "Ready")
	assert.Contains(t, out, "Draft")
	assert.Contains(t, out, "CREATE AS")
	assert.NotContains(t, out, "OPEN AS", "the label was renamed to CREATE AS")
	assert.NotContains(t, out, "Open as draft", "the old checkbox label is gone")
	assert.NotContains(t, out, "submitted as ready by default")
}

func TestRightPanel_InlineShortcutHints(t *testing.T) {
	m := testModel(t, newNodes())
	out := m.View()
	assert.Contains(t, out, "CREATE PR (^x)", "the include switch shows its Ctrl+X hint")
	assert.Contains(t, out, "preview (^p)", "the description toggle shows its Ctrl+P hint")
	assert.Contains(t, out, "open in $EDITOR (^e)", "the CREATE AS row shows the editor shortcut")
}

func TestRightFooter_NextBranchAndSubmit(t *testing.T) {
	m := testModel(t, newNodes())
	_, rightW := m.panelWidths()
	innerW := rightW - 4

	// Bottom-most NEW (cursor starts here, title focused) has a PR up the stack.
	require.NotEqual(t, -1, m.nextEditableIndex())
	require.NotEqual(t, fieldDraft, m.focusedField)
	footer := m.renderRightFooter(m.nodes[m.cursor], innerW)
	assert.Contains(t, footer, "NEXT BRANCH")
	assert.NotContains(t, footer, "(tab)", "the tab hint is hidden unless the CREATE AS row is focused")

	// Focus the CREATE AS row: the tab hint appears (to the left of NEXT BRANCH).
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // description
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // draft (CREATE AS)
	require.Equal(t, fieldDraft, m.focusedField)
	footer = m.renderRightFooter(m.nodes[m.cursor], innerW)
	assert.Contains(t, footer, "(tab) ", "the tab hint shows on the CREATE AS row")
	assert.True(t, strings.Index(footer, "(tab)") < strings.Index(footer, "NEXT BRANCH"), "the tab hint sits to the left")

	// Move up to the top-most NEW: it is the last PR, so the footer offers submit.
	for m.nextEditableIndex() != -1 {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	footer = m.renderRightFooter(m.nodes[m.cursor], innerW)
	assert.Contains(t, footer, "SUBMIT 2 PRs")
	assert.Contains(t, footer, "(^s)")
	assert.True(t, strings.Index(footer, "(^s)") < strings.Index(footer, "SUBMIT"), "the submit hint sits to the left")
}

func TestRightFooter_SkippedShowsInertLabel(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip the focused branch
	require.False(t, m.nodes[m.cursor].Included)
	_, rightW := m.panelWidths()
	footer := m.renderRightFooter(m.nodes[m.cursor], rightW-4)
	assert.Contains(t, footer, "SKIPPED", "a skipped branch shows SKIPPED")
	assert.NotContains(t, footer, "SUBMIT", "no submit button on a skipped branch")
	assert.NotContains(t, footer, "NEXT BRANCH", "no next-branch label on a skipped branch")

	// Clicking the SKIPPED label does nothing.
	before := m.cursor
	_, _, _, _, draftLine := m.rightZones()
	leftW, rW := m.panelWidths()
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: leftW + 1 + rW - 4, Y: m.panelTopRow() + draftLine + 2})
	m = updated.(Model)
	assert.Equal(t, before, m.cursor, "clicking SKIPPED is inert")
	assert.False(t, m.submitRequested)
}

func TestMouse_ClickNextBranchAdvances(t *testing.T) {
	m := testModel(t, newNodes())
	start := m.cursor
	next := m.nextEditableIndex()
	require.NotEqual(t, -1, next)
	_, _, _, _, draftLine := m.rightZones()
	leftW, rightW := m.panelWidths()
	y := m.panelTopRow() + draftLine + 2 // footer row
	x := leftW + 1 + rightW - 4          // within the right-aligned button
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = updated.(Model)
	assert.Equal(t, next, m.cursor, "clicking NEXT BRANCH advances to the next PR")
	assert.NotEqual(t, start, m.cursor)
}

func TestMouse_ClickSubmitRequestsSubmit(t *testing.T) {
	m := testModel(t, newNodes())
	for m.nextEditableIndex() != -1 { // move to the last PR
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	_, _, _, _, draftLine := m.rightZones()
	leftW, rightW := m.panelWidths()
	y := m.panelTopRow() + draftLine + 2
	x := leftW + 1 + rightW - 4
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = updated.(Model)
	assert.True(t, m.submitRequested, "clicking SUBMIT requests the batch submit")
}

// --- dependency cascade ---

func TestCascade_SkippingCascadesUp(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, 1, m.cursor) // bottom-most NEW

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip it
	assert.False(t, m.nodes[1].Included, "the toggled branch is skipped")
	assert.False(t, m.nodes[0].Included, "the branch stacked above it is skipped too")
	assert.Contains(t, m.statusMessage, "Also skipped")
}

func TestCascade_IncludingCascadesDown(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, 1, m.cursor)
	// Skip branch 1, which cascades up to also skip branch 0.
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	require.False(t, m.nodes[0].Included)
	require.False(t, m.nodes[1].Included)

	// Re-include branch 0 (top); the branch below it that it depends on is
	// re-included too.
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, 0, m.cursor)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	assert.True(t, m.nodes[0].Included)
	assert.True(t, m.nodes[1].Included, "the dependency below is re-included")
	assert.Contains(t, m.statusMessage, "Also included")
}

func TestCascade_TopBranchToggleDoesNotCascade(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp}) // top branch
	require.Equal(t, 0, m.cursor)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	assert.False(t, m.nodes[0].Included)
	assert.True(t, m.nodes[1].Included, "branches below the top are unaffected")
	assert.Empty(t, m.statusMessage, "no cascade hint when nothing else changed")
}

func TestPerBranchPersistence(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, 1, m.cursor)
	m = sendKey(t, m, runeKey('X'))
	edited := m.nodes[1].Title
	require.Contains(t, edited, "X")

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp}) // branch 0
	require.Equal(t, 0, m.cursor)
	assert.NotEqual(t, edited, m.titleInput.Value())

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown}) // back to 1
	assert.Equal(t, edited, m.titleInput.Value())
}

func TestSkippedBranch_ShowsDimmedBody(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip the focused branch
	out := m.View()
	assert.Contains(t, out, "CREATE PR", "the header switch reads CREATE PR")
	assert.Contains(t, out, "include", "the footer offers to re-include")
	assert.NotContains(t, out, "Create a pull request", "the old checkbox is gone")
}

func TestSkippedBranch_SpaceReincludes(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip
	require.False(t, m.nodes[1].Included)

	m = sendKey(t, m, runeKey(' ')) // re-include the skipped branch
	assert.True(t, m.nodes[1].Included)
}

func TestIncludeSwitch_ReflectsState(t *testing.T) {
	on := []rune(stripANSI(renderSwitch(true)))
	off := []rune(stripANSI(renderSwitch(false)))
	knobIdx := func(rs []rune) int {
		for i, r := range rs {
			if r == '■' {
				return i
			}
		}
		return -1
	}
	onIdx, offIdx := knobIdx(on), knobIdx(off)
	require.GreaterOrEqual(t, onIdx, 0, "on switch must show a square knob")
	require.GreaterOrEqual(t, offIdx, 0, "off switch must show a square knob")
	assert.Greater(t, onIdx, offIdx, "the knob sits further right when on than off")
	// The knob is inset one cell from each border in both states.
	assert.Greater(t, onIdx, 0, "knob has left padding when on")
	assert.Less(t, onIdx, len(on)-1, "knob has right padding when on")
	assert.Greater(t, offIdx, 0, "knob has left padding when off")
	assert.Less(t, offIdx, len(off)-1, "knob has right padding when off")
}

func TestLockedBranch_ShowsReadOnlyCard(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown}) // locked (draft PR)
	require.True(t, m.nodes[m.cursor].State.Locked())
	out := m.View()
	assert.NotContains(t, out, "already has a pull request")
	assert.NotContains(t, out, "pushed as part of this submit")
	assert.Contains(t, out, "TITLE")
	assert.Contains(t, out, "DESCRIPTION")
	assert.Contains(t, out, "↗ Open on GitHub")
	assert.Contains(t, out, "^o")
}

func TestLockedBranch_CtrlOOpensPR(t *testing.T) {
	m := testModel(t, newNodes())
	// Capture the open instead of launching a real browser.
	var opened string
	m.openURL = func(url string) { opened = url }
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	require.True(t, m.nodes[m.cursor].State.Locked())
	// ^o opens the focused branch's existing PR and must not error or quit.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	assert.False(t, m.cancelled)
	assert.Nil(t, cmd)
	assert.Equal(t, m.nodes[m.cursor].PR.URL, opened, "^o opens the focused PR's URL")
}

func TestLockedBranch_DescriptionScrolls(t *testing.T) {
	open := newNode("feat/open", StateOpen)
	open.Description = "# Heading\n\n" + strings.Repeat("- a list item\n", 200)
	m := testModel(t, []SubmitNode{newNode("feat/new", StateNew), open})
	for m.nodes[m.cursor].State == StateNew {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.True(t, m.nodes[m.cursor].State.Locked())
	assert.True(t, m.isDescScrollable(), "a locked PR's description should be scrollable")

	m.scrollDesc(descScrollStep)
	assert.Equal(t, descScrollStep, m.descScroll, "wheel-down advances the locked description")
	m.scrollDesc(-descScrollStep * 2)
	assert.Equal(t, 0, m.descScroll, "scrolling is clamped at the top")
}

// navigateToLocked moves the cursor down to the first locked branch.
func navigateToLocked(t *testing.T, m Model) Model {
	t.Helper()
	for !m.nodes[m.cursor].State.Locked() {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	return m
}

func TestLockedBranch_ClickOpenButtonOpensPR(t *testing.T) {
	m := navigateToLocked(t, testModel(t, newNodes()))
	n := m.currentNode()
	var opened string
	m.openURL = func(url string) { opened = url }
	_, _, btnStart, btnEnd := m.lockedHeaderTargets(*n)
	require.Greater(t, btnEnd, btnStart, "the Open on GitHub button needs a click range")

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: btnStart, Y: m.panelTopRow()})
	m = updated.(Model)
	assert.Equal(t, n.PR.URL, opened, "clicking the Open on GitHub button opens the PR")
}

func TestLockedBranch_ClickPRNumberOpensPR(t *testing.T) {
	m := navigateToLocked(t, testModel(t, newNodes()))
	n := m.currentNode()
	var opened string
	m.openURL = func(url string) { opened = url }
	numStart, numEnd, _, _ := m.lockedHeaderTargets(*n)
	require.Greater(t, numEnd, numStart, "the PR number needs a click range")

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: (numStart + numEnd) / 2, Y: m.panelTopRow()})
	m = updated.(Model)
	assert.Equal(t, n.PR.URL, opened, "clicking the PR number opens the PR")
}

func TestLockedBranch_ClickCardBodyDoesNotOpen(t *testing.T) {
	m := navigateToLocked(t, testModel(t, newNodes()))
	n := m.currentNode()
	opened := ""
	m.openURL = func(url string) { opened = url }
	numStart, numEnd, btnStart, btnEnd := m.lockedHeaderTargets(*n)
	require.Greater(t, numEnd, numStart)
	require.Greater(t, btnEnd, btnStart)
	require.Less(t, numEnd, btnStart, "the number and button should not be adjacent")
	leftW, _ := m.panelWidths()

	// A click on the header row between the two targets does nothing.
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: (numEnd + btnStart) / 2, Y: m.panelTopRow()})
	m = updated.(Model)
	assert.Empty(t, opened, "clicking between the targets must not open the PR")

	// A click deeper in the card body (description area) does nothing either.
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: leftW + 5, Y: m.panelTopRow() + 6})
	m = updated.(Model)
	assert.Empty(t, opened, "clicking the card body must not open the PR")
}

func TestEsc_QuitsFromAnyField(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // description focused
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)
	assert.True(t, m.cancelled)
	assert.NotNil(t, cmd)
}

// --- mouse on the editor ---

func TestMouse_ClickIncludeChip(t *testing.T) {
	m := testModel(t, newNodes())
	leftW, rightW := m.panelWidths()
	y := m.panelTopRow()        // header row 0
	x := leftW + 1 + rightW - 5 // the chip is right-aligned in the header
	require.True(t, m.nodes[m.cursor].Included)
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = updated.(Model)
	assert.False(t, m.nodes[m.cursor].Included, "clicking the chip skips the branch")
}

func TestMouse_CheckboxClickCascadesUp(t *testing.T) {
	m := testModel(t, newNodes())
	// Click the include checkbox on branch index 1 (its right-edge box).
	y, cbX := leftBranchNode(m, 1)
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: cbX, Y: y})
	m = updated.(Model)
	require.Equal(t, 1, m.cursor)
	assert.False(t, m.nodes[1].Included)
	assert.False(t, m.nodes[0].Included, "clicking a checkbox cascades like ^x")
	assert.Contains(t, m.statusMessage, "Also skipped")
}

func TestLeftPanel_TimelineNoLegend(t *testing.T) {
	m := testModel(t, newNodes())
	out := m.View()
	assert.NotContains(t, out, "LEGEND", "the legend was removed")
	assert.Contains(t, out, "feat/auth/tests", "the full branch name (incl prefix) is shown")
	assert.Contains(t, out, "· #1240", "existing PRs show their number on the right")
	assert.Contains(t, out, "└─ main", "the trunk anchors the timeline")
}

func TestLeftPanel_SkippedUsesDottedRing(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip the focused branch
	out := m.View()
	assert.Contains(t, out, "◌", "skipped branches use a dotted ring")
	assert.Contains(t, out, "[ ]", "skipped branches show an empty checkbox")
}

func TestMouse_ClickWrappedNameFocuses(t *testing.T) {
	long := newNode("feat/really/long/branch/name/that/wraps", StateNew)
	m := testModel(t, []SubmitNode{long, newNode("feat/x", StateNew)})
	require.Equal(t, 1, m.cursor) // starts on the bottom-most NEW
	leftW, _ := m.panelWidths()
	rows := m.buildLeftRows(leftW - 4)
	contY := -1
	for i, r := range rows {
		if r.branch == 0 && !r.nodeLine {
			contY = m.panelTopRow() + i
			break
		}
	}
	require.GreaterOrEqual(t, contY, 0, "the long branch name should wrap")
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 6, Y: contY})
	m = updated.(Model)
	assert.Equal(t, 0, m.cursor, "clicking a wrapped name line focuses that branch")
}

func TestLeftPanel_ExistingPRMetaOnSecondLine(t *testing.T) {
	m := testModel(t, newNodes())
	leftW, _ := m.panelWidths()
	rows := m.buildLeftRows(leftW - 2)
	bi := -1
	for i, n := range m.nodes {
		if n.State != StateNew {
			bi = i
			break
		}
	}
	require.GreaterOrEqual(t, bi, 0, "fixture has an existing-PR branch")

	var nodeText, metaText string
	for _, r := range rows {
		if r.branch != bi {
			continue
		}
		if r.nodeLine {
			nodeText = stripANSI(r.text)
		} else {
			metaText += stripANSI(r.text)
		}
	}
	assert.NotContains(t, nodeText, "·", "the node line shows only the branch name")
	assert.Contains(t, metaText, "· #", "the PR state and number sit on a line below the name")
}

func TestLeftPanel_ScrollsToFocusedBranch(t *testing.T) {
	m := testModel(t, newNodes())
	u, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20}) // short window -> overflow
	m = u.(Model)
	leftW, _ := m.panelWidths()
	require.Greater(t, len(m.buildLeftRows(leftW-2)), m.leftVisibleHeight(), "timeline overflows the panel")
	require.Equal(t, 0, m.leftScroll)

	for m.cursor < len(m.nodes)-1 {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Greater(t, m.leftScroll, 0, "navigating down scrolls the focused branch into view")

	rows := m.buildLeftRows(leftW - 2)
	visH := m.leftVisibleHeight()
	scroll := clampScroll(m.leftScroll, len(rows), visH)
	nodeIdx := -1
	for i, r := range rows {
		if r.branch == m.cursor && r.nodeLine {
			nodeIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, nodeIdx, 0)
	assert.GreaterOrEqual(t, nodeIdx, scroll, "the focused node is not above the window")
	assert.Less(t, nodeIdx, scroll+visH, "the focused node is within the window")

	before := m.leftScroll
	mm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 3})
	m = mm.(Model)
	assert.Less(t, m.leftScroll, before, "wheel-up over the left panel scrolls it up")
}

func TestMouse_ClickTitleFocuses(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // move off title
	require.Equal(t, fieldDescription, m.focusedField)

	titleLine, _, _, _, _ := m.rightZones()
	leftW, _ := m.panelWidths()
	y := m.panelTopRow() + titleLine
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: leftW + 5, Y: y})
	m = updated.(Model)
	assert.Equal(t, fieldTitle, m.focusedField)
}

func TestMouse_ClickDraftSegmentSelects(t *testing.T) {
	m := testModel(t, newNodes())
	_, _, _, _, draftLine := m.rightZones()
	y := m.panelTopRow() + draftLine
	segStart, dividerX, segEnd := m.draftSegmentBounds()
	require.False(t, m.nodes[1].Draft, "new PRs default to ready for review")

	// Clicking the right (Draft) half selects draft.
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: (dividerX + segEnd) / 2, Y: y})
	m = updated.(Model)
	assert.True(t, m.nodes[1].Draft, "clicking the Draft segment selects draft")
	assert.Equal(t, fieldDraft, m.focusedField)

	// Clicking the left (Ready) half selects ready.
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: (segStart + dividerX) / 2, Y: y})
	m = updated.(Model)
	assert.False(t, m.nodes[1].Draft, "clicking the Ready segment selects ready")
}

func TestMouse_ClickDraftLabelDoesNotToggle(t *testing.T) {
	m := testModel(t, newNodes())
	_, _, _, _, draftLine := m.rightZones()
	leftW, _ := m.panelWidths()
	y := m.panelTopRow() + draftLine
	before := m.nodes[1].Draft

	// A click on the "OPEN AS" label (left of the brackets) is inert.
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: leftW + 5, Y: y})
	m = updated.(Model)
	assert.Equal(t, before, m.nodes[1].Draft, "clicking the OPEN AS label must not toggle the value")

	// A click past the closing bracket is also inert.
	_, _, segEnd := m.draftSegmentBounds()
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: segEnd + 3, Y: y})
	m = updated.(Model)
	assert.Equal(t, before, m.nodes[1].Draft, "clicking past the brackets must not toggle the value")
}

// --- description editor ---

func TestDescription_ArrowsMoveCursorWithinText(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	require.Equal(t, fieldDescription, m.focusedField)
	m.descArea.SetValue("one\ntwo\nthree")
	m.descArea.CursorEnd() // last line
	start := m.descArea.Line()
	require.Greater(t, start, 0)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, start-1, m.descArea.Line(), "up arrow moves the cursor up a line")
}

func TestMouse_ClickDescriptionPositionsCursor(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	m.descArea.SetValue("alpha\nbravo\ncharlie\ndelta")
	leftW, _ := m.panelWidths()
	_, _, descTop, _, _ := m.rightZones()
	y := m.panelTopRow() + descTop + 1 + 2 // textarea content row index 2
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: leftW + 5, Y: y})
	m = updated.(Model)
	assert.Equal(t, 2, m.descArea.Line(), "clicking content row 2 positions the cursor on the third line")
	assert.True(t, m.descArea.Focused())
}

func TestMouse_WheelScrollsDescriptionViewport(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString("line\n")
	}
	m.nodes[0].Description = sb.String()
	m.descArea.SetValue(sb.String())
	m.descArea.CursorEnd()
	for i := 0; i < 60; i++ {
		m.descArea.CursorUp() // cursor to the top
	}
	require.Equal(t, 0, m.descArea.Line())

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 60})
	m = updated.(Model)
	assert.Greater(t, m.descScroll, 0, "wheel scrolls the viewport")
	assert.True(t, m.descScrollPinned, "wheel pins the scroll offset")
	assert.Equal(t, 0, m.descArea.Line(), "the cursor does not move while scrolling")

	// A keystroke returns to the cursor-following view.
	m = sendKey(t, m, runeKey('z'))
	assert.False(t, m.descScrollPinned, "typing returns to the cursor-following view")
}

func TestMouse_WheelScrollBoundedByContent(t *testing.T) {
	// The over-scroll bug only appears with a real color profile (the textarea
	// pads blank rows with styled spaces), so force one here.
	old := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(old)

	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("row\n")
	}
	m.nodes[0].Description = strings.TrimRight(sb.String(), "\n")
	m.descArea.SetValue(strings.TrimRight(sb.String(), "\n"))

	_, rightW := m.panelWidths()
	innerW := rightW - 4
	// The wrapped line count must match the real content (30 rows), not the
	// textarea's padded height — otherwise the scroll range over-counts and the
	// user can scroll past the last line.
	assert.Equal(t, 30, len(m.descFullLines(innerW)), "wrapped line count matches content")

	// Wheel down far past the end.
	for i := 0; i < 50; i++ {
		u, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 60})
		m = u.(Model)
	}
	assert.Equal(t, m.maxDescScroll(innerW), m.descScroll, "scroll is clamped to the content")
	// The last rendered row must be real content, not a blank past the last line.
	rows := strings.Split(m.descContent(innerW), "\n")
	last := strings.TrimSpace(stripANSI(rows[len(rows)-1]))
	assert.NotEmpty(t, last, "cannot scroll past the last line of text")
}

func TestDescAreaHeight_FillsAvailableSpace(t *testing.T) {
	tall, _ := testModel(t, newNodes()).Update(tea.WindowSizeMsg{Width: 100, Height: 70})
	short, _ := testModel(t, newNodes()).Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	tallH := tall.(Model).descAreaHeight()
	shortH := short.(Model).descAreaHeight()
	assert.Greater(t, tallH, shortH, "the description box grows with the terminal")
	assert.Greater(t, tallH, 20, "the box is no longer clamped to 20 rows")
}

func TestLoadEditor_StartsAtTop(t *testing.T) {
	m := testModel(t, newNodes())
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("line\n")
	}
	m.nodes[m.cursor].Description = strings.TrimRight(sb.String(), "\n")
	m.loadEditor()
	assert.Equal(t, 0, m.descArea.Line(), "cursor starts on the first line")
	assert.Equal(t, 0, m.descScroll, "view starts scrolled to the top")
	assert.False(t, m.descScrollPinned)
}

func TestDescScroll_PreservedWhenDefocused(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line\n")
	}
	m.nodes[0].Description = strings.TrimRight(sb.String(), "\n")
	m.descArea.SetValue(strings.TrimRight(sb.String(), "\n"))
	for i := 0; i < 3; i++ {
		u, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 60})
		m = u.(Model)
	}
	require.Greater(t, m.descScroll, 0)
	scrolled := m.descScroll

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // de-focus (move to draft)
	require.NotEqual(t, fieldDescription, m.focusedField)
	assert.Equal(t, scrolled, m.descScroll, "scroll is preserved when the box loses focus")
	assert.True(t, m.descScrollPinned)
}

func TestScrollbar_RendersWhenOverflowing(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab})
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString("line\n")
	}
	m.nodes[0].Description = strings.TrimRight(sb.String(), "\n")
	m.descArea.SetValue(strings.TrimRight(sb.String(), "\n"))
	_, rightW := m.panelWidths()
	out := m.descContent(rightW - 4)
	assert.Contains(t, out, "┃", "scrollbar thumb is drawn when content overflows")
	assert.Contains(t, out, "│", "scrollbar track is drawn when content overflows")
}

func TestPreview_ScrollableWithScrollbar(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	var sb strings.Builder
	sb.WriteString("# Title\n\n")
	for i := 0; i < 40; i++ {
		sb.WriteString("A paragraph of preview text.\n\n")
	}
	m.nodes[1].Description = strings.TrimRight(sb.String(), "\n")
	m.descArea.SetValue(strings.TrimRight(sb.String(), "\n"))

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlP}) // switch to preview
	require.True(t, m.descPreview)
	_, rightW := m.panelWidths()
	assert.Contains(t, m.descContent(rightW-4), "┃", "preview has a scrollbar when it overflows")

	before := m.descScroll
	u, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 60})
	m = u.(Model)
	assert.Greater(t, m.descScroll, before, "the wheel scrolls the preview")
}
