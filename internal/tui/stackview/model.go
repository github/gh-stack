package stackview

import (
	"fmt"
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
	Refresh       key.Binding
	Push          key.Binding
	Quit          key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.ToggleCommits, k.ToggleFiles, k.OpenPR, k.Checkout, k.Refresh, k.Push, k.Quit}
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
	Refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	Push: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "push"),
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

	// interactive enables in-place actions that keep the TUI open instead of
	// quitting (used by `gh stack watch`).
	interactive bool

	// checkoutFn performs an in-place checkout in interactive mode. When nil,
	// checkout falls back to the quit-then-checkout behavior.
	checkoutFn func(string) error

	// refreshFn re-syncs PR/stack state and returns fresh nodes (top-down
	// order). When nil, the refresh key is disabled.
	refreshFn func() ([]BranchNode, error)

	// pushFn pushes the whole stack. When nil, the push key is disabled.
	pushFn func() error

	// busy indicates an async action is in flight; action keys are ignored
	// until it completes.
	busy bool

	// confirmMode is true while waiting for a y/n answer before a mutating
	// action. confirmAction names the pending action (e.g. "push").
	confirmMode   bool
	confirmAction string

	// infoMsg holds a transient status/success message for the bottom line.
	infoMsg string

	// errMsg holds a transient error to display at the bottom of the view.
	errMsg string
}

// InteractiveActions bundles the in-place action callbacks used by `watch`.
// Any nil field disables the corresponding key.
type InteractiveActions struct {
	// Checkout switches to the given branch in place.
	Checkout func(string) error
	// Refresh re-syncs PR/stack state and returns fresh nodes in top-down order.
	Refresh func() ([]BranchNode, error)
	// Push pushes the whole stack to the remote.
	Push func() error
}

// checkoutResultMsg reports the outcome of an in-place checkout.
type checkoutResultMsg struct {
	branch string
	err    error
}

// refreshResultMsg reports the outcome of an in-place refresh.
type refreshResultMsg struct {
	nodes []BranchNode
	err   error
}

// actionResultMsg reports the outcome of a mutating action (e.g. push).
type actionResultMsg struct {
	action string
	err    error
}

// New creates a new stack view model.
func New(nodes []BranchNode, trunk stack.BranchRef, version string) Model {
	h := help.New()
	h.ShowAll = true

	// Cursor starts at the current branch, or first non-merged branch
	cursor := 0
	found := false
	for i, n := range nodes {
		if n.IsCurrent && !n.Ref.IsMerged() {
			cursor = i
			found = true
			break
		}
	}
	if !found {
		for i, n := range nodes {
			if !n.Ref.IsMerged() {
				cursor = i
				break
			}
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

// NewInteractive creates a stack view model for the interactive `watch` mode.
// Pressing enter on a branch invokes the Checkout action in place and keeps the
// TUI open, updating the current-branch marker instead of quitting. Refresh and
// Push actions, when supplied, are bound to the `r` and `p` keys.
func NewInteractive(nodes []BranchNode, trunk stack.BranchRef, version string, actions InteractiveActions) Model {
	m := New(nodes, trunk, version)
	m.interactive = true
	m.checkoutFn = actions.Checkout
	m.refreshFn = actions.Refresh
	m.pushFn = actions.Push
	return m
}

// CheckoutBranch returns the branch to checkout after the TUI exits, if any.
func (m Model) CheckoutBranch() string {
	return m.checkoutBranch
}

// checkoutCmd returns a command that performs an in-place checkout and reports
// the result back to the model.
func (m Model) checkoutCmd(branch string) tea.Cmd {
	fn := m.checkoutFn
	return func() tea.Msg {
		var err error
		if fn != nil {
			err = fn(branch)
		}
		return checkoutResultMsg{branch: branch, err: err}
	}
}

// refreshCmd returns a command that re-syncs stack state and reports fresh
// nodes back to the model.
func (m Model) refreshCmd() tea.Cmd {
	fn := m.refreshFn
	return func() tea.Msg {
		if fn == nil {
			return refreshResultMsg{}
		}
		nodes, err := fn()
		return refreshResultMsg{nodes: nodes, err: err}
	}
}

// pushCmd returns a command that pushes the stack and reports the result back
// to the model.
func (m Model) pushCmd() tea.Cmd {
	fn := m.pushFn
	return func() tea.Msg {
		var err error
		if fn != nil {
			err = fn()
		}
		return actionResultMsg{action: "push", err: err}
	}
}

// activeBranchCount returns the number of branches that are neither merged nor
// queued (i.e. the branches a push would actually update).
func (m Model) activeBranchCount() int {
	n := 0
	for _, node := range m.nodes {
		if !node.Ref.IsMerged() && !node.Ref.IsQueued() {
			n++
		}
	}
	return n
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
		// While awaiting a y/n confirmation, only confirm keys are handled.
		if m.confirmMode {
			switch msg.String() {
			case "y", "Y", "enter":
				action := m.confirmAction
				m.confirmMode = false
				m.confirmAction = ""
				if action == "push" {
					m.busy = true
					m.errMsg = ""
					m.infoMsg = "Pushing stack…"
					return m, m.pushCmd()
				}
				return m, nil
			case "n", "N", "esc", "ctrl+c":
				m.confirmMode = false
				m.confirmAction = ""
				m.infoMsg = "Canceled"
				m.errMsg = ""
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, keys.Up):
			m.moveCursor(-1)
			return m, nil

		case key.Matches(msg, keys.Down):
			m.moveCursor(1)
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
					shared.OpenBrowserInBackground(node.PR.URL)
				}
			}
			return m, nil

		case key.Matches(msg, keys.Checkout):
			if m.cursor >= 0 && m.cursor < len(m.nodes) {
				node := m.nodes[m.cursor]
				if !node.IsCurrent && !node.Ref.IsMerged() {
					if m.interactive {
						return m, m.checkoutCmd(node.Ref.Branch)
					}
					m.checkoutBranch = node.Ref.Branch
					return m, tea.Quit
				}
			}
			return m, nil

		case key.Matches(msg, keys.Refresh):
			if m.interactive && m.refreshFn != nil && !m.busy {
				m.busy = true
				m.errMsg = ""
				m.infoMsg = "Refreshing…"
				return m, m.refreshCmd()
			}
			return m, nil

		case key.Matches(msg, keys.Push):
			if m.interactive && m.pushFn != nil && !m.busy {
				m.confirmMode = true
				m.confirmAction = "push"
				m.errMsg = ""
				m.infoMsg = ""
			}
			return m, nil
		}

	case checkoutResultMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("failed to checkout %s: %v", msg.branch, msg.err)
			return m, nil
		}
		for i := range m.nodes {
			m.nodes[i].IsCurrent = m.nodes[i].Ref.Branch == msg.branch
		}
		m.errMsg = ""
		return m, nil

	case refreshResultMsg:
		m.busy = false
		if msg.err != nil {
			m.infoMsg = ""
			m.errMsg = fmt.Sprintf("refresh failed: %v", msg.err)
			return m, nil
		}
		m.nodes = msg.nodes
		if m.cursor >= len(m.nodes) {
			m.cursor = len(m.nodes) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.clampScroll()
		m.errMsg = ""
		m.infoMsg = "Refreshed"
		return m, nil

	case actionResultMsg:
		m.busy = false
		if msg.err != nil {
			m.infoMsg = ""
			m.errMsg = fmt.Sprintf("%s failed: %v", msg.action, msg.err)
			return m, nil
		}
		m.errMsg = ""
		m.infoMsg = fmt.Sprintf("%s complete", msg.action)
		return m, nil

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
	nodes := make([]shared.BranchNodeData, len(m.nodes))
	for i, n := range m.nodes {
		nodes[i] = toBranchNodeData(n)
	}

	result := shared.HandleClick(screenX, screenY, nodes, m.width, m.height, m.scrollOffset, shared.ShouldShowHeader(m.width, m.height), true)
	if result.NodeIndex < 0 {
		return m, nil
	}

	// Don't allow selecting merged branches.
	if m.nodes[result.NodeIndex].Ref.IsMerged() {
		return m, nil
	}

	m.cursor = result.NodeIndex

	if result.OpenURL != "" {
		shared.OpenBrowserInBackground(result.OpenURL)
	}
	if result.ToggleFiles {
		m.nodes[result.NodeIndex].FilesExpanded = !m.nodes[result.NodeIndex].FilesExpanded
		m.clampScroll()
	}
	if result.ToggleCommits {
		m.nodes[result.NodeIndex].CommitsExpanded = !m.nodes[result.NodeIndex].CommitsExpanded
		m.clampScroll()
	}

	return m, nil
}

// nodeLineCount returns how many rendered lines a node occupies.
func (m Model) nodeLineCount(idx int) int {
	return shared.NodeLineCount(toBranchNodeData(m.nodes[idx]))
}

// moveCursor moves the cursor by delta, skipping merged branches.
func (m *Model) moveCursor(delta int) {
	next := m.cursor + delta
	for next >= 0 && next < len(m.nodes) {
		if !m.nodes[next].Ref.IsMerged() {
			m.cursor = next
			m.ensureVisible()
			return
		}
		next += delta
	}
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

	// Apply scrolling
	reservedLines := 0
	if showHeader {
		reservedLines = shared.HeaderHeight
	}
	statusLine := m.statusLine()
	if statusLine != "" {
		reservedLines++ // reserve a line for the status message
	}
	viewHeight := m.height - reservedLines
	if viewHeight < 1 {
		viewHeight = 1
	}

	out.WriteString(shared.ApplyScrollToContent(content, m.scrollOffset, viewHeight))

	if statusLine != "" {
		out.WriteString("\n")
		out.WriteString(statusLine)
	}

	return out.String()
}

// statusLine returns the bottom status line: a pending confirmation prompt, a
// transient error, or a transient info message (in that priority order).
func (m Model) statusLine() string {
	if m.confirmMode && m.confirmAction == "push" {
		count := m.activeBranchCount()
		return fmt.Sprintf("Push %d %s to the remote? (y/n)", count, plural(count, "branch", "branches"))
	}
	if m.errMsg != "" {
		return shared.WarningIcon + " " + m.errMsg
	}
	if m.infoMsg != "" {
		return shared.OpenIcon + " " + m.infoMsg
	}
	return ""
}

// plural returns singular or plural based on n.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
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

	title := "View Stack"
	if m.interactive {
		title = "Watch Stack"
	}

	shortcuts := []shared.ShortcutEntry{
		{Key: "↑", Desc: "up"},
		{Key: "↓", Desc: "down"},
		{Key: "c", Desc: "commits"},
		{Key: "f", Desc: "files"},
		{Key: "o", Desc: "open PR"},
		{Key: "↵", Desc: "checkout"},
		{Key: "q", Desc: "quit"},
	}
	if m.interactive {
		shortcuts = []shared.ShortcutEntry{
			{Key: "↑", Desc: "up"},
			{Key: "↓", Desc: "down"},
			{Key: "c", Desc: "commits"},
			{Key: "f", Desc: "files"},
			{Key: "o", Desc: "open PR"},
			{Key: "↵", Desc: "switch (stays open)"},
			{Key: "r", Desc: "refresh"},
			{Key: "p", Desc: "push"},
			{Key: "q", Desc: "quit"},
		}
	}

	return shared.HeaderConfig{
		ShowArt:  true,
		Title:    title,
		Subtitle: "v" + m.version,
		InfoLines: []shared.HeaderInfoLine{
			{Icon: "✓", Label: "Stack initialized"},
			{Icon: "◆", Label: "Base: " + m.trunk.Branch},
			{Icon: branchIcon, Label: branchInfo},
		},
		ShortcutColumns: 1,
		Shortcuts:       shortcuts,
	}
}

// renderNode renders a single branch node.
func (m Model) renderNode(b *strings.Builder, idx int) {
	node := m.nodes[idx]
	isFocused := idx == m.cursor
	shared.RenderNode(b, toBranchNodeData(node), isFocused, m.width, nil)
}
