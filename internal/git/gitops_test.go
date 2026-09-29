package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	cligit "github.com/cli/cli/v2/git"
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

func requireGitState(t *testing.T, query func() (bool, error)) bool {
	t.Helper()
	value, err := query()
	require.NoError(t, err)
	return value
}

func requireWorktree(t *testing.T, parent Ops, path string) Ops {
	t.Helper()
	scoped, err := parent.ForWorktree(path)
	require.NoError(t, err)
	require.NotNil(t, scoped)
	return scoped
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

// A stack branch whose name begins with "+" must push its own ref, not a
// similarly named sibling. A leading "+" in a git refspec means "force update",
// so passing a bare "+feature" (or "+feature:...") lets git treat it as a
// refspec modifier for "feature". Fully-qualified refspecs keep the "+" part of
// the branch name. Force path (used by push/submit).
func TestIntegration_Push_PlusPrefixedBranch_Force(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create and push a normal "feature" branch (content A).
	gitExec(t, cloneDir, "checkout", "-b", "feature")
	writeFile(t, cloneDir, "feature.txt", "A")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "feature A")
	gitExec(t, cloneDir, "push", "origin", "feature")
	remoteFeatureBefore := remoteBranchSHA(t, bareDir, "feature")

	// Create a local "+feature" branch off main with different content (B).
	gitExec(t, cloneDir, "checkout", "main")
	gitExec(t, cloneDir, "checkout", "-b", "+feature")
	writeFile(t, cloneDir, "plus.txt", "B")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "plus B")
	localPlus := gitExec(t, cloneDir, "rev-parse", "refs/heads/+feature")

	// Advance local "feature" (content C) but do NOT push it. If the push
	// followed refspec syntax, "+feature" would push this ref instead.
	gitExec(t, cloneDir, "checkout", "feature")
	writeFile(t, cloneDir, "feature.txt", "C")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "feature C")
	localFeature := gitExec(t, cloneDir, "rev-parse", "refs/heads/feature")
	require.NotEqual(t, localPlus, localFeature, "test setup: +feature and feature must differ")

	// Push the "+feature" stack branch via the force path.
	gitExec(t, cloneDir, "checkout", "+feature")
	require.NoError(t, d.FetchBranches("origin", []string{"+feature"}))
	require.NoError(t, d.Push("origin", []string{"+feature"}, true, false))

	// Remote "+feature" must point at local "+feature" (B), and remote
	// "feature" must be untouched (still A).
	assert.Equal(t, localPlus, remoteBranchSHA(t, bareDir, "+feature"),
		"remote +feature should hold the +feature commit, not feature's")
	assert.Equal(t, remoteFeatureBefore, remoteBranchSHA(t, bareDir, "feature"),
		"remote feature must not be force-updated")
}

// Same as above for the non-force, atomic path (used by link and by sync when
// no rebase happened). A bare "+feature" operand would force-update remote
// "feature"; a fully-qualified refspec creates remote "+feature" instead.
func TestIntegration_Push_PlusPrefixedBranch_NonForce(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	d := &defaultOps{}

	// Create and push a normal "feature" branch (content A).
	gitExec(t, cloneDir, "checkout", "-b", "feature")
	writeFile(t, cloneDir, "feature.txt", "A")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "feature A")
	gitExec(t, cloneDir, "push", "origin", "feature")
	remoteFeatureBefore := remoteBranchSHA(t, bareDir, "feature")

	// Create a local "+feature" branch off main with different content (B).
	gitExec(t, cloneDir, "checkout", "main")
	gitExec(t, cloneDir, "checkout", "-b", "+feature")
	writeFile(t, cloneDir, "plus.txt", "B")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "plus B")
	localPlus := gitExec(t, cloneDir, "rev-parse", "refs/heads/+feature")

	// Advance local "feature" (content C) but do NOT push it.
	gitExec(t, cloneDir, "checkout", "feature")
	writeFile(t, cloneDir, "feature.txt", "C")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "feature C")

	// Push "+feature" via the non-force, atomic path.
	gitExec(t, cloneDir, "checkout", "+feature")
	require.NoError(t, d.Push("origin", []string{"+feature"}, false, true))

	// Remote "+feature" must be created from local "+feature" (B), and remote
	// "feature" must be untouched (still A).
	assert.Equal(t, localPlus, remoteBranchSHA(t, bareDir, "+feature"),
		"remote +feature should be created from the +feature commit")
	assert.Equal(t, remoteFeatureBefore, remoteBranchSHA(t, bareDir, "feature"),
		"remote feature must not be force-updated")
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

// ---------------------------------------------------------------------------
// Integration tests for saved remote (gh-stack.remote)
// ---------------------------------------------------------------------------

func TestIntegration_ResolveRemote_UsesSavedRemote(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	restoreDir := withGitDir(t, cloneDir)
	defer restoreDir()

	// Create a branch without upstream tracking.
	gitExec(t, cloneDir, "checkout", "-b", "feature")

	// Add a second remote so there are multiple.
	gitExec(t, cloneDir, "remote", "add", "upstream", cloneDir)

	// Without saved remote, multiple remotes should return ErrMultipleRemotes.
	_, err := ResolveRemote("feature")
	var multi *ErrMultipleRemotes
	require.ErrorAs(t, err, &multi)

	// Save a remote preference.
	gitExec(t, cloneDir, "config", "gh-stack.remote", "upstream")

	// Now ResolveRemote should return the saved remote.
	remote, err := ResolveRemote("feature")
	require.NoError(t, err)
	assert.Equal(t, "upstream", remote)
}

func TestIntegration_ResolveRemote_GitPushConfigTakesPrecedence(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	restoreDir := withGitDir(t, cloneDir)
	defer restoreDir()

	// Add a second remote.
	gitExec(t, cloneDir, "remote", "add", "upstream", cloneDir)

	// Save gh-stack.remote to "upstream".
	gitExec(t, cloneDir, "config", "gh-stack.remote", "upstream")

	// Set standard git push config to "origin" — this should take precedence.
	gitExec(t, cloneDir, "config", "remote.pushDefault", "origin")

	remote, err := ResolveRemote("main")
	require.NoError(t, err)
	assert.Equal(t, "origin", remote)
}

func TestIntegration_SaveAndGetRemote(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	restoreDir := withGitDir(t, cloneDir)
	defer restoreDir()

	// Initially no saved remote.
	_, err := GetSavedRemote()
	require.Error(t, err)

	// Save a remote.
	require.NoError(t, SaveRemote("upstream"))

	// Should be retrievable.
	saved, err := GetSavedRemote()
	require.NoError(t, err)
	assert.Equal(t, "upstream", saved)

	// Clear it.
	require.NoError(t, ClearRemote())

	// Should be gone.
	_, err = GetSavedRemote()
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Integration tests for cherry-pick in-progress detection and abort
// ---------------------------------------------------------------------------

// A conflicting cherry-pick must be detected as in-progress, and CherryPickAbort
// must fully restore the working tree/index so branch checkouts succeed again.
// This underpins modify's --abort recovery for fold-down (cherry-pick) conflicts.
func TestIntegration_CherryPickInProgressAndAbort(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	// Ensure runSilent-based git commands have a committer identity.
	gitExec(t, cloneDir, "config", "user.name", "Test")
	gitExec(t, cloneDir, "config", "user.email", "test@test.com")

	// feature edits conflict.txt one way; main edits it another way.
	gitExec(t, cloneDir, "checkout", "-b", "feature")
	writeFile(t, cloneDir, "conflict.txt", "feature change\n")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "feature edit")
	featureSHA := gitExec(t, cloneDir, "rev-parse", "feature")

	gitExec(t, cloneDir, "checkout", "main")
	writeFile(t, cloneDir, "conflict.txt", "main change\n")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "main edit")

	// No cherry-pick in progress before we start.
	assert.False(t, requireGitState(t, IsCherryPickInProgress), "no cherry-pick should be in progress initially")

	// Cherry-picking feature onto main conflicts.
	err := CherryPick([]string{featureSHA})
	require.Error(t, err, "cherry-pick should conflict")
	assert.True(t, requireGitState(t, IsCherryPickInProgress), "cherry-pick should be in progress after a conflict")

	// While mid-conflict, a plain checkout must fail (unmerged index).
	_, coErr := gitExecMayFail(t, cloneDir, "checkout", "feature")
	require.Error(t, coErr, "checkout should fail while cherry-pick index is unmerged")

	// Aborting must fully restore: no longer in progress, clean tree, checkout works.
	require.NoError(t, CherryPickAbort())
	assert.False(t, requireGitState(t, IsCherryPickInProgress), "cherry-pick should not be in progress after abort")

	status, err := gitExecMayFail(t, cloneDir, "status", "--porcelain")
	require.NoError(t, err)
	assert.Empty(t, status, "working tree should be clean after abort")

	_, coErr = gitExecMayFail(t, cloneDir, "checkout", "feature")
	require.NoError(t, coErr, "checkout should succeed after abort restores a clean index")
}

// CherryPickQuit clears the sequencer state but intentionally leaves the index
// as-is, so a plain checkout still fails. This documents why Unwind uses the
// full --abort rather than --quit.
func TestIntegration_CherryPickQuitLeavesIndexUnmerged(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	restore := withGitDir(t, cloneDir)
	defer restore()

	gitExec(t, cloneDir, "config", "user.name", "Test")
	gitExec(t, cloneDir, "config", "user.email", "test@test.com")

	gitExec(t, cloneDir, "checkout", "-b", "feature")
	writeFile(t, cloneDir, "conflict.txt", "feature change\n")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "feature edit")
	featureSHA := gitExec(t, cloneDir, "rev-parse", "feature")

	gitExec(t, cloneDir, "checkout", "main")
	writeFile(t, cloneDir, "conflict.txt", "main change\n")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "main edit")

	require.Error(t, CherryPick([]string{featureSHA}))
	require.True(t, requireGitState(t, IsCherryPickInProgress))

	// --quit clears sequencer state (no longer "in progress") ...
	CherryPickQuit()
	assert.False(t, requireGitState(t, IsCherryPickInProgress), "quit should clear cherry-pick sequencer state")

	// ... but leaves the unmerged index behind, so checkout still fails.
	_, coErr := gitExecMayFail(t, cloneDir, "checkout", "feature")
	require.Error(t, coErr, "checkout should still fail after --quit because the index is unmerged")
}

// ---------------------------------------------------------------------------
// Real linked-worktree integration tests
// ---------------------------------------------------------------------------

func setupWorktreeRepo(t *testing.T) (*defaultOps, string) {
	t.Helper()
	_, dir := setupBareAndClone(t)
	gitExec(t, dir, "config", "user.name", "Test")
	gitExec(t, dir, "config", "user.email", "test@test.com")
	gitExec(t, dir, "config", "commit.gpgsign", "false")
	gitExec(t, dir, "config", "rerere.enabled", "false")
	gitExec(t, dir, "config", "merge.conflictStyle", "merge")
	gitExec(t, dir, "config", "rebase.backend", "merge")
	t.Cleanup(withGitDir(t, dir))
	return &defaultOps{client: &cligit.Client{RepoDir: dir}}, dir
}

func canonicalGitTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return filepath.ToSlash(resolved)
}

func addTestWorktree(t *testing.T, root *defaultOps, dir, branch string) (Ops, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), branch+" worktree")
	gitExec(t, dir, "worktree", "add", "-b", branch, path, "main")
	scoped := requireWorktree(t, root, path)
	got, err := scoped.CurrentBranch()
	require.NoError(t, err)
	require.Equal(t, branch, got)
	return scoped, path
}

func TestIntegration_WorktreeDirectoriesAndDiscovery(t *testing.T) {
	root, dir := setupWorktreeRepo(t)
	beforeWD, err := os.Getwd()
	require.NoError(t, err)
	beforeClientDir := client.RepoDir
	beforeOps := CurrentOps()

	linked, linkedPath := addTestWorktree(t, root, dir, "feature-\u03bb")
	gitExec(t, dir, "worktree", "lock", "--reason", "a reason\nwith newlines", linkedPath)
	detachedPath := filepath.Join(t.TempDir(), "detached")
	gitExec(t, dir, "worktree", "add", "--detach", detachedPath, "main")
	missingPath := filepath.Join(t.TempDir(), "missing")
	gitExec(t, dir, "worktree", "add", "-b", "missing", missingPath, "main")
	canonicalMissing := canonicalGitTestPath(t, missingPath)
	require.NoError(t, os.Rename(missingPath, missingPath+"-moved"))

	common, err := root.CommonDir()
	require.NoError(t, err)
	mainGitDir, err := root.GitDir()
	require.NoError(t, err)
	assert.Equal(t, canonicalGitTestPath(t, filepath.Join(dir, ".git")), common)
	assert.Equal(t, common, mainGitDir)
	linkedCommon, err := linked.CommonDir()
	require.NoError(t, err)
	linkedGitDir, err := linked.GitDir()
	require.NoError(t, err)
	assert.Equal(t, common, linkedCommon)
	assert.NotEqual(t, common, linkedGitDir)
	assert.True(t, filepath.IsAbs(linkedGitDir))
	assert.True(t, strings.HasPrefix(filepath.Clean(linkedGitDir), filepath.Join(common, "worktrees")+string(filepath.Separator)))

	subdir := filepath.Join(linkedPath, "sub", "directory")
	require.NoError(t, os.MkdirAll(subdir, 0755))
	sub := requireWorktree(t, linked, filepath.Join("sub", "directory"))
	subRoot, err := sub.RootDir()
	require.NoError(t, err)
	assert.Equal(t, canonicalGitTestPath(t, linkedPath), subRoot)
	subGitDir, err := sub.GitDir()
	require.NoError(t, err)
	assert.Equal(t, linkedGitDir, subGitDir)
	subCommon, err := sub.CommonDir()
	require.NoError(t, err)
	assert.Equal(t, common, subCommon)

	worktrees, err := sub.Worktrees()
	require.NoError(t, err)
	require.Len(t, worktrees, 4)
	assert.Equal(t, canonicalGitTestPath(t, dir), worktrees[0].Path, "the main owner must not be omitted")
	assert.ElementsMatch(t, []Worktree{
		{Path: canonicalGitTestPath(t, dir), Branch: "main"},
		{Path: canonicalGitTestPath(t, linkedPath), Branch: "feature-\u03bb", Locked: true},
		{Path: canonicalGitTestPath(t, detachedPath), Detached: true},
		{Path: canonicalMissing, Branch: "missing", Prunable: true},
	}, worktrees)

	main := requireWorktree(t, linked, dir)
	branch, err := main.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
	afterWD, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, beforeWD, afterWD)
	assert.Equal(t, beforeClientDir, client.RepoDir)
	assert.Same(t, beforeOps, CurrentOps())
}

func TestIntegration_WorktreePathsPreserveWhitespace(t *testing.T) {
	for _, name := range []string{"space and \u03bb", " leading and trailing ", "embedded\nand trailing\n", `quotes " and backslash \`} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && (strings.ContainsAny(name, "\n\"\\") || strings.HasSuffix(name, " ")) {
				t.Skip("these filename characters are not supported on Windows")
			}
			root, dir := setupWorktreeRepo(t)
			mainPath := filepath.Join(filepath.Dir(dir), "main "+name)
			// Windows cannot rename the process's current directory.
			require.NoError(t, os.Chdir(filepath.Dir(dir)))
			require.NoError(t, os.Rename(dir, mainPath))
			root.client.RepoDir = mainPath
			linkedPath := filepath.Join(t.TempDir(), name)
			gitExec(t, mainPath, "worktree", "add", "-b", "feature", linkedPath, "main")
			linked := requireWorktree(t, root, linkedPath)
			gotRoot, err := linked.RootDir()
			require.NoError(t, err)
			assert.Equal(t, canonicalGitTestPath(t, linkedPath), gotRoot)
			common, err := linked.CommonDir()
			require.NoError(t, err)
			assert.Equal(t, canonicalGitTestPath(t, filepath.Join(mainPath, ".git")), common)
			worktrees, err := linked.Worktrees()
			require.NoError(t, err)
			assert.ElementsMatch(t, []Worktree{
				{Path: canonicalGitTestPath(t, mainPath), Branch: "main"},
				{Path: canonicalGitTestPath(t, linkedPath), Branch: "feature"},
			}, worktrees)
		})
	}
}

func TestIntegration_BareHostedWorktree(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	bare, _ := setupBareAndClone(t)
	linkedPath := filepath.Join(t.TempDir(), "linked")
	gitExec(t, bare, "-c", "safe.bareRepository=all", "worktree", "add", "-b", "feature", linkedPath, "main")
	t.Cleanup(withGitDir(t, linkedPath))
	root := &defaultOps{client: &cligit.Client{RepoDir: linkedPath}}
	linked := requireWorktree(t, root, linkedPath)
	common, err := linked.CommonDir()
	require.NoError(t, err)
	assert.Equal(t, canonicalGitTestPath(t, bare), common)
	gitDir, err := linked.GitDir()
	require.NoError(t, err)
	assert.NotEqual(t, common, gitDir)
	worktrees, err := linked.Worktrees()
	require.NoError(t, err)
	assert.ElementsMatch(t, []Worktree{
		{Path: canonicalGitTestPath(t, bare), Bare: true},
		{Path: canonicalGitTestPath(t, linkedPath), Branch: "feature"},
	}, worktrees)
}

func setupSeparateGitRepo(t *testing.T) (*defaultOps, string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "main")
	gitDir := filepath.Join(t.TempDir(), "separate git directory")
	gitExec(t, ".", "init", "-b", "main", "--separate-git-dir", gitDir, dir)
	writeFile(t, dir, "init.txt", "initial")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "initial")
	t.Cleanup(withGitDir(t, dir))
	root := &defaultOps{client: &cligit.Client{RepoDir: dir}}
	return root, dir, gitDir
}

func TestIntegration_SeparateGitDirectory(t *testing.T) {
	root, dir, gitDir := setupSeparateGitRepo(t)
	linked, linkedPath := addTestWorktree(t, root, dir, "feature")
	for _, scope := range []Ops{root, linked} {
		common, err := scope.CommonDir()
		require.NoError(t, err)
		assert.Equal(t, canonicalGitTestPath(t, gitDir), common)
	}
	mainGitDir, err := root.GitDir()
	require.NoError(t, err)
	assert.Equal(t, canonicalGitTestPath(t, gitDir), mainGitDir)
	linkedRoot, err := linked.RootDir()
	require.NoError(t, err)
	assert.Equal(t, canonicalGitTestPath(t, linkedPath), linkedRoot)
	linkedGitDir, err := linked.GitDir()
	require.NoError(t, err)
	assert.NotEqual(t, mainGitDir, linkedGitDir)
	subdir := filepath.Join(dir, "subdirectory")
	require.NoError(t, os.MkdirAll(subdir, 0755))
	for _, scope := range []Ops{root, requireWorktree(t, root, dir), requireWorktree(t, root, subdir)} {
		worktrees, err := scope.Worktrees()
		require.NoError(t, err)
		assert.Equal(t, canonicalGitTestPath(t, dir), worktrees[0].Path)
		assert.Equal(t, "main", worktrees[0].Branch)
	}
	main := requireWorktree(t, linked, dir)
	mainRoot, err := main.RootDir()
	require.NoError(t, err)
	assert.Equal(t, canonicalGitTestPath(t, dir), mainRoot)
	mainBranch, err := main.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", mainBranch)
}

func TestIntegration_SeparateGitDirectoryBacklink(t *testing.T) {
	tests := []struct {
		name           string
		relative       bool
		worktreeConfig bool
		newlines       bool
	}{
		{name: "common absolute"},
		{name: "common relative", relative: true},
		{name: "main config.worktree", worktreeConfig: true},
		{name: "relative main config.worktree", relative: true, worktreeConfig: true},
		{name: "newline path", worktreeConfig: true, newlines: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.newlines && runtime.GOOS == "windows" {
				t.Skip("Windows does not support newlines in filenames")
			}
			root, dir, gitDir := setupSeparateGitRepo(t)
			if tt.newlines {
				newPath := dir + " with \u03bb\nand trailing\n"
				require.NoError(t, os.Rename(dir, newPath))
				dir = newPath
				root.client.RepoDir = dir
			}
			linked, linkedPath := addTestWorktree(t, root, dir, "feature")
			backlink := canonicalGitTestPath(t, dir)
			if tt.relative {
				var err error
				backlink, err = filepath.Rel(canonicalGitTestPath(t, gitDir), backlink)
				require.NoError(t, err)
			}
			if tt.worktreeConfig {
				gitExec(t, dir, "config", "extensions.worktreeConfig", "true")
				gitExec(t, dir, "config", "--worktree", "core.worktree", backlink)
				gitExec(t, linkedPath, "config", "--worktree", "core.worktree", canonicalGitTestPath(t, linkedPath))
			} else {
				gitExec(t, dir, "config", "core.worktree", backlink)
			}
			worktrees, err := linked.Worktrees()
			require.NoError(t, err)
			require.Len(t, worktrees, 2)
			assert.Equal(t, canonicalGitTestPath(t, dir), worktrees[0].Path)
			assert.Equal(t, "main", worktrees[0].Branch)
			main := requireWorktree(t, linked, worktrees[0].Path)
			mainRoot, err := main.RootDir()
			require.NoError(t, err)
			assert.Equal(t, canonicalGitTestPath(t, dir), mainRoot)
			mainBranch, err := main.CurrentBranch()
			require.NoError(t, err)
			assert.Equal(t, "main", mainBranch)
			linkedBranch, err := linked.CurrentBranch()
			require.NoError(t, err)
			assert.Equal(t, "feature", linkedBranch)
		})
	}
}

func TestIntegration_SeparateGitDirectoryMissingBacklink(t *testing.T) {
	root, dir, gitDir := setupSeparateGitRepo(t)
	linked, linkedPath := addTestWorktree(t, root, dir, "feature")
	worktrees, err := linked.Worktrees()
	require.NoError(t, err, "an unknown main path must not block unrelated worktrees")
	require.Len(t, worktrees, 2)
	assert.Equal(t, canonicalGitTestPath(t, gitDir), worktrees[0].Path)
	assert.Equal(t, "main", worktrees[0].Branch)

	require.NoError(t, linked.CreateBranch("independent", "feature"))
	require.NoError(t, linked.CheckoutBranch("independent"))
	assert.Equal(t, "independent", gitExec(t, linkedPath, "branch", "--show-current"))
	main, err := linked.ForWorktree(worktrees[0].Path)
	require.ErrorContains(t, err, "main worktree")
	assert.Contains(t, err.Error(), "core.worktree backlink")
	assert.Nil(t, main)
	assert.Equal(t, "main", gitExec(t, dir, "branch", "--show-current"))

	// An explicitly supplied real path is still usable without a backlink.
	explicit := requireWorktree(t, linked, dir)
	current, err := explicit.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", current)
}

func TestIntegration_SeparateGitDirectoryUnavailableBacklink(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%t", foreign), func(t *testing.T) {
			root, dir, _ := setupSeparateGitRepo(t)
			linked, _ := addTestWorktree(t, root, dir, "feature")
			backlink := filepath.Join(t.TempDir(), "missing main worktree")
			if foreign {
				_, backlink = setupBareAndClone(t)
			}
			gitExec(t, dir, "config", "extensions.worktreeConfig", "true")
			gitExec(t, dir, "config", "--worktree", "core.worktree", backlink)
			worktrees, err := linked.Worktrees()
			require.NoError(t, err, "an unavailable main must not block unrelated worktrees")
			assert.Equal(t, filepath.ToSlash(filepath.Clean(backlink)), worktrees[0].Path)
			selected, err := linked.ForWorktree(worktrees[0].Path)
			require.Error(t, err)
			assert.Nil(t, selected)
			dirty, err := linked.HasUncommittedChanges()
			require.NoError(t, err)
			assert.False(t, dirty)
		})
	}
}

func TestIntegration_ForWorktreeRejectsInvalidContext(t *testing.T) {
	root, dir := setupWorktreeRepo(t)
	_, other := setupBareAndClone(t)
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing"), other} {
		t.Run(path, func(t *testing.T) {
			selected, err := root.ForWorktree(path)
			require.Error(t, err)
			assert.Nil(t, selected)
		})
	}

	// Removing a nested checkout's gitfile must not fall back to its parent's
	// HEAD/index just because Git can still discover the parent repository.
	nestedPath := filepath.Join(dir, "nested")
	gitExec(t, dir, "worktree", "add", "-b", "nested", nestedPath, "main")
	nested := requireWorktree(t, root, nestedPath)
	_, err := nested.GitDir()
	require.NoError(t, err)
	require.NoError(t, os.Rename(filepath.Join(nestedPath, ".git"), filepath.Join(t.TempDir(), "saved-gitfile")))
	require.ErrorContains(t, nested.ResetHard("HEAD"), "selected Git directory")
	_, err = nested.HasUncommittedChanges()
	require.Error(t, err)
	assert.Equal(t, "main", gitExec(t, dir, "branch", "--show-current"))
}

func TestIntegration_ForWorktreeIndependentExecutors(t *testing.T) {
	root, dir := setupWorktreeRepo(t)
	first, _ := addTestWorktree(t, root, dir, "first")
	second, _ := addTestWorktree(t, root, dir, "second")
	results := make(chan error, 2)
	for branch, scope := range map[string]Ops{"first": first, "second": second} {
		go func(branch string, scope Ops) {
			for i := 0; i < 3; i++ {
				got, err := scope.CurrentBranch()
				if err != nil {
					results <- err
					return
				}
				if got != branch {
					results <- fmt.Errorf("wanted branch %s, got %s", branch, got)
					return
				}
			}
			results <- nil
		}(branch, scope)
	}
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	assert.Equal(t, "main", gitExec(t, dir, "branch", "--show-current"))
}

func TestIntegration_WorktreeMissingCheckoutDoesNotReportStagedChanges(t *testing.T) {
	root, dir := setupWorktreeRepo(t)
	linked, path := addTestWorktree(t, root, dir, "feature")
	require.NoError(t, os.Rename(path, path+"-moved"))

	staged, err := linked.HasStagedChanges()
	require.Error(t, err)
	assert.False(t, staged, "a failed state lookup must not report staged changes")
}

func TestIntegration_WorktreeStateQueriesRejectChangedScope(t *testing.T) {
	for _, change := range []string{"missing checkout", "removed gitfile", "replaced gitfile"} {
		t.Run(change, func(t *testing.T) {
			root, dir := setupWorktreeRepo(t)
			path := filepath.Join(dir, "nested")
			gitExec(t, dir, "worktree", "add", "-b", "nested", path, "main")
			selected := requireWorktree(t, root, path)
			_, otherPath := addTestWorktree(t, root, dir, "other")
			head := gitExec(t, dir, "rev-parse", "HEAD")
			index := gitExec(t, dir, "diff", "--cached", "--name-only")
			switch change {
			case "missing checkout":
				require.NoError(t, os.Rename(path, path+"-moved"))
			case "removed gitfile":
				require.NoError(t, os.Rename(filepath.Join(path, ".git"), filepath.Join(t.TempDir(), "gitfile")))
			case "replaced gitfile":
				data, err := os.ReadFile(filepath.Join(otherPath, ".git"))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(path, ".git"), data, 0644))
			}

			queries := map[string]func() (bool, error){
				"branch":      func() (bool, error) { return selected.BranchExists("nested") },
				"staged":      selected.HasStagedChanges,
				"rebase":      selected.IsRebaseInProgress,
				"cherry-pick": selected.IsCherryPickInProgress,
				"dirty":       selected.HasUncommittedChanges,
			}
			for name, query := range queries {
				t.Run(name, func(t *testing.T) {
					value, err := query()
					require.Error(t, err)
					assert.NotErrorIs(t, err, ErrNotInRepository)
					assert.False(t, value)
				})
			}
			child, err := selected.ForWorktree(otherPath)
			require.Error(t, err)
			assert.Nil(t, child)
			require.Error(t, selected.StageAll())
			require.Error(t, selected.ResetHard("HEAD"))
			require.True(t, IsRebaseStartError(selected.Rebase("main", RebaseOpts{})))
			assert.Equal(t, head, gitExec(t, dir, "rev-parse", "HEAD"))
			assert.Equal(t, index, gitExec(t, dir, "diff", "--cached", "--name-only"))
		})
	}
}

func TestIntegration_GitStateQueriesDistinguishAbsenceAndErrors(t *testing.T) {
	t.Run("normal false and true results", func(t *testing.T) {
		root, dir := setupWorktreeRepo(t)
		linked, path := addTestWorktree(t, root, dir, "feature")
		for _, scope := range []Ops{root, linked} {
			exists, err := scope.BranchExists("feature")
			require.NoError(t, err)
			assert.True(t, exists)
			exists, err = scope.BranchExists("missing")
			require.NoError(t, err)
			assert.False(t, exists)
			assert.False(t, requireGitState(t, scope.HasStagedChanges))
			assert.False(t, requireGitState(t, scope.IsRebaseInProgress))
			assert.False(t, requireGitState(t, scope.IsCherryPickInProgress))
		}
		writeFile(t, path, "new.txt", "staged change\n")
		require.NoError(t, linked.StageAll())
		assert.True(t, requireGitState(t, linked.HasStagedChanges))
		assert.False(t, requireGitState(t, root.HasStagedChanges))
	})
	t.Run("broken ref is not absent", func(t *testing.T) {
		root, dir := setupWorktreeRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "broken"), []byte("invalid object id\n"), 0644))
		exists, err := root.BranchExists("broken")
		require.Error(t, err)
		assert.False(t, exists)
	})
	t.Run("corrupt index is not staged changes", func(t *testing.T) {
		root, dir := setupWorktreeRepo(t)
		linked, _ := addTestWorktree(t, root, dir, "feature")
		gitDir, err := linked.GitDir()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index"), []byte("invalid index\n"), 0644))
		staged, err := linked.HasStagedChanges()
		require.Error(t, err)
		assert.False(t, staged)
	})
	t.Run("missing executable", func(t *testing.T) {
		root, dir := setupWorktreeRepo(t)
		root.client.GitPath = filepath.Join(dir, "missing-git")
		exists, err := root.BranchExists("missing")
		require.Error(t, err)
		assert.False(t, exists)
		staged, err := root.HasStagedChanges()
		require.Error(t, err)
		assert.False(t, staged)
		scoped, err := root.ForWorktree(dir)
		require.Error(t, err)
		assert.Nil(t, scoped)
	})
	t.Run("non-repository directory", func(t *testing.T) {
		root := &defaultOps{client: &cligit.Client{RepoDir: t.TempDir()}}
		exists, err := root.BranchExists("missing")
		require.ErrorIs(t, err, ErrNotInRepository)
		assert.False(t, exists)
		staged, err := root.HasStagedChanges()
		require.Error(t, err)
		assert.False(t, staged)
	})
	t.Run("invalid explicit Git directory is not optional absence", func(t *testing.T) {
		dir := t.TempDir()
		root := &defaultOps{client: &cligit.Client{RepoDir: dir}}
		t.Setenv("GIT_DIR", filepath.Join(dir, "missing"))
		exists, err := root.BranchExists("missing")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNotInRepository)
		assert.False(t, exists)
	})
}

func TestIntegration_GitStateQueriesSurfaceFilesystemErrors(t *testing.T) {
	t.Run("unreadable sequencer", func(t *testing.T) {
		root, dir := setupWorktreeRepo(t)
		linked, _ := addTestWorktree(t, root, dir, "feature")
		gitDir, err := linked.GitDir()
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(gitDir, "sequencer", "todo"), 0755))
		picking, err := linked.IsCherryPickInProgress()
		require.ErrorContains(t, err, "cherry-pick sequencer")
		assert.False(t, picking)
	})
	for _, marker := range []string{"rebase-merge", "CHERRY_PICK_HEAD"} {
		t.Run(marker+" stat failure", func(t *testing.T) {
			root, dir := setupWorktreeRepo(t)
			linked, _ := addTestWorktree(t, root, dir, "feature")
			gitDir, err := linked.GitDir()
			require.NoError(t, err)
			path := filepath.Join(gitDir, marker)
			err = os.Symlink(path, path)
			if err != nil && runtime.GOOS == "windows" {
				t.Skip("creating symlinks requires privileges on Windows")
			}
			require.NoError(t, err)
			query := linked.IsRebaseInProgress
			if marker == "CHERRY_PICK_HEAD" {
				query = linked.IsCherryPickInProgress
			}
			inProgress, err := query()
			require.Error(t, err)
			assert.False(t, inProgress)
		})
	}
}

func TestIntegration_WorktreeRebasePreservesOtherRefsAndConfig(t *testing.T) {
	for _, onto := range []bool{false, true} {
		for _, dates := range []bool{false, true} {
			t.Run(fmt.Sprintf("onto=%t/dates=%t", onto, dates), func(t *testing.T) {
				root, dir := setupWorktreeRepo(t)
				linked, path := addTestWorktree(t, root, dir, "feature")
				base := gitExec(t, dir, "rev-parse", "main")
				writeFile(t, path, "feature.txt", "first")
				gitExec(t, path, "add", ".")
				gitExec(t, path, "commit", "--date=2001-01-01T00:00:00Z", "-m", "first")
				excluded := gitExec(t, path, "rev-parse", "HEAD")
				gitExec(t, path, "branch", "excluded")
				writeFile(t, path, "feature.txt", "second")
				gitExec(t, path, "add", ".")
				gitExec(t, path, "commit", "--date=2002-01-01T00:00:00Z", "-m", "second")
				original := gitExec(t, path, "rev-parse", "HEAD")
				writeFile(t, dir, "main.txt", "updated")
				gitExec(t, dir, "add", ".")
				gitExec(t, dir, "commit", "-m", "main update")
				mainHead := gitExec(t, dir, "rev-parse", "HEAD")
				writeFile(t, dir, "unrelated.txt", "leave the initiating worktree alone")
				mainStatus := gitExec(t, dir, "status", "--porcelain")
				gitExec(t, dir, "worktree", "lock", path)
				gitExec(t, dir, "config", "rebase.updateRefs", "true")
				gitExec(t, dir, "config", "rebase.autoStash", "true")

				opts := RebaseOpts{CommitterDateIsAuthorDate: dates}
				var err error
				if onto {
					err = linked.RebaseOnto("main", base, "feature", opts)
				} else {
					err = linked.Rebase("main", opts)
				}
				require.NoError(t, err)
				assert.NotEqual(t, original, gitExec(t, path, "rev-parse", "HEAD"))
				assert.Equal(t, excluded, gitExec(t, dir, "rev-parse", "excluded"))
				assert.Equal(t, mainHead, gitExec(t, dir, "rev-parse", "HEAD"))
				assert.Equal(t, mainStatus, gitExec(t, dir, "status", "--porcelain"))
				assert.Equal(t, "feature", gitExec(t, path, "branch", "--show-current"))
				assert.Equal(t, "true", gitExec(t, dir, "config", "--get", "rebase.updateRefs"))
				assert.Equal(t, "true", gitExec(t, dir, "config", "--get", "rebase.autoStash"))
				timestamps := strings.Fields(gitExec(t, path, "show", "-s", "--format=%at %ct", "HEAD"))
				require.Len(t, timestamps, 2)
				assert.Equal(t, dates, timestamps[0] == timestamps[1])
			})
		}
	}
}

func TestIntegration_WorktreeRebaseNeverAutostashes(t *testing.T) {
	for _, onto := range []bool{false, true} {
		for _, staged := range []bool{false, true} {
			t.Run(fmt.Sprintf("onto=%t/staged=%t", onto, staged), func(t *testing.T) {
				root, dir := setupWorktreeRepo(t)
				linked, path := addTestWorktree(t, root, dir, "feature")
				base := gitExec(t, dir, "rev-parse", "main")
				writeFile(t, path, "feature.txt", "committed")
				gitExec(t, path, "add", ".")
				gitExec(t, path, "commit", "-m", "feature")
				original := gitExec(t, path, "rev-parse", "HEAD")
				writeFile(t, dir, "main.txt", "main")
				gitExec(t, dir, "add", ".")
				gitExec(t, dir, "commit", "-m", "main")
				writeFile(t, path, "feature.txt", "uncommitted")
				if staged {
					gitExec(t, path, "add", ".")
				}
				status := gitExec(t, path, "status", "--porcelain")
				// Git -c overrides must win over inherited command configuration,
				// not just repository configuration.
				t.Setenv("GIT_CONFIG_COUNT", "2")
				t.Setenv("GIT_CONFIG_KEY_0", "rebase.autoStash")
				t.Setenv("GIT_CONFIG_VALUE_0", "true")
				t.Setenv("GIT_CONFIG_KEY_1", "rebase.updateRefs")
				t.Setenv("GIT_CONFIG_VALUE_1", "true")
				var err error
				if onto {
					err = linked.RebaseOnto("main", base, "feature", RebaseOpts{})
				} else {
					err = linked.Rebase("main", RebaseOpts{})
				}
				require.Error(t, err)
				assert.True(t, IsRebaseStartError(err))
				assert.False(t, requireGitState(t, linked.IsRebaseInProgress))
				assert.Equal(t, original, gitExec(t, path, "rev-parse", "HEAD"))
				assert.Equal(t, status, gitExec(t, path, "status", "--porcelain"))
				assert.Empty(t, gitExec(t, path, "stash", "list"))
			})
		}
	}
}

func setupWorktreeConflict(t *testing.T) (*defaultOps, string, Ops, string) {
	t.Helper()
	root, dir := setupWorktreeRepo(t)
	linked, path := addTestWorktree(t, root, dir, "feature")
	writeFile(t, path, "init.txt", "feature version\n")
	gitExec(t, path, "add", ".")
	gitExec(t, path, "commit", "--date=2001-01-01T00:00:00Z", "-m", "feature conflict")
	writeFile(t, dir, "init.txt", "main version\n")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "--date=2002-01-01T00:00:00Z", "-m", "main conflict")
	return root, dir, linked, path
}

func forbidGlobalWorktreeQueries(t *testing.T) func() {
	t.Helper()
	return SetOps(&MockOps{
		GitDirFn: func() (string, error) {
			t.Error("real scoped operations must not query global mock GitDir")
			return "", fmt.Errorf("global GitDir must not be called")
		},
		IsRebaseInProgressFn: func() (bool, error) {
			t.Error("real scoped operations must not query global mock rebase state")
			return false, nil
		},
		ConflictedFilesFn: func() ([]string, error) {
			t.Error("real scoped operations must not query global mock conflicts")
			return nil, fmt.Errorf("global ConflictedFiles must not be called")
		},
	})
}

func TestIntegration_WorktreeRebaseRecovery(t *testing.T) {
	tests := []struct {
		name    string
		main    bool
		abort   bool
		backend string
		dates   bool
	}{
		{name: "linked continue", backend: "merge", dates: true},
		{name: "linked abort", abort: true, backend: "merge", dates: true},
		{name: "main continue", main: true, backend: "merge", dates: true},
		{name: "main abort", main: true, abort: true, backend: "merge", dates: true},
		{name: "apply continue", backend: "apply"},
		{name: "apply abort", abort: true, backend: "apply"},
		{name: "author dates with configured apply backend", backend: "apply", dates: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, dir, linked, linkedPath := setupWorktreeConflict(t)
			gitExec(t, dir, "config", "rebase.backend", tt.backend)
			t.Setenv("GIT_EDITOR", "false")
			target, observer := linked, requireWorktree(t, root, dir)
			targetPath, observerPath := linkedPath, dir
			branch, base := "feature", "main"
			if tt.main {
				target, observer = observer, target
				targetPath, observerPath = dir, linkedPath
				branch, base = base, branch
			}
			original := gitExec(t, targetPath, "rev-parse", "HEAD")
			observerHead := gitExec(t, observerPath, "rev-parse", "HEAD")
			writeFile(t, observerPath, "leave-alone.txt", "dirty unrelated worktree")
			observerStatus := gitExec(t, observerPath, "status", "--porcelain")
			restore := forbidGlobalWorktreeQueries(t)
			defer restore()

			opts := RebaseOpts{CommitterDateIsAuthorDate: tt.dates}
			err := target.Rebase(base, opts)
			require.Error(t, err)
			assert.False(t, IsRebaseStartError(err))
			require.True(t, requireGitState(t, target.IsRebaseInProgress))
			assert.False(t, requireGitState(t, observer.IsRebaseInProgress))
			assert.True(t, IsRebaseStartError(target.Rebase(base, opts)))
			conflicts, err := target.ConflictedFiles()
			require.NoError(t, err)
			assert.Equal(t, []string{"init.txt"}, conflicts)
			markers, err := target.FindConflictMarkers("init.txt")
			require.NoError(t, err)
			assert.Equal(t, []ConflictSection{{StartLine: 1, EndLine: 5}}, markers.Sections)
			worktrees, err := observer.Worktrees()
			require.NoError(t, err)
			var reserved Worktree
			for _, wt := range worktrees {
				if wt.Path == canonicalGitTestPath(t, targetPath) {
					reserved = wt
				}
			}
			assert.Equal(t, branch, reserved.Branch)
			assert.True(t, reserved.Detached)

			if tt.abort {
				require.NoError(t, target.RebaseAbort())
				assert.Equal(t, original, gitExec(t, targetPath, "rev-parse", "HEAD"))
			} else {
				writeFile(t, targetPath, "init.txt", "resolved\n")
				require.NoError(t, target.StageAll())
				require.NoError(t, target.RebaseContinue(opts))
				assert.NotEqual(t, original, gitExec(t, targetPath, "rev-parse", "HEAD"))
				dates := strings.Fields(gitExec(t, targetPath, "show", "-s", "--format=%at %ct", "HEAD"))
				require.Len(t, dates, 2)
				assert.Equal(t, tt.dates, dates[0] == dates[1])
			}
			assert.False(t, requireGitState(t, target.IsRebaseInProgress))
			assert.Equal(t, branch, gitExec(t, targetPath, "branch", "--show-current"))
			assert.Equal(t, observerHead, gitExec(t, observerPath, "rev-parse", "HEAD"))
			assert.Equal(t, observerStatus, gitExec(t, observerPath, "status", "--porcelain"))
		})
	}
}

func TestIntegration_WorktreeRerereAutoContinuesMultipleCommits(t *testing.T) {
	root, dir := setupWorktreeRepo(t)
	for _, file := range []string{"one.txt", "two.txt"} {
		writeFile(t, dir, file, "initial "+file+"\n")
	}
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "base")
	linked, path := addTestWorktree(t, root, dir, "feature")
	for _, file := range []string{"one.txt", "two.txt"} {
		writeFile(t, path, file, "feature "+file+"\n")
		gitExec(t, path, "add", ".")
		gitExec(t, path, "commit", "-m", file)
		writeFile(t, dir, file, "main "+file+"\n")
	}
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "main changes")
	original := gitExec(t, path, "rev-parse", "HEAD")
	mainHead := gitExec(t, dir, "rev-parse", "HEAD")
	require.NoError(t, linked.EnableRerere())
	t.Setenv("GIT_EDITOR", "false")
	restore := forbidGlobalWorktreeQueries(t)
	defer restore()
	require.Error(t, linked.Rebase("main", RebaseOpts{}))
	writeFile(t, path, "one.txt", "resolved one\n")
	require.NoError(t, linked.StageAll())
	require.Error(t, linked.RebaseContinue(RebaseOpts{}), "the second commit has an unseen conflict")
	writeFile(t, path, "two.txt", "resolved two\n")
	require.NoError(t, linked.StageAll())
	require.NoError(t, linked.RebaseContinue(RebaseOpts{}))
	require.NoError(t, linked.ResetHard(original))

	require.NoError(t, linked.Rebase("main", RebaseOpts{}), "rerere must continue both commits in the linked worktree")
	assert.False(t, requireGitState(t, linked.IsRebaseInProgress))
	assert.Equal(t, mainHead, gitExec(t, dir, "rev-parse", "HEAD"))
	for _, file := range []string{"one.txt", "two.txt"} {
		data, err := os.ReadFile(filepath.Join(path, file))
		require.NoError(t, err)
		assert.Contains(t, string(data), "resolved")
	}
}

func TestIntegration_WorktreeRebaseContinueStopsWhenNoProgress(t *testing.T) {
	_, _, linked, path := setupWorktreeConflict(t)
	require.Error(t, linked.Rebase("main", RebaseOpts{}))
	writeFile(t, path, "init.txt", "resolved\n")
	require.NoError(t, linked.StageAll())
	t.Setenv("GIT_COMMITTER_NAME", "")
	tracePath := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", tracePath)
	require.Error(t, linked.RebaseContinue(RebaseOpts{}))
	trace, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	assert.LessOrEqual(t, strings.Count(string(trace), " rebase --continue"), 2)
	assert.True(t, requireGitState(t, linked.IsRebaseInProgress))
	require.NoError(t, linked.RebaseAbort())
}

func TestIntegration_WorktreeConflictPathsAndMarkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow colons and newlines in filenames")
	}
	root, dir := setupWorktreeRepo(t)
	file := "conflict:\nname: leftover conflict marker [literal] \u03bb.txt"
	writeFile(t, dir, file, "initial\n")
	writeFile(t, dir, ".gitattributes", "* conflict-marker-size=10\n")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "conflict base")
	linked, path := addTestWorktree(t, root, dir, "feature")
	writeFile(t, path, file, "feature\n")
	gitExec(t, path, "add", ".")
	gitExec(t, path, "commit", "-m", "feature")
	writeFile(t, dir, file, "main\n")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "main")
	require.Error(t, linked.Rebase("main", RebaseOpts{}))
	conflicts, err := linked.ConflictedFiles()
	require.NoError(t, err)
	assert.Equal(t, []string{file}, conflicts)
	// Two marker sections, including diff3's optional base marker, exercise
	// native marker-size handling and paths that cannot be parsed by colons.
	writeFile(t, path, file, "<<<<<<<<<< ours\none\n==========\ntwo\n>>>>>>>>>> theirs\nmiddle\n<<<<<<<<<< ours\nthree\n|||||||||| base\nbase\n==========\nfour\n>>>>>>>>>> theirs\n")
	require.NoError(t, os.MkdirAll(filepath.Join(path, "subdir"), 0755))
	sub := requireWorktree(t, linked, "subdir")
	gitExec(t, dir, "config", "diff.relative", "true")
	subConflicts, err := sub.ConflictedFiles()
	require.NoError(t, err)
	assert.Equal(t, []string{file}, subConflicts)
	for _, name := range []string{file, filepath.Join(path, file)} {
		markers, err := sub.FindConflictMarkers(name)
		require.NoError(t, err)
		assert.Equal(t, name, markers.File)
		assert.Equal(t, []ConflictSection{{StartLine: 1, EndLine: 5}, {StartLine: 7, EndLine: 13}}, markers.Sections)
	}
	require.NoError(t, linked.RebaseAbort())
}

func TestIntegration_WorktreeRetainedRebaseOwner(t *testing.T) {
	root, dir, linked, path := setupWorktreeConflict(t)
	require.Error(t, linked.Rebase("main", RebaseOpts{}))
	canonicalPath := canonicalGitTestPath(t, path)
	require.NoError(t, os.Rename(path, path+"-moved"))
	worktrees, err := root.Worktrees()
	require.NoError(t, err)
	assert.Contains(t, worktrees, Worktree{Path: canonicalPath, Branch: "feature", Detached: true, Prunable: true})
	_, err = linked.HasUncommittedChanges()
	require.Error(t, err, "a missing checkout must not silently execute elsewhere")
	gitExec(t, dir, "worktree", "repair", path+"-moved")
	moved := requireWorktree(t, root, path+"-moved")
	require.True(t, requireGitState(t, moved.IsRebaseInProgress))
	require.NoError(t, moved.RebaseAbort())
}

func TestIntegration_WorktreeCherryPickRecovery(t *testing.T) {
	for _, action := range []string{"continue", "abort", "quit"} {
		t.Run(action, func(t *testing.T) {
			root, dir, linked, path := setupWorktreeConflict(t)
			t.Setenv("GIT_EDITOR", "false")
			original := gitExec(t, path, "rev-parse", "HEAD")
			mainHead := gitExec(t, dir, "rev-parse", "HEAD")
			restore := forbidGlobalWorktreeQueries(t)
			defer restore()
			require.Error(t, linked.CherryPick([]string{mainHead}))
			require.True(t, requireGitState(t, linked.IsCherryPickInProgress))
			assert.False(t, requireGitState(t, root.IsCherryPickInProgress))
			assert.False(t, requireGitState(t, linked.IsRebaseInProgress))
			switch action {
			case "continue":
				writeFile(t, path, "init.txt", "resolved\n")
				require.NoError(t, linked.StageAll())
				require.NoError(t, linked.CherryPickContinue())
				assert.NotEqual(t, original, gitExec(t, path, "rev-parse", "HEAD"))
			case "abort":
				require.NoError(t, linked.CherryPickAbort())
				assert.Equal(t, original, gitExec(t, path, "rev-parse", "HEAD"))
			case "quit":
				require.NoError(t, linked.CherryPickQuit())
				conflicts, err := linked.ConflictedFiles()
				require.NoError(t, err)
				assert.NotEmpty(t, conflicts)
				require.NoError(t, linked.ResetHard(original))
			}
			assert.False(t, requireGitState(t, linked.IsCherryPickInProgress))
			assert.Equal(t, mainHead, gitExec(t, dir, "rev-parse", "HEAD"))
			assert.Equal(t, "feature", gitExec(t, path, "branch", "--show-current"))
		})
	}
}

func TestIntegration_WorktreeLocalMutations(t *testing.T) {
	root, dir := setupWorktreeRepo(t)
	linked, path := addTestWorktree(t, root, dir, "feature")
	restore := forbidGlobalWorktreeQueries(t)
	defer restore()
	defaultBranch, err := linked.DefaultBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", defaultBranch)
	initial := gitExec(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "new.txt", "new main commit\n")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "advance main")
	mainHead := gitExec(t, dir, "rev-parse", "HEAD")
	gitExec(t, dir, "config", "merge.autoStash", "true")
	require.NoError(t, linked.MergeFF("main"))
	assert.Equal(t, mainHead, gitExec(t, path, "rev-parse", "HEAD"))
	require.NoError(t, linked.ResetHard(initial))
	assert.NoFileExists(t, filepath.Join(path, "new.txt"))
	assert.FileExists(t, filepath.Join(dir, "new.txt"))
	assert.Equal(t, mainHead, gitExec(t, dir, "rev-parse", "HEAD"))
	require.Error(t, linked.CheckoutBranch("main"))
	require.Error(t, linked.UpdateBranchRef("main", initial))
	require.NoError(t, linked.RenameBranch("feature", "renamed"))
	branch, err := linked.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, "renamed", branch)

	writeFile(t, path, "init.txt", "tracked change")
	writeFile(t, path, "untracked.txt", "untracked change")
	gitExec(t, dir, "config", "status.showUntrackedFiles", "no")
	require.NoError(t, linked.StageTracked())
	assert.Equal(t, "init.txt", gitExec(t, path, "diff", "--cached", "--name-only"))
	assert.Empty(t, gitExec(t, dir, "diff", "--cached", "--name-only"))
	require.NoError(t, linked.StageAll())
	assert.True(t, requireGitState(t, linked.HasStagedChanges))
	_, err = linked.Commit("commit only in linked worktree")
	require.NoError(t, err)
	assert.False(t, requireGitState(t, linked.HasStagedChanges))
	assert.Equal(t, mainHead, gitExec(t, dir, "rev-parse", "HEAD"))
	writeFile(t, path, "hidden-untracked.txt", "still dirty despite user status configuration")
	dirty, err := linked.HasUncommittedChanges()
	require.NoError(t, err)
	assert.True(t, dirty)
}

func TestIntegration_WorktreeCherryPickPendingSequencer(t *testing.T) {
	root, dir, linked, path := setupWorktreeConflict(t)
	first := gitExec(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "second.txt", "second commit")
	gitExec(t, dir, "add", ".")
	gitExec(t, dir, "commit", "-m", "second")
	second := gitExec(t, dir, "rev-parse", "HEAD")
	require.Error(t, linked.CherryPick([]string{first, second}))
	writeFile(t, path, "init.txt", "manual resolution\n")
	require.NoError(t, linked.StageAll())
	_, err := linked.Commit("resolve first pick manually")
	require.NoError(t, err)
	gitDir, err := linked.GitDir()
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
	assert.True(t, requireGitState(t, linked.IsCherryPickInProgress), "the remaining sequencer still owns the worktree")
	assert.False(t, requireGitState(t, root.IsCherryPickInProgress))
	require.NoError(t, linked.CherryPickContinue())
	assert.False(t, requireGitState(t, linked.IsCherryPickInProgress))
	assert.FileExists(t, filepath.Join(path, "second.txt"))
	assert.Equal(t, second, gitExec(t, dir, "rev-parse", "HEAD"))
}

func TestIntegration_WorktreeIgnoresCallerRepositoryEnvironment(t *testing.T) {
	root, dir, linked, path := setupWorktreeConflict(t)
	mainHead := gitExec(t, dir, "rev-parse", "HEAD")
	gitDir, err := root.GitDir()
	require.NoError(t, err)
	t.Setenv("GIT_DIR", gitDir)
	t.Setenv("GIT_COMMON_DIR", gitDir)
	t.Setenv("GIT_WORK_TREE", dir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(gitDir, "index"))
	t.Setenv("GIT_EDITOR", "false")
	// A newly selected scope and an existing one must both ignore the caller's
	// local repository environment, including during continuation.
	selected := requireWorktree(t, root, path)
	for _, scope := range []Ops{selected, linked} {
		branch, err := scope.CurrentBranch()
		require.NoError(t, err)
		assert.Equal(t, "feature", branch)
	}
	err = selected.Rebase("main", RebaseOpts{})
	require.Error(t, err)
	assert.False(t, IsRebaseStartError(err))
	writeFile(t, path, "init.txt", "resolved\n")
	require.NoError(t, selected.StageAll())
	require.NoError(t, selected.RebaseContinue(RebaseOpts{}))
	head, err := root.RevParse("HEAD")
	require.NoError(t, err)
	assert.Equal(t, mainHead, head)
	branch, err := selected.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, "feature", branch)
}
