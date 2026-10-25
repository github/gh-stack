// Package theme defines gh-stack's GitHub-branded, background-aware color
// palette and the helpers that render it, shared by both the interactive TUIs
// and ordinary command output (prompts, status messages).
//
// The palette follows the GitHub Copilot CLI's "github" theme. Neutrals (text,
// borders, and the canvas) are GitHub's own interface colors, and the chromatic
// roles (accent, PR states, status messages) use the vivid Primer Brand scales
// (https://primer.style/brand), choosing the step on each scale that keeps WCAG
// AA contrast on GitHub's dark (#0d1117) and light (#ffffff) canvases. The
// full-screen TUIs paint that canvas behind themselves (see CanvasOutput); all
// other output keeps the user's terminal background.
//
// Colors are expressed as lipgloss.AdaptiveColor, whose Light/Dark variant is
// chosen at render time from the terminal's detected background. Bubble Tea
// triggers that detection once at startup (see bubbletea/tea_init.go) — which,
// because the command package imports Bubble Tea, happens for every command —
// so the right variant is picked automatically and cached. Terminals that don't
// answer the query fall back to the dark palette.
// GH_STACK_THEME (see ApplyOverride) forces a palette when detection is wrong.
//
// Values are truecolor hex rather than ANSI palette indices so they render
// consistently across themes — notably solarized, which repurposes ANSI 8–15 as
// background tones. lipgloss downsamples to the nearest ANSI color on terminals
// without truecolor support.
package theme

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Neutrals: GitHub's interface colors (Primer fgColor/borderColor/bgColor).
var (
	// ColorCanvas is GitHub's page background. The full-screen TUIs paint it as
	// the terminal's default background (see CanvasOutput).
	ColorCanvas = lipgloss.AdaptiveColor{Dark: "#0d1117", Light: "#ffffff"}

	// ColorText is primary/emphasis ink: titles, branch names, links, active
	// keys, the description scrollbar thumb. The full-screen TUIs paint it as
	// the terminal's default foreground alongside ColorCanvas.
	ColorText = lipgloss.AdaptiveColor{Dark: "#f0f6fc", Light: "#1f2328"}
	// ColorTextMuted is secondary ink and dim chrome text: section labels,
	// shortcut descriptions, hints, trunk/merged branches, timestamps.
	ColorTextMuted = lipgloss.AdaptiveColor{Dark: "#9198a1", Light: "#59636e"}
	// ColorTextFaint is disabled/de-emphasized ink: skipped branches, disabled
	// shortcuts.
	ColorTextFaint = lipgloss.AdaptiveColor{Dark: "#656c76", Light: "#818b98"}

	// ColorBorder is structural chrome: panel borders, tree connectors, the
	// vertical spine, horizontal rules, scrollbar tracks, segmented-control frame.
	ColorBorder = lipgloss.AdaptiveColor{Dark: "#3d444d", Light: "#d1d9e0"}
	// ColorRowShade tints the focused (currently-viewed) row's background in the
	// left timeline. A neutral wash that reads as a subtle highlight on either
	// background — light gray on light terminals, and a lifted slate on dark
	// terminals so it stays visible against near-black backgrounds.
	ColorRowShade = lipgloss.AdaptiveColor{Dark: "#353941", Light: "#eaeef2"}
	// ColorCodeBg is the chip behind inline code in rendered markdown (Primer
	// neutral.muted, matching the Copilot CLI's inline-code chip).
	ColorCodeBg = lipgloss.AdaptiveColor{Dark: "#262c36", Light: "#e8e9eb"}
)

// Brand colors: Primer Brand scale steps (dark scale / light scale).
var (
	// ColorAccent is the brand blue for interactive emphasis: the current or
	// focused branch, keyboard shortcut keys, footer accents, spinners, links,
	// and commands called out in plain command output. Primer Brand blue-3 (the
	// brand focus color) on dark, and the brand link color blue-6 on light.
	ColorAccent = lipgloss.AdaptiveColor{Dark: "#3094ff", Light: "#0055d5"}

	// Semantic status colors, mirroring how GitHub colors PR states. Reused for
	// diff stats (green/red), commit SHAs and warnings (yellow), modify action
	// badges, and the success/error/warning message icons.
	ColorBlue   = ColorAccent                                               // NEW
	ColorGreen  = lipgloss.AdaptiveColor{Dark: "#5fed83", Light: "#08872b"} // OPEN, additions, success: green-2 / green-6
	ColorGray   = lipgloss.AdaptiveColor{Dark: "#9198a1", Light: "#59636e"} // DRAFT, dim
	ColorYellow = lipgloss.AdaptiveColor{Dark: "#db9d00", Light: "#a06100"} // QUEUED, warning, commit SHA: yellow-3 / yellow-6
	ColorPurple = lipgloss.AdaptiveColor{Dark: "#a665f9", Light: "#8534f3"} // MERGED, magenta: purple-3 / purple-5
	ColorRed    = lipgloss.AdaptiveColor{Dark: "#fc5c5d", Light: "#cf2230"} // CLOSED, deletions, error: red-3 / red-5

	// Dim status colors blend the PR-state hues 45% toward ColorTextFaint for
	// low-emphasis indicators (the checkout status bar), so they stay legible
	// without pulling focus from surrounding text.
	ColorPurpleDim = lipgloss.AdaptiveColor{Dark: "#8968be", Light: "#835bca"}
	ColorGreenDim  = lipgloss.AdaptiveColor{Dark: "#62b37d", Light: "#3e895c"}
	ColorRedDim    = lipgloss.AdaptiveColor{Dark: "#b86368", Light: "#ac515f"}
)

// Fills: solid backgrounds and the ink drawn on them.
var (
	// ColorPrimaryFill and ColorOnPrimary style primary actions (the submit
	// button, the selected Ready/Draft segment, an enabled switch, completed
	// merge steps) after Primer Brand's primary button: white on green in both
	// modes.
	ColorPrimaryFill = lipgloss.Color("#08872b")
	ColorOnPrimary   = lipgloss.Color("#ffffff")

	// ColorOnInverse is ink drawn on a ColorText fill (e.g. the merge wizard's
	// current step): the canvas color, so the text reads as cut out of the fill.
	ColorOnInverse = ColorCanvas

	// ColorToggleOffTrack and ColorToggleOffKnob draw a switch in its off state.
	ColorToggleOffTrack = lipgloss.AdaptiveColor{Dark: "#6e7681", Light: "#afb8c1"}
	ColorToggleOffKnob  = lipgloss.AdaptiveColor{Dark: "#1c2128", Light: "#57606a"}
)

// Badge is the color pair for a status pill: a state color on a faint wash of
// the same hue (12% of the hue over the canvas). On light backgrounds the text
// is one Primer Brand step deeper than the plain state color so every pill
// keeps AA contrast against its wash.
type Badge struct {
	Fg lipgloss.AdaptiveColor
	Bg lipgloss.AdaptiveColor
}

var (
	BadgeBlue = Badge{
		Fg: ColorBlue,
		Bg: lipgloss.AdaptiveColor{Dark: "#112133", Light: "#e0ebfa"},
	}
	BadgeGreen = Badge{
		Fg: lipgloss.AdaptiveColor{Dark: "#5fed83", Light: "#0d6731"},
		Bg: lipgloss.AdaptiveColor{Dark: "#172b24", Light: "#e1f1e6"},
	}
	BadgeGray = Badge{
		Fg: ColorGray,
		Bg: lipgloss.AdaptiveColor{Dark: "#1d2128", Light: "#ebecee"},
	}
	BadgeYellow = Badge{
		Fg: lipgloss.AdaptiveColor{Dark: "#db9d00", Light: "#834800"},
		Bg: lipgloss.AdaptiveColor{Dark: "#262214", Light: "#f4ece0"},
	}
	BadgePurple = Badge{
		Fg: lipgloss.AdaptiveColor{Dark: "#a665f9", Light: "#6619e1"},
		Bg: lipgloss.AdaptiveColor{Dark: "#1f1b32", Light: "#f0e7fe"},
	}
	BadgeRed = Badge{
		Fg: lipgloss.AdaptiveColor{Dark: "#fc5c5d", Light: "#ae0b29"},
		Bg: lipgloss.AdaptiveColor{Dark: "#2a1a1f", Light: "#f9e4e6"},
	}
)

// ApplyOverride honors the GH_STACK_THEME environment variable, forcing the
// light or dark palette regardless of what the terminal reports. It must be
// called before the first render (e.g. before any colored output or launching a
// Bubble Tea program).
//
//	GH_STACK_THEME=light  force the light palette
//	GH_STACK_THEME=dark   force the dark palette
//	GH_STACK_THEME=auto   (or unset) detect from the terminal background
//
// Use this for terminals that don't answer the background query (some SSH/tmux
// setups) and therefore mis-detect.
func ApplyOverride() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GH_STACK_THEME"))) {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	}
}

// Colorize renders s in the given adaptive color for plain (non-TUI) output. It
// emits ANSI only when the default renderer detects a color-capable terminal, so
// callers should still gate on their own color-enabled check for consistency.
func Colorize(c lipgloss.TerminalColor, s string) string {
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

// The following helpers color plain command output and prompts. They map the
// semantic roles the command layer uses onto the adaptive palette.

// Success renders s in the success (green) color.
func Success(s string) string { return Colorize(ColorGreen, s) }

// Error renders s in the error (red) color.
func Error(s string) string { return Colorize(ColorRed, s) }

// Warning renders s in the warning (yellow) color.
func Warning(s string) string { return Colorize(ColorYellow, s) }

// Blue renders s in the blue color.
func Blue(s string) string { return Colorize(ColorBlue, s) }

// Magenta renders s in the magenta/purple color.
func Magenta(s string) string { return Colorize(ColorPurple, s) }

// Accent renders s in the accent (brand blue) color.
func Accent(s string) string { return Colorize(ColorAccent, s) }

// Gray renders s in the muted gray color.
func Gray(s string) string { return Colorize(ColorGray, s) }

// Bold renders s in bold using the terminal's default foreground, which already
// contrasts with either background.
func Bold(s string) string { return lipgloss.NewStyle().Bold(true).Render(s) }

// FgSeqs returns the raw SGR escape that starts foreground rendering in c and
// the matching reset, for coloring text printed outside lipgloss (e.g. echoed
// terminal input). Both are empty when the default renderer has no color
// support.
func FgSeqs(c lipgloss.TerminalColor) (start, reset string) {
	rendered := lipgloss.NewStyle().Foreground(c).Render("\x00")
	i := strings.IndexByte(rendered, '\x00')
	if i < 0 {
		return "", ""
	}
	return rendered[:i], rendered[i+1:]
}
