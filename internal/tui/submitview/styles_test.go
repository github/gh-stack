package submitview

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"

	"github.com/github/gh-stack/internal/tui/shared"
)

func TestStateLabel(t *testing.T) {
	cases := map[BranchState]string{
		StateNew:    "NEW",
		StateOpen:   "OPEN",
		StateDraft:  "DRAFT",
		StateQueued: "QUEUED",
		StateMerged: "MERGED",
		StateClosed: "CLOSED",
	}
	for state, want := range cases {
		assert.Equal(t, want, state.Label())
	}
}

func TestStateColorAndDot(t *testing.T) {
	for _, s := range []BranchState{StateNew, StateOpen, StateDraft, StateQueued, StateMerged, StateClosed} {
		assert.NotEqual(t, lipgloss.Color(""), s.Color(), "state %s should have a color", s.Label())
		assert.NotEmpty(t, s.Dot(), "state %s should have a dot", s.Label())
	}
}

func TestRenderBadgeContainsLabel(t *testing.T) {
	// The rendered badge may contain ANSI escapes; assert the label text is
	// present regardless of styling.
	for _, s := range []BranchState{StateNew, StateOpen, StateDraft, StateQueued, StateMerged, StateClosed} {
		out := RenderBadge(s)
		assert.Contains(t, out, s.Label())
	}
}

func TestRenderDotContainsGlyph(t *testing.T) {
	assert.Contains(t, RenderDot(StateNew), StateNew.Dot())
}

func TestRenderBadgeUsesStateBadgeColors(t *testing.T) {
	want := map[BranchState]shared.Badge{
		StateNew:    shared.BadgeBlue,
		StateOpen:   shared.BadgeGreen,
		StateDraft:  shared.BadgeGray,
		StateQueued: shared.BadgeYellow,
		StateMerged: shared.BadgePurple,
		StateClosed: shared.BadgeRed,
	}
	for _, dark := range []bool{true, false} {
		forceTrueColor(t, dark)
		for state, badge := range want {
			fg, bg := badge.Fg.Light, badge.Bg.Light
			if dark {
				fg, bg = badge.Fg.Dark, badge.Bg.Dark
			}
			out := RenderBadge(state)
			assert.Contains(t, out, sgr(fg, false), "%s text (dark=%v)", state.Label(), dark)
			assert.Contains(t, out, sgr(bg, true), "%s wash (dark=%v)", state.Label(), dark)
		}
	}
}

// TestPrimaryActionsUseBrandFill verifies the submit button, the selected
// Ready/Draft segment, and the enabled CREATE PR switch share the Primer Brand
// primary button colors: white on green.
func TestPrimaryActionsUseBrandFill(t *testing.T) {
	forceTrueColor(t, true)
	fill := sgr(string(shared.ColorPrimaryFill), true)
	ink := sgr(string(shared.ColorOnPrimary), false)
	for name, out := range map[string]string{
		"submit button":    submitButtonStyle.Render("SUBMIT 2 PRs"),
		"selected segment": segOnStyle.Render("Ready"),
		"enabled switch":   renderSwitch(true),
	} {
		assert.Contains(t, out, fill, name)
		assert.Contains(t, out, ink, name)
	}
	assert.NotContains(t, renderSwitch(false), fill, "a disabled switch is not a primary action")
}

// forceTrueColor renders with the default renderer at truecolor on a dark or
// light background for the rest of the test.
func forceTrueColor(t *testing.T, dark bool) {
	t.Helper()
	profile, bg := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(profile)
		lipgloss.SetHasDarkBackground(bg)
	})
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(dark)
}

// sgr returns the truecolor SGR parameters lipgloss emits to select hex as a
// foreground or background color.
func sgr(hex string, background bool) string {
	return termenv.TrueColor.Color(hex).Sequence(background)
}
