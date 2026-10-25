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

func TestNudge(t *testing.T) {
	assert.Equal(t, "#1e1e22", nudge("#1e1e1e"))
	assert.Equal(t, "#fffffb", nudge("#ffffff"), "moves away from the channel's limit")
	assert.Equal(t, "invalid", nudge("invalid"))
}

// TestPlanRestore checks the restore planned for each kind of terminal color,
// and that planning leaves the terminal showing the colors it had.
func TestPlanRestore(t *testing.T) {
	configured := [2]string{"#1e1e1e", "#cccccc"}
	runtime := [2]string{"#282a36", "#f8f8f2"}
	setRuntime := ansi.SetBackgroundColor(runtime[0]) + ansi.SetForegroundColor(runtime[1])
	tests := []struct {
		name         string
		term         fakeTerminal
		want         string
		wantQueries  int
		wantOverride [2]string
	}{
		{
			name:        "configured colors are reset",
			term:        fakeTerminal{configured: configured},
			want:        canvasReset,
			wantQueries: 2,
		},
		{
			name:         "runtime colors are set back",
			term:         fakeTerminal{configured: configured, override: runtime},
			want:         setRuntime,
			wantQueries:  2,
			wantOverride: runtime,
		},
		{
			name:         "a terminal that ignores resets gets its colors set back",
			term:         fakeTerminal{configured: configured, ignoresReset: true},
			want:         ansi.SetBackgroundColor(configured[0]) + ansi.SetForegroundColor(configured[1]),
			wantQueries:  2,
			wantOverride: configured,
		},
		{
			name:         "each color is planned on its own",
			term:         fakeTerminal{configured: configured, override: [2]string{runtime[0], ""}},
			want:         ansi.SetBackgroundColor(runtime[0]) + ansi.ResetForegroundColor,
			wantQueries:  2,
			wantOverride: [2]string{runtime[0], ""},
		},
		{
			name:        "a terminal that doesn't report colors is reset",
			term:        fakeTerminal{configured: configured, silent: true},
			want:        canvasReset,
			wantQueries: 1,
		},
		{
			name:         "a canvas left by a killed run is reset",
			term:         fakeTerminal{configured: configured, override: [2]string{"#0d1117", "#f0f6fc"}},
			want:         canvasReset,
			wantQueries:  1,
			wantOverride: [2]string{"#0d1117", "#f0f6fc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := tt.term
			term.out = tempFile(t)
			before := term.colors()

			plan := planRestore(term.out, term.query(t))

			term.sync(t)
			assert.Equal(t, tt.want, plan)
			assert.Equal(t, tt.wantQueries, term.queries)
			assert.Equal(t, before, term.colors(), "planning leaves the terminal's colors as they were")
			assert.Equal(t, tt.wantOverride, term.override, "colors set explicitly")
		})
	}
}

// TestCanvasOutputRestoresTerminalColors runs a real alternate-screen program
// that launches an external command, as submit does for $EDITOR, against a
// fake terminal. It checks the colors the command sees and the colors left
// after the program, including when the configured theme changes while the
// program is open (e.g. an automatic light/dark switch).
func TestCanvasOutputRestoresTerminalColors(t *testing.T) {
	dark := [2]string{"#1e1e1e", "#cccccc"}
	light := [2]string{"#fdf6e3", "#657b83"}
	runtime := [2]string{"#282a36", "#f8f8f2"}
	tests := []struct {
		name         string
		term         fakeTerminal
		themeChange  bool // switch the configured colors to light while the command runs
		wantDuring   [2]string
		wantAfter    [2]string
		wantOverride [2]string
	}{
		{
			name:       "configured colors",
			term:       fakeTerminal{configured: dark},
			wantDuring: dark,
			wantAfter:  dark,
		},
		{
			name:        "configured theme changes while open",
			term:        fakeTerminal{configured: dark},
			themeChange: true,
			wantDuring:  dark,
			wantAfter:   light,
		},
		{
			name:         "runtime colors",
			term:         fakeTerminal{configured: dark, override: runtime},
			wantDuring:   runtime,
			wantAfter:    runtime,
			wantOverride: runtime,
		},
		{
			name:         "runtime colors outlast a theme change",
			term:         fakeTerminal{configured: dark, override: runtime},
			themeChange:  true,
			wantDuring:   runtime,
			wantAfter:    runtime,
			wantOverride: runtime,
		},
		{
			name:         "terminal ignores resets",
			term:         fakeTerminal{configured: dark, ignoresReset: true},
			wantDuring:   dark,
			wantAfter:    dark,
			wantOverride: dark,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := tt.term
			term.out = tempFile(t)
			o := newCanvasOutput(term.out, true, term.query(t))
			planned := term.queries
			var during [2]string
			probe := &execProbe{out: o, run: func() {
				term.sync(t)
				during = term.colors()
				if tt.themeChange {
					term.configured = light
				}
			}}
			model := execModel{cmd: tea.Exec(probe, func(error) tea.Msg { return execDoneMsg{} })}

			_, err := tea.NewProgram(model,
				tea.WithAltScreen(),
				tea.WithInput(strings.NewReader("")),
				tea.WithOutput(o),
				tea.WithoutSignalHandler(),
			).Run()
			require.NoError(t, err)
			o.Restore()
			term.sync(t)

			assert.True(t, probe.ran)
			assert.False(t, probe.paintedDuringRun, "the canvas is down while the command runs")
			assert.Equal(t, tt.wantDuring, during, "colors the command sees")
			assert.Equal(t, tt.wantAfter, term.colors(), "colors after the program")
			assert.Equal(t, tt.wantOverride, term.override, "colors left set explicitly")
			assert.Equal(t, planned, term.queries, "no queries while the program owns the terminal")
			assert.Equal(t, 2, strings.Count(readAll(t, term.out)(), canvasPaint(true)), "painted on each alternate-screen enter")
		})
	}
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
// while it ran, then calls run.
type execProbe struct {
	out              *CanvasOutput
	run              func()
	ran              bool
	paintedDuringRun bool
}

func (p *execProbe) Run() error {
	p.out.mu.Lock()
	p.ran, p.paintedDuringRun = true, p.out.painted
	p.out.mu.Unlock()
	if p.run != nil {
		p.run()
	}
	return nil
}

func (p *execProbe) SetStdin(io.Reader)  {}
func (p *execProbe) SetStdout(io.Writer) {}
func (p *execProbe) SetStderr(io.Writer) {}

// fakeTerminal models a terminal's default colors: its configured colors,
// runtime overrides set with OSC 10/11, and resets (OSC 110/111) that clear an
// override unless the terminal ignores them. Before answering a color query or
// reporting its state, it applies everything written to out since last time.
type fakeTerminal struct {
	out          *os.File
	read         int
	configured   [2]string // background, foreground
	override     [2]string // "" when not set
	ignoresReset bool
	silent       bool // answers no color queries
	queries      int
}

func (ft *fakeTerminal) query(t *testing.T) colorQuery {
	return func() (string, string) {
		ft.sync(t)
		ft.queries++
		if ft.silent {
			return "", ""
		}
		c := ft.colors()
		return c[0], c[1]
	}
}

func (ft *fakeTerminal) colors() [2]string {
	c := ft.configured
	for i, o := range ft.override {
		if o != "" {
			c[i] = o
		}
	}
	return c
}

func (ft *fakeTerminal) sync(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(ft.out.Name())
	require.NoError(t, err)
	s := string(b[ft.read:])
	ft.read = len(b)
	for {
		start := strings.Index(s, "\x1b]")
		if start < 0 {
			return
		}
		s = s[start+2:]
		end := strings.IndexAny(s, "\x07\x1b")
		if end < 0 {
			return
		}
		ft.apply(s[:end])
		s = s[end:]
	}
}

func (ft *fakeTerminal) apply(osc string) {
	ps, pt, _ := strings.Cut(osc, ";")
	switch ps {
	case "11":
		ft.override[0] = pt
	case "10":
		ft.override[1] = pt
	case "111", "110":
		if !ft.ignoresReset {
			ft.override[map[string]int{"111": 0, "110": 1}[ps]] = ""
		}
	}
}

// testCanvasOutput returns a CanvasOutput that resets the terminal's colors,
// over a temporary file, and a func that reads everything written to it.
func testCanvasOutput(t *testing.T, dark bool) (*CanvasOutput, func() string) {
	t.Helper()
	f := tempFile(t)
	return &CanvasOutput{f: f, paint: canvasPaint(dark), restore: canvasReset}, readAll(t, f)
}

func readAll(t *testing.T, f *os.File) func() string {
	return func() string {
		b, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		return string(b)
	}
}

func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "canvas")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
