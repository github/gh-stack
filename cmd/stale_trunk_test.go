package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoBranchStack is the stack shared by the tests in this file.
func twoBranchStack() stack.Stack {
	return stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}
}

// staleTrunkRevParse resolves the trunk behind its remote and every other
// branch identically on both sides, so only the trunk needs updating.
func staleTrunkRevParse(ref string) (string, error) {
	switch ref {
	case "main":
		return "trunk-local", nil
	case "origin/main":
		return "trunk-remote", nil
	}
	if strings.HasPrefix(ref, "origin/") {
		return "sha-" + strings.TrimPrefix(ref, "origin/"), nil
	}
	return "sha-" + ref, nil
}

// TestRebase_TrunkLockedByWorktree_RebasesOntoRemoteRef covers issue #155 and
// the Slack report: the local trunk cannot be moved because another worktree
// has it checked out. The cascade must target origin/main instead of silently
// rebasing the stack onto the stale local trunk and reporting success.
func TestRebase_TrunkLockedByWorktree_RebasesOntoRemoteRef(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var rebaseBases []string

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.RevParseFn = staleTrunkRevParse
	mock.IsAncestorFn, _ = ancestorMock([][2]string{{"trunk-remote", "trunk-local"}}, nil)
	mock.UpdateBranchRefFn = func(string, string) error {
		return errors.New("cannot force update the branch 'main' used by worktree at '/elsewhere'")
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, base)
		return nil
	}
	mock.RebaseOntoFn = func(newBase, _, _ string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, newBase)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.NoError(t, err)
	require.Equal(t, []string{"origin/main", "b1"}, rebaseBases,
		"the bottom branch must be rebased onto the remote-tracking ref")
	assert.Contains(t, output, "worktree")
	assert.Contains(t, output, "rebased locally with origin/main",
		"the summary must name the ref the stack actually landed on")
}

// TestRebase_UnstackedAfterCascade_ReportsFailure verifies the post-condition:
// if the branches are still not sitting on their parents once the cascade
// claims success, the command fails instead of printing a success summary.
func TestRebase_UnstackedAfterCascade_ReportsFailure(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	// Every rebase "succeeds" but nothing ever ends up stacked.
	mock.IsAncestorFn, _ = ancestorMock([][2]string{{"main", "b1"}}, nil)
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "still not based on")
	assert.Contains(t, output, "b1")
	assert.NotContains(t, output, "rebased locally with")
}

// A rebase git refused to start is not a conflict: it must fail outright
// without writing recovery state that --continue would act on.
func TestRebase_RebaseStartError_IsFatalNotConflict(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(_, _, branch string, _ git.RebaseOpts) error {
		return &git.RebaseStartError{
			Err: errors.New("fatal: 'b2' is already used by worktree at '/elsewhere'"),
		}
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "could not start rebase of b2 onto b1")
	assert.Contains(t, output, "already used by worktree")
	assert.NotContains(t, output, "conflict")

	_, statErr := os.Stat(filepath.Join(tmpDir, rebaseStateFile))
	assert.True(t, os.IsNotExist(statErr),
		"no rebase state should be written for a rebase that never started")
}

// TestSync_UnstackedAfterCascade_DoesNotPush is the important half of the fix
// for sync: a stack that is not actually rebased must never be force-pushed
// over the remote.
func TestSync_UnstackedAfterCascade_DoesNotPush(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	mock.RevParseFn = staleTrunkRevParse
	// Trunk fast-forwards, but the stack never ends up on it.
	mock.IsAncestorFn, _ = ancestorMock([][2]string{
		{"trunk-remote", "trunk-local"},
		{"main", "b1"},
	}, nil)
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { return nil }
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
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Empty(t, pushCalls, "an unrebased stack must not be force-pushed")
	assert.Contains(t, output, "still not based on")
	assert.NotContains(t, output, "Branches synced")
}

// TestSync_TrunkLockedByWorktree_RebasesOntoRemoteRef is the sync counterpart of
// issue #155. Previously sync concluded nothing was stale (the branches did sit
// on the stale local trunk), skipped the rebase, and force-pushed anyway.
func TestSync_TrunkLockedByWorktree_RebasesOntoRemoteRef(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var rebaseBases []string
	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	mock.RevParseFn = staleTrunkRevParse
	mock.IsAncestorFn, _ = ancestorMock([][2]string{{"trunk-remote", "trunk-local"}}, nil)
	mock.UpdateBranchRefFn = func(string, string) error {
		return errors.New("cannot force update the branch 'main' used by worktree at '/elsewhere'")
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, base)
		return nil
	}
	mock.RebaseOntoFn = func(newBase, _, _ string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, newBase)
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
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.NoError(t, err)
	require.Equal(t, []string{"origin/main", "b1"}, rebaseBases,
		"sync must rebase onto the remote-tracking ref when the local trunk is locked")
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force)
	assert.Contains(t, output, "Stacked on origin/main")
}

// Issue #176: a remote-qualified trunk must not be re-qualified into
// "origin/origin/main", in either command.
func TestRebase_RemoteQualifiedTrunk_IsNormalized(t *testing.T) {
	s := twoBranchStack()
	s.Trunk.Branch = "origin/main"

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var createdBranches []string
	var rebaseBases []string

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool { return name != "origin/main" }
	mock.CreateBranchFn = func(name, base string) error {
		createdBranches = append(createdBranches, name+" from "+base)
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, base)
		return nil
	}
	mock.RebaseOntoFn = func(newBase, _, _ string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, newBase)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)

	assert.NoError(t, err)
	assert.Empty(t, createdBranches, "the existing local main should be reused")
	require.Equal(t, []string{"main", "b1"}, rebaseBases)
	assert.NotContains(t, string(out), "origin/origin/main")
}

// Issue #225 shape: the trunk branch was tracked on the remote and has since
// been merged and deleted, so there is nothing to bring the stack up to date
// with. That must be reported rather than silently rebasing onto a stale local
// trunk.
func TestRebase_TrunkMissingOnRemote_Fails(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var rebaseCalls int

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.UpstreamRemoteFn = func(string) (string, error) { return "origin", nil }
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "origin/main" {
			return "", errors.New("unknown revision")
		}
		return "sha-" + ref, nil
	}
	mock.RebaseFn = func(string, git.RebaseOpts) error { rebaseCalls++; return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { rebaseCalls++; return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Zero(t, rebaseCalls, "nothing should be rebased onto a stale trunk")
	assert.Contains(t, output, "no longer exists on origin")
	assert.Contains(t, output, "--no-trunk")
}

// A trunk that was never pushed is a local integration branch, not an orphaned
// stack: the cascade must still run against it.
func TestRebase_LocalOnlyTrunk_StillRebases(t *testing.T) {
	s := twoBranchStack()
	s.Trunk.Branch = "integration"

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseBases []string

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.UpstreamRemoteFn = func(string) (string, error) { return "", nil }
	mock.RevParseFn = func(ref string) (string, error) {
		if strings.HasPrefix(ref, "origin/") {
			return "", errors.New("unknown revision")
		}
		return "sha-" + ref, nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, base)
		return nil
	}
	mock.RebaseOntoFn = func(newBase, _, _ string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, newBase)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)

	assert.NoError(t, err)
	assert.Equal(t, []string{"integration", "b1"}, rebaseBases)
	assert.Contains(t, string(out), "only exists locally")
}

// --no-trunk still works without a reachable remote trunk.
func TestRebase_NoTrunk_SkipsTrunkResolution(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var rebaseBases []string

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "origin/main" {
			return "", errors.New("unknown revision")
		}
		return "sha-" + ref, nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseOntoFn = func(newBase, _, _ string, _ git.RebaseOpts) error {
		rebaseBases = append(rebaseBases, newBase)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--no-trunk"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1"}, rebaseBases, "only the inter-branch rebase runs")
	assert.Contains(t, string(out), "without trunk")
}

// --autostash stashes once around the whole cascade and restores afterwards.
// git's own --autostash cannot be used: it pops after every individual rebase,
// landing the changes on whichever branch that rebase left checked out.
func TestRebase_Autostash_StashesOnceAndRestores(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var stashPushes []string
	stashPops := 0
	var rebaseOpts []git.RebaseOpts

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.HasUncommittedTrackedChangesFn = func() (bool, error) { return true, nil }
	mock.StashPushFn = func(msg string) error {
		stashPushes = append(stashPushes, msg)
		return nil
	}
	mock.StashPopFn = func() error { stashPops++; return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(_ string, o git.RebaseOpts) error {
		rebaseOpts = append(rebaseOpts, o)
		return nil
	}
	mock.RebaseOntoFn = func(_, _, _ string, o git.RebaseOpts) error {
		rebaseOpts = append(rebaseOpts, o)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--autostash"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)

	assert.NoError(t, err)
	require.Len(t, stashPushes, 1, "the stack should be stashed exactly once")
	assert.Equal(t, stashMessage, stashPushes[0])
	assert.Equal(t, 1, stashPops, "the stash should be restored exactly once")
	assert.Len(t, rebaseOpts, 2)
	assert.Contains(t, string(out), "Restored stashed changes")
}

// On conflict the stash stays put: --continue and --abort restore it once the
// user is done, rather than popping it into a half-rebased working tree.
func TestRebase_Autostash_KeepsStashOnConflict(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	stashPops := 0

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.HasUncommittedTrackedChangesFn = func() (bool, error) { return true, nil }
	mock.StashPushFn = func(string) error { return nil }
	mock.StashPopFn = func() error { stashPops++; return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return errors.New("conflict") }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--autostash"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrConflict)
	assert.Zero(t, stashPops, "the stash must survive a conflict")

	state, loadErr := loadRebaseState(tmpDir)
	require.NoError(t, loadErr)
	assert.True(t, state.Stashed, "recovery state must record the pending stash")
	assert.Equal(t, "main", state.TrunkRef, "recovery must resume against the same trunk ref")
}

// A `gh stack rebase` before `gh stack sync` leaves the branches diverged from
// their remote refs. sync used to derive the force flag purely from whether it
// had rebased in that same run, so the atomic push failed and the stack stayed
// unpushed while sync still reported success.
func TestSync_ForcePushesBranchesRebasedEarlier(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	var pushCalls []pushCall

	mock := newSyncMock(tmpDir, "b1")
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "trunk-sha", nil
		}
		// The local branches carry rewritten history.
		if strings.HasPrefix(ref, "origin/") {
			return "old-" + strings.TrimPrefix(ref, "origin/"), nil
		}
		return "new-" + ref, nil
	}
	// The stack is already correctly stacked, so no rebase happens this run,
	// but the remote tips are not contained in the local branches.
	mock.IsAncestorFn = func(ancestor, descendant string) (bool, error) {
		if strings.HasPrefix(ancestor, "old-") {
			return false, nil
		}
		return true, nil
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
	out, _ := io.ReadAll(errR)

	assert.NoError(t, err)
	require.Len(t, pushCalls, 1)
	assert.True(t, pushCalls[0].force,
		"branches rebased before this run still need --force-with-lease")
	assert.Contains(t, string(out), "Pushed")
}

// When the push does not land, the stack on the remote does not reflect the
// local one, so sync must not sign off as if it did.
func TestSync_PushFailure_DoesNotReportSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, twoBranchStack())

	mock := newSyncMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error {
		return errors.New("remote rejected")
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.NoError(t, err, "a failed push is a warning, not a fatal error")
	assert.Contains(t, output, "were not pushed")
	assert.NotContains(t, output, "Stack synced")
	assert.NotContains(t, output, "Branches synced")
}

func TestBranchesNeedForcePush(t *testing.T) {
	tests := []struct {
		name     string
		ancestor func(string, string) (bool, error)
		revParse func(string) (string, error)
		want     bool
	}{
		{
			name:     "remote tip contained locally needs no force",
			ancestor: func(string, string) (bool, error) { return true, nil },
			want:     false,
		},
		{
			name:     "rewritten history needs force",
			ancestor: func(string, string) (bool, error) { return false, nil },
			want:     true,
		},
		{
			name:     "a branch with no remote ref is ignored",
			revParse: func(string) (string, error) { return "", errors.New("unknown revision") },
			ancestor: func(string, string) (bool, error) { return false, nil },
			want:     false,
		},
		{
			name:     "unknown ancestry does not force",
			ancestor: func(string, string) (bool, error) { return false, errors.New("boom") },
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revParse := tt.revParse
			if revParse == nil {
				revParse = func(ref string) (string, error) { return "sha-" + ref, nil }
			}
			restore := git.SetOps(&git.MockOps{
				RevParseFn:   revParse,
				IsAncestorFn: tt.ancestor,
			})
			defer restore()

			assert.Equal(t, tt.want, branchesNeedForcePush("origin", []string{"b1", "b2"}))
		})
	}
}
