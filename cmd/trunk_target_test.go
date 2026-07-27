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

func TestNormalizeTrunkBranch(t *testing.T) {
	t.Run("strips the selected remote prefix", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			BranchExistsFn: func(string) bool { return false },
		})
		defer restore()

		assert.Equal(t, "main", normalizeTrunkBranch("origin/main", "origin"))
	})

	t.Run("preserves a real local branch with the remote prefix", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			BranchExistsFn: func(name string) bool { return name == "origin/main" },
		})
		defer restore()

		assert.Equal(t, "origin/main", normalizeTrunkBranch("origin/main", "origin"))
	})
}

func TestResolveTrunkTarget(t *testing.T) {
	t.Run("normalizes a remote-qualified trunk before fetching", func(t *testing.T) {
		mock := trunkTargetMock("same", "same")
		mock.BranchExistsFn = func(name string) bool { return name == "main" }
		var fetchedBranch string
		mock.FetchBranchFn = func(remote, branch string) error {
			assert.Equal(t, "origin", remote)
			fetchedBranch = branch
			return nil
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "origin/main"}}
		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")

		require.NoError(t, err)
		assert.Equal(t, "main", fetchedBranch)
		assert.Equal(t, "main", s.Trunk.Branch)
		assert.Equal(t, "main", target.Ref)
	})

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

func TestRebase_LaterStartErrorRestoresEarlierBranches(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	})

	branchSHAs := map[string]string{"b1": "old-b1", "b2": "old-b2"}
	currentBranch := "b1"
	var resets []resetCall

	mock := newRebaseMock(tmpDir, currentBranch)
	mock.BranchExistsFn = func(string) bool { return true }
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "trunk", nil
		}
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		if len(ref) > len("origin/") && ref[:len("origin/")] == "origin/" {
			return branchSHAs[ref[len("origin/"):]], nil
		}
		return "sha-" + ref, nil
	}
	mock.CheckoutBranchFn = func(branch string) error {
		currentBranch = branch
		return nil
	}
	mock.RebaseFn = func(string, git.RebaseOpts) error {
		branchSHAs["b1"] = "rebased-b1"
		return nil
	}
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
		return &git.RebaseStartError{Err: errors.New("branch is checked out elsewhere")}
	}
	mock.ResetHardFn = func(ref string) error {
		resets = append(resets, resetCall{currentBranch, ref})
		branchSHAs[currentBranch] = ref
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrSilent)
	assert.Equal(t, "old-b1", branchSHAs["b1"])
	assert.Equal(t, "old-b2", branchSHAs["b2"])
	assert.Equal(t, []resetCall{{branch: "b1", sha: "old-b1"}}, resets)
}

func TestSync_LaterStartErrorRestoresEarlierBranches(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	})

	branchSHAs := map[string]string{"b1": "old-b1", "b2": "old-b2"}
	currentBranch := "b1"
	pushes := 0

	mock := newSyncMock(tmpDir, currentBranch)
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "main" || ref == "origin/main" {
			return "trunk", nil
		}
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		if len(ref) > len("origin/") && ref[:len("origin/")] == "origin/" {
			return branchSHAs[ref[len("origin/"):]], nil
		}
		return "sha-" + ref, nil
	}
	stacked := false
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "main" && d == "b1" {
			return stacked, nil
		}
		return true, nil
	}
	mock.CheckoutBranchFn = func(branch string) error {
		currentBranch = branch
		return nil
	}
	mock.RebaseFn = func(string, git.RebaseOpts) error {
		branchSHAs["b1"] = "rebased-b1"
		stacked = true
		return nil
	}
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
		return &git.RebaseStartError{Err: errors.New("branch is checked out elsewhere")}
	}
	mock.ResetHardFn = func(ref string) error {
		branchSHAs[currentBranch] = ref
		return nil
	}
	mock.PushFn = func(string, []string, bool, bool) error {
		pushes++
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := SyncCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrSilent)
	assert.Equal(t, "old-b1", branchSHAs["b1"])
	assert.Equal(t, "old-b2", branchSHAs["b2"])
	assert.Zero(t, pushes)
}

func TestRebase_ContinueVerificationFailureRestoresAndClearsState(t *testing.T) {
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	})

	state := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{"b3"},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"b1": "old-b1",
			"b2": "old-b2",
			"b3": "old-b3",
		},
		TrunkRef:   "main",
		TrunkSHA:   "trunk",
		StartIndex: 0,
		EndIndex:   3,
	}
	require.NoError(t, saveRebaseState(tmpDir, state))

	branchSHAs := map[string]string{"b1": "old-b1", "b2": "old-b2", "b3": "old-b3"}
	currentBranch := "b2"
	rebaseInProgress := true
	cascadeDone := false

	mock := newRebaseMock(tmpDir, currentBranch)
	mock.BranchExistsFn = func(string) bool { return true }
	mock.RevParseFn = func(ref string) (string, error) {
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "sha-" + ref, nil
	}
	mock.IsRebaseInProgressFn = func() bool { return rebaseInProgress }
	mock.RebaseContinueFn = func(git.RebaseOpts) error {
		rebaseInProgress = false
		branchSHAs["b2"] = "rebased-b2"
		return nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if a == "b2" && d == "b3" {
			return !cascadeDone, nil
		}
		return true, nil
	}
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
		cascadeDone = true
		branchSHAs["b3"] = "rebased-b3"
		return nil
	}
	mock.CheckoutBranchFn = func(branch string) error {
		currentBranch = branch
		return nil
	}
	mock.ResetHardFn = func(ref string) error {
		branchSHAs[currentBranch] = ref
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--continue"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrSilent)
	assert.Equal(t, "old-b2", branchSHAs["b2"])
	assert.Equal(t, "old-b3", branchSHAs["b3"])
	_, statErr := os.Stat(filepath.Join(tmpDir, rebaseStateFile))
	assert.True(t, os.IsNotExist(statErr), "terminal verification failure must clear stale continuation state")
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
