package pr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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
