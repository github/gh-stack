package branch

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- Slugify: core cases for branch naming ---

func TestSlugify(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"spaces to underscores", "Hello World", "hello_world"},
		{"diacritics stripped", "café résumé", "cafe_resume"},
		{"special chars become underscores", "feat: add login!", "feat_add_login"},
		{"real hyphens preserved", "Add user-authentication", "add_user-authentication"},
		{"adjacent hyphens preserved as a hyphen", "fix--retry", "fix-retry"},
		{"hyphen next to a space preserved as a hyphen", "fix- retry", "fix-retry"},
		{"spaced dash preserved as a hyphen", "fix - retry", "fix-retry"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, Slugify(tt.input))
		})
	}

	t.Run("long string truncated at word boundary", func(t *testing.T) {
		long := "this is a very long commit message that should definitely be truncated at a word boundary"
		result := Slugify(long)
		assert.LessOrEqual(t, len(result), 30)
		assert.False(t, strings.HasSuffix(result, "_"), "should not end with a separator")
		assert.False(t, strings.HasSuffix(result, "-"), "should not end with a separator")
		assert.NotEmpty(t, result)
	})
}

// --- DateSlug: date-prefixed auto-naming ---

func TestDateSlug(t *testing.T) {
	today := time.Now().Format("01-02")

	t.Run("prefixes the date and slugifies the message", func(t *testing.T) {
		name := DateSlug("Add login")
		assert.Equal(t, today+"-add_login", name)
	})

	t.Run("empty message returns just the date", func(t *testing.T) {
		assert.Equal(t, today, DateSlug(""))
	})
}
