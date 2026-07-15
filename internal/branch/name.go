package branch

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	nonAllowedRe = regexp.MustCompile(`[^a-z0-9-]+`)
	multiSepRe   = regexp.MustCompile(`[-_]{2,}`)
)

// Slugify converts a message into a URL/branch-safe slug.
// Lowercases, replaces spaces and other disallowed characters with underscores
// (any hyphens already present in the message are preserved), collapses runs of
// adjacent separators, and truncates to ~30 chars at a word boundary.
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

	// Replace runs of disallowed chars (spaces, punctuation, …) with a single
	// underscore. Hyphens present in the message are allowed and preserved.
	s = nonAllowedRe.ReplaceAllString(s, "_")

	// Collapse any run of adjacent separators into a single underscore.
	s = multiSepRe.ReplaceAllString(s, "_")

	// Trim leading/trailing separators
	s = strings.Trim(s, "-_")

	// Truncate to ~30 chars at a word boundary (underscore)
	if len(s) > 30 {
		s = s[:30]
		if idx := strings.LastIndex(s, "_"); idx > 0 {
			s = s[:idx]
		}
		s = strings.Trim(s, "-_")
	}

	return s
}

// DateSlug returns a branch name in the format MM-DD-slugified-message.
// It is used to auto-generate a branch name from a commit message when no
// explicit branch name is provided.
func DateSlug(message string) string {
	date := time.Now().Format("01-02")
	slug := Slugify(message)
	if slug == "" {
		return date
	}
	return date + "-" + slug
}
