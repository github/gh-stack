package submitview

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test helpers ---

// newNode builds a SubmitNode with prefill snapshots set (so it reads as
// unedited) for the given branch and state.
func newNode(branch string, st BranchState) SubmitNode {
	title := "Title " + branch
	desc := "Desc"
	n := SubmitNode{
		BranchNode:   stackview.BranchNode{Ref: stack.BranchRef{Branch: branch}},
		State:        st,
		Included:     st == StateNew,
		Title:        title,
		Description:  desc,
		titlePrefill: title,
		descPrefill:  desc,
	}
	if st != StateNew {
		n.Ref.PullRequest = &stack.PullRequestRef{Number: 1240, URL: "https://github.com/o/r/pull/1240"}
		n.PR = &ghapi.PRDetails{Number: 1240, State: "OPEN", URL: "https://github.com/o/r/pull/1240"}
	}
	return n
}

// newNodes returns a representative stack: two NEW branches followed by locked
// branches in several states.
func newNodes() []SubmitNode {
	return []SubmitNode{
		newNode("feat/auth/tests", StateNew),
		newNode("feat/auth/middleware", StateNew),
		newNode("feat/auth/handlers", StateDraft),
		newNode("feat/auth/models", StateOpen),
		newNode("feat/auth/router", StateQueued),
		newNode("feat/auth/ui", StateMerged),
	}
}

func testModel(t *testing.T, nodes []SubmitNode) Model {
	t.Helper()
	m := New(Options{
		Nodes:     nodes,
		Trunk:     stack.BranchRef{Branch: "main"},
		RepoLabel: "myorg/myrepo",
		Version:   "1.0.0",
	})
	// Never spawn a real browser from the test suite when opening an existing PR.
	m.openURL = func(string) {}
	// Height accommodates the shared 12-line header plus the full editor.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return updated.(Model)
}

func sendKey(t *testing.T, m Model, msg tea.KeyMsg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// --- constructor ---

func TestNew_OpensEditorOnBottomMostNew(t *testing.T) {
	m := testModel(t, newNodes())
	assert.Equal(t, 1, m.cursor, "opens on the bottom-most NEW branch (closest to trunk)")
	assert.Equal(t, fieldTitle, m.focusedField)
	assert.True(t, m.titleInput.Focused(), "title is focused for immediate editing")
}

func TestNew_AllNewIncludedByDefault(t *testing.T) {
	m := testModel(t, newNodes())
	assert.True(t, m.nodes[0].Included)
	assert.True(t, m.nodes[1].Included)
}

// --- navigation ---

func TestNavigation_MovesAcrossAllBranches(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, 1, m.cursor) // bottom-most NEW

	// Lands on locked branches too (Mode 3).
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 2, m.cursor)
	assert.True(t, m.nodes[m.cursor].State.Locked())

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 1, m.cursor)

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, m.cursor)
}

func TestNavigation_ClampsAtEnds(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, m.cursor)

	for i := 0; i < 20; i++ {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, len(m.nodes)-1, m.cursor)
}

func TestNavigation_FocusesLockedWithNoTextInput(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown}) // locked
	require.True(t, m.nodes[m.cursor].State.Locked())
	assert.False(t, m.titleInput.Focused())
	assert.False(t, m.descArea.Focused())
}

// --- include / skip ---

func TestCtrlX_TogglesInclude(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp}) // focus the top NEW branch
	require.Equal(t, 0, m.cursor)
	require.True(t, m.nodes[0].Included)

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	assert.False(t, m.nodes[0].Included, "^x skips the focused NEW branch")
	assert.False(t, m.titleInput.Focused(), "a skipped branch's title is non-interactive")

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	assert.True(t, m.nodes[0].Included, "^x includes it again")
	assert.True(t, m.titleInput.Focused(), "re-including focuses the title for editing")
}

func TestCtrlX_WorksWhileTypingTitle(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyUp}) // focus the top NEW branch
	require.Equal(t, fieldTitle, m.focusedField)
	m = sendKey(t, m, runeKey('Z')) // typing into title
	require.Contains(t, m.nodes[0].Title, "Z")

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	assert.False(t, m.nodes[0].Included, "^x works mid-text-entry")
	assert.Contains(t, m.nodes[0].Title, "Z", "in-progress title is preserved")
}

func TestCtrlX_NoOpOnLockedBranch(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyDown}) // locked
	require.True(t, m.nodes[m.cursor].State.Locked())
	before := m.nodes[m.cursor].Included
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	assert.Equal(t, before, m.nodes[m.cursor].Included)
}

func TestSpace_ReincludesSkippedBranch(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip the focused branch
	require.False(t, m.nodes[m.cursor].Included)

	// space on a skipped branch re-includes it (no Create-PR field anymore).
	m = sendKey(t, m, runeKey(' '))
	assert.True(t, m.nodes[m.cursor].Included)
}

// --- quit ---

func TestQuit_WhenUnedited(t *testing.T) {
	m := testModel(t, newNodes())
	require.False(t, m.anyEdited())
	// 'q' quits from a non-text field (it is literal while editing a title).
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // description
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // ready/draft toggle
	require.Equal(t, fieldDraft, m.focusedField)
	updated, cmd := m.Update(runeKey('q'))
	m = updated.(Model)
	assert.True(t, m.cancelled)
	assert.False(t, m.confirmingQuit)
	assert.NotNil(t, cmd)
}

func TestEsc_Quits(t *testing.T) {
	m := testModel(t, newNodes())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(Model)
	assert.True(t, m.cancelled)
}

func TestQuitConfirm_WhenEdited(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlX}) // skip a branch = an edit
	require.True(t, m.anyEdited())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(Model)
	assert.True(t, m.confirmingQuit)
	assert.False(t, m.cancelled)
	assert.Contains(t, m.View(), "Discard edits")

	m = sendKey(t, m, runeKey('n'))
	assert.False(t, m.confirmingQuit)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(Model)
	require.True(t, m.confirmingQuit)
	updated, cmd := m.Update(runeKey('y'))
	m = updated.(Model)
	assert.True(t, m.cancelled)
	assert.NotNil(t, cmd)
}

// --- help ---

func TestHelpToggle(t *testing.T) {
	m := testModel(t, newNodes())
	// '?' opens help only when not in a text field; move to the ready/draft toggle.
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // description
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // ready/draft toggle
	require.Equal(t, fieldDraft, m.focusedField)
	m = sendKey(t, m, runeKey('?'))
	assert.True(t, m.showHelp)
	assert.Contains(t, m.View(), "keyboard & mouse")

	m = sendKey(t, m, runeKey('?'))
	assert.False(t, m.showHelp)
}

func TestHelp_NotOpenedWhileTypingTitle(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, fieldTitle, m.focusedField)
	m = sendKey(t, m, runeKey('?'))
	assert.False(t, m.showHelp, "'?' is literal while editing the title")
	assert.Contains(t, m.nodes[1].Title, "?")
}

// --- view ---

func TestView_RendersKeyElements(t *testing.T) {
	m := testModel(t, newNodes())
	out := m.View()
	assert.Contains(t, out, "Submit Stack")       // shared header title
	assert.Contains(t, out, "Repo: myorg/myrepo") // repo info line (header)
	assert.Contains(t, out, "Creating 2 PRs")     // pending-PR info line (header)
	assert.Contains(t, out, "submit PRs")         // keyboard shortcut in the shared header
	assert.Contains(t, out, "CREATE PR")          // include switch
	assert.Contains(t, out, "tests")
	assert.Contains(t, out, "main")
}

func TestHelp_CtrlHTogglesWhileEditing(t *testing.T) {
	m := testModel(t, newNodes())
	require.Equal(t, fieldTitle, m.focusedField) // editing the title

	// '?' is typed into the title field, not treated as help, while editing.
	m = sendKey(t, m, runeKey('?'))
	assert.False(t, m.showHelp, "? is typed into the field while editing")
	assert.Contains(t, m.nodes[m.cursor].Title, "?")

	// ^h opens help even while editing a field.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	m = updated.(Model)
	assert.True(t, m.showHelp, "^h opens help while editing")

	// ^h closes it again.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	m = updated.(Model)
	assert.False(t, m.showHelp, "^h closes help")
}

func TestHelp_OverlayListsFullShortcuts(t *testing.T) {
	m := testModel(t, newNodes())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	m = updated.(Model)
	out := m.View()
	for _, s := range []string{"^x", "^p", "^e", "^o", "^s", "^h", "tab", "mouse"} {
		assert.Contains(t, out, s, "the help overlay lists %q", s)
	}
}

func TestHeaderConfig_InfoLines(t *testing.T) {
	// Repo and base are the first two info lines, in order.
	cfg := testModel(t, newNodes()).buildHeaderConfig()
	require.GreaterOrEqual(t, len(cfg.InfoLines), 3)
	assert.Equal(t, "○", cfg.InfoLines[0].Icon)
	assert.Equal(t, "Repo: myorg/myrepo", cfg.InfoLines[0].Label)
	assert.Equal(t, "◆", cfg.InfoLines[1].Icon)
	assert.Equal(t, "Base: main", cfg.InfoLines[1].Label)

	// Two included NEW branches -> solid (styled) square, pluralized.
	last := cfg.InfoLines[len(cfg.InfoLines)-1]
	assert.Equal(t, "■", last.Icon)
	assert.Equal(t, "Creating 2 PRs", last.Label)
	assert.NotNil(t, last.IconStyle, "the creating line is styled yellow")

	// A single included NEW branch -> singular.
	oneCfg := testModel(t, []SubmitNode{newNode("feat/x", StateNew)}).buildHeaderConfig()
	oneLast := oneCfg.InfoLines[len(oneCfg.InfoLines)-1]
	assert.Equal(t, "■", oneLast.Icon)
	assert.Equal(t, "Creating 1 PR", oneLast.Label)

	// No NEW branches (all already have PRs) -> empty square, default style.
	noneCfg := testModel(t, []SubmitNode{
		newNode("feat/a", StateOpen),
		newNode("feat/b", StateMerged),
	}).buildHeaderConfig()
	noneLast := noneCfg.InfoLines[len(noneCfg.InfoLines)-1]
	assert.Equal(t, "□", noneLast.Icon)
	assert.Equal(t, "No pending PRs", noneLast.Label)
	assert.Nil(t, noneLast.IconStyle, "the empty line uses the default icon style")
}

func TestView_ClosedBanner(t *testing.T) {
	nodes := newNodes()
	nodes = append(nodes, newNode("feat/auth/legacy", StateClosed))
	m := testModel(t, nodes)
	out := m.View()
	assert.Contains(t, out, "closed PR")
	assert.Contains(t, out, "feat/auth/legacy")
}

func TestView_EmptyWhenNoSize(t *testing.T) {
	m := New(Options{Nodes: newNodes(), Trunk: stack.BranchRef{Branch: "main"}})
	assert.Equal(t, "", m.View())
}

// --- mouse ---

func TestMouse_ClickBranchFocuses(t *testing.T) {
	m := testModel(t, newNodes())
	y := m.panelTopRow() + 2 + 1 // branch index 1
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 10, Y: y})
	m = updated.(Model)
	assert.Equal(t, 1, m.cursor)
}

func TestMouse_ClickCheckboxToggles(t *testing.T) {
	m := testModel(t, newNodes())
	require.True(t, m.nodes[0].Included)
	y, cbX := leftBranchNode(m, 0)
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: cbX, Y: y})
	m = updated.(Model)
	assert.False(t, m.nodes[0].Included)
}

// leftBranchNode returns the screen Y of branch idx's node line and the X of its
// right-edge checkbox, using the live left-panel timeline layout (scroll-aware).
func leftBranchNode(m Model, idx int) (y, checkboxX int) {
	leftW, _ := m.panelWidths()
	rows := m.buildLeftRows(leftW - 2)
	visH := m.leftVisibleHeight()
	scroll := clampScroll(m.leftScroll, len(rows), visH)
	for i, r := range rows {
		if r.branch == idx && r.nodeLine {
			y = m.panelTopRow() + (i - scroll)
			break
		}
	}
	checkboxX = leftW - 4 // middle column of the right-aligned "[x]"
	return y, checkboxX
}
