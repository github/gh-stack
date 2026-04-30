package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
	"github.com/github/gh-stack/internal/tui/stackview"
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
		// b1 is Removed=true so it's skipped in the main loop.
		// b2 has no changes and is at its original position → nothing.
		assert.Empty(t, plan, "removed nodes are skipped; unchanged nodes produce nothing")
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
		// b1 has a rename action → included
		// b2 and b3 are Removed → skipped in the loop
		require.Len(t, plan, 1)
		assert.Equal(t, "rename", plan[0].Type)
		assert.Equal(t, "feature-1", plan[0].NewName)
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
		IsRebaseInProgressFn:    func() bool { return true },
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
		IsRebaseInProgressFn:    func() bool { return false },
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
		IsRebaseInProgressFn:    func() bool { return false },
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

func TestCheckModifyStateGuard_IgnoresReadErrors(t *testing.T) {
	// Use a path that doesn't exist and isn't a directory — this tests
	// the "ignore read errors" branch in checkModifyStateGuard.
	err := modify.CheckStateGuard("/nonexistent/path/that/does/not/exist")
	assert.NoError(t, err, "guard should silently ignore read errors")
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
	assert.NoError(t, err, "guard only blocks on 'applying' phase")
}
