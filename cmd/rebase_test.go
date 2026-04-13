package cmd

import (
	"encoding/json"
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
		RevParseFn:       func(ref string) (string, error) { return "sha-" + ref, nil },
		IsAncestorFn:    func(a, d string) (bool, error) { return true, nil },
		FetchFn:         func(string) error { return nil },
		EnableRerereFn:  func() error { return nil },
		IsRebaseInProgressFn: func() bool { return false },
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
	mock.RebaseFn = func(base string) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	assert.Equal(t, "main", allRebaseCalls[0].newBase, "b1 should be rebased onto trunk")
	assert.Equal(t, "b1", allRebaseCalls[1].newBase, "b2 should be rebased onto b1")
	assert.Equal(t, "b2", allRebaseCalls[2].newBase, "b3 should be rebased onto b2")

	assert.Contains(t, output, "rebased locally")
}

// TestRebase_SquashMergedBranch_UsesOnto verifies that when b1 has a merged PR,
// it is skipped and b2 uses RebaseOnto with trunk as newBase and b1's original
// SHA as oldBase. b3 also uses --onto (propagation).
func TestRebase_SquashMergedBranch_UsesOnto(t *testing.T) {
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
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.RevParseFn = func(ref string) (string, error) {
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	assert.Equal(t, rebaseCall{"main", "b1-orig-sha", "b2"}, rebaseCalls[0],
		"b2 should rebase --onto main using b1's original SHA as oldBase")
	assert.Equal(t, rebaseCall{"b2", "b2-orig-sha", "b3"}, rebaseCalls[1],
		"b3 should propagate --onto mode with b2's original SHA as oldBase")
}

// TestRebase_OntoPropagatesToSubsequentBranches verifies that when multiple
// branches are squash-merged, --onto propagates correctly through the chain.
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
	mock.BranchExistsFn = func(name string) bool { return true }
	mock.RevParseFn = func(ref string) (string, error) {
		if sha, ok := branchSHAs[ref]; ok {
			return sha, nil
		}
		return "default-sha", nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	assert.Equal(t, rebaseCall{"main", "b2-orig-sha", "b3"}, rebaseCalls[0],
		"b3 should rebase --onto main with b2's SHA as oldBase")
	assert.Equal(t, rebaseCall{"b3", "b3-orig-sha", "b4"}, rebaseCalls[1],
		"b4 should rebase --onto b3 with b3's original SHA as oldBase")
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
	mock.RebaseFn = func(string) error { return nil } // b1 succeeds
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	mock.RebaseFn = func(base string) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	assert.Equal(t, "main", allRebaseCalls[0].newBase, "b1 should be rebased onto trunk")
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
	mock.RebaseFn = func(base string) error {
		allRebaseCalls = append(allRebaseCalls, rebaseCall{newBase: base, oldBase: "", branch: currentCheckedOut})
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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

// TestRebase_SkipsMergedBranches verifies that merged branches are skipped
// with an appropriate message.
func TestRebase_SkipsMergedBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 42, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b2")
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	mock.IsRebaseInProgressFn = func() bool { return true }
	mock.RebaseContinueFn = func() error {
		rebaseContinueCalled = true
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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

// TestRebase_Continue_OntoMode verifies the --continue path when UseOnto is
// set (squash-merged branches upstream). With no remaining branches, only
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
	mock.IsRebaseInProgressFn = func() bool { return true }
	mock.RebaseContinueFn = func() error {
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
	mock.IsRebaseInProgressFn = func() bool { return true }
	mock.RebaseContinueFn = func() error { return nil }
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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
	var resets []resetCall
	var checkouts []string
	currentBranch := "b2"

	mock := newRebaseMock(tmpDir, currentBranch)
	mock.IsRebaseInProgressFn = func() bool { return true }
	mock.RebaseAbortFn = func() error {
		rebaseAbortCalled = true
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

func TestRebase_SkipsMergedBranchesNotExistingLocally(t *testing.T) {
	// Simulates a stack where b1 is merged and its branch was auto-deleted
	// from the remote, so it doesn't exist locally.
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 42, Merged: true}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var rebaseCalls []rebaseCall

	mock := newRebaseMock(tmpDir, "b2")
	mock.BranchExistsFn = func(name string) bool {
		// b1 does not exist locally (deleted from remote after merge)
		return name != "b1"
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
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
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

	// Only b2 should be rebased
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, "b2", rebaseCalls[0].branch)
}
