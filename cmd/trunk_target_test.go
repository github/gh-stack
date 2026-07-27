package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trunkTargetMock(localSHA, remoteSHA string) *git.MockOps {
	return &git.MockOps{
		BranchExistsFn: func(string) bool { return true },
		RevParseFn: func(ref string) (string, error) {
			switch ref {
			case "main":
				return localSHA, nil
			case "origin/main":
				return remoteSHA, nil
			default:
				return "sha-" + ref, nil
			}
		},
	}
}

func TestResolveTrunkTarget(t *testing.T) {
	t.Run("falls back to fetched remote ref when local trunk cannot move", func(t *testing.T) {
		mock := trunkTargetMock("local", "remote")
		mock.IsAncestorFn = func(a, d string) (bool, error) {
			return a == "local" && d == "remote", nil
		}
		mock.UpdateBranchRefFn = func(string, string) error {
			return errors.New("branch is checked out in another worktree")
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		target, err := resolveTrunkTarget(cfg, &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
		}, "origin", "b1")

		require.NoError(t, err)
		assert.Equal(t, "origin/main", target.Ref)
		assert.Equal(t, "remote", target.SHA)
	})

	t.Run("keeps local trunk when it contains fetched remote tip", func(t *testing.T) {
		mock := trunkTargetMock("local-ahead", "remote")
		mock.IsAncestorFn = func(a, d string) (bool, error) {
			return a == "remote" && d == "local-ahead", nil
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		target, err := resolveTrunkTarget(cfg, &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
		}, "origin", "b1")

		require.NoError(t, err)
		assert.Equal(t, "main", target.Ref)
		assert.Equal(t, "local-ahead", target.SHA)
	})

	t.Run("uses intentional local-only trunk", func(t *testing.T) {
		mock := trunkTargetMock("local", "")
		mock.FetchBranchFn = func(string, string) error {
			return git.ErrRemoteBranchNotFound
		}
		mock.UpstreamRemoteFn = func(string) (string, error) { return "", errors.New("unset") }
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		target, err := resolveTrunkTarget(cfg, &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
		}, "origin", "b1")

		require.NoError(t, err)
		assert.Equal(t, "main", target.Ref)
		assert.Equal(t, "local", target.SHA)
	})

	t.Run("fails when tracked trunk was deleted", func(t *testing.T) {
		mock := trunkTargetMock("local", "")
		mock.FetchBranchFn = func(string, string) error {
			return git.ErrRemoteBranchNotFound
		}
		mock.UpstreamRemoteFn = func(string) (string, error) { return "origin", nil }
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		_, err := resolveTrunkTarget(cfg, &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
		}, "origin", "b1")

		assert.ErrorIs(t, err, ErrSilent)
	})

	t.Run("fails closed on transport error", func(t *testing.T) {
		mock := trunkTargetMock("local", "cached")
		mock.FetchBranchFn = func(string, string) error {
			return errors.New("network unavailable")
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		_, err := resolveTrunkTarget(cfg, &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
		}, "origin", "b1")

		assert.ErrorIs(t, err, ErrSilent)
	})
}

func TestVerifyStackedUsesResolvedTrunk(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	}
	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			return a == "main" && d == "b1", nil
		},
	})
	defer restore()

	assert.Empty(t, verifyStacked(s, "main", 0, 1))
	assert.Equal(t, []string{"b1"}, verifyStacked(s, "origin/main", 0, 1))
}

func TestVerifyStackedKeepsQueuedBranchAsParent(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1}},
			{Branch: "b2"},
		},
	}
	s.Branches[0].Queued = true

	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			return a == "b1" && d == "b2", nil
		},
	})
	defer restore()

	assert.Empty(t, verifyStacked(s, "new-main", 0, 2),
		"downstream branches remain stacked on queued branches while trunk moves")
}

func TestRebase_FetchFailureStopsBeforeCascade(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	rebaseCalls := 0
	mock := newRebaseMock(tmpDir, "b1")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.FetchBranchFn = func(string, string) error { return errors.New("network unavailable") }
	mock.RebaseFn = func(string, git.RebaseOpts) error { rebaseCalls++; return nil }
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	output, _ := io.ReadAll(errR)
	assert.ErrorIs(t, err, ErrSilent)
	assert.Zero(t, rebaseCalls)
	assert.Contains(t, string(output), "failed to fetch trunk branch")
	assert.NotContains(t, string(output), "rebased locally")
}

func TestRebase_StartErrorDoesNotWriteRecoveryState(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	mock := newRebaseMock(tmpDir, "b1")
	mock.BranchExistsFn = func(string) bool { return true }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error {
		return &git.RebaseStartError{Err: errors.New("branch is checked out elsewhere")}
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrSilent)
	_, statErr := os.Stat(filepath.Join(tmpDir, rebaseStateFile))
	assert.True(t, os.IsNotExist(statErr))
}

func TestSync_UnstackedCascadeDoesNotPush(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	})

	pushes := 0
	mock := newSyncMock(tmpDir, "b1")
	mock.RevParseFn = func(ref string) (string, error) {
		switch ref {
		case "main":
			return "local", nil
		case "origin/main":
			return "remote", nil
		default:
			return "sha-" + ref, nil
		}
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "local" && d == "remote" {
			return true, nil
		}
		if a == "main" && d == "b1" {
			return false, nil
		}
		return true, nil
	}
	mock.UpdateBranchRefFn = func(string, string) error { return nil }
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error { return nil }
	mock.PushFn = func(string, []string, bool, bool) error { pushes++; return nil }
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrSilent)
	assert.Zero(t, pushes)
}
