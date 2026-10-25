package theme

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// BackgroundEnv names the environment variable that controls whether the
// full-screen TUIs paint the GitHub canvas: "github" (the default) paints it,
// and "terminal" keeps the terminal's own background and text colors.
const BackgroundEnv = "GH_STACK_BACKGROUND"

// canvasReset returns the terminal's default background and foreground to its
// configured colors (OSC 111/110).
const canvasReset = ansi.ResetBackgroundColor + ansi.ResetForegroundColor

var (
	enterAltScreen = []byte(ansi.SetModeAltScreenSaveCursor)
	exitAltScreen  = []byte(ansi.ResetModeAltScreenSaveCursor)
)

// CanvasSupported reports whether the full-screen TUIs should paint the GitHub
// canvas: output is a color-enabled terminal (color not disabled by NO_COLOR or
// CLICOLOR, and not TERM=dumb) that renders truecolor, so the canvas matches the
// palette exactly, and the user hasn't set GH_STACK_BACKGROUND=terminal.
func CanvasSupported(isTTY, colorEnabled bool) bool {
	return canvasSupported(os.Getenv(BackgroundEnv), os.Getenv("TERM"), isTTY, colorEnabled, lipgloss.ColorProfile())
}

func canvasSupported(background, termName string, isTTY, colorEnabled bool, profile termenv.Profile) bool {
	if strings.EqualFold(strings.TrimSpace(background), "terminal") {
		return false
	}
	if !isTTY || !colorEnabled || termName == "dumb" {
		return false
	}
	return profile == termenv.TrueColor
}

// canvasPaint returns the sequence that paints the GitHub canvas for a dark or
// light background: ColorCanvas as the terminal's default background (OSC 11)
// and ColorText as its default foreground (OSC 10).
func canvasPaint(dark bool) string {
	bg, fg := ColorCanvas.Light, ColorText.Light
	if dark {
		bg, fg = ColorCanvas.Dark, ColorText.Dark
	}
	return ansi.SetBackgroundColor(bg) + ansi.SetForegroundColor(fg)
}

// CanvasOutput is the output for a full-screen Bubble Tea program
// (tea.WithOutput) that paints the GitHub canvas while the program is on the
// alternate screen, the way the Copilot CLI does. The canvas is painted right
// after the program enters the alternate screen and reset right before it
// leaves, so the primary screen never shows it. Bubble Tea leaves the
// alternate screen when the program exits (including on panics and signals)
// and around tea.ExecProcess, which keeps the canvas out of the user's shell,
// scrollback, and $EDITOR.
//
// Resetting (OSC 111/110) returns a terminal to its configured colors, which
// drops colors set at runtime (e.g. by pywal or base16-shell) and does nothing
// on terminals without reset support. So the output also records the
// terminal's colors before painting, and Restore puts back any the reset
// didn't. While $EDITOR runs, the terminal shows its configured colors.
//
// It wraps rather than embeds the *os.File on purpose: Bubble Tea writes escape
// sequences with io.WriteString, which would call the file's WriteString and
// bypass the interception in Write. Because the wrapper is not an *os.File, a
// command run with tea.ExecProcess must set its own Stdout (e.g. os.Stdout);
// otherwise Bubble Tea hands it this output and the command sees a pipe
// instead of a terminal.
type CanvasOutput struct {
	f     *os.File
	paint string

	// savedBg and savedFg are the terminal's colors before painting, when it
	// reported both; query reads the terminal's current colors.
	savedBg, savedFg string
	query            colorQuery

	mu      sync.Mutex
	painted bool
	// touched records that the canvas was painted at least once, so Restore
	// checks that the terminal's colors came back.
	touched bool
}

// colorQuery reports the terminal's current default background and foreground
// as lowercase "#rrggbb", with "" for a color the terminal didn't report.
type colorQuery func() (bg, fg string)

// NewCanvasOutput wraps f, painting the canvas variant that matches the
// detected (or GH_STACK_THEME-forced) terminal background. It asks the
// terminal for its current colors first, so call it before the program starts
// reading input.
func NewCanvasOutput(f *os.File) *CanvasOutput {
	return newCanvasOutput(f, lipgloss.HasDarkBackground(), terminalColors(f))
}

func newCanvasOutput(f *os.File, dark bool, query colorQuery) *CanvasOutput {
	o := &CanvasOutput{f: f, paint: canvasPaint(dark), query: query}
	if bg, fg := query(); bg != "" && fg != "" && !isCanvas(bg, fg) {
		o.savedBg, o.savedFg = bg, fg
	}
	return o
}

// terminalColors queries the terminal behind f for its current default colors
// (OSC 11/10). Terminals that don't answer report "", as do multiplexers and
// Windows consoles, which termenv doesn't query.
func terminalColors(f *os.File) colorQuery {
	return func() (string, string) {
		out := termenv.NewOutput(f)
		return rgbHex(out.BackgroundColor()), rgbHex(out.ForegroundColor())
	}
}

// rgbHex returns c as lowercase "#rrggbb" when it is the terminal's answer to
// a color query, or "" for termenv's fallback guesses.
func rgbHex(c termenv.Color) string {
	if rgb, ok := c.(termenv.RGBColor); ok {
		return strings.ToLower(string(rgb))
	}
	return ""
}

// isCanvas reports whether bg and fg are a GitHub canvas left by an earlier
// run that couldn't restore the terminal (e.g. it was killed), which must not
// be put back as the user's own colors.
func isCanvas(bg, fg string) bool {
	return (bg == ColorCanvas.Dark && fg == ColorText.Dark) ||
		(bg == ColorCanvas.Light && fg == ColorText.Light)
}

// Write forwards p to the terminal, painting the canvas after an
// alternate-screen enter and resetting it before an alternate-screen exit.
func (o *CanvasOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	out := p
	if bytes.Contains(p, enterAltScreen) || bytes.Contains(p, exitAltScreen) {
		out = o.inject(p)
	}
	if _, err := o.f.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// inject returns p with the canvas paint inserted after each alternate-screen
// enter and the canvas reset inserted before each alternate-screen exit, so
// each transition and its color change reach the terminal in a single write.
func (o *CanvasOutput) inject(p []byte) []byte {
	out := make([]byte, 0, len(p)+len(o.paint)+len(canvasReset))
	for {
		enter := bytes.Index(p, enterAltScreen)
		exit := bytes.Index(p, exitAltScreen)
		switch {
		case enter >= 0 && (exit < 0 || enter < exit):
			end := enter + len(enterAltScreen)
			out = append(out, p[:end]...)
			if !o.painted {
				out = append(out, o.paint...)
				o.painted, o.touched = true, true
			}
			p = p[end:]
		case exit >= 0:
			out = append(out, p[:exit]...)
			if o.painted {
				out = append(out, canvasReset...)
				o.painted = false
			}
			out = append(out, exitAltScreen...)
			p = p[exit+len(exitAltScreen):]
		default:
			return append(out, p...)
		}
	}
}

// Restore finishes restoring the terminal once the program has returned; defer
// it right after creating the output. It resets the terminal's default colors
// if the canvas is still painted (a program that returned without leaving the
// alternate screen), then asks the terminal for its colors and puts back any
// that differ from the ones it had before painting. Only those are set
// explicitly, so a terminal whose reset brought its colors back keeps
// following its own configuration (e.g. automatic light/dark switching).
func (o *CanvasOutput) Restore() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.painted {
		o.painted = false
		_, _ = io.WriteString(o.f, canvasReset)
	}
	if !o.touched || o.savedBg == "" {
		return
	}
	o.touched = false
	bg, fg := o.query()
	var fix string
	if bg != "" && bg != o.savedBg {
		fix += ansi.SetBackgroundColor(o.savedBg)
	}
	if fg != "" && fg != o.savedFg {
		fix += ansi.SetForegroundColor(o.savedFg)
	}
	if fix != "" {
		_, _ = io.WriteString(o.f, fix)
	}
}

// Read reads from the wrapped file. With Close and Fd, it lets Bubble Tea treat
// the output as a terminal (raw mode, window size).
func (o *CanvasOutput) Read(p []byte) (int, error) { return o.f.Read(p) }

// Close closes the wrapped file.
func (o *CanvasOutput) Close() error { return o.f.Close() }

// Fd returns the wrapped file's descriptor.
func (o *CanvasOutput) Fd() uintptr { return o.f.Fd() }
