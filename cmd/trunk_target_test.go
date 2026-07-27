package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeTrunkBranch(t *testing.T) {
	tests := []struct {
		name        string
		trunk       string
		remote      string
		localExists map[string]bool
		want        string
	}{
		{
			name:   "plain branch is unchanged",
			trunk:  "main",
			remote: "origin",
			want:   "main",
		},
		{
			name:   "remote-qualified trunk is stripped",
			trunk:  "origin/main",
			remote: "origin",
			want:   "main",
		},
		{
			name:   "non-default remote is stripped",
			trunk:  "upstream/trunk",
			remote: "upstream",
			want:   "trunk",
		},
		{
			name:   "prefix of a different remote is kept",
			trunk:  "upstream/main",
			remote: "origin",
			want:   "upstream/main",
		},
		{
			name:        "a real local branch named like the remote ref is kept",
			trunk:       "origin/main",
			remote:      "origin",
			localExists: map[string]bool{"origin/main": true},
			want:        "origin/main",
		},
		{
			name:   "bare remote name is kept",
			trunk:  "origin/",
			remote: "origin",
			want:   "origin/",
		},
		{
			name:   "no remote resolved",
			trunk:  "origin/main",
			remote: "",
			want:   "origin/main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := git.SetOps(&git.MockOps{
				BranchExistsFn: func(name string) bool { return tt.localExists[name] },
			})
			defer restore()

			assert.Equal(t, tt.want, normalizeTrunkBranch(tt.trunk, tt.remote))
		})
	}
}

// trunkTargetMock builds a git mock for resolveTrunkTarget with the given
// local/remote trunk SHAs.
func trunkTargetMock(localSHA, remoteSHA string) *git.MockOps {
	return &git.MockOps{
		BranchExistsFn: func(string) bool { return true },
		RevParseFn: func(ref string) (string, error) {
			switch ref {
			case "main":
				return localSHA, nil
			case "origin/main":
				if remoteSHA == "" {
					return "", errors.New("unknown revision")
				}
				return remoteSHA, nil
			}
			return "sha-" + ref, nil
		},
	}
}

func TestResolveTrunkTarget(t *testing.T) {
	t.Run("already up to date", func(t *testing.T) {
		mock := trunkTargetMock("same", "same")
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, "main", target.Ref)
		assert.False(t, target.Moved)
		assert.False(t, target.Detached)
	})

	t.Run("fast-forwards the local trunk", func(t *testing.T) {
		mock := trunkTargetMock("old", "new")
		mock.IsAncestorFn, _ = ancestorMock([][2]string{{"new", "old"}}, nil)
		var updated []string
		mock.UpdateBranchRefFn = func(branch, sha string) error {
			updated = append(updated, branch+"="+sha)
			return nil
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, []string{"main=new"}, updated)
		assert.Equal(t, "main", target.Ref)
		assert.Equal(t, "new", target.SHA)
		assert.True(t, target.Moved)
		assert.False(t, target.Detached)
	})

	t.Run("uses MergeFF when trunk is checked out", func(t *testing.T) {
		mock := trunkTargetMock("old", "new")
		mock.IsAncestorFn, _ = ancestorMock([][2]string{{"new", "old"}}, nil)
		var mergeFF []string
		mock.MergeFFFn = func(target string) error {
			mergeFF = append(mergeFF, target)
			return nil
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "main")
		require.NoError(t, err)
		assert.Equal(t, []string{"origin/main"}, mergeFF)
		assert.Equal(t, "main", target.Ref)
	})

	// The regression behind issue #155 and the Slack report: the local trunk
	// ref cannot be moved because another worktree has it checked out. The
	// stack must still be rebased onto the latest trunk.
	t.Run("falls back to the remote ref when the local trunk is locked", func(t *testing.T) {
		mock := trunkTargetMock("old", "new")
		mock.IsAncestorFn, _ = ancestorMock([][2]string{{"new", "old"}}, nil)
		mock.UpdateBranchRefFn = func(string, string) error {
			return errors.New("cannot force update the branch 'main' used by worktree at '/elsewhere'")
		}
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, errR := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, "origin/main", target.Ref, "cascade must target the remote ref")
		assert.Equal(t, "new", target.SHA)
		assert.True(t, target.Moved)
		assert.True(t, target.Detached)

		cfg.Err.Close()
		out, _ := io.ReadAll(errR)
		assert.Contains(t, string(out), "worktree")
		assert.Contains(t, string(out), "origin/main")
	})

	t.Run("falls back to the remote ref when the local trunk diverged", func(t *testing.T) {
		mock := trunkTargetMock("local", "remote")
		mock.IsAncestorFn, _ = ancestorMock([][2]string{
			{"local", "remote"},
			{"remote", "local"},
		}, nil)
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, errR := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, "origin/main", target.Ref)
		assert.True(t, target.Detached)

		cfg.Err.Close()
		out, _ := io.ReadAll(errR)
		assert.Contains(t, string(out), "diverged")
	})

	// Unpushed commits on the local trunk must not be dropped by rebasing the
	// stack onto the older remote ref.
	t.Run("keeps the local trunk when it is ahead of the remote", func(t *testing.T) {
		mock := trunkTargetMock("ahead", "behind")
		mock.IsAncestorFn, _ = ancestorMock([][2]string{{"ahead", "behind"}}, nil)
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, "main", target.Ref)
		assert.False(t, target.Detached)
	})

	// Issue #225 shape: the trunk branch was tracked on the remote and has since
	// been merged and deleted, orphaning the stack.
	t.Run("fails when a tracked trunk no longer exists on the remote", func(t *testing.T) {
		mock := trunkTargetMock("local", "")
		mock.UpstreamRemoteFn = func(string) (string, error) { return "origin", nil }
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, errR := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		_, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		assert.ErrorIs(t, err, ErrSilent)

		cfg.Err.Close()
		out, _ := io.ReadAll(errR)
		assert.Contains(t, string(out), "no longer exists on origin")
		assert.Contains(t, string(out), "--no-trunk")
	})

	// A stack based on a local integration branch that was never pushed: the
	// local branch is the source of truth, so the cascade must still run.
	t.Run("uses a local-only trunk that was never pushed", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			BranchExistsFn:   func(string) bool { return true },
			UpstreamRemoteFn: func(string) (string, error) { return "", nil },
			RevParseFn: func(ref string) (string, error) {
				if strings.HasPrefix(ref, "origin/") {
					return "", errors.New("unknown revision")
				}
				return "sha-" + ref, nil
			},
		})
		defer restore()

		cfg, _, errR := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "integration"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, "integration", target.Ref)
		assert.False(t, target.Detached)

		cfg.Err.Close()
		out, _ := io.ReadAll(errR)
		assert.Contains(t, string(out), "only exists locally")
	})

	t.Run("fails when the trunk exists neither locally nor remotely", func(t *testing.T) {
		mock := trunkTargetMock("local", "")
		mock.BranchExistsFn = func(string) bool { return false }
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, errR := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}

		_, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		assert.ErrorIs(t, err, ErrSilent)

		cfg.Err.Close()
		out, _ := io.ReadAll(errR)
		assert.Contains(t, string(out), "neither locally nor on origin")
	})

	// Issue #176: `gh stack init --base origin/main` records a remote-qualified
	// trunk, which used to be re-qualified into "origin/origin/main".
	t.Run("normalizes a remote-qualified trunk", func(t *testing.T) {
		mock := trunkTargetMock("same", "same")
		mock.BranchExistsFn = func(name string) bool { return name == "main" }
		restore := git.SetOps(mock)
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		s := &stack.Stack{Trunk: stack.BranchRef{Branch: "origin/main"}}

		target, err := resolveTrunkTarget(cfg, s, "origin", "b1")
		require.NoError(t, err)
		assert.Equal(t, "main", target.Ref)
		assert.Equal(t, "main", s.Trunk.Branch, "the stack's trunk name should be repaired")
	})
}

func TestVerifyStacked(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	t.Run("reports branches missing their parent", func(t *testing.T) {
		isAncestor, _ := ancestorMock([][2]string{{"b1", "b2"}}, nil)
		restore := git.SetOps(&git.MockOps{IsAncestorFn: isAncestor})
		defer restore()

		assert.Equal(t, []string{"b2"}, verifyStacked(s, "main", 0, len(s.Branches)))
	})

	t.Run("measures the bottom branch against the given trunk ref", func(t *testing.T) {
		// Stacked on the local trunk but not on the remote-tracking ref: the
		// exact state a stale-trunk rebase used to leave behind.
		isAncestor, _ := ancestorMock([][2]string{{"origin/main", "b1"}}, nil)
		restore := git.SetOps(&git.MockOps{IsAncestorFn: isAncestor})
		defer restore()

		assert.Empty(t, verifyStacked(s, "main", 0, len(s.Branches)))
		assert.Equal(t, []string{"b1"}, verifyStacked(s, "origin/main", 0, len(s.Branches)))
	})

	t.Run("honours the range", func(t *testing.T) {
		isAncestor, _ := ancestorMock([][2]string{{"main", "b1"}}, nil)
		restore := git.SetOps(&git.MockOps{IsAncestorFn: isAncestor})
		defer restore()

		assert.Equal(t, []string{"b1"}, verifyStacked(s, "main", 0, 3))
		assert.Empty(t, verifyStacked(s, "main", 1, 3), "b1 is outside the range")
	})

	t.Run("skips merged branches and uses the nearest active parent", func(t *testing.T) {
		merged := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
				{Branch: "b2"},
			},
		}
		isAncestor, _ := ancestorMock([][2]string{{"b1", "b2"}}, nil)
		restore := git.SetOps(&git.MockOps{IsAncestorFn: isAncestor})
		defer restore()

		assert.Empty(t, verifyStacked(merged, "main", 0, 2),
			"b2 should be measured against main, not the merged b1")
	})

	t.Run("empty trunk ref falls back to the stack trunk", func(t *testing.T) {
		isAncestor, _ := ancestorMock([][2]string{{"main", "b1"}}, nil)
		restore := git.SetOps(&git.MockOps{IsAncestorFn: isAncestor})
		defer restore()

		assert.Equal(t, []string{"b1"}, verifyStacked(s, "", 0, len(s.Branches)))
	})
}

func TestPreflightRebase(t *testing.T) {
	tests := []struct {
		name            string
		rebaseInProgres bool
		trackedDirty    bool
		statusErr       error
		autostash       bool
		wantErr         error
	}{
		{
			name: "clean tree passes",
		},
		{
			name:            "rebase in progress fails",
			rebaseInProgres: true,
			wantErr:         ErrRebaseActive,
		},
		{
			name:         "dirty tracked files fail",
			trackedDirty: true,
			wantErr:      ErrSilent,
		},
		{
			name:         "autostash lifts the clean-tree requirement",
			trackedDirty: true,
			autostash:    true,
		},
		{
			name:            "autostash does not lift the rebase-in-progress check",
			rebaseInProgres: true,
			autostash:       true,
			wantErr:         ErrRebaseActive,
		},
		{
			name:      "an unreadable status is not treated as clean",
			statusErr: errors.New("boom"),
			wantErr:   ErrSilent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := git.SetOps(&git.MockOps{
				IsRebaseInProgressFn: func() bool { return tt.rebaseInProgres },
				HasUncommittedTrackedChangesFn: func() (bool, error) {
					return tt.trackedDirty, tt.statusErr
				},
			})
			defer restore()

			cfg, _, _ := config.NewTestConfig()
			err := preflightRebase(cfg, "rebase", tt.autostash)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// The preflight must only consider tracked files: git rebase runs happily with
// untracked files present, so blocking on them would refuse rebases git accepts.
func TestPreflightRebase_IgnoresUntrackedFiles(t *testing.T) {
	restore := git.SetOps(&git.MockOps{
		IsRebaseInProgressFn: func() bool { return false },
		// A repo with only untracked files: "dirty" overall, clean for rebase.
		HasUncommittedChangesFn:        func() (bool, error) { return true, nil },
		HasUncommittedTrackedChangesFn: func() (bool, error) { return false, nil },
	})
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	assert.NoError(t, preflightRebase(cfg, "rebase", false))
}

func TestStashForRebase(t *testing.T) {
	t.Run("stashes tracked changes", func(t *testing.T) {
		var pushed []string
		restore := git.SetOps(&git.MockOps{
			HasUncommittedTrackedChangesFn: func() (bool, error) { return true, nil },
			StashPushFn: func(msg string) error {
				pushed = append(pushed, msg)
				return nil
			},
		})
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		stashed, err := stashForRebase(cfg)
		require.NoError(t, err)
		assert.True(t, stashed)
		assert.Equal(t, []string{stashMessage}, pushed)
	})

	t.Run("does nothing on a clean tree", func(t *testing.T) {
		called := false
		restore := git.SetOps(&git.MockOps{
			HasUncommittedTrackedChangesFn: func() (bool, error) { return false, nil },
			StashPushFn:                    func(string) error { called = true; return nil },
		})
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		stashed, err := stashForRebase(cfg)
		require.NoError(t, err)
		assert.False(t, stashed)
		assert.False(t, called, "no stash should be created")
	})

	t.Run("surfaces a failed stash", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			HasUncommittedTrackedChangesFn: func() (bool, error) { return true, nil },
			StashPushFn:                    func(string) error { return errors.New("disk full") },
		})
		defer restore()

		cfg, _, _ := config.NewTestConfig()
		_, err := stashForRebase(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not stash local changes")
	})
}
