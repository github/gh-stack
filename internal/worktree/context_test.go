package worktree

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/github/gh-stack/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestorePreservesLaterCommits(t *testing.T) {
	common := t.TempDir()
	root := filepath.Join(common, "root")
	resets := 0
	mock := &git.MockOps{
		CommonDirFn:             func() (string, error) { return common, nil },
		GitDirFn:                func() (string, error) { return common, nil },
		CurrentBranchFn:         func() (string, error) { return "branch", nil },
		RevParseFn:              func(string) (string, error) { return "user-commit", nil },
		HasUncommittedChangesFn: func() (bool, error) { return false, nil },
		ResetHardFn:             func(string) error { resets++; return nil },
		UpdateBranchRefFn:       func(string, string) error { resets++; return nil },
	}
	restore := git.SetOps(mock)
	defer restore()
	ctx := &Context{Origin: Location{Path: root, ID: "."}, Touched: map[string]string{"branch": "our-rebase"}}

	err := ctx.Restore(map[string]string{"branch": "original"})

	require.ErrorContains(t, err, "changed after this operation")
	assert.Zero(t, resets)
	assert.Equal(t, "our-rebase", ctx.Touched["branch"])
}

func TestRestoreOnlyResetsTouchedBranches(t *testing.T) {
	common := t.TempDir()
	var resets []string
	mock := &git.MockOps{
		CommonDirFn:     func() (string, error) { return common, nil },
		GitDirFn:        func() (string, error) { return common, nil },
		CurrentBranchFn: func() (string, error) { return "ours", nil },
		RevParseFn: func(branch string) (string, error) {
			require.Equal(t, "ours", branch)
			return "rebased", nil
		},
		ResetHardFn: func(ref string) error { resets = append(resets, ref); return nil },
	}
	restore := git.SetOps(mock)
	defer restore()
	ctx := &Context{Origin: Location{Path: common, ID: "."}, Touched: map[string]string{"ours": "rebased"}}

	require.NoError(t, ctx.Restore(map[string]string{"ours": "before", "unrelated": "old-unrelated"}))

	assert.Equal(t, []string{"before"}, resets)
	assert.Empty(t, ctx.Touched)
}

func TestDirtyRestoreRetainsRoundTrippableProgress(t *testing.T) {
	common := t.TempDir()
	mock := &git.MockOps{
		CommonDirFn:             func() (string, error) { return common, nil },
		GitDirFn:                func() (string, error) { return common, nil },
		CurrentBranchFn:         func() (string, error) { return "branch", nil },
		RevParseFn:              func(string) (string, error) { return "rebased", nil },
		HasUncommittedChangesFn: func() (bool, error) { return true, nil },
		ResetHardFn:             func(string) error { t.Fatal("must not reset dirty files"); return nil },
	}
	restore := git.SetOps(mock)
	defer restore()
	ctx := &Context{Origin: Location{Path: common, ID: "."}, Touched: map[string]string{"branch": "rebased"}}

	require.ErrorContains(t, ctx.Restore(map[string]string{"branch": "before"}), "uncommitted")
	data, err := json.Marshal(ctx)
	require.NoError(t, err)
	var resumed Context
	require.NoError(t, json.Unmarshal(data, &resumed))
	mock.HasUncommittedChangesFn = func() (bool, error) { return false, nil }
	var reset string
	mock.ResetHardFn = func(ref string) error { reset = ref; return nil }
	require.NoError(t, resumed.Restore(map[string]string{"branch": "before"}))
	assert.Equal(t, "before", reset)
}

func TestMovedWorktreeUsesAdministrationIdentity(t *testing.T) {
	for _, failure := range []string{"factory", "identity"} {
		t.Run(failure, func(t *testing.T) {
			common := t.TempDir()
			oldPath, newPath := filepath.Join(common, "old"), filepath.Join(common, "new")
			unavailable := filepath.Join(common, "unavailable")
			owner := &git.MockOps{
				CommonDirFn: func() (string, error) { return common, nil },
				GitDirFn:    func() (string, error) { return filepath.Join(common, "worktrees", "branch"), nil },
			}
			root := &git.MockOps{
				CommonDirFn: func() (string, error) { return common, nil },
				WorktreesFn: func() ([]git.Worktree, error) {
					return []git.Worktree{{Path: unavailable}, {Path: newPath, Branch: "branch"}}, nil
				},
				ForWorktreeFn: func(path string) (git.Ops, error) {
					if path == newPath {
						return owner, nil
					}
					if failure == "factory" || path == unavailable {
						return nil, errors.New("worktree moved")
					}
					return &git.MockOps{CommonDirFn: func() (string, error) {
						return "", errors.New("worktree identity unavailable")
					}}, nil
				},
			}
			restore := git.SetOps(root)
			defer restore()
			ctx := &Context{Owners: map[string]*Location{"branch": {Path: oldPath, ID: filepath.Join("worktrees", "branch")}}}

			ops, err := ctx.Ops("branch")

			require.NoError(t, err)
			assert.Same(t, owner, ops)
			assert.Equal(t, newPath, ctx.Location("branch").Path)
		})
	}
}

func TestFactoryFailureDoesNotUseInvokingWorktree(t *testing.T) {
	common, origin := t.TempDir(), t.TempDir()
	scopeErr := errors.New("recorded worktree unavailable")
	restore := git.SetOps(&git.MockOps{
		CommonDirFn: func() (string, error) { return common, nil },
		ForWorktreeFn: func(string) (git.Ops, error) {
			return nil, scopeErr
		},
		CheckoutBranchFn: func(string) error {
			t.Fatal("failed owner lookup must not fall back to the invoking worktree")
			return nil
		},
	})
	defer restore()
	ctx := &Context{Origin: Location{Path: origin, ID: "."}}

	ops, err := ctx.Prepare("branch")

	require.ErrorIs(t, err, scopeErr)
	assert.Nil(t, ops)
	assert.Equal(t, origin, ctx.Origin.Path)
}

func TestPreflightStateFailureDoesNotCheckout(t *testing.T) {
	for _, query := range []string{"rebase", "cherry-pick"} {
		t.Run(query, func(t *testing.T) {
			common, origin := t.TempDir(), t.TempDir()
			lookupErr := errors.New("state lookup failed")
			mock := &git.MockOps{
				CommonDirFn: func() (string, error) { return common, nil },
				GitDirFn:    func() (string, error) { return common, nil },
				CheckoutBranchFn: func(string) error {
					t.Fatal("state lookup failure must precede checkout")
					return nil
				},
			}
			fail := func() (bool, error) { return false, lookupErr }
			if query == "rebase" {
				mock.IsRebaseInProgressFn = fail
			} else {
				mock.IsCherryPickInProgressFn = fail
			}
			restore := git.SetOps(mock)
			defer restore()
			ctx := &Context{Origin: Location{Path: origin, ID: "."}}

			ops, err := ctx.Prepare("branch")

			require.ErrorIs(t, err, lookupErr)
			assert.Nil(t, ops)
		})
	}
}

func TestPreflightDoesNotInspectUnrelatedWorktrees(t *testing.T) {
	common := t.TempDir()
	rootPath := filepath.Join(common, "root")
	affectedPath := filepath.Join(common, "affected")
	unrelatedPath := filepath.Join(common, "unrelated")
	inspectedUnrelated := 0
	mock := &git.MockOps{
		CommonDirFn: func() (string, error) { return common, nil },
		RootDirFn:   func() (string, error) { return rootPath, nil },
		WorktreesFn: func() ([]git.Worktree, error) {
			return []git.Worktree{{Path: affectedPath, Branch: "affected"}, {Path: unrelatedPath, Branch: "unrelated"}}, nil
		},
	}
	mock.ForWorktreeFn = func(path string) (git.Ops, error) {
		branch := filepath.Base(path)
		dir := common
		if path != rootPath {
			dir = filepath.Join(common, "worktrees", branch)
		}
		return &git.MockOps{
			CommonDirFn:     func() (string, error) { return common, nil },
			GitDirFn:        func() (string, error) { return dir, nil },
			CurrentBranchFn: func() (string, error) { return branch, nil },
			HasUncommittedChangesFn: func() (bool, error) {
				if path == unrelatedPath {
					inspectedUnrelated++
					return true, nil
				}
				return false, nil
			},
		}, nil
	}
	restore := git.SetOps(mock)
	defer restore()
	ctx, err := New()
	require.NoError(t, err)

	require.NoError(t, ctx.Preflight([]string{"affected"}))
	assert.Zero(t, inspectedUnrelated)
	require.ErrorContains(t, ctx.Preflight([]string{"unrelated"}), "uncommitted")
}

func TestInvalidRecoveryLocationDoesNotExecuteGit(t *testing.T) {
	mock := &git.MockOps{ForWorktreeFn: func(string) (git.Ops, error) {
		t.Fatal("must not use the calling worktree for an invalid record")
		return nil, nil
	}}
	restore := git.SetOps(mock)
	defer restore()
	for _, location := range []Location{{}, {Path: "relative"}, {Path: t.TempDir(), ID: "../other-repo"}} {
		ctx := &Context{Origin: location}
		_, err := ctx.OriginOps()
		require.Error(t, err)
	}
}

func TestStartRejectsCommitsAfterSnapshot(t *testing.T) {
	common := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		CommonDirFn: func() (string, error) { return common, nil },
		GitDirFn:    func() (string, error) { return common, nil },
		RevParseFn:  func(string) (string, error) { return "new-user-commit", nil },
	})
	defer restore()
	ctx := &Context{Origin: Location{Path: common, ID: "."}}

	require.ErrorContains(t, ctx.Start("branch", "snapshot-commit"), "changed since")
	assert.Empty(t, ctx.Pending)
	assert.Empty(t, ctx.Touched)
}
