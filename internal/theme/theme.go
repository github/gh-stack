// Package theme defines gh-stack's background-aware color palette and the
// helpers that render it, shared by both the interactive TUIs and ordinary
// command output (prompts, status messages).
//
// Colors are expressed as lipgloss.AdaptiveColor, whose Light/Dark variant is
// chosen at render time from the terminal's detected background. Bubble Tea
// triggers that detection once at startup (see bubbletea/tea_init.go) — which,
// because the command package imports Bubble Tea, happens for every command —
// so the right variant is picked automatically and cached. Terminals that don't
// answer the query fall back to the dark palette, preserving the original look.
// GH_STACK_THEME (see ApplyOverride) forces a palette when detection is wrong.
//
// Values are truecolor hex (GitHub Primer-inspired) rather than ANSI palette
// indices so they render consistently across themes — notably solarized, which
// repurposes ANSI 8–15 as background tones. lipgloss downsamples to the nearest
// ANSI color on terminals without truecolor support.
package theme

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// ColorText is primary/emphasis ink: titles, branch names, links, active
	// keys, the description scrollbar thumb.
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

	// ColorAccent is interactive emphasis: the current/focused branch, keyboard
	// shortcut keys, footer accents, and the cyan used in plain command output.
	ColorAccent = lipgloss.AdaptiveColor{Dark: "#2dd4bf", Light: "#0a7ea4"}

	// Semantic status colors, mirroring how GitHub colors PR states. Reused for
	// diff stats (green/red), commit SHAs and warnings (yellow), modify action
	// badges, and the success/error/warning message icons.
	ColorBlue   = lipgloss.AdaptiveColor{Dark: "#4493f8", Light: "#0969da"} // NEW, blue
	ColorGreen  = lipgloss.AdaptiveColor{Dark: "#3fb950", Light: "#1a7f37"} // OPEN, additions, success
	ColorGray   = lipgloss.AdaptiveColor{Dark: "#9198a1", Light: "#59636e"} // DRAFT, dim
	ColorYellow = lipgloss.AdaptiveColor{Dark: "#d29922", Light: "#9a6700"} // QUEUED, warning, commit SHA
	ColorPurple = lipgloss.AdaptiveColor{Dark: "#bc8cff", Light: "#8250df"} // MERGED, magenta
	ColorRed    = lipgloss.AdaptiveColor{Dark: "#f85149", Light: "#cf222e"} // CLOSED, deletions, error

	// ColorOnFill is text drawn on top of a solid colored fill (e.g. the green
	// "selected" pill): near-black on the lighter dark-mode fills, white on the
	// darker light-mode fills.
	ColorOnFill = lipgloss.AdaptiveColor{Dark: "#0d1117", Light: "#ffffff"}

	// ColorButtonFg/ColorButtonBg style the prominent inverted action button
	// (e.g. submit). The background inverts against the terminal so the button
	// stays prominent in both modes.
	ColorButtonBg = lipgloss.AdaptiveColor{Dark: "#f0f6fc", Light: "#1f2328"}
	ColorButtonFg = lipgloss.AdaptiveColor{Dark: "#0d1117", Light: "#ffffff"}
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

// Cyan renders s in the accent (cyan/teal) color.
func Cyan(s string) string { return Colorize(ColorAccent, s) }

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
