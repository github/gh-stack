package branch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	nonAlphanumRe    = regexp.MustCompile(`[^a-z0-9-]+`)
	multiHyphenRe    = regexp.MustCompile(`-{2,}`)
	numberedBranchRe = regexp.MustCompile(`/(\d+)$`)
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
func DateSlug(message string) string {
	date := time.Now().Format("2006-01-02")
	slug := Slugify(message)
	if slug == "" {
		return date
	}
	return date + "-" + slug
}

// FollowsNumbering returns true if branchName matches the pattern {prefix}/\d+.
func FollowsNumbering(prefix, branchName string) bool {
	if !strings.HasPrefix(branchName, prefix+"/") {
		return false
	}
	suffix := branchName[len(prefix)+1:]
	_, err := strconv.Atoi(suffix)
	return err == nil
}

// NextNumberedName scans existingBranches for the highest number matching
// {prefix}/NN and returns {prefix}/{next} with zero-padded two digits.
func NextNumberedName(prefix string, existingBranches []string) string {
	maxNum := 0
	for _, b := range existingBranches {
		if m := numberedBranchRe.FindStringSubmatch(b); m != nil {
			if strings.HasPrefix(b, prefix+"/") {
				n, _ := strconv.Atoi(m[1])
				if n > maxNum {
					maxNum = n
				}
			}
		}
	}
	return fmt.Sprintf("%s/%02d", prefix, maxNum+1)
}

// ResolveBranchName implements the full decision tree for branch name generation.
//
// Parameters:
//   - prefix: configured stack prefix (may be empty)
//   - message: commit message (from -m flag; may be empty if not using auto-naming)
//   - explicitName: branch name provided as argument (may be empty)
//   - existingBranches: current branch names in the stack
//   - numbered: true if the stack uses auto-incrementing numbered branches
//
// Returns the resolved branch name and an informational message (may be empty).
func ResolveBranchName(prefix, message, explicitName string, existingBranches []string, numbered bool) (name string, info string) {
	if explicitName != "" {
		// Explicit name provided
		if prefix != "" {
			name = prefix + "/" + explicitName
			info = fmt.Sprintf("Branch name prefixed: %s", name)
		} else {
			name = explicitName
		}
		return
	}

	// Auto-generate from message
	if message == "" {
		return "", ""
	}

	if prefix != "" {
		if numbered {
			name = NextNumberedName(prefix, existingBranches)
		} else {
			name = prefix + "/" + DateSlug(message)
		}
	} else {
		// No prefix — always use date+slug
		name = DateSlug(message)
	}

	return
}
