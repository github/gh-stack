package pr

import (
	"strings"

	"github.com/cli/cli/v2/pkg/githubtemplate"
)

// FindTemplate searches the repository root for a default pull request
// template and returns its content with any YAML front-matter stripped.
// It returns an empty string if no template is found.
func FindTemplate(repoRoot string) string {
	path := githubtemplate.FindLegacy(repoRoot, "pull_request_template")
	if path == "" {
		return ""
	}
	return strings.TrimSpace(string(githubtemplate.ExtractContents(path)))
}
