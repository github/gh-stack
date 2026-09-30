package modify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/modifyview"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/github/gh-stack/internal/worktree"
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
		BranchExistsFn:  func(name string) (bool, error) { _, ok := branchSHAs[name]; return ok, nil },
		RevParseFn: func(ref string) (string, error) {
			if sha, ok := branchSHAs[ref]; ok {
				return sha, nil
			}
			return "sha-" + ref, nil
		},
		IsAncestorFn:         func(a, d string) (bool, error) { return false, nil },
		MergeBaseFn:          func(a, b string) (string, error) { return "merge-base", nil },
		CheckoutBranchFn:     func(string) error { return nil },
		RebaseOntoFn:         func(string, string, string, git.RebaseOpts) error { return nil },
		IsRebaseInProgressFn: func() (bool, error) { return false, nil },
		RenameBranchFn: func(oldName, newName string) error {
			branchSHAs[newName] = branchSHAs[oldName]
			delete(branchSHAs, oldName)
			return nil
		},
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return []git.CommitInfo{{SHA: "commit-1"}, {SHA: "commit-2"}}, nil
		},
		CherryPickFn:      func([]string) error { return nil },
		ConflictedFilesFn: func() ([]string, error) { return nil, nil },
		ResetHardFn:       func(string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			branchSHAs[name] = base
			return nil
		},
		RebaseAbortFn: func() error { return nil },
	}
}

func requireWorktree(t *testing.T, parent git.Ops, path string) git.Ops {
	t.Helper()
	ops, err := parent.ForWorktree(path)
	require.NoError(t, err)
	require.NotNil(t, ops)
	return ops
}

func requireGitState(t *testing.T, query func() (bool, error)) bool {
	t.Helper()
	state, err := query()
	require.NoError(t, err)
	return state
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

func TestApplyPlan_BranchLookupFailureBeforeMutation(t *testing.T) {
	gitDir := t.TempDir()
	sf := writeTestStackFile(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
	})
	lookupErr := errors.New("branch lookup failed")
	mock := newApplyMock(gitDir, map[string]string{"A": "original-a", "B": "original-b"})
	mock.BranchExistsFn = func(name string) (bool, error) {
		if name == "B" {
			return false, lookupErr
		}
		return name != "renamed", nil
	}
	mock.RenameBranchFn = func(string, string) error {
		t.Fatal("must not rename after a failed lookup")
		return nil
	}
	mock.CheckoutBranchFn = func(string) error {
		t.Fatal("must not unwind untouched branches after a failed lookup")
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "renamed"}
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.ErrorIs(t, err, lookupErr)
	assert.Nil(t, result)
	assert.Nil(t, conflict)
	assert.False(t, StateExists(gitDir), "no recovery journal should be written before state checks succeed")
	loaded, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Equal(t, sf.Stacks, loaded.Stacks)
}

func TestUnwind_StateLookupFailurePreservesJournal(t *testing.T) {
	for _, query := range []string{"rebase", "cherry-pick", "snapshot branch", "cleanup branch"} {
		t.Run(query, func(t *testing.T) {
			gitDir := t.TempDir()
			sf := writeTestStackFile(t, gitDir, stack.Stack{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "A"}},
			})
			metadata, err := json.Marshal(sf.Stacks[0])
			require.NoError(t, err)
			snapshot := Snapshot{
				Branches:      []BranchSnapshot{{Name: "A", TipSHA: "original"}},
				StackMetadata: metadata,
			}
			state := &StateFile{
				SchemaVersion: 1, Phase: PhaseConflict, Snapshot: snapshot,
				Plan: []Action{{Type: "rename", Branch: "A", NewName: "renamed"}},
			}
			require.NoError(t, SaveState(gitDir, state))
			before, err := os.ReadFile(StatePath(gitDir))
			require.NoError(t, err)
			lookupErr := errors.New("state lookup failed")
			mock := &git.MockOps{
				GitDirFn: func() (string, error) { return gitDir, nil },
				IsRebaseInProgressFn: func() (bool, error) {
					if query == "rebase" {
						return false, lookupErr
					}
					return true, nil
				},
				IsCherryPickInProgressFn: func() (bool, error) {
					if query == "cherry-pick" {
						return false, lookupErr
					}
					return false, nil
				},
				BranchExistsFn: func(name string) (bool, error) {
					if (query == "snapshot branch" && name == "A") || (query == "cleanup branch" && name == "renamed") {
						return false, lookupErr
					}
					return true, nil
				},
				RebaseAbortFn: func() error {
					t.Fatal("must not abort before all state checks succeed")
					return nil
				},
				CheckoutBranchFn: func(string) error {
					t.Fatal("must not restore branches after a failed state lookup")
					return nil
				},
				DeleteBranchFn: func(string, bool) error {
					t.Fatal("must not clean up branches after a failed state lookup")
					return nil
				},
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			err = Unwind(cfg, gitDir, snapshot, 0, sf, state.Plan)
			require.ErrorIs(t, err, lookupErr)
			after, err := os.ReadFile(StatePath(gitDir))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestModifyRecovery_CheckedQueriesPreserveJournal(t *testing.T) {
	for _, location := range []string{"legacy", "origin", "foreign"} {
		for _, action := range []string{"continue", "abort"} {
			for _, query := range []string{"factory", "rebase", "cherry-pick", "branch"} {
				t.Run(fmt.Sprintf("%s/%s/%s", location, action, query), func(t *testing.T) {
					dir, origin, foreign := t.TempDir(), t.TempDir(), t.TempDir()
					sf := writeTestStackFile(t, dir, stack.Stack{
						Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}},
					})
					metadata, err := json.Marshal(sf.Stacks[0])
					require.NoError(t, err)
					state := &StateFile{
						SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase",
						OriginalBranch: "A", ConflictBranch: "A",
						Snapshot: Snapshot{
							Branches: []BranchSnapshot{{Name: "A", TipSHA: "original"}}, StackMetadata: metadata,
						},
					}
					if location != "legacy" {
						state.Worktrees = &worktree.Context{Origin: worktree.Location{Path: origin, ID: "."}}
					}
					if location == "foreign" {
						state.Worktrees.Owners = map[string]*worktree.Location{"A": {Path: foreign, ID: filepath.Join("worktrees", "foreign")}}
						state.Worktrees.Pending, state.Worktrees.PendingBefore = "A", "original"
					}
					require.NoError(t, SaveState(dir, state))
					before, err := os.ReadFile(StatePath(dir))
					require.NoError(t, err)
					catalog, err := os.ReadFile(filepath.Join(dir, "gh-stack"))
					require.NoError(t, err)
					lookupErr := errors.New("checked query failed")
					mock := newApplyMock(dir, map[string]string{"A": "original", "main": "trunk"})
					mock.RootDirFn = func() (string, error) { return origin, nil }
					target := mock
					if location == "foreign" {
						target = newApplyMock(dir, map[string]string{"A": "original", "main": "trunk"})
						target.RootDirFn = func() (string, error) { return foreign, nil }
						target.CommonDirFn = func() (string, error) { return dir, nil }
						target.GitDirFn = func() (string, error) { return filepath.Join(dir, "worktrees", "foreign"), nil }
						mock.ForWorktreeFn = func(path string) (git.Ops, error) {
							if worktree.SamePath(path, foreign) {
								if query == "factory" {
									return nil, lookupErr
								}
								return target, nil
							}
							return mock, nil
						}
					}
					target.IsRebaseInProgressFn = func() (bool, error) { return true, nil }
					if query == "factory" && location != "foreign" {
						mock.ForWorktreeFn = func(string) (git.Ops, error) { return nil, lookupErr }
					} else if query == "rebase" {
						target.IsRebaseInProgressFn = func() (bool, error) { return false, lookupErr }
					} else if query == "cherry-pick" {
						target.IsCherryPickInProgressFn = func() (bool, error) { return false, lookupErr }
					} else if query == "branch" {
						mock.BranchExistsFn = func(string) (bool, error) { return false, lookupErr }
					}
					for _, ops := range []*git.MockOps{mock, target} {
						ops.RebaseContinueFn = func(git.RebaseOpts) error { t.Fatal("must not continue after query failure"); return nil }
						ops.RebaseAbortFn = func() error { t.Fatal("must not abort after query failure"); return nil }
						ops.CheckoutBranchFn = func(string) error { t.Fatal("must not change checkout after query failure"); return nil }
						ops.ResetHardFn = func(string) error { t.Fatal("must not reset after query failure"); return nil }
					}
					restore := git.SetOps(mock)
					defer restore()
					cfg, _, _ := config.NewTestConfig()
					defer cfg.Out.Close()
					defer cfg.Err.Close()

					if action == "continue" {
						err = ContinueApply(cfg, dir, noopUpdateBaseSHAs)
					} else {
						err = UnwindFromStateFile(cfg, dir)
					}

					require.ErrorIs(t, err, lookupErr)
					after, err := os.ReadFile(StatePath(dir))
					require.NoError(t, err)
					assert.Equal(t, before, after)
					afterCatalog, err := os.ReadFile(filepath.Join(dir, "gh-stack"))
					require.NoError(t, err)
					assert.Equal(t, catalog, afterCatalog)
				})
			}
		}
	}
}

func TestContinueApply_MissingNativeOperationPreservesJournal(t *testing.T) {
	for _, location := range []string{"legacy", "origin", "foreign"} {
		for _, conflictType := range []string{"", "rebase", "cherry_pick"} {
			t.Run(fmt.Sprintf("%s/type=%s", location, conflictType), func(t *testing.T) {
				dir, origin, foreign := t.TempDir(), t.TempDir(), t.TempDir()
				s := stack.Stack{
					Trunk:    stack.BranchRef{Branch: "main"},
					Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
				}
				writeTestStackFile(t, dir, s)
				metadata, err := json.Marshal(s)
				require.NoError(t, err)
				state := &StateFile{
					SchemaVersion: 1, Phase: PhaseConflict, ConflictType: conflictType,
					OriginalBranch: "A", ConflictBranch: "A", RemainingBranches: []string{"B"},
					FoldBranch: "B", FoldTarget: "A",
					OriginalRefs: map[string]string{"B": "original-A"},
					Snapshot: Snapshot{
						Branches:      []BranchSnapshot{{Name: "A", TipSHA: "original-A"}, {Name: "B", TipSHA: "original-B"}},
						StackMetadata: metadata,
					},
				}
				if location != "legacy" {
					state.Worktrees = &worktree.Context{
						Origin:  worktree.Location{Path: origin, ID: "."},
						Pending: "A", PendingBefore: "original-A",
					}
				}
				if location == "foreign" {
					state.Worktrees.Owners = map[string]*worktree.Location{
						"A": {Path: foreign, ID: filepath.Join("worktrees", "foreign")},
					}
				}
				require.NoError(t, SaveState(dir, state))
				before, err := os.ReadFile(StatePath(dir))
				require.NoError(t, err)
				catalog, err := os.ReadFile(filepath.Join(dir, "gh-stack"))
				require.NoError(t, err)
				refs := map[string]string{"main": "trunk", "A": "original-A", "B": "original-B"}
				mock := newApplyMock(dir, refs)
				mock.RootDirFn = func() (string, error) { return origin, nil }
				mock.CurrentBranchFn = func() (string, error) { return "A", nil }
				target := mock
				if location == "foreign" {
					target = newApplyMock(dir, refs)
					target.RootDirFn = func() (string, error) { return foreign, nil }
					target.CommonDirFn = func() (string, error) { return dir, nil }
					target.GitDirFn = func() (string, error) { return filepath.Join(dir, "worktrees", "foreign"), nil }
					mock.ForWorktreeFn = func(path string) (git.Ops, error) {
						if worktree.SamePath(path, foreign) {
							return target, nil
						}
						return mock, nil
					}
					mock.IsRebaseInProgressFn = func() (bool, error) { return true, nil }
					mock.IsCherryPickInProgressFn = func() (bool, error) { return true, nil }
				}
				rebasing, picking := conflictType != "cherry_pick", conflictType == "cherry_pick"
				target.IsRebaseInProgressFn = func() (bool, error) { return rebasing, nil }
				target.IsCherryPickInProgressFn = func() (bool, error) { return picking, nil }
				target.RebaseAbortFn = func() error { rebasing = false; return nil }
				target.CherryPickAbortFn = func() error { picking = false; return nil }
				nativeContinues, refReads, mutations := 0, 0, 0
				for _, ops := range []*git.MockOps{mock, target} {
					ops.RebaseContinueFn = func(git.RebaseOpts) error {
						nativeContinues++
						return errors.New("no native rebase in progress")
					}
					ops.CherryPickContinueFn = func() error {
						nativeContinues++
						return errors.New("no native cherry-pick in progress")
					}
					ops.RevParseFn = func(ref string) (string, error) {
						refReads++
						return refs[ref], nil
					}
					ops.RebaseOntoFn = func(_, _, branch string, _ git.RebaseOpts) error {
						mutations++
						refs[branch] = "replayed"
						return nil
					}
					ops.CheckoutBranchFn = func(string) error { mutations++; return nil }
				}
				restore := git.SetOps(mock)
				defer restore()
				// An external abort removes Git's marker, then a new commit appears.
				if picking {
					require.NoError(t, target.CherryPickAbort())
				} else {
					require.NoError(t, target.RebaseAbort())
				}
				refs["A"] = "external-commit"
				cfg, _, _ := config.NewTestConfig()
				defer cfg.Out.Close()
				defer cfg.Err.Close()

				err = ContinueApply(cfg, dir, noopUpdateBaseSHAs)

				assert.ErrorContains(t, err, "gh stack modify --abort")
				if location == "foreign" {
					assert.ErrorContains(t, err, foreign)
				}
				assert.Zero(t, nativeContinues)
				assert.Zero(t, refReads, "must not claim the current tip as completed modify work")
				assert.Zero(t, mutations)
				assert.Equal(t, "external-commit", refs["A"])
				assert.Equal(t, "original-B", refs["B"])
				after, readErr := os.ReadFile(StatePath(dir))
				assert.NoError(t, readErr)
				assert.Equal(t, before, after)
				afterCatalog, readErr := os.ReadFile(filepath.Join(dir, "gh-stack"))
				require.NoError(t, readErr)
				assert.Equal(t, catalog, afterCatalog)
			})
		}
	}
}

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

	t.Run("removed nodes retain recovery actions", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
				Removed:          true,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionDrop},
			},
		}
		plan := BuildPlan(nodes)
		assert.Equal(t, []Action{{Type: "drop", Branch: "A"}}, plan)
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
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
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
		if base == "sha-A" && head == "B" {
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
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
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
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
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
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
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
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
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
	// Stack composition, not a stale catalog index or shared trunk, identifies
	// the stack being continued.
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
		SchemaVersion:     1,
		StackName:         "main",
		StackIndex:        0, // Stale index now points at the unrelated stack
		Phase:             PhaseConflict,
		ConflictBranch:    "A",
		ConflictType:      "rebase",
		RemainingBranches: []string{"B", "C"},
		OriginalRefs:      map[string]string{"B": "sha-A", "C": "sha-B"},
	}
	state.RecordStack(&sf.Stacks[1])
	require.NoError(t, SaveState(gitDir, state))

	mock := newApplyMock(gitDir, map[string]string{
		"main": "sha-main", "A": "sha-A", "B": "sha-B", "C": "sha-C",
	})
	inProgress := true
	mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
	mock.RebaseContinueFn = func(opts git.RebaseOpts) error { inProgress = false; return nil }

	var rebasedBranches []string
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
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
	stateFile.RecordStack(&sf.Stacks[0])
	require.NoError(t, SaveState(gitDir, stateFile))

	var resetCalls []struct{ branch, sha string }
	var checkoutCalls []string
	currentBranch := "A"

	mock := &git.MockOps{
		GitDirFn:             func() (string, error) { return gitDir, nil },
		IsRebaseInProgressFn: func() (bool, error) { return false, nil },
		BranchExistsFn:       func(name string) (bool, error) { return true, nil },
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
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
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
		SchemaVersion:     1,
		StackName:         "main",
		StackIndex:        0,
		Phase:             "conflict",
		ConflictBranch:    "B",
		RemainingBranches: []string{"C"},
		OriginalBranch:    "A",
		OriginalRefs: map[string]string{
			"A": "sha-A",
			"B": "sha-B",
			"C": "sha-C",
		},
	}
	stateFile.RecordStack(&s)
	require.NoError(t, SaveState(gitDir, stateFile))

	var rebaseContinueCalled bool
	var rebaseCalls []rebaseCall
	var checkoutCalls []string
	inProgress := true

	mock := &git.MockOps{
		GitDirFn:             func() (string, error) { return gitDir, nil },
		CurrentBranchFn:      func() (string, error) { return "B", nil },
		BranchExistsFn:       func(string) (bool, error) { return true, nil },
		IsRebaseInProgressFn: func() (bool, error) { return inProgress, nil },
		RebaseContinueFn: func(git.RebaseOpts) error {
			rebaseContinueCalled = true
			inProgress = false
			return nil
		},
		RebaseOntoFn: func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
			rebaseCalls = append(rebaseCalls, rebaseCall{newBase, oldBase, branch})
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkoutCalls = append(checkoutCalls, name)
			return nil
		},
		IsAncestorFn: func(a, d string) (bool, error) { return false, nil },
		MergeBaseFn:  func(a, b string) (string, error) { return "merge-base", nil },
		RevParseFn:   func(ref string) (string, error) { return "sha-" + ref, nil },
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
	inProgress := true
	mock := &git.MockOps{
		GitDirFn:             func() (string, error) { return gitDir, nil },
		IsRebaseInProgressFn: func() (bool, error) { return inProgress, nil },
		RebaseAbortFn: func() error {
			rebaseAbortCalled = true
			inProgress = false
			return nil
		},
		BranchExistsFn:   func(string) (bool, error) { return true, nil },
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

// ─── Unwind with active cherry-pick ─────────────────────────────────────────

// A fold-down conflict leaves an in-progress cherry-pick with an unmerged
// index. Unwind must abort it (git cherry-pick --abort) before restoring
// branches, otherwise the restore checkouts fail on the unmerged index.
func TestUnwind_AbortsActiveCherryPick(t *testing.T) {
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
		SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "cherry_pick", Snapshot: snapshot,
	}))

	var cherryPickAbortCalled bool
	var rebaseAbortCalled bool
	inProgress := true
	mock := &git.MockOps{
		GitDirFn:                 func() (string, error) { return gitDir, nil },
		IsRebaseInProgressFn:     func() (bool, error) { return false, nil },
		IsCherryPickInProgressFn: func() (bool, error) { return inProgress, nil },
		RebaseAbortFn:            func() error { rebaseAbortCalled = true; return nil },
		CherryPickAbortFn:        func() error { cherryPickAbortCalled = true; inProgress = false; return nil },
		BranchExistsFn:           func(string) (bool, error) { return true, nil },
		CheckoutBranchFn:         func(string) error { return nil },
		ResetHardFn:              func(string) error { return nil },
		CreateBranchFn:           func(string, string) error { return nil },
	}

	restore = git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err = Unwind(cfg, gitDir, snapshot, 0, sf, nil)
	require.NoError(t, err)
	assert.True(t, cherryPickAbortCalled, "CherryPickAbort should be called when a cherry-pick is in progress")
	assert.False(t, rebaseAbortCalled, "RebaseAbort should not be called when no rebase is in progress")
	assert.False(t, StateExists(gitDir))
}

// ─── ContinueApply: subsequent conflict after fold-down is a rebase ─────────

// After resolving an initial fold-down (cherry-pick) conflict, the cascading
// rebase over the remaining branches may itself conflict. That conflict is a
// rebase, so ContinueApply must update ConflictType from "cherry_pick" to
// "rebase" — otherwise the next --continue wrongly calls CherryPickContinue
// and fails, stranding the user.
func TestContinueApply_SubsequentConflictBecomesRebase(t *testing.T) {
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
	_ = sf

	// State as written when a fold-down of B into A conflicts on cherry-pick.
	// B is still present in the stack metadata (it is removed only after the
	// cherry-pick succeeds in ContinueApply).
	state := &StateFile{
		SchemaVersion:     1,
		StackName:         "main",
		StackIndex:        0,
		Phase:             PhaseConflict,
		ConflictType:      "cherry_pick",
		ConflictBranch:    "B",
		FoldBranch:        "B",
		FoldTarget:        "A",
		RemainingBranches: []string{"A", "C"},
		OriginalBranch:    "A",
		OriginalRefs:      map[string]string{"A": "sha-main", "C": "sha-A-old"},
	}
	state.RecordStack(&s)
	require.NoError(t, SaveState(gitDir, state))

	mock := newApplyMock(gitDir, map[string]string{
		"main": "sha-main", "A": "sha-A", "B": "sha-B", "C": "sha-C",
	})
	// The user resolved the cherry-pick; --continue finishes it cleanly.
	picking := true
	mock.IsCherryPickInProgressFn = func() (bool, error) { return picking, nil }
	mock.CherryPickContinueFn = func() error { picking = false; return nil }
	// A rebases cleanly onto main; C then conflicts.
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "C" {
			return assert.AnError
		}
		return nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) { return []string{"c.go"}, nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.Error(t, err)

	got, loadErr := LoadState(gitDir)
	require.NoError(t, loadErr)
	require.NotNil(t, got)
	assert.Equal(t, PhaseConflict, got.Phase)
	assert.Equal(t, "rebase", got.ConflictType, "subsequent cascade conflict must be recorded as a rebase")
	assert.Equal(t, "C", got.ConflictBranch)
}

// Regression test for the review on PR #167: after an initial fold-down
// (cherry-pick) conflict is resolved, a subsequent cascade rebase conflict must
// persist the fold-branch removal to disk. Otherwise the next --continue
// re-reads stale on-disk metadata and — because ConflictType is now "rebase" —
// skips the fold-removal step, silently resurrecting the folded branch as a
// phantom entry once recovery completes.
func TestContinueApply_FoldThenCascadeConflict_DoesNotResurrectFoldedBranch(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
			{Branch: "C"},
		},
	}

	gitDir := t.TempDir()
	writeTestStackFile(t, gitDir, s)

	// State written by ApplyPlan when the fold-down of B into A conflicts on
	// cherry-pick. B is still present in the on-disk metadata at this point.
	state := &StateFile{
		SchemaVersion:     1,
		StackName:         "main",
		StackIndex:        0,
		Phase:             PhaseConflict,
		ConflictType:      "cherry_pick",
		ConflictBranch:    "B",
		FoldBranch:        "B",
		FoldTarget:        "A",
		RemainingBranches: []string{"A", "C"},
		OriginalBranch:    "A",
		OriginalRefs:      map[string]string{"A": "sha-main", "C": "sha-A-old"},
	}
	state.RecordStack(&s)
	require.NoError(t, SaveState(gitDir, state))

	mock := newApplyMock(gitDir, map[string]string{
		"main": "sha-main", "A": "sha-A", "B": "sha-B", "C": "sha-C",
	})
	picking := true
	mock.IsCherryPickInProgressFn = func() (bool, error) { return picking, nil }
	mock.CherryPickContinueFn = func() error { picking = false; return nil }
	inProgress := false
	mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
	mock.RebaseContinueFn = func(git.RebaseOpts) error { inProgress = false; return nil }
	// C conflicts on its first rebase attempt, then succeeds (user resolved it).
	cRebases := 0
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "C" {
			cRebases++
			if cRebases == 1 {
				inProgress = true
				return assert.AnError
			}
		}
		return nil
	}
	mock.ConflictedFilesFn = func() ([]string, error) { return []string{"c.go"}, nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// First --continue: finishes the fold, then conflicts rebasing C.
	err := ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.Error(t, err)

	// The fold-branch removal must already be persisted on disk, even though
	// the cascade hit a conflict.
	afterFirst, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Equal(t, -1, afterFirst.Stacks[0].IndexOf("B"),
		"folded branch B must not be present on disk after the cascade conflict")

	// Second --continue: rebase resolves and recovery completes.
	err = ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.NoError(t, err)

	final, err := stack.Load(gitDir)
	require.NoError(t, err)
	names := make([]string, len(final.Stacks[0].Branches))
	for i, b := range final.Stacks[0].Branches {
		names[i] = b.Branch
	}
	assert.Equal(t, []string{"A", "C"}, names,
		"folded branch B must stay removed after recovery completes")
	assert.False(t, StateExists(gitDir), "state should be cleared after successful recovery")
}

func TestContinueApply_RebaseStartErrorPersistsRetryState(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B"},
			{Branch: "C"},
		},
	}

	gitDir := t.TempDir()
	writeTestStackFile(t, gitDir, s)

	state := &StateFile{
		SchemaVersion:     1,
		StackName:         "main",
		StackIndex:        0,
		Phase:             PhaseConflict,
		ConflictType:      "cherry_pick",
		ConflictBranch:    "B",
		FoldBranch:        "B",
		FoldTarget:        "A",
		RemainingBranches: []string{"A", "C"},
		OriginalBranch:    "A",
		OriginalRefs:      map[string]string{"A": "sha-main", "C": "sha-A-old"},
	}
	state.RecordStack(&s)
	require.NoError(t, SaveState(gitDir, state))

	mock := newApplyMock(gitDir, map[string]string{
		"main": "sha-main", "A": "sha-A", "B": "sha-B", "C": "sha-C",
	})
	cherryPickContinues := 0
	picking := true
	mock.IsCherryPickInProgressFn = func() (bool, error) { return picking, nil }
	mock.CherryPickContinueFn = func() error {
		cherryPickContinues++
		picking = false
		return nil
	}
	cRebases := 0
	mock.RebaseOntoFn = func(newBase, oldBase, branch string, opts git.RebaseOpts) error {
		if branch == "C" {
			cRebases++
			if cRebases == 1 {
				return &git.RebaseStartError{Err: errors.New("branch is checked out elsewhere")}
			}
		}
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.Error(t, err)

	retryState, err := LoadState(gitDir)
	require.NoError(t, err)
	require.NotNil(t, retryState)
	assert.Equal(t, "rebase_start", retryState.ConflictType)
	assert.Equal(t, "C", retryState.ConflictBranch)
	assert.Empty(t, retryState.RemainingBranches)

	afterFirst, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Equal(t, -1, afterFirst.Stacks[0].IndexOf("B"),
		"completed fold metadata must be saved before waiting to retry the rebase")

	err = ContinueApply(cfg, gitDir, noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Equal(t, 1, cherryPickContinues, "retry must not repeat the completed cherry-pick")
	assert.Equal(t, 2, cRebases, "retry must restart the refused rebase")
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
		GitDirFn:             func() (string, error) { return gitDir, nil },
		IsRebaseInProgressFn: func() (bool, error) { return false, nil },
		BranchExistsFn: func(name string) (bool, error) {
			return name != "A", nil // A was renamed away
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
			{Branch: "A", PullRequest: &stack.PullRequestRef{Number: 1}},
			{Branch: "B", PullRequest: &stack.PullRequestRef{Number: 2}},
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
	mock.IsAncestorFn = func(a, d string) (bool, error) { return false, nil }
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

	// Reverse nodes so position differs → triggers rebase of PR branches
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0], nodes[1] = nodes[1], nodes[0]

	result, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Remote stack with PR branches affected should transition to "pending_submit"
	state, loadErr := LoadState(gitDir)
	require.NoError(t, loadErr)
	require.NotNil(t, state)
	assert.Equal(t, "pending_submit", state.Phase)
	assert.True(t, result.NeedsSubmit, "NeedsSubmit should be true when PR branches are affected")
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

func TestApplyPlan_ClearsStateForRemoteStackWithNoPRBranches(t *testing.T) {
	// Remote stack (has ID) but branches have no PRs — local-only modify
	s := stack.Stack{
		ID:    "remote-stack-456",
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

	result, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Remote stack but no PR branches affected → state should be cleared
	assert.False(t, StateExists(gitDir), "state file should be cleared when no PR branches are affected")
	assert.False(t, result.NeedsSubmit, "NeedsSubmit should be false when no PR branches are affected")
}

func TestApplyPlan_PendingSubmitOnlyWhenPRBranchesAffected(t *testing.T) {
	// Stack with one PR branch (A) and one local branch (B).
	// Only rename the local branch B — PRs should not be affected.
	s := stack.Stack{
		ID:    "remote-stack-789",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A", PullRequest: &stack.PullRequestRef{Number: 1}},
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
		if a == "A" && b == "B" || a == "A" && b == "B-renamed" {
			return branchSHAs["A"], nil
		}
		return "merge-base", nil
	}
	mock.RenameBranchFn = func(old, newName string) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	nodes := makeNodes(&sf.Stacks[0])
	// Rename only the non-PR branch B
	nodes[1].PendingAction = &modifyview.PendingAction{
		Type:    modifyview.ActionRename,
		NewName: "B-renamed",
	}

	result, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Only non-PR branch was renamed — should clear state, not pending submit
	assert.False(t, StateExists(gitDir), "state file should be cleared when only non-PR branches are renamed")
	assert.False(t, result.NeedsSubmit, "NeedsSubmit should be false when only non-PR branches are affected")
}

// ─── resolveCheckoutBranch ──────────────────────────────────────────────────

func TestResolveCheckoutBranch_StillInStack(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{{Name: "A", Position: 0}, {Name: "B", Position: 1}},
	}

	result := resolveCheckoutBranch("A", nil, snapshot, s)
	assert.Equal(t, "A", result)
}

func TestResolveCheckoutBranch_Renamed(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "new-A"}, {Branch: "B"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{{Name: "A", Position: 0}, {Name: "B", Position: 1}},
	}
	plan := []Action{{Type: "rename", Branch: "A", NewName: "new-A"}}

	result := resolveCheckoutBranch("A", plan, snapshot, s)
	assert.Equal(t, "new-A", result)
}

func TestResolveCheckoutBranch_FoldDown(t *testing.T) {
	// B is folded down into A. After fold, stack has [A, C].
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "C"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
		},
	}
	plan := []Action{{Type: "fold_down", Branch: "B"}}

	result := resolveCheckoutBranch("B", plan, snapshot, s)
	assert.Equal(t, "A", result)
}

func TestResolveCheckoutBranch_FoldUp(t *testing.T) {
	// B is folded up into C. After fold, stack has [A, C].
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "C"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
		},
	}
	plan := []Action{{Type: "fold_up", Branch: "B"}}

	result := resolveCheckoutBranch("B", plan, snapshot, s)
	assert.Equal(t, "C", result)
}

func TestResolveCheckoutBranch_FoldReceiverAcrossRemovedBranches(t *testing.T) {
	snapshot := Snapshot{Branches: []BranchSnapshot{
		{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"},
	}}
	for _, tt := range []struct {
		name, original, kind, target, renamed, want string
		branches                                    []string
	}{
		{"compiled fold up", "B", "fold_up", "D", "", "D", []string{"A", "D", "E"}},
		{"compiled fold down", "D", "fold_down", "B", "", "B", []string{"A", "B", "E"}},
		{"compiled renamed receiver", "B", "fold_up", "new-D", "", "new-D", []string{"A", "new-D", "E"}},
		{"legacy fold up", "B", "fold_up", "", "", "D", []string{"A", "D", "E"}},
		{"legacy fold down", "D", "fold_down", "", "", "B", []string{"A", "B", "E"}},
		{"legacy renamed receiver", "B", "fold_up", "", "D", "new-D", []string{"A", "new-D", "E"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &stack.Stack{Trunk: stack.BranchRef{Branch: "main"}}
			for _, name := range tt.branches {
				s.Branches = append(s.Branches, stack.BranchRef{Branch: name})
			}
			plan := []Action{{Type: tt.kind, Branch: tt.original, Target: tt.target}}
			if tt.renamed != "" {
				plan = append(plan, Action{Type: "rename", Branch: tt.renamed, NewName: tt.want})
			}
			assert.Equal(t, tt.want, resolveCheckoutBranch(tt.original, plan, snapshot, s))
		})
	}
}

func TestRestoreCheckout_UsesCompiledFoldReceiver(t *testing.T) {
	for _, tt := range []struct {
		name              string
		foreign, aborting bool
		receiver, want    string
	}{
		{"available receiver", false, false, "D", "D"},
		{"foreign receiver", true, false, "D", "B"},
		{"renamed foreign receiver", true, false, "new-D", "B"},
		{"abort restores original", true, true, "D", "B"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			common, origin, foreign := t.TempDir(), t.TempDir(), t.TempDir()
			current := "A"
			var checkouts []string
			originOps := &git.MockOps{
				CommonDirFn:     func() (string, error) { return common, nil },
				GitDirFn:        func() (string, error) { return common, nil },
				RootDirFn:       func() (string, error) { return origin, nil },
				CurrentBranchFn: func() (string, error) { return current, nil },
				CheckoutBranchFn: func(name string) error {
					checkouts = append(checkouts, name)
					current = name
					return nil
				},
			}
			foreignOps := &git.MockOps{
				CommonDirFn: func() (string, error) { return common, nil },
				GitDirFn:    func() (string, error) { return filepath.Join(common, "worktrees", "receiver"), nil },
				RootDirFn:   func() (string, error) { return foreign, nil },
				CheckoutBranchFn: func(string) error {
					t.Fatal("must not switch the receiver's worktree")
					return nil
				},
			}
			originOps.ForWorktreeFn = func(path string) (git.Ops, error) {
				if worktree.SamePath(path, foreign) {
					return foreignOps, nil
				}
				require.True(t, worktree.SamePath(path, origin))
				return originOps, nil
			}
			originOps.WorktreesFn = func() ([]git.Worktree, error) {
				trees := []git.Worktree{{Path: origin, Branch: current}}
				if tt.foreign {
					trees = append(trees, git.Worktree{Path: foreign, Branch: tt.receiver})
				}
				return trees, nil
			}
			restore := git.SetOps(originOps)
			defer restore()
			original := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{
				{Branch: "A"}, {Branch: "B"}, {Branch: "C"}, {Branch: "D"}, {Branch: "E"},
			}}
			nodes := makeNodes(&original)
			nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
			nodes[1].Removed = true
			nodes[2].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
			nodes[2].Removed = true
			if tt.receiver != "D" {
				nodes[3].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: tt.receiver}
			}
			execution, desired, err := compileActions(&original, nodes)
			require.NoError(t, err)
			require.Contains(t, execution, Action{Type: "fold_up", Branch: "B", Target: tt.receiver})
			state := &StateFile{
				OriginalBranch: "B", Plan: BuildPlan(nodes), Execution: execution, DesiredOrder: desired,
				Worktrees: &worktree.Context{Origin: worktree.Location{Path: origin}},
			}
			for _, branch := range original.Branches {
				state.Snapshot.Branches = append(state.Snapshot.Branches, BranchSnapshot{Name: branch.Branch})
			}
			require.NoError(t, SaveState(common, state))
			state, err = LoadState(common)
			require.NoError(t, err)
			require.NotNil(t, state)
			s := &stack.Stack{Trunk: original.Trunk, Branches: []stack.BranchRef{
				{Branch: "A"}, {Branch: tt.receiver}, {Branch: "E"},
			}}
			cfg, outR, errR := config.NewTestConfig()
			defer outR.Close()
			defer errR.Close()
			err = restoreCheckout(cfg, state, s, tt.aborting)
			require.NoError(t, cfg.Out.Close())
			require.NoError(t, cfg.Err.Close())
			require.NoError(t, err)
			output, err := io.ReadAll(errR)
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, checkouts)
			assert.Equal(t, tt.want, current)
			if tt.foreign && !tt.aborting {
				assert.Contains(t, string(output), "surviving branch "+tt.receiver)
				assert.Contains(t, string(output), foreign)
			}
			assert.NotContains(t, string(output), "surviving branch E")
		})
	}
}

func TestResolveCheckoutBranch_Dropped_HasAbove(t *testing.T) {
	// B is dropped. Stack has [A, C]. Should pick C (above B).
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "C"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
		},
	}
	plan := []Action{{Type: "drop", Branch: "B"}}

	result := resolveCheckoutBranch("B", plan, snapshot, s)
	assert.Equal(t, "C", result)
}

func TestResolveCheckoutBranch_Dropped_TopBranch(t *testing.T) {
	// C (topmost) is dropped. Stack has [A, B]. Should pick B (below C).
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
		},
	}
	plan := []Action{{Type: "drop", Branch: "C"}}

	result := resolveCheckoutBranch("C", plan, snapshot, s)
	assert.Equal(t, "B", result)
}

func TestResolveCheckoutBranch_Dropped_MultipleDropped(t *testing.T) {
	// B and C both dropped. Stack has [A, D]. Original on B → should pick D (nearest above).
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "D"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
			{Name: "D", Position: 3},
		},
	}
	plan := []Action{
		{Type: "drop", Branch: "B"},
		{Type: "drop", Branch: "C"},
	}

	result := resolveCheckoutBranch("B", plan, snapshot, s)
	assert.Equal(t, "D", result)
}

func TestResolveCheckoutBranch_Fallback_EmptyStack(t *testing.T) {
	// All branches removed — falls back to original (no crash).
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{{Name: "A", Position: 0}},
	}
	plan := []Action{{Type: "drop", Branch: "A"}}

	result := resolveCheckoutBranch("A", plan, snapshot, s)
	// No surviving branches → returns original as last resort
	assert.Equal(t, "A", result)
}

func TestResolveCheckoutBranch_Fallback_TopBranch(t *testing.T) {
	// Original branch not in plan and not in stack → fallback to topmost.
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "X"}, {Branch: "Y"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{{Name: "A", Position: 0}},
	}

	result := resolveCheckoutBranch("A", nil, snapshot, s)
	assert.Equal(t, "Y", result)
}

func TestResolveCheckoutBranch_FoldDown_TargetRenamed(t *testing.T) {
	// B is folded down into A, and A is renamed to new-A in the same operation.
	// After apply, stack has [new-A, C].
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "new-A"}, {Branch: "C"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
		},
	}
	plan := []Action{
		{Type: "rename", Branch: "A", NewName: "new-A"},
		{Type: "fold_down", Branch: "B"},
	}

	result := resolveCheckoutBranch("B", plan, snapshot, s)
	assert.Equal(t, "new-A", result)
}

func TestResolveCheckoutBranch_Dropped_NeighborRenamed(t *testing.T) {
	// B is dropped, and C (above) is renamed to new-C in the same operation.
	// After apply, stack has [A, new-C].
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "new-C"}},
	}
	snapshot := Snapshot{
		Branches: []BranchSnapshot{
			{Name: "A", Position: 0},
			{Name: "B", Position: 1},
			{Name: "C", Position: 2},
		},
	}
	plan := []Action{
		{Type: "rename", Branch: "C", NewName: "new-C"},
		{Type: "drop", Branch: "B"},
	}

	result := resolveCheckoutBranch("B", plan, snapshot, s)
	assert.Equal(t, "new-C", result)
}

// ─── ApplyPlan: Checkout behavior after drop ────────────────────────────────

func TestApplyPlan_Drop_ChecksOutNearestBranch(t *testing.T) {
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

	var lastCheckout string
	mock := newApplyMock(gitDir, branchSHAs)
	mock.CheckoutBranchFn = func(name string) error {
		lastCheckout = name
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Drop B, user was on B
	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[1].Removed = true

	_, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Should check out C (branch above B), not B
	assert.Equal(t, "C", lastCheckout)
}

func TestApplyPlan_FoldDown_ChecksOutTarget(t *testing.T) {
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

	var lastCheckout string
	mock := newApplyMock(gitDir, branchSHAs)
	mock.CheckoutBranchFn = func(name string) error {
		lastCheckout = name
		return nil
	}
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{SHA: "commit-1"}}, nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Fold B down into A, user was on B
	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldDown}
	nodes[1].Removed = true

	_, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Should check out A (fold target), not B
	assert.Equal(t, "A", lastCheckout)
}

func TestApplyPlan_Rename_ChecksOutNewName(t *testing.T) {
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

	var lastCheckout string
	mock := newApplyMock(gitDir, branchSHAs)
	mock.CheckoutBranchFn = func(name string) error {
		lastCheckout = name
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Rename A to new-A, user was on A
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"}

	_, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)

	// Should check out new-A, not A
	assert.Equal(t, "new-A", lastCheckout)
}

// ─── BuildPlan: Insert ──────────────────────────────────────────────────────

func TestBuildPlan_Insert(t *testing.T) {
	t.Run("insert below produces insert_below action", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "new-branch"}},
				OriginalPosition: -1,
				IsInserted:       true,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionInsertBelow, NewName: "new-branch"},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "B"}},
				OriginalPosition: 1,
			},
		}
		plan := BuildPlan(nodes)
		require.Len(t, plan, 1)
		assert.Equal(t, "insert_below", plan[0].Type)
		assert.Equal(t, "new-branch", plan[0].Branch)
		assert.Equal(t, "new-branch", plan[0].NewName)
		assert.Equal(t, 1, plan[0].NewPosition)
	})

	t.Run("insert above produces insert_above action", func(t *testing.T) {
		nodes := []modifyview.ModifyBranchNode{
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "new-branch"}},
				OriginalPosition: -1,
				IsInserted:       true,
				PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionInsertAbove, NewName: "new-branch"},
			},
			{
				BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: "A"}},
				OriginalPosition: 0,
			},
		}
		plan := BuildPlan(nodes)
		require.Len(t, plan, 1)
		assert.Equal(t, "insert_above", plan[0].Type)
		assert.Equal(t, "new-branch", plan[0].NewName)
		assert.Equal(t, 0, plan[0].NewPosition)
	})
}

// ─── ApplyPlan: Insert ──────────────────────────────────────────────────────

func TestApplyPlan_Insert(t *testing.T) {
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

	var createCalls []struct{ name, base string }
	mock := newApplyMock(gitDir, branchSHAs)
	mock.CreateBranchFn = func(name, base string) error {
		createCalls = append(createCalls, struct{ name, base string }{name, base})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Insert "new-branch" between A and B (at position 1 in stack order)
	nodes := makeNodes(&sf.Stacks[0])
	insertNode := modifyview.ModifyBranchNode{
		BranchNode: stackview.BranchNode{
			Ref:      stack.BranchRef{Branch: "new-branch"},
			IsLinear: true,
		},
		PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionInsertBelow, NewName: "new-branch"},
		OriginalPosition: -1,
		IsInserted:       true,
	}
	// Insert between A(0) and B(1)
	allNodes := []modifyview.ModifyBranchNode{nodes[0], insertNode, nodes[1]}

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, allNodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// Branch should have been created
	require.Len(t, createCalls, 1)
	assert.Equal(t, "new-branch", createCalls[0].name)
	assert.Equal(t, "sha-A", createCalls[0].base)

	// Stack should now have 3 branches: A, new-branch, B
	require.Len(t, sf.Stacks[0].Branches, 3)
	assert.Equal(t, "A", sf.Stacks[0].Branches[0].Branch)
	assert.Equal(t, "new-branch", sf.Stacks[0].Branches[1].Branch)
	assert.Equal(t, "B", sf.Stacks[0].Branches[2].Branch)

	// new-branch should be in InsertedBranches
	require.Len(t, result.InsertedBranches, 1)
	assert.Equal(t, "new-branch", result.InsertedBranches[0])
}

func TestApplyPlan_InsertAtStart(t *testing.T) {
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

	var createCalls []struct{ name, base string }
	mock := newApplyMock(gitDir, branchSHAs)
	mock.CreateBranchFn = func(name, base string) error {
		createCalls = append(createCalls, struct{ name, base string }{name, base})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Insert "new-branch" at the start (before A)
	nodes := makeNodes(&sf.Stacks[0])
	insertNode := modifyview.ModifyBranchNode{
		BranchNode: stackview.BranchNode{
			Ref:      stack.BranchRef{Branch: "new-branch"},
			IsLinear: true,
		},
		PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionInsertAbove, NewName: "new-branch"},
		OriginalPosition: -1,
		IsInserted:       true,
	}
	allNodes := []modifyview.ModifyBranchNode{insertNode, nodes[0], nodes[1]}

	result, conflict, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, allNodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	require.NotNil(t, result)

	// Branch should be created from trunk
	require.Len(t, createCalls, 1)
	assert.Equal(t, "new-branch", createCalls[0].name)
	assert.Equal(t, "sha-main", createCalls[0].base)

	// Stack should now have 3 branches: new-branch, A, B
	require.Len(t, sf.Stacks[0].Branches, 3)
	assert.Equal(t, "new-branch", sf.Stacks[0].Branches[0].Branch)
	assert.Equal(t, "A", sf.Stacks[0].Branches[1].Branch)
	assert.Equal(t, "B", sf.Stacks[0].Branches[2].Branch)
}

func TestApplyPlan_InsertAffectsPRs(t *testing.T) {
	s := stack.Stack{
		ID:    "test-id",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"},
			{Branch: "B", PullRequest: &stack.PullRequestRef{Number: 42}},
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
	mock.CreateBranchFn = func(name, base string) error { return nil }

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Insert between A and B — B has a PR, so base changes → affectsPRs
	nodes := makeNodes(&sf.Stacks[0])
	insertNode := modifyview.ModifyBranchNode{
		BranchNode: stackview.BranchNode{
			Ref:      stack.BranchRef{Branch: "new-branch"},
			IsLinear: true,
		},
		PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionInsertBelow, NewName: "new-branch"},
		OriginalPosition: -1,
		IsInserted:       true,
	}
	allNodes := []modifyview.ModifyBranchNode{nodes[0], insertNode, nodes[1]}

	result, _, err := ApplyPlan(cfg, gitDir, &sf.Stacks[0], sf, allNodes, "A", noopUpdateBaseSHAs)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should need submit because insertion changes the base of a branch with PR
	assert.True(t, result.NeedsSubmit, "inserting before a branch with a PR should trigger NeedsSubmit")
}

func TestMatchesStack(t *testing.T) {
	original := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "A"}, {Branch: "B"},
		},
	}
	metadata, err := json.Marshal(original)
	require.NoError(t, err)
	renamed := original
	renamed.Branches = []stack.BranchRef{{Branch: "new-A"}, {Branch: "B"}}
	other := stack.Stack{Trunk: original.Trunk, Branches: []stack.BranchRef{{Branch: "X"}}}
	tests := []struct {
		name   string
		state  *StateFile
		target stack.Stack
		want   bool
	}{
		{"legacy snapshot", &StateFile{Snapshot: Snapshot{StackMetadata: metadata}}, original, true},
		{"legacy renamed snapshot", &StateFile{
			Snapshot: Snapshot{StackMetadata: metadata},
			Plan:     []Action{{Type: "rename", Branch: "A", NewName: "new-A"}},
		}, renamed, true},
		{"index and trunk are not identity", &StateFile{StackIndex: 0, StackName: "main"}, other, false},
		{"same trunk different stack", &StateFile{Snapshot: Snapshot{StackMetadata: metadata}}, other, false},
		{"conflicting remote identity", &StateFile{PriorRemoteStackID: "first"}, stack.Stack{ID: "second"}, false},
		{"matching remote identity", &StateFile{PriorRemoteStackID: "first"}, stack.Stack{ID: "first"}, true},
		{"nil state", nil, original, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchesStack(tt.state, &tt.target))
		})
	}
	t.Run("catalog publication boundary", func(t *testing.T) {
		state := &StateFile{Phase: PhaseApplying}
		state.RecordStack(&original)
		state.RecordStack(&renamed)
		assert.True(t, MatchesStack(state, &original))
		assert.True(t, MatchesStack(state, &renamed))
		state.Phase = PhasePendingSubmit
		assert.False(t, MatchesStack(state, &original), "submit must match only the completed composition")
		assert.True(t, MatchesStack(state, &renamed))
	})
}

func TestContinueApply_RejectsUnidentifiableStack(t *testing.T) {
	dir := t.TempDir()
	target := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}}}
	other := stack.Stack{Trunk: target.Trunk, Branches: []stack.BranchRef{{Branch: "B"}}}
	writeTestStackFile(t, dir, other)
	state := &StateFile{SchemaVersion: 1, Phase: PhaseConflict, StackIndex: 0, ConflictBranch: "A"}
	state.RecordStack(&target)
	require.NoError(t, SaveState(dir, state))
	called := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:         func() (string, error) { return dir, nil },
		RebaseContinueFn: func(git.RebaseOpts) error { called = true; return nil },
	})
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.ErrorContains(t, ContinueApply(cfg, dir, noopUpdateBaseSHAs), "recorded by modify was not found")
	assert.False(t, called)
	assert.True(t, StateExists(dir))
}

func TestContinueApply_RejectsUnpublishedCatalogChange(t *testing.T) {
	dir := t.TempDir()
	original := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
	}
	modified := original
	modified.Branches = []stack.BranchRef{{Branch: "B"}}
	writeTestStackFile(t, dir, original)
	state := &StateFile{SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase", ConflictBranch: "B"}
	state.RecordStack(&original)
	state.RecordStack(&modified)
	require.NoError(t, SaveState(dir, state))
	called := false
	restore := git.SetOps(&git.MockOps{
		RebaseContinueFn: func(git.RebaseOpts) error { called = true; return nil },
	})
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.ErrorContains(t, ContinueApply(cfg, dir, noopUpdateBaseSHAs), "catalog update did not complete")
	assert.False(t, called, "continuation must not resurrect a branch omitted by an unpublished catalog update")
	assert.True(t, StateExists(dir))
}

func TestContinueApply_LegacyInsertedBranchCanAbort(t *testing.T) {
	dir := t.TempDir()
	original := stack.Stack{
		ID: "remote-stack", Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
	}
	modified := original
	modified.Branches = []stack.BranchRef{{Branch: "A"}, {Branch: "inserted"}, {Branch: "B"}}
	writeTestStackFile(t, dir, modified)
	metadata, err := json.Marshal(original)
	require.NoError(t, err)
	state := &StateFile{
		SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase", ConflictBranch: "inserted",
		PriorRemoteStackID: original.ID, OriginalBranch: "A", RemainingBranches: []string{"B"},
		OriginalRefs: map[string]string{"B": "sha-A"},
		Snapshot: Snapshot{
			StackMetadata: metadata,
			Branches:      []BranchSnapshot{{Name: "A", TipSHA: "sha-A"}, {Name: "B", TipSHA: "sha-B"}},
		},
		Plan: []Action{{Type: "insert_below", Branch: "inserted", NewName: "inserted", NewPosition: 1}},
	}
	require.NoError(t, SaveState(dir, state))
	refs := map[string]string{"main": "sha-main", "A": "sha-A", "B": "sha-B", "inserted": "sha-inserted"}
	mock := newApplyMock(dir, refs)
	inProgress := true
	mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
	mock.RebaseContinueFn = func(git.RebaseOpts) error { inProgress = false; return nil }
	mock.RebaseOntoFn = func(string, string, string, git.RebaseOpts) error {
		inProgress = true
		return assert.AnError
	}
	mock.RebaseAbortFn = func() error { inProgress = false; return nil }
	mock.DeleteBranchFn = func(name string, _ bool) error { delete(refs, name); return nil }
	restore := git.SetOps(mock)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.Error(t, ContinueApply(cfg, dir, noopUpdateBaseSHAs))
	require.NoError(t, UnwindFromStateFile(cfg, dir))
	exists, err := mock.BranchExists("inserted")
	require.NoError(t, err)
	assert.False(t, exists)
	assert.False(t, StateExists(dir))
	saved, err := stack.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B"}, saved.Stacks[0].BranchNames())
}

func TestApplyPlan_RenameFailureUnwindsWithoutNestedLock(t *testing.T) {
	dir := t.TempDir()
	s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}}}
	sf := writeTestStackFile(t, dir, s)
	mock := newApplyMock(dir, map[string]string{"main": "base", "A": "original"})
	mock.RenameBranchFn = func(string, string) error {
		lock, err := stack.Lock(dir)
		require.NoError(t, err, "Git mutations must not hold the catalog lock")
		lock.Unlock()
		return assert.AnError
	}
	restore := git.SetOps(mock)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"}
	_, _, err := ApplyPlan(cfg, dir, &sf.Stacks[0], sf, nodes, "A", noopUpdateBaseSHAs)
	require.ErrorIs(t, err, assert.AnError)
	assert.False(t, StateExists(dir), "successful unwind should clear the recovery journal")
}

func TestUnwind_PartialFailureRetainsState(t *testing.T) {
	for _, failure := range []string{"abort", "reset", "catalog"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			origin := t.TempDir()
			s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}}}
			writeTestStackFile(t, dir, s)
			metadata, err := json.Marshal(s)
			require.NoError(t, err)
			state := &StateFile{
				SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase", OriginalBranch: "A",
				Snapshot: Snapshot{
					StackMetadata: metadata,
					Branches:      []BranchSnapshot{{Name: "A", TipSHA: "original"}},
				},
				Worktrees: &worktree.Context{
					Origin:  worktree.Location{Path: origin},
					Touched: map[string]string{"A": "changed"},
				},
			}
			state.RecordStack(&s)
			require.NoError(t, SaveState(dir, state))
			sha := "changed"
			mock := &git.MockOps{
				GitDirFn:             func() (string, error) { return dir, nil },
				RootDirFn:            func() (string, error) { return origin, nil },
				CurrentBranchFn:      func() (string, error) { return "A", nil },
				RevParseFn:           func(string) (string, error) { return sha, nil },
				IsRebaseInProgressFn: func() (bool, error) { return failure == "abort", nil },
				RebaseAbortFn:        func() error { return assert.AnError },
				ResetHardFn: func(value string) error {
					if failure == "reset" {
						return assert.AnError
					}
					sha = value
					external, err := stack.Load(dir)
					require.NoError(t, err)
					external.Stacks = append(external.Stacks, stack.Stack{
						Trunk: s.Trunk, Branches: []stack.BranchRef{{Branch: "other"}},
					})
					require.NoError(t, stack.Save(dir, external))
					return nil
				},
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			require.Error(t, UnwindFromStateFile(cfg, dir))
			saved, err := LoadState(dir)
			require.NoError(t, err)
			require.NotNil(t, saved)
			assert.Equal(t, PhaseApplying, saved.Phase)
			if failure == "catalog" {
				external, err := stack.Load(dir)
				require.NoError(t, err)
				assert.Len(t, external.Stacks, 2, "stale recovery must not overwrite another catalog writer")
			}
		})
	}
}

func runModifyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, output)
	return strings.TrimSpace(string(output))
}

func setupModifyWorktrees(t *testing.T, conflicting bool, initOptions ...string) (root, origin, caller, common string, sf *stack.StackFile) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Modify Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "modify@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Modify Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "modify@example.com")
	t.Setenv("GIT_EDITOR", "true")
	dir := t.TempDir()
	root, origin, caller = filepath.Join(dir, "repo"), filepath.Join(dir, "modify worktree"), filepath.Join(dir, "caller")
	require.NoError(t, os.Mkdir(root, 0755))
	runModifyGit(t, root, append([]string{"init", "-q", "-b", "main"}, initOptions...)...)
	writeCommit := func(file, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, file), []byte(content), 0644))
		runModifyGit(t, root, "add", file)
		runModifyGit(t, root, "commit", "-qm", content)
	}
	writeCommit("base.txt", "base\n")
	runModifyGit(t, root, "checkout", "-qb", "A")
	if conflicting {
		writeCommit("base.txt", "A\n")
	} else {
		writeCommit("a.txt", "A\n")
	}
	runModifyGit(t, root, "checkout", "-qb", "B")
	if conflicting {
		writeCommit("base.txt", "B\n")
	} else {
		writeCommit("b.txt", "B\n")
	}
	runModifyGit(t, root, "checkout", "-q", "main")
	runModifyGit(t, root, "worktree", "add", "-q", origin, "B")
	runModifyGit(t, root, "worktree", "add", "-q", "-b", "observer", caller, "main")
	common = runModifyGit(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	sf = writeTestStackFile(t, common, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}},
	})
	// Bootstrap an executor for this test repository without changing cwd.
	// ForWorktree then strips these variables and binds its own Git context.
	t.Setenv("GIT_DIR", common)
	t.Setenv("GIT_WORK_TREE", root)
	return
}

func TestApplyPlan_SeparateGitDirOrigin(t *testing.T) {
	for _, location := range []string{"main", "linked"} {
		t.Run(location, func(t *testing.T) {
			root, linked, caller, common, sf := setupModifyWorktrees(t, false, "--separate-git-dir", t.TempDir())
			origin := linked
			if location == "main" {
				runModifyGit(t, linked, "checkout", "-q", "--detach")
				runModifyGit(t, root, "checkout", "-q", "B")
				origin = root
			}
			restore := git.SetOps(requireWorktree(t, git.CurrentOps(), origin))
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			nodes := makeNodes(&sf.Stacks[0])
			nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-B"}
			_, conflict, err := ApplyPlan(cfg, common, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
			require.NoError(t, err, "the invoking worktree is known even when Git's main-owner path is not discoverable remotely")
			assert.Nil(t, conflict)
			assert.Equal(t, "new-B", runModifyGit(t, origin, "branch", "--show-current"))
			assert.Equal(t, "observer", runModifyGit(t, caller, "branch", "--show-current"))
			assert.False(t, StateExists(common))
		})
	}
}

func TestModifyRecovery_MissingSeparateGitDirOriginRetainsOriginalLocation(t *testing.T) {
	root, linked, caller, common, sf := setupModifyWorktrees(t, false, "--separate-git-dir", t.TempDir())
	runModifyGit(t, linked, "checkout", "-q", "--detach")
	runModifyGit(t, root, "checkout", "-q", "B")
	originOps := requireWorktree(t, git.CurrentOps(), root)
	callerOps := requireWorktree(t, originOps, caller)
	restore := git.SetOps(originOps)
	defer restore()
	ctx, err := CheckWorktrees(&sf.Stacks[0])
	require.NoError(t, err)
	snapshot, err := BuildSnapshot(&sf.Stacks[0])
	require.NoError(t, err)
	state := &StateFile{
		SchemaVersion: 1, Phase: PhaseApplying, OriginalBranch: "B",
		Worktrees: ctx, Snapshot: snapshot,
	}
	state.RecordStack(&sf.Stacks[0])
	require.NoError(t, SaveState(common, state))
	before, err := os.ReadFile(StatePath(common))
	require.NoError(t, err)
	require.NoError(t, os.Rename(root, root+"-moved"))
	restoreCaller := git.SetOps(callerOps)
	defer restoreCaller()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.Error(t, UnwindFromStateFile(cfg, common))
	after, err := os.ReadFile(StatePath(common))
	require.NoError(t, err)
	assert.Equal(t, before, after, "failed origin discovery must not replace the saved location with the administration directory")
	assert.Equal(t, "observer", runModifyGit(t, caller, "branch", "--show-current"))
}

func TestApplyPlan_LinkedWorktreeWithForeignTrunk(t *testing.T) {
	root, origin, caller, common, sf := setupModifyWorktrees(t, false)
	require.NoError(t, os.WriteFile(filepath.Join(caller, "note.txt"), []byte("keep"), 0644))
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[0].Removed = true
	result, conflict, err := ApplyPlan(cfg, common, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Nil(t, conflict)
	assert.Equal(t, []string{"B"}, sf.Stacks[0].BranchNames())
	assert.Equal(t, "B", runModifyGit(t, origin, "branch", "--show-current"))
	assert.Equal(t, "main", runModifyGit(t, root, "branch", "--show-current"))
	assert.Equal(t, "observer", runModifyGit(t, caller, "branch", "--show-current"))
	assert.Equal(t, "?? note.txt", runModifyGit(t, caller, "status", "--porcelain"))
	assert.False(t, StateExists(common))
}

func TestApplyPlan_DirtyDistributedOwnerRejectedBeforeMutation(t *testing.T) {
	root, origin, _, common, sf := setupModifyWorktrees(t, false)
	owner := filepath.Join(filepath.Dir(origin), "A owner")
	runModifyGit(t, root, "worktree", "add", "-q", owner, "A")
	require.NoError(t, os.WriteFile(filepath.Join(owner, "uncommitted.txt"), []byte("keep"), 0644))
	before, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	require.NoError(t, err)
	original := runModifyGit(t, root, "rev-parse", "B")
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-B"}
	_, _, err = ApplyPlan(cfg, common, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
	require.ErrorContains(t, err, "uncommitted changes")
	assert.Contains(t, err.Error(), "A owner")
	after, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, original, runModifyGit(t, root, "rev-parse", "B"))
	assert.Equal(t, "B", runModifyGit(t, origin, "branch", "--show-current"))
	assert.False(t, StateExists(common))
}

func TestModifyRecovery_FromAnotherWorktree(t *testing.T) {
	for _, name := range []string{"continue", "abort", "abort renamed origin"} {
		t.Run(name, func(t *testing.T) {
			root, origin, caller, common, sf := setupModifyWorktrees(t, true)
			cwd, err := os.Getwd()
			require.NoError(t, err)
			originalB := runModifyGit(t, root, "rev-parse", "B")
			observer := runModifyGit(t, caller, "rev-parse", "HEAD")
			require.NoError(t, os.WriteFile(filepath.Join(caller, "note.txt"), []byte("keep"), 0644))
			originOps := requireWorktree(t, git.CurrentOps(), origin)
			restore := git.SetOps(originOps)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			nodes := makeNodes(&sf.Stacks[0])
			nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
			nodes[0].Removed = true
			if name == "abort renamed origin" {
				nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-B"}
			}
			_, conflict, err := ApplyPlan(cfg, common, &sf.Stacks[0], sf, nodes, "B", noopUpdateBaseSHAs)
			require.Error(t, err)
			require.NotNil(t, conflict)
			state, err := LoadState(common)
			require.NoError(t, err)
			require.NotNil(t, state.Worktrees)
			assert.True(t, worktree.SamePath(origin, state.Worktrees.Origin.Path))
			assert.Equal(t, "B", state.OriginalBranch)
			assert.True(t, requireGitState(t, originOps.IsRebaseInProgress))
			callerOps := requireWorktree(t, originOps, caller)
			assert.False(t, requireGitState(t, callerOps.IsRebaseInProgress))
			restoreCaller := git.SetOps(callerOps)
			defer restoreCaller()
			if name == "continue" {
				require.NoError(t, os.WriteFile(filepath.Join(origin, "base.txt"), []byte("resolved\n"), 0644))
				runModifyGit(t, origin, "add", "base.txt")
				require.NoError(t, ContinueApply(cfg, common, noopUpdateBaseSHAs))
			} else {
				require.NoError(t, UnwindFromStateFile(cfg, common))
				assert.Equal(t, originalB, runModifyGit(t, origin, "rev-parse", "B"))
			}
			assert.False(t, StateExists(common))
			assert.False(t, requireGitState(t, originOps.IsRebaseInProgress))
			assert.Equal(t, "B", runModifyGit(t, origin, "branch", "--show-current"))
			assert.Equal(t, "", runModifyGit(t, origin, "status", "--porcelain"))
			assert.Equal(t, "main", runModifyGit(t, root, "branch", "--show-current"))
			assert.Equal(t, observer, runModifyGit(t, caller, "rev-parse", "HEAD"))
			assert.Equal(t, "?? note.txt", runModifyGit(t, caller, "status", "--porcelain"))
			afterCwd, err := os.Getwd()
			require.NoError(t, err)
			assert.Equal(t, cwd, afterCwd)
			recovered, err := stack.Load(common)
			require.NoError(t, err)
			if name == "continue" {
				assert.Equal(t, []string{"B"}, recovered.Stacks[0].BranchNames())
			} else {
				assert.Equal(t, []string{"A", "B"}, recovered.Stacks[0].BranchNames())
			}
		})
	}
}

func TestModifyRecovery_PreservesExternalCommitAfterSaveFailure(t *testing.T) {
	_, origin, caller, common, sf := setupModifyWorktrees(t, false)
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[0].Removed = true
	_, _, err := ApplyPlan(cfg, common, &sf.Stacks[0], sf, nodes, "B", func(*stack.Stack) {
		external, loadErr := stack.Load(common)
		require.NoError(t, loadErr)
		external.Stacks = append(external.Stacks, stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "observer"}},
		})
		require.NoError(t, stack.Save(common, external))
	})
	var stale *stack.StaleError
	require.ErrorAs(t, err, &stale)
	state, err := LoadState(common)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, PhaseApplying, state.Phase)
	require.NoError(t, os.WriteFile(filepath.Join(origin, "external.txt"), []byte("keep this commit\n"), 0644))
	runModifyGit(t, origin, "add", "external.txt")
	runModifyGit(t, origin, "commit", "-qm", "external change")
	externalTip := runModifyGit(t, origin, "rev-parse", "B")
	restoreCaller := git.SetOps(requireWorktree(t, git.CurrentOps(), caller))
	defer restoreCaller()
	require.ErrorContains(t, UnwindFromStateFile(cfg, common), "changed after this operation")
	assert.Equal(t, externalTip, runModifyGit(t, origin, "rev-parse", "B"))
	assert.True(t, StateExists(common))
	saved, err := stack.Load(common)
	require.NoError(t, err)
	assert.Len(t, saved.Stacks, 2)
}

func TestContinueApply_PreservesExternalCommitOnRemainingBranch(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "recorded context"
		if legacy {
			name = "legacy journal"
		}
		t.Run(name, func(t *testing.T) {
			dir, origin := t.TempDir(), t.TempDir()
			s := stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "B"}, {Branch: "C"},
				},
			}
			writeTestStackFile(t, dir, s)
			metadata, err := json.Marshal(s)
			require.NoError(t, err)
			state := &StateFile{
				SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase", ConflictBranch: "B",
				OriginalBranch: "B", RemainingBranches: []string{"C"},
				OriginalRefs: map[string]string{"C": "B-original"},
				Snapshot: Snapshot{
					StackMetadata: metadata,
					Branches: []BranchSnapshot{
						{Name: "B", TipSHA: "B-original"}, {Name: "C", TipSHA: "C-original"},
					},
				},
			}
			if !legacy {
				state.Worktrees = &worktree.Context{Origin: worktree.Location{Path: origin}}
			}
			state.RecordStack(&s)
			require.NoError(t, SaveState(dir, state))
			refs := map[string]string{"B": "B-original", "C": "C-external-commit"}
			inProgress := true
			var started, reset []string
			mock := newApplyMock(dir, refs)
			mock.RootDirFn = func() (string, error) { return origin, nil }
			mock.CurrentBranchFn = func() (string, error) { return "B", nil }
			mock.IsRebaseInProgressFn = func() (bool, error) { return inProgress, nil }
			mock.RebaseContinueFn = func(git.RebaseOpts) error {
				inProgress = false
				refs["B"] = "B-rebased"
				return nil
			}
			mock.RebaseAbortFn = func() error { inProgress = false; return nil }
			mock.RebaseOntoFn = func(_, _, branch string, _ git.RebaseOpts) error {
				started = append(started, branch)
				refs[branch] = "overwritten"
				return nil
			}
			mock.ResetHardFn = func(sha string) error {
				reset = append(reset, "B")
				refs["B"] = sha
				return nil
			}
			mock.UpdateBranchRefFn = func(branch, sha string) error {
				reset = append(reset, branch)
				refs[branch] = sha
				return nil
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()

			require.ErrorContains(t, ContinueApply(cfg, dir, noopUpdateBaseSHAs), "C changed since")
			assert.Empty(t, started, "the changed remaining branch must be rejected before starting its rebase")
			saved, err := LoadState(dir)
			require.NoError(t, err)
			require.NotNil(t, saved)
			if legacy {
				assert.Nil(t, saved.Worktrees, "legacy adoption must not claim an external change")
				require.ErrorContains(t, UnwindFromStateFile(cfg, dir), "C changed since")
				assert.Empty(t, reset)
				assert.True(t, inProgress, "ambiguous legacy recovery must stop before aborting Git")
				assert.True(t, StateExists(dir))
			} else {
				assert.NotContains(t, saved.Worktrees.Touched, "C")
				assert.Empty(t, saved.Worktrees.Pending)
				require.NoError(t, UnwindFromStateFile(cfg, dir))
				assert.Equal(t, []string{"B"}, reset)
				assert.False(t, StateExists(dir))
			}
			assert.Equal(t, "B-original", refs["B"])
			assert.Equal(t, "C-external-commit", refs["C"])
		})
	}
}

func TestStartRefMutation_UsesRecordedExpectedTip(t *testing.T) {
	for _, touched := range []bool{false, true} {
		name, expected := "original snapshot", "original"
		if touched {
			name, expected = "previously modified branch", "last-written"
		}
		t.Run(name, func(t *testing.T) {
			dir, origin := t.TempDir(), t.TempDir()
			state := &StateFile{
				SchemaVersion: 1, Phase: PhaseApplying,
				Snapshot:  Snapshot{Branches: []BranchSnapshot{{Name: "A", TipSHA: "original"}}},
				Worktrees: &worktree.Context{Origin: worktree.Location{Path: origin}},
			}
			if touched {
				state.Worktrees.Touched = map[string]string{"A": expected}
			}
			mock := newApplyMock(dir, map[string]string{"A": expected})
			mock.RootDirFn = func() (string, error) { return origin, nil }
			restore := git.SetOps(mock)
			defer restore()
			_, err := startRefMutation(dir, state, "A")
			require.NoError(t, err)
			assert.Equal(t, "A", state.Worktrees.Pending)
			assert.Equal(t, expected, state.Worktrees.PendingBefore)
		})
	}
}

type distributedModifyRepo struct {
	root, origin, caller, common string
	owners                       map[string]string
	refs                         map[string]string
	sf                           *stack.StackFile
}

func commitModifyFile(t *testing.T, dir, file, content, message string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0644))
	runModifyGit(t, dir, "add", file)
	runModifyGit(t, dir, "commit", "-qm", message)
}

func setupDistributedModify(t *testing.T, conflicting bool) distributedModifyRepo {
	t.Helper()
	root, origin, caller, common, sf := setupModifyWorktrees(t, conflicting)
	runModifyGit(t, origin, "checkout", "-qb", "C")
	if conflicting {
		commitModifyFile(t, origin, "base.txt", "C\n", "C")
	} else {
		commitModifyFile(t, origin, "c.txt", "C\n", "C")
	}
	sf.Stacks[0].Branches = append(sf.Stacks[0].Branches, stack.BranchRef{Branch: "C"})
	require.NoError(t, stack.Save(common, sf))
	repo := distributedModifyRepo{
		root: root, origin: origin, caller: caller, common: common, sf: sf,
		owners: map[string]string{"C": origin}, refs: make(map[string]string),
	}
	for _, branch := range []string{"A", "B"} {
		path := filepath.Join(filepath.Dir(origin), "owner "+branch)
		runModifyGit(t, root, "worktree", "add", "-q", path, branch)
		// Git expands Windows short paths and uses forward slashes in diagnostics.
		repo.owners[branch] = runModifyGit(t, path, "rev-parse", "--show-toplevel")
	}
	for _, branch := range []string{"A", "B", "C"} {
		repo.refs[branch] = runModifyGit(t, root, "rev-parse", branch)
	}
	return repo
}

func insertedModifyNode(name string) modifyview.ModifyBranchNode {
	return modifyview.ModifyBranchNode{
		BranchNode:       stackview.BranchNode{Ref: stack.BranchRef{Branch: name}},
		OriginalPosition: -1,
		IsInserted:       true,
		PendingAction:    &modifyview.PendingAction{Type: modifyview.ActionInsertBelow, NewName: name},
	}
}

func TestDistributedModify_AllActions(t *testing.T) {
	tests := []struct {
		name     string
		nodes    func([]modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode
		order    []string
		files    []string
		renamedA bool
		renamedB bool
	}{
		{
			name: "rename",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"}
				return nodes
			},
			order: []string{"new-A", "B", "C"}, files: []string{"a.txt", "b.txt", "base.txt", "c.txt"}, renamedA: true,
		},
		{
			name: "insert",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				return []modifyview.ModifyBranchNode{nodes[0], insertedModifyNode("inserted"), nodes[1], nodes[2]}
			},
			order: []string{"A", "inserted", "B", "C"}, files: []string{"a.txt", "b.txt", "base.txt", "c.txt"},
		},
		{
			name: "drop",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
				nodes[0].Removed = true
				return nodes
			},
			order: []string{"B", "C"}, files: []string{"b.txt", "base.txt", "c.txt"},
		},
		{
			name: "fold down",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldDown}
				nodes[1].Removed = true
				return nodes
			},
			order: []string{"A", "C"}, files: []string{"a.txt", "b.txt", "base.txt", "c.txt"},
		},
		{
			name: "fold up",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
				nodes[1].Removed = true
				return nodes
			},
			order: []string{"A", "C"}, files: []string{"a.txt", "b.txt", "base.txt", "c.txt"},
		},
		{
			name: "reorder",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				return []modifyview.ModifyBranchNode{nodes[2], nodes[1], nodes[0]}
			},
			order: []string{"C", "B", "A"}, files: []string{"a.txt", "b.txt", "base.txt", "c.txt"},
		},
		{
			name: "rename insert and drop",
			nodes: func(nodes []modifyview.ModifyBranchNode) []modifyview.ModifyBranchNode {
				nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
				nodes[0].Removed = true
				nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-B"}
				return []modifyview.ModifyBranchNode{nodes[0], nodes[1], insertedModifyNode("inserted"), nodes[2]}
			},
			order: []string{"new-B", "inserted", "C"}, files: []string{"b.txt", "base.txt", "c.txt"}, renamedB: true,
		},
	}
	for _, tt := range tests {
		for _, unoccupied := range []bool{false, true} {
			layout := "occupied"
			if unoccupied {
				layout = "mixed"
			}
			t.Run(tt.name+"/"+layout, func(t *testing.T) {
				repo := setupDistributedModify(t, false)
				if unoccupied {
					runModifyGit(t, repo.owners["B"], "checkout", "-qb", "b-observer", "main")
					require.NoError(t, os.WriteFile(filepath.Join(repo.owners["B"], "unrelated.txt"), []byte("keep"), 0644))
				}
				require.NoError(t, os.WriteFile(filepath.Join(repo.caller, "unrelated.txt"), []byte("keep"), 0644))
				beforeTrees := runModifyGit(t, repo.root, "worktree", "list", "--porcelain")
				restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
				defer restore()
				cfg, _, _ := config.NewTestConfig()
				defer cfg.Out.Close()
				defer cfg.Err.Close()
				result, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf,
					tt.nodes(makeNodes(&repo.sf.Stacks[0])), "C", noopUpdateBaseSHAs)
				require.NoError(t, err)
				assert.Nil(t, conflict)
				require.NotNil(t, result)
				saved, err := stack.Load(repo.common)
				require.NoError(t, err)
				assert.Equal(t, tt.order, saved.Stacks[0].BranchNames())
				top := tt.order[len(tt.order)-1]
				assert.Equal(t, tt.files, strings.Fields(runModifyGit(t, repo.root, "ls-tree", "--name-only", top)))
				for i, branch := range tt.order {
					parent := "main"
					if i > 0 {
						parent = tt.order[i-1]
					}
					runModifyGit(t, repo.root, "merge-base", "--is-ancestor", parent, branch)
				}
				aName, bName := "A", "B"
				if tt.renamedA {
					aName = "new-A"
				}
				if tt.renamedB {
					bName = "new-B"
				}
				if unoccupied {
					bName = "b-observer"
					assert.Equal(t, "?? unrelated.txt", runModifyGit(t, repo.owners["B"], "status", "--porcelain"))
				}
				assert.Equal(t, aName, runModifyGit(t, repo.owners["A"], "branch", "--show-current"))
				assert.Equal(t, bName, runModifyGit(t, repo.owners["B"], "branch", "--show-current"))
				assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
				assert.Equal(t, "observer", runModifyGit(t, repo.caller, "branch", "--show-current"))
				afterTrees := runModifyGit(t, repo.root, "worktree", "list", "--porcelain")
				assert.Equal(t, strings.Count(beforeTrees, "worktree "), strings.Count(afterTrees, "worktree "))
				if tt.name == "drop" {
					assert.Equal(t, repo.refs["A"], runModifyGit(t, repo.root, "rev-parse", "A"))
				}
				if tt.name == "fold down" || tt.name == "fold up" {
					assert.Equal(t, repo.refs["B"], runModifyGit(t, repo.root, "rev-parse", "B"))
				}
				if tt.name == "rename insert and drop" {
					assert.Equal(t, runModifyGit(t, repo.root, "rev-parse", "new-B"), runModifyGit(t, repo.root, "rev-parse", "inserted"),
						"an inserted empty layer must not resurrect the dropped parent's commits")
				}
				assert.False(t, StateExists(repo.common))
			})
		}
	}
}

func TestDistributedModify_RetainsOriginForForeignSurvivor(t *testing.T) {
	for _, action := range []modifyview.ActionType{modifyview.ActionDrop, modifyview.ActionFoldDown} {
		t.Run(string(action), func(t *testing.T) {
			repo := setupDistributedModify(t, false)
			restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
			defer restore()
			cfg, _, errR := config.NewTestConfig()
			nodes := makeNodes(&repo.sf.Stacks[0])
			nodes[2].PendingAction = &modifyview.PendingAction{Type: action}
			nodes[2].Removed = true
			_, _, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
			cfg.Out.Close()
			cfg.Err.Close()
			require.NoError(t, err)
			output, err := io.ReadAll(errR)
			require.NoError(t, err)
			assert.Contains(t, string(output), repo.owners["B"])
			assert.Contains(t, string(output), "Kept C")
			assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
			assert.Equal(t, repo.refs["C"], runModifyGit(t, repo.root, "rev-parse", "C"))
			assert.Equal(t, "B", runModifyGit(t, repo.owners["B"], "branch", "--show-current"))
			assert.Equal(t, []string{"A", "B"}, repo.sf.Stacks[0].BranchNames())
		})
	}
}

func TestDistributedModify_PreflightsAllTargets(t *testing.T) {
	for _, condition := range []string{"dirty", "busy"} {
		t.Run(condition, func(t *testing.T) {
			repo := setupDistributedModify(t, false)
			path := filepath.Join(repo.owners["B"], "uncommitted.txt")
			message := "uncommitted changes"
			if condition == "busy" {
				dir := runModifyGit(t, repo.owners["B"], "rev-parse", "--absolute-git-dir")
				path, message = filepath.Join(dir, "MERGE_HEAD"), "Git operation"
			}
			require.NoError(t, os.WriteFile(path, []byte("keep\n"), 0644))
			before, err := os.ReadFile(filepath.Join(repo.common, "gh-stack"))
			require.NoError(t, err)
			restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			nodes := makeNodes(&repo.sf.Stacks[0])
			nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"}
			_, _, err = ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
			require.ErrorContains(t, err, message)
			assert.Contains(t, err.Error(), repo.owners["B"])
			for branch, sha := range repo.refs {
				assert.Equal(t, sha, runModifyGit(t, repo.root, "rev-parse", branch))
			}
			after, err := os.ReadFile(filepath.Join(repo.common, "gh-stack"))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.False(t, StateExists(repo.common))
		})
	}
}

func TestDistributedModify_DropLeavesDirtySourceUntouched(t *testing.T) {
	repo := setupDistributedModify(t, false)
	require.NoError(t, os.WriteFile(filepath.Join(repo.owners["B"], "uncommitted.txt"), []byte("keep"), 0644))
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[1].Removed = true
	_, _, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Equal(t, repo.refs["B"], runModifyGit(t, repo.root, "rev-parse", "B"))
	assert.Equal(t, "?? uncommitted.txt", runModifyGit(t, repo.owners["B"], "status", "--porcelain"))
	assert.Equal(t, "B", runModifyGit(t, repo.owners["B"], "branch", "--show-current"))
}

func TestDistributedModify_RebaseRecovery(t *testing.T) {
	for _, scenario := range []string{"foreign conflicts", "same-tree remaining branch", "moved owner", "abort renamed owner"} {
		t.Run(scenario, func(t *testing.T) {
			repo := setupDistributedModify(t, true)
			if scenario == "same-tree remaining branch" {
				runModifyGit(t, repo.owners["B"], "checkout", "-qb", "b-observer", "main")
			}
			originOps := requireWorktree(t, git.CurrentOps(), repo.origin)
			restore := git.SetOps(originOps)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			nodes := makeNodes(&repo.sf.Stacks[0])
			nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
			nodes[0].Removed = true
			bName := "B"
			if scenario == "abort renamed owner" {
				bName = "new-B"
				nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: bName}
			}
			_, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
			require.Error(t, err)
			require.NotNil(t, conflict)
			assert.Equal(t, bName, conflict.Branch)
			state, err := LoadState(repo.common)
			require.NoError(t, err)
			require.NotNil(t, state)
			bPath := repo.owners["B"]
			if scenario == "same-tree remaining branch" {
				bPath = repo.origin
			}
			assert.True(t, worktree.SamePath(state.Worktrees.Location(bName).Path, bPath))
			assert.True(t, requireGitState(t, requireWorktree(t, originOps, bPath).IsRebaseInProgress))
			if scenario == "moved owner" {
				moved := bPath + "-moved"
				runModifyGit(t, repo.root, "worktree", "move", bPath, moved)
				bPath, repo.owners["B"] = moved, moved
			}
			restoreCaller := git.SetOps(requireWorktree(t, originOps, repo.caller))
			defer restoreCaller()
			if scenario == "abort renamed owner" {
				require.NoError(t, UnwindFromStateFile(cfg, repo.common))
				assert.Equal(t, "B", runModifyGit(t, repo.owners["B"], "branch", "--show-current"))
				for branch, sha := range repo.refs {
					assert.Equal(t, sha, runModifyGit(t, repo.root, "rev-parse", branch))
				}
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(bPath, "base.txt"), []byte("resolved B\n"), 0644))
				runModifyGit(t, bPath, "add", "base.txt")
				require.ErrorContains(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs), "rebase conflict on C")
				next, err := LoadState(repo.common)
				require.NoError(t, err)
				assert.Equal(t, "C", next.Worktrees.Pending)
				assert.True(t, worktree.SamePath(next.Worktrees.Location("C").Path, repo.origin))
				require.NoError(t, os.WriteFile(filepath.Join(repo.origin, "base.txt"), []byte("resolved C\n"), 0644))
				runModifyGit(t, repo.origin, "add", "base.txt")
				restoreAnother := git.SetOps(requireWorktree(t, originOps, repo.owners["A"]))
				defer restoreAnother()
				require.NoError(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs))
				saved, err := stack.Load(repo.common)
				require.NoError(t, err)
				assert.Equal(t, []string{"B", "C"}, saved.Stacks[0].BranchNames())
				runModifyGit(t, repo.root, "merge-base", "--is-ancestor", "B", "C")
			}
			assert.Equal(t, "A", runModifyGit(t, repo.owners["A"], "branch", "--show-current"))
			assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
			assert.Equal(t, "observer", runModifyGit(t, repo.caller, "branch", "--show-current"))
			assert.False(t, StateExists(repo.common))
		})
	}
}

func setupDistributedFoldConflict(t *testing.T) distributedModifyRepo {
	t.Helper()
	repo := setupDistributedModify(t, true)
	runModifyGit(t, repo.origin, "checkout", "-qb", "D")
	commitModifyFile(t, repo.origin, "base.txt", "D\n", "D")
	path := filepath.Join(filepath.Dir(repo.origin), "owner C")
	runModifyGit(t, repo.root, "worktree", "add", "-q", path, "C")
	repo.owners["C"], repo.owners["D"] = path, repo.origin
	repo.refs["D"] = runModifyGit(t, repo.root, "rev-parse", "D")
	repo.sf.Stacks[0].Branches = append(repo.sf.Stacks[0].Branches, stack.BranchRef{Branch: "D"})
	require.NoError(t, stack.Save(repo.common, repo.sf))
	return repo
}

func distributedFoldConflictNodes(s *stack.Stack) []modifyview.ModifyBranchNode {
	nodes := makeNodes(s)
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[1].Removed = true
	nodes[2].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldDown}
	nodes[2].Removed = true
	nodes[3].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-D"}
	return append(nodes, insertedModifyNode("inserted"))
}

func TestDistributedModify_FoldThenRebaseRecovery(t *testing.T) {
	for _, outcome := range []string{"continue", "abort"} {
		t.Run(outcome, func(t *testing.T) {
			repo := setupDistributedFoldConflict(t)
			originOps := requireWorktree(t, git.CurrentOps(), repo.origin)
			restore := git.SetOps(originOps)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			_, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf,
				distributedFoldConflictNodes(&repo.sf.Stacks[0]), "D", noopUpdateBaseSHAs)
			require.Error(t, err)
			require.NotNil(t, conflict)
			assert.Equal(t, "C", conflict.Branch)
			state, err := LoadState(repo.common)
			require.NoError(t, err)
			assert.Equal(t, "cherry_pick", state.ConflictType)
			assert.Equal(t, "A", state.Worktrees.Pending)
			assert.Equal(t, 2, state.NextAction, "rename and insertion must already be checkpointed")
			assert.True(t, worktree.SamePath(state.Worktrees.Location("A").Path, repo.owners["A"]))
			restoreCaller := git.SetOps(requireWorktree(t, originOps, repo.caller))
			defer restoreCaller()
			require.NoError(t, os.WriteFile(filepath.Join(repo.owners["A"], "base.txt"), []byte("resolved C\n"), 0644))
			runModifyGit(t, repo.owners["A"], "add", "base.txt")
			dirtyPath := filepath.Join(repo.origin, "unrelated.txt")
			require.NoError(t, os.WriteFile(dirtyPath, []byte("keep"), 0644))
			require.ErrorContains(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs), "uncommitted changes")
			assert.True(t, requireGitState(t, requireWorktree(t, originOps, repo.owners["A"]).IsCherryPickInProgress),
				"other target worktrees must be preflighted before finishing the pending native operation")
			require.NoError(t, os.Remove(dirtyPath))
			require.ErrorContains(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs), "rebase conflict on new-D")
			state, err = LoadState(repo.common)
			require.NoError(t, err)
			assert.Equal(t, "rebase", state.ConflictType)
			assert.Equal(t, "new-D", state.Worktrees.Pending)
			assert.Equal(t, len(state.Execution), state.NextAction)
			saved, err := stack.Load(repo.common)
			require.NoError(t, err)
			assert.Equal(t, []string{"A", "new-D", "inserted"}, saved.Stacks[0].BranchNames(),
				"continuation must run the drop after the fold, not resurrect its source branches")
			if outcome == "abort" {
				require.NoError(t, UnwindFromStateFile(cfg, repo.common))
				for branch, sha := range repo.refs {
					assert.Equal(t, sha, runModifyGit(t, repo.root, "rev-parse", branch))
				}
				for _, name := range []string{"new-D", "inserted"} {
					exists, err := originOps.BranchExists(name)
					require.NoError(t, err)
					assert.False(t, exists)
				}
				assert.Equal(t, "D", runModifyGit(t, repo.origin, "branch", "--show-current"))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(repo.origin, "base.txt"), []byte("resolved D\n"), 0644))
				runModifyGit(t, repo.origin, "add", "base.txt")
				restoreSource := git.SetOps(requireWorktree(t, originOps, repo.owners["C"]))
				defer restoreSource()
				require.NoError(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs))
				assert.Equal(t, "new-D", runModifyGit(t, repo.origin, "branch", "--show-current"))
				assert.Equal(t, runModifyGit(t, repo.root, "rev-parse", "new-D"), runModifyGit(t, repo.root, "rev-parse", "inserted"))
			}
			for _, branch := range []string{"A", "B", "C"} {
				assert.Equal(t, branch, runModifyGit(t, repo.owners[branch], "branch", "--show-current"))
			}
			for _, branch := range []string{"B", "C"} {
				assert.Equal(t, repo.refs[branch], runModifyGit(t, repo.root, "rev-parse", branch),
					"dropped/folded source refs must remain intact")
			}
			assert.Equal(t, "observer", runModifyGit(t, repo.caller, "branch", "--show-current"))
			assert.False(t, StateExists(repo.common))
		})
	}
}

func TestDistributedModify_PreservesNewCommitOnRemainingOwner(t *testing.T) {
	repo := setupDistributedModify(t, true)
	originOps := requireWorktree(t, git.CurrentOps(), repo.origin)
	restore := git.SetOps(originOps)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[0].Removed = true
	_, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
	require.Error(t, err)
	require.NotNil(t, conflict)
	commitModifyFile(t, repo.origin, "external.txt", "keep this\n", "external commit while modify paused")
	external := runModifyGit(t, repo.root, "rev-parse", "C")
	restoreCaller := git.SetOps(requireWorktree(t, originOps, repo.caller))
	defer restoreCaller()
	require.ErrorContains(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs), "C changed since")
	assert.True(t, requireGitState(t, requireWorktree(t, originOps, repo.owners["B"]).IsRebaseInProgress))
	saved, err := LoadState(repo.common)
	require.NoError(t, err)
	assert.NotContains(t, saved.Worktrees.Touched, "C")
	require.NoError(t, UnwindFromStateFile(cfg, repo.common))
	assert.Equal(t, external, runModifyGit(t, repo.root, "rev-parse", "C"))
	assert.Equal(t, repo.refs["B"], runModifyGit(t, repo.root, "rev-parse", "B"))
	assert.False(t, StateExists(repo.common))
}

func TestDistributedModify_MetadataSaveFailureRetainsRecovery(t *testing.T) {
	repo := setupDistributedModify(t, false)
	originOps := requireWorktree(t, git.CurrentOps(), repo.origin)
	restore := git.SetOps(originOps)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-A"}
	_, _, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", func(*stack.Stack) {
		external, err := stack.Load(repo.common)
		require.NoError(t, err)
		external.Stacks = append(external.Stacks, stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "observer"}},
		})
		require.NoError(t, stack.Save(repo.common, external))
	})
	var stale *stack.StaleError
	require.ErrorAs(t, err, &stale)
	saved, err := LoadState(repo.common)
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, PhaseApplying, saved.Phase)
	assert.Equal(t, "new-A", runModifyGit(t, repo.owners["A"], "branch", "--show-current"))
	restoreCaller := git.SetOps(requireWorktree(t, originOps, repo.caller))
	defer restoreCaller()
	require.NoError(t, UnwindFromStateFile(cfg, repo.common))
	assert.Equal(t, "A", runModifyGit(t, repo.owners["A"], "branch", "--show-current"))
	assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
	final, err := stack.Load(repo.common)
	require.NoError(t, err)
	require.Len(t, final.Stacks, 2)
	assert.Equal(t, []string{"A", "B", "C"}, final.Stacks[0].BranchNames())
	assert.Equal(t, []string{"observer"}, final.Stacks[1].BranchNames())
	assert.False(t, StateExists(repo.common))
}

func TestDistributedModify_AbortPreservesChangedPendingRef(t *testing.T) {
	repo := setupDistributedModify(t, true)
	originOps := requireWorktree(t, git.CurrentOps(), repo.origin)
	restore := git.SetOps(originOps)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[0].Removed = true
	_, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
	require.Error(t, err)
	require.NotNil(t, conflict)
	tree := runModifyGit(t, repo.root, "rev-parse", "B^{tree}")
	external := runModifyGit(t, repo.root, "commit-tree", tree, "-p", "B", "-m", "external commit")
	runModifyGit(t, repo.root, "update-ref", "refs/heads/B", external)
	restoreCaller := git.SetOps(requireWorktree(t, originOps, repo.caller))
	defer restoreCaller()
	require.ErrorContains(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs), "changed after modify paused")
	require.ErrorContains(t, UnwindFromStateFile(cfg, repo.common), "changed after modify paused")
	assert.Equal(t, external, runModifyGit(t, repo.root, "rev-parse", "B"))
	assert.True(t, requireGitState(t, requireWorktree(t, originOps, repo.owners["B"]).IsRebaseInProgress),
		"abort must reject the changed branch ref before native Git can reset it")
	assert.True(t, StateExists(repo.common))
}

func TestDistributedModify_MultipleFoldsKeepOriginalCutoffs(t *testing.T) {
	for _, direction := range []modifyview.ActionType{modifyview.ActionFoldDown, modifyview.ActionFoldUp} {
		t.Run(string(direction), func(t *testing.T) {
			repo := setupDistributedModify(t, false)
			original := "C"
			nodes := makeNodes(&repo.sf.Stacks[0])
			if direction == modifyview.ActionFoldDown {
				nodes[1].PendingAction = &modifyview.PendingAction{Type: direction}
				nodes[1].Removed = true
				nodes[2].PendingAction = &modifyview.PendingAction{Type: direction}
				nodes[2].Removed = true
			} else {
				nodes[0].PendingAction = &modifyview.PendingAction{Type: direction}
				nodes[0].Removed = true
				nodes[1].PendingAction = &modifyview.PendingAction{Type: direction}
				nodes[1].Removed = true
			}
			restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			_, _, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, original, noopUpdateBaseSHAs)
			require.NoError(t, err)
			receiver := "C"
			if direction == modifyview.ActionFoldDown {
				receiver = "A"
			}
			assert.Equal(t, []string{receiver}, repo.sf.Stacks[0].BranchNames())
			assert.Equal(t, []string{"a.txt", "b.txt", "base.txt", "c.txt"},
				strings.Fields(runModifyGit(t, repo.root, "ls-tree", "--name-only", receiver)))
			assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
			assert.Equal(t, "A", runModifyGit(t, repo.owners["A"], "branch", "--show-current"))
			assert.Equal(t, "B", runModifyGit(t, repo.owners["B"], "branch", "--show-current"))
		})
	}
}

func TestDistributedModify_MergedOwnerIsNotTouched(t *testing.T) {
	repo := setupDistributedModify(t, false)
	repo.sf.Stacks[0].Branches[0].PullRequest = &stack.PullRequestRef{Number: 1, Merged: true}
	require.NoError(t, stack.Save(repo.common, repo.sf))
	require.NoError(t, os.WriteFile(filepath.Join(repo.owners["A"], "uncommitted.txt"), []byte("keep"), 0644))
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes[2].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionRename, NewName: "new-C"}
	_, _, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Equal(t, repo.refs["A"], runModifyGit(t, repo.root, "rev-parse", "A"))
	assert.Equal(t, "?? uncommitted.txt", runModifyGit(t, repo.owners["A"], "status", "--porcelain"))
	assert.Equal(t, "A", runModifyGit(t, repo.owners["A"], "branch", "--show-current"))
}

func TestUnwind_RetryAfterCreatedRefCleanupSaveFailure(t *testing.T) {
	dir, origin := t.TempDir(), t.TempDir()
	original := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}}}
	modified := original
	modified.Branches = append([]stack.BranchRef{}, original.Branches...)
	modified.Branches = append(modified.Branches, stack.BranchRef{Branch: "inserted"})
	writeTestStackFile(t, dir, modified)
	metadata, err := json.Marshal(original)
	require.NoError(t, err)
	state := &StateFile{
		SchemaVersion: 1, Phase: PhaseApplying, OriginalBranch: "A",
		Snapshot: Snapshot{StackMetadata: metadata, Branches: []BranchSnapshot{{Name: "A", TipSHA: "sha-A"}}},
		Worktrees: &worktree.Context{
			Origin: worktree.Location{Path: origin}, Touched: map[string]string{"inserted": "sha-created"},
		},
		CreatedBranches: map[string]string{"inserted": "sha-created"},
	}
	state.RecordStack(&modified)
	require.NoError(t, SaveState(dir, state))
	refs := map[string]string{"A": "sha-A", "inserted": "sha-created"}
	mock := newApplyMock(dir, refs)
	mock.RootDirFn = func() (string, error) { return origin, nil }
	mock.CurrentBranchFn = func() (string, error) { return "A", nil }
	backup := StatePath(dir) + ".before-failure"
	mock.DeleteBranchFn = func(name string, _ bool) error {
		delete(refs, name)
		require.NoError(t, os.Rename(StatePath(dir), backup))
		require.NoError(t, os.Mkdir(StatePath(dir), 0755))
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.Error(t, UnwindFromStateFile(cfg, dir))
	require.NoError(t, os.Remove(StatePath(dir)))
	require.NoError(t, os.Rename(backup, StatePath(dir)))
	saved, err := LoadState(dir)
	require.NoError(t, err)
	assert.Empty(t, saved.Worktrees.Touched, "ref restoration must be durable before created refs are deleted")
	mock.DeleteBranchFn = func(string, bool) error {
		t.Fatal("already deleted operation-created ref must not be deleted again")
		return nil
	}
	require.NoError(t, UnwindFromStateFile(cfg, dir))
	assert.False(t, StateExists(dir))
	final, err := stack.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"A"}, final.Stacks[0].BranchNames())
}

func TestContinueApply_RejectsMergedPendingBranch(t *testing.T) {
	dir := t.TempDir()
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}}},
	}
	writeTestStackFile(t, dir, s)
	state := &StateFile{SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase", ConflictBranch: "A"}
	state.RecordStack(&s)
	require.NoError(t, SaveState(dir, state))
	called := false
	restore := git.SetOps(&git.MockOps{
		RebaseContinueFn: func(git.RebaseOpts) error { called = true; return nil },
	})
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.ErrorContains(t, ContinueApply(cfg, dir, noopUpdateBaseSHAs), "merged or queued branch A")
	assert.False(t, called)
	assert.True(t, StateExists(dir))
}

func TestModifyState_RejectsInvalidActionProgress(t *testing.T) {
	for _, next := range []int{-1, 2} {
		t.Run(fmt.Sprint(next), func(t *testing.T) {
			dir := t.TempDir()
			state := &StateFile{
				SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "actions",
				Execution: []Action{{Type: "drop", Branch: "A"}}, NextAction: next, DesiredOrder: []string{"B"},
			}
			require.NoError(t, SaveState(dir, state))
			_, err := LoadState(dir)
			require.ErrorContains(t, err, "invalid action progress")
			assert.True(t, StateExists(dir))
		})
	}
}

func TestContinueApply_RejectsChangedPendingBase(t *testing.T) {
	dir, origin := t.TempDir(), t.TempDir()
	s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}}}
	writeTestStackFile(t, dir, s)
	state := &StateFile{
		SchemaVersion: 1, Phase: PhaseConflict, ConflictType: "rebase", ConflictBranch: "B",
		Snapshot: Snapshot{Branches: []BranchSnapshot{{Name: "A", TipSHA: "original-A"}, {Name: "B", TipSHA: "original-B"}}},
		Worktrees: &worktree.Context{
			Origin: worktree.Location{Path: origin}, Touched: map[string]string{"A": "rewritten-A"},
			Pending: "B", PendingBefore: "original-B",
		},
	}
	state.RecordStack(&s)
	require.NoError(t, SaveState(dir, state))
	mock := newApplyMock(dir, map[string]string{"A": "external-A", "B": "original-B"})
	mock.RootDirFn = func() (string, error) { return origin, nil }
	mock.IsRebaseInProgressFn = func() (bool, error) { return true, nil }
	continued := false
	mock.RebaseContinueFn = func(git.RebaseOpts) error { continued = true; return nil }
	restore := git.SetOps(mock)
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.ErrorContains(t, ContinueApply(cfg, dir, noopUpdateBaseSHAs), "A changed since")
	assert.False(t, continued)
	assert.True(t, StateExists(dir))
}

func TestApplyPlan_RejectsReorderedFoldBeforeMutation(t *testing.T) {
	repo := setupDistributedModify(t, false)
	before, err := os.ReadFile(filepath.Join(repo.common, "gh-stack"))
	require.NoError(t, err)
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes = []modifyview.ModifyBranchNode{nodes[1], nodes[0], nodes[2]}
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
	nodes[0].Removed = true

	result, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
	if err == nil {
		t.Logf("unchecked mixed plan left A=%s, main=%s, C files=%q",
			runModifyGit(t, repo.root, "rev-parse", "A"),
			runModifyGit(t, repo.root, "rev-parse", "main"),
			runModifyGit(t, repo.root, "ls-tree", "--name-only", "C"))
	}
	assert.ErrorContains(t, err, "cannot mix reordering")
	assert.Nil(t, result)
	assert.Nil(t, conflict)
	for branch, sha := range repo.refs {
		assert.Equal(t, sha, runModifyGit(t, repo.root, "rev-parse", branch), branch+" must not move")
		assert.Equal(t, branch, runModifyGit(t, repo.owners[branch], "branch", "--show-current"))
	}
	assert.Equal(t, []string{"a.txt", "b.txt", "base.txt", "c.txt"},
		strings.Fields(runModifyGit(t, repo.root, "ls-tree", "--name-only", "C")))
	after, err := os.ReadFile(filepath.Join(repo.common, "gh-stack"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "an invalid mixed plan must not change the catalog")
	assert.False(t, StateExists(repo.common), "an invalid mixed plan must not leave a modify journal")
}

func TestCompileActions_ReorderStructureExclusivity(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "A"}, {Branch: "B"}, {Branch: "C"}},
	}
	restore := git.SetOps(&git.MockOps{})
	defer restore()
	for _, action := range []modifyview.ActionType{
		modifyview.ActionDrop, modifyview.ActionFoldDown, modifyview.ActionFoldUp,
		modifyview.ActionRename, modifyview.ActionInsertBelow, modifyview.ActionInsertAbove,
	} {
		t.Run("reject "+string(action), func(t *testing.T) {
			nodes := makeNodes(&s)
			nodes = []modifyview.ModifyBranchNode{nodes[1], nodes[0], nodes[2]}
			switch action {
			case modifyview.ActionInsertBelow, modifyview.ActionInsertAbove:
				inserted := insertedModifyNode("inserted")
				inserted.PendingAction.Type = action
				nodes = append(nodes[:2], inserted, nodes[2])
			case modifyview.ActionRename:
				nodes[0].PendingAction = &modifyview.PendingAction{Type: action, NewName: "renamed"}
			default:
				nodes[0].PendingAction = &modifyview.PendingAction{Type: action}
				nodes[0].Removed = true
			}
			_, _, err := compileActions(&s, nodes)
			require.ErrorContains(t, err, "cannot mix reordering")
		})
	}

	t.Run("pure reorder", func(t *testing.T) {
		nodes := makeNodes(&s)
		nodes[0], nodes[1] = nodes[1], nodes[0]
		execution, desired, err := compileActions(&s, nodes)
		require.NoError(t, err)
		assert.Empty(t, execution)
		assert.Equal(t, []string{"B", "A", "C"}, desired)
	})
	t.Run("insertion shifts positions without reordering", func(t *testing.T) {
		nodes := makeNodes(&s)
		for i := range nodes {
			nodes[i].OriginalPosition = len(nodes) - 1 - i
		}
		nodes = []modifyview.ModifyBranchNode{nodes[0], insertedModifyNode("inserted"), nodes[1], nodes[2]}
		_, desired, err := compileActions(&s, nodes)
		require.NoError(t, err)
		assert.Equal(t, []string{"A", "inserted", "B", "C"}, desired)
	})
	t.Run("fold with original TUI display positions", func(t *testing.T) {
		nodes := makeNodes(&s)
		for i := range nodes {
			nodes[i].OriginalPosition = len(nodes) - 1 - i
		}
		nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
		nodes[1].Removed = true
		execution, desired, err := compileActions(&s, nodes)
		require.NoError(t, err)
		require.Len(t, execution, 1)
		assert.Equal(t, "C", execution[0].Target)
		assert.Equal(t, []string{"A", "C"}, desired)
	})
}

func TestDistributedModify_FoldUpAcrossDropExcludesDroppedHistory(t *testing.T) {
	repo := setupDistributedModify(t, false)
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	nodes := makeNodes(&repo.sf.Stacks[0])
	nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
	nodes[1].Removed = true
	nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
	nodes[0].Removed = true
	_, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "C", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Nil(t, conflict)
	assert.Equal(t, []string{"C"}, repo.sf.Stacks[0].BranchNames())
	assert.Equal(t, []string{"a.txt", "base.txt", "c.txt"},
		strings.Fields(runModifyGit(t, repo.root, "ls-tree", "--name-only", "C")))
	assert.Equal(t, []string{"A", "C"},
		strings.Split(runModifyGit(t, repo.root, "log", "--reverse", "--format=%s", "main..C"), "\n"))
	for _, source := range []string{"A", "B"} {
		assert.Equal(t, repo.refs[source], runModifyGit(t, repo.root, "rev-parse", source))
		assert.Equal(t, source, runModifyGit(t, repo.owners[source], "branch", "--show-current"))
		assert.Equal(t, "", runModifyGit(t, repo.owners[source], "status", "--porcelain"))
	}
	assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
	assert.False(t, StateExists(repo.common))
}

func TestDistributedModify_FoldUpDropNormalizationRecovery(t *testing.T) {
	for _, outcome := range []string{"continue", "abort"} {
		t.Run(outcome, func(t *testing.T) {
			repo := setupDistributedModify(t, true)
			invoker := requireWorktree(t, git.CurrentOps(), repo.owners["A"])
			restore := git.SetOps(invoker)
			defer restore()
			cfg, _, _ := config.NewTestConfig()
			defer cfg.Out.Close()
			defer cfg.Err.Close()
			nodes := makeNodes(&repo.sf.Stacks[0])
			nodes[1].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
			nodes[1].Removed = true
			nodes[0].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
			nodes[0].Removed = true
			_, conflict, err := ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "A", noopUpdateBaseSHAs)
			require.Error(t, err)
			require.NotNil(t, conflict)
			assert.Equal(t, "C", conflict.Branch)
			state, err := LoadState(repo.common)
			require.NoError(t, err)
			require.NotNil(t, state.PendingAction)
			assert.Equal(t, "fold_rebase", state.PendingAction.Type)
			assert.Equal(t, 0, state.NextAction)
			assert.Equal(t, repo.refs["B"], state.OriginalRefs["C"], "do not widen the cutoff before normalization completes")
			assert.True(t, worktree.SamePath(state.Worktrees.Location("C").Path, repo.origin))
			assert.True(t, requireGitState(t, requireWorktree(t, invoker, repo.origin).IsRebaseInProgress))
			saved, err := stack.Load(repo.common)
			require.NoError(t, err)
			assert.Equal(t, []string{"A", "B", "C"}, saved.Stacks[0].BranchNames())
			restoreCaller := git.SetOps(requireWorktree(t, invoker, repo.caller))
			defer restoreCaller()
			if outcome == "abort" {
				require.NoError(t, UnwindFromStateFile(cfg, repo.common))
				for branch, sha := range repo.refs {
					assert.Equal(t, sha, runModifyGit(t, repo.root, "rev-parse", branch))
				}
				assert.Equal(t, "A\nB\nC", runModifyGit(t, repo.root, "log", "--reverse", "--format=%s", "main..C"))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(repo.origin, "base.txt"), []byte("C\n"), 0644))
				runModifyGit(t, repo.origin, "add", "base.txt")
				require.NoError(t, ContinueApply(cfg, repo.common, noopUpdateBaseSHAs))
				assert.Equal(t, "C", runModifyGit(t, repo.root, "show", "C:base.txt", "--"))
				assert.Equal(t, "A\nC", runModifyGit(t, repo.root, "log", "--reverse", "--format=%s", "main..C"))
				saved, err = stack.Load(repo.common)
				require.NoError(t, err)
				assert.Equal(t, []string{"C"}, saved.Stacks[0].BranchNames())
			}
			for _, source := range []string{"A", "B"} {
				assert.Equal(t, repo.refs[source], runModifyGit(t, repo.root, "rev-parse", source))
				assert.Equal(t, source, runModifyGit(t, repo.owners[source], "branch", "--show-current"))
			}
			assert.Equal(t, "C", runModifyGit(t, repo.origin, "branch", "--show-current"))
			assert.False(t, requireGitState(t, requireWorktree(t, invoker, repo.origin).IsRebaseInProgress))
			assert.False(t, StateExists(repo.common))
		})
	}
}

func TestDistributedModify_FoldUpAcrossMultipleDroppedRanges(t *testing.T) {
	repo := setupDistributedModify(t, false)
	runModifyGit(t, repo.origin, "checkout", "-qb", "D")
	commitModifyFile(t, repo.origin, "d.txt", "D\n", "D")
	runModifyGit(t, repo.origin, "checkout", "-qb", "E")
	commitModifyFile(t, repo.origin, "e.txt", "E\n", "E")
	repo.sf.Stacks[0].Branches = append(repo.sf.Stacks[0].Branches, stack.BranchRef{Branch: "D"}, stack.BranchRef{Branch: "E"})
	require.NoError(t, stack.Save(repo.common, repo.sf))
	nodes := makeNodes(&repo.sf.Stacks[0])
	for _, index := range []int{1, 3} {
		nodes[index].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionDrop}
		nodes[index].Removed = true
	}
	for _, index := range []int{0, 2} {
		nodes[index].PendingAction = &modifyview.PendingAction{Type: modifyview.ActionFoldUp}
		nodes[index].Removed = true
	}
	restore := git.SetOps(requireWorktree(t, git.CurrentOps(), repo.origin))
	defer restore()
	execution, _, err := compileActions(&repo.sf.Stacks[0], nodes)
	require.NoError(t, err)
	var excluded []string
	for _, action := range execution {
		if action.Type == "fold_rebase" {
			excluded = append(excluded, action.Target)
		}
	}
	assert.Equal(t, []string{"D", "B"}, excluded, "normalize each dropped range once, from top to bottom")
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	_, _, err = ApplyPlan(cfg, repo.common, &repo.sf.Stacks[0], repo.sf, nodes, "E", noopUpdateBaseSHAs)
	require.NoError(t, err)
	assert.Equal(t, []string{"E"}, repo.sf.Stacks[0].BranchNames())
	assert.Equal(t, []string{"a.txt", "base.txt", "c.txt", "e.txt"},
		strings.Fields(runModifyGit(t, repo.root, "ls-tree", "--name-only", "E")))
	assert.Equal(t, "A\nC\nE", runModifyGit(t, repo.root, "log", "--reverse", "--format=%s", "main..E"))
}
