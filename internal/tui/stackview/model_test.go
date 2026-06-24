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

func TestInteractive_EnterChecksOutInPlace(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	nodes[0].IsCurrent = true

	var checkedOut string
	m := NewInteractive(nodes, testTrunk, "0.0.1", InteractiveActions{Checkout: func(branch string) error {
		checkedOut = branch
		return nil
	}})

	// Move to b2 (non-current).
	updated, _ := m.Update(keyMsg("down"))
	m = updated.(Model)
	assert.Equal(t, 1, m.cursor)

	// Press enter: interactive mode should not quit and should not set checkoutBranch.
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(Model)
	assert.Equal(t, "", m.CheckoutBranch(), "interactive checkout should not use quit-then-checkout path")
	if assert.NotNil(t, cmd, "interactive enter should produce a checkout command") {
		// Run the command to obtain the result message and feed it back.
		msg := cmd()
		result, ok := msg.(checkoutResultMsg)
		assert.True(t, ok, "command should produce a checkoutResultMsg")
		assert.Equal(t, "b2", checkedOut, "checkoutFn should be invoked with the selected branch")

		updated, quitCmd := m.Update(result)
		m = updated.(Model)
		assert.Nil(t, quitCmd, "successful in-place checkout should not quit")
		assert.False(t, m.nodes[0].IsCurrent, "previous current marker should be cleared")
		assert.True(t, m.nodes[1].IsCurrent, "selected branch should become current")
		assert.Equal(t, "", m.errMsg)
	}
}

func TestInteractive_CheckoutErrorSetsMessage(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	nodes[0].IsCurrent = true

	m := NewInteractive(nodes, testTrunk, "0.0.1", InteractiveActions{Checkout: func(branch string) error {
		return fmt.Errorf("boom")
	}})

	updated, _ := m.Update(keyMsg("down"))
	m = updated.(Model)

	_, cmd := m.Update(keyMsg("enter"))
	msg := cmd()
	result := msg.(checkoutResultMsg)

	updated, quitCmd := m.Update(result)
	m = updated.(Model)
	assert.Nil(t, quitCmd, "checkout error should not quit")
	assert.Contains(t, m.errMsg, "boom", "error message should be surfaced")
	assert.True(t, m.nodes[0].IsCurrent, "current marker should be unchanged on error")
	assert.False(t, m.nodes[1].IsCurrent)
}

func TestInteractive_RefreshReplacesNodes(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	nodes[0].IsCurrent = true

	fresh := makeNodes("b1", "b2", "b3")
	called := false
	m := NewInteractive(nodes, testTrunk, "0.0.1", InteractiveActions{
		Refresh: func() ([]BranchNode, error) {
			called = true
			return fresh, nil
		},
	})

	updated, cmd := m.Update(keyMsg("r"))
	m = updated.(Model)
	assert.True(t, m.busy, "refresh should mark the model busy")
	if assert.NotNil(t, cmd, "refresh should produce a command") {
		msg := cmd()
		result, ok := msg.(refreshResultMsg)
		assert.True(t, ok, "command should produce a refreshResultMsg")
		assert.True(t, called, "refresh func should be invoked")

		updated, _ = m.Update(result)
		m = updated.(Model)
		assert.False(t, m.busy, "refresh result should clear busy")
		assert.Len(t, m.nodes, 3, "nodes should be replaced with fresh data")
		assert.Equal(t, "Refreshed", m.infoMsg)
	}
}

func TestInteractive_RefreshErrorSetsMessage(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := NewInteractive(nodes, testTrunk, "0.0.1", InteractiveActions{
		Refresh: func() ([]BranchNode, error) {
			return nil, fmt.Errorf("network down")
		},
	})

	updated, cmd := m.Update(keyMsg("r"))
	m = updated.(Model)
	result := cmd().(refreshResultMsg)
	updated, _ = m.Update(result)
	m = updated.(Model)
	assert.False(t, m.busy)
	assert.Len(t, m.nodes, 2, "nodes should be unchanged on error")
	assert.Contains(t, m.errMsg, "network down")
}

func TestInteractive_PushRequiresConfirmation(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	pushed := false
	m := NewInteractive(nodes, testTrunk, "0.0.1", InteractiveActions{
		Push: func() error {
			pushed = true
			return nil
		},
	})

	// Press p: should enter confirm mode without pushing.
	updated, cmd := m.Update(keyMsg("p"))
	m = updated.(Model)
	assert.True(t, m.confirmMode, "p should enter confirm mode")
	assert.Equal(t, "push", m.confirmAction)
	assert.Nil(t, cmd, "p should not dispatch a push yet")
	assert.False(t, pushed)
	assert.Contains(t, m.statusLine(), "Push 2 branches")

	// Cancel with n.
	updated, _ = m.Update(keyMsg("n"))
	m = updated.(Model)
	assert.False(t, m.confirmMode, "n should cancel confirm mode")
	assert.False(t, pushed)

	// Press p then confirm with y.
	updated, _ = m.Update(keyMsg("p"))
	m = updated.(Model)
	updated, cmd = m.Update(keyMsg("y"))
	m = updated.(Model)
	assert.False(t, m.confirmMode, "y should exit confirm mode")
	assert.True(t, m.busy, "confirmed push should mark busy")
	if assert.NotNil(t, cmd, "confirmed push should dispatch a command") {
		result, ok := cmd().(actionResultMsg)
		assert.True(t, ok)
		assert.True(t, pushed, "push func should be invoked after confirmation")
		updated, _ = m.Update(result)
		m = updated.(Model)
		assert.False(t, m.busy)
		assert.Equal(t, "push complete", m.infoMsg)
	}
}

func TestInteractive_PushErrorSetsMessage(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := NewInteractive(nodes, testTrunk, "0.0.1", InteractiveActions{
		Push: func() error {
			return fmt.Errorf("rejected")
		},
	})

	updated, _ := m.Update(keyMsg("p"))
	m = updated.(Model)
	updated, cmd := m.Update(keyMsg("y"))
	m = updated.(Model)
	result := cmd().(actionResultMsg)
	updated, _ = m.Update(result)
	m = updated.(Model)
	assert.False(t, m.busy)
	assert.Contains(t, m.errMsg, "rejected")
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

func TestView_HeaderShortcutsAlwaysVisible(t *testing.T) {
	nodes := makeNodes("b1", "b2")
	m := New(nodes, testTrunk, "0.0.1")

	// Even at medium width, shortcuts should still be visible
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = updated.(Model)

	view := m.View()
	assert.Contains(t, view, "┌", "header should be shown")
	assert.Contains(t, view, "checkout", "shortcuts should always be visible")
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

func TestUpdate_CursorSkipsMergedBranches(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	nodes[1].Ref.PullRequest = &stack.PullRequestRef{Number: 2, Merged: true}
	m := New(nodes, testTrunk, "0.0.1")
	assert.Equal(t, 0, m.cursor, "cursor should start on first non-merged branch")

	// Down should skip b2 (merged) and land on b3
	updated, _ := m.Update(keyMsg("down"))
	m = updated.(Model)
	assert.Equal(t, 2, m.cursor, "down should skip merged b2 and land on b3")

	// Up should skip b2 (merged) and land back on b1
	updated, _ = m.Update(keyMsg("up"))
	m = updated.(Model)
	assert.Equal(t, 0, m.cursor, "up should skip merged b2 and land on b1")
}

func TestNew_CursorSkipsMergedBranch(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	nodes[0].Ref.PullRequest = &stack.PullRequestRef{Number: 1, Merged: true}
	m := New(nodes, testTrunk, "0.0.1")
	assert.Equal(t, 1, m.cursor, "cursor should skip merged b1 and start on b2")
}

func TestNew_CursorSkipsMergedCurrentBranch(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	nodes[0].IsCurrent = true
	nodes[0].Ref.PullRequest = &stack.PullRequestRef{Number: 1, Merged: true}
	m := New(nodes, testTrunk, "0.0.1")
	assert.Equal(t, 1, m.cursor, "cursor should not start on merged current branch")
}

func TestUpdate_EnterOnMergedDoesNothing(t *testing.T) {
	// All non-merged so we can navigate, but force cursor onto a merged node
	// by having b1 active and b2 merged and b3 active.
	nodes := makeNodes("b1", "b2")
	nodes[0].Ref.PullRequest = &stack.PullRequestRef{Number: 1, Merged: true}
	m := New(nodes, testTrunk, "0.0.1")
	// Cursor is on b2 (first non-merged). Manually set to b1 to test guard.
	m.cursor = 0

	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(Model)
	assert.Equal(t, "", m.CheckoutBranch(), "enter on merged branch should not set checkout")
	assert.Nil(t, cmd, "enter on merged branch should not quit")
}
