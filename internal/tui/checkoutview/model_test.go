package checkoutview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/github/gh-stack/internal/stack"
)

func stripANSI(s string) string { return ansi.Strip(s) }

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

func testNumberLinks(rows []StackRow) map[int]string {
	links := make(map[int]string)
	for _, row := range rows {
		if row.Number > 0 {
			links[row.Number] = fmt.Sprintf("\x1b[4m\x1b]8;;https://github.com/o/r/stack/%d\x1b\\%d\x1b]8;;\x1b\\\x1b[24m", row.Number, row.Number)
		}
	}
	return links
}

func TestNumberLinksPreserveRows(t *testing.T) {
	profile := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(profile)

	rows := sampleRows()
	m := sized(New(rows).WithNumberLinks(testNumberLinks(rows)))
	plain := sized(New(rows))
	assert.Equal(t, stripANSI(plain.View()), stripANSI(m.View()))
	assert.Contains(t, m.View(), testNumberLinks(rows)[55])
	assert.NotContains(t, m.View(), "/stack/0")

	// URL text is presentation only, not a search term.
	searchURL := drive(m, runeKey("/"), runeKey("https"))
	assert.Empty(t, searchURL.filtered)
	m = drive(m, runeKey("/"), runeKey("42"))
	require.Len(t, m.filtered, 1)
	assert.Contains(t, m.View(), testNumberLinks(rows)[42])
	assert.NotContains(t, m.View(), "/stack/55")
	m = drive(m, tea.KeyMsg{Type: tea.KeyEnter})
	row, selected := m.Result()
	require.True(t, selected)
	assert.Equal(t, rows[2], row)
}

func TestNumberLinksScrollingAndClipping(t *testing.T) {
	profile := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(profile)

	rows := manyRows(20)
	rows[0].BottomBranch = "界面"
	links := testNumberLinks(rows)
	m := New(rows).WithNumberLinks(links)
	for _, width := range []int{1, 2, 3, 6, 12, 24, 40, 60, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			mm := drive(m, tea.WindowSizeMsg{Width: width, Height: 11})
			for range 15 {
				mm = drive(mm, tea.KeyMsg{Type: tea.KeyDown})
			}
			output := mm.View()
			for _, line := range strings.Split(output, "\n") {
				assert.LessOrEqual(t, lipgloss.Width(line), width)
				assert.Equal(t, strings.Count(line, "\x1b]8;;https://"), strings.Count(line, "\x1b]8;;\x1b\\"), "each line closes its hyperlinks")
			}
			assert.NotContains(t, output, "/stack/1000", "scrolled-out rows have no links")
			if width == 100 {
				assert.Contains(t, output, links[1015])
			}
			assert.NotContains(t, stripANSI(output), "https://")
		})
	}
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
	tests := []struct {
		name string
		up   tea.KeyMsg
		down tea.KeyMsg
	}{
		{name: "arrow keys", up: tea.KeyMsg{Type: tea.KeyUp}, down: tea.KeyMsg{Type: tea.KeyDown}},
		{name: "vim keys", up: runeKey("k"), down: runeKey("j")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := sized(New(sampleRows()))
			m = drive(m, tt.up)
			assert.Equal(t, 0, m.cursor)

			m = drive(m, tt.down, tt.down, tt.down)
			assert.Equal(t, 2, m.cursor)
		})
	}
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

func TestVimKeysTypeIntoSearchInsteadOfNavigating(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, runeKey("/"), runeKey("j"), runeKey("k"))

	assert.True(t, m.searching)
	assert.Equal(t, "jk", m.query)
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
	assert.Contains(t, out, "↓↑/jk")
}

func TestView_SearchFooterAndPrompt(t *testing.T) {
	m := sized(New(sampleRows()))
	m = drive(m, runeKey("/"), runeKey("a"))
	out := stripANSI(m.View())
	assert.Contains(t, out, "/ a")
	assert.Contains(t, out, "clear search")
	assert.Contains(t, out, "↓↑ navigate")
	assert.NotContains(t, out, "↓↑/jk")
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

func TestView_NoLineExceedsWidth(t *testing.T) {
	// Every rendered line must fit within the terminal width so nothing wraps
	// (which would break the inline picker's bounded height).
	m := New(sampleRows())
	for _, w := range []int{100, 60, 40, 24, 12} {
		mm := drive(m, tea.WindowSizeMsg{Width: w, Height: 20})
		for _, ln := range strings.Split(mm.View(), "\n") {
			assert.LessOrEqualf(t, lipgloss.Width(ln), w, "line wider than %d: %q", w, stripANSI(ln))
		}
	}
}

func TestSearchTogglingKeepsCursorVisible(t *testing.T) {
	// On a short terminal, entering search shrinks the body; the selected row
	// must not scroll out of view (Enter would otherwise select a hidden row).
	m := drive(New(manyRows(20)), tea.WindowSizeMsg{Width: 90, Height: 11})
	for m.cursor < m.bodyHeight()-1 {
		m = drive(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m = drive(m, runeKey("/"))
	require.True(t, m.searching)
	assert.GreaterOrEqual(t, m.cursor, m.scrollOffset)
	assert.Less(t, m.cursor, m.scrollOffset+m.bodyHeight(), "cursor stays visible after entering search")
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

func TestBodyHeight_UsesAvailableTerminalSpace(t *testing.T) {
	tests := []struct {
		name      string
		rows      int
		height    int
		searching bool
		want      int
	}{
		{name: "unknown terminal size uses fallback", rows: 30, want: initialVisibleRows},
		{name: "small list stays compact", rows: 3, height: 50, want: 3},
		{name: "tall terminal expands past fallback", rows: 100, height: 50, want: 44},
		{name: "short terminal shrinks", rows: 30, height: 12, want: 6},
		{name: "search reserves another line", rows: 100, height: 50, searching: true, want: 43},
		{name: "empty list reserves one row", height: 50, want: 1},
		{name: "tiny terminal reserves one row", rows: 30, height: 3, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(manyRows(tt.rows))
			m.height = tt.height
			m.searching = tt.searching
			assert.Equal(t, tt.want, m.bodyHeight())
		})
	}
}

func TestView_ExpandsToAvailableTerminalHeight(t *testing.T) {
	const terminalHeight = 50
	m := drive(New(manyRows(100)), tea.WindowSizeMsg{Width: 90, Height: terminalHeight})

	assert.Greater(t, m.bodyHeight(), initialVisibleRows)
	assert.Equal(t, terminalHeight-m.chromeHeight(), m.bodyHeight())
	lines := len(strings.Split(m.View(), "\n"))
	assert.Equal(t, terminalHeight-1, lines, "picker keeps one safety row free")
	assert.Contains(t, stripANSI(m.View()), "of 100", "shows a scroll position indicator")
}

func TestView_ShrinksToShortTerminal(t *testing.T) {
	m := drive(New(manyRows(30)), tea.WindowSizeMsg{Width: 90, Height: 12})
	lines := len(strings.Split(m.View(), "\n"))
	assert.Equal(t, 12-m.chromeHeight(), m.bodyHeight())
	assert.LessOrEqual(t, lines, 12, "must fit within a short terminal")
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
