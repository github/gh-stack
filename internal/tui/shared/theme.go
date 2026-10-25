package shared

import "github.com/github/gh-stack/internal/theme"

// The background-aware color palette lives in internal/theme so it can be shared
// by both the TUIs and ordinary command output. These aliases keep the TUI code
// referring to shared.ColorX.
var (
	ColorCanvas         = theme.ColorCanvas
	ColorText           = theme.ColorText
	ColorTextMuted      = theme.ColorTextMuted
	ColorTextFaint      = theme.ColorTextFaint
	ColorBorder         = theme.ColorBorder
	ColorRowShade       = theme.ColorRowShade
	ColorCodeBg         = theme.ColorCodeBg
	ColorAccent         = theme.ColorAccent
	ColorBlue           = theme.ColorBlue
	ColorGreen          = theme.ColorGreen
	ColorGray           = theme.ColorGray
	ColorYellow         = theme.ColorYellow
	ColorPurple         = theme.ColorPurple
	ColorRed            = theme.ColorRed
	ColorPurpleDim      = theme.ColorPurpleDim
	ColorGreenDim       = theme.ColorGreenDim
	ColorRedDim         = theme.ColorRedDim
	ColorPrimaryFill    = theme.ColorPrimaryFill
	ColorOnPrimary      = theme.ColorOnPrimary
	ColorOnInverse      = theme.ColorOnInverse
	ColorToggleOffTrack = theme.ColorToggleOffTrack
	ColorToggleOffKnob  = theme.ColorToggleOffKnob
)

// Badge is the color pair for a status pill (see theme.Badge).
type Badge = theme.Badge

var (
	BadgeBlue   = theme.BadgeBlue
	BadgeGreen  = theme.BadgeGreen
	BadgeGray   = theme.BadgeGray
	BadgeYellow = theme.BadgeYellow
	BadgePurple = theme.BadgePurple
	BadgeRed    = theme.BadgeRed
)
