package theme

import (
	"io"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	enter = ansi.SetModeAltScreenSaveCursor
	exit  = ansi.ResetModeAltScreenSaveCursor
)

func TestCanvasSupported(t *testing.T) {
	tests := []struct {
		name         string
		background   string
		term         string
		isTTY        bool
		colorEnabled bool
		profile      termenv.Profile
		want         bool
	}{
		{name: "truecolor terminal", term: "xterm-256color", isTTY: true, colorEnabled: true, profile: termenv.TrueColor, want: true},
		{name: "explicit github", background: "github", term: "xterm-256color", isTTY: true, colorEnabled: true, profile: termenv.TrueColor, want: true},
		{name: "unknown value keeps the default", background: "auto", term: "xterm-256color", isTTY: true, colorEnabled: true, profile: termenv.TrueColor, want: true},
		{name: "opted out", background: "terminal", term: "xterm-256color", isTTY: true, colorEnabled: true, profile: termenv.TrueColor},
		{name: "opted out (case and space)", background: " Terminal ", term: "xterm-256color", isTTY: true, colorEnabled: true, profile: termenv.TrueColor},
		{name: "not a terminal", term: "xterm-256color", colorEnabled: true, profile: termenv.TrueColor},
		{name: "color disabled", term: "xterm-256color", isTTY: true, profile: termenv.TrueColor},
		{name: "dumb terminal", term: "dumb", isTTY: true, colorEnabled: true, profile: termenv.TrueColor},
		{name: "256 colors", term: "xterm-256color", isTTY: true, colorEnabled: true, profile: termenv.ANSI256},
		{name: "16 colors", term: "xterm", isTTY: true, colorEnabled: true, profile: termenv.ANSI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canvasSupported(tt.background, tt.term, tt.isTTY, tt.colorEnabled, tt.profile)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCanvasPaint(t *testing.T) {
	assert.Equal(t, "\x1b]11;#0d1117\x07\x1b]10;#f0f6fc\x07", canvasPaint(true), "GitHub dark canvas and ink")
	assert.Equal(t, "\x1b]11;#ffffff\x07\x1b]10;#1f2328\x07", canvasPaint(false), "GitHub light canvas and ink")
	assert.Equal(t, "\x1b]111\x07\x1b]110\x07", canvasReset)
}

func TestNewCanvasOutputFollowsBackground(t *testing.T) {
	before := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(before) })
	f := tempFile(t)

	lipgloss.SetHasDarkBackground(true)
	assert.Equal(t, canvasPaint(true), NewCanvasOutput(f).paint)
	lipgloss.SetHasDarkBackground(false)
	assert.Equal(t, canvasPaint(false), NewCanvasOutput(f).paint)
}

func TestCanvasOutputWrite(t *testing.T) {
	paint := canvasPaint(true)
	tests := []struct {
		name        string
		writes      []string
		want        string
		wantPainted bool
	}{
		{name: "plain output passes through", writes: []string{"hello"}, want: "hello"},
		{name: "paints after entering", writes: []string{enter}, want: enter + paint, wantPainted: true},
		{name: "paints within a larger write", writes: []string{"a" + enter + "b"}, want: "a" + enter + paint + "b", wantPainted: true},
		{name: "restores before leaving", writes: []string{enter, "frame", exit}, want: enter + paint + "frame" + canvasReset + exit},
		{name: "leaving unpainted adds nothing", writes: []string{exit}, want: exit},
		{name: "repeated enter paints once", writes: []string{enter, enter}, want: enter + paint + enter, wantPainted: true},
		{
			name:        "release and restore in one write",
			writes:      []string{enter, "x" + exit + "y" + enter + "z"},
			want:        enter + paint + "x" + canvasReset + exit + "y" + enter + paint + "z",
			wantPainted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, read := testCanvasOutput(t, true)
			for _, w := range tt.writes {
				n, err := o.Write([]byte(w))
				require.NoError(t, err)
				assert.Equal(t, len(w), n, "reports only the caller's bytes")
			}
			assert.Equal(t, tt.want, read())
			assert.Equal(t, tt.wantPainted, o.painted)
		})
	}
}

// TestCanvasOutputWriteString guards against embedding *os.File: Bubble Tea
// writes escape sequences with io.WriteString, which must still be intercepted.
func TestCanvasOutputWriteString(t *testing.T) {
	o, read := testCanvasOutput(t, false)
	_, err := io.WriteString(o, enter)
	require.NoError(t, err)
	assert.Equal(t, enter+canvasPaint(false), read())
}

func TestCanvasOutputRestore(t *testing.T) {
	o, read := testCanvasOutput(t, true)
	o.Restore()
	assert.Empty(t, read(), "nothing to restore before painting")

	_, err := o.Write([]byte(enter))
	require.NoError(t, err)
	o.Restore()
	o.Restore()
	assert.Equal(t, enter+canvasPaint(true)+canvasReset, read(), "restores exactly once")
	assert.False(t, o.painted)
}

func TestRGBHex(t *testing.T) {
	assert.Equal(t, "#1e1e1e", rgbHex(termenv.RGBColor("#1E1E1E")), "a terminal's answer")
	assert.Empty(t, rgbHex(termenv.ANSIColor(0)), "termenv's fallback guess")
	assert.Empty(t, rgbHex(nil), "no answer")
}

func TestNewCanvasOutputSavesColors(t *testing.T) {
	tests := []struct {
		name           string
		bg, fg         string
		wantBg, wantFg string
	}{
		{name: "terminal reports both", bg: "#282a36", fg: "#f8f8f2", wantBg: "#282a36", wantFg: "#f8f8f2"},
		{name: "terminal reports only the background", bg: "#282a36"},
		{name: "terminal reports nothing"},
		{name: "dark canvas left by a killed run", bg: "#0d1117", fg: "#f0f6fc"},
		{name: "light canvas left by a killed run", bg: "#ffffff", fg: "#1f2328"},
		{name: "a GitHub-like theme of the user's own", bg: "#0d1117", fg: "#e6edf3", wantBg: "#0d1117", wantFg: "#e6edf3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &scriptedColors{answers: [][2]string{{tt.bg, tt.fg}}}
			o := newCanvasOutput(tempFile(t), true, q.query)
			assert.Equal(t, tt.wantBg, o.savedBg)
			assert.Equal(t, tt.wantFg, o.savedFg)
			assert.Equal(t, 1, q.calls, "asks the terminal once before painting")
		})
	}
}

// TestCanvasOutputRestoreChecksColors verifies Restore puts back the colors
// the terminal had before painting when the reset didn't, and leaves the
// terminal on its own configuration when it did.
func TestCanvasOutputRestoreChecksColors(t *testing.T) {
	const savedBg, savedFg = "#282a36", "#f8f8f2"
	setBoth := ansi.SetBackgroundColor(savedBg) + ansi.SetForegroundColor(savedFg)
	tests := []struct {
		name      string
		after     [2]string // the terminal's colors after the reset
		want      string    // written after the reset
		wantCalls int
	}{
		{name: "reset brought the colors back", after: [2]string{savedBg, savedFg}, wantCalls: 2},
		{name: "reset dropped runtime colors", after: [2]string{"#1e1e1e", "#cccccc"}, want: setBoth, wantCalls: 2},
		{name: "terminal ignored the reset", after: [2]string{"#0d1117", "#f0f6fc"}, want: setBoth, wantCalls: 2},
		{name: "only the foreground differs", after: [2]string{savedBg, "#cccccc"}, want: ansi.SetForegroundColor(savedFg), wantCalls: 2},
		{name: "terminal stopped answering", wantCalls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &scriptedColors{answers: [][2]string{{savedBg, savedFg}, tt.after}}
			o, read := testCanvasOutputWith(t, q.query)
			_, err := o.Write([]byte(enter + exit))
			require.NoError(t, err)

			o.Restore()
			o.Restore()

			assert.Equal(t, enter+canvasPaint(true)+canvasReset+exit+tt.want, read())
			assert.Equal(t, tt.wantCalls, q.calls, "checks once, after the program")
		})
	}

	t.Run("never painted", func(t *testing.T) {
		q := &scriptedColors{answers: [][2]string{{savedBg, savedFg}}}
		o, read := testCanvasOutputWith(t, q.query)
		o.Restore()
		assert.Empty(t, read())
		assert.Equal(t, 1, q.calls, "no check without painting")
	})

	t.Run("colors unknown before painting", func(t *testing.T) {
		q := &scriptedColors{answers: [][2]string{{"", ""}}}
		o, read := testCanvasOutputWith(t, q.query)
		_, err := o.Write([]byte(enter))
		require.NoError(t, err)
		o.Restore()
		assert.Equal(t, enter+canvasPaint(true)+canvasReset, read(), "falls back to the reset")
		assert.Equal(t, 1, q.calls)
	})
}

// TestCanvasOutputWithBubbleTea runs a real alternate-screen program that
// launches an external command, verifying the canvas is painted only while the
// program is on screen: reset before the command runs (so $EDITOR sees the
// terminal's colors), repainted when the program returns, and reset on exit,
// after which Restore puts back runtime colors the reset dropped.
func TestCanvasOutputWithBubbleTea(t *testing.T) {
	q := &scriptedColors{answers: [][2]string{{"#282a36", "#f8f8f2"}, {"#1e1e1e", "#cccccc"}}}
	o, read := testCanvasOutputWith(t, q.query)
	probe := &execProbe{out: o}
	model := execModel{cmd: tea.Exec(probe, func(error) tea.Msg { return execDoneMsg{} })}

	_, err := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(o),
		tea.WithoutSignalHandler(),
	).Run()
	require.NoError(t, err)
	assert.Equal(t, 1, q.calls, "the terminal isn't queried while the program owns it")
	o.Restore()

	assert.True(t, probe.ran)
	assert.False(t, probe.paintedDuringRun, "the external command must see the terminal's own colors")
	assert.Equal(t,
		[]string{"enter", "paint", "reset", "exit", "enter", "paint", "reset", "exit"},
		canvasEvents(read()),
	)
	assert.True(t, strings.HasSuffix(read(), ansi.SetBackgroundColor("#282a36")+ansi.SetForegroundColor("#f8f8f2")),
		"runtime colors come back after the program")
}

type execDoneMsg struct{}

type execModel struct{ cmd tea.Cmd }

func (m execModel) Init() tea.Cmd { return m.cmd }

func (m execModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(execDoneMsg); ok {
		return m, tea.Quit
	}
	return m, nil
}

func (m execModel) View() string { return "canvas" }

// execProbe is a tea.ExecCommand that records whether the canvas was painted
// while it ran.
type execProbe struct {
	out              *CanvasOutput
	ran              bool
	paintedDuringRun bool
}

func (p *execProbe) Run() error {
	p.out.mu.Lock()
	defer p.out.mu.Unlock()
	p.ran, p.paintedDuringRun = true, p.out.painted
	return nil
}

func (p *execProbe) SetStdin(io.Reader)  {}
func (p *execProbe) SetStdout(io.Writer) {}
func (p *execProbe) SetStderr(io.Writer) {}

// canvasEvents returns the alternate-screen transitions and canvas color
// changes in s, in order.
func canvasEvents(s string) []string {
	names := map[string]string{
		enter:             "enter",
		exit:              "exit",
		canvasPaint(true): "paint",
		canvasReset:       "reset",
	}
	var events []string
	for i := 0; i < len(s); i++ {
		for seq, name := range names {
			if strings.HasPrefix(s[i:], seq) {
				events = append(events, name)
				i += len(seq) - 1
				break
			}
		}
	}
	return events
}

// testCanvasOutput returns a CanvasOutput over a temporary file and a func
// that reads everything written to it.
func testCanvasOutput(t *testing.T, dark bool) (*CanvasOutput, func() string) {
	t.Helper()
	f := tempFile(t)
	return &CanvasOutput{f: f, paint: canvasPaint(dark)}, readAll(t, f)
}

// testCanvasOutputWith is testCanvasOutput for the dark canvas, asking query
// for the terminal's colors.
func testCanvasOutputWith(t *testing.T, query colorQuery) (*CanvasOutput, func() string) {
	t.Helper()
	f := tempFile(t)
	return newCanvasOutput(f, true, query), readAll(t, f)
}

func readAll(t *testing.T, f *os.File) func() string {
	return func() string {
		b, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		return string(b)
	}
}

// scriptedColors is a colorQuery that answers with each of answers in turn,
// then with nothing.
type scriptedColors struct {
	answers [][2]string
	calls   int
}

func (s *scriptedColors) query() (string, string) {
	s.calls++
	if s.calls > len(s.answers) {
		return "", ""
	}
	a := s.answers[s.calls-1]
	return a[0], a[1]
}

func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "canvas")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
