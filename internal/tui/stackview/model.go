package stackview

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/stack"
)

// keyMap defines the key bindings for the stack view.
type keyMap struct {
	Up            key.Binding
	Down          key.Binding
	ToggleCommits key.Binding
	ToggleFiles   key.Binding
	OpenPR        key.Binding
	Checkout      key.Binding
	Quit          key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.ToggleCommits, k.ToggleFiles, k.OpenPR, k.Checkout, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "down"),
	),
	ToggleCommits: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "commits"),
	),
	ToggleFiles: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "files"),
	),
	OpenPR: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open PR"),
	),
	Checkout: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "checkout"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

// headerHeight is the total number of lines the header box occupies (top border + 10 art lines + bottom border).
const headerHeight = 12

// minHeightForHeader is the minimum terminal height to show the header.
const minHeightForHeader = 25

// minWidthForShortcuts is the minimum terminal width to show keyboard shortcuts in the header.
// Below this, the header is shown without the right-side shortcuts column.
const minWidthForShortcuts = 65

// minWidthForHeader is the minimum terminal width to show the header at all.
const minWidthForHeader = 50

// artLines contains the braille ASCII art displayed in the header.
var artLines = [10]string{
	"⠀⠀⠀⠀⠀⠀⣀⣤⣤⣤⣤⣤⣤⣀⠀⠀⠀⠀⠀⠀",
	"⠀⠀⠀⣠⣴⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣶⣄⠀⠀⠀",
	"⠀⢀⣼⣿⣿⠛⠛⠿⠿⠿⠿⠿⠿⠛⠛⣿⣿⣷⡀⠀",
	"⠀⣾⣿⣿⣿⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣿⣿⣿⣷⡀",
	"⢸⣿⣿⣿⡇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⣿⣿⣿⡇",
	"⢸⣿⣿⣿⡇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⣿⣿⣿⡇",
	"⠘⣿⣿⣿⣿⣦⡀⠀⠀⠀⠀⠀⠀⢀⣤⣿⣿⣿⣿⠇",
	"⠀⠹⣿⣦⡈⠻⢿⠟⠀⠀⠀⠀⢻⣿⣿⣿⣿⣿⠏⠀",
	"⠀⠀⠈⠻⣷⣤⣀⡀⠀⠀⠀⠀⢸⣿⣿⣿⡿⠃⠀⠀",
	"⠀⠀⠀⠀⠈⠙⠻⠇⠀⠀⠀⠀⠸⠟⠛⠁⠀⠀⠀⠀",
}

// artDisplayWidth is the visual column width of each art line.
const artDisplayWidth = 20

// Model is the Bubbletea model for the interactive stack view.
type Model struct {
	nodes   []BranchNode
	trunk   stack.BranchRef
	version string
	cursor  int // index into nodes (displayed top-down, so 0 = top of stack)
	help    help.Model
	width   int
	height  int

	// scrollOffset tracks vertical scroll position for tall stacks.
	scrollOffset int

	// checkoutBranch is set when the user wants to checkout a branch after quitting.
	checkoutBranch string
}

// New creates a new stack view model.
func New(nodes []BranchNode, trunk stack.BranchRef, version string) Model {
	h := help.New()
	h.ShowAll = true

	// Cursor starts at the current branch, or top of stack
	cursor := 0
	for i, n := range nodes {
		if n.IsCurrent {
			cursor = i
			break
		}
	}

	return Model{
		nodes:   nodes,
		trunk:   trunk,
		version: version,
		cursor:  cursor,
		help:    h,
	}
}

// CheckoutBranch returns the branch to checkout after the TUI exits, if any.
func (m Model) CheckoutBranch() string {
	return m.checkoutBranch
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.Width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
				m.ensureVisible()
			}
			return m, nil

		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.nodes)-1 {
				m.cursor++
				m.ensureVisible()
			}
			return m, nil

		case key.Matches(msg, keys.ToggleCommits):
			if m.cursor >= 0 && m.cursor < len(m.nodes) {
				m.nodes[m.cursor].CommitsExpanded = !m.nodes[m.cursor].CommitsExpanded
				m.clampScroll()
				m.ensureVisible()
			}
			return m, nil

		case key.Matches(msg, keys.ToggleFiles):
			if m.cursor >= 0 && m.cursor < len(m.nodes) {
				m.nodes[m.cursor].FilesExpanded = !m.nodes[m.cursor].FilesExpanded
				m.clampScroll()
				m.ensureVisible()
			}
			return m, nil

		case key.Matches(msg, keys.OpenPR):
			if m.cursor >= 0 && m.cursor < len(m.nodes) {
				node := m.nodes[m.cursor]
				if node.PR != nil && node.PR.URL != "" {
					openBrowserInBackground(node.PR.URL)
				}
			}
			return m, nil

		case key.Matches(msg, keys.Checkout):
			if m.cursor >= 0 && m.cursor < len(m.nodes) {
				node := m.nodes[m.cursor]
				if !node.IsCurrent {
					m.checkoutBranch = node.Ref.Branch
					return m, tea.Quit
				}
			}
			return m, nil
		}

	case tea.MouseMsg:
		switch msg.Action {
		case tea.MouseActionPress:
			if msg.Button == tea.MouseButtonLeft {
				return m.handleMouseClick(msg.X, msg.Y)
			}
			if msg.Button == tea.MouseButtonWheelUp {
				if m.scrollOffset > 0 {
					m.scrollOffset--
				}
				return m, nil
			}
			if msg.Button == tea.MouseButtonWheelDown {
				m.scrollOffset++
				m.clampScroll()
				return m, nil
			}
		}
	}

	return m, nil
}

// openBrowserInBackground launches the system browser for the given URL.
func openBrowserInBackground(url string) {
	cmd := browserCmd(url)
	_ = cmd.Start()
}

// handleMouseClick processes a mouse click at the given screen position.
func (m Model) handleMouseClick(screenX, screenY int) (tea.Model, tea.Cmd) {
	// If header is visible, clicks in the header area are ignored
	yOffset := 0
	if m.showHeader() {
		if screenY < headerHeight {
			return m, nil
		}
		yOffset = headerHeight
	}

	// Map screen Y to content line, accounting for scroll offset and header
	contentLine := (screenY - yOffset) + m.scrollOffset

	// Walk through rendered lines to find which node was clicked.
	// Account for the merged/queued separator lines that may appear between nodes.
	line := 0
	prevWasMerged := false
	prevWasQueued := false
	for i := 0; i < len(m.nodes); i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		isQueued := m.nodes[i].Ref.IsQueued()
		if isMerged && !prevWasMerged && i > 0 {
			line++ // separator line
		} else if isQueued && !prevWasQueued && !prevWasMerged && i > 0 {
			line++ // separator line
		}
		prevWasMerged = isMerged
		prevWasQueued = isQueued

		nodeStart := line
		nodeLines := m.nodeLineCount(i)

		if contentLine >= nodeStart && contentLine < nodeStart+nodeLines {
			m.cursor = i

			// Click on PR header line — only open browser if clicking the PR number
			if contentLine == nodeStart && m.nodes[i].PR != nil && m.nodes[i].PR.URL != "" {
				prStartX, prEndX := m.prLabelColumns(i)
				if screenX >= prStartX && screenX < prEndX {
					openBrowserInBackground(m.nodes[i].PR.URL)
				}
			}

			// Click on files toggle line → toggle expansion
			if len(m.nodes[i].FilesChanged) > 0 {
				filesToggleLine := nodeStart + m.filesToggleLineOffset(i)
				if contentLine == filesToggleLine {
					m.nodes[i].FilesExpanded = !m.nodes[i].FilesExpanded
					m.clampScroll()
				}
			}

			// Click on commits toggle line → toggle expansion
			if len(m.nodes[i].Commits) > 0 {
				commitToggleLine := nodeStart + m.commitToggleLineOffset(i)
				if contentLine == commitToggleLine {
					m.nodes[i].CommitsExpanded = !m.nodes[i].CommitsExpanded
					m.clampScroll()
				}
			}

			return m, nil
		}
		line += nodeLines
	}

	return m, nil
}

// nodeLineCount returns how many rendered lines a node occupies.
func (m Model) nodeLineCount(idx int) int {
	node := m.nodes[idx]
	lines := 1 // header line (PR line or branch line)

	if node.PR != nil {
		lines++ // branch + diff stats line (below PR header)
	}

	if len(node.FilesChanged) > 0 {
		lines++ // files toggle line
		if node.FilesExpanded {
			lines += len(node.FilesChanged)
		}
	}

	if len(node.Commits) > 0 {
		lines++ // commits toggle line
		if node.CommitsExpanded {
			lines += len(node.Commits)
		}
	}

	lines++ // connector/spacer line
	return lines
}

// commitToggleLineOffset returns the offset from node start to the commits toggle line.
func (m Model) commitToggleLineOffset(idx int) int {
	node := m.nodes[idx]
	offset := 1 // after header
	if node.PR != nil {
		offset++ // branch + diff line
	}
	if len(node.FilesChanged) > 0 {
		offset++ // files toggle line
		if node.FilesExpanded {
			offset += len(node.FilesChanged)
		}
	}
	return offset
}

// filesToggleLineOffset returns the offset from node start to the files toggle line.
func (m Model) filesToggleLineOffset(idx int) int {
	node := m.nodes[idx]
	offset := 1 // after header
	if node.PR != nil {
		offset++ // branch + diff line
	}
	return offset
}

// prLabelColumns returns the start and end X columns of the PR number label
// (e.g. "#123") on the PR header line, for click hit-testing.
func (m Model) prLabelColumns(idx int) (int, int) {
	node := m.nodes[idx]
	// Layout: "├ " (2) + optional status icon + " " (2) + "#N..."
	col := 2 // bullet + space
	icon := m.statusIcon(node)
	if icon != "" {
		col += 2 // icon (1 visible char) + space
	}
	prLabel := fmt.Sprintf("#%d", node.PR.Number)
	return col, col + len(prLabel)
}

// ensureVisible adjusts scroll offset so the cursor is visible.
func (m *Model) ensureVisible() {
	if m.height == 0 {
		return
	}

	// Calculate the line range for the cursor node, accounting for separator lines
	startLine := 0
	prevWasMerged := false
	prevWasQueued := false
	for i := 0; i < m.cursor; i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		isQueued := m.nodes[i].Ref.IsQueued()
		if isMerged && !prevWasMerged && i > 0 {
			startLine++ // separator line
		} else if isQueued && !prevWasQueued && !prevWasMerged && i > 0 {
			startLine++ // separator line
		}
		prevWasMerged = isMerged
		prevWasQueued = isQueued
		startLine += m.nodeLineCount(i)
	}
	// Check if the cursor node itself is preceded by a separator
	if m.cursor < len(m.nodes) {
		isMerged := m.nodes[m.cursor].Ref.IsMerged()
		isQueued := m.nodes[m.cursor].Ref.IsQueued()
		if isMerged && !prevWasMerged && m.cursor > 0 {
			startLine++
		} else if isQueued && !prevWasQueued && !prevWasMerged && m.cursor > 0 {
			startLine++
		}
	}
	endLine := startLine + m.nodeLineCount(m.cursor)

	// Available content height (reserve space for header or help bar)
	viewHeight := m.contentViewHeight()
	if viewHeight < 1 {
		viewHeight = 1
	}

	if startLine < m.scrollOffset {
		m.scrollOffset = startLine
	}
	if endLine > m.scrollOffset+viewHeight {
		m.scrollOffset = endLine - viewHeight
	}
}

// showHeader returns true if the terminal is large enough for the header.
func (m Model) showHeader() bool {
	return m.height >= minHeightForHeader && m.width >= minWidthForHeader
}

// showShortcuts returns true if the terminal is wide enough for the shortcuts column in the header.
func (m Model) showShortcuts() bool {
	return m.width >= minWidthForShortcuts
}

// totalContentLines returns the total number of rendered content lines (excluding header).
func (m Model) totalContentLines() int {
	lines := 0
	prevWasMerged := false
	prevWasQueued := false
	for i := 0; i < len(m.nodes); i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		isQueued := m.nodes[i].Ref.IsQueued()
		if isMerged && !prevWasMerged && i > 0 {
			lines++ // separator line
		} else if isQueued && !prevWasQueued && !prevWasMerged && i > 0 {
			lines++ // separator line
		}
		prevWasMerged = isMerged
		prevWasQueued = isQueued
		lines += m.nodeLineCount(i)
	}
	lines++ // trunk line
	return lines
}

// contentViewHeight returns the number of lines available for stack content.
func (m Model) contentViewHeight() int {
	reserved := 0
	if m.showHeader() {
		reserved = headerHeight
	}
	h := m.height - reserved
	if h < 1 {
		h = 1
	}
	return h
}

// clampScroll ensures scrollOffset doesn't exceed content bounds.
func (m *Model) clampScroll() {
	maxScroll := m.totalContentLines() - m.contentViewHeight()
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var out strings.Builder

	showHeader := m.showHeader()
	if showHeader {
		m.renderHeader(&out)
	}

	var b strings.Builder

	// Render nodes in order (index 0 = top of stack, displayed first)
	prevWasMerged := false
	prevWasQueued := false
	for i := 0; i < len(m.nodes); i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		isQueued := m.nodes[i].Ref.IsQueued()
		if isMerged && !prevWasMerged && i > 0 {
			b.WriteString(connectorStyle.Render("────") + dimStyle.Render(" merged ") + connectorStyle.Render("─────") + "\n")
		} else if isQueued && !prevWasQueued && !prevWasMerged && i > 0 {
			b.WriteString(connectorStyle.Render("────") + dimStyle.Render(" queued ") + connectorStyle.Render("─────") + "\n")
		}
		m.renderNode(&b, i)
		prevWasMerged = isMerged
		prevWasQueued = isQueued
	}

	// Trunk
	b.WriteString(connectorStyle.Render("└ "))
	b.WriteString(trunkStyle.Render(m.trunk.Branch))
	b.WriteString("\n")

	content := b.String()
	contentLines := strings.Split(content, "\n")

	// Apply scrolling
	reservedLines := 0
	if showHeader {
		reservedLines = headerHeight
	}
	viewHeight := m.height - reservedLines
	if viewHeight < 1 {
		viewHeight = 1
	}

	// Clamp scroll offset so we can't scroll past content
	maxScroll := len(contentLines) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	start := m.scrollOffset
	if start > maxScroll {
		start = maxScroll
	}
	end := start + viewHeight
	if end > len(contentLines) {
		end = len(contentLines)
	}

	visibleContent := strings.Join(contentLines[start:end], "\n")
	out.WriteString(visibleContent)

	return out.String()
}

// renderHeader renders the full-width stylized header box with ASCII art, stack info, and keyboard shortcuts.
func (m Model) renderHeader(b *strings.Builder) {
	w := m.width
	if w < 2 {
		return
	}
	innerWidth := w - 2 // subtract left and right border chars

	// Build info lines (placed to the right of art on specific rows)
	mergedCount := 0
	queuedCount := 0
	for _, n := range m.nodes {
		if n.Ref.IsMerged() {
			mergedCount++
		}
		if n.Ref.IsQueued() {
			queuedCount++
		}
	}
	branchCount := len(m.nodes)
	branchInfo := fmt.Sprintf("%d branches", branchCount)
	if branchCount == 1 {
		branchInfo = "1 branch"
	}
	if mergedCount > 0 {
		branchInfo += fmt.Sprintf(" (%d merged)", mergedCount)
	}
	if queuedCount > 0 {
		branchInfo += fmt.Sprintf(" (%d queued)", queuedCount)
	}

	// Branch progress icon: ○ none merged, ◐ some merged, ● all merged
	branchIcon := "○"
	if mergedCount > 0 && mergedCount < branchCount {
		branchIcon = "◐"
	} else if branchCount > 0 && mergedCount == branchCount {
		branchIcon = "●"
	}

	// Info text mapped to art row indices (0-based)
	infoByRow := map[int]string{
		2: headerTitleStyle.Render("GitHub Stacks"),
		3: headerInfoLabelStyle.Render("v" + m.version),
		5: headerInfoStyle.Render("✓") + headerInfoLabelStyle.Render(" Stack initialized"),
		6: headerInfoStyle.Render("◆") + headerInfoLabelStyle.Render(" Base: "+m.trunk.Branch),
		7: headerInfoStyle.Render(branchIcon) + headerInfoLabelStyle.Render(" "+branchInfo),
	}

	showShortcuts := m.showShortcuts()

	// Build shortcut lines (rendered content + visual widths)
	type shortcutLine struct {
		text     string
		visWidth int
	}
	var shortcuts []shortcutLine
	maxShortcutWidth := 0
	rightColWidth := 0

	if showShortcuts {
		shortcuts = []shortcutLine{
			{headerShortcutKey.Render("↑") + headerShortcutDesc.Render(" up  ") +
				headerShortcutKey.Render("↓") + headerShortcutDesc.Render(" down"), 0},
			{headerShortcutKey.Render("c") + headerShortcutDesc.Render(" commits"), 0},
			{headerShortcutKey.Render("f") + headerShortcutDesc.Render(" files"), 0},
			{headerShortcutKey.Render("o") + headerShortcutDesc.Render(" open PR"), 0},
			{headerShortcutKey.Render("↵") + headerShortcutDesc.Render(" checkout"), 0},
			{headerShortcutKey.Render("q") + headerShortcutDesc.Render(" quit"), 0},
		}
		for i := range shortcuts {
			shortcuts[i].visWidth = lipgloss.Width(shortcuts[i].text)
			if shortcuts[i].visWidth > maxShortcutWidth {
				maxShortcutWidth = shortcuts[i].visWidth
			}
		}
		rightColWidth = maxShortcutWidth + 2
	}

	// Left content base: 1 (margin) + artDisplayWidth
	leftContentBase := 1 + artDisplayWidth

	// Vertically center shortcuts within the 10 content rows
	scStartRow := 0
	if len(shortcuts) > 0 {
		scStartRow = (10 - len(shortcuts)) / 2
	}

	// Top border
	b.WriteString(headerBorderStyle.Render("┌" + strings.Repeat("─", innerWidth) + "┐"))
	b.WriteString("\n")

	// Content rows
	gap := "  " // gap between art and info text
	for i := 0; i < 10; i++ {
		art := artLines[i]

		// Build info segment
		infoText := ""
		infoVisualLen := 0
		if info, ok := infoByRow[i]; ok {
			infoText = gap + info
			infoVisualLen = 2 + lipgloss.Width(info)
		}

		leftUsed := leftContentBase + infoVisualLen

		if showShortcuts {
			// Two-column layout: left (art+info) | right (shortcuts)
			shortcutCol := innerWidth - rightColWidth
			midPad := shortcutCol - leftUsed
			if midPad < 0 {
				midPad = 0
			}

			scIdx := i - scStartRow
			shortcutRendered := ""
			scVisWidth := 0
			if scIdx >= 0 && scIdx < len(shortcuts) {
				shortcutRendered = shortcuts[scIdx].text
				scVisWidth = shortcuts[scIdx].visWidth
			}
			scTrailingPad := rightColWidth - scVisWidth
			if scTrailingPad < 0 {
				scTrailingPad = 0
			}

			b.WriteString(headerBorderStyle.Render("│"))
			b.WriteString(" ")
			b.WriteString(art)
			b.WriteString(infoText)
			b.WriteString(strings.Repeat(" ", midPad))
			b.WriteString(shortcutRendered)
			b.WriteString(strings.Repeat(" ", scTrailingPad))
			b.WriteString(headerBorderStyle.Render("│"))
		} else {
			// Single-column layout: art + info, padded to fill
			trailingPad := innerWidth - leftUsed
			if trailingPad < 0 {
				trailingPad = 0
			}

			b.WriteString(headerBorderStyle.Render("│"))
			b.WriteString(" ")
			b.WriteString(art)
			b.WriteString(infoText)
			b.WriteString(strings.Repeat(" ", trailingPad))
			b.WriteString(headerBorderStyle.Render("│"))
		}
		b.WriteString("\n")
	}

	// Bottom border
	b.WriteString(headerBorderStyle.Render("└" + strings.Repeat("─", innerWidth) + "┘"))
	b.WriteString("\n")
}

// renderNode renders a single branch node.
func (m Model) renderNode(b *strings.Builder, idx int) {
	node := m.nodes[idx]
	isFocused := idx == m.cursor

	// Determine connector character and style
	connector := "│"
	connStyle := connectorStyle
	isMerged := node.Ref.IsMerged()
	isQueued := node.Ref.IsQueued()
	if !node.IsLinear && !isMerged && !isQueued {
		connector = "┊"
		connStyle = connectorDashedStyle
	}
	// Override style when this node is focused
	if isFocused {
		if node.IsCurrent {
			connStyle = connectorCurrentStyle
		} else if isMerged {
			connStyle = connectorMergedStyle
		} else if isQueued {
			connStyle = connectorQueuedStyle
		} else {
			connStyle = connectorFocusedStyle
		}
	}

	// Render header: either PR line + branch line, or just branch line
	if node.PR != nil {
		m.renderPRHeader(b, node, isFocused, connStyle)
		m.renderBranchLine(b, node, connector, connStyle)
	} else {
		m.renderBranchHeader(b, node, isFocused, connStyle)
	}

	// Files changed toggle + expanded file list
	if len(node.FilesChanged) > 0 {
		m.renderFiles(b, node, connector, connStyle)
	}

	// Commits toggle + expanded commits
	if len(node.Commits) > 0 {
		m.renderCommits(b, node, connector, connStyle)
	}

	// Connector/spacer
	b.WriteString(connStyle.Render(connector))
	b.WriteString("\n")
}

// renderPRHeader renders the top line when a PR exists: bullet + status icon + PR number + state.
func (m Model) renderPRHeader(b *strings.Builder, node BranchNode, isFocused bool, connStyle lipgloss.Style) {
	bullet := "├"
	if isFocused {
		bullet = "▶"
	}

	b.WriteString(connStyle.Render(bullet + " "))

	statusIcon := m.statusIcon(node)

	if statusIcon != "" {
		b.WriteString(statusIcon + " ")
	}

	// PR number + state label
	pr := node.PR
	prLabel := fmt.Sprintf("#%d", pr.Number)
	stateLabel := ""
	style := prOpenStyle
	switch {
	case pr.Merged:
		stateLabel = " MERGED"
		style = prMergedStyle
	case pr.IsQueued:
		stateLabel = " QUEUED"
		style = prQueuedStyle
	case pr.State == "CLOSED":
		stateLabel = " CLOSED"
		style = prClosedStyle
	case pr.IsDraft:
		stateLabel = " DRAFT"
		style = prDraftStyle
	default:
		stateLabel = " OPEN"
	}
	b.WriteString(style.Underline(true).Render(prLabel) + style.Render(stateLabel))

	b.WriteString("\n")
}

// renderBranchLine renders the branch name + diff stats below the PR header.
func (m Model) renderBranchLine(b *strings.Builder, node BranchNode, connector string, connStyle lipgloss.Style) {
	b.WriteString(connStyle.Render(connector))
	b.WriteString(" ")

	branchName := node.Ref.Branch
	if node.IsCurrent {
		b.WriteString(currentBranchStyle.Render(branchName + " (current)"))
	} else {
		b.WriteString(normalBranchStyle.Render(branchName))
	}

	m.renderDiffStats(b, node)
	b.WriteString("\n")
}

// renderBranchHeader renders the header line when there is no PR: bullet + branch name + diff stats.
func (m Model) renderBranchHeader(b *strings.Builder, node BranchNode, isFocused bool, connStyle lipgloss.Style) {
	bullet := "├"
	if isFocused {
		bullet = "▶"
	}

	b.WriteString(connStyle.Render(bullet + " "))

	// Status indicator
	statusIcon := m.statusIcon(node)
	if statusIcon != "" {
		b.WriteString(statusIcon + " ")
	}

	// Branch name
	branchName := node.Ref.Branch
	if node.IsCurrent {
		b.WriteString(currentBranchStyle.Render(branchName + " (current)"))
	} else {
		b.WriteString(normalBranchStyle.Render(branchName))
	}

	m.renderDiffStats(b, node)
	b.WriteString("\n")
}

// renderDiffStats appends +N -N diff stats to the current line if available.
func (m Model) renderDiffStats(b *strings.Builder, node BranchNode) {
	if node.Additions > 0 || node.Deletions > 0 {
		b.WriteString("  ")
		b.WriteString(additionsStyle.Render(fmt.Sprintf("+%d", node.Additions)))
		b.WriteString(" ")
		b.WriteString(deletionsStyle.Render(fmt.Sprintf("-%d", node.Deletions)))
	}
}

// statusIcon returns the appropriate status icon for a branch.
func (m Model) statusIcon(node BranchNode) string {
	if node.Ref.IsMerged() {
		return mergedIcon
	}
	if node.Ref.IsQueued() {
		return queuedIcon
	}
	if !node.IsLinear {
		return warningIcon
	}
	if node.PR != nil && node.PR.Number != 0 {
		return openIcon
	}
	return ""
}

// renderFiles renders the files changed toggle and optionally the expanded file list.
func (m Model) renderFiles(b *strings.Builder, node BranchNode, connector string, connStyle lipgloss.Style) {
	b.WriteString(connStyle.Render(connector))
	b.WriteString("  ")

	icon := collapsedIcon
	if node.FilesExpanded {
		icon = expandedIcon
	}
	fileLabel := "files changed"
	if len(node.FilesChanged) == 1 {
		fileLabel = "file changed"
	}
	b.WriteString(commitTimeStyle.Render(fmt.Sprintf("%s %d %s", icon, len(node.FilesChanged), fileLabel)))
	b.WriteString("\n")

	if !node.FilesExpanded {
		return
	}

	for _, f := range node.FilesChanged {
		b.WriteString(connStyle.Render(connector))
		b.WriteString("    ")

		path := f.Path
		maxLen := m.width - 30
		if maxLen < 20 {
			maxLen = 20
		}
		if len(path) > maxLen {
			path = "…" + path[len(path)-maxLen+1:]
		}
		b.WriteString(normalBranchStyle.Render(path))
		b.WriteString("  ")
		b.WriteString(additionsStyle.Render(fmt.Sprintf("+%d", f.Additions)))
		b.WriteString(" ")
		b.WriteString(deletionsStyle.Render(fmt.Sprintf("-%d", f.Deletions)))
		b.WriteString("\n")
	}
}

// renderCommits renders the commits toggle and optionally the expanded commit list.
func (m Model) renderCommits(b *strings.Builder, node BranchNode, connector string, connStyle lipgloss.Style) {
	b.WriteString(connStyle.Render(connector))
	b.WriteString("  ")

	icon := collapsedIcon
	if node.CommitsExpanded {
		icon = expandedIcon
	}
	commitLabel := "commits"
	if len(node.Commits) == 1 {
		commitLabel = "commit"
	}
	b.WriteString(commitTimeStyle.Render(fmt.Sprintf("%s %d %s", icon, len(node.Commits), commitLabel)))
	b.WriteString("\n")

	if !node.CommitsExpanded {
		return
	}

	for _, c := range node.Commits {
		b.WriteString(connStyle.Render(connector))
		b.WriteString("    ")

		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		b.WriteString(commitSHAStyle.Render(sha))
		b.WriteString(" ")

		subject := c.Subject
		maxLen := m.width - 35
		if maxLen < 20 {
			maxLen = 20
		}
		if len(subject) > maxLen {
			subject = subject[:maxLen-1] + "…"
		}
		b.WriteString(commitSubjectStyle.Render(subject))
		b.WriteString("  ")
		b.WriteString(commitTimeStyle.Render(timeAgo(c.Time)))
		b.WriteString("\n")
	}
}

// timeAgo returns a human-readable time-ago string.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		secs := int(d.Seconds())
		if secs == 1 {
			return "1 second ago"
		}
		return fmt.Sprintf("%d seconds ago", secs)
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months <= 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}

// browserCmd returns an exec.Cmd to open a URL in the default browser.
func browserCmd(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("cmd", "/c", "start", url)
	default:
		return exec.Command("xdg-open", url)
	}
}
