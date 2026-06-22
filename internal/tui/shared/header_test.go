package shared

import (
	"testing"

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
