package modifyview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/shared"
	"github.com/github/gh-stack/internal/tui/stackview"
)

// modifyKeyMap defines key bindings for the modify view.
type modifyKeyMap struct {
	Up            key.Binding
	Down          key.Binding
	MoveUp        key.Binding
	MoveDown      key.Binding
	Drop          key.Binding
	FoldDown      key.Binding
	FoldUp        key.Binding
	Rename        key.Binding
	InsertBelow   key.Binding
	InsertAbove   key.Binding
	Undo          key.Binding
	ToggleCommits key.Binding
	ToggleFiles   key.Binding
	Apply         key.Binding
	Help          key.Binding
	Quit          key.Binding
}

func (k modifyKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Drop, k.FoldDown, k.InsertBelow, k.Rename, k.ToggleCommits, k.ToggleFiles, k.Apply, k.Help, k.Quit}
}

func (k modifyKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

var modifyKeys = modifyKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	MoveUp: key.NewBinding(
		key.WithKeys("K", "shift+up"),
		key.WithHelp("shift+↑", "move up"),
	),
	MoveDown: key.NewBinding(
		key.WithKeys("J", "shift+down"),
		key.WithHelp("shift+↓", "move down"),
	),
	Drop: key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "drop"),
	),
	FoldDown: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "fold down"),
	),
	FoldUp: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "fold up"),
	),
	Rename: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "rename"),
	),
	InsertBelow: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "insert below"),
	),
	InsertAbove: key.NewBinding(
		key.WithKeys("I"),
		key.WithHelp("I", "insert above"),
	),
	Undo: key.NewBinding(
		key.WithKeys("z"),
		key.WithHelp("z", "undo"),
	),
	ToggleCommits: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "commits"),
	),
	ToggleFiles: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "files"),
	),
	Apply: key.NewBinding(
		key.WithKeys("ctrl+s"),
		key.WithHelp("ctrl+s", "apply"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

// Model is the Bubble Tea model for the interactive modify view.
type Model struct {
	nodes        []ModifyBranchNode
	trunk        stack.BranchRef
	version      string
	cursor       int
	width        int
	height       int
	scrollOffset int

	// Undo stack
	actionStack []StagedAction

	// Rename mode
	renameMode     bool
	renameInput    textinput.Model
	renameOriginal string // original branch name shown as label

	// Insert mode
	insertMode      bool
	insertDirection ActionType // ActionInsertBelow or ActionInsertAbove
	insertInput     textinput.Model

	// Help overlay
	showHelp bool

	// Status/transient message
	statusMessage string
	statusIsError bool

	// Apply result
	applied        bool
	cancelled      bool
	applyRequested bool
	result         *ApplyResult
	conflict       *ConflictInfo

	// Conflict choice
	conflictChoice string // "editor" or "unwind"
}

// New creates a new modify view model.
func New(nodes []ModifyBranchNode, trunk stack.BranchRef, version string) Model {
	ti := textinput.New()
	ti.CharLimit = 100

	ii := textinput.New()
	ii.CharLimit = 100

	// Default cursor to the current active branch, or first non-merged branch
	cursor := 0
	found := false
	for i, n := range nodes {
		if n.IsCurrent {
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
		nodes:       nodes,
		trunk:       trunk,
		version:     version,
		cursor:      cursor,
		renameInput: ti,
		insertInput: ii,
	}
}

// --- Getters for the command layer ---

func (m Model) Applied() bool                 { return m.applied }
func (m Model) Cancelled() bool               { return m.cancelled }
func (m Model) ApplyRequested() bool          { return m.applyRequested }
func (m Model) Result() *ApplyResult          { return m.result }
func (m Model) Conflict() *ConflictInfo       { return m.conflict }
func (m Model) ConflictChoice() string        { return m.conflictChoice }
func (m Model) StagedActions() []StagedAction { return m.actionStack }

// Nodes returns the current node state for the apply engine.
func (m Model) Nodes() []ModifyBranchNode { return m.nodes }

// SetResult is called by the command layer after apply completes.
func (m *Model) SetResult(r *ApplyResult) { m.result = r; m.applied = true }

// SetConflict is called by the command layer when a rebase conflict occurs.
func (m *Model) SetConflict(c *ConflictInfo) { m.conflict = c }

// --- Bubble Tea interface ---

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Clear transient status on any key press
		m.statusMessage = ""
		m.statusIsError = false

		if m.showHelp {
			return m.updateHelp(msg)
		}
		if m.renameMode {
			return m.updateRename(msg)
		}
		if m.insertMode {
			return m.updateInsert(msg)
		}
		return m.updateNormal(msg)

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

// updateHelp handles keys while the help overlay is visible.
func (m Model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, modifyKeys.Help) || msg.Type == tea.KeyEscape {
		m.showHelp = false
	}
	return m, nil
}

// updateRename handles keys while in rename mode.
func (m Model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		newName := strings.TrimSpace(m.renameInput.Value())
		if newName == "" {
			m.renameMode = false
			return m, nil
		}
		node := &m.nodes[m.cursor]
		oldName := node.Ref.Branch

		if newName == oldName {
			m.renameMode = false
			return m, nil
		}

		// Validate: git ref name rules
		if err := git.ValidateRefName(newName); err != nil {
			m.statusMessage = fmt.Sprintf("Invalid branch name: %s", err)
			m.statusIsError = true
			return m, nil
		}

		// Validate: not already used by another local branch
		if git.BranchExists(newName) {
			m.statusMessage = fmt.Sprintf("Branch %q already exists locally", newName)
			m.statusIsError = true
			return m, nil
		}

		// Validate: not already used in this stack (by another node)
		for j, other := range m.nodes {
			if j == m.cursor {
				continue
			}
			checkName := other.Ref.Branch
			if other.PendingAction != nil && other.PendingAction.Type == ActionRename {
				checkName = other.PendingAction.NewName
			}
			if checkName == newName {
				m.statusMessage = fmt.Sprintf("Branch %q already used in this stack", newName)
				m.statusIsError = true
				return m, nil
			}
		}

		// For inserted nodes, update the insert action's name directly
		// rather than creating a separate rename action.
		if node.IsInserted {
			oldInsertName := node.Ref.Branch
			node.Ref.Branch = newName
			node.PendingAction.NewName = newName
			// Update the matching undo stack entry
			for i := len(m.actionStack) - 1; i >= 0; i-- {
				a := &m.actionStack[i]
				if (a.Type == ActionInsertBelow || a.Type == ActionInsertAbove) && a.BranchName == oldInsertName {
					a.BranchName = newName
					break
				}
			}
			m.renameMode = false
			return m, nil
		}

		// Record undo action
		m.actionStack = append(m.actionStack, StagedAction{
			Type:         ActionRename,
			BranchName:   oldName,
			OriginalName: oldName,
			NewName:      newName,
		})

		node.PendingAction = &PendingAction{
			Type:    ActionRename,
			NewName: newName,
		}
		m.renameMode = false
		return m, nil

	case tea.KeyEscape:
		m.renameMode = false
		return m, nil

	default:
		var cmd tea.Cmd
		m.renameInput, cmd = m.renameInput.Update(msg)
		return m, cmd
	}
}

// updateInsert handles keys while in insert mode (typing a new branch name).
func (m Model) updateInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		newName := strings.TrimSpace(m.insertInput.Value())
		if newName == "" {
			m.insertMode = false
			return m, nil
		}

		// Validate: git ref name rules
		if err := git.ValidateRefName(newName); err != nil {
			m.statusMessage = fmt.Sprintf("Invalid branch name: %s", err)
			m.statusIsError = true
			return m, nil
		}

		// Validate: not already used by another local branch
		if git.BranchExists(newName) {
			m.statusMessage = fmt.Sprintf("Branch %q already exists locally", newName)
			m.statusIsError = true
			return m, nil
		}

		// Validate: not already used in this stack (by another node)
		for _, other := range m.nodes {
			checkName := other.Ref.Branch
			if other.PendingAction != nil && other.PendingAction.Type == ActionRename {
				checkName = other.PendingAction.NewName
			}
			if other.PendingAction != nil && (other.PendingAction.Type == ActionInsertBelow || other.PendingAction.Type == ActionInsertAbove) {
				checkName = other.PendingAction.NewName
			}
			if checkName == newName {
				m.statusMessage = fmt.Sprintf("Branch %q already used in this stack", newName)
				m.statusIsError = true
				return m, nil
			}
		}

		// Determine insertion position
		insertIdx := m.cursor
		if m.insertDirection == ActionInsertBelow {
			insertIdx = m.cursor + 1
		}
		// For InsertAbove, insertIdx stays at m.cursor (insert before cursor)

		// Create the new node
		newNode := ModifyBranchNode{
			BranchNode: stackview.BranchNode{
				Ref:      stack.BranchRef{Branch: newName},
				IsLinear: true,
			},
			PendingAction: &PendingAction{
				Type:    m.insertDirection,
				NewName: newName,
			},
			OriginalPosition: -1, // sentinel: this node has no original position
			IsInserted:       true,
		}

		// Insert the node into the slice
		m.nodes = append(m.nodes, ModifyBranchNode{})
		copy(m.nodes[insertIdx+1:], m.nodes[insertIdx:])
		m.nodes[insertIdx] = newNode

		// Record undo action
		m.actionStack = append(m.actionStack, StagedAction{
			Type:       m.insertDirection,
			BranchName: newName,
		})

		// Move cursor to the newly inserted node
		m.cursor = insertIdx
		m.insertMode = false
		m.ensureVisible()
		return m, nil

	case tea.KeyEscape:
		m.insertMode = false
		return m, nil

	default:
		var cmd tea.Cmd
		m.insertInput, cmd = m.insertInput.Update(msg)
		return m, cmd
	}
}
func (m Model) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, modifyKeys.Quit):
		m.cancelled = true
		return m, tea.Quit

	case key.Matches(msg, modifyKeys.Up):
		m.moveCursor(-1)
		return m, nil

	case key.Matches(msg, modifyKeys.Down):
		m.moveCursor(1)
		return m, nil

	case key.Matches(msg, modifyKeys.MoveUp):
		if m.currentMode() == modeStructure {
			m.statusMessage = "Cannot reorder while drops, folds, inserts, or renames are staged — undo them first"
			m.statusIsError = true
			return m, nil
		}
		m.moveNode(-1)
		return m, nil

	case key.Matches(msg, modifyKeys.MoveDown):
		if m.currentMode() == modeStructure {
			m.statusMessage = "Cannot reorder while drops, folds, inserts, or renames are staged — undo them first"
			m.statusIsError = true
			return m, nil
		}
		m.moveNode(1)
		return m, nil

	case key.Matches(msg, modifyKeys.Drop):
		if m.currentMode() == modeReorder {
			m.statusMessage = "Cannot drop while branches are reordered — undo moves first"
			m.statusIsError = true
			return m, nil
		}
		m.toggleDrop()
		return m, nil

	case key.Matches(msg, modifyKeys.FoldDown):
		if m.currentMode() == modeReorder {
			m.statusMessage = "Cannot fold while branches are reordered — undo moves first"
			m.statusIsError = true
			return m, nil
		}
		m.fold(ActionFoldDown)
		return m, nil

	case key.Matches(msg, modifyKeys.FoldUp):
		if m.currentMode() == modeReorder {
			m.statusMessage = "Cannot fold while branches are reordered — undo moves first"
			m.statusIsError = true
			return m, nil
		}
		m.fold(ActionFoldUp)
		return m, nil

	case key.Matches(msg, modifyKeys.Rename):
		if m.currentMode() == modeReorder {
			m.statusMessage = "Cannot rename while branches are reordered — undo moves first"
			m.statusIsError = true
			return m, nil
		}
		m.startRename()
		return m, nil

	case key.Matches(msg, modifyKeys.InsertBelow):
		if m.currentMode() == modeReorder {
			m.statusMessage = "Cannot insert while branches are reordered — undo moves first"
			m.statusIsError = true
			return m, nil
		}
		m.startInsert(ActionInsertBelow)
		return m, nil

	case key.Matches(msg, modifyKeys.InsertAbove):
		if m.currentMode() == modeReorder {
			m.statusMessage = "Cannot insert while branches are reordered — undo moves first"
			m.statusIsError = true
			return m, nil
		}
		m.startInsert(ActionInsertAbove)
		return m, nil

	case key.Matches(msg, modifyKeys.Undo):
		m.undoLast()
		return m, nil

	case key.Matches(msg, modifyKeys.ToggleCommits):
		if m.cursor >= 0 && m.cursor < len(m.nodes) {
			m.nodes[m.cursor].CommitsExpanded = !m.nodes[m.cursor].CommitsExpanded
			m.clampScroll()
			m.ensureVisible()
		}
		return m, nil

	case key.Matches(msg, modifyKeys.ToggleFiles):
		if m.cursor >= 0 && m.cursor < len(m.nodes) {
			m.nodes[m.cursor].FilesExpanded = !m.nodes[m.cursor].FilesExpanded
			m.clampScroll()
			m.ensureVisible()
		}
		return m, nil

	case key.Matches(msg, modifyKeys.Apply):
		return m.tryApply()

	case key.Matches(msg, modifyKeys.Help):
		m.showHelp = true
		return m, nil
	}

	return m, nil
}

// --- Action handlers ---

// actionMode represents which exclusive mode the user is in.
type actionMode int

const (
	modeNone      actionMode = iota // no actions yet
	modeReorder                     // user has moved/reordered branches
	modeStructure                   // user has dropped, folded, or renamed branches
)

// currentMode returns the exclusive action mode based on pending actions.
func (m *Model) currentMode() actionMode {
	hasReorder := false
	hasStructure := false

	for _, n := range m.nodes {
		if n.PendingAction != nil {
			switch n.PendingAction.Type {
			case ActionDrop, ActionFoldDown, ActionFoldUp, ActionRename, ActionInsertBelow, ActionInsertAbove:
				hasStructure = true
			}
		}
	}

	// Position change without explicit action = reorder.
	// Skip inserted nodes — they don't have an original position and
	// their presence shifts indices of other nodes.
	effectiveIdx := 0
	for _, n := range m.nodes {
		if n.IsInserted {
			continue
		}
		if !n.Ref.IsMerged() && n.OriginalPosition != effectiveIdx && n.PendingAction == nil {
			hasReorder = true
			break
		}
		effectiveIdx++
	}

	if hasReorder {
		return modeReorder
	}
	if hasStructure {
		return modeStructure
	}
	return modeNone
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

// moveNode swaps the current node with an adjacent non-removed, non-merged node.
func (m *Model) moveNode(delta int) {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	cur := &m.nodes[m.cursor]
	if cur.Ref.IsMerged() {
		m.statusMessage = "Cannot move a merged branch"
		m.statusIsError = true
		return
	}
	if cur.Removed {
		return
	}

	// Find the target position
	target := m.cursor + delta
	for target >= 0 && target < len(m.nodes) {
		if !m.nodes[target].Removed && !m.nodes[target].Ref.IsMerged() {
			break
		}
		target += delta
	}
	if target < 0 || target >= len(m.nodes) {
		return
	}
	if m.nodes[target].Ref.IsMerged() {
		m.statusMessage = "Cannot move past a merged branch"
		m.statusIsError = true
		return
	}

	// Record undo
	m.actionStack = append(m.actionStack, StagedAction{
		Type:             ActionMove,
		BranchName:       cur.Ref.Branch,
		OriginalPosition: m.cursor,
		NewPosition:      target,
	})

	// Swap
	m.nodes[m.cursor], m.nodes[target] = m.nodes[target], m.nodes[m.cursor]
	m.cursor = target
	m.ensureVisible()
}

// toggleDrop toggles the drop action on the current node.
func (m *Model) toggleDrop() {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	node := &m.nodes[m.cursor]
	if node.Ref.IsMerged() {
		m.statusMessage = "Cannot drop a merged branch"
		m.statusIsError = true
		return
	}

	// Dropping an inserted node removes it entirely (undo the insert).
	// Pop the original insert action from the undo stack rather than
	// pushing a new entry — this makes the drop behave as a direct
	// cancellation of the insert.
	if node.IsInserted {
		branchName := node.Ref.Branch
		m.nodes = append(m.nodes[:m.cursor], m.nodes[m.cursor+1:]...)
		// Remove the matching insert action from the undo stack
		for i := len(m.actionStack) - 1; i >= 0; i-- {
			a := m.actionStack[i]
			if (a.Type == ActionInsertBelow || a.Type == ActionInsertAbove) && a.BranchName == branchName {
				m.actionStack = append(m.actionStack[:i], m.actionStack[i+1:]...)
				break
			}
		}
		if m.cursor >= len(m.nodes) {
			m.cursor = len(m.nodes) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return
	}

	if node.PendingAction != nil && node.PendingAction.Type == ActionDrop {
		// Undo drop
		m.actionStack = append(m.actionStack, StagedAction{
			Type:       ActionDrop,
			BranchName: node.Ref.Branch,
		})
		node.PendingAction = nil
		node.Removed = false
	} else {
		// Check if any other branch has a fold targeting this branch.
		// A fold-up targets the branch above (lower index), fold-down
		// targets the branch below (higher index).
		// Also check if dropping this branch would cause a fold to
		// retarget to an inserted branch.
		for i, other := range m.nodes {
			if other.PendingAction == nil || i == m.cursor {
				continue
			}
			if other.PendingAction.Type == ActionFoldUp {
				// fold-up target = nearest non-removed, non-merged node above (lower index)
				for j := i - 1; j >= 0; j-- {
					if !m.nodes[j].Removed && !m.nodes[j].Ref.IsMerged() {
						if j == m.cursor {
							// This branch is the current target. Check what the
							// next target would be after dropping it.
							nextTarget := -1
							for k := j - 1; k >= 0; k-- {
								if !m.nodes[k].Removed && !m.nodes[k].Ref.IsMerged() {
									nextTarget = k
									break
								}
							}
							if nextTarget < 0 {
								m.statusMessage = fmt.Sprintf("Cannot drop: %s is folding into this branch", other.Ref.Branch)
								m.statusIsError = true
								return
							}
							if m.nodes[nextTarget].IsInserted {
								m.statusMessage = fmt.Sprintf("Cannot drop: %s would fold into an inserted branch", other.Ref.Branch)
								m.statusIsError = true
								return
							}
							m.statusMessage = fmt.Sprintf("Cannot drop: %s is folding into this branch", other.Ref.Branch)
							m.statusIsError = true
							return
						}
						break
					}
				}
			}
			if other.PendingAction.Type == ActionFoldDown {
				// fold-down target = nearest non-removed, non-merged node below (higher index)
				for j := i + 1; j < len(m.nodes); j++ {
					if !m.nodes[j].Removed && !m.nodes[j].Ref.IsMerged() {
						if j == m.cursor {
							// This branch is the current target. Check what the
							// next target would be after dropping it.
							nextTarget := -1
							for k := j + 1; k < len(m.nodes); k++ {
								if !m.nodes[k].Removed && !m.nodes[k].Ref.IsMerged() {
									nextTarget = k
									break
								}
							}
							if nextTarget < 0 {
								m.statusMessage = fmt.Sprintf("Cannot drop: %s is folding into this branch", other.Ref.Branch)
								m.statusIsError = true
								return
							}
							if m.nodes[nextTarget].IsInserted {
								m.statusMessage = fmt.Sprintf("Cannot drop: %s would fold into an inserted branch", other.Ref.Branch)
								m.statusIsError = true
								return
							}
							m.statusMessage = fmt.Sprintf("Cannot drop: %s is folding into this branch", other.Ref.Branch)
							m.statusIsError = true
							return
						}
						break
					}
				}
			}
		}

		// Check if this would remove the last active original branch
		active := 0
		for j, other := range m.nodes {
			if j == m.cursor {
				continue // skip the branch we're about to drop
			}
			if !other.Removed && !other.Ref.IsMerged() && !other.IsInserted {
				active++
			}
		}
		if active < 1 {
			m.statusMessage = "Cannot drop the last branch in the stack"
			m.statusIsError = true
			return
		}

		// Apply drop
		m.actionStack = append(m.actionStack, StagedAction{
			Type:       ActionDrop,
			BranchName: node.Ref.Branch,
		})
		node.PendingAction = &PendingAction{Type: ActionDrop}
		node.Removed = true
	}
}

// fold stages a fold action on the current node.
func (m *Model) fold(action ActionType) {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	node := &m.nodes[m.cursor]
	if node.IsInserted {
		m.statusMessage = "Cannot fold an inserted branch — drop it with x to remove"
		m.statusIsError = true
		return
	}
	if node.Ref.IsMerged() {
		m.statusMessage = "Cannot fold a merged branch"
		m.statusIsError = true
		return
	}

	// If already has this same fold type, un-fold (toggle off)
	if node.PendingAction != nil && node.PendingAction.Type == action {
		m.actionStack = append(m.actionStack, StagedAction{
			Type:       action,
			BranchName: node.Ref.Branch,
		})
		node.PendingAction = nil
		node.Removed = false
		return
	}

	// If already has a different fold type, replace it
	if node.PendingAction != nil && (node.PendingAction.Type == ActionFoldDown || node.PendingAction.Type == ActionFoldUp) {
		// Clear the old fold first, then fall through to apply the new one
		node.PendingAction = nil
		node.Removed = false
	}

	// Can't fold a branch that's already dropped
	if node.Removed {
		return
	}

	// Check if the current node is the target of another fold — folding
	// a branch that is receiving commits from another fold is not allowed.
	for i, other := range m.nodes {
		if other.PendingAction == nil || i == m.cursor {
			continue
		}
		if other.PendingAction.Type == ActionFoldUp {
			for j := i - 1; j >= 0; j-- {
				if !m.nodes[j].Removed && !m.nodes[j].Ref.IsMerged() {
					if j == m.cursor {
						m.statusMessage = fmt.Sprintf("Cannot fold: %s is folding into this branch", other.Ref.Branch)
						m.statusIsError = true
						return
					}
					break
				}
			}
		}
		if other.PendingAction.Type == ActionFoldDown {
			for j := i + 1; j < len(m.nodes); j++ {
				if !m.nodes[j].Removed && !m.nodes[j].Ref.IsMerged() {
					if j == m.cursor {
						m.statusMessage = fmt.Sprintf("Cannot fold: %s is folding into this branch", other.Ref.Branch)
						m.statusIsError = true
						return
					}
					break
				}
			}
		}
	}

	// Find the target branch
	var targetIdx int
	var found bool

	if action == ActionFoldDown {
		// Fold down: target is the next non-removed, non-merged node toward trunk (higher index)
		for i := m.cursor + 1; i < len(m.nodes); i++ {
			if !m.nodes[i].Removed && !m.nodes[i].Ref.IsMerged() {
				targetIdx = i
				found = true
				break
			}
		}
		if !found {
			m.statusMessage = "No branch below to fold into"
			m.statusIsError = true
			return
		}
		if m.nodes[targetIdx].IsInserted {
			m.statusMessage = "Cannot fold into an inserted branch"
			m.statusIsError = true
			return
		}
	} else {
		// Fold up: target is the previous non-removed, non-merged node away from trunk (lower index)
		for i := m.cursor - 1; i >= 0; i-- {
			if !m.nodes[i].Removed && !m.nodes[i].Ref.IsMerged() {
				targetIdx = i
				found = true
				break
			}
		}
		if !found {
			m.statusMessage = "No branch above to fold into"
			m.statusIsError = true
			return
		}
		if m.nodes[targetIdx].IsInserted {
			m.statusMessage = "Cannot fold into an inserted branch"
			m.statusIsError = true
			return
		}
	}

	// Check if the target is already folding (mutual fold / chain fold)
	target := &m.nodes[targetIdx]
	if target.PendingAction != nil && (target.PendingAction.Type == ActionFoldDown || target.PendingAction.Type == ActionFoldUp) {
		m.statusMessage = fmt.Sprintf("Cannot fold: %s is already folding in the opposite direction", target.Ref.Branch)
		m.statusIsError = true
		return
	}

	// Check if this would remove the last active original branch
	active := 0
	for j, other := range m.nodes {
		if j == m.cursor {
			continue
		}
		if !other.Removed && !other.Ref.IsMerged() && !other.IsInserted {
			active++
		}
	}
	if active < 1 {
		m.statusMessage = "Cannot fold the last branch in the stack"
		m.statusIsError = true
		return
	}

	m.actionStack = append(m.actionStack, StagedAction{
		Type:       action,
		BranchName: node.Ref.Branch,
		FoldTarget: m.nodes[targetIdx].Ref.Branch,
	})
	node.PendingAction = &PendingAction{Type: action}
	node.Removed = true
}

// startRename enters rename mode for the current node.
func (m *Model) startRename() {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	node := &m.nodes[m.cursor]
	if node.Ref.IsMerged() {
		m.statusMessage = "Cannot rename a merged branch"
		m.statusIsError = true
		return
	}
	if node.Removed {
		return
	}

	m.renameMode = true
	m.renameOriginal = node.Ref.Branch
	m.renameInput.SetValue(node.Ref.Branch)
	m.renameInput.Prompt = ""
	m.renameInput.Focus()
	m.renameInput.CursorEnd()
}

// startInsert enters insert mode to type a new branch name.
func (m *Model) startInsert(direction ActionType) {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return
	}
	node := &m.nodes[m.cursor]
	if node.Ref.IsMerged() {
		m.statusMessage = "Cannot insert next to a merged branch"
		m.statusIsError = true
		return
	}
	if node.Removed {
		return
	}

	// Compute where the node would be inserted
	insertIdx := m.cursor
	if direction == ActionInsertBelow {
		insertIdx = m.cursor + 1
	}

	// Check if inserting here would place the new branch between a
	// folding branch and its target, making it the new fold target.
	for i, other := range m.nodes {
		if other.PendingAction == nil {
			continue
		}
		if other.PendingAction.Type == ActionFoldDown {
			for j := i + 1; j < len(m.nodes); j++ {
				if !m.nodes[j].Removed && !m.nodes[j].Ref.IsMerged() {
					if insertIdx > i && insertIdx <= j {
						m.statusMessage = fmt.Sprintf("Cannot insert here: %s is folding into %s", other.Ref.Branch, m.nodes[j].Ref.Branch)
						m.statusIsError = true
						return
					}
					break
				}
			}
		}
		if other.PendingAction.Type == ActionFoldUp {
			for j := i - 1; j >= 0; j-- {
				if !m.nodes[j].Removed && !m.nodes[j].Ref.IsMerged() {
					if insertIdx > j && insertIdx <= i {
						m.statusMessage = fmt.Sprintf("Cannot insert here: %s is folding into %s", other.Ref.Branch, m.nodes[j].Ref.Branch)
						m.statusIsError = true
						return
					}
					break
				}
			}
		}
	}

	m.insertMode = true
	m.insertDirection = direction
	m.insertInput.SetValue("")
	m.insertInput.Prompt = ""
	m.insertInput.Focus()
}

// undoLast reverses the most recent action from the stack.
func (m *Model) undoLast() {
	if len(m.actionStack) == 0 {
		m.statusMessage = "Nothing to undo"
		m.statusIsError = false
		return
	}

	action := m.actionStack[len(m.actionStack)-1]
	m.actionStack = m.actionStack[:len(m.actionStack)-1]

	switch action.Type {
	case ActionDrop:
		// Find the branch and toggle its state
		for i := range m.nodes {
			if m.nodes[i].Ref.Branch == action.BranchName || (m.nodes[i].PendingAction != nil && m.nodes[i].PendingAction.Type == ActionDrop && m.nodes[i].Ref.Branch == action.BranchName) {
				if m.nodes[i].PendingAction != nil && m.nodes[i].PendingAction.Type == ActionDrop {
					m.nodes[i].PendingAction = nil
					m.nodes[i].Removed = false
				} else {
					m.nodes[i].PendingAction = &PendingAction{Type: ActionDrop}
					m.nodes[i].Removed = true
				}
				break
			}
		}

	case ActionFoldDown, ActionFoldUp:
		for i := range m.nodes {
			if m.nodes[i].Ref.Branch == action.BranchName {
				if m.nodes[i].PendingAction != nil && m.nodes[i].PendingAction.Type == action.Type {
					m.nodes[i].PendingAction = nil
					m.nodes[i].Removed = false
				} else {
					m.nodes[i].PendingAction = &PendingAction{Type: action.Type}
					m.nodes[i].Removed = true
				}
				break
			}
		}

	case ActionMove:
		// Swap back to original positions
		from := -1
		to := action.OriginalPosition
		for i := range m.nodes {
			if m.nodes[i].Ref.Branch == action.BranchName {
				from = i
				break
			}
		}
		if from >= 0 && from != to && to >= 0 && to < len(m.nodes) {
			m.nodes[from], m.nodes[to] = m.nodes[to], m.nodes[from]
			m.cursor = to
		}

	case ActionRename:
		for i := range m.nodes {
			if m.nodes[i].Ref.Branch == action.BranchName || (m.nodes[i].PendingAction != nil && m.nodes[i].PendingAction.Type == ActionRename && m.nodes[i].PendingAction.NewName == action.NewName) {
				m.nodes[i].PendingAction = nil
				break
			}
		}

	case ActionInsertBelow, ActionInsertAbove:
		// Remove the inserted node from the slice
		for i := range m.nodes {
			if m.nodes[i].IsInserted && m.nodes[i].Ref.Branch == action.BranchName {
				m.nodes = append(m.nodes[:i], m.nodes[i+1:]...)
				if m.cursor >= len(m.nodes) {
					m.cursor = len(m.nodes) - 1
				}
				if m.cursor < 0 {
					m.cursor = 0
				}
				break
			}
		}
	}
}

// tryApply validates and initiates apply.
func (m Model) tryApply() (tea.Model, tea.Cmd) {
	hasPending := false
	effectiveIdx := 0
	for _, n := range m.nodes {
		if n.PendingAction != nil {
			hasPending = true
			break
		}
		if n.IsInserted {
			continue
		}
		if !n.Removed && n.OriginalPosition != effectiveIdx {
			hasPending = true
			break
		}
		effectiveIdx++
	}

	if !hasPending {
		m.statusMessage = "No pending changes to apply"
		m.statusIsError = false
		return m, nil
	}

	// Ensure at least one non-removed, non-merged, non-inserted branch remains
	active := 0
	for _, n := range m.nodes {
		if !n.Removed && !n.Ref.IsMerged() && !n.IsInserted {
			active++
		}
	}
	if active < 1 {
		m.statusMessage = "Cannot remove all branches from the stack"
		m.statusIsError = true
		return m, nil
	}

	m.applyRequested = true
	return m, tea.Quit
}

// --- Scrolling ---

func (m *Model) ensureVisible() {
	if m.height == 0 {
		return
	}
	startLine := 0
	for i := 0; i < m.cursor; i++ {
		startLine += m.nodeLineCount(i)
	}
	endLine := startLine + m.nodeLineCount(m.cursor)

	viewHeight := m.contentViewHeight()
	m.scrollOffset = shared.EnsureVisible(startLine, endLine, m.scrollOffset, viewHeight)
}

func (m Model) nodeLineCount(idx int) int {
	return shared.NodeLineCount(toNodeData(m.nodes[idx], idx, idx))
}

func (m Model) contentViewHeight() int {
	reserved := 3 // post-scroll newline + context line + status bar
	if shared.ShouldShowHeader(m.width, m.height) {
		reserved += shared.HeaderHeight
	}
	h := m.height - reserved
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) clampScroll() {
	total := 0
	for i := range m.nodes {
		total += m.nodeLineCount(i)
	}
	total++ // trunk line
	m.scrollOffset = shared.ClampScroll(total, m.contentViewHeight(), m.scrollOffset)
}

// --- Mouse handling ---

// handleMouseClick processes a mouse click at the given screen position.
func (m Model) handleMouseClick(screenX, screenY int) (tea.Model, tea.Cmd) {
	nodes := make([]shared.BranchNodeData, len(m.nodes))
	for i, n := range m.nodes {
		nodes[i] = toNodeData(n, i, i)
	}

	result := shared.HandleClick(screenX, screenY, nodes, m.width, m.height, m.scrollOffset, shared.ShouldShowHeader(m.width, m.height), false)
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

// --- View ---

// toNodeData converts a ModifyBranchNode to shared.BranchNodeData,
// applying drop/fold/move visual overrides. currentIdx is the node's
// current position in the list. effectiveIdx is the position among
// non-inserted nodes (used for move detection).
func toNodeData(n ModifyBranchNode, currentIdx int, effectiveIdx int) shared.BranchNodeData {
	data := shared.BranchNodeData{
		Ref:             n.Ref,
		IsCurrent:       n.IsCurrent,
		IsLinear:        n.IsLinear,
		BaseBranch:      n.BaseBranch,
		Commits:         n.Commits,
		FilesChanged:    n.FilesChanged,
		PR:              n.PR,
		Additions:       n.Additions,
		Deletions:       n.Deletions,
		CommitsExpanded: n.CommitsExpanded,
		FilesExpanded:   n.FilesExpanded,
	}

	if n.PendingAction != nil {
		switch n.PendingAction.Type {
		case ActionDrop:
			s := dropBranchStyle
			c := dropConnectorStyle
			data.BranchNameStyleOverride = &s
			data.ConnectorStyleOverride = &c
			data.ForceDashedConnector = true
		case ActionFoldDown, ActionFoldUp:
			s := foldBranchStyle
			c := foldConnectorStyle
			data.BranchNameStyleOverride = &s
			data.ConnectorStyleOverride = &c
			data.ForceDashedConnector = true
		case ActionInsertBelow, ActionInsertAbove:
			s := insertBranchStyle
			c := insertConnectorStyle
			data.BranchNameStyleOverride = &s
			data.ConnectorStyleOverride = &c
		}
	}

	// Moved branch: purple solid connector (no dash, no strikethrough)
	if n.PendingAction == nil && !n.Ref.IsMerged() && !n.IsInserted && n.OriginalPosition != effectiveIdx {
		c := movedConnectorStyle
		data.ConnectorStyleOverride = &c
	}

	return data
}

// nodeAnnotation builds an optional annotation from the node's pending action
// or its position change. effectiveIdx is the node's position among non-inserted
// nodes, used for move detection.
func nodeAnnotation(n ModifyBranchNode, effectiveIdx int) *shared.NodeAnnotation {
	if n.Ref.IsMerged() {
		return &shared.NodeAnnotation{Text: "🔒", Style: shared.DimStyle}
	}
	if n.PendingAction != nil {
		switch n.PendingAction.Type {
		case ActionDrop:
			return &shared.NodeAnnotation{Text: "✗ drop", Style: dropBadge}
		case ActionFoldDown:
			return &shared.NodeAnnotation{Text: "↓ fold down", Style: foldBadge}
		case ActionFoldUp:
			return &shared.NodeAnnotation{Text: "↑ fold up", Style: foldBadge}
		case ActionRename:
			return &shared.NodeAnnotation{Text: "→ " + n.PendingAction.NewName, Style: renameBadge}
		case ActionInsertBelow, ActionInsertAbove:
			return &shared.NodeAnnotation{Text: "✚ insert", Style: insertBadge}
		case ActionMove:
			return &shared.NodeAnnotation{Text: "↕ moved", Style: moveBadge}
		}
	}
	// Show move annotation when position changed (even without explicit PendingAction)
	if !n.Ref.IsMerged() && !n.IsInserted && n.OriginalPosition != effectiveIdx {
		delta := n.OriginalPosition - effectiveIdx // positive = moved up (toward top)
		direction := "up"
		layers := delta
		if delta < 0 {
			direction = "down"
			layers = -delta
		}
		label := "layers"
		if layers == 1 {
			label = "layer"
		}
		text := fmt.Sprintf("↕ moved %d %s %s", layers, label, direction)
		return &shared.NodeAnnotation{Text: text, Style: moveBadge}
	}
	return nil
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	if m.showHelp {
		return renderHelpOverlay(m.width, m.height)
	}

	var out strings.Builder

	// Header
	showHeader := shared.ShouldShowHeader(m.width, m.height)
	if showHeader {
		shared.RenderHeader(&out, m.buildHeaderConfig(), m.width, m.height)
	}

	// Build the scrollable branch list content
	var b strings.Builder
	effectiveIdx := 0
	for i := 0; i < len(m.nodes); i++ {
		ei := effectiveIdx
		if m.nodes[i].IsInserted {
			ei = -1 // inserted nodes have no effective position
		} else {
			effectiveIdx++
		}
		nodeData := toNodeData(m.nodes[i], i, ei)
		isFocused := i == m.cursor
		annotation := nodeAnnotation(m.nodes[i], ei)
		shared.RenderNode(&b, nodeData, isFocused, m.width, annotation)
	}
	shared.RenderTrunk(&b, m.trunk.Branch)

	// Count fixed bottom lines (always visible, not scrollable).
	// The bottom section always has 2 lines: one for contextual info
	// (rename prompt or error, blank when neither) and one for the status bar.
	bottomLines := 2 // error/status line + status bar (post-scroll newline is inline)

	// Scrolling — reserve space for header and fixed bottom
	reservedLines := bottomLines
	if showHeader {
		reservedLines += shared.HeaderHeight
	}
	viewHeight := m.height - reservedLines
	if viewHeight < 1 {
		viewHeight = 1
	}

	out.WriteString(shared.ApplyScrollToContent(b.String(), m.scrollOffset, viewHeight))
	out.WriteString("\n")

	// Second-to-bottom: error/status message line (always present, blank when empty)
	if m.statusMessage != "" {
		if m.statusIsError {
			out.WriteString(transientErrorStyle.Render("✗ " + m.statusMessage))
		} else {
			out.WriteString(transientInfoStyle.Render(m.statusMessage))
		}
	}

	// Bottom line: rename prompt (when active) or status bar
	out.WriteString("\n")
	if m.renameMode {
		out.WriteString(renameBadge.Render(fmt.Sprintf("Rename: %s → ", m.renameOriginal)))
		out.WriteString(m.renameInput.View())
	} else if m.insertMode {
		direction := "below"
		if m.insertDirection == ActionInsertAbove {
			direction = "above"
		}
		out.WriteString(insertBadge.Render(fmt.Sprintf("Insert %s: ", direction)))
		out.WriteString(m.insertInput.View())
	} else {
		out.WriteString(renderStatusLine(m.nodes, m.width))
	}

	return out.String()
}

// buildHeaderConfig creates the header configuration for modify mode.
func (m Model) buildHeaderConfig() shared.HeaderConfig {
	mergedCount := 0
	for _, n := range m.nodes {
		if n.Ref.IsMerged() {
			mergedCount++
		}
	}

	// Count only original branches (exclude staged inserts)
	branchCount := 0
	for _, n := range m.nodes {
		if !n.IsInserted {
			branchCount++
		}
	}
	branchInfo := fmt.Sprintf("%d branches", branchCount)
	if branchCount == 1 {
		branchInfo = "1 branch"
	}
	if mergedCount > 0 {
		branchInfo += fmt.Sprintf(" (%d merged, locked)", mergedCount)
	}

	pendingSummary := pendingChangeSummary(m.nodes)

	infoLines := []shared.HeaderInfoLine{
		{Icon: "◆", Label: "Base: " + m.trunk.Branch},
		{Icon: "○", Label: branchInfo},
	}
	if pendingSummary != "" {
		yellowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
		infoLines = append(infoLines, shared.HeaderInfoLine{Icon: "■", Label: pendingSummary, IconStyle: &yellowStyle})
	} else {
		infoLines = append(infoLines, shared.HeaderInfoLine{Icon: "□", Label: "No pending changes"})
	}

	mode := m.currentMode()
	reorderDisabled := mode == modeStructure
	structureDisabled := mode == modeReorder

	return shared.HeaderConfig{
		ShowArt:         true,
		Title:           "Modify Stack",
		Subtitle:        "v" + m.version,
		InfoLines:       infoLines,
		ShortcutColumns: 2,
		Shortcuts: []shared.ShortcutEntry{
			// Left column                          // Right column
			{Key: "↑↓", Desc: "select branch"}, {Key: "x", Desc: "drop", Disabled: structureDisabled},
			{Key: "f", Desc: "view files"}, {Key: "r", Desc: "rename", Disabled: structureDisabled},
			{Key: "c", Desc: "view commits"}, {Key: "i/I", Desc: "insert below/above", Disabled: structureDisabled},
			{Key: "?", Desc: "help"}, {Key: "d/u", Desc: "fold down/up", Disabled: structureDisabled},
			{Key: "q/esc", Desc: "quit"}, {Key: "shift+↑↓", Desc: "reorder", Disabled: reorderDisabled},
			{Key: "^S", Desc: "apply changes"}, {Key: "z", Desc: "undo"},
		},
	}
}

// Ensure Model satisfies the tea.Model interface.
var _ tea.Model = Model{}
