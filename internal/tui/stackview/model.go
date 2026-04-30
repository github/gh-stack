package stackview

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/shared"
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

// toBranchNodeData converts a BranchNode to shared.BranchNodeData.
func toBranchNodeData(node BranchNode) shared.BranchNodeData {
	return shared.BranchNodeData{
		Ref:              node.Ref,
		IsCurrent:        node.IsCurrent,
		IsLinear:         node.IsLinear,
		BaseBranch:       node.BaseBranch,
		Commits:          node.Commits,
		FilesChanged:     node.FilesChanged,
		PR:               node.PR,
		Additions:        node.Additions,
		Deletions:        node.Deletions,
		CommitsExpanded:  node.CommitsExpanded,
		FilesExpanded:    node.FilesExpanded,
		ShowCurrentLabel: true,
	}
}

// handleMouseClick processes a mouse click at the given screen position.
func (m Model) handleMouseClick(screenX, screenY int) (tea.Model, tea.Cmd) {
	// If header is visible, clicks in the header area are ignored
	yOffset := 0
	if shared.ShouldShowHeader(m.width, m.height) {
		if screenY < shared.HeaderHeight {
			return m, nil
		}
		yOffset = shared.HeaderHeight
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
	return shared.NodeLineCount(toBranchNodeData(m.nodes[idx]))
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
	icon := shared.StatusIcon(toBranchNodeData(node))
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

	viewHeight := m.contentViewHeight()
	m.scrollOffset = shared.EnsureVisible(startLine, endLine, m.scrollOffset, viewHeight)
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
	if shared.ShouldShowHeader(m.width, m.height) {
		reserved = shared.HeaderHeight
	}
	h := m.height - reserved
	if h < 1 {
		h = 1
	}
	return h
}

// clampScroll ensures scrollOffset doesn't exceed content bounds.
func (m *Model) clampScroll() {
	m.scrollOffset = shared.ClampScroll(m.totalContentLines(), m.contentViewHeight(), m.scrollOffset)
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var out strings.Builder

	showHeader := shared.ShouldShowHeader(m.width, m.height)
	if showHeader {
		shared.RenderHeader(&out, m.buildHeaderConfig(), m.width, m.height)
	}

	var b strings.Builder

	// Render nodes in order (index 0 = top of stack, displayed first)
	prevWasMerged := false
	prevWasQueued := false
	for i := 0; i < len(m.nodes); i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		isQueued := m.nodes[i].Ref.IsQueued()
		if isMerged && !prevWasMerged && i > 0 {
			shared.RenderMergedSeparator(&b)
		} else if isQueued && !prevWasQueued && !prevWasMerged && i > 0 {
			shared.RenderQueuedSeparator(&b)
		}
		m.renderNode(&b, i)
		prevWasMerged = isMerged
		prevWasQueued = isQueued
	}

	// Trunk
	shared.RenderTrunk(&b, m.trunk.Branch)

	content := b.String()
	contentLines := strings.Split(content, "\n")

	// Apply scrolling
	reservedLines := 0
	if showHeader {
		reservedLines = shared.HeaderHeight
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

// buildHeaderConfig produces the header configuration for the stack view.
func (m Model) buildHeaderConfig() shared.HeaderConfig {
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

	branchIcon := "○"
	if mergedCount > 0 && mergedCount < branchCount {
		branchIcon = "◐"
	} else if branchCount > 0 && mergedCount == branchCount {
		branchIcon = "●"
	}

	return shared.HeaderConfig{
		ShowArt:  true,
		Title:    "GitHub Stacks",
		Subtitle: "v" + m.version,
		InfoLines: []shared.HeaderInfoLine{
			{Icon: "✓", Label: "Stack initialized"},
			{Icon: "◆", Label: "Base: " + m.trunk.Branch},
			{Icon: branchIcon, Label: branchInfo},
		},
		ShortcutColumns: 1,
		Shortcuts: []shared.ShortcutEntry{
			{Key: "↑", Desc: "up"},
			{Key: "↓", Desc: "down"},
			{Key: "c", Desc: "commits"},
			{Key: "f", Desc: "files"},
			{Key: "o", Desc: "open PR"},
			{Key: "↵", Desc: "checkout"},
			{Key: "q", Desc: "quit"},
		},
	}
}

// renderNode renders a single branch node.
func (m Model) renderNode(b *strings.Builder, idx int) {
	node := m.nodes[idx]
	isFocused := idx == m.cursor
	shared.RenderNode(b, toBranchNodeData(node), isFocused, m.width, nil)
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
