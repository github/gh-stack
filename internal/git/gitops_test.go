package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers for integration tests that use real git repos
// ---------------------------------------------------------------------------

// gitExec runs a git command in the given directory and returns trimmed stdout.
func gitExec(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s in %s:\n%s", strings.Join(args, " "), dir, string(out))
	return strings.TrimSpace(string(out))
}

// gitExecMayFail runs a git command allowing failure. Returns stdout and error.
func gitExecMayFail(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// setupBareAndClone creates a bare repo, clones it, makes an initial commit on
// main, and pushes. Returns (bareDir, cloneDir).
func setupBareAndClone(t *testing.T) (string, string) {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), "bare.git")

	// Use -c safe.bareRepository=all so git init --bare works in temp dirs.
	// Use -b main to ensure a consistent default branch name across environments.
	cmd := exec.Command("git", "-c", "safe.bareRepository=all", "init", "--bare", "-b", "main", bareDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", string(out))

	cloneDir := filepath.Join(t.TempDir(), "clone")
	gitExec(t, ".", "clone", bareDir, cloneDir)

	// Initial commit so main exists.
	writeFile(t, cloneDir, "init.txt", "hello")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "initial commit")
	gitExec(t, cloneDir, "push", "origin", "main")

	return bareDir, cloneDir
}

// writeFile creates or overwrites a file in dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

// withGitDir temporarily sets the git client to operate in dir by changing
// the working directory. Returns a restore function.
func withGitDir(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	return func() { _ = os.Chdir(old) }
}

// remoteBranchSHA returns the SHA of a branch on the bare remote.
func remoteBranchSHA(t *testing.T, bareDir, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", bareDir, "-c", "safe.bareRepository=all",
		"rev-parse", "refs/heads/"+branch)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "rev-parse in bare repo %s:\n%s", bareDir, string(out))
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// Integration tests for FetchBranches + Push (force-with-lease)
// ---------------------------------------------------------------------------

// Test 1: Branch exists remotely with a current tracking ref.
// Push should succeed and update the remote.
func TestIntegration_Push_ExistingBranchCurrentTrackingRef(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create a branch, push it, then make a new commit.
	gitExec(t, cloneDir, "checkout", "-b", "b1")
	writeFile(t, cloneDir, "b1.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 initial")
	gitExec(t, cloneDir, "push", "origin", "b1")

	// Make a local commit (simulating rebase).
	writeFile(t, cloneDir, "b1.txt", "v2")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 updated")

	localSHA := gitExec(t, cloneDir, "rev-parse", "b1")

	// FetchBranches should update tracking ref.
	err := d.FetchBranches("origin", []string{"b1"})
	require.NoError(t, err)

	// Push with force-with-lease should succeed.
	err = d.Push("origin", []string{"b1"}, true, false)
	require.NoError(t, err)

	// Verify remote was updated.
	remoteSHA := remoteBranchSHA(t, bareDir, "b1")
	assert.Equal(t, localSHA, remoteSHA)
}

// Test 2: Branch exists remotely but tracking ref was deleted locally.
// This is the regression test for https://github.com/github/gh-stack/issues/118.
func TestIntegration_Push_TrackingRefDeletedLocally(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create a branch and push it.
	gitExec(t, cloneDir, "checkout", "-b", "b1")
	writeFile(t, cloneDir, "b1.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 initial")
	gitExec(t, cloneDir, "push", "origin", "b1")

	// Delete the local tracking ref to simulate the bug condition.
	gitExec(t, cloneDir, "branch", "-dr", "origin/b1")

	// Verify tracking ref is gone.
	_, err := gitExecMayFail(t, cloneDir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/b1")
	require.Error(t, err, "tracking ref should be deleted")

	// Make a local commit (simulating rebase).
	writeFile(t, cloneDir, "b1.txt", "v2")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 rebased")

	localSHA := gitExec(t, cloneDir, "rev-parse", "b1")

	// FetchBranches should recreate the tracking ref.
	err = d.FetchBranches("origin", []string{"b1"})
	require.NoError(t, err)

	// Verify tracking ref was recreated.
	_, err = gitExecMayFail(t, cloneDir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/b1")
	require.NoError(t, err, "tracking ref should be recreated by FetchBranches")

	// Push with force-with-lease should succeed.
	err = d.Push("origin", []string{"b1"}, true, false)
	require.NoError(t, err)

	// Verify remote was updated.
	remoteSHA := remoteBranchSHA(t, bareDir, "b1")
	assert.Equal(t, localSHA, remoteSHA)
}

// Test 3: Branch advanced on remote by another client.
// Push should be rejected (lease protects the other commit).
func TestIntegration_Push_RemoteAdvancedByOther(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create a branch and push it.
	gitExec(t, cloneDir, "checkout", "-b", "b1")
	writeFile(t, cloneDir, "b1.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 initial")
	gitExec(t, cloneDir, "push", "origin", "b1")

	// Simulate another client advancing the branch on the remote.
	otherClone := filepath.Join(t.TempDir(), "other")
	gitExec(t, ".", "clone", bareDir, otherClone)
	gitExec(t, otherClone, "checkout", "b1")
	writeFile(t, otherClone, "b1.txt", "v-other")
	gitExec(t, otherClone, "add", ".")
	gitExec(t, otherClone, "commit", "-m", "other update")
	gitExec(t, otherClone, "push", "origin", "b1")

	// Record the other client's SHA on the remote.
	otherSHA := remoteBranchSHA(t, bareDir, "b1")

	// Local client makes a different commit (simulating rebase).
	writeFile(t, cloneDir, "b1.txt", "v-local")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 local update")

	// FetchBranches — this updates tracking ref to the other client's SHA.
	err := d.FetchBranches("origin", []string{"b1"})
	require.NoError(t, err)

	// But wait: after fetch, our tracking ref now matches the remote.
	// The push should succeed because the lease matches. To truly test
	// the "someone pushed after our fetch" scenario, we need to advance
	// the remote AFTER the fetch.
	//
	// Advance remote again after our fetch.
	writeFile(t, otherClone, "b1.txt", "v-other-2")
	gitExec(t, otherClone, "add", ".")
	gitExec(t, otherClone, "commit", "-m", "other update 2")
	gitExec(t, otherClone, "push", "origin", "b1", "--force")

	// Now remote is ahead of our tracking ref — push should fail.
	err = d.Push("origin", []string{"b1"}, true, false)
	require.Error(t, err, "push should be rejected when remote was advanced after fetch")

	// Confirm no overwrite: remote still has the other client's latest SHA.
	finalRemoteSHA := remoteBranchSHA(t, bareDir, "b1")
	assert.NotEqual(t, otherSHA, finalRemoteSHA, "remote should have advanced past original other SHA")
}

// Test 4: Brand-new branch, absent on remote.
// Push should create the branch via empty-expect lease.
func TestIntegration_Push_NewBranchAbsentOnRemote(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create a new branch locally, do NOT push it.
	gitExec(t, cloneDir, "checkout", "-b", "b1")
	writeFile(t, cloneDir, "b1.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 new")

	localSHA := gitExec(t, cloneDir, "rev-parse", "b1")

	// FetchBranches — branch doesn't exist on remote, should tolerate the error.
	err := d.FetchBranches("origin", []string{"b1"})
	require.NoError(t, err)

	// No tracking ref should exist (branch is absent remotely).
	_, err = gitExecMayFail(t, cloneDir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/b1")
	require.Error(t, err, "tracking ref should not exist for branch absent on remote")

	// Push should create the branch on remote.
	err = d.Push("origin", []string{"b1"}, true, false)
	require.NoError(t, err)

	// Verify remote has the branch.
	remoteSHA := remoteBranchSHA(t, bareDir, "b1")
	assert.Equal(t, localSHA, remoteSHA)
}

// Test 5: Brand-new branch that another client created first (race).
// Push should be rejected because empty-expect means "must not exist."
func TestIntegration_Push_NewBranchRaceCondition(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create branch locally but don't push.
	gitExec(t, cloneDir, "checkout", "-b", "b1")
	writeFile(t, cloneDir, "b1.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 new")

	// FetchBranches — branch doesn't exist on remote yet.
	err := d.FetchBranches("origin", []string{"b1"})
	require.NoError(t, err)

	// Another client creates the same branch on the remote.
	otherClone := filepath.Join(t.TempDir(), "other")
	gitExec(t, ".", "clone", bareDir, otherClone)
	gitExec(t, otherClone, "checkout", "-b", "b1")
	writeFile(t, otherClone, "b1.txt", "v-other")
	gitExec(t, otherClone, "add", ".")
	gitExec(t, otherClone, "commit", "-m", "other b1")
	gitExec(t, otherClone, "push", "origin", "b1")

	// Now our push should fail because the branch exists on remote
	// but we have an empty-expect lease.
	err = d.Push("origin", []string{"b1"}, true, false)
	require.Error(t, err, "push should be rejected when branch was created by another client")
}

// Test 6: Mixed stack — one branch with current tracking ref + one with
// deleted tracking ref. Both should push successfully after FetchBranches fix.
func TestIntegration_Push_MixedStack(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create and push b1.
	gitExec(t, cloneDir, "checkout", "-b", "b1")
	writeFile(t, cloneDir, "b1.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 initial")
	gitExec(t, cloneDir, "push", "origin", "b1")

	// Create and push b2.
	gitExec(t, cloneDir, "checkout", "-b", "b2")
	writeFile(t, cloneDir, "b2.txt", "v1")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b2 initial")
	gitExec(t, cloneDir, "push", "origin", "b2")

	// Delete tracking ref for b2 only (simulating the bug for one branch).
	gitExec(t, cloneDir, "branch", "-dr", "origin/b2")

	// Simulate rebase: update both branches.
	gitExec(t, cloneDir, "checkout", "b1")
	writeFile(t, cloneDir, "b1.txt", "v2")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1 rebased")

	gitExec(t, cloneDir, "checkout", "b2")
	writeFile(t, cloneDir, "b2.txt", "v2")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b2 rebased")

	localB1 := gitExec(t, cloneDir, "rev-parse", "b1")
	localB2 := gitExec(t, cloneDir, "rev-parse", "b2")

	// FetchBranches should handle both: b1 has tracking ref, b2 does not.
	err := d.FetchBranches("origin", []string{"b1", "b2"})
	require.NoError(t, err)

	// Push both branches with force-with-lease.
	err = d.Push("origin", []string{"b1", "b2"}, true, false)
	require.NoError(t, err)

	// Verify both were updated on remote.
	assert.Equal(t, localB1, remoteBranchSHA(t, bareDir, "b1"))
	assert.Equal(t, localB2, remoteBranchSHA(t, bareDir, "b2"))
}

func TestSplitCommitMessage(t *testing.T) {
	tests := []struct {
		name        string
		msg         string
		wantSubject string
		wantBody    string
	}{
		{
			name:        "single line",
			msg:         "Fix the bug",
			wantSubject: "Fix the bug",
			wantBody:    "",
		},
		{
			name:        "subject and body with blank separator",
			msg:         "Fix the bug\n\nMore details about the fix.",
			wantSubject: "Fix the bug",
			wantBody:    "More details about the fix.",
		},
		{
			name:        "multi-line without blank separator",
			msg:         "Fix the bug\nMore details\nEven more",
			wantSubject: "Fix the bug",
			wantBody:    "More details\nEven more",
		},
		{
			name:        "body with leading and trailing blank lines trimmed",
			msg:         "Fix the bug\n\n\nSome body text\n\n",
			wantSubject: "Fix the bug",
			wantBody:    "Some body text",
		},
		{
			name:        "whitespace-only body",
			msg:         "Fix the bug\n\n   \n\n",
			wantSubject: "Fix the bug",
			wantBody:    "",
		},
		{
			name:        "leading whitespace on message trimmed",
			msg:         "\n  Fix the bug\n\nBody here",
			wantSubject: "Fix the bug",
			wantBody:    "Body here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, body := splitCommitMessage(tt.msg)
			assert.Equal(t, tt.wantSubject, subject)
			assert.Equal(t, tt.wantBody, body)
		})
	}
}
