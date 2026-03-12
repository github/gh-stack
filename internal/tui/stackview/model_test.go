package stackview

import (
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

	m := New(nodes, testTrunk)

	assert.Equal(t, 1, m.cursor)
}

func TestNew_CursorAtZeroWhenNoCurrent(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")

	m := New(nodes, testTrunk)

	assert.Equal(t, 0, m.cursor)
}

func TestUpdate_KeyboardNavigation(t *testing.T) {
	nodes := makeNodes("b1", "b2", "b3")
	m := New(nodes, testTrunk)
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
	m := New(nodes, testTrunk)

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
	m := New(nodes, testTrunk)

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
	m := New(nodes, testTrunk)

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
	m := New(nodes, testTrunk)

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
	m := New(nodes, testTrunk)
	assert.Equal(t, 0, m.cursor)

	// Press enter on current node
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(Model)

	assert.Equal(t, "", m.CheckoutBranch(), "enter on current branch should not set checkout")
	assert.Nil(t, cmd, "enter on current branch should not quit")
}

