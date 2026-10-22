package shared

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestArtFitsViewport(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          bool
	}{
		{"wide and tall enough", MinWidthForArt, MinHeightForArt, true},
		{"comfortably large", 200, 60, true},
		{"too short by one (resize-artifact band)", MinWidthForArt, MinHeightForArt - 1, false},
		{"too narrow by one", MinWidthForArt - 1, MinHeightForArt, false},
		{"wide but short", 200, 20, false},
		{"tall but narrow", 40, 60, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, artFitsViewport(tt.width, tt.height))
		})
	}
}

// The logo must hide at a larger height than the header so that, while shrinking
// a tall window, the logo is gone before reaching the short heights where a
// vertical resize leaves a ghost of the inline image.
func TestLogoHidesBeforeHeader(t *testing.T) {
	assert.Greater(t, MinHeightForArt, MinHeightForHeader,
		"logo height threshold should exceed the header's so the logo hides first")

	// At heights between the two thresholds the header shows but the logo does not.
	for h := MinHeightForHeader; h < MinHeightForArt; h++ {
		assert.True(t, ShouldShowHeader(MinWidthForArt, h), "header should show at height %d", h)
		assert.False(t, artFitsViewport(MinWidthForArt, h), "logo should be hidden at height %d", h)
	}
}

func TestHeaderLinkAt(t *testing.T) {
	const url = "https://github.com/o/r/stack/7"
	cfg := HeaderConfig{
		Title: "View Stack",
		InfoLines: []HeaderInfoLine{
			{Icon: "◆", Label: "Stack #7 | o/r", LinkLabel: "#7", URL: url},
			{Icon: "○", Label: "Base: main"},
		},
		Shortcuts: []ShortcutEntry{{Key: "q", Desc: "quit"}},
	}
	tests := []struct {
		name                string
		width, height, x, y int
		want                string
	}{
		{"first cell", 100, 40, 10, 4, url},
		{"last cell", 100, 40, 11, 4, url},
		{"icon", 100, 40, 2, 4, ""},
		{"padding", 100, 40, 3, 4, ""},
		{"word", 100, 40, 4, 4, ""},
		{"space", 100, 40, 9, 4, ""},
		{"suffix", 100, 40, 12, 4, ""},
		{"other info", 100, 40, 4, 5, ""},
		{"title", 100, 40, 4, 1, ""},
		{"short", 100, MinHeightForHeader - 1, 10, 4, ""},
		{"narrow", MinWidthForHeader - 1, 40, 10, 4, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HeaderLinkAt(cfg, tt.width, tt.height, tt.x, tt.y))
		})
	}

	cfg.Shortcuts = []ShortcutEntry{{Key: "q", Desc: strings.Repeat("x", 35)}}
	assert.True(t, ShouldShowHeader(60, 40))
	assert.Empty(t, HeaderLinkAt(cfg, 60, 40, 10, 4), "info can be hidden even when the header is visible")
	var b strings.Builder
	RenderHeader(&b, cfg, 60, 40)
	assert.NotContains(t, b.String(), "Stack #7")

	cfg.Shortcuts = nil
	cfg.InfoLines[0].URL = ""
	assert.Empty(t, HeaderLinkAt(cfg, 100, 40, 10, 4))
}

func TestHeaderLinkPreservesLayout(t *testing.T) {
	plain := HeaderInfoLine{Icon: "◆", Label: "Stack #7 | o/r", LinkLabel: "#7"}
	linked := plain
	linked.URL = "https://github.com/o/r/stack/7"
	assert.Equal(t, lipgloss.Width(renderHeaderInfoLabel(plain)), lipgloss.Width(renderHeaderInfoLabel(linked)))
	assert.NotContains(t, renderHeaderInfoLabel(linked), linked.URL, "native links stay compact")
}
