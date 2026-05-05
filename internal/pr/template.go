package pr

import (
	"os"
	"path/filepath"
	"strings"
)

// templatePaths lists the candidate locations for a pull request template.
var templatePaths = []string{
	".github/pull_request_template.md",
	".github/PULL_REQUEST_TEMPLATE.md",
	"pull_request_template.md",
	"PULL_REQUEST_TEMPLATE.md",
	"docs/pull_request_template.md",
	"docs/PULL_REQUEST_TEMPLATE.md",
}

// FindTemplate searches the repository root for a default pull request
// template and returns its content. Returns an empty string if no template
// is found or cannot be read.
func FindTemplate(repoRoot string) string {
	for _, candidate := range templatePaths {
		path := filepath.Join(repoRoot, candidate)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content != "" {
			return content
		}
	}
	return ""
}
