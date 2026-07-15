package pr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTemplate is a test helper that creates a file with the given content,
// creating parent directories as needed. It calls t.Fatal on any error so
// that setup failures are clearly distinguished from feature failures.
func writeTemplate(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("test setup: MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("test setup: WriteFile: %v", err)
	}
}

func TestFindTemplate_GitHubDir(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, ".github", "pull_request_template.md"), []byte("## Description\n\nFill in details."))

	got := FindTemplate(root)
	assert.Equal(t, "## Description\n\nFill in details.", got)
}

func TestFindTemplate_RootDir(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, "pull_request_template.md"), []byte("Root template"))

	got := FindTemplate(root)
	assert.Equal(t, "Root template", got)
}

func TestFindTemplate_DocsDir(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, "docs", "PULL_REQUEST_TEMPLATE.md"), []byte("Docs template"))

	got := FindTemplate(root)
	assert.Equal(t, "Docs template", got)
}

func TestFindTemplate_PriorityOrder(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, ".github", "pull_request_template.md"), []byte("github template"))
	writeTemplate(t, filepath.Join(root, "pull_request_template.md"), []byte("root template"))

	got := FindTemplate(root)
	assert.Equal(t, "github template", got)
}

func TestFindTemplate_NoTemplate(t *testing.T) {
	root := t.TempDir()

	got := FindTemplate(root)
	assert.Equal(t, "", got)
}

func TestFindTemplate_EmptyFile(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, "pull_request_template.md"), []byte("  \n  "))

	got := FindTemplate(root)
	assert.Equal(t, "", got, "empty/whitespace-only template should be treated as no template")
}

func TestFindTemplate_UpperCase(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, "PULL_REQUEST_TEMPLATE.md"), []byte("UPPER template"))

	got := FindTemplate(root)
	assert.Equal(t, "UPPER template", got)
}

// TestFindTemplate_IgnoresSymlink verifies that a symlinked PR template is not
// followed: FindTemplate ignores it rather than reading through to the file the
// symlink points at.
func TestFindTemplate_IgnoresSymlink(t *testing.T) {
	root := t.TempDir()

	// A file outside the repository that the symlinked template points at.
	linked := filepath.Join(t.TempDir(), "linked.txt")
	require.NoError(t, os.WriteFile(linked, []byte("LINKED_FILE_CONTENTS"), 0o600))

	ghDir := filepath.Join(root, ".github")
	require.NoError(t, os.MkdirAll(ghDir, 0o755))
	link := filepath.Join(ghDir, "pull_request_template.md")
	if err := os.Symlink(linked, link); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	got := FindTemplate(root)
	assert.Empty(t, got, "symlinked PR template must be ignored")
	assert.NotContains(t, got, "LINKED_FILE_CONTENTS", "symlink target contents must not be read")
}

// TestFindTemplate_StripsFrontMatter documents that discovery now delegates to
// cli/cli's githubtemplate package, which strips leading YAML front-matter from
// the template (matching `gh pr create`).
func TestFindTemplate_StripsFrontMatter(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, ".github", "pull_request_template.md"),
		[]byte("---\nname: PR\nabout: test\n---\n\n## Description\n\nBody text."))

	got := FindTemplate(root)
	assert.Equal(t, "## Description\n\nBody text.", got)
	assert.NotContains(t, got, "name: PR", "YAML front-matter should be stripped")
}

// TestFindTemplate_HyphenatedName documents the broadened filename matching that
// comes with reusing githubtemplate.FindLegacy (hyphenated variants are now
// recognized, closer to GitHub's own acceptance).
func TestFindTemplate_HyphenatedName(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, filepath.Join(root, ".github", "pull-request-template.md"), []byte("Hyphenated template"))

	got := FindTemplate(root)
	assert.Equal(t, "Hyphenated template", got)
}
