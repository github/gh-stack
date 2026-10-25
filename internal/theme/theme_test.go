package theme

import (
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPaletteIsBackgroundAware verifies that the palette's adaptive colors
// resolve to different output under a light vs dark background. It uses a local
// renderer with a color-capable profile so it doesn't mutate global state.
func TestPaletteIsBackgroundAware(t *testing.T) {
	colors := map[string]lipgloss.AdaptiveColor{
		"canvas":         ColorCanvas,
		"text":           ColorText,
		"textMuted":      ColorTextMuted,
		"textFaint":      ColorTextFaint,
		"border":         ColorBorder,
		"rowShade":       ColorRowShade,
		"codeBg":         ColorCodeBg,
		"accent":         ColorAccent,
		"green":          ColorGreen,
		"red":            ColorRed,
		"yellow":         ColorYellow,
		"blue":           ColorBlue,
		"purple":         ColorPurple,
		"gray":           ColorGray,
		"purpleDim":      ColorPurpleDim,
		"greenDim":       ColorGreenDim,
		"redDim":         ColorRedDim,
		"onInverse":      ColorOnInverse,
		"toggleOffTrack": ColorToggleOffTrack,
		"toggleOffKnob":  ColorToggleOffKnob,
	}
	for name, badge := range badges() {
		colors[name+"Fg"] = badge.Fg
		colors[name+"Bg"] = badge.Bg
	}

	for name, c := range colors {
		t.Run(name, func(t *testing.T) {
			r := lipgloss.NewRenderer(io.Discard)
			r.SetColorProfile(termenv.TrueColor)

			r.SetHasDarkBackground(true)
			dark := r.NewStyle().Foreground(c).Render("x")
			r.SetHasDarkBackground(false)
			light := r.NewStyle().Foreground(c).Render("x")

			assert.NotEqual(t, dark, light, "%s should differ between dark and light backgrounds", name)
		})
	}
}

// TestPaletteContrast guards legibility on the GitHub canvas the full-screen
// TUIs paint (and that plain output assumes): text roles meet WCAG AA (4.5:1),
// while disabled text, non-text chrome, and bold labels on the focused-row
// shade meet the 3:1 minimum for large text and UI components.
func TestPaletteContrast(t *testing.T) {
	type pair struct {
		name   string
		fg, bg lipgloss.AdaptiveColor
		min    float64
	}
	pairs := []pair{
		{"text", ColorText, ColorCanvas, 4.5},
		{"textMuted", ColorTextMuted, ColorCanvas, 4.5},
		{"textFaint", ColorTextFaint, ColorCanvas, 3},
		{"accent", ColorAccent, ColorCanvas, 4.5},
		{"green", ColorGreen, ColorCanvas, 4.5},
		{"gray", ColorGray, ColorCanvas, 4.5},
		{"yellow", ColorYellow, ColorCanvas, 4.5},
		{"purple", ColorPurple, ColorCanvas, 4.5},
		{"red", ColorRed, ColorCanvas, 4.5},
		{"onInverse on text", ColorOnInverse, ColorText, 4.5},
		{"text on code chip", ColorText, ColorCodeBg, 4.5},
		{"text on row shade", ColorText, ColorRowShade, 4.5},
		{"accent on row shade", ColorAccent, ColorRowShade, 3},
		{"green on row shade", ColorGreen, ColorRowShade, 3},
		{"yellow on row shade", ColorYellow, ColorRowShade, 3},
		{"purple on row shade", ColorPurple, ColorRowShade, 3},
		{"red on row shade", ColorRed, ColorRowShade, 3},
		{"toggle knob on track", ColorToggleOffKnob, ColorToggleOffTrack, 3},
	}
	for name, badge := range badges() {
		pairs = append(pairs, pair{"badge " + name, badge.Fg, badge.Bg, 4.5})
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			assert.GreaterOrEqual(t, contrast(t, p.fg.Dark, p.bg.Dark), p.min, "dark")
			assert.GreaterOrEqual(t, contrast(t, p.fg.Light, p.bg.Light), p.min, "light")
		})
	}

	t.Run("primary fill", func(t *testing.T) {
		fill, ink := string(ColorPrimaryFill), string(ColorOnPrimary)
		assert.GreaterOrEqual(t, contrast(t, ink, fill), 4.5, "label on the fill")
		assert.GreaterOrEqual(t, contrast(t, fill, ColorCanvas.Dark), 3.0, "fill on the dark canvas")
		assert.GreaterOrEqual(t, contrast(t, fill, ColorCanvas.Light), 3.0, "fill on the light canvas")
	})
}

func badges() map[string]Badge {
	return map[string]Badge{
		"blue":   BadgeBlue,
		"green":  BadgeGreen,
		"gray":   BadgeGray,
		"yellow": BadgeYellow,
		"purple": BadgePurple,
		"red":    BadgeRed,
	}
}

// contrast returns the WCAG 2 contrast ratio between two #rrggbb colors.
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// luminance returns the WCAG 2 relative luminance of a #rrggbb color.
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	var r, g, b uint8
	_, err := fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b)
	require.NoError(t, err, "parsing %q", hex)
	linear := func(c uint8) float64 {
		v := float64(c) / 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

func TestApplyOverride(t *testing.T) {
	// ApplyOverride mutates the default renderer; restore it afterwards.
	before := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(before) })

	t.Run("light forces a light background", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(true)
		t.Setenv("GH_STACK_THEME", "light")
		ApplyOverride()
		assert.False(t, lipgloss.HasDarkBackground())
	})

	t.Run("dark forces a dark background", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(false)
		t.Setenv("GH_STACK_THEME", "dark")
		ApplyOverride()
		assert.True(t, lipgloss.HasDarkBackground())
	})

	t.Run("auto leaves the detected value unchanged", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(true)
		t.Setenv("GH_STACK_THEME", "auto")
		ApplyOverride()
		assert.True(t, lipgloss.HasDarkBackground())
	})

	t.Run("unset leaves the detected value unchanged", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(false)
		t.Setenv("GH_STACK_THEME", "")
		ApplyOverride()
		assert.False(t, lipgloss.HasDarkBackground())
	})
}

// forceColorProfile sets the default renderer to a color-capable profile for the
// duration of a test, restoring the prior profile and background afterwards. The
// colorizers and FgSeqs use the default renderer, so this lets us assert on their
// emitted escapes deterministically.
func forceColorProfile(t *testing.T) {
	t.Helper()
	beforeProfile := lipgloss.ColorProfile()
	beforeBg := lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(beforeProfile)
		lipgloss.SetHasDarkBackground(beforeBg)
	})
	lipgloss.SetColorProfile(termenv.TrueColor)
}

func TestColorizersAreBackgroundAware(t *testing.T) {
	forceColorProfile(t)

	fns := map[string]func(string) string{
		"Success": Success,
		"Error":   Error,
		"Warning": Warning,
		"Blue":    Blue,
		"Magenta": Magenta,
		"Accent":  Accent,
		"Gray":    Gray,
	}
	for name, fn := range fns {
		t.Run(name, func(t *testing.T) {
			lipgloss.SetHasDarkBackground(true)
			dark := fn("x")
			lipgloss.SetHasDarkBackground(false)
			light := fn("x")

			assert.Contains(t, dark, "x")
			assert.NotEqual(t, dark, light, "%s should adapt to the terminal background", name)
		})
	}
}

func TestFgSeqs(t *testing.T) {
	forceColorProfile(t)

	start, reset := FgSeqs(ColorAccent)
	require.NotEmpty(t, start, "a color-capable terminal yields a start sequence")
	require.NotEmpty(t, reset, "a color-capable terminal yields a reset sequence")
	assert.True(t, strings.HasPrefix(start, "\x1b["), "start is an SGR escape")
	assert.Contains(t, reset, "\x1b[0m")
	assert.NotContains(t, start, "\x00", "the sentinel is stripped")
}

func TestFgSeqsNoColor(t *testing.T) {
	beforeProfile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(beforeProfile) })
	lipgloss.SetColorProfile(termenv.Ascii)

	start, reset := FgSeqs(ColorAccent)
	assert.Empty(t, start)
	assert.Empty(t, reset)
}
