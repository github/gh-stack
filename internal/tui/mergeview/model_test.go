package mergeview

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseOptions() Options {
	return Options{
		PRs:               []PRItem{{Number: 1, Title: "a"}, {Number: 2, Title: "b"}, {Number: 3, Title: "c"}},
		BaseRef:           "main",
		AllowedMethods:    []string{"merge", "squash", "rebase"},
		DefaultMethod:     "squash",
		PreselectTopIndex: -1,
	}
}

func step(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func space() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeySpace} }

func TestNew_DefaultsSelectAll(t *testing.T) {
	m := New(baseOptions())
	assert.Equal(t, StepSelectPRs, m.step)
	assert.Equal(t, 2, m.topIndex, "all PRs selected by default")
	assert.Equal(t, "squash", m.method)
	assert.Equal(t, 1, m.methodCursor)
	assert.Equal(t, []int{1, 2, 3}, m.selectedNumbers())
	assert.Equal(t, 3, m.targetPR())
}

func TestNew_DefaultMethodFallback(t *testing.T) {
	opts := baseOptions()
	opts.DefaultMethod = "rebase"
	opts.AllowedMethods = []string{"merge", "rebase"} // squash disallowed
	m := New(opts)
	assert.Equal(t, "rebase", m.method)

	opts.DefaultMethod = "squash" // not allowed -> first allowed
	m = New(opts)
	assert.Equal(t, "merge", m.method)
}

func TestNew_PreselectSkipsSelectStep(t *testing.T) {
	opts := baseOptions()
	opts.PreselectTopIndex = 1
	m := New(opts)
	assert.Equal(t, StepMethod, m.step)
	assert.Equal(t, 1, m.topIndex)
	assert.Equal(t, 2, m.targetPR())
	assert.Equal(t, []int{1, 2}, m.selectedNumbers())
}

func TestSelect_CascadeToggle(t *testing.T) {
	m := New(baseOptions()) // topIndex=2, cursor=2

	// Toggling the top (index 2) lowers the water line to include only 1,2.
	m = step(m, space())
	assert.Equal(t, []int{1, 2}, m.selectedNumbers())

	// Move cursor to index 0 (down toward the bottom of the stack) and toggle:
	// deselects everything.
	m = step(m, keyType(tea.KeyDown))
	m = step(m, keyType(tea.KeyDown))
	assert.Equal(t, 0, m.cursor)
	m = step(m, space())
	assert.Empty(t, m.selectedNumbers())

	// Toggling index 0 again selects only the bottom PR.
	m = step(m, space())
	assert.Equal(t, []int{1}, m.selectedNumbers())
	assert.Equal(t, 1, m.targetPR())
}

func TestSelect_ArrowDirection(t *testing.T) {
	m := New(baseOptions()) // cursor starts at the top of the stack (index 2)
	assert.Equal(t, 2, m.cursor)

	// "up" moves toward the top of the stack and is clamped there.
	m = step(m, keyType(tea.KeyUp))
	assert.Equal(t, 2, m.cursor)

	// "down" moves toward the bottom of the stack (lower index).
	m = step(m, keyType(tea.KeyDown))
	assert.Equal(t, 1, m.cursor)
	m = step(m, keyType(tea.KeyUp))
	assert.Equal(t, 2, m.cursor)
}

func TestSelect_AdvanceRequiresSelection(t *testing.T) {
	m := New(baseOptions())

	// Move to the bottom PR and deselect everything.
	m = step(m, keyType(tea.KeyDown))
	m = step(m, keyType(tea.KeyDown))
	require.Equal(t, 0, m.cursor)
	m = step(m, space())
	require.Empty(t, m.selectedNumbers())

	// Enter should not advance with nothing selected.
	m = step(m, keyType(tea.KeyEnter))
	assert.Equal(t, StepSelectPRs, m.step)

	// Select the bottom PR, then advance.
	m = step(m, space())
	m = step(m, keyType(tea.KeyEnter))
	assert.Equal(t, StepMethod, m.step)
}

func TestMethod_BackWithShiftTab(t *testing.T) {
	m := New(baseOptions())
	m = step(m, keyType(tea.KeyEnter)) // -> method
	require.Equal(t, StepMethod, m.step)
	m = step(m, keyType(tea.KeyShiftTab))
	assert.Equal(t, StepSelectPRs, m.step)
}

func TestConfirm_BackWithShiftTab(t *testing.T) {
	m := New(baseOptions())
	m = step(m, keyType(tea.KeyTab)) // select -> method
	m = step(m, keyType(tea.KeyTab)) // method -> confirm
	require.Equal(t, StepConfirm, m.step)
	m = step(m, keyType(tea.KeyShiftTab))
	assert.Equal(t, StepMethod, m.step)
}

func TestMethod_SelectAndAdvance(t *testing.T) {
	m := New(baseOptions())
	m = step(m, keyType(tea.KeyEnter)) // -> method step
	require.Equal(t, StepMethod, m.step)
	assert.Equal(t, 1, m.methodCursor) // squash preselected

	m = step(m, keyType(tea.KeyDown)) // rebase
	m = step(m, keyType(tea.KeyEnter))
	assert.Equal(t, StepConfirm, m.step)
	assert.Equal(t, "rebase", m.method)
}

func TestMethod_EscCancels(t *testing.T) {
	m := New(baseOptions())
	m = step(m, keyType(tea.KeyEnter))
	require.Equal(t, StepMethod, m.step)
	m = step(m, keyType(tea.KeyEsc))
	assert.True(t, m.Outcome().Cancelled)
}

func TestConfirm_SubmitAlreadyMerged(t *testing.T) {
	m := New(baseOptions())
	// submitDoneMsg is handled regardless of step; simulate an already-merged
	// response.
	m = step(m, submitDoneMsg{status: MergeStatus{Merged: true, Message: "Pull request is already merged.", SHA: "abc1234"}})
	out := m.Outcome()
	assert.True(t, out.Merged)
	assert.False(t, out.Failed)
	assert.Equal(t, []int{1, 2, 3}, out.MergedPRs)
}

func TestProgress_QueuedThenFailed(t *testing.T) {
	m := New(baseOptions())
	m.step = StepProgress
	m.submitted = true

	m = step(m, submitDoneMsg{status: MergeStatus{Queued: true, UUID: "u1", Message: "enqueued"}})
	assert.False(t, m.done(), "still in progress after queued submit")

	m = step(m, pollDoneMsg{status: MergeStatus{Queued: false, Merged: false, Message: "Merge conflict."}})
	out := m.Outcome()
	assert.True(t, out.Failed)
	assert.False(t, out.Merged)
	assert.Equal(t, "Merge conflict.", out.Message)
}

func TestProgress_QueuedThenMerged(t *testing.T) {
	m := New(baseOptions())
	m.step = StepProgress
	m.submitted = true

	m = step(m, submitDoneMsg{status: MergeStatus{Queued: true, UUID: "u1"}})
	m = step(m, pollDoneMsg{status: MergeStatus{Merged: true, SHA: "deadbee"}})
	out := m.Outcome()
	assert.True(t, out.Merged)
	assert.Equal(t, []int{1, 2, 3}, out.MergedPRs)
}

func TestSubmit_NotMergeable(t *testing.T) {
	m := New(baseOptions())
	m.step = StepProgress
	m.submitted = true
	m = step(m, submitDoneMsg{status: MergeStatus{Queued: false, Merged: false, Message: "Pull request is closed."}})
	out := m.Outcome()
	assert.True(t, out.Failed)
	assert.Equal(t, "Pull request is closed.", out.Message)
}

func TestSubmit_TransportError(t *testing.T) {
	m := New(baseOptions())
	m.step = StepProgress
	m = step(m, submitDoneMsg{err: errors.New("boom")})
	out := m.Outcome()
	assert.Error(t, out.Err)
	assert.True(t, out.Failed)
}

func TestCancel_FromSelect(t *testing.T) {
	m := New(baseOptions())
	m = step(m, keyType(tea.KeyEsc))
	out := m.Outcome()
	assert.True(t, out.Cancelled)
	assert.False(t, out.Merged)
}

func TestView_RendersBannerAndSteps(t *testing.T) {
	m := New(baseOptions())
	sel := m.View()
	assert.Contains(t, sel, "Merge stack")
	assert.Contains(t, sel, "Select PRs")
	assert.Contains(t, sel, "Select Merge Method")
	assert.Contains(t, sel, "Confirm")
	assert.Contains(t, sel, "Select how far up the stack")

	m = step(m, keyType(tea.KeyTab))
	assert.Contains(t, m.View(), "Choose a merge method")

	m = step(m, keyType(tea.KeyTab))
	confirm := m.View()
	assert.Contains(t, confirm, "Merge 3 PRs")
	assert.Contains(t, confirm, "#1, #2, #3")
}

func TestPRCount(t *testing.T) {
	assert.Equal(t, "1 PR", prCount(1))
	assert.Equal(t, "2 PRs", prCount(2))
	assert.Equal(t, "5 PRs", prCount(5))
}
