package branch

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	nonAlphanumRe = regexp.MustCompile(`[^a-z0-9-]+`)
	multiHyphenRe = regexp.MustCompile(`-{2,}`)
)

// Slugify converts a message into a URL/branch-safe slug.
// Lowercases, replaces special chars with hyphens, strips consecutive hyphens,
// and truncates to ~50 chars at a word boundary.
func Slugify(message string) string {
	// Normalize unicode and lowercase
	s := strings.ToLower(norm.NFKD.String(message))

	// Strip non-ASCII diacritics (combining marks)
	var b strings.Builder
	for _, r := range s {
		if !unicode.Is(unicode.Mn, r) { // Mn = nonspacing marks
			b.WriteRune(r)
		}
	}
	s = b.String()

	// Replace non-alphanumeric chars with hyphens
	s = nonAlphanumRe.ReplaceAllString(s, "-")

	// Collapse consecutive hyphens
	s = multiHyphenRe.ReplaceAllString(s, "-")

	// Trim leading/trailing hyphens
	s = strings.Trim(s, "-")

	// Truncate to ~50 chars at word boundary
	if len(s) > 50 {
		s = s[:50]
		if idx := strings.LastIndex(s, "-"); idx > 0 {
			s = s[:idx]
		}
	}

	return s
}

// DateSlug returns a branch name in the format YYYY-MM-DD-slugified-message.
// It is used to auto-generate a branch name from a commit message when no
// explicit branch name is provided.
func DateSlug(message string) string {
	date := time.Now().Format("2006-01-02")
	slug := Slugify(message)
	if slug == "" {
		return date
	}
	return date + "-" + slug
}
