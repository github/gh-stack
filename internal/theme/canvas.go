package theme

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
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
// after the program enters the alternate screen and taken down right before it
// leaves, so the primary screen never shows it. Bubble Tea leaves the
// alternate screen when the program exits (including on panics and signals)
// and around tea.ExecProcess, which keeps the canvas out of the user's shell,
// scrollback, and $EDITOR.
//
// Resetting (OSC 111/110) returns a terminal to its configured colors, which
// drops colors set at runtime (e.g. by pywal or base16-shell) and does nothing
// on terminals without reset support. So before painting, the output plans how
// to put each color back (see planRestore) and applies that plan every time it
// leaves the alternate screen.
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
	// restore puts the terminal's colors back when the canvas comes down.
	restore string

	mu      sync.Mutex
	painted bool
}

// colorQuery reports the terminal's current default background and foreground
// as lowercase "#rrggbb", with "" for a color the terminal didn't report.
type colorQuery func() (bg, fg string)

// NewCanvasOutput wraps f, painting the canvas variant that matches the
// detected (or GH_STACK_THEME-forced) terminal background. It queries the
// terminal to plan the restore, so call it before the program starts reading
// input.
func NewCanvasOutput(f *os.File) *CanvasOutput {
	return newCanvasOutput(f, lipgloss.HasDarkBackground(), terminalColors(f))
}

func newCanvasOutput(f *os.File, dark bool, query colorQuery) *CanvasOutput {
	return &CanvasOutput{f: f, paint: canvasPaint(dark), restore: planRestore(f, query)}
}

// planRestore decides how to put each of the terminal's default colors back
// when the canvas comes down, while the terminal can still be queried. A reset
// is used when it brings the color back, so configuration changes made while
// the screen is open (e.g. an automatic light/dark switch) still apply. A color
// set at runtime, or any color on a terminal that ignores resets, is set back
// explicitly instead. To tell these apart, planRestore nudges each color
// imperceptibly, resets it, and reads back what the terminal shows, then puts
// back right away any color the reset didn't restore. Without answers from the
// terminal, it falls back to the reset.
func planRestore(w io.Writer, query colorQuery) string {
	bg, fg := query()
	if bg == "" || fg == "" || isCanvas(bg, fg) {
		return canvasReset
	}
	_, _ = io.WriteString(w, ansi.SetBackgroundColor(nudge(bg))+ansi.SetForegroundColor(nudge(fg))+canvasReset)
	resetBg, resetFg := query()
	bgPlan := restoreColor(bg, resetBg, ansi.ResetBackgroundColor, ansi.SetBackgroundColor)
	fgPlan := restoreColor(fg, resetFg, ansi.ResetForegroundColor, ansi.SetForegroundColor)
	if fix := explicit(bgPlan) + explicit(fgPlan); fix != "" {
		_, _ = io.WriteString(w, fix)
	}
	return bgPlan + fgPlan
}

// restoreColor returns how to restore a color that was before painting and
// reads afterReset once reset: the reset itself when that brought the color
// back (or the terminal stopped answering), otherwise the color, set
// explicitly.
func restoreColor(before, afterReset, reset string, set func(string) string) string {
	if afterReset == "" || afterReset == before {
		return reset
	}
	return set(before)
}

// explicit returns plan when it sets a color explicitly, or "" for a reset.
func explicit(plan string) string {
	if plan == ansi.ResetBackgroundColor || plan == ansi.ResetForegroundColor {
		return ""
	}
	return plan
}

// nudge returns hex with its blue channel shifted by a few units: a color the
// eye can't tell apart from hex, used to see whether a reset takes effect.
func nudge(hex string) string {
	if len(hex) != 7 {
		return hex
	}
	b, err := strconv.ParseUint(hex[5:], 16, 8)
	if err != nil {
		return hex
	}
	if b >= 0x80 {
		b -= 4
	} else {
		b += 4
	}
	return fmt.Sprintf("%s%02x", hex[:5], b)
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
// alternate-screen enter and restoring the terminal's colors before an
// alternate-screen exit.
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
// enter and the color restore inserted before each alternate-screen exit, so
// each transition and its color change reach the terminal in a single write.
func (o *CanvasOutput) inject(p []byte) []byte {
	out := make([]byte, 0, len(p)+len(o.paint)+len(o.restore))
	for {
		enter := bytes.Index(p, enterAltScreen)
		exit := bytes.Index(p, exitAltScreen)
		switch {
		case enter >= 0 && (exit < 0 || enter < exit):
			end := enter + len(enterAltScreen)
			out = append(out, p[:end]...)
			if !o.painted {
				out = append(out, o.paint...)
				o.painted = true
			}
			p = p[end:]
		case exit >= 0:
			out = append(out, p[:exit]...)
			if o.painted {
				out = append(out, o.restore...)
				o.painted = false
			}
			out = append(out, exitAltScreen...)
			p = p[exit+len(exitAltScreen):]
		default:
			return append(out, p...)
		}
	}
}

// Restore puts the terminal's colors back if the canvas is still painted. It is
// a safety net for a program that returns without leaving the alternate
// screen; defer it right after creating the output.
func (o *CanvasOutput) Restore() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.painted {
		return
	}
	o.painted = false
	_, _ = io.WriteString(o.f, o.restore)
}

// Read reads from the wrapped file. With Close and Fd, it lets Bubble Tea treat
// the output as a terminal (raw mode, window size).
func (o *CanvasOutput) Read(p []byte) (int, error) { return o.f.Read(p) }

// Close closes the wrapped file.
func (o *CanvasOutput) Close() error { return o.f.Close() }

// Fd returns the wrapped file's descriptor.
func (o *CanvasOutput) Fd() uintptr { return o.f.Fd() }
