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
		{"spaces to hyphens", "Hello World", "hello-world"},
		{"diacritics stripped", "café résumé", "cafe-resume"},
		{"special chars removed", "feat: add login!", "feat-add-login"},
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
		assert.LessOrEqual(t, len(result), 50)
		assert.False(t, strings.HasSuffix(result, "-"), "should not end with hyphen")
		assert.NotEmpty(t, result)
	})
}

// --- FollowsNumbering: pattern detection ---

func TestFollowsNumbering(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		branch   string
		expected bool
	}{
		{"matching pattern", "stack", "stack/1", true},
		{"multi-digit", "stack", "stack/42", true},
		{"non-numeric suffix", "stack", "stack/abc", false},
		{"wrong prefix", "other", "stack/1", false},
		{"empty suffix", "stack", "stack/", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, FollowsNumbering(tt.prefix, tt.branch))
		})
	}
}

// --- NextNumberedName: auto-increment ---

func TestNextNumberedName(t *testing.T) {
	t.Run("empty list starts at 01", func(t *testing.T) {
		assert.Equal(t, "prefix/01", NextNumberedName("prefix", nil))
	})

	t.Run("increments from highest", func(t *testing.T) {
		branches := []string{"prefix/01", "prefix/02"}
		assert.Equal(t, "prefix/03", NextNumberedName("prefix", branches))
	})

	t.Run("handles gaps by using max", func(t *testing.T) {
		branches := []string{"prefix/01", "prefix/05"}
		assert.Equal(t, "prefix/06", NextNumberedName("prefix", branches))
	})

	t.Run("ignores branches with different prefix", func(t *testing.T) {
		branches := []string{"other/10", "prefix/02"}
		assert.Equal(t, "prefix/03", NextNumberedName("prefix", branches))
	})
}

// --- ResolveBranchName: the full decision tree ---

func TestResolveBranchName(t *testing.T) {
	t.Run("explicit name with prefix uses slash separator", func(t *testing.T) {
		name, info := ResolveBranchName("mystack", "", "feature", nil, false)
		assert.Equal(t, "mystack/feature", name)
		assert.Contains(t, info, "prefixed")
	})

	t.Run("explicit name without prefix uses name as-is", func(t *testing.T) {
		name, info := ResolveBranchName("", "", "feature", nil, false)
		assert.Equal(t, "feature", name)
		assert.Empty(t, info)
	})

	t.Run("message with prefix and numbered uses numbered format", func(t *testing.T) {
		name, _ := ResolveBranchName("stack", "add login", "", nil, true)
		assert.Equal(t, "stack/01", name)
	})

	t.Run("message with prefix and numbered continues sequence", func(t *testing.T) {
		existing := []string{"stack/01", "stack/02"}
		name, _ := ResolveBranchName("stack", "add login", "", existing, true)
		assert.Equal(t, "stack/03", name)
	})

	t.Run("message with prefix not numbered uses date-slug", func(t *testing.T) {
		existing := []string{"stack/some-feature"}
		name, _ := ResolveBranchName("stack", "add login", "", existing, false)
		today := time.Now().Format("2006-01-02")
		assert.True(t, strings.HasPrefix(name, "stack/"+today), "expected date prefix, got: %s", name)
		assert.Contains(t, name, "add-login")
	})

	t.Run("message without prefix uses date-slug", func(t *testing.T) {
		name, _ := ResolveBranchName("", "add login", "", nil, false)
		today := time.Now().Format("2006-01-02")
		assert.True(t, strings.HasPrefix(name, today))
		assert.Contains(t, name, "add-login")
	})

	t.Run("no message no name returns empty", func(t *testing.T) {
		name, info := ResolveBranchName("stack", "", "", nil, false)
		assert.Empty(t, name)
		assert.Empty(t, info)
	})
}
