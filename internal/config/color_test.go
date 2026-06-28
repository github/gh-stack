package config

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColorFuncsAreBackgroundAware verifies that, when color is enabled, the
// Config's color functions resolve to background-aware (adaptive) colors so plain
// command output adapts to the terminal like the TUIs do.
func TestColorFuncsAreBackgroundAware(t *testing.T) {
	// Force color on (even though tests have no tty) and a color-capable profile.
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	beforeProfile := lipgloss.ColorProfile()
	beforeBg := lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(beforeProfile)
		lipgloss.SetHasDarkBackground(beforeBg)
	})
	lipgloss.SetColorProfile(termenv.TrueColor)

	cfg := New()
	require.True(t, cfg.Terminal.IsColorEnabled(), "CLICOLOR_FORCE should enable color")

	for name, fn := range map[string]func(string) string{
		"ColorSuccess": cfg.ColorSuccess,
		"ColorError":   cfg.ColorError,
		"ColorWarning": cfg.ColorWarning,
		"ColorCyan":    cfg.ColorCyan,
		"ColorBlue":    cfg.ColorBlue,
		"ColorMagenta": cfg.ColorMagenta,
		"ColorGray":    cfg.ColorGray,
	} {
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
