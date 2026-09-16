package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/github/gh-stack/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 1. State file management tests
// ---------------------------------------------------------------------------

func TestModifyStateLifecycle(t *testing.T) {
	gitDir := t.TempDir()

	// Initially no state file exists
	assert.False(t, modify.StateExists(gitDir), "no state file should exist initially")

	loaded, err := modify.LoadState(gitDir)
	assert.NoError(t, err)
	assert.Nil(t, loaded, "loadModifyState should return nil when file does not exist")

	// Save a state file
	state := &modify.StateFile{
		SchemaVersion: 1,
		StackName:     "main",
		StartedAt:     time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		Phase:         "applying",
		Snapshot: modify.Snapshot{
			Branches: []modify.BranchSnapshot{
				{Name: "b1", TipSHA: "aaa111", Position: 0},
				{Name: "b2", TipSHA: "bbb222", Position: 1},
			},
			StackMetadata: json.RawMessage(`{"trunk":{"branch":"main"}}`),
		},
		Plan: []modify.Action{
			{Type: "drop", Branch: "b1"},
			{Type: "rename", Branch: "b2", NewName: "b2-new"},
		},
	}

	err = modify.SaveState(gitDir, state)
	require.NoError(t, err)
	assert.True(t, modify.StateExists(gitDir), "state file should exist after save")

	// Load it back and verify round-trip
	loaded, err = modify.LoadState(gitDir)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, 1, loaded.SchemaVersion)
	assert.Equal(t, "main", loaded.StackName)
	assert.Equal(t, "applying", loaded.Phase)
	assert.Equal(t, state.StartedAt, loaded.StartedAt)
	require.Len(t, loaded.Snapshot.Branches, 2)
	assert.Equal(t, "b1", loaded.Snapshot.Branches[0].Name)
	assert.Equal(t, "aaa111", loaded.Snapshot.Branches[0].TipSHA)
	assert.Equal(t, 0, loaded.Snapshot.Branches[0].Position)
	assert.Equal(t, "b2", loaded.Snapshot.Branches[1].Name)
	assert.Equal(t, "bbb222", loaded.Snapshot.Branches[1].TipSHA)
	assert.Equal(t, 1, loaded.Snapshot.Branches[1].Position)
	require.Len(t, loaded.Plan, 2)
	assert.Equal(t, "drop", loaded.Plan[0].Type)
	assert.Equal(t, "b1", loaded.Plan[0].Branch)
	assert.Equal(t, "rename", loaded.Plan[1].Type)
	assert.Equal(t, "b2-new", loaded.Plan[1].NewName)

	// Clear the state
	modify.ClearState(gitDir)
	assert.False(t, modify.StateExists(gitDir), "state file should be removed after clear")

	loaded, err = modify.LoadState(gitDir)
	assert.NoError(t, err)
	assert.Nil(t, loaded, "loadModifyState should return nil after clear")
}

func TestModifyStateAtomicWrite(t *testing.T) {
	gitDir := t.TempDir()

	state := &modify.StateFile{
		SchemaVersion: 1,
		StackName:     "main",
		Phase:         "applying",
		StartedAt:     time.Now().UTC(),
	}

	err := modify.SaveState(gitDir, state)
	require.NoError(t, err)

	// The final file should exist
	_, err = os.Stat(modify.StatePath(gitDir))
	assert.NoError(t, err, "state file should exist after atomic write")

	// No .tmp file should be left behind
	_, err = os.Stat(modify.StatePath(gitDir) + ".tmp")
	assert.True(t, os.IsNotExist(err), "no .tmp file should remain after successful write")
}

func TestModifyStateReadError(t *testing.T) {
	dir := t.TempDir()
	path := modify.StatePath(dir)
	require.NoError(t, os.Mkdir(path, 0700))
	got, err := modify.LoadState(dir)
	require.ErrorContains(t, err, "reading modify state")
	assert.Nil(t, got)
	var pathErr *os.PathError
	require.ErrorAs(t, err, &pathErr)
	assert.Equal(t, path, pathErr.Path)
}

func TestModifyStateConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	state := &modify.StateFile{
		SchemaVersion: 1, Phase: modify.PhaseConflict,
		OriginalBranch: "initial", ConflictBranch: "initial",
		Snapshot: modify.Snapshot{StackMetadata: json.RawMessage("{}")},
	}
	require.NoError(t, modify.SaveState(dir, state))
	stop := make(chan struct{})
	errs := make(chan error, 2)
	var reads atomic.Int64
	var readers sync.WaitGroup
	for range 2 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := modify.LoadState(dir)
				if err != nil {
					errs <- err
					return
				}
				if got == nil || got.OriginalBranch == "" || got.OriginalBranch != got.ConflictBranch {
					errs <- fmt.Errorf("reader observed an incomplete modify state")
					return
				}
				reads.Add(1)
			}
		})
	}
	var writeErr error
	for i := range 50 {
		branch := strings.Repeat(fmt.Sprintf("%04d", i), 8192)
		state.OriginalBranch, state.ConflictBranch = branch, branch
		if writeErr = modify.SaveState(dir, state); writeErr != nil {
			break
		}
	}
	close(stop)
	readers.Wait()
	close(errs)
	require.NoError(t, writeErr)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Positive(t, reads.Load())
	got, err := modify.LoadState(dir)
	require.NoError(t, err)
	assert.Equal(t, state, got)
}

func TestCheckModifyStateGuard(t *testing.T) {
	t.Run("no state file", func(t *testing.T) {
		gitDir := t.TempDir()
		err := modify.CheckStateGuard(gitDir)
		assert.NoError(t, err, "guard should pass when no state file exists")
	})

	t.Run("phase applying returns error", func(t *testing.T) {
		gitDir := t.TempDir()
		state := &modify.StateFile{
			SchemaVersion: 1,
			Phase:         "applying",
			StartedAt:     time.Now().UTC(),
		}
		require.NoError(t, modify.SaveState(gitDir, state))

		err := modify.CheckStateGuard(gitDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "modify session was interrupted")
	})

	t.Run("phase pending_submit passes", func(t *testing.T) {
		gitDir := t.TempDir()
		state := &modify.StateFile{
			SchemaVersion: 1,
			Phase:         "pending_submit",
			StartedAt:     time.Now().UTC(),
		}
		require.NoError(t, modify.SaveState(gitDir, state))

		err := modify.CheckStateGuard(gitDir)
		assert.NoError(t, err, "guard should pass when phase is pending_submit")
	})
}

// ---------------------------------------------------------------------------
// 2. Precondition check tests
// ---------------------------------------------------------------------------

func TestCheckStackLinearity(t *testing.T) {
	t.Run("linear stack passes", func(t *testing.T) {
		mock := &git.MockOps{
			IsAncestorFn: func(a, d string) (bool, error) { return true, nil },
			LogMergesFn:  func(base, head string) ([]git.CommitInfo, error) { return nil, nil },
		}
		restore := git.SetOps(mock)
		defer restore()

		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1"},
				{Branch: "b2"},
				{Branch: "b3"},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckStackLinearity(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.NoError(t, err)
	})

	t.Run("diverged branch fails", func(t *testing.T) {
		mock := &git.MockOps{
			IsAncestorFn: func(a, d string) (bool, error) {
				// b1 is not an ancestor of b2
				if a == "b1" && d == "b2" {
					return false, nil
				}
				return true, nil
			},
			LogMergesFn: func(base, head string) ([]git.CommitInfo, error) { return nil, nil },
		}
		restore := git.SetOps(mock)
		defer restore()

		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1"},
				{Branch: "b2"},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckStackLinearity(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.Error(t, err)
	})

	t.Run("merge commit fails", func(t *testing.T) {
		mock := &git.MockOps{
			IsAncestorFn: func(a, d string) (bool, error) { return true, nil },
			LogMergesFn: func(base, head string) ([]git.CommitInfo, error) {
				if head == "b2" {
					return []git.CommitInfo{{SHA: "merge-sha"}}, nil
				}
				return nil, nil
			},
		}
		restore := git.SetOps(mock)
		defer restore()

		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1"},
				{Branch: "b2"},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckStackLinearity(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.Error(t, err)
	})

	t.Run("skips merged branches", func(t *testing.T) {
		var isAncestorCalls []string
		mock := &git.MockOps{
			IsAncestorFn: func(a, d string) (bool, error) {
				isAncestorCalls = append(isAncestorCalls, d)
				return true, nil
			},
			LogMergesFn: func(base, head string) ([]git.CommitInfo, error) { return nil, nil },
		}
		restore := git.SetOps(mock)
		defer restore()

		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
				{Branch: "b2"},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckStackLinearity(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.NoError(t, err)

		// b1 is merged so IsAncestor should only be called for b2
		assert.NotContains(t, isAncestorCalls, "b1", "merged branch b1 should be skipped")
		assert.Contains(t, isAncestorCalls, "b2", "active branch b2 should be checked")
	})
}

func TestCheckNoMergeQueuePRs(t *testing.T) {
	t.Run("no queued PRs passes", func(t *testing.T) {
		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
				{Branch: "b2"},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckNoMergeQueuePRs(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.NoError(t, err)
	})

	t.Run("queued unmerged PR fails", func(t *testing.T) {
		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}, Queued: true},
				{Branch: "b2"},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckNoMergeQueuePRs(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.Error(t, err)
	})

	t.Run("queued merged PR passes", func(t *testing.T) {
		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}, Queued: true},
			},
		}

		cfg, _, _ := config.NewTestConfig()
		err := modify.CheckNoMergeQueuePRs(cfg, s)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.NoError(t, err)
	})
}

func TestCheckNoModifyInProgress(t *testing.T) {
	t.Run("no state file passes", func(t *testing.T) {
		gitDir := t.TempDir()
		cfg, _, _ := config.NewTestConfig()
		err := checkNoModifyInProgress(cfg, gitDir)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.NoError(t, err)
	})

	t.Run("applying phase returns ErrModifyRecovery", func(t *testing.T) {
		gitDir := t.TempDir()
		state := &modify.StateFile{
			SchemaVersion: 1,
			Phase:         "applying",
			StartedAt:     time.Now().UTC(),
		}
		require.NoError(t, modify.SaveState(gitDir, state))

		cfg, _, _ := config.NewTestConfig()
		err := checkNoModifyInProgress(cfg, gitDir)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.ErrorIs(t, err, ErrModifyRecovery)
	})

	t.Run("pending_submit phase returns ErrSilent", func(t *testing.T) {
		gitDir := t.TempDir()
		state := &modify.StateFile{
			SchemaVersion: 1,
			Phase:         "pending_submit",
			StartedAt:     time.Now().UTC(),
		}
		require.NoError(t, modify.SaveState(gitDir, state))

		cfg, _, _ := config.NewTestConfig()
		err := checkNoModifyInProgress(cfg, gitDir)
		cfg.Out.Close()
		cfg.Err.Close()
		assert.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// 3. Build functions tests
// ---------------------------------------------------------------------------

func TestBuildModifySnapshot(t *testing.T) {
	mock := &git.MockOps{
		RevParseFn: func(ref string) (string, error) {
			return "sha-" + ref, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	snapshot, err := modify.BuildSnapshot(s)
	require.NoError(t, err)

	// Verify branch snapshots
	require.Len(t, snapshot.Branches, 3)
	assert.Equal(t, "b1", snapshot.Branches[0].Name)
	assert.Equal(t, "sha-b1", snapshot.Branches[0].TipSHA)
	assert.Equal(t, 0, snapshot.Branches[0].Position)

	assert.Equal(t, "b2", snapshot.Branches[1].Name)
	assert.Equal(t, "sha-b2", snapshot.Branches[1].TipSHA)
	assert.Equal(t, 1, snapshot.Branches[1].Position)

	assert.Equal(t, "b3", snapshot.Branches[2].Name)
	assert.Equal(t, "sha-b3", snapshot.Branches[2].TipSHA)
	assert.Equal(t, 2, snapshot.Branches[2].Position)

	// Verify stack metadata is valid JSON containing the stack
	var restoredStack stack.Stack
	err = json.Unmarshal(snapshot.StackMetadata, &restoredStack)
	require.NoError(t, err)
	assert.Equal(t, "main", restoredStack.Trunk.Branch)
	require.Len(t, restoredStack.Branches, 3)
	assert.Equal(t, "b1", restoredStack.Branches[0].Branch)
	assert.Equal(t, "b2", restoredStack.Branches[1].Branch)
	assert.Equal(t, "b3", restoredStack.Branches[2].Branch)
}

func TestBuildModifyPlan(t *testing.T) {
	t.Run("drop action", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionDrop},
				Removed:          true,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
			},
		}

		plan := modify.BuildPlan(nodes)
		assert.Equal(t, []modify.Action{{Type: "drop", Branch: "b1"}}, plan)
	})

	t.Run("rename action", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "b1-new"},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
			},
		}

		plan := modify.BuildPlan(nodes)
		require.Len(t, plan, 1)
		assert.Equal(t, "rename", plan[0].Type)
		assert.Equal(t, "b1", plan[0].Branch)
		assert.Equal(t, "b1-new", plan[0].NewName)
	})

	t.Run("mixed actions", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "feature-1"},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionDrop},
				Removed:          true,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b3"}},
				OriginalPosition: 2,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionFoldDown},
				Removed:          true,
			},
		}

		plan := modify.BuildPlan(nodes)
		require.Len(t, plan, 3)
		assert.Equal(t, "rename", plan[0].Type)
		assert.Equal(t, "feature-1", plan[0].NewName)
		assert.Equal(t, "drop", plan[1].Type)
		assert.Equal(t, "fold_down", plan[2].Type)
	})

	t.Run("no changes produces empty plan", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1,
			},
		}

		plan := modify.BuildPlan(nodes)
		assert.Empty(t, plan)
	})

	t.Run("position change produces move action", func(t *testing.T) {
		// b2 moved to position 0, b1 moved to position 1 (swapped)
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b2"}},
				OriginalPosition: 1, // was at 1, now at 0
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "b1"}},
				OriginalPosition: 0, // was at 0, now at 1
			},
		}

		plan := modify.BuildPlan(nodes)
		require.Len(t, plan, 2)
		assert.Equal(t, "move", plan[0].Type)
		assert.Equal(t, "b2", plan[0].Branch)
		assert.Equal(t, 0, plan[0].NewPosition)
		assert.Equal(t, "move", plan[1].Type)
		assert.Equal(t, "b1", plan[1].Branch)
		assert.Equal(t, 1, plan[1].NewPosition)
	})
}

// ---------------------------------------------------------------------------
// 4. Full preconditions integration test
// ---------------------------------------------------------------------------

func TestCheckModifyPreconditions_NotInteractive(t *testing.T) {
	// cfg from NewTestConfig is not interactive by default (piped output)
	cfg, _, _ := config.NewTestConfig()
	// Ensure ForceInteractive is false (default)
	cfg.ForceInteractive = false

	tmpDir := t.TempDir()
	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	_, err := checkModifyPreconditions(cfg)
	cfg.Out.Close()
	cfg.Err.Close()
	assert.Error(t, err)
}

func TestCheckModifyPreconditions_RebaseInProgress(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:                func() (string, error) { return tmpDir, nil },
		CurrentBranchFn:         func() (string, error) { return "b1", nil },
		IsRebaseInProgressFn:    func() (bool, error) { return true, nil },
		HasUncommittedChangesFn: func() (bool, error) { return false, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true

	_, err := checkModifyPreconditions(cfg)
	cfg.Out.Close()
	cfg.Err.Close()
	assert.ErrorIs(t, err, ErrRebaseActive)
}

func TestCheckModifyPreconditions_DirtyWorkingTree(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:                func() (string, error) { return tmpDir, nil },
		CurrentBranchFn:         func() (string, error) { return "b1", nil },
		IsRebaseInProgressFn:    func() (bool, error) { return false, nil },
		HasUncommittedChangesFn: func() (bool, error) { return true, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true

	_, err := checkModifyPreconditions(cfg)
	cfg.Out.Close()
	cfg.Err.Close()
	assert.Error(t, err)
}

func TestCheckModifyPreconditions_AllPass(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:                func() (string, error) { return tmpDir, nil },
		CurrentBranchFn:         func() (string, error) { return "b1", nil },
		IsRebaseInProgressFn:    func() (bool, error) { return false, nil },
		HasUncommittedChangesFn: func() (bool, error) { return false, nil },
		IsAncestorFn:            func(a, d string) (bool, error) { return true, nil },
		LogMergesFn:             func(base, head string) ([]git.CommitInfo, error) { return nil, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true
	// Inject mock GitHub client so syncStackPRs doesn't fail
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
	}

	result, err := checkModifyPreconditions(cfg)
	cfg.Out.Close()
	cfg.Err.Close()
	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, tmpDir, result.GitDir)
	assert.Equal(t, "b1", result.CurrentBranch)
}

func TestRunModify_FullyMergedStack_ShortCircuits(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:                func() (string, error) { return tmpDir, nil },
		CurrentBranchFn:         func() (string, error) { return "b1", nil },
		IsRebaseInProgressFn:    func() (bool, error) { return false, nil },
		HasUncommittedChangesFn: func() (bool, error) { return false, nil },
		BranchExistsFn:          func(string) (bool, error) { return true, nil },
		IsAncestorFn:            func(a, d string) (bool, error) { return true, nil },
		LogMergesFn:             func(base, head string) ([]git.CommitInfo, error) { return nil, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
	}

	// runModify must short-circuit (and never launch the TUI) on a fully
	// merged stack, returning cleanly like submit's "nothing to submit" path.
	err := runModify(cfg)

	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	assert.NoError(t, err)
	assert.Contains(t, output, "All branches in this stack have been merged")
	assert.Contains(t, output, "gh stack init")
}

// ---------------------------------------------------------------------------
// 5. State file path / exists edge cases
// ---------------------------------------------------------------------------

func TestModifyStatePath(t *testing.T) {
	p := modify.StatePath("/fake/git/dir")
	assert.Equal(t, filepath.Join("/fake/git/dir", "gh-stack-modify-state"), p)
}

func TestModifyStateExistsAfterSaveAndClear(t *testing.T) {
	gitDir := t.TempDir()

	assert.False(t, modify.StateExists(gitDir))

	state := &modify.StateFile{SchemaVersion: 1, Phase: "applying", StartedAt: time.Now().UTC()}
	require.NoError(t, modify.SaveState(gitDir, state))
	assert.True(t, modify.StateExists(gitDir))

	modify.ClearState(gitDir)
	assert.False(t, modify.StateExists(gitDir))
}

func TestLoadModifyState_InvalidJSON(t *testing.T) {
	gitDir := t.TempDir()
	err := os.WriteFile(modify.StatePath(gitDir), []byte("not json"), 0644)
	require.NoError(t, err)

	loaded, err := modify.LoadState(gitDir)
	assert.Error(t, err)
	assert.Nil(t, loaded)
	assert.Contains(t, err.Error(), "parsing modify state")
}

// ---------------------------------------------------------------------------
// 6. State round-trip with prior remote stack ID
// ---------------------------------------------------------------------------

func TestModifyStateRoundTrip_WithPriorStackID(t *testing.T) {
	gitDir := t.TempDir()

	state := &modify.StateFile{
		SchemaVersion:      1,
		StackName:          "main",
		StartedAt:          time.Now().UTC(),
		Phase:              "pending_submit",
		PriorRemoteStackID: "stack-abc-123",
		Snapshot: modify.Snapshot{
			Branches: []modify.BranchSnapshot{
				{Name: "b1", TipSHA: "aaa", Position: 0},
			},
			StackMetadata: json.RawMessage(`{}`),
		},
		Plan: []modify.Action{
			{Type: "fold_down", Branch: "b2"},
		},
	}

	require.NoError(t, modify.SaveState(gitDir, state))

	loaded, err := modify.LoadState(gitDir)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, "pending_submit", loaded.Phase)
	assert.Equal(t, "stack-abc-123", loaded.PriorRemoteStackID)
}

// ---------------------------------------------------------------------------
// 7. checkModifyStateGuard edge cases
// ---------------------------------------------------------------------------

func TestCheckModifyStateGuard_MissingState(t *testing.T) {
	err := modify.CheckStateGuard(t.TempDir())
	assert.NoError(t, err)
}

func TestCheckModifyStateGuard_UnknownPhase(t *testing.T) {
	gitDir := t.TempDir()
	state := &modify.StateFile{
		SchemaVersion: 1,
		Phase:         "unknown_phase",
		StartedAt:     time.Now().UTC(),
	}
	require.NoError(t, modify.SaveState(gitDir, state))

	err := modify.CheckStateGuard(gitDir)
	assert.ErrorContains(t, err, "unrecognized modify state phase")
}

// ---------------------------------------------------------------------------
// 6. runModifyAbort recovery
// ---------------------------------------------------------------------------

// Regression test: aborting a modify that stopped at a conflict must actually
// unwind the stack (abort the in-flight rebase, reset branch tips to their
// pre-modify SHAs, restore metadata, clear state) — not fall into a default
// branch that merely deletes the state file and strands the user.
func TestRunModifyAbort_ConflictPhase_Unwinds(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	meta, err := json.Marshal(s)
	require.NoError(t, err)

	snapshot := modify.Snapshot{
		Branches: []modify.BranchSnapshot{
			{Name: "A", TipSHA: "sha-A-original", Position: 0},
			{Name: "B", TipSHA: "sha-B-original", Position: 1},
		},
		StackMetadata: meta,
	}

	// The state ApplyPlan persists when a cascade rebase conflicts.
	state := &modify.StateFile{
		SchemaVersion:  1,
		StackName:      "main",
		StackIndex:     0,
		Phase:          modify.PhaseConflict,
		ConflictBranch: "B",
		ConflictType:   "rebase",
		Snapshot:       snapshot,
	}
	require.NoError(t, modify.SaveState(tmpDir, state))

	var rebaseAborted bool
	var resetCalls []struct{ branch, sha string }
	current := ""
	inProgress := true
	mock := &git.MockOps{
		GitDirFn:                 func() (string, error) { return tmpDir, nil },
		IsRebaseInProgressFn:     func() (bool, error) { return inProgress, nil },
		IsCherryPickInProgressFn: func() (bool, error) { return false, nil },
		RebaseAbortFn:            func() error { rebaseAborted = true; inProgress = false; return nil },
		BranchExistsFn:           func(string) (bool, error) { return true, nil },
		CheckoutBranchFn:         func(name string) error { current = name; return nil },
		ResetHardFn: func(sha string) error {
			resetCalls = append(resetCalls, struct{ branch, sha string }{current, sha})
			return nil
		},
		CreateBranchFn: func(string, string) error { return nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()

	err = runModifyAbort(cfg)

	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	require.NoError(t, err)

	// The in-flight rebase must have been aborted.
	assert.True(t, rebaseAborted, "in-progress rebase should be aborted during recovery")

	// The state file must be cleared because recovery ran (not left dangling,
	// and not deleted-without-unwind).
	assert.False(t, modify.StateExists(tmpDir), "state file should be cleared after a successful abort")

	// Branch tips must be reset to their pre-modify snapshot SHAs.
	resetMap := map[string]string{}
	for _, r := range resetCalls {
		resetMap[r.branch] = r.sha
	}
	assert.Equal(t, "sha-A-original", resetMap["A"])
	assert.Equal(t, "sha-B-original", resetMap["B"])

	// It must not fall into the old default branch.
	assert.NotContains(t, output, "unexpected modify state phase")
	assert.Contains(t, output, "Restoring stack to pre-modify state")
}

// PendingSubmit abort is a no-op that guides the user to submit; it must not
// try to unwind (the local changes already succeeded).
func TestRunModifyAbort_PendingSubmit_NoUnwind(t *testing.T) {
	tmpDir := t.TempDir()

	state := &modify.StateFile{
		SchemaVersion: 1,
		StackName:     "main",
		StackIndex:    0,
		Phase:         modify.PhasePendingSubmit,
	}
	require.NoError(t, modify.SaveState(tmpDir, state))

	var resetCalled bool
	mock := &git.MockOps{
		GitDirFn:    func() (string, error) { return tmpDir, nil },
		ResetHardFn: func(string) error { resetCalled = true; return nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()

	err := runModifyAbort(cfg)

	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(errR)
	output := string(out)

	require.NoError(t, err)
	assert.False(t, resetCalled, "pending-submit abort must not unwind branches")
	assert.True(t, modify.StateExists(tmpDir), "pending-submit state should be preserved")
	assert.Contains(t, output, "gh stack submit")
}

func TestCheckModifyPreconditions_Worktrees(t *testing.T) {
	for _, ownerBranch := range []string{"", "main", "b2"} {
		t.Run("foreign owner "+ownerBranch, func(t *testing.T) {
			dir, origin, foreign := t.TempDir(), t.TempDir(), t.TempDir()
			s := stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "b1"}, {Branch: "b2"},
				},
			}
			writeStackFile(t, dir, s)
			mock := &git.MockOps{
				GitDirFn:        func() (string, error) { return dir, nil },
				RootDirFn:       func() (string, error) { return origin, nil },
				CurrentBranchFn: func() (string, error) { return "b1", nil },
				BranchExistsFn:  func(string) (bool, error) { return true, nil },
				IsAncestorFn:    func(string, string) (bool, error) { return true, nil },
				WorktreesFn: func() ([]git.Worktree, error) {
					return []git.Worktree{
						{Path: origin, Branch: "b1"},
						{Path: foreign, Branch: ownerBranch},
					}, nil
				},
			}
			mock.ForWorktreeFn = func(path string) (git.Ops, error) {
				if worktree.SamePath(path, foreign) {
					return &git.MockOps{RootDirFn: func() (string, error) { return foreign, nil }}, nil
				}
				return mock, nil
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, _, errR := config.NewTestConfig()
			cfg.ForceInteractive = true
			var prQueries atomic.Int32
			cfg.GitHubClientOverride = &github.MockClient{
				FindPRForBranchFn: func(string) (*github.PullRequest, error) {
					prQueries.Add(1)
					return nil, nil
				},
			}
			_, err := checkModifyPreconditions(cfg)
			cfg.Out.Close()
			cfg.Err.Close()
			output, readErr := io.ReadAll(errR)
			require.NoError(t, readErr)
			require.NoError(t, err)
			assert.NotContains(t, string(output), "distributed modify is not supported")
			assert.Positive(t, prQueries.Load(), "action-specific owner checks happen after the TUI produces its plan")
			assert.False(t, modify.StateExists(dir))
		})
	}
}

func TestRunModifyRecovery_UsesRecordedOrigin(t *testing.T) {
	for _, tc := range []struct{ command, conflictType string }{
		{"continue", "rebase"}, {"abort", "rebase"},
		{"continue", "cherry_pick"}, {"abort", "cherry_pick"},
	} {
		t.Run(tc.command+" "+tc.conflictType, func(t *testing.T) {
			common, origin, caller := t.TempDir(), t.TempDir(), t.TempDir()
			originDir, callerDir := filepath.Join(common, "worktrees", "origin"), filepath.Join(common, "worktrees", "caller")
			require.NoError(t, os.MkdirAll(originDir, 0755))
			require.NoError(t, os.MkdirAll(callerDir, 0755))
			s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}}}
			if tc.conflictType == "cherry_pick" {
				s.Branches = append(s.Branches, stack.BranchRef{Branch: "B"})
			}
			writeStackFile(t, common, s)
			metadata, err := json.Marshal(s)
			require.NoError(t, err)
			state := &modify.StateFile{
				SchemaVersion: 1, Phase: modify.PhaseConflict, ConflictBranch: "A", ConflictType: tc.conflictType,
				OriginalBranch: "A",
				Snapshot: modify.Snapshot{
					StackMetadata: metadata,
					Branches:      []modify.BranchSnapshot{{Name: "A", TipSHA: "original"}},
				},
				Worktrees: &worktree.Context{
					Origin:        worktree.Location{Path: origin, ID: filepath.Join("worktrees", "origin")},
					Pending:       "A",
					PendingBefore: "original",
				},
			}
			if tc.conflictType == "cherry_pick" {
				state.ConflictBranch, state.FoldBranch, state.FoldTarget = "B", "B", "A"
				state.Snapshot.Branches = append(state.Snapshot.Branches, modify.BranchSnapshot{Name: "B", TipSHA: "source"})
			}
			state.RecordStack(&s)
			require.NoError(t, modify.SaveState(common, state))
			inProgress, continued, aborted := true, false, false
			sha := "original"
			revParse := func(ref string) (string, error) {
				if ref == "B" {
					return "source", nil
				}
				return sha, nil
			}
			originOps := &git.MockOps{
				GitDirFn:             func() (string, error) { return originDir, nil },
				CommonDirFn:          func() (string, error) { return common, nil },
				RootDirFn:            func() (string, error) { return origin, nil },
				CurrentBranchFn:      func() (string, error) { return "A", nil },
				RevParseFn:           revParse,
				IsRebaseInProgressFn: func() (bool, error) { return inProgress && tc.conflictType == "rebase", nil },
				RebaseContinueFn: func(git.RebaseOpts) error {
					require.Equal(t, "rebase", tc.conflictType)
					continued, inProgress, sha = true, false, "updated"
					return nil
				},
				RebaseAbortFn: func() error {
					require.Equal(t, "rebase", tc.conflictType)
					aborted, inProgress = true, false
					return nil
				},
				IsCherryPickInProgressFn: func() (bool, error) { return inProgress && tc.conflictType == "cherry_pick", nil },
				CherryPickContinueFn: func() error {
					require.Equal(t, "cherry_pick", tc.conflictType)
					continued, inProgress, sha = true, false, "updated"
					return nil
				},
				CherryPickAbortFn: func() error {
					require.Equal(t, "cherry_pick", tc.conflictType)
					aborted, inProgress = true, false
					return nil
				},
			}
			callerSensitiveCalls := 0
			callerOps := &git.MockOps{
				GitDirFn:        func() (string, error) { return callerDir, nil },
				CommonDirFn:     func() (string, error) { return common, nil },
				RootDirFn:       func() (string, error) { return caller, nil },
				CurrentBranchFn: func() (string, error) { return "observer", nil },
				RevParseFn:      revParse,
				CheckoutBranchFn: func(string) error {
					callerSensitiveCalls++
					return nil
				},
				IsRebaseInProgressFn: func() (bool, error) { callerSensitiveCalls++; return false, nil },
				IsCherryPickInProgressFn: func() (bool, error) {
					callerSensitiveCalls++
					return false, nil
				},
				HasUncommittedChangesFn: func() (bool, error) {
					callerSensitiveCalls++
					return true, nil
				},
				WorktreesFn: func() ([]git.Worktree, error) {
					return []git.Worktree{{Path: origin, Branch: "A"}, {Path: caller, Branch: "observer"}}, nil
				},
			}
			callerOps.ForWorktreeFn = func(path string) (git.Ops, error) {
				require.True(t, worktree.SamePath(path, origin))
				return originOps, nil
			}
			restore := git.SetOps(callerOps)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			if tc.command == "continue" {
				require.NoError(t, runModifyContinue(cfg))
				assert.True(t, continued)
			} else {
				require.NoError(t, runModifyAbort(cfg))
				assert.True(t, aborted)
			}
			assert.Zero(t, callerSensitiveCalls)
			assert.False(t, modify.StateExists(common))
			assert.Nil(t, cfg.StackMutation)
		})
	}
}

func TestModify_InvalidJournalsAreRetained(t *testing.T) {
	for _, content := range []string{"not json", `{"schema_version":1,"phase":"unknown"}`} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			path := modify.StatePath(dir)
			require.NoError(t, os.WriteFile(path, []byte(content), 0644))
			restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return dir, nil }})
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			require.Error(t, runModifyAbort(cfg))
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, content, string(got))
			require.Error(t, checkNoModifyInProgress(cfg, dir))
		})
	}
}

func TestModifyStateIOFailures(t *testing.T) {
	dir := t.TempDir()
	path := modify.StatePath(dir)
	require.NoError(t, os.Mkdir(path, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0644))
	require.Error(t, modify.SaveState(dir, &modify.StateFile{SchemaVersion: 1, Phase: modify.PhaseApplying}))
	require.Error(t, modify.ClearState(dir))
	_, err := os.Stat(filepath.Join(path, "keep"))
	require.NoError(t, err)
	require.Error(t, modify.CheckStateGuard(dir))
}

func TestModifyApply_DoesNotReportAdministrationDirectoryAsOwner(t *testing.T) {
	dir, origin := t.TempDir(), t.TempDir()
	s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}}}
	writeStackFile(t, dir, s)
	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return filepath.Join(dir, "worktrees", "origin"), nil },
		CommonDirFn:     func() (string, error) { return dir, nil },
		RootDirFn:       func() (string, error) { return origin, nil },
		CurrentBranchFn: func() (string, error) { return "B", nil },
		BranchExistsFn:  func(string) (bool, error) { return true, nil },
		RevParseFn:      func(ref string) (string, error) { return "sha-" + ref, nil },
		WorktreesFn: func() ([]git.Worktree, error) {
			return []git.Worktree{{Path: dir, Branch: "A"}, {Path: origin, Branch: "B"}}, nil
		},
	}
	mock.ForWorktreeFn = func(path string) (git.Ops, error) {
		if worktree.SamePath(path, dir) {
			return &git.MockOps{
				GitDirFn:    func() (string, error) { return dir, nil },
				CommonDirFn: func() (string, error) { return dir, nil },
				RootDirFn:   func() (string, error) { return "", assert.AnError },
			}, nil
		}
		return mock, nil
	}
	restore := git.SetOps(mock)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	sf, err := stack.Load(dir)
	require.NoError(t, err)
	nodes := []modifyview.ModifyBranchNode{
		{BranchNode: stackview.BranchNode{Ref: s.Branches[1]}, OriginalPosition: 1},
		{BranchNode: stackview.BranchNode{Ref: s.Branches[0]}, OriginalPosition: 0},
	}
	_, _, err = modify.ApplyPlan(cfg, dir, &sf.Stacks[0], sf, nodes, "B", func(*stack.Stack) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "working-tree root")
	assert.NotContains(t, err.Error(), "checked out in worktree "+dir)
	assert.False(t, modify.StateExists(dir))
}

func TestRunModifyContinue_LegacyPrivateJournalKeepsOriginalCatalog(t *testing.T) {
	common, origin := t.TempDir(), t.TempDir()
	private := filepath.Join(common, "worktrees", "legacy")
	require.NoError(t, os.MkdirAll(private, 0755))
	other := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "other"}}}
	s := stack.Stack{Trunk: other.Trunk, Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}, {Branch: "C"}}}
	writeStackFile(t, common, other)
	writeStackFile(t, private, s)
	metadata, err := json.Marshal(s)
	require.NoError(t, err)
	state := &modify.StateFile{
		SchemaVersion: 1, Phase: modify.PhaseConflict, ConflictType: "rebase", ConflictBranch: "A",
		OriginalBranch: "A", RemainingBranches: []string{"B", "C"},
		OriginalRefs: map[string]string{"A": "sha-main", "B": "sha-A", "C": "sha-B"},
		Snapshot: modify.Snapshot{
			StackMetadata: metadata,
			Branches: []modify.BranchSnapshot{
				{Name: "A", TipSHA: "sha-A"}, {Name: "B", TipSHA: "sha-B"}, {Name: "C", TipSHA: "sha-C"},
			},
		},
	}
	require.NoError(t, modify.SaveState(private, state))
	inProgress, refused := true, false
	continued := 0
	current := "A"
	restore := git.SetOps(&git.MockOps{
		GitDirFn:             func() (string, error) { return private, nil },
		CommonDirFn:          func() (string, error) { return common, nil },
		RootDirFn:            func() (string, error) { return origin, nil },
		CurrentBranchFn:      func() (string, error) { return current, nil },
		BranchExistsFn:       func(string) (bool, error) { return true, nil },
		RevParseFn:           func(ref string) (string, error) { return "sha-" + ref, nil },
		IsAncestorFn:         func(string, string) (bool, error) { return false, nil },
		IsRebaseInProgressFn: func() (bool, error) { return inProgress, nil },
		RebaseContinueFn: func(git.RebaseOpts) error {
			continued++
			inProgress = false
			return nil
		},
		RebaseOntoFn: func(_, _, branch string, _ git.RebaseOpts) error {
			current = branch
			if branch == "B" && !refused {
				refused, inProgress = true, true
				return assert.AnError
			}
			return nil
		},
		CheckoutBranchFn: func(branch string) error { current = branch; return nil },
	})
	defer restore()
	cfg, _, errR := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.ErrorIs(t, runModifyContinue(cfg), ErrConflict)
	saved, err := modify.LoadState(private)
	require.NoError(t, err)
	require.NotNil(t, saved.Worktrees, "continuation should record its origin before further changes")
	continueErr := runModifyContinue(cfg)
	cfg.Out.Close()
	cfg.Err.Close()
	stderr, err := io.ReadAll(errR)
	require.NoError(t, err)
	require.NoError(t, continueErr, "%s", stderr)
	assert.Equal(t, 2, continued)
	assert.False(t, modify.StateExists(private))
	commonCatalog, err := stack.Load(common)
	require.NoError(t, err)
	require.Len(t, commonCatalog.Stacks, 1)
	assert.Equal(t, []string{"other"}, commonCatalog.Stacks[0].BranchNames())
	privateCatalog, err := stack.Load(private)
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B", "C"}, privateCatalog.Stacks[0].BranchNames())
}

func TestRunModifyContinue_UsesForeignPendingOwner(t *testing.T) {
	common, origin, target, caller := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	refs := map[string]string{"main": "sha-main", "A": "sha-A", "C": "sha-C"}
	s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "C"}}}
	writeStackFile(t, common, s)
	metadata, err := json.Marshal(s)
	require.NoError(t, err)
	state := &modify.StateFile{
		SchemaVersion: 1, Phase: modify.PhaseConflict, ConflictType: "rebase", ConflictBranch: "A",
		OriginalBranch: "C", RemainingBranches: []string{"C"}, OriginalRefs: map[string]string{"C": "sha-A"},
		Snapshot: modify.Snapshot{
			StackMetadata: metadata,
			Branches:      []modify.BranchSnapshot{{Name: "A", TipSHA: "sha-A"}, {Name: "C", TipSHA: "sha-C"}},
		},
		Worktrees: &worktree.Context{
			Origin:        worktree.Location{Path: origin},
			Owners:        map[string]*worktree.Location{"A": {Path: target}},
			Pending:       "A",
			PendingBefore: "sha-A",
		},
	}
	state.RecordStack(&s)
	require.NoError(t, modify.SaveState(common, state))
	scoped := func(path, name string) *git.MockOps {
		return &git.MockOps{
			RootDirFn:       func() (string, error) { return path, nil },
			CommonDirFn:     func() (string, error) { return common, nil },
			GitDirFn:        func() (string, error) { return filepath.Join(common, "worktrees", name), nil },
			CurrentBranchFn: func() (string, error) { return name, nil },
			RevParseFn:      func(ref string) (string, error) { return refs[ref], nil },
			IsAncestorFn:    func(string, string) (bool, error) { return true, nil },
			MergeBaseFn:     func(string, string) (string, error) { return "sha-A", nil },
			RebaseContinueFn: func(git.RebaseOpts) error {
				t.Fatal("native continuation must run only in the pending owner's worktree")
				return nil
			},
		}
	}
	originOps, targetOps, callerOps := scoped(origin, "C"), scoped(target, "A"), scoped(caller, "observer")
	inProgress, continued := true, false
	targetOps.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
	targetOps.RebaseContinueFn = func(git.RebaseOpts) error { inProgress, continued = false, true; return nil }
	callerOps.WorktreesFn = func() ([]git.Worktree, error) {
		return []git.Worktree{{Path: origin, Branch: "C"}, {Path: target, Branch: "A"}, {Path: caller, Branch: "observer"}}, nil
	}
	callerOps.ForWorktreeFn = func(path string) (git.Ops, error) {
		if worktree.SamePath(path, target) {
			return targetOps, nil
		}
		if worktree.SamePath(path, origin) {
			return originOps, nil
		}
		return callerOps, nil
	}
	restore := git.SetOps(callerOps)
	defer restore()
	nativeOps, path, err := modify.ConflictOps(state)
	require.NoError(t, err)
	assert.Same(t, targetOps, nativeOps)
	assert.Equal(t, target, path)
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.NoError(t, runModifyContinue(cfg))
	assert.True(t, continued)
	assert.False(t, modify.StateExists(common))
	assert.Nil(t, cfg.StackMutation)
}

func TestModifyTUI_RejectsMixedReorderFold(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		keys  []rune
		order []string
		kind  modifyview.ActionType
	}{
		{"move B below A then fold up", []rune{'J', 'u'}, []string{"C", "A", "B"}, modifyview.ActionMove},
		{"fold B up then move", []rune{'u', 'J'}, []string{"C", "B", "A"}, modifyview.ActionFoldUp},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			nodes := []modifyview.ModifyBranchNode{
				{BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "C"}}, OriginalPosition: 0},
				{BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "B"}, IsCurrent: true}, OriginalPosition: 1},
				{BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}}, OriginalPosition: 2},
			}
			model := modifyview.New(nodes, stack.BranchRef{Branch: "main"}, "test")
			for _, key := range scenario.keys {
				updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
				var ok bool
				model, ok = updated.(modifyview.Model)
				require.True(t, ok)
			}
			var order []string
			for _, node := range model.Nodes() {
				order = append(order, node.Ref.Branch)
				if scenario.kind == modifyview.ActionMove {
					assert.Nil(t, node.PendingAction, "fold must be rejected after reordering")
					assert.False(t, node.Removed)
				}
			}

			assert.Equal(t, scenario.order, order)
			require.Len(t, model.StagedActions(), 1, "only the first operation may be staged")
			assert.Equal(t, scenario.kind, model.StagedActions()[0].Type)
		})
	}
}

func TestModifyTUI_DropThenFoldUpSkipsDroppedNeighbor(t *testing.T) {
	nodes := []modifyview.ModifyBranchNode{
		{BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "C"}}, OriginalPosition: 0},
		{BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "B"}, IsCurrent: true}, OriginalPosition: 1},
		{BranchNode: stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}}, OriginalPosition: 2},
	}
	model := modifyview.New(nodes, stack.BranchRef{Branch: "main"}, "test")
	for _, key := range []rune{'x', 'j', 'u'} {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		var ok bool
		model, ok = updated.(modifyview.Model)
		require.True(t, ok)
	}
	actions := model.StagedActions()
	require.Len(t, actions, 2)
	assert.Equal(t, modifyview.ActionDrop, actions[0].Type)
	assert.Equal(t, "B", actions[0].BranchName)
	assert.Equal(t, modifyview.ActionFoldUp, actions[1].Type)
	assert.Equal(t, "A", actions[1].BranchName)
	assert.Equal(t, "C", actions[1].FoldTarget)
	assert.True(t, model.Nodes()[1].Removed)
	assert.True(t, model.Nodes()[2].Removed)
}
