package pr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindTemplate_GitHubDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "pull_request_template.md"), []byte("## Description\n\nFill in details."), 0o644)

	got := FindTemplate(root)
	assert.Equal(t, "## Description\n\nFill in details.", got)
}

func TestFindTemplate_RootDir(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pull_request_template.md"), []byte("Root template"), 0o644)

	got := FindTemplate(root)
	assert.Equal(t, "Root template", got)
}

func TestFindTemplate_DocsDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "PULL_REQUEST_TEMPLATE.md"), []byte("Docs template"), 0o644)

	got := FindTemplate(root)
	assert.Equal(t, "Docs template", got)
}

func TestFindTemplate_PriorityOrder(t *testing.T) {
	root := t.TempDir()
	ghDir := filepath.Join(root, ".github")
	os.MkdirAll(ghDir, 0o755)
	os.WriteFile(filepath.Join(ghDir, "pull_request_template.md"), []byte("github template"), 0o644)
	os.WriteFile(filepath.Join(root, "pull_request_template.md"), []byte("root template"), 0o644)

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
	os.WriteFile(filepath.Join(root, "pull_request_template.md"), []byte("  \n  "), 0o644)

	got := FindTemplate(root)
	assert.Equal(t, "", got, "empty/whitespace-only template should be treated as no template")
}

func TestFindTemplate_UpperCase(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "PULL_REQUEST_TEMPLATE.md"), []byte("UPPER template"), 0o644)

	got := FindTemplate(root)
	assert.Equal(t, "UPPER template", got)
}
