package cmd

import (
	"fmt"
	"io"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
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
		RevParseFn:       func(ref string) (string, error) { return "sha-" + ref, nil },
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
		return "sha-" + ref, nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.RebaseFn = func(base string) error {
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
	mock.RebaseFn = func(base string) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{branch: "(rebase)" + base})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	mock.RebaseFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(string, string, string) error { return nil }

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
		return "sha-" + ref, nil
	}
	// Neither is ancestor of the other → diverged
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		return false, nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	mock.RebaseFn = func(string) error { return nil } // b1 succeeds
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	mock.RebaseFn = func(string) error {
		rebaseCount++
		return nil
	}
	mock.RebaseOntoFn = func(string, string, string) error {
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
			mock.RebaseFn = func(string) error { return nil }
			mock.RebaseOntoFn = func(string, string, string) error { return nil }

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

// TestSync_SquashMergedBranch_UsesOnto verifies that when a squash-merged
// branch exists in the stack, sync's cascade rebase correctly uses --onto
// to skip the merged branch and rebase subsequent branches onto the right base.
func TestSync_SquashMergedBranch_UsesOnto(t *testing.T) {
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
		return a == "local-sha" && d == "remote-sha", nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	mock.RebaseFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(string, string, string) error { return nil }
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
