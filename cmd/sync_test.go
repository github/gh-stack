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
		IsAncestorFn:         func(a, d string) (bool, error) { return true, nil },
		FetchFn:              func(string) error { return nil },
		EnableRerereFn:       func() error { return nil },
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
	// Stack branches are NOT rebased onto trunk until the cascade runs.
	rebased := false
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "main" && d == "b1" && !rebased {
			return false, nil
		}
		return true, nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{branch: "(rebase)" + base})
		rebased = true
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		rebased = true
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
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		return true, nil
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
					return true, nil
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

// TestSync_QueuedBranch_DownstreamStaysStacked verifies the #144 fix in the sync
// path: a queued branch is skipped from push (frozen in the merge queue) but
// downstream branches stay stacked on top of it — they are NOT rebased --onto
// trunk with the queued commits dropped.
func TestSync_QueuedBranch_DownstreamStaysStacked(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseOntoCalls []rebaseCall
	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b2")
	// Trunk behind remote to trigger rebase; branches match their remote.
	mock.RevParseFn = func(ref string) (string, error) {
		switch ref {
		case "main":
			return "local-sha", nil
		case "origin/main":
			return "remote-sha", nil
		}
		if strings.HasPrefix(ref, "origin/") {
			return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "sha-" + ref, nil
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

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = queuedPRClient(map[int]string{10: "b1"})
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "queued")

	// b1 is queued → skipped, but downstream stays stacked on it:
	// b2 onto b1 (not --onto main), b3 onto b2.
	require.Len(t, rebaseOntoCalls, 2)
	assert.Equal(t, rebaseCall{"b1", "sha-b1", "b2"}, rebaseOntoCalls[0],
		"b2 should rebase onto the queued b1, keeping its commits")
	assert.Equal(t, rebaseCall{"b2", "sha-b2", "b3"}, rebaseOntoCalls[1],
		"b3 should rebase onto b2")

	// The queued branch is excluded from push; only b2 and b3 are pushed.
	require.Len(t, pushCalls, 1)
	assert.Equal(t, []string{"b2", "b3"}, pushCalls[0].branches,
		"queued b1 must not be pushed")
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
		return true, nil
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
		return true, nil
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
		return true, nil
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
	var listCalls int
	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102}),
		ListStacksFn: func() ([]github.RemoteStack, error) {
			listCalls++
			return nil, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			createdWith = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called when no remote stack exists")
			return nil, nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Equal(t, []int{101, 102}, createdWith, "should create the stack from both PR numbers")
	assert.Equal(t, 1, listCalls, "should issue exactly one ListStacks on the create path (no redundant round-trip)")
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
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102}}}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			t.Fatal("CreateStack should not be called when the remote stack matches")
			return nil, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called when the remote stack matches")
			return nil, nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Contains(t, output, "already up to date")
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

	var updatedNumber int
	var addedWith []int
	ghMock := &github.MockClient{
		FindPRForBranchFn: openPRFinder(map[string]int{"b1": 101, "b2": 102, "b3": 103}),
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102}}}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			t.Fatal("CreateStack should not be called when a matching stack exists")
			return nil, nil
		},
		GetStackFn: func(stackNumber int) (*github.RemoteStack, error) {
			assert.Equal(t, 9, stackNumber)
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102}}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			updatedNumber = stackNumber
			addedWith = prNumbers
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102, 103}}, nil
		},
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.Equal(t, 9, updatedNumber)
	assert.Equal(t, []int{103}, addedWith)
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
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			createCalled = true
			return &github.RemoteStack{}, nil
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
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
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
				{ID: 9, Number: 9, PullRequests: []int{101}},
				{ID: 10, Number: 10, PullRequests: []int{102}},
			}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { createCalled = true; return nil, nil },
		AddToStackFn:  func(int, []int) (*github.RemoteStack, error) { updateCalled = true; return nil, nil },
	}

	output := runSyncWithGitHub(t, newSyncMockNoRebase(tmpDir, "b1"), ghMock)

	assert.False(t, createCalled, "CreateStack should not be called on divergence")
	assert.False(t, updateCalled, "AddToStack should not be called on divergence")
	assert.Contains(t, output, "multiple stacks")
	assert.NotContains(t, output, "submitting", "divergence guidance should be command-neutral, not submit-specific")
	assert.Contains(t, output, "Branches synced")
	assert.NotContains(t, output, "Stack synced")
}

// --- Remote-ahead pull & divergence reconciliation ---

// runSyncCfg runs sync against tmpDir with the given git mock, allowing the
// caller to configure the Config (GitHub override, interactivity, SelectFn).
// It returns the captured stderr output and the command's error.
func runSyncCfg(t *testing.T, gitMock *git.MockOps, configure func(*config.Config)) (string, error) {
	t.Helper()
	restore := git.SetOps(gitMock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	if configure != nil {
		configure(cfg)
	}
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	return string(errOut), err
}

// prByNumberFinder returns a FindPRByNumber func that reports OPEN PRs for the
// given number->branch map and nil for any other number.
func prByNumberFinder(branchByNum map[int]string) func(int) (*github.PullRequest, error) {
	return func(n int) (*github.PullRequest, error) {
		b, ok := branchByNum[n]
		if !ok {
			return nil, nil
		}
		return &github.PullRequest{
			Number:      n,
			ID:          fmt.Sprintf("PR_%d", n),
			URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			HeadRefName: b,
			State:       "OPEN",
		}, nil
	}
}

func TestClassifyRemoteStack(t *testing.T) {
	tests := []struct {
		name         string
		localActive  []string
		remoteActive []string
		want         remoteStackClass
	}{
		{"identical", []string{"b1", "b2"}, []string{"b1", "b2"}, remoteStackInSync},
		{"clean append on top", []string{"b1", "b2"}, []string{"b1", "b2", "b3"}, remoteStackCleanAhead},
		{"local ahead", []string{"b1", "b2", "b3"}, []string{"b1", "b2"}, remoteStackLocalAhead},
		{"divergent tip", []string{"b1", "b2", "b3"}, []string{"b1", "b2", "b4"}, remoteStackDivergent},
		{"divergent reorder", []string{"b1", "b2"}, []string{"b2", "b1"}, remoteStackDivergent},
		{"empty local", nil, []string{"b1"}, remoteStackCleanAhead},
		{"empty remote", []string{"b1"}, nil, remoteStackLocalAhead},
		{"both empty", nil, nil, remoteStackInSync},
		{"divergent middle", []string{"b1", "x"}, []string{"b1", "y", "z"}, remoteStackDivergent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyRemoteStack(tt.localActive, tt.remoteActive))
		})
	}
}

// TestSync_RemoteAhead_PullsNewBranches verifies the core new behavior: when the
// remote stack has PRs appended on top of the local stack, sync pulls the new
// branches down and adds them to the local stack.
func TestSync_RemoteAhead_PullsNewBranches(t *testing.T) {
	s := stack.Stack{
		ID:    "9",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 103}},
		},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var created, fetched []string
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.BranchExistsFn = func(name string) bool { return name != "b4" && name != "b5" }
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }
	mock.FetchBranchesFn = func(_ string, branches []string) error { fetched = append(fetched, branches...); return nil }
	mock.SetUpstreamTrackingFn = func(string, string) error { return nil }

	ghMock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102, 103, 104, 105}}}, nil
		},
		GetStackFn: func(stackNumber int) (*github.RemoteStack, error) {
			assert.Equal(t, 9, stackNumber)
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102, 103, 104, 105}}, nil
		},
		FindPRByNumberFn: prByNumberFinder(map[int]string{101: "b1", 102: "b2", 103: "b3", 104: "b4", 105: "b5"}),
	}

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) { cfg.GitHubClientOverride = ghMock })
	require.NoError(t, err)

	assert.Contains(t, created, "b4")
	assert.Contains(t, created, "b5")
	assert.Subset(t, fetched, []string{"b4", "b5"})
	assert.Contains(t, output, "Pulling 2 new branches from the remote stack")
	assert.Contains(t, output, "Pulled 2 new branches into the stack")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2", "b3", "b4", "b5"}, sf.Stacks[0].BranchNames())
}

// TestSync_RemoteAhead_QueuedBranchNotPushed verifies that a pulled branch whose
// PR is in the merge queue has its transient queued state copied from the fresh
// PR details during reconciliation, so it is not force-pushed by the later push
// step.
func TestSync_RemoteAhead_QueuedBranchNotPushed(t *testing.T) {
	s := stack.Stack{
		ID:    "9",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var created []string
	var pushes []pushCall
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.BranchExistsFn = func(name string) bool { return name != "b3" }
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }
	mock.SetUpstreamTrackingFn = func(string, string) error { return nil }
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushes = append(pushes, pushCall{remote, branches, force, atomic})
		return nil
	}

	ghMock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102, 103}}}, nil
		},
		GetStackFn: func(stackNumber int) (*github.RemoteStack, error) {
			assert.Equal(t, 9, stackNumber)
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102, 103}}, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			branch := map[int]string{101: "b1", 102: "b2", 103: "b3"}[n]
			if branch == "" {
				return nil, nil
			}
			pr := &github.PullRequest{
				Number: n, ID: fmt.Sprintf("PR_%d", n),
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
				HeadRefName: branch, State: "OPEN",
			}
			if n == 103 {
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQ1"}
			}
			return pr, nil
		},
	}

	_, err := runSyncCfg(t, mock, func(cfg *config.Config) { cfg.GitHubClientOverride = ghMock })
	require.NoError(t, err)

	assert.Contains(t, created, "b3", "the queued branch is still pulled into the local stack")
	for _, pc := range pushes {
		assert.NotContains(t, pc.branches, "b3", "a merge-queued branch must not be pushed")
	}
}

// TestSync_RemoteAhead_DuplicateBranchAborts verifies that pulling a remote
// addition whose branch is already owned by another local stack aborts rather
// than writing the branch into two stacks.
func TestSync_RemoteAhead_DuplicateBranchAborts(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFileMulti(t, tmpDir,
		stack.Stack{
			ID:    "9",
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
				{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
			},
		},
		stack.Stack{
			Trunk:    stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{{Branch: "b3"}}, // another stack already owns b3
		},
	)

	var created []string
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }

	ghMock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102, 103}}}, nil
		},
		FindPRByNumberFn: prByNumberFinder(map[int]string{101: "b1", 102: "b2", 103: "b3"}),
	}

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) { cfg.GitHubClientOverride = ghMock })

	assert.Error(t, err)
	assert.Contains(t, output, "Cannot pull b3")
	assert.NotContains(t, created, "b3", "must not pull a branch owned by another stack")

	sf, loadErr := stack.Load(tmpDir)
	require.NoError(t, loadErr)
	assert.Equal(t, []string{"b1", "b2"}, sf.Stacks[0].BranchNames(), "tracked stack unchanged")
}

// TestSync_Divergent_UseRemote_DirtyCheckErrorAborts verifies that when the
// working-tree status cannot be determined, "use remote" aborts instead of
// treating the tree as clean and running the destructive replace.
func TestSync_Divergent_UseRemote_DirtyCheckErrorAborts(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir)

	ghMock := divergentRemoteMock()
	var created []string
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }
	mock.HasUncommittedChangesFn = func() (bool, error) { return false, fmt.Errorf("git status failed") }

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 0, nil }
	})

	assert.Error(t, err)
	assert.Contains(t, output, "Could not determine whether the working tree is clean")
	assert.Empty(t, created, "must not replace the local stack when the working-tree check fails")

	sf, loadErr := stack.Load(tmpDir)
	require.NoError(t, loadErr)
	assert.Equal(t, []string{"b1", "b2", "b3"}, sf.Stacks[0].BranchNames(), "local stack untouched")
}

// TestSync_RemoteInSync_NoPull verifies that when local and remote match, no
// branches are pulled and no divergence is reported.
func TestSync_RemoteInSync_NoPull(t *testing.T) {
	s := stack.Stack{
		ID:    "9",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var created []string
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }

	ghMock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102}}}, nil
		},
		GetStackFn: func(stackNumber int) (*github.RemoteStack, error) {
			assert.Equal(t, 9, stackNumber)
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102}}, nil
		},
		FindPRByNumberFn: prByNumberFinder(map[int]string{101: "b1", 102: "b2"}),
	}

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) { cfg.GitHubClientOverride = ghMock })
	require.NoError(t, err)

	assert.Empty(t, created, "no branches should be pulled when in sync")
	assert.NotContains(t, output, "Pulling")
	assert.NotContains(t, output, "diverged")
	assert.Contains(t, output, "Stack synced")
}

// divergentStack returns a stack file (ID 9) and GitHub mock configured so that
// the local stack [b1,b2,b3] diverges from the remote stack [b1,b2,b4].
func divergentStack(t *testing.T, tmpDir string) {
	t.Helper()
	s := stack.Stack{
		ID:    "9",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 103}},
		},
	}
	writeStackFile(t, tmpDir, s)
}

func divergentRemoteMock() *github.MockClient {
	return &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102, 104}}}, nil
		},
		GetStackFn: func(stackNumber int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102, 104}}, nil
		},
		FindPRByNumberFn: prByNumberFinder(map[int]string{101: "b1", 102: "b2", 103: "b3", 104: "b4"}),
	}
}

// TestSync_Divergent_NonInteractive_Aborts verifies that a divergence in a
// non-interactive terminal aborts the sync: no branches are pushed, no stack API
// mutations occur, guidance is printed, the association is preserved, and it
// exits successfully.
func TestSync_Divergent_NonInteractive_Aborts(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir)

	ghMock := divergentRemoteMock()
	var created []string
	var pushed bool
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }
	mock.PushFn = func(string, []string, bool, bool) error { pushed = true; return nil }
	ghMock.CreateStackFn = func([]int) (*github.RemoteStack, error) {
		t.Fatal("CreateStack must not be called")
		return nil, nil
	}
	ghMock.AddToStackFn = func(int, []int) (*github.RemoteStack, error) {
		t.Fatal("AddToStack must not be called")
		return nil, nil
	}
	ghMock.UnstackFn = func(int) (*github.RemoteStack, bool, error) {
		t.Fatal("Unstack must not be called")
		return nil, false, nil
	}

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) { cfg.GitHubClientOverride = ghMock })
	require.NoError(t, err)

	assert.Empty(t, created)
	assert.False(t, pushed, "branches must not be pushed when sync aborts")
	assert.Contains(t, output, "diverged")
	assert.Contains(t, output, "Sync aborted")
	assert.NotContains(t, output, "Branches synced")
	assert.NotContains(t, output, "Stack synced")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "9", sf.Stacks[0].ID, "association is preserved")
	assert.Equal(t, []string{"b1", "b2", "b3"}, sf.Stacks[0].BranchNames())
}

// TestSync_Divergent_UseRemote replaces the local stack with the remote version.
func TestSync_Divergent_UseRemote(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir)

	ghMock := divergentRemoteMock()
	var created []string
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.BranchExistsFn = func(name string) bool { return name != "b4" }
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }
	mock.SetUpstreamTrackingFn = func(string, string) error { return nil }
	mock.HasUncommittedChangesFn = func() (bool, error) { return false, nil }

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 0, nil }
	})
	require.NoError(t, err)

	assert.Contains(t, created, "b4")
	assert.Contains(t, output, "replaced with the remote version")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2", "b4"}, sf.Stacks[0].BranchNames())
	assert.Equal(t, "9", sf.Stacks[0].ID)
}

func TestNearestBranchAfterReplace(t *testing.T) {
	newStack := func(branches ...string) *stack.Stack {
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}
		for _, b := range branches {
			s.Branches = append(s.Branches, stack.BranchRef{Branch: b})
		}
		return s
	}
	tests := []struct {
		name        string
		old         []string
		current     string
		newBranches []string
		want        string
	}{
		{"still in stack", []string{"b1", "b2", "b3"}, "b2", []string{"b1", "b2", "b4"}, "b2"},
		{"dropped top prefers below", []string{"b1", "b2", "b3"}, "b3", []string{"b1", "b2", "b4"}, "b2"},
		{"dropped middle prefers above", []string{"b1", "b2", "b3"}, "b2", []string{"b1", "b3"}, "b3"},
		{"on trunk stays put", []string{"b1", "b2"}, "main", []string{"b1", "b2", "b4"}, "main"},
		{"none survive falls back to top", []string{"x", "y", "z"}, "y", []string{"a", "b", "c"}, "c"},
		{"empty new stack falls back to trunk", []string{"b1"}, "b1", nil, "main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, nearestBranchAfterReplace(tt.old, tt.current, newStack(tt.newBranches...)))
		})
	}
}

// TestSync_Divergent_UseRemote_SwitchesOffDroppedBranch verifies that when the
// user is on a branch that the remote stack no longer contains, replacing the
// local stack with the remote moves them to the nearest surviving branch.
func TestSync_Divergent_UseRemote_SwitchesOffDroppedBranch(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir) // local [b1,b2,b3], remote [b1,b2,b4]; user on b3 (dropped)

	ghMock := divergentRemoteMock()
	current := "b3"
	var checkouts []string
	mock := newSyncMockNoRebase(tmpDir, "b3")
	mock.CurrentBranchFn = func() (string, error) { return current, nil }
	mock.CheckoutBranchFn = func(name string) error { current = name; checkouts = append(checkouts, name); return nil }
	mock.BranchExistsFn = func(name string) bool { return name != "b4" }
	mock.CreateBranchFn = func(string, string) error { return nil }
	mock.SetUpstreamTrackingFn = func(string, string) error { return nil }
	mock.HasUncommittedChangesFn = func() (bool, error) { return false, nil }

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 0, nil }
	})
	require.NoError(t, err)

	assert.Contains(t, checkouts, "b2", "should switch off dropped branch b3 to nearest surviving branch b2")
	assert.NotContains(t, checkouts, "b3", "should never check the dropped branch back out")
	assert.Contains(t, output, "Switched to b2")
	assert.Contains(t, output, "no longer in the stack")
	assert.Equal(t, "b2", current, "should end on b2, not the dropped b3")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2", "b4"}, sf.Stacks[0].BranchNames())
}
func TestSync_Divergent_UseRemote_DirtyBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir)

	ghMock := divergentRemoteMock()
	var created []string
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }
	mock.HasUncommittedChangesFn = func() (bool, error) { return true, nil }

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 0, nil }
	})

	assert.Error(t, err)
	assert.Contains(t, output, "uncommitted changes")
	assert.Empty(t, created)

	sf, loadErr := stack.Load(tmpDir)
	require.NoError(t, loadErr)
	assert.Equal(t, []string{"b1", "b2", "b3"}, sf.Stacks[0].BranchNames(), "local stack untouched")
	assert.Equal(t, "9", sf.Stacks[0].ID)
}

// TestSync_Divergent_DeleteRemote deletes the diverged remote stack, clears the
// local association, and stops the sync (pointing the user at submit) without
// recreating the stack or pushing.
func TestSync_Divergent_DeleteRemote(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir)

	deleted := false
	var deletedNumber int
	var pushed bool
	ghMock := divergentRemoteMock()
	ghMock.UnstackFn = func(number int) (*github.RemoteStack, bool, error) {
		deleted = true
		deletedNumber = number
		return nil, true, nil
	}
	ghMock.CreateStackFn = func([]int) (*github.RemoteStack, error) {
		t.Fatal("CreateStack must not be called")
		return nil, nil
	}
	ghMock.AddToStackFn = func(int, []int) (*github.RemoteStack, error) {
		t.Fatal("AddToStack must not be called")
		return nil, nil
	}
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { pushed = true; return nil }

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 1, nil }
	})
	require.NoError(t, err)

	assert.True(t, deleted, "remote stack should be deleted")
	assert.Equal(t, 9, deletedNumber)
	assert.False(t, pushed, "sync should stop after deleting the remote stack")
	assert.Contains(t, output, "Deleted the stack on GitHub")
	assert.Contains(t, output, "gh stack submit")
	assert.NotContains(t, output, "Stack synced")
	assert.NotContains(t, output, "Branches synced")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "", sf.Stacks[0].ID, "local association is cleared")
	assert.Equal(t, []string{"b1", "b2", "b3"}, sf.Stacks[0].BranchNames(), "local branches untouched")
}

// TestSync_Divergent_Cancel makes no changes and preserves the association.
func TestSync_Divergent_Cancel(t *testing.T) {
	tmpDir := t.TempDir()
	divergentStack(t, tmpDir)

	ghMock := divergentRemoteMock()
	ghMock.UnstackFn = func(int) (*github.RemoteStack, bool, error) {
		t.Fatal("Unstack must not be called")
		return nil, false, nil
	}
	ghMock.CreateStackFn = func([]int) (*github.RemoteStack, error) {
		t.Fatal("CreateStack must not be called")
		return nil, nil
	}
	ghMock.AddToStackFn = func(int, []int) (*github.RemoteStack, error) {
		t.Fatal("AddToStack must not be called")
		return nil, nil
	}
	var pushed bool
	mock := newSyncMockNoRebase(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { pushed = true; return nil }

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 2, nil }
	})
	require.NoError(t, err)

	assert.False(t, pushed, "branches must not be pushed when the user cancels")
	assert.Contains(t, output, "Sync aborted")
	assert.NotContains(t, output, "Branches synced")
	assert.NotContains(t, output, "Stack synced")

	sf, err := stack.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "9", sf.Stacks[0].ID, "association is preserved")
	assert.Equal(t, []string{"b1", "b2", "b3"}, sf.Stacks[0].BranchNames())
}

// TestSync_MergedBranchPruned_NoFalseDivergence verifies that a merged branch
// (still tracked locally but reported merged by the remote) does not classify as
// a divergence.
func TestSync_MergedBranchPruned_NoFalseDivergence(t *testing.T) {
	s := stack.Stack{
		ID:    "9",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 103}},
		},
	}
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var created []string
	mock := newSyncMockNoRebase(tmpDir, "b2")
	mock.CreateBranchFn = func(name, base string) error { created = append(created, name); return nil }

	ghMock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 9, Number: 9, PullRequests: []int{101, 102, 103}}}, nil
		},
		GetStackFn: func(stackNumber int) (*github.RemoteStack, error) {
			assert.Equal(t, 9, stackNumber)
			return &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102, 103}}, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			branch := map[int]string{101: "b1", 102: "b2", 103: "b3"}[n]
			if branch == "" {
				return nil, nil
			}
			return &github.PullRequest{
				Number: n, ID: fmt.Sprintf("PR_%d", n),
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
				HeadRefName: branch,
				State:       map[bool]string{true: "MERGED", false: "OPEN"}[n == 101],
				Merged:      n == 101,
			}, nil
		},
	}

	output, err := runSyncCfg(t, mock, func(cfg *config.Config) {
		cfg.GitHubClientOverride = ghMock
		cfg.ForceInteractive = true
		cfg.SelectFn = func(_, _ string, _ []string) (int, error) {
			t.Fatal("no prompt expected when merged branch is pruned")
			return 0, nil
		}
	})
	require.NoError(t, err)

	assert.Empty(t, created)
	assert.NotContains(t, output, "diverged")
}
