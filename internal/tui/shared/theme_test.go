package shared

import (
	"io"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
)

// TestPaletteIsBackgroundAware verifies that the palette's adaptive colors
// resolve to different output under a light vs dark background. It uses a local
// renderer with a color-capable profile so it doesn't mutate global state.
func TestPaletteIsBackgroundAware(t *testing.T) {
	colors := map[string]lipgloss.AdaptiveColor{
		"text":      ColorText,
		"textMuted": ColorTextMuted,
		"textFaint": ColorTextFaint,
		"border":    ColorBorder,
		"accent":    ColorAccent,
		"green":     ColorGreen,
		"red":       ColorRed,
		"buttonBg":  ColorButtonBg,
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

func TestApplyThemeOverride(t *testing.T) {
	// ApplyThemeOverride mutates the default renderer; restore it afterwards.
	before := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(before) })

	t.Run("light forces a light background", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(true)
		t.Setenv("GH_STACK_THEME", "light")
		ApplyThemeOverride()
		assert.False(t, lipgloss.HasDarkBackground())
	})

	t.Run("dark forces a dark background", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(false)
		t.Setenv("GH_STACK_THEME", "dark")
		ApplyThemeOverride()
		assert.True(t, lipgloss.HasDarkBackground())
	})

	t.Run("auto leaves the detected value unchanged", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(true)
		t.Setenv("GH_STACK_THEME", "auto")
		ApplyThemeOverride()
		assert.True(t, lipgloss.HasDarkBackground())
	})

	t.Run("unset leaves the detected value unchanged", func(t *testing.T) {
		lipgloss.SetHasDarkBackground(false)
		t.Setenv("GH_STACK_THEME", "")
		ApplyThemeOverride()
		assert.False(t, lipgloss.HasDarkBackground())
	})
}
