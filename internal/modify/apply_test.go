package modify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rebaseCall records arguments passed to RebaseOnto.
type rebaseCall struct {
	newBase string
	oldBase string
	branch  string
}

// writeTestStackFile writes a stack file to disk and returns the loaded StackFile
// (with correct checksum for later Save calls).
func writeTestStackFile(t *testing.T, dir string, s stack.Stack) *stack.StackFile {
	t.Helper()
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks:        []stack.Stack{s},
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh-stack"), data, 0644))
	// Reload so the StackFile has the correct loadChecksum for Save.
	loaded, err := stack.Load(dir)
	require.NoError(t, err)
	return loaded
}

// newApplyMock creates a MockOps pre-configured for apply tests.
func newApplyMock(gitDir string, branchSHAs map[string]string) *git.MockOps {
	return &git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(name string) bool { return true },
		RevParseFn: func(ref string) (string, error) {
			if sha, ok := branchSHAs[ref]; ok {
				return sha, nil
			}
			return "sha-" + ref, nil
		},
		IsAncestorFn:        func(a, d string) (bool, error) { return false, nil },
		MergeBaseFn:          func(a, b string) (string, error) { return "merge-base", nil },
		CheckoutBranchFn:     func(string) error { return nil },
		RebaseOntoFn:         func(string, string, string) error { return nil },
		IsRebaseInProgressFn: func() bool { return false },
		RenameBranchFn:       func(string, string) error { return nil },
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return []git.CommitInfo{{SHA: "commit-1"}, {SHA: "commit-2"}}, nil
		},
		CherryPickFn:    func([]string) error { return nil },
		ConflictedFilesFn: func() ([]string, error) { return nil, nil },
		ResetHardFn:     func(string) error { return nil },
		CreateBranchFn:  func(string, string) error { return nil },
		RebaseAbortFn:   func() error { return nil },
	}
}

// makeNodes creates ModifyBranchNodes from a stack for testing.
func makeNodes(s *stack.Stack) []modifyview.ModifyBranchNode {
	nodes := make([]modifyview.ModifyBranchNode, len(s.Branches))
	for i, b := range s.Branches {
		nodes[i] = modifyview.ModifyBranchNode{
			BranchNode: stackview.BranchNode{
				Ref: b,
			},
			OriginalPosition: i,
		}
	}
	return nodes
}

func noopUpdateBaseSHAs(s *stack.Stack) {}

// ─── BuildSnapshot ───────────────────────────────────────────────────────────

func TestBuildSnapshot(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	branchSHAs := map[string]string{
		"A": "sha-aaa",
		"B": "sha-bbb",
	}
	mock := &git.MockOps{
		RevParseFn: func(ref string) (string, error) {
			if sha, ok := branchSHAs[ref]; ok {
				return sha, nil
			}
			return "sha-" + ref, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	snap, err := BuildSnapshot(&s)
	require.NoError(t, err)
	require.Len(t, snap.Branches, 2)

	assert.Equal(t, "A", snap.Branches[0].Name)
	assert.Equal(t, "sha-aaa", snap.Branches[0].TipSHA)
	assert.Equal(t, 0, snap.Branches[0].Position)

	assert.Equal(t, "B", snap.Branches[1].Name)
	assert.Equal(t, "sha-bbb", snap.Branches[1].TipSHA)
	assert.Equal(t, 1, snap.Branches[1].Position)

	// Verify stack metadata round-trips through JSON
	var restored stack.Stack
	require.NoError(t, json.Unmarshal(snap.StackMetadata, &restored))
	assert.Equal(t, "main", restored.Trunk.Branch)
	assert.Equal(t, "A", restored.Branches[0].Branch)
	assert.Equal(t, "B", restored.Branches[1].Branch)
}

// ─── BuildPlan ───────────────────────────────────────────────────────────────

func TestBuildPlan_VariousActions(t *testing.T) {
	t.Run("no changes produces empty plan", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "B"}},
				OriginalPosition: 1,
			},
		}
		plan := BuildPlan(nodes)
		assert.Empty(t, plan)
	})

	t.Run("rename produces rename action", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "B"}},
				OriginalPosition: 1,
			},
		}
		plan := BuildPlan(nodes)
		require.Len(t, plan, 1)
		assert.Equal(t, "rename", plan[0].Type)
		assert.Equal(t, "A", plan[0].Branch)
		assert.Equal(t, "new-A", plan[0].NewName)
	})

	t.Run("move produces move action", func(t *testing.T) {
		// Original order: A(0), B(1), C(2). Desired: A(0), C(1), B(2)
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "C"}},
				OriginalPosition: 2,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "B"}},
				OriginalPosition: 1,
			},
		}
		plan := BuildPlan(nodes)
		// C moved from 2→1, B moved from 1→2
		require.Len(t, plan, 2)
		assert.Equal(t, "move", plan[0].Type)
		assert.Equal(t, "C", plan[0].Branch)
		assert.Equal(t, 1, plan[0].NewPosition)
		assert.Equal(t, "move", plan[1].Type)
		assert.Equal(t, "B", plan[1].Branch)
		assert.Equal(t, 2, plan[1].NewPosition)
	})

	t.Run("removed nodes with drop action not in plan directly", func(t *testing.T) {
		// BuildPlan skips Removed nodes — the drop is recorded by the non-removed
		// logic. But nodes with PendingAction and NOT Removed do get recorded.
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
				Removed:          true,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionDrop},
			},
		}
		plan := BuildPlan(nodes)
		// Removed == true, so it's skipped in BuildPlan
		assert.Empty(t, plan)
	})
}

// ─── ApplyPlan: Drop ─────────────────────────────────────────────────────────

func TestApplyPlan_Drop(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B", PullRequest: &stack.PullRequestRef{Number: 42}},
			{Branch: "C"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
		"C":    "sha-C",
	}

	var rebaseCalls []rebaseCall
	mock := newApplyMock(gitDir, branchSHAs)
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Build nodes: Drop B
	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[1].Removed = true

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// B should be removed from stack
	assert.Equal(t, 2, len(sf.Stacks[0].Branches))
	assert.Equal(t, "A", sf.Stacks[0].Branches[0].Branch)
	assert.Equal(t, "C", sf.Stacks[0].Branches[1].Branch)

	// B's PR should be in DroppedPRs
	require.Len(t, result.DroppedPRs, 1)
	assert.Equal(t, "B", result.DroppedPRs[0].Branch)
	assert.Equal(t, 42, result.DroppedPRs[0].PRNumber)

	// C should be rebased onto A (B's parent), with B's old tip as oldBase
	var cRebase *rebaseCall
	for _, rc := range rebaseCalls {
		if rc.branch == "C" {
			cRebase = &rc
			break
		}
	}
	require.NotNil(t, cRebase, "C should be rebased")
	assert.Equal(t, "A", cRebase.newBase)
}

// ─── ApplyPlan: FoldDown ─────────────────────────────────────────────────────

func TestApplyPlan_FoldDown(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	var cherryPickCalls [][]string
	var checkoutCalls []string

	mock := newApplyMock(gitDir, branchSHAs)
	mock.CheckoutBranchFn = func(name string) error {
		checkoutCalls = append(checkoutCalls, name)
		return nil
	}
	mock.CherryPickFn = func(shas []string) error {
		cherryPickCalls = append(cherryPickCalls, shas)
		return nil
	}
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		if base == "A" && head == "B" {
			return []git.CommitInfo{
				{SHA: "commit-b2"},
				{SHA: "commit-b1"},
			}, nil
		}
		return nil, nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldDown}
	nodes[1].Removed = true

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// CheckoutBranch should be called with "A" (the target below)
	assert.Contains(t, checkoutCalls, "A")

	// CherryPick should be called with B's commit SHAs (reversed for chronological order)
	require.Len(t, cherryPickCalls, 1)
	assert.Equal(t, []string{"commit-b1", "commit-b2"}, cherryPickCalls[0])

	// B should be removed from stack
	assert.Equal(t, 1, len(sf.Stacks[0].Branches))
	assert.Equal(t, "A", sf.Stacks[0].Branches[0].Branch)
}

// ─── ApplyPlan: FoldUp ───────────────────────────────────────────────────────

func TestApplyPlan_FoldUp(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
			{Branch: "C"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
		"C":    "sha-C",
	}

	var cherryPickCalls [][]string
	var rebaseCalls []rebaseCall

	mock := newApplyMock(gitDir, branchSHAs)
	mock.CherryPickFn = func(shas []string) error {
		cherryPickCalls = append(cherryPickCalls, shas)
		return nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
	nodes[1].Removed = true

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// Fold-up should NOT call CherryPick
	assert.Empty(t, cherryPickCalls, "fold-up should not cherry-pick")

	// B should be removed from stack
	assert.Equal(t, 2, len(sf.Stacks[0].Branches))
	assert.Equal(t, "A", sf.Stacks[0].Branches[0].Branch)
	assert.Equal(t, "C", sf.Stacks[0].Branches[1].Branch)

	// C's rebase should use B's base (A's tip) as oldBase, not B's tip.
	// The fold-up adjusts originalParentTips[C] = originalParentTips[B] = sha-A
	var cRebase *rebaseCall
	for _, rc := range rebaseCalls {
		if rc.branch == "C" {
			cRebase = &rc
			break
		}
	}
	require.NotNil(t, cRebase, "C should be rebased")
	assert.Equal(t, "A", cRebase.newBase, "C should rebase onto A (B's parent)")
	assert.Equal(t, "sha-A", cRebase.oldBase, "C should use A's tip (B's original parent tip) as oldBase")
}

// ─── ApplyPlan: Rename ───────────────────────────────────────────────────────

func TestApplyPlan_Rename(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	var renameCalls []struct{ oldName, newName string }

	mock := newApplyMock(gitDir, branchSHAs)
	mock.RenameBranchFn = func(old, new string) error {
		renameCalls = append(renameCalls, struct{ oldName, newName string }{old, new})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"}

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// RenameBranch called with correct args
	require.Len(t, renameCalls, 1)
	assert.Equal(t, "A", renameCalls[0].oldName)
	assert.Equal(t, "new-A", renameCalls[0].newName)

	// In-memory branch name updated
	assert.Equal(t, "new-A", sf.Stacks[0].Branches[0].Branch)

	// Result tracks rename
	require.Len(t, result.RenamedBranches, 1)
	assert.Equal(t, "A", result.RenamedBranches[0].OldName)
	assert.Equal(t, "new-A", result.RenamedBranches[0].NewName)
}

// ─── ApplyPlan: Reorder ──────────────────────────────────────────────────────

func TestApplyPlan_Reorder(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
			{Branch: "C"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
		"C":    "sha-C",
	}

	var rebaseCalls []rebaseCall

	mock := newApplyMock(gitDir, branchSHAs)
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Desired order: A, C, B (move C between A and B)
	nodes := []modifyview.ModifyBranchNode{
		{
			BranchNode:       stackview.BranchNode{Ref: sf.Stacks[0].Branches[0]}, // A
			OriginalPosition: 0,
		},
		{
			BranchNode:       stackview.BranchNode{Ref: sf.Stacks[0].Branches[2]}, // C
			OriginalPosition: 2,
		},
		{
			BranchNode:       stackview.BranchNode{Ref: sf.Stacks[0].Branches[1]}, // B
			OriginalPosition: 1,
		},
	}

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// Verify stack order is now A, C, B
	require.Len(t, sf.Stacks[0].Branches, 3)
	assert.Equal(t, "A", sf.Stacks[0].Branches[0].Branch)
	assert.Equal(t, "C", sf.Stacks[0].Branches[1].Branch)
	assert.Equal(t, "B", sf.Stacks[0].Branches[2].Branch)

	// Both C and B should be rebased onto their new parents
	rebaseMap := make(map[string]rebaseCall)
	for _, rc := range rebaseCalls {
		rebaseMap[rc.branch] = rc
	}

	if cCall, ok := rebaseMap["C"]; ok {
		assert.Equal(t, "A", cCall.newBase, "C should be rebased onto A")
	}
	if bCall, ok := rebaseMap["B"]; ok {
		assert.Equal(t, "C", bCall.newBase, "B should be rebased onto C")
	}
}

// ─── ApplyPlan: Mixed Drop and Fold ─────────────────────────────────────────

func TestApplyPlan_MixedDropAndFold(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
			{Branch: "C"},
			{Branch: "D"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
		"C":    "sha-C",
		"D":    "sha-D",
	}

	var cherryPickCalls [][]string
	var checkoutCalls []string
	var rebaseCalls []rebaseCall

	mock := newApplyMock(gitDir, branchSHAs)
	mock.CheckoutBranchFn = func(name string) error {
		checkoutCalls = append(checkoutCalls, name)
		return nil
	}
	mock.CherryPickFn = func(shas []string) error {
		cherryPickCalls = append(cherryPickCalls, shas)
		return nil
	}
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		if head == "C" {
			return []git.CommitInfo{{SHA: "c-commit-1"}}, nil
		}
		return nil, nil
	}
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Drop B, fold C down into A
	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[1].Removed = true
	nodes[2].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldDown}
	nodes[2].Removed = true

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// B and C should be removed, leaving A and D
	branchNames := make([]string, len(sf.Stacks[0].Branches))
	for i, b := range sf.Stacks[0].Branches {
		branchNames[i] = b.Branch
	}
	assert.Equal(t, []string{"A", "D"}, branchNames)

	// C's commits should have been cherry-picked onto A
	require.Len(t, cherryPickCalls, 1)

	// D should be rebased onto A
	var dRebase *rebaseCall
	for _, rc := range rebaseCalls {
		if rc.branch == "D" {
			dRebase = &rc
			break
		}
	}
	require.NotNil(t, dRebase, "D should be rebased")
	assert.Equal(t, "A", dRebase.newBase, "D should be rebased onto A")
}

// ─── ApplyPlan: Conflict During Rebase ───────────────────────────────────────

func TestApplyPlan_ConflictDuringRebase(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	mock := newApplyMock(gitDir, branchSHAs)
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		if branch == "B" {
			return assert.AnError
		}
		return nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) {
		return []string{"file.go"}, nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Drop A so B must rebase onto main
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[0].Removed = true

	_, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	assert.Error(t, err)
	require.NotNil(t, conflict)
	assert.Equal(t, "B", conflict.Branch)
	assert.Contains(t, conflict.ConflictedFiles, "file.go")

	// Verify state file written with phase "conflict"
	state, loadErr := LoadState(gitDir)
	require.NoError(t, loadErr)
	require.NotNil(t, state)
	assert.Equal(t, "conflict", state.Phase)
	assert.Equal(t, "B", state.ConflictBranch)
}

// ─── ApplyPlan: Conflict During CherryPick ───────────────────────────────────

func TestApplyPlan_ConflictDuringCherryPick(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	mock := newApplyMock(gitDir, branchSHAs)
	mock.CherryPickFn = func(shas []string) error {
		return assert.AnError
	}
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{SHA: "commit-1"}}, nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) {
		return []string{"conflict.go"}, nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Fold B down into A
	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldDown}
	nodes[1].Removed = true

	_, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	assert.Error(t, err)
	require.NotNil(t, conflict)
	assert.Equal(t, "B", conflict.Branch)

	// Cherry-pick conflicts now save state for --continue recovery
	state, loadErr := LoadState(gitDir)
	require.NoError(t, loadErr)
	require.NotNil(t, state)
	assert.Equal(t, PhaseConflict, state.Phase)
	assert.Equal(t, "cherry_pick", state.ConflictType)
	assert.Equal(t, "B", state.FoldBranch)
	assert.Equal(t, "A", state.FoldTarget)
	assert.Equal(t, "A", state.OriginalBranch)
	assert.Contains(t, state.RemainingBranches, "A")
}

// ─── ContinueApply: Multi-Stack Finds Correct Stack ─────────────────────────

func TestContinueApply_MultiStackFindsCorrectStack(t *testing.T) {
	// When multiple stacks share the same trunk, ContinueApply should use
	// StackIndex to find the right stack, not just trunk name matching.
	gitDir := t.TempDir()

	// Stack 0: main <- X (a different stack)
	// Stack 1: main <- A <- B <- C (the one being modified)
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "X"}},
			},
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "A"},
					{Branch: "B"},
					{Branch: "C"},
				},
			},
		},
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "gh-stack"), data, 0644))

	// Create a state file pointing at Stack 1 (index 1)
	state := &StateFile{
		SchemaVersion:   1,
		StackName:       "main",
		StackIndex:      1, // The correct stack is at index 1
		Phase:           PhaseConflict,
		ConflictBranch:  "A",
		ConflictType:    "rebase",
		RemainingBranches: []string{"B", "C"},
		OriginalRefs:    map[string]string{"B": "sha-A", "C": "sha-B"},
	}
	require.NoError(t, SaveState(gitDir, state))

	mock := newApplyMock(gitDir, map[string]string{
		"main": "sha-main", "A": "sha-A", "B": "sha-B", "C": "sha-C",
	})
	mock.IsRebaseInProgressFn = func() bool { return true }
	mock.RebaseContinueFn = func() error { return nil }

	var rebasedBranches []string
	mock.RebaseOntoFn = func(newBase, oldBase, branch string) error {
		rebasedBranches = append(rebasedBranches, branch)
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err = ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.NoError(t, err)

	// B and C should have been found and processed (not "no longer in stack")
	assert.Contains(t, rebasedBranches, "B", "B should be rebased")
	assert.Contains(t, rebasedBranches, "C", "C should be rebased")
}

// ─── Unwind ──────────────────────────────────────────────────────────────────

func TestUnwind(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	// Build a snapshot of the original state
	branchSHAs := map[string]string{
		"A": "sha-A-original",
		"B": "sha-B-original",
	}

	snapshotMock := &git.MockOps{
		RevParseFn: func(ref string) (string, error) {
			if sha, ok := branchSHAs[ref]; ok {
				return sha, nil
			}
			return "sha-" + ref, nil
		},
	}
	restore := git.SetOps(snapshotMock)
	snapshot, err := BuildSnapshot(&s)
	require.NoError(t, err)
	restore()

	// Save a state file
	stateFile := &StateFile{
		SchemaVersion: 1,
		StackName:     "main",
		StackIndex:    0,
		Phase:         "applying",
		Snapshot:      snapshot,
	}
	require.NoError(t, SaveState(gitDir, stateFile))

	// Simulate partial apply: modify the stack
	sf.Stacks[0].Branches = []stack.BranchRef{{Branch: "A"}} // B was removed

	var resetCalls []struct{ branch, sha string }
	var checkoutCalls []string
	currentBranch := "A"

	mock := &git.MockOps{
		IsRebaseInProgressFn: func() bool { return false },
		BranchExistsFn:       func(name string) bool { return true },
		CheckoutBranchFn: func(name string) error {
			checkoutCalls = append(checkoutCalls, name)
			currentBranch = name
			return nil
		},
		ResetHardFn: func(ref string) error {
			resetCalls = append(resetCalls, struct{ branch, sha string }{currentBranch, ref})
			return nil
		},
		CreateBranchFn: func(name, base string) error { return nil },
	}

	restore = git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err = Unwind(cfg, gitDir, snapshot, 0, sf, nil)
	require.NoError(t, err)

	// ResetHard should be called for each branch with snapshot SHAs
	resetMap := make(map[string]string)
	for _, r := range resetCalls {
		resetMap[r.branch] = r.sha
	}
	assert.Equal(t, "sha-A-original", resetMap["A"])
	assert.Equal(t, "sha-B-original", resetMap["B"])

	// Stack should be restored to original (2 branches)
	assert.Equal(t, 2, len(sf.Stacks[0].Branches))
	assert.Equal(t, "A", sf.Stacks[0].Branches[0].Branch)
	assert.Equal(t, "B", sf.Stacks[0].Branches[1].Branch)

	// State file should be cleared
	assert.False(t, StateExists(gitDir))
}

// ─── ApplyPlan: No-op (empty plan) ──────────────────────────────────────────

func TestApplyPlan_NoChanges(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	mock := newApplyMock(gitDir, branchSHAs)
	// Make IsAncestor return true and MergeBase match oldBase to skip rebases
	mock.IsAncestorFn = func(a, d string) (bool, error) { return true, nil }
	mock.MergeBaseFn = func(a, b string) (string, error) {
		// Return the parent tip SHA so the "no rebase needed" check passes
		if a == "main" && b == "A" {
			return branchSHAs["main"], nil
		}
		if a == "A" && b == "B" {
			return branchSHAs["A"], nil
		}
		return "merge-base", nil
	}

	var rebaseCalls int
	mock.RebaseOntoFn = func(string, string, string) error {
		rebaseCalls++
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Equal(t, 0, rebaseCalls, "no rebase should be needed when nothing changed")
}

// ─── ApplyPlan: Drop with no PR ─────────────────────────────────────────────

func TestApplyPlan_DropNoPR(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	mock := newApplyMock(gitDir, branchSHAs)
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[0].Removed = true

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// No PR means no DroppedPRs entry
	assert.Empty(t, result.DroppedPRs)

	// A should be removed
	assert.Equal(t, 1, len(sf.Stacks[0].Branches))
	assert.Equal(t, "B", sf.Stacks[0].Branches[0].Branch)
}

// ─── ContinueApply ──────────────────────────────────────────────────────────

func TestContinueApply(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
			{Branch: "C"},
		},
	}

	gitDir := t.TempDir()
	_ = writeTestStackFile(t, gitDir, s)

	// Write a conflict state file
	stateFile := &StateFile{
		SchemaVersion:      1,
		StackName:          "main",
		StackIndex:         0,
		Phase:              "conflict",
		ConflictBranch:     "B",
		RemainingBranches:  []string{"C"},
		OriginalBranch:     "A",
		OriginalRefs: map[string]string{
			"A": "sha-A",
			"B": "sha-B",
			"C": "sha-C",
		},
	}
	require.NoError(t, SaveState(gitDir, stateFile))

	var rebaseContinueCalled bool
	var rebaseCalls []rebaseCall
	var checkoutCalls []string

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "B", nil },
		BranchExistsFn:  func(string) bool { return true },
		IsRebaseInProgressFn: func() bool { return true },
		RebaseContinueFn: func() error {
			rebaseContinueCalled = true
			return nil
		},
		RebaseOntoFn: func(newBase, oldBase, branch string) error {
			rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkoutCalls = append(checkoutCalls, name)
			return nil
		},
		IsAncestorFn: func(a, d string) (bool, error) { return false, nil },
		MergeBaseFn:   func(a, b string) (string, error) { return "merge-base", nil },
		RevParseFn:    func(ref string) (string, error) { return "sha-" + ref, nil },
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.NoError(t, err)

	assert.True(t, rebaseContinueCalled, "RebaseContinue should be called")

	// C should be rebased
	require.Len(t, rebaseCalls, 1)
	assert.Equal(t, "C", rebaseCalls[0].branch)
	assert.Equal(t, "B", rebaseCalls[0].newBase)
	assert.Equal(t, "sha-C", rebaseCalls[0].oldBase)

	// Should checkout original branch
	assert.Contains(t, checkoutCalls, "A")

	// State file should be cleared (no remote stack ID)
	assert.False(t, StateExists(gitDir))
}

func TestContinueApply_NoStateFile(t *testing.T) {
	gitDir := t.TempDir()

	mock := &git.MockOps{
		GitDirFn: func() (string, error) { return gitDir, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no modify state file found")
}

func TestContinueApply_WrongPhase(t *testing.T) {
	gitDir := t.TempDir()

	stateFile := &StateFile{
		SchemaVersion: 1,
		Phase:         "applying",
	}
	require.NoError(t, SaveState(gitDir, stateFile))

	mock := &git.MockOps{
		GitDirFn: func() (string, error) { return gitDir, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no modify conflict in progress")
}

// ─── Unwind with active rebase ──────────────────────────────────────────────

func TestUnwind_AbortsActiveRebase(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	snapshotMock := &git.MockOps{
		RevParseFn: func(ref string) (string, error) { return "sha-" + ref, nil },
	}
	restore := git.SetOps(snapshotMock)
	snapshot, err := BuildSnapshot(&s)
	require.NoError(t, err)
	restore()

	require.NoError(t, SaveState(gitDir, &StateFile{
		SchemaVersion: 1, Phase: "conflict", Snapshot: snapshot,
	}))

	var rebaseAbortCalled bool
	mock := &git.MockOps{
		IsRebaseInProgressFn: func() bool { return true },
		RebaseAbortFn: func() error {
			rebaseAbortCalled = true
			return nil
		},
		BranchExistsFn:   func(string) bool { return true },
		CheckoutBranchFn: func(string) error { return nil },
		ResetHardFn:      func(string) error { return nil },
		CreateBranchFn:   func(string, string) error { return nil },
	}

	restore = git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err = Unwind(cfg, gitDir, snapshot, 0, sf, nil)
	require.NoError(t, err)
	assert.True(t, rebaseAbortCalled, "RebaseAbort should be called when rebase is in progress")
	assert.False(t, StateExists(gitDir))
}

// ─── Unwind restores renamed branch ─────────────────────────────────────────

func TestUnwind_RestoresRenamedBranch(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	snapshotMock := &git.MockOps{
		RevParseFn: func(ref string) (string, error) { return "sha-" + ref, nil },
	}
	restore := git.SetOps(snapshotMock)
	snapshot, err := BuildSnapshot(&s)
	require.NoError(t, err)
	restore()

	// Simulate: A was renamed to new-A, so A no longer exists
	var createdBranches []struct{ name, sha string }
	mock := &git.MockOps{
		IsRebaseInProgressFn: func() bool { return false },
		BranchExistsFn: func(name string) bool {
			return name != "A" // A was renamed away
		},
		CreateBranchFn: func(name, sha string) error {
			createdBranches = append(createdBranches, struct{ name, sha string }{name, sha})
			return nil
		},
		CheckoutBranchFn: func(string) error { return nil },
		ResetHardFn:      func(string) error { return nil },
	}

	restore = git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err = Unwind(cfg, gitDir, snapshot, 0, sf, nil)
	require.NoError(t, err)

	// A should be recreated via CreateBranch
	require.Len(t, createdBranches, 1)
	assert.Equal(t, "A", createdBranches[0].name)
	assert.Equal(t, "sha-A", createdBranches[0].sha)
}

// ─── ApplyPlan: State file transitions for remote stack ─────────────────────

func TestApplyPlan_PendingSubmitForRemoteStack(t *testing.T) {
	s := stack.Stack{
		ID:    "remote-stack-123",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
		"B":    "sha-B",
	}

	mock := newApplyMock(gitDir, branchSHAs)
	mock.IsAncestorFn = func(a, d string) (bool, error) { return true, nil }
	mock.MergeBaseFn = func(a, b string) (string, error) {
		if a == "main" && b == "A" {
			return branchSHAs["main"], nil
		}
		if a == "A" && b == "B" {
			return branchSHAs["A"], nil
		}
		return "merge-base", nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])

	_, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Remote stack should transition to "pending_submit"
	state, loadErr := LoadState(gitDir)
	require.NoError(t, loadErr)
	require.NotNil(t, state)
	assert.Equal(t, "pending_submit", state.Phase)
}

func TestApplyPlan_ClearsStateForLocalStack(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
		},
	}

	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, s)

	branchSHAs := map[string]string{
		"main": "sha-main",
		"A":    "sha-A",
	}

	mock := newApplyMock(gitDir, branchSHAs)
	mock.IsAncestorFn = func(a, d string) (bool, error) { return true, nil }
	mock.MergeBaseFn = func(a, b string) (string, error) {
		return branchSHAs["main"], nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])

	_, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Local stack (no ID) should clear the state file
	assert.False(t, StateExists(gitDir))
}
