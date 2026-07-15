package submitview

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
)

// deselectedNew builds a NEW branch node that the user has deselected.
func deselectedNew(branch string) SubmitNode {
	n := newNode(branch, StateNew)
	n.Included = false
	return n
}

// stackModel builds a sized model with the given CanCreateStack flag.
func stackModel(t *testing.T, nodes []SubmitNode, canCreateStack bool) Model {
	t.Helper()
	m := New(Options{
		Nodes:          nodes,
		Trunk:          stack.BranchRef{Branch: "main"},
		RepoLabel:      "myorg/myrepo",
		Version:        "1.0.0",
		CanCreateStack: canCreateStack,
	})
	m.openURL = func(string) {}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return updated.(Model)
}

func TestCountOpenPRs(t *testing.T) {
	nodes := []SubmitNode{
		newNode("a", StateNew),
		newNode("b", StateOpen),
		newNode("c", StateDraft),
		newNode("d", StateQueued),
		newNode("e", StateMerged),
		newNode("f", StateClosed),
	}
	assert.Equal(t, 2, CountOpenPRs(nodes), "only open and draft PRs count")
}

func TestCanStackExistingPRs(t *testing.T) {
	twoOpen := []SubmitNode{newNode("a", StateOpen), newNode("b", StateDraft)}
	tests := []struct {
		name           string
		nodes          []SubmitNode
		canCreateStack bool
		want           bool
	}{
		{"two open PRs, no stack, none selected", twoOpen, true, true},
		{"deselected NEW + two open PRs", []SubmitNode{deselectedNew("n"), newNode("a", StateOpen), newNode("b", StateDraft)}, true, true},
		{"has a remote stack already", twoOpen, false, false},
		{"a NEW branch is still selected", []SubmitNode{newNode("n", StateNew), newNode("a", StateOpen), newNode("b", StateDraft)}, true, false},
		{"only one open PR", []SubmitNode{newNode("a", StateOpen)}, true, false},
		{"open PRs but queued/merged do not count", []SubmitNode{newNode("a", StateOpen), newNode("b", StateQueued), newNode("c", StateMerged)}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := stackModel(t, tt.nodes, tt.canCreateStack)
			assert.Equal(t, tt.want, m.canStackExistingPRs())
		})
	}
}

func TestStackButton_RendersWhenEligible(t *testing.T) {
	m := stackModel(t, []SubmitNode{deselectedNew("feat/new"), newNode("a", StateOpen), newNode("b", StateDraft)}, true)
	view := m.View()
	assert.Contains(t, view, "STACK 2 PRs")
	assert.Contains(t, view, "(^b)")
}

func TestStackButton_HiddenWhenNotEligible(t *testing.T) {
	// Has a remote stack already.
	withStack := stackModel(t, []SubmitNode{newNode("a", StateOpen), newNode("b", StateDraft)}, false)
	assert.NotContains(t, withStack.View(), "STACK 2 PRs")
	assert.NotContains(t, withStack.View(), "(^b)")

	// A NEW branch is still selected for creation.
	newSelected := stackModel(t, []SubmitNode{newNode("n", StateNew), newNode("a", StateOpen), newNode("b", StateDraft)}, true)
	assert.NotContains(t, newSelected.View(), "STACK 2 PRs")
	assert.NotContains(t, newSelected.View(), "(^b)")
}

func TestStackButton_HeaderHintSwaps(t *testing.T) {
	eligible := stackModel(t, []SubmitNode{deselectedNew("feat/new"), newNode("a", StateOpen), newNode("b", StateDraft)}, true)
	view := eligible.View()
	assert.Contains(t, view, "stack PRs", "header shows the stack hint when eligible")
	assert.NotContains(t, view, "submit PRs", "the submit hint is swapped out in stack mode")
}

func TestCtrlB_TriggersSubmitWhenEligible(t *testing.T) {
	m := stackModel(t, []SubmitNode{deselectedNew("feat/new"), newNode("a", StateOpen), newNode("b", StateDraft)}, true)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	assert.True(t, m.SubmitRequested(), "Ctrl+B requests submit when the STACK action is offered")
}

func TestCtrlB_NoOpWhenNotEligible(t *testing.T) {
	// A NEW branch is included (editing context) — Ctrl+B must not submit.
	m := stackModel(t, []SubmitNode{newNode("n", StateNew), newNode("a", StateOpen)}, true)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	assert.False(t, m.SubmitRequested(), "Ctrl+B does not submit when the STACK action is not offered")
}

func TestStackButton_MouseClickTriggersSubmit(t *testing.T) {
	m := stackModel(t, []SubmitNode{deselectedNew("feat/new"), newNode("a", StateOpen), newNode("b", StateDraft)}, true)
	y := m.panelTopRow() + m.leftVisibleHeight() + 1 // the button's reserved bottom row
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 2, Y: y})
	assert.True(t, updated.(Model).SubmitRequested(), "clicking the STACK button requests submit")
}
