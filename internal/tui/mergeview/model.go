package mergeview

import (
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/theme"
	"github.com/github/gh-stack/internal/tui/shared"
)

// Model is the Bubble Tea model backing the merge wizard.
type Model struct {
	opts Options

	step Step

	// topIndex is the highest selected PR index (the merge high-water mark);
	// -1 means nothing selected. Selecting index i implies 0..i are included.
	topIndex int
	cursor   int

	methodCursor int
	method       string

	spinner spinner.Model
	status  MergeStatus

	submitted    bool
	merged       bool
	enqueued     bool
	failed       bool
	cancelled    bool
	watchStopped bool
	message      string
	err          error

	pollInterval time.Duration

	width        int
	height       int
	scrollOffset int
	usePowerline bool
}

// New builds a merge wizard model from the given options.
func New(opts Options) Model {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = time.Second
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(theme.ColorAccent)

	m := Model{
		opts:         opts,
		topIndex:     len(opts.PRs) - 1,
		cursor:       len(opts.PRs) - 1,
		method:       normalizeDefaultMethod(opts),
		pollInterval: interval,
		spinner:      sp,
		usePowerline: powerlineEnabled(),
	}
	m.methodCursor = indexOf(opts.AllowedMethods, m.method)

	if opts.PreselectTopIndex >= 0 && opts.PreselectTopIndex < len(opts.PRs) {
		// PR-number mode: the target is fixed, so skip the selection step.
		m.topIndex = opts.PreselectTopIndex
		m.cursor = opts.PreselectTopIndex
		m.step = StepMethod
	}

	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.scrollOffset = m.clampScroll()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case submitDoneMsg:
		return m.handleSubmitDone(msg)

	case pollTickMsg:
		if m.step == StepProgress && !m.done() {
			return m, m.pollCmd()
		}
		return m, nil

	case pollDoneMsg:
		return m.handlePollDone(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		if m.step == StepProgress && m.submitted && !m.done() {
			// The merge is running server-side; stop watching without cancelling it.
			m.watchStopped = true
			return m.finish()
		}
		m.cancelled = true
		return m.finish()
	}

	switch m.step {
	case StepSelectPRs:
		return m.handleSelectKey(key)
	case StepMethod:
		return m.handleMethodKey(key)
	case StepConfirm:
		return m.handleConfirmKey(key)
	}
	return m, nil
}

func (m Model) handleSelectKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		// The stack renders top-first, so moving "up" goes toward the top of
		// the stack (a higher index).
		if m.cursor < len(m.opts.PRs)-1 {
			m.cursor++
		}
	case "down", "j":
		if m.cursor > 0 {
			m.cursor--
		}
	case "shift+up":
		// Jump to the top of the stack.
		m.cursor = len(m.opts.PRs) - 1
	case "shift+down":
		// Jump to the bottom of the stack.
		m.cursor = 0
	case " ", "x":
		if m.cursor <= m.topIndex {
			// Uncheck the cursor and everything above it.
			m.topIndex = m.cursor - 1
		} else {
			// Fill selection up to and including the cursor.
			m.topIndex = m.cursor
		}
	case "enter", "tab":
		if m.topIndex >= 0 {
			m.step = StepMethod
		}
	case "esc", "q":
		m.cancelled = true
		return m.finish()
	}
	m.scrollOffset = m.clampScroll()
	return m, nil
}

// maxVisibleItems caps how many pull requests the select step shows at once; the
// rest are reached by scrolling so the picker never takes over the screen.
const maxVisibleItems = 10

// visibleItems is the number of pull requests shown in the select window at
// once — capped at maxVisibleItems and shrunk to fit a short terminal (each
// item renders on two lines). When the terminal size is unknown, it still caps
// at maxVisibleItems so the first frame can't overflow a large stack.
func (m Model) visibleItems() int {
	n := len(m.opts.PRs)
	if m.height <= 0 {
		if n > maxVisibleItems {
			return maxVisibleItems
		}
		return n
	}
	// Reserve lines for the header, scroll indicators, summary, and footer.
	const chrome = 11
	avail := (m.height - chrome) / 2
	limit := maxVisibleItems
	if avail < limit {
		limit = avail
	}
	if limit < 1 {
		limit = 1
	}
	if n < limit {
		limit = n
	}
	return limit
}

// clampScroll returns a scroll offset (in display rows, where row 0 is the top
// of the stack) that keeps the cursor's row visible within the select window.
func (m Model) clampScroll() int {
	n := len(m.opts.PRs)
	cursorRow := n - 1 - m.cursor
	return shared.EnsureVisible(cursorRow, cursorRow+1, m.scrollOffset, m.visibleItems())
}

func (m Model) handleMethodKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.methodCursor > 0 {
			m.methodCursor--
		}
	case "down", "j":
		if m.methodCursor < len(m.opts.AllowedMethods)-1 {
			m.methodCursor++
		}
	case "enter", "tab", " ":
		if len(m.opts.AllowedMethods) > 0 {
			m.method = m.opts.AllowedMethods[m.methodCursor]
			m.step = StepConfirm
		}
	case "shift+tab":
		if m.opts.PreselectTopIndex < 0 {
			m.step = StepSelectPRs
		}
	case "esc", "q":
		m.cancelled = true
		return m.finish()
	}
	return m, nil
}

func (m Model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter", "y", "Y":
		m.step = StepProgress
		m.submitted = true
		return m, tea.Batch(m.spinner.Tick, m.submitCmd())
	case "shift+tab":
		m.step = StepMethod
	case "esc", "q":
		m.cancelled = true
		return m.finish()
	}
	return m, nil
}

func (m Model) handleSubmitDone(msg submitDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.failed = true
		m.message = msg.err.Error()
		return m.finish()
	}

	m.status = msg.status
	m.message = msg.status.Message

	switch msg.status.Status {
	case StatusMerged:
		m.merged = true
		return m.finish()
	case StatusEnqueued:
		m.enqueued = true
		return m.finish()
	case StatusFailed:
		m.failed = true
		return m.finish()
	default:
		// Pending (or an existing request adopted): poll if we have a UUID;
		// otherwise this is unexpected, so treat it as a failure.
		if msg.status.UUID != "" {
			return m, m.pollTickCmd()
		}
		m.failed = true
		return m.finish()
	}
}

func (m Model) handlePollDone(msg pollDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.failed = true
		m.message = msg.err.Error()
		return m.finish()
	}

	m.status = msg.status
	m.message = msg.status.Message

	switch msg.status.Status {
	case StatusMerged:
		m.merged = true
		return m.finish()
	case StatusEnqueued:
		m.enqueued = true
		return m.finish()
	case StatusFailed:
		m.failed = true
		return m.finish()
	default:
		// Still pending: keep polling.
		return m, m.pollTickCmd()
	}
}

func (m Model) finish() (tea.Model, tea.Cmd) {
	m.step = StepDone
	return m, tea.Quit
}

func (m Model) done() bool { return m.merged || m.enqueued || m.failed || m.step == StepDone }

// Outcome reports the final result of the wizard for the command layer.
func (m Model) Outcome() Outcome {
	o := Outcome{
		Cancelled:    m.cancelled,
		Submitted:    m.submitted,
		Merged:       m.merged,
		Enqueued:     m.enqueued,
		Failed:       m.failed,
		WatchStopped: m.watchStopped,
		Message:      m.message,
		TargetPR:     m.targetPR(),
		Method:       m.method,
		SHA:          m.status.SHA,
		Err:          m.err,
	}
	if m.merged || m.enqueued {
		o.MergedPRs = m.selectedNumbers()
	}
	return o
}

func (m Model) targetPR() int {
	if m.topIndex >= 0 && m.topIndex < len(m.opts.PRs) {
		return m.opts.PRs[m.topIndex].Number
	}
	return 0
}

func (m Model) selectedNumbers() []int {
	if m.topIndex < 0 {
		return nil
	}
	nums := make([]int, 0, m.topIndex+1)
	for i := 0; i <= m.topIndex && i < len(m.opts.PRs); i++ {
		nums = append(nums, m.opts.PRs[i].Number)
	}
	return nums
}

// --- async commands ---

type submitDoneMsg struct {
	status MergeStatus
	err    error
}

type pollDoneMsg struct {
	status MergeStatus
	err    error
}

type pollTickMsg struct{}

func (m Model) submitCmd() tea.Cmd {
	target := m.targetPR()
	method := m.method
	submit := m.opts.Submit
	return func() tea.Msg {
		if submit == nil {
			return submitDoneMsg{err: errors.New("no submit function configured")}
		}
		s, err := submit(target, method)
		return submitDoneMsg{status: s, err: err}
	}
}

func (m Model) pollCmd() tea.Cmd {
	target := m.targetPR()
	uuid := m.status.UUID
	poll := m.opts.Poll
	return func() tea.Msg {
		if poll == nil {
			return pollDoneMsg{err: errors.New("no poll function configured")}
		}
		s, err := poll(target, uuid)
		return pollDoneMsg{status: s, err: err}
	}
}

func (m Model) pollTickCmd() tea.Cmd {
	return tea.Tick(m.pollInterval, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// --- helpers ---

func normalizeDefaultMethod(opts Options) string {
	if opts.DefaultMethod != "" && contains(opts.AllowedMethods, opts.DefaultMethod) {
		return opts.DefaultMethod
	}
	if len(opts.AllowedMethods) > 0 {
		return opts.AllowedMethods[0]
	}
	return opts.DefaultMethod
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return 0
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
