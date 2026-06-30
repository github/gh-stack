package theme

import (
	"io"
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
		"text":      ColorText,
		"textMuted": ColorTextMuted,
		"accent":    ColorAccent,
		"green":     ColorGreen,
		"red":       ColorRed,
		"yellow":    ColorYellow,
		"blue":      ColorBlue,
		"purple":    ColorPurple,
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
		"Cyan":    Cyan,
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
