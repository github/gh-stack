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
	assert.False(t, IsCherryPickInProgress(), "no cherry-pick should be in progress initially")

	// Cherry-picking feature onto main conflicts.
	err := CherryPick([]string{featureSHA})
	require.Error(t, err, "cherry-pick should conflict")
	assert.True(t, IsCherryPickInProgress(), "cherry-pick should be in progress after a conflict")

	// While mid-conflict, a plain checkout must fail (unmerged index).
	_, coErr := gitExecMayFail(t, cloneDir, "checkout", "feature")
	require.Error(t, coErr, "checkout should fail while cherry-pick index is unmerged")

	// Aborting must fully restore: no longer in progress, clean tree, checkout works.
	require.NoError(t, CherryPickAbort())
	assert.False(t, IsCherryPickInProgress(), "cherry-pick should not be in progress after abort")

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
	require.True(t, IsCherryPickInProgress())

	// --quit clears sequencer state (no longer "in progress") ...
	CherryPickQuit()
	assert.False(t, IsCherryPickInProgress(), "quit should clear cherry-pick sequencer state")

	// ... but leaves the unmerged index behind, so checkout still fails.
	_, coErr := gitExecMayFail(t, cloneDir, "checkout", "feature")
	require.Error(t, coErr, "checkout should still fail after --quit because the index is unmerged")
}

// ---------------------------------------------------------------------------
// Integration tests for rebase failure classification
//
// A rebase that git refuses to start (dirty working tree, branch checked out
// in another worktree, unresolvable upstream, rebase already in progress)
// leaves no rebase state behind. These used to be reported as success, which
// made `gh stack rebase` print "Rebased X onto Y" without touching anything.
// ---------------------------------------------------------------------------

// setupStackedRepo creates main <- A <- B in a fresh clone and leaves main
// one commit ahead of where A branched off, so a rebase has work to do.
// Returns the clone directory.
func setupStackedRepo(t *testing.T) string {
	t.Helper()
	_, cloneDir := setupBareAndClone(t)
	gitExec(t, cloneDir, "config", "user.name", "Test")
	gitExec(t, cloneDir, "config", "user.email", "test@test.com")

	gitExec(t, cloneDir, "checkout", "-b", "A")
	writeFile(t, cloneDir, "a.txt", "a")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "a1")

	gitExec(t, cloneDir, "checkout", "-b", "B")
	writeFile(t, cloneDir, "b.txt", "b")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1")

	gitExec(t, cloneDir, "checkout", "main")
	writeFile(t, cloneDir, "m.txt", "m")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "m1")

	return cloneDir
}

func TestIntegration_Rebase_Succeeds(t *testing.T) {
	cloneDir := setupStackedRepo(t)
	defer withGitDir(t, cloneDir)()

	gitExec(t, cloneDir, "checkout", "A")
	require.NoError(t, Rebase("main", RebaseOpts{}))

	isAnc, err := IsAncestor("main", "A")
	require.NoError(t, err)
	assert.True(t, isAnc, "main must be an ancestor of A after a successful rebase")
}

func TestIntegration_Rebase_DirtyWorkingTree(t *testing.T) {
	cloneDir := setupStackedRepo(t)
	defer withGitDir(t, cloneDir)()

	gitExec(t, cloneDir, "checkout", "A")
	// Modify a tracked file so git refuses to rebase.
	writeFile(t, cloneDir, "init.txt", "dirty")

	err := Rebase("main", RebaseOpts{})

	require.Error(t, err, "a rebase git refused to start must not report success")
	assert.True(t, IsRebaseStartError(err), "expected a RebaseStartError, got %v", err)
	assert.False(t, IsRebaseInProgress(), "no rebase state should be left behind")

	isAnc, ancErr := IsAncestor("main", "A")
	require.NoError(t, ancErr)
	assert.False(t, isAnc, "A must be untouched")
}

func TestIntegration_Rebase_AutoStashSucceedsWithDirtyTree(t *testing.T) {
	cloneDir := setupStackedRepo(t)
	defer withGitDir(t, cloneDir)()

	gitExec(t, cloneDir, "checkout", "A")
	writeFile(t, cloneDir, "init.txt", "dirty")

	require.NoError(t, Rebase("main", RebaseOpts{AutoStash: true}))

	isAnc, err := IsAncestor("main", "A")
	require.NoError(t, err)
	assert.True(t, isAnc, "main must be an ancestor of A")

	dirty, err := HasUncommittedChanges()
	require.NoError(t, err)
	assert.True(t, dirty, "autostash must restore the local changes")
}

func TestIntegration_Rebase_InvalidUpstream(t *testing.T) {
	cloneDir := setupStackedRepo(t)
	defer withGitDir(t, cloneDir)()

	gitExec(t, cloneDir, "checkout", "A")

	err := Rebase("no-such-branch", RebaseOpts{})

	require.Error(t, err, "an unresolvable upstream must not report success")
	assert.True(t, IsRebaseStartError(err), "expected a RebaseStartError, got %v", err)
}

func TestIntegration_Rebase_AlreadyInProgress(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	defer withGitDir(t, cloneDir)()
	gitExec(t, cloneDir, "config", "user.name", "Test")
	gitExec(t, cloneDir, "config", "user.email", "test@test.com")

	// Build a guaranteed conflict between main and A.
	gitExec(t, cloneDir, "checkout", "-b", "A")
	writeFile(t, cloneDir, "init.txt", "from A")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "a1")
	gitExec(t, cloneDir, "checkout", "main")
	writeFile(t, cloneDir, "init.txt", "from main")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "m1")
	gitExec(t, cloneDir, "checkout", "A")

	// First rebase conflicts and leaves rebase state behind.
	require.Error(t, Rebase("main", RebaseOpts{}))
	require.True(t, IsRebaseInProgress())

	// A second rebase cannot start while the first is unresolved.
	err := Rebase("main", RebaseOpts{})
	require.Error(t, err)
	assert.True(t, IsRebaseStartError(err), "expected a RebaseStartError, got %v", err)
}

func TestIntegration_RebaseOnto_BranchCheckedOutInAnotherWorktree(t *testing.T) {
	cloneDir := setupStackedRepo(t)
	defer withGitDir(t, cloneDir)()

	aSHA := gitExec(t, cloneDir, "rev-parse", "A")

	// Check B out in a separate worktree so git refuses to rebase it here.
	wtDir := filepath.Join(t.TempDir(), "wt")
	gitExec(t, cloneDir, "worktree", "add", wtDir, "B")

	gitExec(t, cloneDir, "checkout", "A")
	require.NoError(t, Rebase("main", RebaseOpts{}))

	err := RebaseOnto("A", aSHA, "B", RebaseOpts{})

	require.Error(t, err, "rebasing a branch checked out elsewhere must not report success")
	assert.True(t, IsRebaseStartError(err), "expected a RebaseStartError, got %v", err)

	isAnc, ancErr := IsAncestor("A", "B")
	require.NoError(t, ancErr)
	assert.False(t, isAnc, "B must be untouched")
}

func TestIntegration_RebaseOnto_ConflictIsNotAStartError(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)
	defer withGitDir(t, cloneDir)()
	gitExec(t, cloneDir, "config", "user.name", "Test")
	gitExec(t, cloneDir, "config", "user.email", "test@test.com")

	gitExec(t, cloneDir, "checkout", "-b", "A")
	writeFile(t, cloneDir, "shared.txt", "from A")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "a1")
	aSHA := gitExec(t, cloneDir, "rev-parse", "A")

	gitExec(t, cloneDir, "checkout", "-b", "B")
	writeFile(t, cloneDir, "shared.txt", "from B")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "b1")

	gitExec(t, cloneDir, "checkout", "main")
	writeFile(t, cloneDir, "shared.txt", "from main")
	gitExec(t, cloneDir, "add", ".")
	gitExec(t, cloneDir, "commit", "-m", "m1")

	err := RebaseOnto("main", aSHA, "B", RebaseOpts{})

	require.Error(t, err)
	assert.False(t, IsRebaseStartError(err), "a real conflict must stay recoverable")
	assert.True(t, IsRebaseInProgress(), "a conflicted rebase leaves state to continue or abort")
	_ = RebaseAbort()
}
