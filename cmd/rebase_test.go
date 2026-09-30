package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRebase_RecoveryStateLookupFailurePreservesJournal(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		t.Run(action, func(t *testing.T) {
			gitDir := t.TempDir()
			writeStackFile(t, gitDir, stack.Stack{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "b1"}},
			})
			state := &rebaseState{
				OriginalBranch: "b1",
				ConflictBranch: "b1",
				OriginalRefs:   map[string]string{"b1": "original"},
			}
			require.NoError(t, saveRebaseState(gitDir, state))
			lookupErr := fmt.Errorf("rebase state lookup failed")
			mock := newRebaseMock(gitDir, "b1")
			mock.IsRebaseInProgressFn = func() (bool, error) { return true, lookupErr }
			mock.RebaseContinueFn = func(git.RebaseOpts) error {
				t.Fatal("must not continue after a failed state lookup")
				return nil
			}
			mock.RebaseAbortFn = func() error {
				t.Fatal("must not abort after a failed state lookup")
				return nil
			}
			mock.CheckoutBranchFn = func(string) error {
				t.Fatal("must not check out branches after a failed state lookup")
				return nil
			}
			mock.ResetHardFn = func(string) error {
				t.Fatal("must not reset branches after a failed state lookup")
				return nil
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			cfg.GitHubClientOverride = &github.MockClient{}
			var err error
			if action == "continue" {
				err = continueRebase(cfg, gitDir)
			} else {
				err = abortRebase(cfg, gitDir)
			}
			require.ErrorIs(t, err, lookupErr)
			loaded, err := loadRebaseState(gitDir)
			require.NoError(t, err)
			assert.Equal(t, state, loaded)
		})
	}
}

func TestRebase_WorktreeRecoveryLookupFailurePreservesJournal(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		for _, query := range []string{"factory", "native state", "branch"} {
			if action == "continue" && query == "branch" {
				continue
			}
			t.Run(action+"/"+query, func(t *testing.T) {
				dir := t.TempDir()
				s := stack.Stack{
					Trunk:    stack.BranchRef{Branch: "main"},
					Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
				}
				writeStackFile(t, dir, s)
				mock := newRebaseMock(dir, "b1")
				restore := git.SetOps(mock)
				defer restore()
				ctx, err := worktree.New()
				require.NoError(t, err)
				ctx.Pending, ctx.PendingBefore = "b1", "old-b1"
				ctx.Touched["b2"] = "new-b2"
				state := newWorktreeRebaseState(&s, ctx, "b1", map[string]string{"b1": "old-b1", "b2": "old-b2"}, trunkTarget{}, 0, 2)
				state.Phase, state.ConflictBranch = "conflict", "b1"
				require.NoError(t, saveRebaseState(dir, state))
				beforeJournal, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
				require.NoError(t, err)
				beforeCatalog, err := os.ReadFile(filepath.Join(dir, "gh-stack"))
				require.NoError(t, err)
				lookupErr := fmt.Errorf("%s lookup failed", query)
				switch query {
				case "factory":
					mock.ForWorktreeFn = func(string) (git.Ops, error) { return nil, lookupErr }
				case "native state":
					mock.IsRebaseInProgressFn = func() (bool, error) { return true, lookupErr }
				case "branch":
					mock.BranchExistsFn = func(branch string) (bool, error) {
						if branch == "b2" {
							return false, lookupErr
						}
						return true, nil
					}
				}
				forbidRewriteMutations(t, mock)
				mock.RebaseContinueFn = func(git.RebaseOpts) error { t.Fatal("must not continue after a lookup error"); return nil }
				mock.RebaseAbortFn = func() error { t.Fatal("must not abort after a lookup error"); return nil }
				cfg := issue250TestConfig(t)

				err = runRebase(cfg, &rebaseOptions{cont: action == "continue", abort: action == "abort"})

				require.ErrorIs(t, err, lookupErr)
				afterJournal, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
				require.NoError(t, err)
				assert.Equal(t, beforeJournal, afterJournal)
				afterCatalog, err := os.ReadFile(filepath.Join(dir, "gh-stack"))
				require.NoError(t, err)
				assert.Equal(t, beforeCatalog, afterCatalog)
			})
		}
	}
}

// rebaseCall records arguments passed to RebaseOnto or Rebase.
type rebaseCall struct {
	newBase string
	oldBase string
	branch  string
}

// resetCall records arguments passed to CheckoutBranch + ResetHard.
type resetCall struct {
	branch string
	sha    string
}

// newRebaseMock creates a MockOps pre-configured for rebase tests.
// It returns stable SHAs based on ref name, tracks checkout, and allows
// callers to override specific function fields after creation.
func newRebaseMock(tmpDir string, currentBranch string) *git.MockOps {
	return &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return currentBranch, nil },
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
		IsRebaseInProgressFn: func() (bool, error) { return false, nil },
	}
}

// TestRebase_CascadeRebase verifies that a stack [b1, b2, b3] with all active
// branches triggers the correct cascade: b1 rebased onto trunk, b2 onto b1,
// b3 onto b2.
func TestRebase_CascadeRebase(t *testing.T) {
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

	var allRebaseCalls []rebaseCall
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)

	// All branches should be rebased in order: b1 onto main, b2 onto b1, b3 onto b2
	require.Len(t, allRebaseCalls, 3)
	assert.Equal(t, "sha-main", allRebaseCalls[0].newBase, "b1 should be rebased onto the pinned trunk")
	assert.Equal(t, "b1", allRebaseCalls[1].newBase, "b2 should be rebased onto b1")
	assert.Equal(t, "b2", allRebaseCalls[2].newBase, "b3 should be rebased onto b2")

	assert.Contains(t, output, "rebased locally")
}

// TestRebase_MergedBranch_UsesOnto verifies that when b1 has a merged PR,
// it is skipped and b2 uses RebaseOnto with trunk as newBase and b1's original
// SHA as oldBase. b3 also uses --onto (propagation).
func TestRebase_MergedBranch_UsesOnto(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	// Use explicit SHAs so assertions are self-documenting
	branchSHAs := map[string]string{
		"main": "main-sha-aaa",
		"b1":   "b1-orig-sha",
		"b2":   "b2-orig-sha",
		"b3":   "b3-orig-sha",
	}

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RevParseFn = func(ref string) (string, error) {
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")

	// b2: onto trunk, oldBase = b1's original SHA
	// b3: onto b2, oldBase = b2's original SHA (propagation)
	require.Len(t, rebaseCalls, 2)
	assert.Equal(t, rebaseCall{"default-sha", "b1-orig-sha", "b2"}, rebaseCalls[0],
		"b2 should rebase --onto main using b1's original SHA as oldBase")
	assert.Equal(t, rebaseCall{"b2", "b2-orig-sha", "b3"}, rebaseCalls[1],
		"b3 should propagate --onto mode with b2's original SHA as oldBase")
}

// TestRebase_OntoPropagatesToSubsequentBranches verifies that when multiple
// branches are merged, --onto propagates correctly through the chain.
func TestRebase_OntoPropagatesToSubsequentBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11, Merged: true}},
			{Branch: "b3"},
			{Branch: "b4"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	// Use explicit SHAs so assertions are self-documenting
	branchSHAs := map[string]string{
		"main": "main-sha-aaa",
		"b1":   "b1-orig-sha",
		"b2":   "b2-orig-sha",
		"b3":   "b3-orig-sha",
		"b4":   "b4-orig-sha",
	}

	mock := newRebaseMock(tmpDir, "b3")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RevParseFn = func(ref string) (string, error) {
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")
	assert.Contains(t, output, "Skipping b2")

	// b1 merged → ontoOldBase = b1-orig-sha
	// b2 merged → ontoOldBase = b2-orig-sha
	// b3: first non-merged ancestor search finds none → newBase = trunk
	//   RebaseOnto("main", "b2-orig-sha", "b3")
	// b4: first non-merged ancestor = b3 → newBase = b3
	//   RebaseOnto("b3", "b3-orig-sha", "b4")
	require.Len(t, rebaseCalls, 2)
	assert.Equal(t, rebaseCall{"default-sha", "b2-orig-sha", "b3"}, rebaseCalls[0],
		"b3 should rebase --onto main with b2's SHA as oldBase")
	assert.Equal(t, rebaseCall{"b3", "b3-orig-sha", "b4"}, rebaseCalls[1],
		"b4 should rebase --onto b3 with b3's original SHA as oldBase")
}

// TestRebase_StaleOntoOldBase_UsesForkPoint verifies that when a branch
// was already rebased past the merged branch's tip (e.g. by a previous run),
// the stale ontoOldBase is replaced with a reflog fork-point that the branch
// actually contains.
func TestRebase_StaleOntoOldBase_UsesForkPoint(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	// b1's local ref is the stale pre-squash tip from before a previous rebase.
	// b2 was already rebased onto main by a previous run, so b1's old tip
	// is NOT an ancestor of b2.
	branchSHAs := map[string]string{
		"main": "main-sha",
		"b1":   "b1-stale-presquash-sha",
		"b2":   "b2-on-main-sha",
		"b3":   "b3-on-b2-sha",
	}

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RevParseFn = func(ref string) (string, error) {
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.IsAncestorFn = func(ancestor, descendant string) (bool, error) {
		// b1's stale SHA is NOT an ancestor of b2 (b2 was already rebased onto main)
		if ancestor == "b1-stale-presquash-sha" {
			return false, nil
		}
		return true, nil
	}
	mock.MergeBaseForkPointFn = func(a, b string) (string, error) {
		if a == "main" && b == "b2" {
			return "main-b2-forkpoint", nil
		}
		return "default-forkpoint", nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	require.Len(t, rebaseCalls, 2)

	// b2: stale ontoOldBase detected → uses fork-point(main, b2)
	assert.Equal(t, rebaseCall{"default-sha", "main-b2-forkpoint", "b2"}, rebaseCalls[0],
		"b2 should use the reflog fork-point when ontoOldBase is stale")

	// b3: b2's SHA is a valid ancestor → uses it directly
	assert.Equal(t, rebaseCall{"b2", "b2-on-main-sha", "b3"}, rebaseCalls[1],
		"b3 should use b2's original SHA as oldBase (not stale)")
}

// TestRebase_ConflictSavesState verifies that when a rebase conflict occurs,
// the state is saved with the conflict branch and remaining branches.
func TestRebase_ConflictSavesState(t *testing.T) {
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

	mock := newRebaseMock(tmpDir, "b1")
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil } // b1 succeeds
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "b2" {
			return assert.AnError // conflict on b2
		}
		return nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) { return nil, nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, output, "--continue")

	// Verify state file was saved
	stateData, readErr := os.ReadFile(filepath.Join(tmpDir, "gh-stack-rebase-state"))
	require.NoError(t, readErr, "rebase state file should be saved")

	var state rebaseState
	require.NoError(t, json.Unmarshal(stateData, &state))
	assert.Equal(t, "b2", state.ConflictBranch)
	assert.Equal(t, []string{"b3"}, state.RemainingBranches)
	assert.Equal(t, "b1", state.OriginalBranch)
	assert.Contains(t, state.OriginalRefs, "b1")
	assert.Contains(t, state.OriginalRefs, "b2")
	assert.Contains(t, state.OriginalRefs, "b3")
}

// TestRebase_Continue_NoState verifies that --continue without a state file
// produces a "no rebase in progress" message.
func TestRebase_Continue_NoState(t *testing.T) {
	tmpDir := t.TempDir()

	mock := newRebaseMock(tmpDir, "b1")
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--continue"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "no rebase in progress")
}

// TestRebase_Abort_RestoresBranches verifies that --abort restores all branches
// to their original SHAs and removes the state file.
func TestRebase_Abort_RestoresBranches(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create rebase state
	state := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{"b3"},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"b1": "orig-sha-b1",
			"b2": "orig-sha-b2",
			"b3": "orig-sha-b3",
		},
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "gh-stack-rebase-state"), stateData, 0644))

	var resets []resetCall
	var checkouts []string
	currentBranch := "b2" // simulating we're on the conflict branch

	mock := newRebaseMock(tmpDir, currentBranch)
	mock.BranchExistsFn = func(string) (bool, error) { return true, nil }
	mock.CurrentBranchFn = func() (string, error) { return currentBranch, nil }
	mock.CheckoutBranchFn = func(name string) error {
		checkouts = append(checkouts, name)
		currentBranch = name
		return nil
	}
	mock.ResetHardFn = func(ref string) error {
		resets = append(resets, resetCall{currentBranch, ref})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--abort"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Rebase aborted and branches restored")

	// Verify each branch was reset to its original SHA.
	// Map iteration order is non-deterministic, so collect into a map.
	resetMap := make(map[string]string)
	for _, r := range resets {
		resetMap[r.branch] = r.sha
	}
	assert.Equal(t, "orig-sha-b1", resetMap["b1"])
	assert.Equal(t, "orig-sha-b2", resetMap["b2"])
	assert.Equal(t, "orig-sha-b3", resetMap["b3"])

	// State file should be removed
	_, err = os.Stat(filepath.Join(tmpDir, "gh-stack-rebase-state"))
	assert.True(t, os.IsNotExist(err), "state file should be removed after abort")

	// Should return to original branch
	assert.Contains(t, checkouts, "b1", "should checkout original branch at end")
}

// TestRebase_DownstackOnly verifies that --downstack only rebases branches
// from trunk to the current branch (inclusive).
func TestRebase_DownstackOnly(t *testing.T) {
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

	var allRebaseCalls []rebaseCall
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--downstack"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	// b2 is at index 1, so downstack = [b1, b2] (indices 0..1)
	require.Len(t, allRebaseCalls, 2, "downstack should rebase b1 and b2 only")
	assert.Equal(t, "sha-main", allRebaseCalls[0].newBase, "b1 should be rebased onto the pinned trunk")
	assert.Equal(t, "b1", allRebaseCalls[1].newBase, "b2 should be rebased onto b1")
}

// TestRebase_UpstackOnly verifies that --upstack only rebases branches
// from the current branch to the top.
func TestRebase_UpstackOnly(t *testing.T) {
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

	var allRebaseCalls []rebaseCall
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--upstack"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	// b2 is at index 1, upstack = [b2, b3] (indices 1..2)
	require.Len(t, allRebaseCalls, 2, "upstack should rebase b2 and b3")
	assert.Equal(t, "b1", allRebaseCalls[0].newBase, "b2 should be rebased onto b1")
	assert.Equal(t, "b2", allRebaseCalls[1].newBase, "b3 should be rebased onto b2")
}

// TestRebase_UpstackWithMergedBranchBelow verifies that --upstack pre-seeds
// --onto state when a merged branch exists immediately below the rebase range.
func TestRebase_UpstackWithMergedBranchBelow(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var allRebaseCalls []rebaseCall
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--upstack"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	// b2 is at index 1, upstack = [b2, b3]. b1 is merged below.
	// b2 should use --onto because b1 was merged.
	require.Len(t, allRebaseCalls, 2, "upstack should rebase b2 and b3")

	// b2: --onto rebase with b1's old SHA as old base
	assert.Equal(t, "sha-main", allRebaseCalls[0].newBase, "b2 should be rebased onto the pinned main (first non-merged ancestor)")
	assert.Equal(t, "sha-b1", allRebaseCalls[0].oldBase, "b2 should use b1's original SHA as old base")
	assert.Equal(t, "b2", allRebaseCalls[0].branch, "b2 should be the branch being rebased")

	// b3: --onto continues to propagate
	assert.Equal(t, "b2", allRebaseCalls[1].newBase, "b3 should be rebased onto b2")
	assert.NotEmpty(t, allRebaseCalls[1].oldBase, "b3 should also use --onto")
}

// TestRebase_SkipsMergedBranches verifies that merged branches are skipped
// with an appropriate message.
func TestRebase_SkipsMergedBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", Head: "sha-b1", PullRequest: &stack.PullRequestRef{Number: 42, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b2")
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")
	assert.Contains(t, output, "PR #42 merged")

	// Only b2 should be rebased
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, "b2", rebaseCalls[0].branch)
}

// queuedPRClient returns a MockClient whose FindPRByNumber reports the given PR
// numbers as queued (in a merge queue, open, not merged) and finds no PR by
// branch name. Used to drive the transient Queued state through syncStackPRs in
// rebase/sync tests.
func queuedPRClient(headByNumber map[int]string) *github.MockClient {
	return &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			head, ok := headByNumber[n]
			if !ok {
				return nil, nil
			}
			return &github.PullRequest{
				Number:          n,
				HeadRefName:     head,
				State:           "OPEN",
				Merged:          false,
				MergeQueueEntry: &github.MergeQueueEntry{ID: fmt.Sprintf("MQ_%d", n)},
			}, nil
		},
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
	}
}

// TestRebase_QueuedBranch_DownstreamStaysStacked verifies the #144 fix: a queued
// PR is NOT treated as merged. Its branch is skipped (frozen in the merge queue),
// but downstream branches stay stacked on top of it — they rebase onto the queued
// branch, not --onto trunk with the queued commits dropped.
func TestRebase_QueuedBranch_DownstreamStaysStacked(t *testing.T) {
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

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = queuedPRClient(map[int]string{10: "b1"})
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")
	assert.Contains(t, output, "queued")
	assert.NotContains(t, output, "adjusted for merged PR",
		"queued branches must not trigger the merged --onto path")

	// b2 stays stacked on the queued b1 (not rebased --onto main); b3 onto b2.
	require.Len(t, rebaseCalls, 2)
	assert.Equal(t, rebaseCall{"b1", "sha-b1", "b2"}, rebaseCalls[0],
		"b2 should rebase onto the queued branch b1, keeping its commits")
	assert.Equal(t, rebaseCall{"b2", "sha-b2", "b3"}, rebaseCalls[1],
		"b3 should rebase onto b2")
}

// TestRebase_MergedBelowQueued_KeepsStackedOnQueued verifies that when a merged
// branch sits below a queued branch, the branch above the queued one stays
// stacked on the queued branch. The queued branch is frozen and still carries the
// merged branch's commits, so downstream cannot drop them via --onto.
func TestRebase_MergedBelowQueued_KeepsStackedOnQueued(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b3")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = queuedPRClient(map[int]string{11: "b2"})
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")
	assert.Contains(t, output, "PR #10 merged")
	assert.Contains(t, output, "Skipping b2")
	assert.Contains(t, output, "queued")

	// b1 merged and b2 queued are both skipped. b3 stays stacked on the queued
	// b2 — it must NOT be rebased --onto main (which would drop b2's + b1's
	// commits while b2 is frozen).
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, rebaseCall{"b2", "sha-b2", "b3"}, rebaseCalls[0],
		"b3 should rebase onto the queued b2, not --onto main")
	assert.NotContains(t, output, "adjusted for merged PR")
}

// TestRebase_UpstackAboveQueuedBranch verifies the onto-seed fix: with --upstack
// starting just above a queued branch, the first in-range branch rebases normally
// onto the queued predecessor rather than dropping its commits via --onto.
func TestRebase_UpstackAboveQueuedBranch(t *testing.T) {
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

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = queuedPRClient(map[int]string{10: "b1"})
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--upstack"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	// upstack from b2 = [b2, b3]; b1 (queued) is below the range.
	require.Len(t, rebaseCalls, 2)
	assert.Equal(t, rebaseCall{"b1", "sha-b1", "b2"}, rebaseCalls[0],
		"b2 should rebase onto the queued predecessor b1, not --onto main")
	assert.Equal(t, rebaseCall{"b2", "sha-b2", "b3"}, rebaseCalls[1],
		"b3 should rebase onto b2")
	assert.NotContains(t, output, "adjusted for merged PR")
}

// TestRebase_StateRoundTrip verifies that rebase state can be saved and loaded
// back with all fields preserved, including the --onto fields.
func TestRebase_StateRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	original := &rebaseState{
		CurrentBranchIndex: 2,
		ConflictBranch:     "feature-b",
		RemainingBranches:  []string{"feature-c", "feature-d"},
		OriginalBranch:     "feature-a",
		OriginalRefs: map[string]string{
			"feature-a": "aaa111",
			"feature-b": "bbb222",
			"feature-c": "ccc333",
			"feature-d": "ddd444",
		},
		UseOnto:     true,
		OntoOldBase: "bbb222",
	}

	err := saveRebaseState(tmpDir, original)
	require.NoError(t, err)

	loaded, err := loadRebaseState(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, original.CurrentBranchIndex, loaded.CurrentBranchIndex)
	assert.Equal(t, original.ConflictBranch, loaded.ConflictBranch)
	assert.Equal(t, original.RemainingBranches, loaded.RemainingBranches)
	assert.Equal(t, original.OriginalBranch, loaded.OriginalBranch)
	assert.Equal(t, original.OriginalRefs, loaded.OriginalRefs)
	assert.Equal(t, original.UseOnto, loaded.UseOnto)
	assert.Equal(t, original.OntoOldBase, loaded.OntoOldBase)
}

func TestRebase_StateReadErrors(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "invalid JSON"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, rebaseStateFile)
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(path, 0700))
			case "invalid JSON":
				require.NoError(t, os.WriteFile(path, []byte("{incomplete"), 0600))
			}
			got, err := loadRebaseState(dir)
			require.Error(t, err)
			assert.Nil(t, got)
			switch kind {
			case "missing":
				assert.ErrorIs(t, err, os.ErrNotExist)
			case "directory":
				var pathErr *os.PathError
				require.ErrorAs(t, err, &pathErr)
				assert.Equal(t, path, pathErr.Path)
			case "invalid JSON":
				var syntaxErr *json.SyntaxError
				assert.ErrorAs(t, err, &syntaxErr)
			}
		})
	}
}

func TestRebase_StateConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	state := &rebaseState{OriginalBranch: "initial", ConflictBranch: "initial"}
	require.NoError(t, saveRebaseState(dir, state))
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
				got, err := loadRebaseState(dir)
				if err != nil {
					errs <- err
					return
				}
				if got == nil || got.OriginalBranch == "" || got.OriginalBranch != got.ConflictBranch {
					errs <- fmt.Errorf("reader observed an incomplete rebase state")
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
		if writeErr = saveRebaseState(dir, state); writeErr != nil {
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
	got, err := loadRebaseState(dir)
	require.NoError(t, err)
	assert.Equal(t, state, got)
}

// TestRebase_Continue_RebasesRemainingBranches verifies the --continue success
// path: RebaseContinue is called, remaining branches are rebased via RebaseOnto,
// the state file is cleaned up, and the original branch is restored.
func TestRebase_Continue_RebasesRemainingBranches(t *testing.T) {
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

	// State: b2 had a conflict (index 1), b3 remains to be rebased.
	state := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{"b3"},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"main": "main-orig-sha",
			"b1":   "b1-orig-sha",
			"b2":   "b2-orig-sha",
			"b3":   "b3-orig-sha",
		},
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "gh-stack-rebase-state"), stateData, 0644))

	var rebaseContinueCalled bool
	var rebaseCalls []rebaseCall
	var checkouts []string

	mock := newRebaseMock(tmpDir, "b2")
	mock.IsRebaseInProgressFn = func() (bool, error) { return !rebaseContinueCalled, nil }
	mock.RebaseContinueFn = func(opts git.RebaseOpts) error {
		rebaseContinueCalled = true
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.CheckoutBranchFn = func(name string) error {
		checkouts = append(checkouts, name)
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

	assert.NoError(t, err)
	assert.True(t, rebaseContinueCalled, "RebaseContinue should be called")

	// b3 is at idx 2 (idx > 0, not UseOnto) → RebaseOnto(base=b2, originalRefs[b2], b3)
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, rebaseCall{"b2", "b2-orig-sha", "b3"}, rebaseCalls[0])

	// State file should be removed after success
	_, statErr := os.Stat(filepath.Join(tmpDir, "gh-stack-rebase-state"))
	assert.True(t, os.IsNotExist(statErr), "state file should be removed after success")

	// Original branch should be checked out at the end
	assert.Contains(t, checkouts, "b1", "should checkout original branch")
}

// TestRebase_Continue_QueuedBranchBelowConflict verifies that a queued branch is
// still skipped when the cascade resumes via --continue after a conflict below
// it. The Queued flag is transient and lost when continueRebase reloads the
// stack from disk, so it must be refreshed before the remaining cascade — else
// the frozen merge-queue branch would be rebased.
func TestRebase_Continue_QueuedBranchBelowConflict(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 20}},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// State: b1 (below the queued b2) conflicted; b2 and b3 remain.
	state := &rebaseState{
		CurrentBranchIndex: 0,
		ConflictBranch:     "b1",
		RemainingBranches:  []string{"b2", "b3"},
		OriginalBranch:     "b3",
		OriginalRefs: map[string]string{
			"main": "main-orig-sha",
			"b1":   "sha-b1",
			"b2":   "sha-b2",
			"b3":   "sha-b3",
		},
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "gh-stack-rebase-state"), stateData, 0644))

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b1")
	mock.BranchExistsFn = func(name string) (bool, error) { return true, nil }
	inProgress := true
	mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
	mock.RebaseContinueFn = func(opts git.RebaseOpts) error { inProgress = false; return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = queuedPRClient(map[int]string{20: "b2"})
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--continue"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b2")
	assert.Contains(t, output, "queued")

	// Only b3 is rebased, onto the queued b2. The queued b2 itself must not be
	// rebased (its branch is frozen in the merge queue).
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, rebaseCall{"b2", "sha-b2", "b3"}, rebaseCalls[0])
	for _, c := range rebaseCalls {
		assert.NotEqual(t, "b2", c.branch, "the frozen queued branch must not be rebased")
	}
}

// TestRebase_Continue_OntoMode verifies the --continue path when UseOnto is
// set (merged branches upstream). With no remaining branches, only
// RebaseContinue runs and the state is cleaned up.
func TestRebase_Continue_OntoMode(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11, Merged: true}},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// b3 was the conflict branch; no remaining branches after it.
	state := &rebaseState{
		CurrentBranchIndex: 2,
		ConflictBranch:     "b3",
		RemainingBranches:  []string{},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"main": "sha-main",
			"b1":   "sha-b1",
			"b2":   "sha-b2",
			"b3":   "sha-b3",
		},
		UseOnto:     true,
		OntoOldBase: "sha-b2",
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "gh-stack-rebase-state"), stateData, 0644))

	var rebaseContinueCalled bool

	mock := newRebaseMock(tmpDir, "b3")
	mock.IsRebaseInProgressFn = func() (bool, error) { return !rebaseContinueCalled, nil }
	mock.RebaseContinueFn = func(opts git.RebaseOpts) error {
		rebaseContinueCalled = true
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--continue"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.True(t, rebaseContinueCalled, "RebaseContinue should be called")

	// State file should be removed after success
	_, statErr := os.Stat(filepath.Join(tmpDir, "gh-stack-rebase-state"))
	assert.True(t, os.IsNotExist(statErr), "state file should be removed after success")
}

// TestRebase_Continue_ConflictOnRemaining verifies that when --continue
// successfully resolves the first conflict but hits a new conflict on a
// remaining branch, the state is updated and ErrConflict is returned.
func TestRebase_Continue_ConflictOnRemaining(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
			{Branch: "b4"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	state := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{"b3", "b4"},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"main": "sha-main",
			"b1":   "sha-b1",
			"b2":   "sha-b2",
			"b3":   "sha-b3",
			"b4":   "sha-b4",
		},
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "gh-stack-rebase-state"), stateData, 0644))

	mock := newRebaseMock(tmpDir, "b2")
	mock.IsRebaseInProgressFn = func() (bool, error) { return true, nil }
	mock.RebaseContinueFn = func(opts git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "b3" {
			return assert.AnError // conflict on b3
		}
		return nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) { return nil, nil }
	mock.CheckoutBranchFn = func(string) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--continue"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, output, "--continue")

	// State file should still exist with updated conflict info
	updatedData, readErr := os.ReadFile(filepath.Join(tmpDir, "gh-stack-rebase-state"))
	require.NoError(t, readErr, "state file should still exist after new conflict")

	var updatedState rebaseState
	require.NoError(t, json.Unmarshal(updatedData, &updatedState))
	assert.Equal(t, "b3", updatedState.ConflictBranch)
	assert.Equal(t, []string{"b4"}, updatedState.RemainingBranches)
}

// TestRebase_Abort_WithActiveRebase verifies that --abort calls RebaseAbort
// when a git rebase is in progress, restores branches, and cleans up the state.
func TestRebase_Abort_WithActiveRebase(t *testing.T) {
	tmpDir := t.TempDir()

	state := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"b1": "orig-sha-b1",
			"b2": "orig-sha-b2",
		},
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "gh-stack-rebase-state"), stateData, 0644))

	var rebaseAbortCalled bool
	inProgress := true
	var resets []resetCall
	var checkouts []string
	currentBranch := "b2"

	mock := newRebaseMock(tmpDir, currentBranch)
	mock.BranchExistsFn = func(string) (bool, error) { return true, nil }
	mock.CurrentBranchFn = func() (string, error) { return currentBranch, nil }
	mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
	mock.RebaseAbortFn = func() error {
		rebaseAbortCalled = true
		inProgress = false
		return nil
	}
	mock.CheckoutBranchFn = func(name string) error {
		checkouts = append(checkouts, name)
		currentBranch = name
		return nil
	}
	mock.ResetHardFn = func(ref string) error {
		resets = append(resets, resetCall{currentBranch, ref})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--abort"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.True(t, rebaseAbortCalled, "RebaseAbort should be called when rebase is in progress")
	assert.Contains(t, output, "Rebase aborted and branches restored")

	// Verify branches restored to original SHAs
	resetMap := make(map[string]string)
	for _, r := range resets {
		resetMap[r.branch] = r.sha
	}
	assert.Equal(t, "orig-sha-b1", resetMap["b1"])
	assert.Equal(t, "orig-sha-b2", resetMap["b2"])

	// State file should be removed
	_, statErr := os.Stat(filepath.Join(tmpDir, "gh-stack-rebase-state"))
	assert.True(t, os.IsNotExist(statErr), "state file should be removed after abort")

	// Should return to original branch
	assert.Contains(t, checkouts, "b1", "should checkout original branch at end")
}

// TestRebase_FastForwardsBranchFromRemote verifies that when origin/b1 is ahead
// of local b1 (someone pushed a new commit), the branch is fast-forwarded before
// the cascade rebase so downstream branches include the new commits.
func TestRebase_FastForwardsBranchFromRemote(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var allRebaseCalls []rebaseCall
	var updateBranchRefCalls []struct{ branch, sha string }

	mock := newRebaseMock(tmpDir, "b2")
	// b1 is behind origin/b1 (remote has new commit)
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "b1" {
			return "b1-local-sha", nil
		}
		if ref == "origin/b1" {
			return "b1-remote-sha", nil
		}
		// trunk and origin/trunk same — trunk already up to date
		if ref == "main" || ref == "origin/main" {
			return "main-sha", nil
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
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	// b1 should be fast-forwarded to remote SHA
	require.Len(t, updateBranchRefCalls, 1, "should fast-forward b1 via UpdateBranchRef")
	assert.Equal(t, "b1", updateBranchRefCalls[0].branch)
	assert.Equal(t, "b1-remote-sha", updateBranchRefCalls[0].sha)

	assert.Contains(t, output, "Fast-forwarded b1")

	// Cascade rebase should still occur
	assert.NotEmpty(t, allRebaseCalls, "cascade rebase should still happen")
}

// TestRebase_BranchAlreadyUpToDate_NoFF verifies that when a branch's local
// and remote SHAs match, no fast-forward occurs.
func TestRebase_BranchAlreadyUpToDate_NoFF(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var updateBranchRefCalls int
	var mergeFFCalls int

	mock := newRebaseMock(tmpDir, "b1")
	// Same SHA for b1 and origin/b1 — already up to date (default mock handles this)
	mock.UpdateBranchRefFn = func(string, string) error {
		updateBranchRefCalls++
		return nil
	}
	mock.MergeFFFn = func(string) error {
		mergeFFCalls++
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	assert.Equal(t, 0, updateBranchRefCalls, "no UpdateBranchRef for branches already up to date")
	assert.Equal(t, 0, mergeFFCalls, "no MergeFF for branches already up to date")
}

// TestRebase_BranchDiverged_NoFF verifies that when local and remote branches
// have diverged (e.g., after a previous local rebase), no fast-forward occurs.
func TestRebase_BranchDiverged_NoFF(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var updateBranchRefCalls int

	mock := newRebaseMock(tmpDir, "b1")
	// Different SHAs for b1 and origin/b1
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "b1" {
			return "b1-local-sha", nil
		}
		if ref == "origin/b1" {
			return "b1-remote-sha", nil
		}
		if ref == "main" || ref == "origin/main" {
			return "main-sha", nil
		}
		return "sha-" + ref, nil
	}
	// Neither is ancestor of the other — diverged
	mock.IsAncestorFn = func(a, d string) (bool, error) {
		if (a == "b1-local-sha" && d == "b1-remote-sha") ||
			(a == "b1-remote-sha" && d == "b1-local-sha") {
			return false, nil
		}
		return true, nil
	}
	mock.UpdateBranchRefFn = func(string, string) error {
		updateBranchRefCalls++
		return nil
	}
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	assert.Equal(t, 0, updateBranchRefCalls, "no FF when branches have diverged")
}

func TestRebase_SkipsMergedBranchesNotExistingLocally(t *testing.T) {
	// Simulates a stack where b1 is merged and its branch was auto-deleted
	// from the remote, so it doesn't exist locally. The stored Head SHA is
	// used as ontoOldBase for the next branch's --onto rebase.
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", Head: "b1-stored-head-sha", PullRequest: &stack.PullRequestRef{Number: 42, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) (bool, error) {
		// b1 does not exist locally (deleted from remote after merge)
		return name != "b1", nil
	}
	mock.RevParseMultiFn = func(refs []string) ([]string, error) {
		// Only resolve refs that exist — b1 should not be in the list
		shas := make([]string, len(refs))
		for i, r := range refs {
			if r == "b1" {
				t.Fatalf("RevParseMulti should not be called with non-existent branch b1")
			}
			shas[i] = "sha-" + r
		}
		return shas, nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "Skipping b1")

	// Only b2 should be rebased, and the rebase should use b1's stored
	// Head SHA as oldBase so `git rebase --onto` receives valid arguments.
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, "b2", rebaseCalls[0].branch)
	assert.Equal(t, "sha-main", rebaseCalls[0].newBase)
	assert.Equal(t, "b1-stored-head-sha", rebaseCalls[0].oldBase)
}

// TestRebase_CommitterDateIsAuthorDate verifies that when
// --committer-date-is-author-date is passed, it is forwarded to all rebase
// calls in the cascade.
func TestRebase_CommitterDateIsAuthorDate(t *testing.T) {
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

	var receivedOpts []git.RebaseOpts
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		receivedOpts = append(receivedOpts, opts)
		_ = currentCheckedOut
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		receivedOpts = append(receivedOpts, opts)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--committer-date-is-author-date"})
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "rebased locally")

	// All 3 rebase calls should have CommitterDateIsAuthorDate set.
	require.Len(t, receivedOpts, 3)
	for i, opts := range receivedOpts {
		assert.True(t, opts.CommitterDateIsAuthorDate,
			"rebase call %d should have CommitterDateIsAuthorDate=true", i)
	}
}

// TestRebase_PreserveDatesAlias verifies that --preserve-dates is an alias
// for --committer-date-is-author-date.
func TestRebase_PreserveDatesAlias(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var receivedOpts []git.RebaseOpts

	mock := newRebaseMock(tmpDir, "b1")
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		receivedOpts = append(receivedOpts, opts)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--preserve-dates"})
	err := cmd.Execute()

	assert.NoError(t, err)
	require.Len(t, receivedOpts, 1)
	assert.True(t, receivedOpts[0].CommitterDateIsAuthorDate,
		"--preserve-dates should set CommitterDateIsAuthorDate=true")
}

// TestRebase_StateRoundTrip_CommitterDateIsAuthorDate verifies that
// CommitterDateIsAuthorDate is persisted and restored in rebase state.
func TestRebase_StateRoundTrip_CommitterDateIsAuthorDate(t *testing.T) {
	tmpDir := t.TempDir()

	original := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{"b3"},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"b1": "sha-b1",
			"b2": "sha-b2",
			"b3": "sha-b3",
		},
		CommitterDateIsAuthorDate: true,
	}

	err := saveRebaseState(tmpDir, original)
	require.NoError(t, err)

	loaded, err := loadRebaseState(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, true, loaded.CommitterDateIsAuthorDate)
}

// TestRebase_Continue_PreservesCommitterDateFlag verifies that --continue
// restores the committer-date-is-author-date flag from saved state and
// passes it to subsequent rebase calls.
func TestRebase_Continue_PreservesCommitterDateFlag(t *testing.T) {
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

	// State: b2 had a conflict, b3 remains. Flag was set.
	state := &rebaseState{
		CurrentBranchIndex: 1,
		ConflictBranch:     "b2",
		RemainingBranches:  []string{"b3"},
		OriginalBranch:     "b1",
		OriginalRefs: map[string]string{
			"main": "main-orig-sha",
			"b1":   "b1-orig-sha",
			"b2":   "b2-orig-sha",
			"b3":   "b3-orig-sha",
		},
		CommitterDateIsAuthorDate: true,
	}
	require.NoError(t, saveRebaseState(tmpDir, state))

	var continueCalled bool
	var continueOpts git.RebaseOpts
	var rebaseOntoOpts []git.RebaseOpts

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.IsRebaseInProgressFn = func() (bool, error) { return continueCalled == false, nil }
	mock.RebaseContinueFn = func(opts git.RebaseOpts) error {
		continueCalled = true
		continueOpts = opts
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		rebaseOntoOpts = append(rebaseOntoOpts, opts)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--continue"})
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.True(t, continueCalled)
	assert.True(t, continueOpts.CommitterDateIsAuthorDate,
		"RebaseContinue should receive CommitterDateIsAuthorDate=true from saved state")
	require.Len(t, rebaseOntoOpts, 1)
	assert.True(t, rebaseOntoOpts[0].CommitterDateIsAuthorDate,
		"remaining cascade rebase should receive CommitterDateIsAuthorDate=true from saved state")
}

// TestRebase_ConflictSavesCommitterDateFlag verifies that when a conflict
// occurs with --committer-date-is-author-date active, the flag is persisted
// in the saved state.
func TestRebase_ConflictSavesCommitterDateFlag(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newRebaseMock(tmpDir, "b1")
	mock.CheckoutBranchFn = func(string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		return nil // b1 succeeds
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "b2" {
			return fmt.Errorf("conflict")
		}
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--committer-date-is-author-date"})
	_ = cmd.Execute()

	// Load the saved state and verify the flag is persisted.
	loaded, err := loadRebaseState(tmpDir)
	require.NoError(t, err)
	assert.True(t, loaded.CommitterDateIsAuthorDate,
		"saved rebase state should preserve CommitterDateIsAuthorDate flag")
}

// TestRebase_NoTrunk_SkipsTrunkRebase verifies that --no-trunk skips rebasing
// branch 1 onto trunk but still cascades inter-branch rebases.
func TestRebase_NoTrunk_SkipsTrunkRebase(t *testing.T) {
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

	var allRebaseCalls []rebaseCall
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
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
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)

	// Only b2 onto b1 and b3 onto b2 — no rebase onto trunk (main).
	require.Len(t, allRebaseCalls, 2, "should only rebase b2 and b3 (skip b1 onto trunk)")
	assert.Equal(t, "b1", allRebaseCalls[0].newBase, "b2 should be rebased onto b1")
	assert.Equal(t, "b2", allRebaseCalls[1].newBase, "b3 should be rebased onto b2")

	assert.Contains(t, output, "without trunk")
}

// TestRebase_NoTrunk_SkipsFetch verifies that --no-trunk does not call Fetch.
func TestRebase_NoTrunk_SkipsFetch(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	fetchCalled := false

	mock := newRebaseMock(tmpDir, "b1")
	mock.CheckoutBranchFn = func(name string) error { return nil }
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error { return nil }
	mock.FetchFn = func(remote string) error {
		fetchCalled = true
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--no-trunk"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	assert.False(t, fetchCalled, "Fetch should not be called with --no-trunk")
}

// TestRebase_NoTrunk_SingleBranch verifies that --no-trunk with a single-branch
// stack has no branches to rebase (since branch 1 onto trunk is skipped).
func TestRebase_NoTrunk_SingleBranch(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newRebaseMock(tmpDir, "b1")
	mock.CheckoutBranchFn = func(name string) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--no-trunk"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "No branches to rebase")
}

// TestRebase_NoTrunk_WithUpstack verifies --no-trunk combined with --upstack
// when the current branch is above index 0. The --no-trunk should not change
// behavior since --upstack already starts from a non-trunk branch.
func TestRebase_NoTrunk_WithUpstack(t *testing.T) {
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

	var allRebaseCalls []rebaseCall
	var currentCheckedOut string

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error {
		currentCheckedOut = name
		return nil
	}
	mock.RebaseFn = func(base string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--no-trunk", "--upstack"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()

	assert.NoError(t, err)
	// --upstack from b2 = [b2, b3], --no-trunk doesn't change this since startIdx is already 1
	require.Len(t, allRebaseCalls, 2, "upstack should rebase b2 and b3")
	assert.Equal(t, "b1", allRebaseCalls[0].newBase, "b2 should be rebased onto b1")
	assert.Equal(t, "b2", allRebaseCalls[1].newBase, "b3 should be rebased onto b2")
}

// TestRebase_NoTrunk_ConflictSavesState verifies that --no-trunk persists the
// NoTrunk flag in the rebase state when a conflict occurs.
func TestRebase_NoTrunk_ConflictSavesState(t *testing.T) {
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

	mock := newRebaseMock(tmpDir, "b2")
	mock.CheckoutBranchFn = func(name string) error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "b2" {
			return fmt.Errorf("conflict")
		}
		return nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) { return nil, nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := RebaseCmd(cfg)
	cmd.SetArgs([]string{"--no-trunk"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	_ = cmd.Execute()

	// Load the saved state and verify the NoTrunk flag is persisted.
	loaded, err := loadRebaseState(tmpDir)
	require.NoError(t, err)
	assert.True(t, loaded.NoTrunk,
		"saved rebase state should preserve NoTrunk flag")
}

func TestResolveRebaseOldBase(t *testing.T) {
	t.Run("uses current parent tip when the branch contains it", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: func(ancestor, branch string) (bool, error) {
				return ancestor == "current-parent" && branch == "child", nil
			},
		})
		defer restore()

		oldBase, err := resolveRebaseOldBase("current-parent", "recorded-base", "parent", "child")
		require.NoError(t, err)
		assert.Equal(t, "current-parent", oldBase)
	})

	t.Run("uses recorded base after the parent was rewritten", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: func(ancestor, branch string) (bool, error) {
				return ancestor == "recorded-base" && branch == "child", nil
			},
		})
		defer restore()

		oldBase, err := resolveRebaseOldBase("amended-parent", "recorded-base", "parent", "child")
		require.NoError(t, err)
		assert.Equal(t, "recorded-base", oldBase)
	})

	t.Run("uses fork point when metadata was already corrupted", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: func(ancestor, branch string) (bool, error) {
				return ancestor == "old-parent" && branch == "child", nil
			},
			MergeBaseForkPointFn: func(ref, branch string) (string, error) {
				return "old-parent", nil
			},
		})
		defer restore()

		oldBase, err := resolveRebaseOldBase("amended-parent", "amended-parent", "parent", "child")
		require.NoError(t, err)
		assert.Equal(t, "old-parent", oldBase)
	})

	t.Run("fails when no safe boundary can be recovered", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: func(string, string) (bool, error) { return false, nil },
			MergeBaseForkPointFn: func(string, string) (string, error) {
				return "", errors.New("no fork point")
			},
		})
		defer restore()

		_, err := resolveRebaseOldBase("amended-parent", "amended-parent", "parent", "child")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rebase this branch manually")
	})
}

type amendedParentRepo struct {
	dir       string
	gitDir    string
	oldParent string
	newParent string
}

func issue250Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s:\n%s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func issue250GitMayFail(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	return cmd.Run()
}

func issue250WriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

func setupAmendedParentRepo(t *testing.T, corruptBase bool) amendedParentRepo {
	t.Helper()
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	cloneDir := filepath.Join(t.TempDir(), "clone")

	issue250Git(t, ".", "-c", "safe.bareRepository=all", "init", "--bare", "-b", "main", remoteDir)
	issue250Git(t, ".", "clone", remoteDir, cloneDir)
	issue250Git(t, cloneDir, "config", "user.name", "Test")
	issue250Git(t, cloneDir, "config", "user.email", "test@example.com")

	issue250WriteFile(t, cloneDir, "base.txt", "base\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "base")
	issue250Git(t, cloneDir, "push", "-u", "origin", "main")
	mainSHA := issue250Git(t, cloneDir, "rev-parse", "main")

	issue250Git(t, cloneDir, "checkout", "-b", "parent")
	issue250WriteFile(t, cloneDir, "old-parent.txt", "old parent\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "parent old")
	oldParent := issue250Git(t, cloneDir, "rev-parse", "parent")
	issue250Git(t, cloneDir, "push", "-u", "origin", "parent")

	issue250Git(t, cloneDir, "checkout", "-b", "child")
	issue250WriteFile(t, cloneDir, "child.txt", "child\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "child commit")
	childSHA := issue250Git(t, cloneDir, "rev-parse", "child")
	issue250Git(t, cloneDir, "push", "-u", "origin", "child")

	gitDir := filepath.Join(cloneDir, ".git")
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main", Head: mainSHA},
		Branches: []stack.BranchRef{
			{Branch: "parent", Head: oldParent, Base: mainSHA},
			{Branch: "child", Head: childSHA, Base: oldParent},
		},
	}
	writeStackFile(t, gitDir, s)

	issue250Git(t, cloneDir, "checkout", "parent")
	issue250Git(t, cloneDir, "rm", "old-parent.txt")
	issue250WriteFile(t, cloneDir, "new-parent.txt", "new parent\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "--amend", "-m", "parent amended")
	newParent := issue250Git(t, cloneDir, "rev-parse", "parent")

	if corruptBase {
		issue250Git(t, cloneDir, "push", "--force", "origin", "parent")
		s.Branches[0].Head = newParent
		s.Branches[1].Base = newParent
		writeStackFile(t, gitDir, s)
	}
	issue250Git(t, cloneDir, "checkout", "child")

	return amendedParentRepo{
		dir:       cloneDir,
		gitDir:    gitDir,
		oldParent: oldParent,
		newParent: newParent,
	}
}

func issue250TestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}
	t.Cleanup(func() {
		_ = cfg.Out.Close()
		_ = cfg.Err.Close()
		_ = outR.Close()
		_ = errR.Close()
	})
	return cfg
}

func withIssue250Repo(t *testing.T, dir string) {
	t.Helper()
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
}

func assertIssue250History(t *testing.T, repo amendedParentRepo) {
	t.Helper()
	subjects := strings.Split(issue250Git(t, repo.dir, "log", "--format=%s", "main..child"), "\n")
	assert.Equal(t, []string{"child commit", "parent amended"}, subjects)
	assert.Error(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", repo.oldParent, "child"))
	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", repo.newParent, "child"))
	_, oldErr := os.Stat(filepath.Join(repo.dir, "old-parent.txt"))
	assert.True(t, os.IsNotExist(oldErr))
	_, newErr := os.Stat(filepath.Join(repo.dir, "new-parent.txt"))
	assert.NoError(t, newErr)
}

func TestIntegration_AmendedParentPushThenRebase(t *testing.T) {
	repo := setupAmendedParentRepo(t, false)
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)

	require.NoError(t, runPush(cfg, &pushOptions{remote: "origin"}))

	sf, err := stack.Load(repo.gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, repo.oldParent, sf.Stacks[0].Branches[1].Base,
		"push must not replace the child's valid base with an amended parent tip")

	require.NoError(t, runRebase(cfg, &rebaseOptions{remote: "origin"}))
	assertIssue250History(t, repo)
}

func TestIntegration_AmendedParentRecoversCorruptedBase(t *testing.T) {
	repo := setupAmendedParentRepo(t, true)
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)

	require.NoError(t, runRebase(cfg, &rebaseOptions{remote: "origin"}))
	assertIssue250History(t, repo)
}

func TestIntegration_AmendedParentWithoutForkPointFailsSafely(t *testing.T) {
	repo := setupAmendedParentRepo(t, true)
	issue250Git(t, repo.dir, "reflog", "expire", "--expire=now", "--all")
	require.Error(t, issue250GitMayFail(t, repo.dir, "merge-base", "--fork-point", "parent", "child"))

	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)
	parentBefore := issue250Git(t, repo.dir, "rev-parse", "parent")
	childBefore := issue250Git(t, repo.dir, "rev-parse", "child")

	err := runRebase(cfg, &rebaseOptions{remote: "origin"})
	require.Error(t, err)
	assert.Equal(t, parentBefore, issue250Git(t, repo.dir, "rev-parse", "parent"))
	assert.Equal(t, childBefore, issue250Git(t, repo.dir, "rev-parse", "child"))
}

func TestIntegration_AdoptedBranchRebasesFromCommonAncestor(t *testing.T) {
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	cloneDir := filepath.Join(t.TempDir(), "clone")

	issue250Git(t, ".", "-c", "safe.bareRepository=all", "init", "--bare", "-b", "main", remoteDir)
	issue250Git(t, ".", "clone", remoteDir, cloneDir)
	issue250Git(t, cloneDir, "config", "user.name", "Test")
	issue250Git(t, cloneDir, "config", "user.email", "test@example.com")

	issue250WriteFile(t, cloneDir, "base.txt", "base\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "base")
	issue250Git(t, cloneDir, "push", "-u", "origin", "main")
	mainSHA := issue250Git(t, cloneDir, "rev-parse", "main")

	issue250Git(t, cloneDir, "checkout", "-b", "parent")
	issue250WriteFile(t, cloneDir, "parent.txt", "parent\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "parent commit")
	parentSHA := issue250Git(t, cloneDir, "rev-parse", "parent")

	issue250Git(t, cloneDir, "checkout", "-b", "imported", "main")
	issue250WriteFile(t, cloneDir, "imported-one.txt", "one\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "imported one")
	issue250WriteFile(t, cloneDir, "imported-two.txt", "two\n")
	issue250Git(t, cloneDir, "add", ".")
	issue250Git(t, cloneDir, "commit", "-m", "imported two")

	gitDir := filepath.Join(cloneDir, ".git")
	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main", Head: mainSHA},
		Branches: []stack.BranchRef{
			{Branch: "parent", Head: parentSHA, Base: mainSHA},
		},
	})

	issue250Git(t, cloneDir, "checkout", "parent")
	withIssue250Repo(t, cloneDir)
	cfg := issue250TestConfig(t)

	require.NoError(t, runAdd(cfg, &addOptions{}, []string{"imported"}))

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	require.Len(t, sf.Stacks[0].Branches, 2)
	assert.Equal(t, mainSHA, sf.Stacks[0].Branches[1].Base,
		"adopting a separate main-based branch should record main as its old boundary")

	require.NoError(t, runRebase(cfg, &rebaseOptions{remote: "origin"}))

	subjects := strings.Split(issue250Git(t, cloneDir, "log", "--format=%s", "main..imported"), "\n")
	assert.Equal(t, []string{"imported two", "imported one", "parent commit"}, subjects)
	require.NoError(t, issue250GitMayFail(t, cloneDir, "merge-base", "--is-ancestor", "parent", "imported"))
}

type worktreeRebaseRepo struct {
	amendedParentRepo
	parentDir string
	childDir  string
}

func setupWorktreeRebaseRepo(t *testing.T, conflict bool) worktreeRebaseRepo {
	t.Helper()
	repo := setupAmendedParentRepo(t, false)
	issue250Git(t, repo.dir, "config", "commit.gpgSign", "false")
	issue250Git(t, repo.dir, "config", "core.hooksPath", os.DevNull)
	issue250Git(t, repo.dir, "checkout", "main")
	parentDir := filepath.Join(t.TempDir(), "parent worktree")
	childDir := filepath.Join(t.TempDir(), "child worktree")
	issue250Git(t, repo.dir, "worktree", "add", parentDir, "parent")
	issue250Git(t, repo.dir, "worktree", "add", childDir, "child")
	if conflict {
		issue250Git(t, repo.dir, "commit", "--allow-empty", "-m", "advance trunk")
		issue250Git(t, repo.dir, "push", "origin", "main")
		issue250WriteFile(t, parentDir, "base.txt", "parent change\n")
		issue250Git(t, parentDir, "add", "base.txt")
		issue250Git(t, parentDir, "commit", "--amend", "--no-edit")
		issue250WriteFile(t, childDir, "base.txt", "child change\n")
		issue250Git(t, childDir, "add", "base.txt")
		issue250Git(t, childDir, "commit", "-m", "child conflict")
	}
	return worktreeRebaseRepo{repo, parentDir, childDir}
}

func setupSharedTrunkRebaseRepo(t *testing.T, conflict bool) worktreeRebaseRepo {
	t.Helper()
	repo := setupWorktreeRebaseRepo(t, conflict)
	issue250Git(t, repo.childDir, "checkout", "--detach")
	issue250Git(t, repo.dir, "branch", "independent", "main")
	sf, err := stack.Load(repo.gitDir)
	require.NoError(t, err)
	sf.AddStack(stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "independent"}}})
	require.NoError(t, stack.Save(repo.gitDir, sf))
	return repo
}

func TestRebase_SharedTrunkSelectionPreservesOriginAndRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts rebaseOptions
		want []string
	}{
		{name: "whole stack", want: []string{"b1", "b2", "b3"}},
		{name: "upstack from trunk", opts: rebaseOptions{upstack: true}, want: []string{"b1", "b2", "b3"}},
		{name: "downstack from trunk", opts: rebaseOptions{downstack: true}, want: []string{"b1"}},
		{name: "explicit upstack", opts: rebaseOptions{branch: "b2", upstack: true}, want: []string{"b2", "b3"}},
		{name: "explicit downstack", opts: rebaseOptions{branch: "b2", downstack: true}, want: []string{"b1", "b2"}},
		{name: "without trunk", opts: rebaseOptions{noTrunk: true}, want: []string{"b2", "b3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStackFileMulti(t, dir,
				stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}}},
				stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "independent"}}},
			)
			current := "main"
			var rebased []string
			mock := newRebaseMock(dir, current)
			mock.CurrentBranchFn = func() (string, error) { return current, nil }
			mock.BranchExistsFn = func(string) (bool, error) { return true, nil }
			mock.CheckoutBranchFn = func(branch string) error { current = branch; return nil }
			mock.RebaseFn = func(string, git.RebaseOpts) error { rebased = append(rebased, current); return nil }
			mock.RebaseOntoFn = func(_, _, branch string, _ git.RebaseOpts) error {
				rebased = append(rebased, branch)
				return nil
			}
			mock.IsRerereEnabledFn = func() (bool, error) { return true, nil }
			restore := git.SetOps(mock)
			defer restore()
			cfg := issue250TestConfig(t)
			cfg.ForceInteractive = true
			cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 0, nil }
			tc.opts.remote = "origin"

			require.NoError(t, runRebase(cfg, &tc.opts))

			assert.Equal(t, tc.want, rebased)
			assert.Equal(t, "main", current)
			assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
		})
	}
}

func TestRebase_SharedTrunkSelectionRecoveryPreservesOrigin(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(fmt.Sprintf("abort=%t", abort), func(t *testing.T) {
			repo := setupSharedTrunkRebaseRepo(t, true)
			observerHead := issue250Git(t, repo.childDir, "rev-parse", "HEAD")
			withIssue250Repo(t, repo.dir)
			cfg := issue250TestConfig(t)
			cfg.ForceInteractive = true
			selections := 0
			cfg.SelectFn = func(_, _ string, _ []string) (int, error) { selections++; return 0, nil }
			cfg.ConfirmFn = func(string, bool) (bool, error) { return false, nil }
			require.ErrorIs(t, runRebase(cfg, &rebaseOptions{remote: "origin"}), ErrConflict)
			state, err := loadRebaseState(repo.gitDir)
			require.NoError(t, err)
			assert.Equal(t, "main", state.OriginalBranch)
			if !abort {
				issue250WriteFile(t, repo.dir, "base.txt", "resolved\n")
				issue250Git(t, repo.dir, "add", "base.txt")
			}
			withIssue250Repo(t, repo.parentDir)

			require.NoError(t, runRebase(cfg, &rebaseOptions{abort: abort, cont: !abort}))

			assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
			assert.Equal(t, "parent", issue250Git(t, repo.parentDir, "branch", "--show-current"))
			assert.Equal(t, observerHead, issue250Git(t, repo.childDir, "rev-parse", "HEAD"))
			assert.Equal(t, 1, selections)
			assert.NoFileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
		})
	}
}

func TestRebase_WorktreesPreserveCheckouts(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, false)
	issue250WriteFile(t, repo.dir, "unrelated.txt", "leave main alone\n")
	nested := filepath.Join(repo.childDir, "nested")
	require.NoError(t, os.MkdirAll(nested, 0755))
	withIssue250Repo(t, nested)
	cfg := issue250TestConfig(t)

	require.NoError(t, runRebase(cfg, &rebaseOptions{remote: "origin"}))

	assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
	assert.Equal(t, "parent", issue250Git(t, repo.parentDir, "branch", "--show-current"))
	assert.Equal(t, "child", issue250Git(t, repo.childDir, "branch", "--show-current"))
	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "parent", "child"))
	assert.Error(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", repo.oldParent, "child"))
	data, err := os.ReadFile(filepath.Join(repo.dir, "unrelated.txt"))
	require.NoError(t, err)
	assert.Equal(t, "leave main alone\n", string(data))
	sf, err := stack.Load(repo.gitDir)
	require.NoError(t, err)
	assert.Equal(t, issue250Git(t, repo.dir, "rev-parse", "parent"), sf.Stacks[0].Branches[1].Base)
	_, err = os.Stat(filepath.Join(repo.gitDir, rebaseStateFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRebase_WorktreesDirtyTargetFailsBeforeMutation(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, false)
	issue250WriteFile(t, repo.parentDir, "uncommitted.txt", "preserve me\n")
	parentBefore := issue250Git(t, repo.dir, "rev-parse", "parent")
	childBefore := issue250Git(t, repo.dir, "rev-parse", "child")
	withIssue250Repo(t, repo.childDir)
	cfg := issue250TestConfig(t)

	assert.ErrorIs(t, runRebase(cfg, &rebaseOptions{remote: "origin"}), ErrSilent)

	assert.Equal(t, parentBefore, issue250Git(t, repo.dir, "rev-parse", "parent"))
	assert.Equal(t, childBefore, issue250Git(t, repo.dir, "rev-parse", "child"))
	assert.FileExists(t, filepath.Join(repo.parentDir, "uncommitted.txt"))
	_, err := os.Stat(filepath.Join(repo.gitDir, rebaseStateFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRebase_WorktreesConflictContinueFromDifferentWorktree(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, true)
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)
	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{remote: "origin"}), ErrConflict)

	state, err := loadRebaseState(repo.gitDir)
	require.NoError(t, err)
	require.NotNil(t, state.Worktrees)
	assert.True(t, worktree.SamePath(repo.childDir, state.Worktrees.Location("child").Path))
	assert.False(t, requireGitState(t, git.IsRebaseInProgress), "the initiating main worktree must remain usable")
	assert.True(t, requireGitState(t, requireWorktree(t, git.CurrentOps(), repo.childDir).IsRebaseInProgress))

	issue250WriteFile(t, repo.childDir, "base.txt", "resolved\n")
	issue250Git(t, repo.childDir, "add", "base.txt")
	withIssue250Repo(t, repo.parentDir)
	require.NoError(t, runRebase(cfg, &rebaseOptions{cont: true}))

	assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
	assert.Equal(t, "parent", issue250Git(t, repo.parentDir, "branch", "--show-current"))
	assert.Equal(t, "child", issue250Git(t, repo.childDir, "branch", "--show-current"))
	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "parent", "child"))
	_, err = os.Stat(filepath.Join(repo.gitDir, rebaseStateFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRebase_RecoveryFindsMovedOrigin(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, true)
	issue250Git(t, repo.parentDir, "checkout", "--detach")
	childBefore := issue250Git(t, repo.dir, "rev-parse", "child")
	withIssue250Repo(t, repo.childDir)
	cfg := issue250TestConfig(t)
	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{noTrunk: true}), ErrConflict)

	withIssue250Repo(t, repo.dir)
	moved := filepath.Join(t.TempDir(), "moved child")
	issue250Git(t, repo.dir, "worktree", "move", repo.childDir, moved)
	assert.True(t, requireGitState(t, requireWorktree(t, git.CurrentOps(), moved).IsRebaseInProgress))

	require.NoError(t, runRebase(cfg, &rebaseOptions{abort: true}))
	assert.False(t, requireGitState(t, requireWorktree(t, git.CurrentOps(), moved).IsRebaseInProgress))
	assert.Equal(t, childBefore, issue250Git(t, moved, "rev-parse", "child"))
	assert.Equal(t, "child", issue250Git(t, moved, "branch", "--show-current"))
	assert.NoFileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
}

func TestRebase_WorktreesAbortRetainsRecoveryForNewEdits(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, true)
	parentBefore := issue250Git(t, repo.dir, "rev-parse", "parent")
	childBefore := issue250Git(t, repo.dir, "rev-parse", "child")
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)
	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{remote: "origin"}), ErrConflict)
	parentRebased := issue250Git(t, repo.dir, "rev-parse", "parent")
	require.NotEqual(t, parentBefore, parentRebased)
	issue250WriteFile(t, repo.parentDir, "new-work.txt", "new work after conflict\n")

	withIssue250Repo(t, repo.childDir)
	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{abort: true}), ErrSilent)
	assert.Equal(t, parentRebased, issue250Git(t, repo.dir, "rev-parse", "parent"))
	assert.Equal(t, childBefore, issue250Git(t, repo.dir, "rev-parse", "child"))
	assert.FileExists(t, filepath.Join(repo.parentDir, "new-work.txt"))
	assert.FileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))

	require.NoError(t, os.Remove(filepath.Join(repo.parentDir, "new-work.txt")))
	require.NoError(t, runRebase(cfg, &rebaseOptions{abort: true}))
	assert.Equal(t, parentBefore, issue250Git(t, repo.dir, "rev-parse", "parent"))
	assert.Equal(t, childBefore, issue250Git(t, repo.dir, "rev-parse", "child"))
	assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
	_, err := os.Stat(filepath.Join(repo.gitDir, rebaseStateFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRebase_WorktreesUpstackDoesNotRequireDirtyLowerWorktree(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, false)
	parentBefore := issue250Git(t, repo.dir, "rev-parse", "parent")
	issue250WriteFile(t, repo.parentDir, "unfinished.txt", "keep working\n")
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)

	require.NoError(t, runRebase(cfg, &rebaseOptions{branch: "child", upstack: true, noTrunk: true}))

	assert.Equal(t, parentBefore, issue250Git(t, repo.dir, "rev-parse", "parent"))
	assert.FileExists(t, filepath.Join(repo.parentDir, "unfinished.txt"))
	assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "parent", "child"))
}

func TestRebase_WorktreesExplicitTargetRecoverySelectsCorrectStack(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, true)
	issue250Git(t, repo.dir, "checkout", "-b", "independent", "main")
	sf, err := stack.Load(repo.gitDir)
	require.NoError(t, err)
	sf.AddStack(stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "independent"}}})
	require.NoError(t, stack.Save(repo.gitDir, sf))
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)

	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{branch: "child", upstack: true, remote: "origin"}), ErrConflict)
	issue250WriteFile(t, repo.childDir, "base.txt", "resolved\n")
	issue250Git(t, repo.childDir, "add", "base.txt")
	require.NoError(t, runRebase(cfg, &rebaseOptions{cont: true}))

	assert.Equal(t, "independent", issue250Git(t, repo.dir, "branch", "--show-current"))
	sf, err = stack.Load(repo.gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 2)
	assert.Equal(t, []string{"parent", "child"}, sf.Stacks[0].BranchNames())
	assert.Equal(t, []string{"independent"}, sf.Stacks[1].BranchNames())
	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "parent", "child"))
}

func TestRebase_ContinueWithRemainingBranchInConflictWorktree(t *testing.T) {
	repo := setupWorktreeRebaseRepo(t, true)
	issue250Git(t, repo.dir, "worktree", "remove", repo.parentDir)
	issue250Git(t, repo.dir, "worktree", "remove", repo.childDir)
	issue250Git(t, repo.dir, "branch", "grandchild", "child")
	sf, err := stack.Load(repo.gitDir)
	require.NoError(t, err)
	child := issue250Git(t, repo.dir, "rev-parse", "child")
	sf.Stacks[0].Branches = append(sf.Stacks[0].Branches, stack.BranchRef{Branch: "grandchild", Head: child, Base: child})
	require.NoError(t, stack.Save(repo.gitDir, sf))
	withIssue250Repo(t, repo.dir)
	cfg := issue250TestConfig(t)

	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{remote: "origin"}), ErrConflict)
	issue250WriteFile(t, repo.dir, "base.txt", "resolved\n")
	issue250Git(t, repo.dir, "add", "base.txt")
	require.NoError(t, runRebase(cfg, &rebaseOptions{cont: true}))

	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "child", "grandchild"))
	assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
	assert.False(t, requireGitState(t, git.IsRebaseInProgress))
	_, err = os.Stat(filepath.Join(repo.gitDir, rebaseStateFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRebase_LegacyPrivateRecoveryKeepsOriginalCatalog(t *testing.T) {
	common := t.TempDir()
	private := filepath.Join(common, "worktrees", "legacy")
	require.NoError(t, os.MkdirAll(private, 0755))
	writeStackFile(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "unrelated"}}})
	writeStackFile(t, private, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1", Head: "old-b1"}}})
	before, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	require.NoError(t, err)
	require.NoError(t, saveRebaseState(private, &rebaseState{
		OriginalBranch: "b1", ConflictBranch: "b1", OriginalRefs: map[string]string{"b1": "old-b1"},
		TrunkRef: "main", TrunkSHA: "sha-main", EndIndex: 1,
	}))
	mock := newRebaseMock(private, "b1")
	mock.CommonDirFn = func() (string, error) { return common, nil }
	restore := git.SetOps(mock)
	defer restore()

	require.NoError(t, runRebase(issue250TestConfig(t), &rebaseOptions{cont: true}))

	after, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	sf, err := stack.Load(private)
	require.NoError(t, err)
	assert.Equal(t, "sha-b1", sf.Stacks[0].Branches[0].Head)
	assert.FileExists(t, filepath.Join(private, "gh-stack"))
	assert.NoFileExists(t, filepath.Join(private, rebaseStateFile))
	assert.NoFileExists(t, filepath.Join(common, "gh-stack-migration"))
}

func TestRebase_AbortRetainsPartialRestore(t *testing.T) {
	dir := t.TempDir()
	s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}}}
	writeStackFile(t, dir, s)
	current := "b1"
	refs := map[string]string{"b1": "rebased-b1", "b2": "rebased-b2"}
	fail := true
	mock := newRebaseMock(dir, current)
	mock.CurrentBranchFn = func() (string, error) { return current, nil }
	mock.RevParseFn = func(ref string) (string, error) { return refs[ref], nil }
	mock.CheckoutBranchFn = func(branch string) error { current = branch; return nil }
	mock.ResetHardFn = func(sha string) error { refs[current] = sha; return nil }
	mock.UpdateBranchRefFn = func(branch, sha string) error {
		if branch == "b2" && fail {
			return errors.New("ref restore failed")
		}
		refs[branch] = sha
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()
	ctx, err := worktree.New()
	require.NoError(t, err)
	ctx.Touched = map[string]string{"b1": "rebased-b1", "b2": "rebased-b2"}
	state := newWorktreeRebaseState(&s, ctx, "b1", map[string]string{"b1": "old-b1", "b2": "old-b2"}, trunkTarget{}, 0, 2)
	state.CurrentBranchIndex = 2
	require.NoError(t, saveRebaseState(dir, state))
	cfg := issue250TestConfig(t)

	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{abort: true}), ErrSilent)
	retained, err := loadRebaseState(dir)
	require.NoError(t, err)
	assert.Equal(t, "restoring", retained.Phase)
	assert.Equal(t, map[string]string{"b2": "rebased-b2"}, retained.Worktrees.Touched)
	assert.Equal(t, "old-b1", refs["b1"])
	assert.Equal(t, "rebased-b2", refs["b2"])

	fail = false
	require.NoError(t, runRebase(cfg, &rebaseOptions{abort: true}))
	assert.Equal(t, "old-b2", refs["b2"])
	assert.Equal(t, "b1", current)
	assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
}

func TestRebase_CompletedJournalSurvivesCatalogSaveFailure(t *testing.T) {
	dir := t.TempDir()
	s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}}}
	writeStackFile(t, dir, s)
	mock := newRebaseMock(dir, "b1")
	mock.RebaseContinueFn = func(git.RebaseOpts) error { t.Fatal("must not repeat a completed native rebase"); return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { t.Fatal("must not repeat a completed rebase"); return nil }
	restore := git.SetOps(mock)
	defer restore()
	ctx, err := worktree.New()
	require.NoError(t, err)
	ctx.Touched["b1"] = "sha-b1"
	state := newWorktreeRebaseState(&s, ctx, "b1", map[string]string{"b1": "old-b1"}, trunkTarget{Ref: "main", SHA: "sha-main"}, 0, 1)
	state.CurrentBranchIndex = state.EndIndex
	require.NoError(t, saveRebaseState(dir, state))
	lock, err := stack.Lock(dir)
	require.NoError(t, err)
	defer lock.Unlock()
	oldTimeout := stack.LockTimeout
	stack.LockTimeout = 10 * time.Millisecond
	defer func() { stack.LockTimeout = oldTimeout }()
	cfg := issue250TestConfig(t)

	require.ErrorIs(t, runRebase(cfg, &rebaseOptions{cont: true}), ErrLockFailed)
	retained, err := loadRebaseState(dir)
	require.NoError(t, err)
	assert.Equal(t, "complete", retained.Phase)
	assert.Equal(t, map[string]string{"b1": "sha-b1"}, retained.Worktrees.Touched)

	lock.Unlock()
	require.NoError(t, runRebase(cfg, &rebaseOptions{cont: true}))
	assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
}

func TestRebase_CompletedRecoveryPreservesNewWork(t *testing.T) {
	for _, action := range []string{"continue", "abort"} {
		for _, busy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/busy=%t", action, busy), func(t *testing.T) {
				dir := t.TempDir()
				s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}}}
				writeStackFile(t, dir, s)
				mock := newRebaseMock(dir, "b1")
				mock.IsRebaseInProgressFn = func() (bool, error) { return busy, nil }
				mock.HasUncommittedChangesFn = func() (bool, error) { return !busy, nil }
				mock.RebaseAbortFn = func() error { t.Fatal("must not abort a new Git operation"); return nil }
				mock.RebaseContinueFn = func(git.RebaseOpts) error { t.Fatal("must not continue a new Git operation"); return nil }
				forbidRewriteMutations(t, mock)
				restore := git.SetOps(mock)
				defer restore()
				ctx, err := worktree.New()
				require.NoError(t, err)
				ctx.Touched["b1"] = "sha-b1"
				state := newWorktreeRebaseState(&s, ctx, "b1", map[string]string{"b1": "old-b1"}, trunkTarget{}, 0, 1)
				state.Phase, state.CurrentBranchIndex = "complete", state.EndIndex
				require.NoError(t, saveRebaseState(dir, state))

				err = runRebase(issue250TestConfig(t), &rebaseOptions{cont: action == "continue", abort: action == "abort"})
				if action == "abort" {
					require.ErrorIs(t, err, ErrSilent)
					retained, err := loadRebaseState(dir)
					require.NoError(t, err)
					assert.Equal(t, "restoring", retained.Phase)
					assert.Equal(t, ctx.Touched, retained.Worktrees.Touched)
				} else {
					require.NoError(t, err)
					assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
				}
				assert.Equal(t, busy, requireGitState(t, mock.IsRebaseInProgress))
				dirty, err := mock.HasUncommittedChanges()
				require.NoError(t, err)
				assert.Equal(t, !busy, dirty)
			})
		}
	}
}

func forbidRewriteMutations(t *testing.T, mock *git.MockOps) {
	t.Helper()
	mock.CheckoutBranchFn = func(string) error { t.Fatal("unexpected checkout"); return nil }
	mock.CreateBranchFn = func(string, string) error { t.Fatal("unexpected branch creation"); return nil }
	mock.UpdateBranchRefFn = func(string, string) error { t.Fatal("unexpected ref update"); return nil }
	mock.ResetHardFn = func(string) error { t.Fatal("unexpected reset"); return nil }
	mock.MergeFFFn = func(string) error { t.Fatal("unexpected fast-forward"); return nil }
	mock.RebaseFn = func(string, git.RebaseOpts) error { t.Fatal("unexpected rebase"); return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
		t.Fatal("unexpected rebase onto")
		return nil
	}
	mock.PushFn = func(string, []string, bool, bool) error { t.Fatal("unexpected push"); return nil }
	mock.DeleteBranchFn = func(string, bool) error { t.Fatal("unexpected branch deletion"); return nil }
	mock.DeleteTrackingRefFn = func(string, string) error { t.Fatal("unexpected tracking ref deletion"); return nil }
}

func TestRebase_RecoveryRejectsDifferentExecutionLifecycle(t *testing.T) {
	actions := []struct {
		name string
		run  func(*config.Config, string) error
		want error
	}{
		{"continue", func(cfg *config.Config, _ string) error { return runRebase(cfg, &rebaseOptions{cont: true}) }, ErrRebaseActive},
		{"abort", func(cfg *config.Config, _ string) error { return runRebase(cfg, &rebaseOptions{abort: true}) }, ErrRebaseActive},
		{"sync", func(cfg *config.Config, _ string) error { return runSync(cfg, &syncOptions{}) }, ErrRebaseActive},
		{"direct continue", continueRebase, ErrSilent},
		{"direct abort", abortRebase, ErrSilent},
	}
	for _, mode := range []string{originOnlyRebaseMode, "future-lifecycle"} {
		for _, withContext := range []bool{false, true} {
			for _, phase := range []string{"applying", "conflict", "complete", "restoring"} {
				for _, action := range actions {
					t.Run(fmt.Sprintf("%s/context=%t/%s/%s", mode, withContext, phase, action.name), func(t *testing.T) {
						dir := t.TempDir()
						private := filepath.Join(dir, "worktrees", "caller")
						require.NoError(t, os.MkdirAll(private, 0755))
						writeStackFile(t, dir, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}}})
						writeStackFile(t, private, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "independent"}}})
						mock := newRebaseMock(private, "b1")
						mock.CommonDirFn = func() (string, error) { return dir, nil }
						forbidRewriteMutations(t, mock)
						mock.CurrentBranchFn = func() (string, error) { t.Fatal("must reject before reading native checkout state"); return "", nil }
						mock.IsRebaseInProgressFn = func() (bool, error) { t.Fatal("must reject before reading native rebase state"); return false, nil }
						mock.ForWorktreeFn = func(string) (git.Ops, error) {
							t.Fatal("must reject before resolving a recorded worktree")
							return nil, nil
						}
						mock.RebaseAbortFn = func() error { t.Fatal("must not abort an incompatible lifecycle"); return nil }
						mock.RebaseContinueFn = func(git.RebaseOpts) error { t.Fatal("must not continue an incompatible lifecycle"); return nil }
						restore := git.SetOps(mock)
						defer restore()
						state := &rebaseState{
							ExecutionMode: mode, Phase: phase, OriginalBranch: "b1", ConflictBranch: "b1",
							OriginalRefs: map[string]string{"b1": "before"}, EndIndex: 1,
						}
						origin := filepath.Join(dir, "recorded-origin")
						if withContext {
							state.Worktrees = &worktree.Context{Origin: worktree.Location{Path: origin, ID: "."}}
						}
						require.NoError(t, saveRebaseState(dir, state))
						paths := []string{filepath.Join(dir, rebaseStateFile), filepath.Join(dir, "gh-stack"), filepath.Join(private, "gh-stack")}
						before := make([][]byte, len(paths))
						for i, path := range paths {
							var err error
							before[i], err = os.ReadFile(path)
							require.NoError(t, err)
						}
						_, err := loadRebaseState(dir)
						require.Error(t, err)
						cfg, outR, errR := config.NewTestConfig()

						require.ErrorIs(t, action.run(cfg, dir), action.want)

						_, output := commandOutput(t, cfg, outR, errR)
						assert.NotContains(t, output, "no rebase in progress")
						if mode == originOnlyRebaseMode {
							assert.Contains(t, output, "matching layer3 gh-stack build")
						} else {
							assert.Contains(t, output, `unsupported rebase execution mode "future-lifecycle"`)
						}
						assert.Contains(t, output, "continue or abort")
						assert.Contains(t, output, "recovery state was retained")
						if withContext {
							assert.Contains(t, output, fmt.Sprintf("%q", origin))
						}
						for i, path := range paths {
							after, err := os.ReadFile(path)
							require.NoError(t, err)
							assert.Equal(t, before[i], after, path)
						}
						assert.Nil(t, cfg.StackMutation)
						assert.NoFileExists(t, filepath.Join(dir, "gh-stack-migration"))
						assert.NoFileExists(t, filepath.Join(dir, "gh-stack.pre-worktree-migration"))
						assert.NoFileExists(t, filepath.Join(private, "gh-stack.pre-worktree-migration"))
					})
				}
			}
		}
	}
}

func TestLoadRebaseState_UnmarkedLifecycles(t *testing.T) {
	for _, withContext := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%t", withContext), func(t *testing.T) {
			dir := t.TempDir()
			s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}}}
			var ctx *worktree.Context
			if withContext {
				ctx = &worktree.Context{Origin: worktree.Location{Path: dir, ID: "."}}
			}
			state := newWorktreeRebaseState(&s, ctx, "b1", map[string]string{"b1": "before"}, trunkTarget{}, 0, 1)
			require.NoError(t, saveRebaseState(dir, state))

			loaded, err := loadRebaseState(dir)

			require.NoError(t, err)
			assert.Equal(t, state, loaded)
			data, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
			require.NoError(t, err)
			assert.NotContains(t, string(data), "executionMode")
		})
	}
}

func TestRebase_LegacyContinuePersistsCatalogUnderOperationLock(t *testing.T) {
	dir := t.TempDir()
	writeStackFile(t, dir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1", Head: "old-b1", Base: "old-base"}},
	})
	require.NoError(t, saveRebaseState(dir, &rebaseState{
		OriginalBranch: "b1", ConflictBranch: "b1", OriginalRefs: map[string]string{"b1": "old-b1"},
		TrunkRef: "main", TrunkSHA: "sha-main", EndIndex: 1,
	}))
	mock := newRebaseMock(dir, "b1")
	mock.RevParseFn = func(ref string) (string, error) {
		if ref == "b1" {
			return "new-b1", nil
		}
		return "sha-main", nil
	}
	mock.IsAncestorFn = func(a, d string) (bool, error) { return a == "sha-main" && d == "b1", nil }
	restore := git.SetOps(mock)
	defer restore()
	cfg := issue250TestConfig(t)

	require.NoError(t, runRebase(cfg, &rebaseOptions{cont: true}))

	sf, err := stack.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "new-b1", sf.Stacks[0].Branches[0].Head)
	assert.Equal(t, "sha-main", sf.Stacks[0].Branches[0].Base)
	_, err = os.Stat(filepath.Join(dir, rebaseStateFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRebase_LegacyPhaseGuards(t *testing.T) {
	for _, tc := range []struct {
		phase string
		abort bool
	}{
		{phase: "applying"},
		{phase: "restoring"},
		{phase: "unknown"},
		{phase: "unknown", abort: true},
	} {
		t.Run(fmt.Sprintf("%s/abort=%t", tc.phase, tc.abort), func(t *testing.T) {
			dir := t.TempDir()
			writeStackFile(t, dir, stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}},
			})
			state := &rebaseState{
				Phase: tc.phase, OriginalBranch: "b1", ConflictBranch: "b1",
				OriginalRefs: map[string]string{"b1": "before"},
			}
			require.NoError(t, saveRebaseState(dir, state))
			before, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
			require.NoError(t, err)
			mock := newRebaseMock(dir, "b1")
			forbidRewriteMutations(t, mock)
			mock.IsRebaseInProgressFn = func() (bool, error) {
				t.Error("phase rejection must precede native state queries")
				return false, nil
			}
			mock.RebaseContinueFn = func(git.RebaseOpts) error { t.Error("unexpected native continuation"); return nil }
			mock.RebaseAbortFn = func() error { t.Error("unexpected native abort"); return nil }
			restore := git.SetOps(mock)
			defer restore()

			err = runRebase(issue250TestConfig(t), &rebaseOptions{cont: !tc.abort, abort: tc.abort})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.phase)
			if tc.phase != "unknown" {
				assert.Contains(t, err.Error(), "gh stack rebase --abort")
			}
			after, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestRebase_LegacyCompleteOnlyPublishes(t *testing.T) {
	dir := t.TempDir()
	writeStackFile(t, dir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})
	state := &rebaseState{
		Phase: "complete", OriginalBranch: "b1", ConflictBranch: "b1",
		RemainingBranches: []string{"b2"}, OriginalRefs: map[string]string{"b1": "old-b1", "b2": "old-b2"},
	}
	require.NoError(t, saveRebaseState(dir, state))
	mock := newRebaseMock(dir, "b1")
	forbidRewriteMutations(t, mock)
	mock.IsRebaseInProgressFn = func() (bool, error) { return true, nil }
	mock.RebaseContinueFn = func(git.RebaseOpts) error {
		t.Error("must not continue a new unrelated native operation")
		return assert.AnError
	}
	restore := git.SetOps(mock)
	defer restore()

	require.NoError(t, runRebase(issue250TestConfig(t), &rebaseOptions{cont: true}))

	assert.True(t, requireGitState(t, mock.IsRebaseInProgress))
	assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
}

func TestRebase_LegacyCompleteAbortPreservesNewNativeOperation(t *testing.T) {
	dir := t.TempDir()
	state := &rebaseState{
		Phase: "complete", OriginalBranch: "b1", OriginalRefs: map[string]string{"b1": "before"},
	}
	require.NoError(t, saveRebaseState(dir, state))
	before, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
	require.NoError(t, err)
	mock := newRebaseMock(dir, "b1")
	forbidRewriteMutations(t, mock)
	mock.IsRebaseInProgressFn = func() (bool, error) { return true, nil }
	mock.RebaseAbortFn = func() error {
		t.Fatal("must not abort a native operation started after completion")
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	require.Error(t, runRebase(issue250TestConfig(t), &rebaseOptions{abort: true}))

	after, err := os.ReadFile(filepath.Join(dir, rebaseStateFile))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestRebase_LegacyAbortRetainsFailures(t *testing.T) {
	for _, failure := range []string{"native abort", "branch checkout", "reset", "original checkout", "branch lookup"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			refs := map[string]string{"b1": "new-b1", "b2": "new-b2"}
			state := &rebaseState{
				Phase: "conflict", OriginalBranch: "main", ConflictBranch: "b2",
				OriginalRefs: map[string]string{"b1": "old-b1", "b2": "old-b2"},
			}
			require.NoError(t, saveRebaseState(dir, state))
			current, failing := "b2", true
			inProgress := failure == "native abort"
			mock := newRebaseMock(dir, current)
			mock.CurrentBranchFn = func() (string, error) { return current, nil }
			mock.BranchExistsFn = func(branch string) (bool, error) {
				if failing && failure == "branch lookup" && branch == "b2" {
					return false, assert.AnError
				}
				return true, nil
			}
			mock.RevParseFn = func(ref string) (string, error) { return refs[ref], nil }
			mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
			mock.RebaseAbortFn = func() error {
				if failing && failure == "native abort" {
					return assert.AnError
				}
				inProgress = false
				return nil
			}
			mock.CheckoutBranchFn = func(branch string) error {
				if failing && ((failure == "branch checkout" && branch == "b2") || (failure == "original checkout" && branch == "main")) {
					return assert.AnError
				}
				current = branch
				return nil
			}
			mock.ResetHardFn = func(sha string) error {
				if failing && failure == "reset" && current == "b2" {
					return assert.AnError
				}
				refs[current] = sha
				return nil
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg := issue250TestConfig(t)

			require.Error(t, runRebase(cfg, &rebaseOptions{abort: true}))
			retained, err := loadRebaseState(dir)
			require.NoError(t, err, "failure must retain recovery state")
			assert.Equal(t, state.OriginalRefs, retained.OriginalRefs)
			if failure == "branch lookup" {
				assert.Equal(t, state, retained, "query errors must precede journal changes")
			} else {
				assert.Equal(t, "restoring", retained.Phase)
			}

			failing = false
			require.NoError(t, runRebase(cfg, &rebaseOptions{abort: true}))
			assert.Equal(t, state.OriginalRefs, refs)
			assert.Equal(t, "main", current)
			assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
		})
	}
}

func TestRebase_LegacyContinueRollbackRetainsJournal(t *testing.T) {
	for _, reason := range []string{"cascade error", "verification failure"} {
		t.Run(reason, func(t *testing.T) {
			dir := t.TempDir()
			writeStackFile(t, dir, stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
			})
			state := &rebaseState{
				OriginalBranch: "b1", ConflictBranch: "b1", RemainingBranches: []string{"b2"},
				OriginalRefs: map[string]string{"b1": "old-b1", "b2": "old-b2"},
			}
			require.NoError(t, saveRebaseState(dir, state))
			refs := map[string]string{"b1": "new-b1", "b2": "new-b2"}
			current, failing := "b1", true
			mock := newRebaseMock(dir, current)
			mock.CurrentBranchFn = func() (string, error) { return current, nil }
			mock.BranchExistsFn = func(string) (bool, error) { return true, nil }
			mock.RevParseFn = func(ref string) (string, error) { return refs[ref], nil }
			mock.CheckoutBranchFn = func(branch string) error { current = branch; return nil }
			mock.ResetHardFn = func(sha string) error {
				if failing && current == "b2" {
					return assert.AnError
				}
				refs[current] = sha
				return nil
			}
			mock.RebaseOntoFn = func(_, _, branch string, _ git.RebaseOpts) error {
				current = branch
				if reason == "cascade error" {
					return &git.RebaseStartError{Err: assert.AnError}
				}
				return nil
			}
			mock.IsAncestorFn = func(base, branch string) (bool, error) {
				return !(reason == "verification failure" && base == "main" && branch == "b1"), nil
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg := issue250TestConfig(t)

			require.Error(t, runRebase(cfg, &rebaseOptions{cont: true}))
			retained, err := loadRebaseState(dir)
			require.NoError(t, err)
			assert.Equal(t, "restoring", retained.Phase)
			assert.Equal(t, "old-b1", refs["b1"])
			assert.Equal(t, "new-b2", refs["b2"])

			failing = false
			require.NoError(t, runRebase(cfg, &rebaseOptions{abort: true}))
			assert.Equal(t, state.OriginalRefs, refs)
			assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
		})
	}
}

func TestRebase_LegacyCompletionRetainsRetryState(t *testing.T) {
	for _, failure := range []string{"catalog save", "original checkout"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			writeStackFile(t, dir, stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}},
			})
			require.NoError(t, saveRebaseState(dir, &rebaseState{
				OriginalBranch: "main", ConflictBranch: "b1", OriginalRefs: map[string]string{"b1": "before"},
			}))
			current, failing, inProgress := "b1", true, true
			continues := 0
			mock := newRebaseMock(dir, current)
			mock.CurrentBranchFn = func() (string, error) { return current, nil }
			mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
			mock.RebaseContinueFn = func(git.RebaseOpts) error {
				continues++
				inProgress = false
				return nil
			}
			mock.CheckoutBranchFn = func(branch string) error {
				if failing && failure == "original checkout" {
					return assert.AnError
				}
				current = branch
				return nil
			}
			restore := git.SetOps(mock)
			defer restore()
			oldTimeout := stack.LockTimeout
			stack.LockTimeout = 0
			defer func() { stack.LockTimeout = oldTimeout }()
			var lock *stack.FileLock
			if failure == "catalog save" {
				var err error
				lock, err = stack.Lock(dir)
				require.NoError(t, err)
				defer lock.Unlock()
			}
			cfg := issue250TestConfig(t)

			require.Error(t, runRebase(cfg, &rebaseOptions{cont: true}))
			retained, err := loadRebaseState(dir)
			require.NoError(t, err)
			assert.Equal(t, "complete", retained.Phase)
			assert.Equal(t, 1, continues)
			if lock != nil {
				lock.Unlock()
				inProgress = true
			}
			failing = false

			require.NoError(t, runRebase(cfg, &rebaseOptions{cont: true}))
			assert.Equal(t, 1, continues, "completed publication must not repeat native continuation")
			assert.Equal(t, "main", current)
			assert.NoFileExists(t, filepath.Join(dir, rebaseStateFile))
		})
	}
}
