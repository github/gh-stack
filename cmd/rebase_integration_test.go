package cmd

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise `gh stack rebase` against a real git repository with a
// real remote, using the production git operations rather than mocks. They
// guard the class of bug where the command reports a successful cascade while
// git actually refused to do anything.

// gitRun runs a git command in dir and fails the test if it errors.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(t, dir, args...)
	require.NoError(t, err, "git %s in %s:\n%s", strings.Join(args, " "), dir, out)
	return out
}

// gitTry runs a git command in dir and returns its combined output and error.
func gitTry(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// commitFile writes a file and commits it in dir.
func commitFile(t *testing.T, dir, name, content, message string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", message)
}

// setupRealStackRepo creates a clone with a bare origin containing
// main <- b1 <- b2, pushes everything, then adds a new commit to main on the
// remote so a rebase has real work to do. Returns the clone directory and the
// SHA of the commit that landed on the remote trunk.
func setupRealStackRepo(t *testing.T) (string, string) {
	t.Helper()

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	_, err := gitTry(t, ".", "-c", "safe.bareRepository=all", "init", "--bare", "-b", "main", bareDir)
	require.NoError(t, err)

	cloneDir := filepath.Join(t.TempDir(), "clone")
	gitRun(t, ".", "clone", bareDir, cloneDir)
	gitRun(t, cloneDir, "config", "user.name", "Test")
	gitRun(t, cloneDir, "config", "user.email", "test@test.com")

	commitFile(t, cloneDir, "init.txt", "hello", "initial commit")
	gitRun(t, cloneDir, "push", "origin", "main")

	gitRun(t, cloneDir, "checkout", "-b", "b1")
	commitFile(t, cloneDir, "b1.txt", "b1", "b1 work")
	gitRun(t, cloneDir, "push", "-u", "origin", "b1")

	gitRun(t, cloneDir, "checkout", "-b", "b2")
	commitFile(t, cloneDir, "b2.txt", "b2", "b2 work")
	gitRun(t, cloneDir, "push", "-u", "origin", "b2")

	// Someone else lands a commit on main.
	otherDir := filepath.Join(t.TempDir(), "other")
	gitRun(t, ".", "clone", bareDir, otherDir)
	gitRun(t, otherDir, "config", "user.name", "Other")
	gitRun(t, otherDir, "config", "user.email", "other@test.com")
	commitFile(t, otherDir, "upstream.txt", "landed", "upstream work")
	gitRun(t, otherDir, "push", "origin", "main")
	upstreamSHA := gitRun(t, otherDir, "rev-parse", "main")

	gitRun(t, cloneDir, "checkout", "b2")
	return cloneDir, upstreamSHA
}

// writeRealStackFile writes the stack file into the repo's .git directory.
func writeRealStackFile(t *testing.T, cloneDir string, s stack.Stack) {
	t.Helper()
	sf := &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{s}}
	data, err := json.MarshalIndent(sf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".git", "gh-stack"), data, 0644))
}

// runRealRebase runs the rebase command with real git ops inside cloneDir.
func runRealRebase(t *testing.T, cloneDir string, args []string) (string, error) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(cloneDir))
	defer func() { _ = os.Chdir(old) }()

	cfg, _, errR := config.NewTestConfig()
	// Keep the command off the network; the stack has no PRs.
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
	}
	cmd := RebaseCmd(cfg)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmdErr := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	return string(errOut), cmdErr
}

// isAncestor reports whether ancestor is in descendant's history.
func isAncestor(t *testing.T, dir, ancestor, descendant string) bool {
	t.Helper()
	_, err := gitTry(t, dir, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func twoBranchStack() stack.Stack {
	return stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
}

// TestIntegration_Rebase_PullsInTrunk is the regression test for stacks that
// stayed on their original base while the command reported success.
func TestIntegration_Rebase_PullsInTrunk(t *testing.T) {
	cloneDir, upstreamSHA := setupRealStackRepo(t)
	writeRealStackFile(t, cloneDir, twoBranchStack())

	output, err := runRealRebase(t, cloneDir, nil)
	require.NoError(t, err, output)

	assert.True(t, isAncestor(t, cloneDir, upstreamSHA, "b1"), "b1 must contain the new trunk commit")
	assert.True(t, isAncestor(t, cloneDir, "b1", "b2"), "b2 must be stacked on b1")
	assert.Contains(t, output, "rebased locally with main")

	// Each branch keeps exactly its own commit — nothing duplicated.
	assert.Equal(t, "b1 work", gitRun(t, cloneDir, "log", "--format=%s", "main..b1"))
	assert.Equal(t, "b2 work", gitRun(t, cloneDir, "log", "--format=%s", "b1..b2"))
}

// TestIntegration_Rebase_DirtyTreeIsRejected verifies the command refuses to
// run rather than reporting a rebase it never performed.
func TestIntegration_Rebase_DirtyTreeIsRejected(t *testing.T) {
	cloneDir, upstreamSHA := setupRealStackRepo(t)
	writeRealStackFile(t, cloneDir, twoBranchStack())

	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "init.txt"), []byte("uncommitted"), 0644))

	output, err := runRealRebase(t, cloneDir, nil)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "uncommitted changes in working tree")
	assert.NotContains(t, output, "rebased locally")
	assert.False(t, isAncestor(t, cloneDir, upstreamSHA, "b1"), "the stack must be untouched")
}

// TestIntegration_Rebase_AutostashRebasesAndRestores verifies --autostash both
// completes the rebase and puts the local changes back.
func TestIntegration_Rebase_AutostashRebasesAndRestores(t *testing.T) {
	cloneDir, upstreamSHA := setupRealStackRepo(t)
	writeRealStackFile(t, cloneDir, twoBranchStack())

	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "init.txt"), []byte("uncommitted"), 0644))

	output, err := runRealRebase(t, cloneDir, []string{"--autostash"})
	require.NoError(t, err, output)

	assert.True(t, isAncestor(t, cloneDir, upstreamSHA, "b1"), "b1 must contain the new trunk commit")
	assert.True(t, isAncestor(t, cloneDir, "b1", "b2"), "b2 must be stacked on b1")

	status := gitRun(t, cloneDir, "status", "--porcelain")
	assert.Contains(t, status, "init.txt", "autostash must restore the local changes")
}

// TestIntegration_Rebase_BranchCheckedOutInAnotherWorktree covers the case
// reported by users working with multiple worktrees: git refuses to rebase a
// branch that is checked out elsewhere.
func TestIntegration_Rebase_BranchCheckedOutInAnotherWorktree(t *testing.T) {
	cloneDir, _ := setupRealStackRepo(t)
	writeRealStackFile(t, cloneDir, twoBranchStack())

	gitRun(t, cloneDir, "checkout", "b1")
	wtDir := filepath.Join(t.TempDir(), "wt")
	gitRun(t, cloneDir, "worktree", "add", wtDir, "b2")

	output, err := runRealRebase(t, cloneDir, nil)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "could not start rebase of b2 onto b1")
	assert.NotContains(t, output, "rebased locally")
	assert.False(t, isAncestor(t, cloneDir, "b1", "b2"), "b2 must be untouched")
}

// TestIntegration_Rebase_ParentAmendedDoesNotDuplicateCommits verifies that a
// parent branch rewritten outside gh-stack does not get its commits replayed
// onto the child.
func TestIntegration_Rebase_ParentAmendedDoesNotDuplicateCommits(t *testing.T) {
	cloneDir, _ := setupRealStackRepo(t)

	// Record the parent tip b2 was stacked on, exactly as a prior
	// rebase/sync/push would have.
	b1Tip := gitRun(t, cloneDir, "rev-parse", "b1")
	s := twoBranchStack()
	s.Branches[1].Base = b1Tip
	writeRealStackFile(t, cloneDir, s)

	// The user amends b1 out of band, changing its content.
	gitRun(t, cloneDir, "checkout", "b1")
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "b1.txt"), []byte("b1 amended"), 0644))
	gitRun(t, cloneDir, "add", ".")
	gitRun(t, cloneDir, "commit", "--amend", "-m", "b1 work (amended)")
	gitRun(t, cloneDir, "checkout", "b2")

	output, err := runRealRebase(t, cloneDir, nil)
	require.NoError(t, err, output)

	assert.True(t, isAncestor(t, cloneDir, "b1", "b2"), "b2 must be stacked on the amended b1")
	assert.Equal(t, "b2 work", gitRun(t, cloneDir, "log", "--format=%s", "b1..b2"),
		"b2 must contain only its own commit, not a replay of b1's rewritten commit")
	assert.Equal(t, "b1 amended", readFileOnBranch(t, cloneDir, "b2", "b1.txt"),
		"b2 must carry the amended content, not a duplicate of the old commit")
}

// readFileOnBranch returns the contents of a file as of the given branch.
func readFileOnBranch(t *testing.T, dir, branch, path string) string {
	t.Helper()
	return gitRun(t, dir, "show", branch+":"+path)
}
