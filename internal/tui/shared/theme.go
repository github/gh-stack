package shared

import "github.com/github/gh-stack/internal/theme"

// The background-aware color palette lives in internal/theme so it can be shared
// by both the TUIs and ordinary command output. These aliases keep the TUI code
// referring to shared.ColorX.
var (
	ColorText      = theme.ColorText
	ColorTextMuted = theme.ColorTextMuted
	ColorTextFaint = theme.ColorTextFaint
	ColorBorder    = theme.ColorBorder
	ColorRowShade  = theme.ColorRowShade
	ColorAccent    = theme.ColorAccent
	ColorBlue      = theme.ColorBlue
	ColorGreen     = theme.ColorGreen
	ColorGray      = theme.ColorGray
	ColorYellow    = theme.ColorYellow
	ColorPurple    = theme.ColorPurple
	ColorRed       = theme.ColorRed
	ColorOnFill    = theme.ColorOnFill
	ColorButtonBg  = theme.ColorButtonBg
	ColorButtonFg  = theme.ColorButtonFg
)
