package cmd

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushCall records arguments passed to Push.
type pushCall struct {
	remote   string
	branches []string
	force    bool
	atomic   bool
}

// newSyncMock creates a MockOps pre-configured for sync tests. By default
// trunk and origin/trunk return the same SHA (no update needed). Override
// RevParseFn for specific test scenarios.
func newSyncMock(tmpDir string, currentBranch string) *git.MockOps {
	return &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return currentBranch, nil },
		BranchExistsFn:  func(name string) bool { return true },
		RevParseFn: func(ref string) (string, error) {
			// Default: origin/<branch> returns same SHA as <branch> (no FF needed)
			if strings.HasPrefix(ref, "origin/") {
				return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
			}
			return "sha-" + ref, nil
		},
		IsAncestorFn:    func(a, d string) (bool, error) { return true, nil },
		FetchFn:         func(string) error { return nil },
		EnableRerereFn:  func() error { return nil },
		IsRebaseInProgressFn: func() bool { return false },
		PushFn:               func(string, []string, bool, bool) error { return nil },
	}
}

// TestSync_TrunkAlreadyUpToDate verifies that when trunk and origin/trunk have
// the same SHA, no rebase occurs and push is normal (not force).
func TestSync_TrunkAlreadyUpToDate(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall
	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	// Use same explicit SHA for local and remote trunk — already up to date
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "aaa111aaa111", nil
		}
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{branch: "rebase-" + base})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "up to date")
	assert.Empty(t, rebaseCalls, "no rebase should occur when trunk is up to date")

	// Push should happen without force
	require.Len(t, pushCalls, 1)
	assert.False(t, pushCalls[0].force, "push should not use force when no rebase occurred")
}

// TestSync_TrunkUpToDate_StackStale verifies that when trunk is already up to
// date locally (no FF needed) but the stack branches haven't been rebased onto
// the current trunk, sync still performs the cascade rebase. This is the core
// bug fix — previously sync would skip the rebase entirely in this scenario.
func TestSync_TrunkUpToDate_StackStale(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall
	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	// Trunk is already up to date — same SHA locally and remotely.
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "aaa111aaa111", nil
		}
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	// Stack branches are NOT rebased onto trunk — parent is not an ancestor.
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		// main is NOT an ancestor of b1 → stack is stale
		if a == "main" && d == "b1" {
			return false, nil
		}
		return true, nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{branch: "(rebase)" + base})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "up to date")

	// Rebase SHOULD occur even though trunk was already up to date,
	// because the stack branches are stale (not rebased onto trunk).
	assert.NotEmpty(t, rebaseCalls, "rebase should occur when stack is stale even if trunk is up to date")

	// Push should use force since rebase occurred
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force, "push should use force-with-lease after rebase")
}

// TestSync_TrunkFastForward_TriggersRebase verifies that when trunk is behind
// origin/trunk, it fast-forwards and triggers a cascade rebase with force push.
func TestSync_TrunkFastForward_TriggersRebase(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall
	var pushCalls []pushCall
	var updateBranchRefCalls []struct{ branch, sha string }

	mock := newSyncMock(tmpDir, "b1")
	// Different SHAs for trunk vs origin/trunk
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		// Default: origin/<branch> same as <branch> — no branch FF
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		// local is ancestor of remote → can fast-forward
		if a == "local-sha" && d == "remote-sha" {
			return true, nil
		}
		return true, nil
	}
	mock.UpdateBranchRefFn = func(branch, sha string) error {
		updateBranchRefCalls = append(updateBranchRefCalls, struct{ branch, sha string }{branch, sha})
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{branch: "(rebase)" + base})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)

	// UpdateBranchRef should be called (not on trunk since currentBranch != trunk)
	require.Len(t, updateBranchRefCalls, 1, "should fast-forward trunk via UpdateBranchRef")
	assert.Equal(t, "main", updateBranchRefCalls[0].branch)
	assert.Equal(t, "remote-sha", updateBranchRefCalls[0].sha)

	assert.Contains(t, output, "fast-forwarded")

	// Rebase should have been triggered
	assert.NotEmpty(t, rebaseCalls, "rebase should occur after trunk fast-forward")

	// Push should use force-with-lease after rebase
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force, "push should use force-with-lease after rebase")
}

// TestSync_TrunkFastForward_WhenOnTrunk verifies that when currently on trunk,
// MergeFF is used instead of UpdateBranchRef.
func TestSync_TrunkFastForward_WhenOnTrunk(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var mergeFFCalls []string
	var updateBranchRefCalls []string

	mock := newSyncMock(tmpDir, "main")
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		return a == "local-sha" && d == "remote-sha", nil
	}
	mock.MergeFFFn = func(target string) error {
		mergeFFCalls = append(mergeFFCalls, target)
		return nil
	}
	mock.UpdateBranchRefFn = func(branch, sha string) error {
		updateBranchRefCalls = append(updateBranchRefCalls, branch)
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	assert.Len(t, mergeFFCalls, 1, "should use MergeFF when on trunk")
	assert.Equal(t, "origin/main", mergeFFCalls[0])
	assert.Empty(t, updateBranchRefCalls, "should NOT use UpdateBranchRef when on trunk")
}

// TestSync_TrunkDiverged verifies that when trunk has diverged from origin,
// no rebase occurs and a warning is shown.
func TestSync_TrunkDiverged(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall
	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		// Stack branches: local and remote have same SHA (no branch FF needed)
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	// Neither is ancestor of the other → diverged (for trunk FF check)
	// But stack branches DO have local trunk as ancestor (for stackNeedsRebase)
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "local-sha" && d == "remote-sha" {
			return false, nil
		}
		if a == "remote-sha" && d == "local-sha" {
			return false, nil
		}
		// Stack branches have their parent as ancestor
		return true, nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "diverged")
	assert.Empty(t, rebaseCalls, "no rebase should occur when trunk diverged")

	// Push should happen without force (no rebase occurred)
	require.Len(t, pushCalls, 1)
	assert.False(t, pushCalls[0].force, "push should not use force when no rebase")
}

// TestSync_NoLocalTrunk_SkipsSilently verifies that when the trunk branch
// does not exist locally (only origin/main exists), sync skips the
// fast-forward silently without emitting a warning.
func TestSync_NoLocalTrunk_SkipsSilently(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	// Trunk does not exist locally.
	mock.BranchExistsFn = func(name string) bool { return name != "main" }
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.NotContains(t, output, "Could not compare trunk")
	assert.NotContains(t, output, "skipping trunk update")

	// Push should still happen
	require.Len(t, pushCalls, 1)
}

// TestSync_RebaseConflict_RestoresAll verifies that when a rebase conflict
// occurs during sync, all branches are restored to their original state.
func TestSync_RebaseConflict_RestoresAll(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var resets []resetCall
	var checkouts []string
	currentBranch := "b1"
	abortCalled := false

	mock := newSyncMock(tmpDir, "b1")
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		return a == "local-sha" && d == "remote-sha", nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(name string) error {
		checkouts = append(checkouts, name)
		currentBranch = name
		return nil
	}
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil } // b1 succeeds
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "b2" {
			return fmt.Errorf("conflict")
		}
		return nil
	}
	mock.RebaseAbortFn = func() error {
		abortCalled = true
		return nil
	}
	mock.ResetHardFn = func(ref string) error {
		resets = append(resets, resetCall{currentBranch, ref})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Error(t, err, "sync returns error on conflict")
	assert.Contains(t, output, "Conflict detected")
	assert.Contains(t, output, "gh stack rebase")

	// All branches should be restored
	resetMap := make(map[string]string)
	for _, r := range resets {
		resetMap[r.branch] = r.sha
	}
	assert.Equal(t, "sha-b1", resetMap["b1"])
	assert.Equal(t, "sha-b2", resetMap["b2"])
	assert.Equal(t, "sha-b3", resetMap["b3"])

	_ = abortCalled // RebaseAbort is called if IsRebaseInProgress returns true
}

// TestSync_NoRebaseWhenTrunkDidntMove verifies that when trunk hasn't moved,
// absolutely no rebase calls are made.
func TestSync_NoRebaseWhenTrunkDidntMove(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	rebaseCount := 0
	rebaseOntoCount := 0

	mock := newSyncMock(tmpDir, "b1")
	// Same SHA = no trunk movement
	mock.RevParseFn = func(ref string) (string, error) {
		return "same-sha", nil
	}
	mock.RebaseFn = func(string, git.RebaseOpts) error {
		rebaseCount++
		return nil
	}
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
		rebaseOntoCount++
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	assert.Equal(t, 0, rebaseCount, "no Rebase calls when trunk didn't move")
	assert.Equal(t, 0, rebaseOntoCount, "no RebaseOnto calls when trunk didn't move")
}

// TestSync_PushForceFlagDependsOnRebase verifies that the force flag on Push
// correlates with whether a rebase actually happened.
func TestSync_PushForceFlagDependsOnRebase(t *testing.T) {
	tests := []struct {
		name          string
		trunkMoved    bool
		expectedForce bool
	}{
		{"trunk_moved_force_push", true, true},
		{"trunk_static_normal_push", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "b1"},
				},
			}

			tmpDir := t.TempDir()
			writeStackFile(t, tmpDir, s)

			var pushCalls []pushCall

			mock := newSyncMock(tmpDir, "b1")
			mock.CheckoutBranchFn = func(string) error { return nil }
			mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
			mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { return nil }

			if tt.trunkMoved {
				mock.RevParseFn = func(ref string) (string, error) {
					if ref == "main" {
						return "local-sha", nil
					}
					if ref == "origin/main" {
						return "remote-sha", nil
					}
					return "sha-" + ref, nil
				}
				mock.IsAncestorFn = func(a, d string) (bool, error) {
					return a == "local-sha" && d == "remote-sha", nil
				}
				mock.UpdateBranchRefFn = func(string, string) error { return nil }
			} else {
				mock.RevParseFn = func(ref string) (string, error) {
					return "same-sha", nil
				}
			}

			mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
				pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
				return nil
			}

			restore := git.SetOps(mock)
			defer restore()

			cfg, _, _ := config.NewTestConfig()
			cmd := SyncCmd(cfg)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()

			cfg.Out.Close()
			cfg.Err.Close()

			assert.NoError(t, err)
			require.Len(t, pushCalls, 1, "exactly one push call expected")
			assert.Equal(t, tt.expectedForce, pushCalls[0].force,
				"force flag should be %v when trunkMoved=%v", tt.expectedForce, tt.trunkMoved)
		})
	}
}

// TestSync_MergedBranch_UsesOnto verifies that when a merged
// branch exists in the stack, sync's cascade rebase correctly uses --onto
// to skip the merged branch and rebase subsequent branches onto the right base.
func TestSync_MergedBranch_UsesOnto(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseOntoCalls []rebaseCall
	var pushCalls []pushCall

	// Use explicit SHAs so assertions are self-documenting
	branchSHAs := map[string]string{
		"b1": "b1-orig-sha",
		"b2": "b2-orig-sha",
		"b3": "b3-orig-sha",
	}

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	// Trunk behind remote to trigger rebase
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		// Trunk: local is behind remote → triggers fast-forward
		if a == "local-sha" && d == "remote-sha" {
			return true, nil
		}
		// For --onto stale-check: old bases are valid ancestors (first-run)
		return true, nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseOntoCalls = append(rebaseOntoCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)

	// b1 is merged → skipped, needsOnto=true, ontoOldBase=b1-orig-sha
	// b2: first active branch after merged → RebaseOnto(main, b1-orig-sha, b2)
	// b3: normal --onto → RebaseOnto(b2, b2-orig-sha, b3)
	require.Len(t, rebaseOntoCalls, 2)
	assert.Equal(t, rebaseCall{"main", "b1-orig-sha", "b2"}, rebaseOntoCalls[0])
	assert.Equal(t, rebaseCall{"b2", "b2-orig-sha", "b3"}, rebaseOntoCalls[1])

	// Push should use force (rebase happened)
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force)
}

// TestSync_StaleOntoOldBase_FallsBackToMergeBase verifies that when a branch
// was already rebased past the merged branch's tip, sync detects the stale
// ontoOldBase and falls back to merge-base for the correct divergence point.
func TestSync_StaleOntoOldBase_FallsBackToMergeBase(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseOntoCalls []rebaseCall

	branchSHAs := map[string]string{
		"b1": "b1-stale-presquash-sha",
		"b2": "b2-on-main-sha",
		"b3": "b3-on-b2-sha",
	}

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		// Trunk: local is behind remote
		if a == "local-sha" && d == "remote-sha" {
			return true, nil
		}
		// b1's stale SHA is NOT an ancestor of b2 (already rebased)
		if a == "b1-stale-presquash-sha" {
			return false, nil
		}
		return true, nil
	}
	mock.MergeBaseFn = func(a, b string) (string, error) {
		if a == "main" && b == "b2" {
			return "main-b2-mergebase", nil
		}
		return "default-mergebase", nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseOntoCalls = append(rebaseOntoCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(string, []string, bool, bool) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	require.Len(t, rebaseOntoCalls, 2)

	// b2: stale ontoOldBase → falls back to merge-base(main, b2)
	assert.Equal(t, rebaseCall{"main", "main-b2-mergebase", "b2"}, rebaseOntoCalls[0],
		"b2 should use merge-base as oldBase when ontoOldBase is stale")

	// b3: b2's SHA is a valid ancestor → uses it directly
	assert.Equal(t, rebaseCall{"b2", "b2-on-main-sha", "b3"}, rebaseOntoCalls[1],
		"b3 should use b2's original SHA as oldBase")
}

// TestSync_PushFailureAfterRebase verifies that when push fails after a
// successful rebase, the command does not return a fatal error — only a
// warning is printed about the push failure.
func TestSync_PushFailureAfterRebase(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	// Trunk behind remote → triggers rebase
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		return a == "local-sha" && d == "remote-sha", nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { return nil }
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return fmt.Errorf("network error: connection refused")
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	// Push failures are warnings, not fatal errors.
	assert.NoError(t, err)
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force, "push after rebase should use force")
	assert.Contains(t, output, "Push failed")
}

// TestSync_BranchFastForward_TriggersRebase verifies that when trunk hasn't
// moved but a stack branch has new remote commits, the branch is fast-forwarded,
// downstream branches are cascade-rebased, and force push is used.
func TestSync_BranchFastForward_TriggersRebase(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall
	var pushCalls []pushCall
	var mergeFFCalls []string

	mock := newSyncMock(tmpDir, "b1")
	// Trunk is up to date (same SHA), but b1 is behind origin/b1
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "trunk-sha", nil
		}
		if ref == "b1" {
			return "b1-local-sha", nil
		}
		if ref == "origin/b1" {
			return "b1-remote-sha", nil
		}
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "b1-local-sha" && d == "b1-remote-sha" {
			return true, nil
		}
		return false, nil
	}
	mock.MergeFFFn = func(target string) error {
		mergeFFCalls = append(mergeFFCalls, target)
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{branch: "(rebase)" + base})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)

	// b1 should be fast-forwarded via MergeFF (since we're on b1)
	require.Len(t, mergeFFCalls, 1, "should fast-forward b1 via MergeFF")
	assert.Equal(t, "origin/b1", mergeFFCalls[0])
	assert.Contains(t, output, "Fast-forwarded b1")

	// Cascade rebase should be triggered (even though trunk didn't move)
	assert.NotEmpty(t, rebaseCalls, "rebase should occur when branch was fast-forwarded")

	// Push should use force-with-lease after rebase
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force, "push should use force when rebase occurred after branch FF")
}

// TestSync_BranchFastForward_WithTrunkUpdate verifies that when both trunk
// and a stack branch have remote updates, both are handled correctly.
func TestSync_BranchFastForward_WithTrunkUpdate(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var updateBranchRefCalls []struct{ branch, sha string }
	var rebaseCalls2 []rebaseCall
	var pushCalls2 []pushCall

	mock := newSyncMock(tmpDir, "b1")
	// Trunk and b2 both behind remote
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "trunk-local", nil
		}
		if ref == "origin/main" {
			return "trunk-remote", nil
		}
		if ref == "b2" {
			return "b2-local", nil
		}
		if ref == "origin/b2" {
			return "b2-remote", nil
		}
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "trunk-local" && d == "trunk-remote" {
			return true, nil
		}
		if a == "b2-local" && d == "b2-remote" {
			return true, nil
		}
		return false, nil
	}
	mock.UpdateBranchRefFn = func(branch, sha string) error {
		updateBranchRefCalls = append(updateBranchRefCalls, struct{ branch, sha string }{branch, sha})
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		rebaseCalls2 = append(rebaseCalls2, rebaseCall{branch: "(rebase)" + base})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls2 = append(rebaseCalls2, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls2 = append(pushCalls2, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	// Both trunk and b2 should be updated
	branchUpdates := make(map[string]string)
	for _, c := range updateBranchRefCalls {
		branchUpdates[c.branch] = c.sha
	}
	assert.Equal(t, "trunk-remote", branchUpdates["main"], "trunk should be fast-forwarded")
	assert.Equal(t, "b2-remote", branchUpdates["b2"], "b2 should be fast-forwarded")

	assert.Contains(t, output, "fast-forwarded")
	assert.NotEmpty(t, rebaseCalls2, "rebase should occur")
	require.Len(t, pushCalls2, 1)
	assert.True(t, pushCalls2[0].force, "push should use force after rebase")
}

func TestSync_MergedBranchDeletedFromRemote(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", Head: "b1-stored-head-sha", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseOntoCalls []rebaseCall

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool {
		// b1 does not exist locally (deleted from remote after merge)
		return name != "b1"
	}
	mock.RevParseMultiFn = func(refs []string) ([]string, error) {
		shas := make([]string, len(refs))
		for i, r := range refs {
			if r == "b1" {
				t.Fatalf("RevParseMulti should not be called with non-existent branch b1")
			}
			if r == "main" {
				shas[i] = "local-sha"
			} else if r == "origin/main" {
				shas[i] = "remote-sha"
			} else {
				shas[i] = "sha-" + r
			}
		}
		return shas, nil
	}
	// Trunk behind remote to trigger rebase
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" {
			return "local-sha", nil
		}
		if ref == "origin/main" {
			return "remote-sha", nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		// Trunk FF check
		if a == "local-sha" && d == "remote-sha" {
			return true, nil
		}
		// For --onto stale-check: old bases are valid ancestors (first-run)
		return true, nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseOntoCalls = append(rebaseOntoCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")

	// Only b2 should be rebased, and the rebase should use b1's stored
	// Head SHA as oldBase so `git rebase --onto` receives valid arguments.
	require.Len(t, rebaseOntoCalls, 1)
	assert.Equal(t, "b2", rebaseOntoCalls[0].branch)
	assert.Equal(t, "main", rebaseOntoCalls[0].newBase)
	assert.Equal(t, "b1-stored-head-sha", rebaseOntoCalls[0].oldBase)
}

// TestSync_Prune_DeletesMergedBranches verifies that --prune deletes local
// branches for merged PRs while keeping them in the stack metadata.
func TestSync_Prune_DeletesMergedBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var deletedBranches []string
	var deletedTrackingRefs []string

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(name string, force bool) error {
		deletedBranches = append(deletedBranches, name)
		assert.True(t, force, "should force-delete merged branch")
		return nil
	}
	mock.DeleteTrackingRefFn = func(remote, branch string) error {
		deletedTrackingRefs = append(deletedTrackingRefs, remote+"/"+branch)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetArgs([]string{"--prune"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1"}, deletedBranches)
	assert.Equal(t, []string{"origin/b1"}, deletedTrackingRefs, "should delete remote-tracking ref for pruned branch")
	assert.Contains(t, output, "Pruned b1 (merged)")
	assert.Contains(t, output, "Pruned 1 merged branch")
}

// TestSync_Prune_SkipsNonExistentBranches verifies that --prune does not
// attempt to delete branches that have already been removed locally.
func TestSync_Prune_SkipsNonExistentBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", Head: "sha-b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool {
		return name != "b1" // b1 already deleted
	}
	mock.DeleteBranchFn = func(string, bool) error {
		t.Fatal("DeleteBranch should not be called for non-existent branches")
		return nil
	}

	var deletedTrackingRefs []string
	mock.DeleteTrackingRefFn = func(remote, branch string) error {
		deletedTrackingRefs = append(deletedTrackingRefs, remote+"/"+branch)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetArgs([]string{"--prune"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "No merged branches to prune")
	// Tracking ref should still be cleaned up even though local branch is gone
	assert.Equal(t, []string{"origin/b1"}, deletedTrackingRefs, "should delete tracking ref even when local branch is already gone")
}

// TestSync_Prune_SwitchesToLowestUnmergedBranch verifies that when the user is
// on a merged branch being pruned, checkout moves to the lowest active branch.
func TestSync_Prune_SwitchesToLowestUnmergedBranch(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var deletedBranches []string
	var checkoutTarget string

	mock := newSyncMock(tmpDir, "b1") // currently on merged branch
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.CheckoutBranchFn = func(name string) error {
		checkoutTarget = name
		return nil
	}
	mock.DeleteBranchFn = func(name string, force bool) error {
		deletedBranches = append(deletedBranches, name)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetArgs([]string{"--prune"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1"}, deletedBranches)
	// Should have switched to b2 (first active branch), not trunk
	assert.Equal(t, "b2", checkoutTarget)
	assert.Contains(t, output, "Pruned b1 (merged)")
}

// TestSync_Prune_SwitchesToTrunkWhenAllMerged verifies that when all branches
// are merged, checkout moves to the trunk.
func TestSync_Prune_SwitchesToTrunkWhenAllMerged(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var deletedBranches []string
	var checkoutTarget string

	mock := newSyncMock(tmpDir, "b1") // currently on merged branch
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.CheckoutBranchFn = func(name string) error {
		checkoutTarget = name
		return nil
	}
	mock.DeleteBranchFn = func(name string, force bool) error {
		deletedBranches = append(deletedBranches, name)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetArgs([]string{"--prune"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2"}, deletedBranches)
	// Should have switched to trunk since all branches are merged
	assert.Equal(t, "main", checkoutTarget)
	assert.Contains(t, output, "Pruned 2 merged branches")
}

// TestSync_NoPrune_DoesNotDeleteBranches verifies that without --prune,
// merged branches are not deleted (default behavior is unchanged).
func TestSync_NoPrune_DoesNotDeleteBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(string, bool) error {
		t.Fatal("DeleteBranch should not be called without --prune")
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	// No --prune flag
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
}

// TestSync_Prune_DeleteFailureContinues verifies that a failed branch deletion
// logs a warning and does not abort the sync.
func TestSync_Prune_DeleteFailureContinues(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var deletedBranches []string

	mock := newSyncMock(tmpDir, "b3")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(name string, force bool) error {
		if name == "b1" {
			return fmt.Errorf("permission denied")
		}
		deletedBranches = append(deletedBranches, name)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetArgs([]string{"--prune"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	// b1 failed, b2 succeeded
	assert.Equal(t, []string{"b2"}, deletedBranches)
	assert.Contains(t, output, "Failed to delete b1")
	assert.Contains(t, output, "Pruned b2 (merged)")
	assert.Contains(t, output, "Pruned 1 merged branch")
}

// TestSync_InteractivePrune_PromptsAndPrunes verifies that when running in an
// interactive terminal without --prune, the user is prompted and merged branches
// are pruned when they confirm.
func TestSync_InteractivePrune_PromptsAndPrunes(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var deletedBranches []string
	var promptShown string

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(name string, force bool) error {
		deletedBranches = append(deletedBranches, name)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(prompt string, defaultValue bool) (bool, error) {
		promptShown = prompt
		assert.True(t, defaultValue, "default should be yes")
		return true, nil // user confirms
	}

	cmd := SyncCmd(cfg)
	// No --prune flag
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, promptShown, "Prune 1 merged branch")
	assert.Equal(t, []string{"b1"}, deletedBranches)
	assert.Contains(t, output, "Pruned b1 (merged)")
}

// TestSync_InteractivePrune_UserDeclines verifies that when the user declines
// the prune prompt, no branches are deleted.
func TestSync_InteractivePrune_UserDeclines(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(string, bool) error {
		t.Fatal("DeleteBranch should not be called when user declines")
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) {
		return false, nil // user declines
	}

	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
}

// TestSync_NonInteractive_NoPrunePrompt verifies that when the terminal is not
// interactive and --prune is not set, no prompt is shown and no branches are deleted.
func TestSync_NonInteractive_NoPrunePrompt(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(string, bool) error {
		t.Fatal("DeleteBranch should not be called in non-interactive mode without --prune")
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	// ForceInteractive is false by default — simulates non-interactive/CI/agent

	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
}

// TestSync_ExplicitPrune_SkipsPrompt verifies that --prune flag bypasses the
// interactive prompt and prunes directly.
func TestSync_ExplicitPrune_SkipsPrompt(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var deletedBranches []string

	mock := newSyncMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.DeleteBranchFn = func(name string, force bool) error {
		deletedBranches = append(deletedBranches, name)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) {
		t.Fatal("ConfirmFn should not be called when --prune is explicit")
		return false, nil
	}

	cmd := SyncCmd(cfg)
	cmd.SetArgs([]string{"--prune"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1"}, deletedBranches)
}

// --- Remote stack object reconciliation -------------------------------------

// newSyncMockNoRebase returns a sync git mock whose trunk is already up to date,
// so no fast-forward or cascade rebase occurs and the run reaches the remote
// stack reconciliation step cleanly.
func newSyncMockNoRebase(tmpDir, currentBranch string) *git.MockOps {
	m := newSyncMock(tmpDir, currentBranch)
	m.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "trunk-sha", nil
		}
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	return m
}

// openPRFinder returns a FindPRForBranch func that reports OPEN PRs for the
// branches in prFor (branch name -> PR number) and nil for any other branch.
func openPRFinder(prFor map[string]int) func(string) (*github.PullRequest, error) {
	return func(branch string) (*github.PullRequest, error) {
		n, ok := prFor[branch]
		if !ok {
			return nil, nil
		}
		return &github.PullRequest{
			Number:      n,
			State:       "OPEN",
			URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			HeadRefName: branch,
		}, nil
	}
}

// runSyncWithGitHub executes sync against tmpDir using the supplied git and
// GitHub mocks and returns the captured stderr output.
func runSyncWithGitHub(t *testing.T, gitMock *git.MockOps, ghMock *github.MockClient) string {
	t.Helper()
	restore := git.SetOps(gitMock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = ghMock
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	return string(errOut)
}

// TestSync_CreatesRemoteStackWhenPRsExist verifies the core fix: when the
// branches already have open PRs but no stack exists on GitHub, sync creates the
// stack object and reports "Stack synced".
func TestSync_CreatesRemoteStackWhenPRsExist(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var createdWith []int
	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102}),
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
		CreateStackFn: func(prNumbers []int) (int, error) {
			createdWith = prNumbers
			return 7, nil
		},
		UpdateStackFn: func(string, []int) error {
			t.Fatal("UpdateStack should not be called when no remote stack exists")
			return nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Equal(t, []int{101, 102}, createdWith, "should create the stack from both PR numbers")
	assert.Contains(t, output, "Stack created on GitHub with 2 PRs")
	assert.Contains(t, output, "Stack synced")
	assert.NotContains(t, output, "Branches synced")

	// The new remote stack ID must be persisted to the stack file.
	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, "7", sf.Stacks[0].ID)
}

// TestSync_AdoptsExistingEqualRemoteStack verifies that when a remote stack
// already lists exactly the local PRs, sync records its ID without issuing a
// redundant create/update and still reports "Stack synced".
func TestSync_AdoptsExistingEqualRemoteStack(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102}),
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, PullRequests: []int{101, 102}}}, nil
		},
		CreateStackFn: func([]int) (int, error) {
			t.Fatal("CreateStack should not be called when the remote stack matches")
			return 0, nil
		},
		UpdateStackFn: func(string, []int) error {
			t.Fatal("UpdateStack should not be called when the remote stack matches")
			return nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Contains(t, output, "Stack already up to date on GitHub")
	assert.Contains(t, output, "Stack synced")
	assert.NotContains(t, output, "Branches synced")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "9", sf.Stacks[0].ID, "should record the adopted stack ID")
}

// TestSync_UpdatesPartialRemoteStack verifies that when a remote stack contains
// only some of the local PRs, sync updates it with the full list.
func TestSync_UpdatesPartialRemoteStack(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var updatedID string
	var updatedWith []int
	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102, "b3": 103}),
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, PullRequests: []int{101, 102}}}, nil
		},
		CreateStackFn: func([]int) (int, error) {
			t.Fatal("CreateStack should not be called when a matching stack exists")
			return 0, nil
		},
		UpdateStackFn: func(stackID string, prNumbers []int) error {
			updatedID = stackID
			updatedWith = prNumbers
			return nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Equal(t, "9", updatedID)
	assert.Equal(t, []int{101, 102, 103}, updatedWith)
	assert.Contains(t, output, "Stack updated on GitHub with 3 PRs")
	assert.Contains(t, output, "Stack synced")
	assert.NotContains(t, output, "Branches synced")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "9", sf.Stacks[0].ID)
}

// TestSync_FewerThanTwoPRs_BranchesSynced verifies that with only one open PR
// (no stack is possible), sync skips all stack API calls and reports
// "Branches synced" rather than "Stack synced".
func TestSync_FewerThanTwoPRs_BranchesSynced(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var listCalled, createCalled bool
	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101}), // b2 has no PR
		ListStacksFn: func() ([]github.RemoteStack, error) {
			listCalled = true
			return nil, nil
		},
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 0, nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.False(t, listCalled, "ListStacks should not be called with fewer than two PRs")
	assert.False(t, createCalled, "CreateStack should not be called with fewer than two PRs")
	assert.Contains(t, output, "Branches synced")
	assert.NotContains(t, output, "Stack synced")
}

// TestSync_StacksUnavailable_BranchesSynced verifies that when the stacks API
// is unavailable (404 on create), sync warns and reports "Branches synced".
func TestSync_StacksUnavailable_BranchesSynced(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102}),
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
		CreateStackFn: func([]int) (int, error) {
			return 0, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Contains(t, output, "Branches synced")
	assert.NotContains(t, output, "Stack synced")
}

// TestSync_PRsSpanMultipleStacks_BranchesSynced verifies that when the local
// PRs belong to more than one remote stack, sync refuses to auto-resolve the
// divergence and reports "Branches synced".
func TestSync_PRsSpanMultipleStacks_BranchesSynced(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var createCalled, updateCalled bool
	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102}),
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 9, PullRequests: []int{101}},
				{ID: 10, PullRequests: []int{102}},
			}, nil
		},
		CreateStackFn: func([]int) (int, error) { createCalled = true; return 0, nil },
		UpdateStackFn: func(string, []int) error { updateCalled = true; return nil },
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.False(t, createCalled, "CreateStack should not be called on divergence")
	assert.False(t, updateCalled, "UpdateStack should not be called on divergence")
	assert.Contains(t, output, "multiple stacks")
	assert.Contains(t, output, "Branches synced")
	assert.NotContains(t, output, "Stack synced")
}
