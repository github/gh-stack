package git

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackedRepo builds a clone with main <- feature and returns the clone dir.
func stackedRepo(t *testing.T) string {
	t.Helper()
	_, clone := setupBareAndClone(t)

	// These tests drive the production rebase helpers, which shell out to git
	// without the identity env vars gitExec sets. Record it in the repo config
	// so any git process started in this clone can create commits, including on
	// machines with no global identity configured.
	gitExec(t, clone, "config", "user.name", "Test")
	gitExec(t, clone, "config", "user.email", "test@test.com")

	writeFile(t, clone, "trunk.txt", "trunk v1")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "trunk commit")
	gitExec(t, clone, "push", "origin", "main")

	gitExec(t, clone, "checkout", "-b", "feature")
	writeFile(t, clone, "feature.txt", "feature")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "feature commit")

	// Advance main so the feature branch genuinely has something to rebase onto.
	gitExec(t, clone, "checkout", "main")
	writeFile(t, clone, "trunk.txt", "trunk v2")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "trunk commit 2")
	gitExec(t, clone, "checkout", "feature")

	return clone
}

// A rebase that git refuses to start must not be reported as a success. This
// is the root cause behind "gh stack rebase says it rebased but nothing moved".
func TestIntegration_Rebase_DirtyWorkingTree_IsStartError(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	writeFile(t, clone, "feature.txt", "uncommitted edit")

	before := gitExec(t, clone, "rev-parse", "feature")
	err := Rebase("main", RebaseOpts{})

	require.Error(t, err, "a rebase git refused must not report success")
	assert.True(t, IsRebaseStartError(err), "want RebaseStartError, got %v", err)
	assert.False(t, IsRebaseInProgress(), "no rebase state should be left behind")
	assert.Equal(t, before, gitExec(t, clone, "rev-parse", "feature"),
		"the branch must not have moved")
}

func TestIntegration_RebaseOnto_BranchInAnotherWorktree_IsStartError(t *testing.T) {
	clone := stackedRepo(t)

	gitExec(t, clone, "checkout", "-b", "upper")
	writeFile(t, clone, "upper.txt", "upper")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "upper commit")
	gitExec(t, clone, "checkout", "main")

	wt := filepath.Join(t.TempDir(), "wt")
	gitExec(t, clone, "worktree", "add", wt, "upper")

	restore := withGitDir(t, clone)
	defer restore()

	before := gitExec(t, clone, "rev-parse", "upper")
	err := RebaseOnto("main", "feature", "upper", RebaseOpts{})

	require.Error(t, err)
	assert.True(t, IsRebaseStartError(err), "want RebaseStartError, got %v", err)
	assert.Contains(t, err.Error(), "worktree", "git's own message should be preserved")
	assert.Equal(t, before, gitExec(t, clone, "rev-parse", "upper"))
}

func TestIntegration_Rebase_UnknownUpstream_IsStartError(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	err := Rebase("no-such-branch", RebaseOpts{})
	require.Error(t, err)
	assert.True(t, IsRebaseStartError(err), "want RebaseStartError, got %v", err)
}

// A rebase started while another is unresolved must be reported up front, not
// mistaken for this rebase's own conflicts.
func TestIntegration_Rebase_AlreadyInProgress_IsStartError(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	// Produce a real conflict so a rebase is genuinely in progress.
	gitExec(t, clone, "checkout", "feature")
	writeFile(t, clone, "trunk.txt", "conflicting")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "conflict with trunk")

	err := Rebase("main", RebaseOpts{})
	require.Error(t, err, "expected a conflict")
	assert.False(t, IsRebaseStartError(err), "a real conflict is not a start error")
	require.True(t, IsRebaseInProgress())

	// A second rebase must not be reported as either success or a new conflict.
	err = Rebase("main", RebaseOpts{})
	require.Error(t, err)
	assert.True(t, IsRebaseStartError(err), "want RebaseStartError, got %v", err)

	gitExec(t, clone, "rebase", "--abort")
}

// A successful rebase still reports success.
func TestIntegration_Rebase_Success(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	require.NoError(t, Rebase("main", RebaseOpts{}))

	mainSHA := gitExec(t, clone, "rev-parse", "main")
	isAnc, err := IsAncestor(mainSHA, "feature")
	require.NoError(t, err)
	assert.True(t, isAnc, "feature should contain the trunk tip after rebasing")
}

// git rejects "git rebase <option> --continue" with a usage error, which made
// --committer-date-is-author-date break every --continue. The option is
// persisted in the rebase state, so --continue alone honors it.
func TestIntegration_RebaseContinue_WithCommitterDateOption(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	gitExec(t, clone, "checkout", "feature")
	writeFile(t, clone, "trunk.txt", "conflicting")
	gitExec(t, clone, "add", ".")
	gitExec(t, clone, "commit", "-m", "conflict with trunk")

	opts := RebaseOpts{CommitterDateIsAuthorDate: true}
	require.Error(t, Rebase("main", opts), "expected a conflict")
	require.True(t, IsRebaseInProgress())

	writeFile(t, clone, "trunk.txt", "resolved")
	gitExec(t, clone, "add", "trunk.txt")

	require.NoError(t, RebaseContinue(opts))
	assert.False(t, IsRebaseInProgress())

	mainSHA := gitExec(t, clone, "rev-parse", "main")
	isAnc, err := IsAncestor(mainSHA, "feature")
	require.NoError(t, err)
	assert.True(t, isAnc)
}

// git rebase runs fine with untracked files present, so the tracked-only check
// must not report them as dirty.
func TestIntegration_HasUncommittedTrackedChanges(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	tracked, err := HasUncommittedTrackedChanges()
	require.NoError(t, err)
	assert.False(t, tracked, "a clean tree has no tracked changes")

	writeFile(t, clone, "scratch.local", "notes")
	tracked, err = HasUncommittedTrackedChanges()
	require.NoError(t, err)
	assert.False(t, tracked, "untracked files must not count as dirty")

	all, err := HasUncommittedChanges()
	require.NoError(t, err)
	assert.True(t, all, "untracked files do make the tree dirty overall")

	// git itself accepts a rebase in this state.
	assert.NoError(t, Rebase("main", RebaseOpts{}))

	writeFile(t, clone, "feature.txt", "edited")
	tracked, err = HasUncommittedTrackedChanges()
	require.NoError(t, err)
	assert.True(t, tracked, "an edited tracked file is dirty")
}

func TestIntegration_StashPushPop(t *testing.T) {
	clone := stackedRepo(t)
	restore := withGitDir(t, clone)
	defer restore()

	writeFile(t, clone, "feature.txt", "work in progress")
	require.NoError(t, StashPush("gh-stack: autostash"))

	tracked, err := HasUncommittedTrackedChanges()
	require.NoError(t, err)
	assert.False(t, tracked, "the working tree should be clean after stashing")

	// The rebase git previously refused now runs.
	require.NoError(t, Rebase("main", RebaseOpts{}))

	require.NoError(t, StashPop())
	content, err := exec.Command("cat", filepath.Join(clone, "feature.txt")).Output()
	require.NoError(t, err)
	assert.Equal(t, "work in progress", string(content), "local changes should be restored")

	mainSHA := gitExec(t, clone, "rev-parse", "main")
	isAnc, err := IsAncestor(mainSHA, "feature")
	require.NoError(t, err)
	assert.True(t, isAnc, "the stack should still have been rebased")
}

// FetchBranches must tolerate a branch that does not exist on the remote, but
// surface any other failure so callers do not print "Fetched latest changes".
func TestIntegration_FetchBranches_ErrorReporting(t *testing.T) {
	_, clone := setupBareAndClone(t)
	restore := withGitDir(t, clone)
	defer restore()

	assert.NoError(t, FetchBranches("origin", []string{"main"}))
	assert.NoError(t, FetchBranches("origin", []string{"main", "never-pushed"}),
		"a branch missing on the remote is expected and tolerated")
	assert.Error(t, FetchBranches("no-such-remote", []string{"main"}),
		"an unusable remote must be reported")
}
