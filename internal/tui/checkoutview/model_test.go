package checkoutview

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/github/gh-stack/internal/stack"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// drive applies a sequence of messages to the model and returns the result.
func drive(m Model, msgs ...tea.Msg) Model {
	var tm tea.Model = m
	for _, msg := range msgs {
		tm, _ = tm.Update(msg)
	}
	return tm.(Model)
}

func sampleRows() []StackRow {
	now := time.Now()
	return []StackRow{
		{Number: 0, Type: TypeLocal, BottomBranch: "wip-a", TopBranch: "wip-b", Base: "main",
			Status: StatusCounts{Unpushed: 2}, LocalStack: &stack.Stack{Number: 0}},
		{Number: 55, Type: TypeRemote, BottomBranch: "ref-1", TopBranch: "ref-3", Base: "main",
			Status: StatusCounts{Open: 3}, HasCreated: true, Created: now.Add(-time.Hour)},
		{Number: 42, Type: TypeLocal, BottomBranch: "api-1", TopBranch: "api-3", Base: "trunk",
			Status: StatusCounts{Merged: 1, Open: 1, Unpushed: 1}, HasCreated: true, Created: now.Add(-72 * time.Hour),
			LocalStack: &stack.Stack{Number: 42}},
	}
}

func sized(m Model) Model {
	return drive(m, tea.WindowSizeMsg{Width: 100, Height: 20})
}

func TestNew_InitialState(t *testing.T) {
	m := New(sampleRows())
	assert.Equal(t, tabAll, m.tab)
	assert.Len(t, m.filtered, 3)
	assert.Equal(t, 0, m.cursor)
	assert.False(t, m.searching)
}

func TestTabFiltering(t *testing.T) {
	m := sized(New(sampleRows()))

	// All -> Local (one Right).
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, tabLocal, m.tab)
	require.Len(t, m.filtered, 2)
	for _, r := range m.filtered {
		assert.Equal(t, TypeLocal, r.Type)
	}

	// Local -> Remote (another Right).
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, tabRemote, m.tab)
	require.Len(t, m.filtered, 1)
	assert.Equal(t, 55, m.filtered[0].Number)

	// Remote -> wraps back to All (another Right).
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, tabAll, m.tab)
	assert.Len(t, m.filtered, 3)

	// Left wraps backwards to Remote.
	m = drive(m, tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, tabRemote, m.tab)
}

func TestTabSwitchResetsCursor(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 2, m.cursor)
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, 0, m.cursor, "switching tabs resets the cursor")
}

func TestSearchFiltering(t *testing.T) {
	m := sized(New(sampleRows()))

	m = drive(m, runeKey("/"))
	assert.True(t, m.searching)

	m = drive(m, runeKey("a"), runeKey("p"), runeKey("i"))
	assert.Equal(t, "api", m.query)
	require.Len(t, m.filtered, 1)
	assert.Equal(t, 42, m.filtered[0].Number)

	// Backspace widens the results again.
	m = drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "ap", m.query)

	// Esc clears the search and restores all rows.
	m = drive(m, tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, m.searching)
	assert.Equal(t, "", m.query)
	assert.Len(t, m.filtered, 3)
}

func TestSearchMatchesNumberAndType(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, runeKey("/"), runeKey("5"), runeKey("5"))
	require.Len(t, m.filtered, 1)
	assert.Equal(t, 55, m.filtered[0].Number)

	m = drive(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = drive(m, runeKey("/"), runeKey("r"), runeKey("e"), runeKey("m"))
	require.Len(t, m.filtered, 1)
	assert.Equal(t, TypeRemote, m.filtered[0].Type)
}

func TestSearchMatchesMidStackBranch(t *testing.T) {
	rows := []StackRow{
		{Number: 7, Type: TypeLocal, BottomBranch: "feat/bottom", TopBranch: "feat/top",
			Branches: []string{"feat/bottom", "feat/middle-xyz", "feat/top"}, Base: "main"},
		{Number: 8, Type: TypeRemote, BottomBranch: "other/a", TopBranch: "other/b",
			Branches: []string{"other/a", "other/b"}, Base: "main"},
	}
	m := sized(New(rows))
	m = drive(m, runeKey("/"))
	for _, r := range "middle" {
		m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	require.Len(t, m.filtered, 1, "a mid-stack branch name should match")
	assert.Equal(t, 7, m.filtered[0].Number)
	// The matched mid-stack branch is not shown in the Branches column.
	assert.NotContains(t, stripANSI(m.View()), "middle-xyz")
}

func TestCursorNavigationClamps(t *testing.T) {
	m := sized(New(sampleRows()))
	// Up at the top stays at 0.
	m = drive(m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, m.cursor)
	// Down past the end clamps to the last row.
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 2, m.cursor)
}

func TestEnterSelectsLocalRow(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, tea.KeyMsg{Type: tea.KeyEnter})

	row, ok := m.Result()
	require.True(t, ok)
	assert.Equal(t, 0, row.Number)
	assert.Equal(t, TypeLocal, row.Type)
	require.NotNil(t, row.LocalStack)
	assert.False(t, m.Cancelled())
}

func TestEnterSelectsRemoteRow(t *testing.T) {
	m := sized(New(sampleRows()))
	// Move to the remote-only stack (#55, index 1).
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})

	row, ok := m.Result()
	require.True(t, ok)
	assert.Equal(t, 55, row.Number)
	assert.Equal(t, TypeRemote, row.Type)
	assert.Nil(t, row.LocalStack)
}

func TestCancelKeys(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyEsc},
		runeKey("q"),
		tea.KeyMsg{Type: tea.KeyCtrlC},
	} {
		m := sized(New(sampleRows()))
		m = drive(m, msg)
		_, ok := m.Result()
		assert.False(t, ok, "no selection after cancel")
		assert.True(t, m.Cancelled())
	}
}

func TestEnterWithNoMatchesDoesNothing(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, runeKey("/"), runeKey("z"), runeKey("z"), runeKey("z"))
	require.Empty(t, m.filtered)

	m = drive(m, tea.KeyMsg{Type: tea.KeyEnter})
	_, ok := m.Result()
	assert.False(t, ok, "Enter selects nothing when the list is empty")
	assert.False(t, m.Cancelled())
}

func TestQTypesIntoSearchInsteadOfQuitting(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, runeKey("/"), runeKey("q"))
	assert.True(t, m.searching)
	assert.Equal(t, "q", m.query)
	assert.False(t, m.Cancelled())
}

func TestView_RendersColumnsAndRows(t *testing.T) {
	m := sized(New(sampleRows()))
	out := stripANSI(m.View())

	assert.Contains(t, out, "Checkout a stack")
	for _, col := range []string{"#", "Branches", "Base", "Status", "Type", "Created"} {
		assert.Contains(t, out, col)
	}
	assert.Contains(t, out, "wip-a...wip-b")
	assert.Contains(t, out, "Remote")
	assert.Contains(t, out, "1h ago")
	assert.Contains(t, out, "/ search")
}

func TestView_SearchFooterAndPrompt(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, runeKey("/"), runeKey("a"))
	out := stripANSI(m.View())
	assert.Contains(t, out, "/ a")
	assert.Contains(t, out, "clear search")
}

func TestView_EmptyStateMessage(t *testing.T) {
	m := sized(New(nil))
	out := stripANSI(m.View())
	assert.Contains(t, out, "No stacks match.")
}

func TestView_NarrowTerminalDoesNotPanic(t *testing.T) {
	m := New(sampleRows())
	for _, size := range []tea.WindowSizeMsg{
		{Width: 20, Height: 6},
		{Width: 1, Height: 1},
		{Width: 40, Height: 3},
	} {
		mm := drive(m, size)
		assert.NotPanics(t, func() {
			_ = mm.View()
		})
	}
}

func TestView_ZeroSizeReturnsEmpty(t *testing.T) {
	m := New(sampleRows())
	assert.Equal(t, "", m.View())
}

func manyRows(n int) []StackRow {
	rows := make([]StackRow, n)
	for i := 0; i < n; i++ {
		rows[i] = StackRow{
			Number: 1000 + i, Type: TypeRemote,
			BottomBranch: fmt.Sprintf("b%d", i), TopBranch: fmt.Sprintf("t%d", i),
			Base: "main", Status: StatusCounts{Open: 2},
		}
	}
	return rows
}

func TestView_InlineHeightIsBounded(t *testing.T) {
	// A long list on a tall terminal must not take over the screen: it shows at
	// most maxVisibleRows rows plus a little chrome, and offers a scroll hint.
	m := drive(New(manyRows(30)), tea.WindowSizeMsg{Width: 90, Height: 50})
	lines := len(strings.Split(m.View(), "\n"))
	assert.LessOrEqual(t, lines, maxVisibleRows+6, "picker must not fill a tall terminal")
	assert.GreaterOrEqual(t, lines, maxVisibleRows, "shows up to maxVisibleRows rows")
	assert.Contains(t, stripANSI(m.View()), "of 30", "shows a scroll position indicator")
}

func TestView_ShrinksToShortTerminal(t *testing.T) {
	m := drive(New(manyRows(30)), tea.WindowSizeMsg{Width: 90, Height: 12})
	lines := len(strings.Split(m.View(), "\n"))
	assert.LessOrEqual(t, lines, 12, "must fit within a short terminal")
	assert.Less(t, lines, maxVisibleRows+6)
}

func TestView_ClearsOnExit(t *testing.T) {
	selected := drive(sized(New(sampleRows())), tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "", selected.View(), "inline picker clears itself after selection")

	cancelled := drive(sized(New(sampleRows())), tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, "", cancelled.View(), "inline picker clears itself after cancel")
}

func TestView_NoScrollIndicatorWhenAllFit(t *testing.T) {
	m := sized(New(sampleRows())) // 3 rows on a 20-line terminal
	assert.NotContains(t, stripANSI(m.View()), " of ")
}

func TestStatusBar_EmptyAndColors(t *testing.T) {
	assert.Equal(t, "—", stripANSI(statusBar(StatusCounts{}, false)))
	bar := stripANSI(statusBar(StatusCounts{Merged: 1, Open: 2}, false))
	assert.Equal(t, strings.Repeat(statusBox, 3), bar)
}

func TestStatusBar_CapsLargeStacks(t *testing.T) {
	// A huge stack is summarized into at most maxStatusBoxes cells.
	bar := stripANSI(statusBar(StatusCounts{Merged: 40, Open: 40, Closed: 20}, false))
	assert.Equal(t, maxStatusBoxes, len([]rune(bar)))
}
