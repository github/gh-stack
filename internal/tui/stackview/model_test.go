package stackview

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/git"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
)

func makeNodes(branches ...string) []BranchNode {
	nodes := make([]BranchNode, len(branches))
	for i, b := range branches {
		nodes[i] = BranchNode{
			Ref: stack.BranchRef{Branch: b},
		}
	}
	return nodes
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "up":
		return tea.KeyMsg(tea.Key{Type: tea.KeyUp})
	case "down":
		return tea.KeyMsg(tea.Key{Type: tea.KeyDown})
	case "enter":
		return tea.KeyMsg(tea.Key{Type: tea.KeyEnter})
	case "esc":
		return tea.KeyMsg(tea.Key{Type: tea.KeyEscape})
	case "ctrl+c":
		return tea.KeyMsg(tea.Key{Type: tea.KeyCtrlC})
	default:
		// Single rune key like 'c', 'f', 'q', 'o'
		return tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune(k)})
	}
}

var testTrunk = stack.BranchRef{Branch: "main"}

func TestNew_CursorAtCurrentBranch(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	nodes[1].IsCurrent = true

	m := New(nodes, testTrunk, "0.0.1")

	assert.Equal(t, 1, m.cursor)
}

func TestNew_CursorAtZeroWhenNoCurrent(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")

	m := New(nodes, testTrunk, "0.0.1")

	assert.Equal(t, 0, m.cursor)
}

func TestUpdate_KeyboardNavigation(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	m := New(nodes, testTrunk, "0.0.1")
	assert.Equal(t, 0, m.cursor)

	// Down
	updated, _ := m.Update(keyMsg("down"))
	m = updated.(Model)
	assert.Equal(t, 1, m.cursor)

	// Down again
	updated, _ = m.Update(keyMsg("down"))
	m = updated.(Model)
	assert.Equal(t, 2, m.cursor)

	// Down at bottom — should clamp
	updated, _ = m.Update(keyMsg("down"))
	m = updated.(Model)
	assert.Equal(t, 2, m.cursor, "cursor should clamp at bottom")

	// Up
	updated, _ = m.Update(keyMsg("up"))
	m = updated.(Model)
	assert.Equal(t, 1, m.cursor)

	// Up
	updated, _ = m.Update(keyMsg("up"))
	m = updated.(Model)
	assert.Equal(t, 0, m.cursor)

	// Up at top — should clamp
	updated, _ = m.Update(keyMsg("up"))
	m = updated.(Model)
	assert.Equal(t, 0, m.cursor, "cursor should clamp at top")
}

func TestUpdate_ToggleCommits(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	nodes[0].Commits = []git.CommitInfo{{SHA: "abc", Subject: "test"}}
	m := New(nodes, testTrunk, "0.0.1")

	assert.False(t, m.nodes[0].CommitsExpanded)

	updated, _ := m.Update(keyMsg("c"))
	m = updated.(Model)
	assert.True(t, m.nodes[0].CommitsExpanded)

	// Toggle back
	updated, _ = m.Update(keyMsg("c"))
	m = updated.(Model)
	assert.False(t, m.nodes[0].CommitsExpanded)
}

func TestUpdate_ToggleFiles(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := New(nodes, testTrunk, "0.0.1")

	assert.False(t, m.nodes[0].FilesExpanded)

	updated, _ := m.Update(keyMsg("f"))
	m = updated.(Model)
	assert.True(t, m.nodes[0].FilesExpanded)

	// Toggle back
	updated, _ = m.Update(keyMsg("f"))
	m = updated.(Model)
	assert.False(t, m.nodes[0].FilesExpanded)
}

func TestUpdate_Quit(t *testing.T) {
	nodes := makeNodes("b1")
	m := New(nodes, testTrunk, "0.0.1")

	quitKeys := []string{"q", "esc", "ctrl+c"}
	for _, k := range quitKeys {
		t.Run(k, func(t *testing.T) {
			_, cmd := m.Update(keyMsg(k))
			assert.NotNil(t, cmd, "key %q should produce a quit command", k)
		})
	}
}

func TestUpdate_CheckoutOnEnter(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	nodes[0].IsCurrent = true
	nodes[1].PR = &ghapi.PRDetails{Number: 42, URL: "https://github.com/pr/42"}
	m := New(nodes, testTrunk, "0.0.1")

	// Move to b2 (non-current)
	updated, _ := m.Update(keyMsg("down"))
	m = updated.(Model)
	assert.Equal(t, 1, m.cursor)

	// Press enter on non-current node
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(Model)

	assert.Equal(t, "b2", m.CheckoutBranch())
	assert.NotNil(t, cmd, "enter on non-current should produce quit command")
}

func TestUpdate_EnterOnCurrentDoesNothing(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	nodes[0].IsCurrent = true
	m := New(nodes, testTrunk, "0.0.1")
	assert.Equal(t, 0, m.cursor)

	// Press enter on current node
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(Model)

	assert.Equal(t, "", m.CheckoutBranch(), "enter on current branch should not set checkout")
	assert.Nil(t, cmd, "enter on current branch should not quit")
}

func TestView_HeaderShownWhenTallEnough(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := New(nodes, testTrunk, "0.0.1")

	// Simulate a tall and wide terminal
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	view := m.View()
	assert.Contains(t, view, "┌")
	assert.Contains(t, view, "┘")
	assert.Contains(t, view, "View Stack")
	assert.Contains(t, view, "v0.0.1")
	assert.Contains(t, view, "Base: main")
	assert.Contains(t, view, "2 branches")
	assert.Contains(t, view, "↑")
	assert.Contains(t, view, "quit")
}

func TestView_HeaderHiddenWhenShort(t *testing.T) {
	nodes := makeNodes("b1")
	m := New(nodes, testTrunk, "0.0.1")

	// Simulate a short terminal (below minHeightForHeader)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(Model)

	view := m.View()
	// Should NOT contain header box
	assert.NotContains(t, view, "┌")
	assert.NotContains(t, view, "View Stack")
	// Should NOT contain help bar either (hints are only in header)
	assert.NotContains(t, view, "commits")
}

func TestView_HeaderHiddenWhenNarrow(t *testing.T) {
	nodes := makeNodes("b1")
	m := New(nodes, testTrunk, "0.0.1")

	// Tall but too narrow for header (below minWidthForHeader)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 35, Height: 40})
	m = updated.(Model)

	view := m.View()
	assert.NotContains(t, view, "┌")
	assert.NotContains(t, view, "View Stack")
}

func TestView_HeaderWithoutShortcutsWhenMediumWidth(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := New(nodes, testTrunk, "0.0.1")

	// Wide enough for header but not for shortcuts (between minWidthForHeader and minWidthForShortcuts)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = updated.(Model)

	view := m.View()
	assert.Contains(t, view, "┌", "header should be shown")
	assert.Contains(t, view, "View Stack", "info should be shown")
	assert.NotContains(t, view, "checkout", "shortcuts should be hidden at this width")
}

func TestView_HeaderShowsMergedCount(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	nodes[0].Ref.PullRequest = &stack.PullRequestRef{Merged: true}
	m := New(nodes, testTrunk, "0.0.1")

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	view := m.View()
	assert.Contains(t, view, "3 branches (1 merged)")
}

func TestView_HeaderShowsQueuedCount(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	nodes[1].Ref.Queued = true
	nodes[1].Ref.PullRequest = &stack.PullRequestRef{Number: 10}
	m := New(nodes, testTrunk, "0.0.1")

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	view := m.View()
	assert.Contains(t, view, "3 branches (1 queued)")
}

func TestView_QueuedPRShowsQueuedLabel(t *testing.T) {
	nodes := makeNodes("b1")
	nodes[0].PR = &ghapi.PRDetails{Number: 99, IsQueued: true}
	m := New(nodes, testTrunk, "0.0.1")

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updated.(Model)

	view := m.View()
	assert.Contains(t, view, "QUEUED")
	assert.Contains(t, view, "#99")
}

func TestView_BranchProgressIcon(t *testing.T) {
	tests := []struct {
		name     string
		merged   []int // indices of merged branches
		total    int
		wantIcon string
	}{
		{"none merged", nil, 3, "○"},
		{"some merged", []int{0}, 3, "◐"},
		{"all merged", []int{0, 1, 2}, 3, "●"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names := make([]string, tt.total)
			for i := range names {
				names[i] = fmt.Sprintf("b%d", i)
			}
			nodes := makeNodes(names...)
			for _, idx := range tt.merged {
				nodes[idx].Ref.PullRequest = &stack.PullRequestRef{Merged: true}
			}
			m := New(nodes, testTrunk, "0.0.1")
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
			m = updated.(Model)

			view := m.View()
			assert.Contains(t, view, tt.wantIcon)
		})
	}
}

func TestMouseClick_HeaderAreaIgnored(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := New(nodes, testTrunk, "0.0.1")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	// Click inside the header area (row 5 is inside the 12-line header)
	updated, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      10,
		Y:      5,
	})
	result := updated.(Model)
	assert.Equal(t, 0, result.cursor, "clicking in header should not change cursor")
}

func TestScrollClamp_CannotScrollPastContent(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := New(nodes, testTrunk, "0.0.1")

	// Tall terminal with plenty of room for content
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updated.(Model)

	// Scroll down many times — should not scroll past content
	for i := 0; i < 50; i++ {
		updated, _ = m.Update(tea.MouseMsg{
			Action: tea.MouseActionPress,
			Button: tea.MouseButtonWheelDown,
		})
		m = updated.(Model)
	}

	// scrollOffset should be clamped (content fits in view, so offset stays 0)
	view := m.View()
	assert.Contains(t, view, "b1", "content should still be visible after excessive scrolling")
}
