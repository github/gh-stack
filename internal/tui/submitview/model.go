package submitview

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/stack"
)

// editField identifies the focused field in the right editor panel.
type editField int

const (
	fieldTitle editField = iota
	fieldDescription
	fieldDraft
)

// keyMap holds the bindings the model matches centrally (the help overlay lists
// the full set separately).
type keyMap struct {
	Help key.Binding
	Quit key.Binding
}

var keys = keyMap{
	Help: key.NewBinding(
		key.WithKeys("?", "ctrl+h"),
		key.WithHelp("?", "help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

// Options configures a new submit TUI model.
type Options struct {
	// Nodes is the branch list in display order (index 0 = top of stack).
	Nodes []SubmitNode
	// Trunk is the stack's trunk branch, shown for context.
	Trunk stack.BranchRef
	// StackName is the human-readable stack name shown in the header.
	StackName string
	// RepoLabel is the "owner/repo" string shown in the header.
	RepoLabel string
	// Version is the CLI version string.
	Version string
}

// Model is the Bubble Tea model backing the interactive `gh stack submit` TUI.
type Model struct {
	nodes     []SubmitNode
	trunk     stack.BranchRef
	stackName string
	repoLabel string
	version   string

	// prefix is the common slash-delimited branch-name prefix, used to render
	// short branch names in the stack map.
	prefix string

	cursor int // index into nodes (the focused branch)

	width, height int

	// Editor state for the focused branch.
	titleInput   textinput.Model
	descArea     textarea.Model
	focusedField editField
	descPreview  bool // description preview (vs edit)

	// descScroll is the wheel-scroll offset (absolute top visual row) for the
	// description box. descScrollPinned is true while the user is free-scrolling
	// with the wheel; when false the view follows the cursor. A keystroke unpins
	// it so editing always shows the cursor.
	descScroll       int
	descScrollPinned bool

	showHelp bool

	// Transient status line shown below the content (cleared on next key).
	statusMessage string
	statusIsError bool

	// hoverRow is the node index currently under the mouse pointer, or -1.
	hoverRow int

	// leftScroll is the first visible row offset of the left stack timeline when
	// its content is taller than the panel.
	leftScroll int

	// confirmingQuit is true while the discard-edits confirmation is shown.
	confirmingQuit bool

	// Outcome flags consumed by the command layer once the program exits.
	submitRequested bool
	cancelled       bool

	// openURL, when non-nil, is called instead of launching the system browser
	// to open an existing PR. Tests inject a no-op so the suite never spawns a
	// real browser.
	openURL func(string)
}

// New constructs a submit TUI model from the given options. The single screen
// opens immediately with the first branch focused (preferring the first NEW
// branch) and the title field ready for editing.
func New(opts Options) Model {
	branchNames := make([]string, len(opts.Nodes))
	for i, n := range opts.Nodes {
		branchNames[i] = n.Ref.Branch
	}

	// Start on the bottom-most NEW branch (closest to trunk) — the first PR
	// created, in stack order. Nodes are ordered top (index 0) to bottom, so the
	// bottom-most NEW branch is the highest-indexed one.
	cursor := 0
	for i, n := range opts.Nodes {
		if n.State == StateNew {
			cursor = i
		}
	}

	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 256

	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // unlimited
	// Soften the textarea chrome; the panel provides the border.
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()

	m := Model{
		nodes:     opts.Nodes,
		trunk:     opts.Trunk,
		stackName: opts.StackName,
		repoLabel: opts.RepoLabel,
		version:   opts.Version,
		prefix:    CommonPrefix(branchNames),
		cursor:    cursor,
		hoverRow:  -1,

		titleInput:   ti,
		descArea:     ta,
		focusedField: fieldTitle,
	}

	m.loadEditor()
	// Focus the first field of the initial branch (title for an included NEW
	// branch, the Create-PR toggle otherwise).
	_ = m.focusFirstField()

	return m
}

// --- Getters for the command layer ---

// SubmitRequested reports whether the user confirmed the batch submit.
func (m Model) SubmitRequested() bool { return m.submitRequested }

// Cancelled reports whether the user quit without submitting.
func (m Model) Cancelled() bool { return m.cancelled }

// Nodes returns the current per-branch state, from which the command builds
// the per-PR overrides.
func (m Model) Nodes() []SubmitNode { return m.nodes }

// --- Bubble Tea interface ---

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeEditor()
		return m, nil

	case tea.KeyMsg:
		// Any key dismisses a transient status hint.
		m.statusMessage = ""
		m.statusIsError = false

		if m.confirmingQuit {
			return m.updateQuitConfirm(msg)
		}
		if m.showHelp {
			return m.updateHelp(msg)
		}
		return m.updateScreen(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case editorFinishedMsg:
		return m.handleEditorFinished(msg)
	}

	return m, nil
}

// updateHelp handles keys while the help overlay is visible.
func (m Model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Help) || msg.Type == tea.KeyEscape {
		m.showHelp = false
	}
	return m, nil
}

// updateQuitConfirm handles keys while the discard-edits confirmation is shown.
func (m Model) updateQuitConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.cancelled = true
		return m, tea.Quit
	case "n", "N", "esc":
		m.confirmingQuit = false
		return m, nil
	}
	if msg.Type == tea.KeyCtrlC {
		m.cancelled = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.confirmingQuit {
		return renderQuitConfirm(m.width, m.height)
	}
	if m.showHelp {
		return renderHelpOverlay(m.width, m.height)
	}
	return m.viewScreen()
}

// anyEdited reports whether the user has made any change worth a quit
// confirmation: a deselected NEW branch or an edited title/description/draft.
func (m Model) anyEdited() bool {
	for _, n := range m.nodes {
		if n.Edited() {
			return true
		}
	}
	return false
}

// quit marks the session cancelled and exits. If the user has unsaved edits, it
// first raises a discard-edits confirmation instead of quitting immediately.
func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.anyEdited() && !m.confirmingQuit {
		m.confirmingQuit = true
		return m, nil
	}
	m.cancelled = true
	return m, tea.Quit
}

// Ensure Model satisfies the tea.Model interface.
var _ tea.Model = Model{}
