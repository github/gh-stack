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

// Model is the Bubbletea model for the interactive stack view.
type Model struct {
	nodes  []BranchNode
	trunk  stack.BranchRef
	cursor int // index into nodes (displayed top-down, so 0 = top of stack)
	help   help.Model
	width  int
	height int

	// scrollOffset tracks vertical scroll position for tall stacks.
	scrollOffset int

	// checkoutBranch is set when the user wants to checkout a branch after quitting.
	checkoutBranch string
}

// New creates a new stack view model.
func New(nodes []BranchNode, trunk stack.BranchRef) Model {
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
		nodes:  nodes,
		trunk:  trunk,
		cursor: cursor,
		help:   h,
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
				m.ensureVisible()
			}
			return m, nil

		case key.Matches(msg, keys.ToggleFiles):
			if m.cursor >= 0 && m.cursor < len(m.nodes) {
				m.nodes[m.cursor].FilesExpanded = !m.nodes[m.cursor].FilesExpanded
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
	// Map screen Y to content line, accounting for scroll offset
	contentLine := screenY + m.scrollOffset

	// Walk through rendered lines to find which node was clicked.
	// Account for the merged separator line that may appear between nodes.
	line := 0
	prevWasMerged := false
	for i := 0; i < len(m.nodes); i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		if isMerged && !prevWasMerged && i > 0 {
			line++ // separator line
		}
		prevWasMerged = isMerged

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
				}
			}

			// Click on commits toggle line → toggle expansion
			if len(m.nodes[i].Commits) > 0 {
				commitToggleLine := nodeStart + m.commitToggleLineOffset(i)
				if contentLine == commitToggleLine {
					m.nodes[i].CommitsExpanded = !m.nodes[i].CommitsExpanded
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
	if node.PR != nil && (node.PR.Merged || !node.IsLinear || node.PR.Number != 0) {
		icon := m.statusIcon(node)
		if icon != "" {
			col += 2 // icon (1 visible char) + space
		}
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
	for i := 0; i < m.cursor; i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		if isMerged && !prevWasMerged && i > 0 {
			startLine++ // separator line
		}
		prevWasMerged = isMerged
		startLine += m.nodeLineCount(i)
	}
	// Check if the cursor node itself is preceded by a separator
	if m.cursor < len(m.nodes) {
		isMerged := m.nodes[m.cursor].Ref.IsMerged()
		if isMerged && !prevWasMerged && m.cursor > 0 {
			startLine++
		}
	}
	endLine := startLine + m.nodeLineCount(m.cursor)

	// Available content height (reserve 2 for help bar)
	viewHeight := m.height - 2
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

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var b strings.Builder

	// Render nodes in order (index 0 = top of stack, displayed first)
	prevWasMerged := false
	for i := 0; i < len(m.nodes); i++ {
		isMerged := m.nodes[i].Ref.IsMerged()
		if isMerged && !prevWasMerged && i > 0 {
			b.WriteString(connectorStyle.Render("────") + dimStyle.Render(" merged ") + connectorStyle.Render("─────") + "\n")
		}
		m.renderNode(&b, i)
		prevWasMerged = isMerged
	}

	// Trunk
	b.WriteString(connectorStyle.Render("└ "))
	b.WriteString(trunkStyle.Render(m.trunk.Branch))
	b.WriteString("\n")

	content := b.String()
	contentLines := strings.Split(content, "\n")

	// Apply scrolling
	viewHeight := m.height - 2 // reserve for help bar
	if viewHeight < 1 {
		viewHeight = 1
	}

	start := m.scrollOffset
	if start > len(contentLines) {
		start = len(contentLines)
	}
	end := start + viewHeight
	if end > len(contentLines) {
		end = len(contentLines)
	}

	visibleContent := strings.Join(contentLines[start:end], "\n")

	// Add help bar at the bottom
	helpView := m.help.View(keys)

	return visibleContent + "\n" + helpView
}

// renderNode renders a single branch node.
func (m Model) renderNode(b *strings.Builder, idx int) {
	node := m.nodes[idx]
	isFocused := idx == m.cursor

	// Determine connector character and style
	connector := "│"
	connStyle := connectorStyle
	isMerged := node.PR != nil && node.PR.Merged
	if !node.IsLinear && !isMerged {
		connector = "┊"
		connStyle = connectorDashedStyle
	}
	// Override style when this node is focused
	if isFocused {
		if node.IsCurrent {
			connStyle = connectorCurrentStyle
		} else if isMerged {
			connStyle = connectorMergedStyle
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
	} else if node.PR != nil && node.PR.Merged {
		b.WriteString(normalBranchStyle.Render(branchName))
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
	if node.PR != nil && node.PR.Merged {
		return mergedIcon
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
