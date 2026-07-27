package git

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rebaseTestRepo(t *testing.T) string {
	t.Helper()
	_, clone := setupBareAndClone(t)

	gitExec(t, clone, "checkout", "-b", "feature")
	writeFile(t, clone, "feature.txt", "feature")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "feature")

	gitExec(t, clone, "checkout", "main")
	writeFile(t, clone, "main.txt", "main update")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "main update")
	gitExec(t, clone, "checkout", "feature")

	return clone
}

func TestIntegration_RebaseRefusedBeforeStart(t *testing.T) {
	clone := rebaseTestRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	writeFile(t, clone, "feature.txt", "uncommitted")

	err := Rebase("main", RebaseOpts{})
	require.Error(t, err)
	assert.True(t, IsRebaseStartError(err))
	assert.False(t, IsRebaseInProgress())
}

func TestIntegration_RebaseConflictIsNotStartError(t *testing.T) {
	clone := rebaseTestRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	writeFile(t, clone, "init.txt", "feature version")
	gitExec(t, clone, "add", "init.txt")
	gitExec(t, clone, "commit", "-m", "feature conflict")

	gitExec(t, clone, "checkout", "main")
	writeFile(t, clone, "init.txt", "main version")
	gitExec(t, clone, "add", "init.txt")
	gitExec(t, clone, "commit", "-m", "main conflict")
	gitExec(t, clone, "checkout", "feature")

	err := Rebase("main", RebaseOpts{})
	require.Error(t, err)
	assert.False(t, IsRebaseStartError(err))
	assert.True(t, IsRebaseInProgress())
	gitExec(t, clone, "rebase", "--abort")
}

func TestIntegration_FetchBranchRejectsDeletedRemoteBranchWithStaleTrackingRef(t *testing.T) {
	bare, clone := setupBareAndClone(t)

	gitExec(t, clone, "checkout", "-b", "feature-trunk")
	writeFile(t, clone, "feature.txt", "feature")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "feature trunk")
	gitExec(t, clone, "push", "origin", "feature-trunk")
	staleSHA := gitExec(t, clone, "rev-parse", "origin/feature-trunk")

	other := filepath.Join(t.TempDir(), "other")
	gitExec(t, ".", "clone", bare, other)
	gitExec(t, other, "push", "origin", "--delete", "feature-trunk")

	restore := withGitDir(t, clone)
	defer restore()

	err := FetchBranch("origin", "feature-trunk")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRemoteBranchNotFound))
	assert.Equal(t, staleSHA, gitExec(t, clone, "rev-parse", "origin/feature-trunk"),
		"the stale tracking ref still exists, so callers must trust the fetch result")
}
