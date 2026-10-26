package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdd_ForeignAdoptionAndCommitPreflight(t *testing.T) {
	tests := []struct {
		name      string
		opts      addOptions
		empty     bool
		noStack   bool
		wantError bool
	}{
		{name: "metadata adoption"},
		{name: "message", opts: addOptions{message: "commit"}, wantError: true},
		{name: "stage all", opts: addOptions{stageAll: true}, wantError: true},
		{name: "stage tracked", opts: addOptions{stageTracked: true}, wantError: true},
		{name: "empty layer shortcut", opts: addOptions{stageAll: true, message: "commit"}, empty: true, wantError: true},
		{name: "initialize from add", opts: addOptions{stageAll: true, message: "commit"}, noStack: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common, local, root, owner := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			if !tt.noStack {
				saveStack(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}}})
			}
			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return local, nil },
				CommonDirFn:     func() (string, error) { return common, nil },
				RootDirFn:       func() (string, error) { return root, nil },
				CurrentBranchFn: func() (string, error) { return "b1", nil },
				BranchExistsFn:  func(string) (bool, error) { return true, nil },
				WorktreesFn:     func() ([]git.Worktree, error) { return []git.Worktree{{Path: owner, Branch: "b2"}}, nil },
				RevParseMultiFn: func([]string) ([]string, error) {
					if tt.empty {
						return []string{"same", "same"}, nil
					}
					return []string{"parent", "current"}, nil
				},
				MergeBaseFn: func(string, string) (string, error) { return "adopted-base", nil },
				StageAllFn: func() error {
					t.Fatal("must reject before staging")
					return nil
				},
				StageTrackedFn: func() error {
					t.Fatal("must reject before staging")
					return nil
				},
				CommitFn: func(string) (string, error) {
					t.Fatal("must not commit in either worktree")
					return "", nil
				},
				CheckoutBranchFn: func(string) error {
					t.Fatal("foreign adoption must not check out")
					return nil
				},
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = true
			cfg.ConfirmFn = func(string, bool) (bool, error) { return true, nil }
			cfg.GitHubClientOverride = &github.MockClient{}
			err := runAdd(cfg, &tt.opts, []string{"b2"})
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			assert.Contains(t, diagnostics, owner)
			sf, loadErr := stack.Load(common)
			require.NoError(t, loadErr)
			if tt.wantError {
				assert.ErrorIs(t, err, ErrInvalidArgs)
				if tt.noStack {
					assert.Empty(t, sf.Stacks)
				} else {
					require.Len(t, sf.Stacks, 1)
					assert.Equal(t, []string{"b1"}, sf.Stacks[0].BranchNames())
				}
			} else {
				require.NoError(t, err)
				require.Len(t, sf.Stacks, 1)
				assert.Equal(t, []string{"b1", "b2"}, sf.Stacks[0].BranchNames())
				assert.Equal(t, "adopted-base", sf.Stacks[0].Branches[1].Base)
				assert.Contains(t, diagnostics, "Adopted")
				assert.Contains(t, diagnostics, "Your current checkout is unchanged.")
			}
		})
	}
}

func TestAdd_InteractiveInitForeignTargetFailsBeforeStaging(t *testing.T) {
	common, root, owner := t.TempDir(), t.TempDir(), t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:          func() (string, error) { return common, nil },
		RootDirFn:         func() (string, error) { return root, nil },
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		BranchExistsFn:    func(string) (bool, error) { return true, nil },
		WorktreesFn:       func() ([]git.Worktree, error) { return []git.Worktree{{Path: owner, Branch: "foreign"}}, nil },
		StageAllFn: func() error {
			t.Fatal("must reject the prompted foreign target before staging")
			return nil
		},
	})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) { return true, nil }
	cfg.InputFn = func(string) (string, error) { return "foreign", nil }
	cfg.GitHubClientOverride = &github.MockClient{}
	require.ErrorIs(t, runAdd(cfg, &addOptions{stageAll: true}, nil), ErrInvalidArgs)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, owner)
	sf, err := stack.Load(common)
	require.NoError(t, err)
	assert.Empty(t, sf.Stacks)
}

// saveStack is a helper to pre-create a stack file for add tests.
func saveStack(t *testing.T, gitDir string, s stack.Stack) {
	t.Helper()
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks:        []stack.Stack{s},
	}
	require.NoError(t, stack.Save(gitDir, sf), "saving seed stack")
}

func TestAdd_StateLookupFailureDoesNotMutate(t *testing.T) {
	for _, query := range []string{"branch", "staged"} {
		t.Run(query, func(t *testing.T) {
			gitDir := t.TempDir()
			s := stack.Stack{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "b1"}},
			}
			saveStack(t, gitDir, s)
			lookupErr := fmt.Errorf("state lookup failed")
			mock := &git.MockOps{
				GitDirFn:        func() (string, error) { return gitDir, nil },
				CurrentBranchFn: func() (string, error) { return "b1", nil },
				RevParseMultiFn: func([]string) ([]string, error) {
					return []string{"parent", "head"}, nil
				},
				CreateBranchFn: func(string, string) error {
					t.Fatal("must not create a branch after a failed lookup")
					return nil
				},
				CheckoutBranchFn: func(string) error {
					t.Fatal("must not check out a branch after a failed lookup")
					return nil
				},
				CommitFn: func(string) (string, error) {
					t.Fatal("must not commit after a failed lookup")
					return "", nil
				},
			}
			if query == "branch" {
				mock.BranchExistsFn = func(string) (bool, error) { return true, lookupErr }
			} else {
				mock.HasStagedChangesFn = func() (bool, error) { return true, lookupErr }
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			err := runAdd(cfg, &addOptions{message: "new commit"}, []string{"new-branch"})
			require.ErrorIs(t, err, ErrSilent)
			output := collectOutput(cfg, outR, errR)
			assert.Contains(t, output, lookupErr.Error())
			assert.NotContains(t, output, "nothing to commit")
			sf, err := stack.Load(gitDir)
			require.NoError(t, err)
			assert.Equal(t, s, sf.Stacks[0])
		})
	}
}

func TestAdd_CreatesNewBranch(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch, checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Equal(t, "newbranch", createdBranch, "CreateBranch")
	assert.Equal(t, "newbranch", checkedOut, "CheckoutBranch")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	names := sf.Stacks[0].BranchNames()
	assert.Equal(t, "newbranch", names[len(names)-1], "top branch")
}

func TestAdd_Fork(t *testing.T) {
	for _, tt := range []struct {
		name, current              string
		opts                       addOptions
		adopted, foreign, generate bool
		noCatalog, prompt          bool
	}{
		{name: "middle", current: "b1"},
		{name: "top", current: "b2"},
		{name: "shared trunk", current: "main"},
		{name: "untracked", current: "outside"},
		{name: "first stack", current: "main", noCatalog: true},
		{name: "prompted name", current: "b1", prompt: true},
		{name: "adopt", current: "b1", adopted: true},
		{name: "foreign adoption", current: "b1", adopted: true, foreign: true},
		{name: "stage all", current: "b1", opts: addOptions{stageAll: true, message: "New layer"}},
		{name: "stage tracked", current: "b1", opts: addOptions{stageTracked: true, message: "New layer"}},
		{name: "staged commit", current: "b1", opts: addOptions{message: "New layer"}},
		{name: "editor", current: "b1", opts: addOptions{stageAll: true}},
		{name: "generated name", current: "b1", opts: addOptions{message: "New layer"}, generate: true},
		{name: "adopt and commit", current: "b1", opts: addOptions{message: "New layer"}, adopted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, owner := t.TempDir(), t.TempDir()
			if !tt.noCatalog {
				writeStackFileMulti(t, dir,
					stack.Stack{Trunk: stack.BranchRef{Branch: "b1"}, Branches: []stack.BranchRef{{Branch: "old-child"}}},
					stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}}},
					stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "other"}}},
				)
			}
			before, err := stack.Load(dir)
			require.NoError(t, err)
			current := tt.current
			name := "new-child"
			args := []string{name}
			if tt.generate {
				args = nil
				name = time.Now().Format("01-02") + "-new_layer"
			} else if tt.prompt {
				args = nil
			}
			var staged, committed, created bool
			commit := func() (string, error) {
				assert.Equal(t, name, current, "never commit on the parent, even when it is empty")
				committed = true
				return "commit", nil
			}
			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return dir, nil },
				CurrentBranchFn: func() (string, error) { return current, nil },
				BranchExistsFn:  func(branch string) (bool, error) { return tt.adopted && branch == name, nil },
				RevParseFn: func(ref string) (string, error) {
					assert.Equal(t, "refs/heads/"+tt.current, ref)
					return "parent-sha", nil
				},
				RevParseMultiFn: func([]string) ([]string, error) {
					t.Fatal("fork must bypass the empty-layer shortcut")
					return nil, nil
				},
				MergeBaseFn: func(base, branch string) (string, error) {
					assert.Equal(t, "parent-sha", base)
					assert.Equal(t, "refs/heads/"+name, branch)
					return "common-base", nil
				},
				WorktreesFn: func() ([]git.Worktree, error) {
					if tt.foreign {
						return []git.Worktree{{Path: owner, Branch: name}}, nil
					}
					return nil, nil
				},
				CreateBranchFn: func(branch, base string) error {
					assert.Equal(t, name, branch)
					assert.Equal(t, "parent-sha", base)
					created = true
					return nil
				},
				CheckoutBranchFn:    func(branch string) error { current = branch; return nil },
				StageAllFn:          func() error { staged = true; return nil },
				StageTrackedFn:      func() error { staged = true; return nil },
				HasStagedChangesFn:  func() (bool, error) { return true, nil },
				CommitFn:            func(string) (string, error) { return commit() },
				CommitInteractiveFn: commit,
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = tt.prompt
			cfg.InputFn = func(string) (string, error) {
				assert.True(t, tt.prompt)
				return name, nil
			}
			cfg.ConfirmFn = func(string, bool) (bool, error) {
				t.Fatal("explicit fork does not require confirmation")
				return false, nil
			}
			cfg.SelectFn = func(string, string, []string) (int, error) {
				t.Fatal("explicit fork must not select an existing stack")
				return 0, nil
			}
			cfg.GitHubClientOverride = &github.MockClient{FindPRForBranchFn: func(string) (*github.PullRequest, error) {
				t.Fatal("fork is local-only")
				return nil, nil
			}}
			tt.opts.fork = true
			require.NoError(t, runAdd(cfg, &tt.opts, args))
			_, output := commandOutput(t, cfg, outR, errR)
			after, err := stack.Load(dir)
			require.NoError(t, err)
			require.Len(t, after.Stacks, len(before.Stacks)+1)
			assert.Equal(t, before.Stacks, after.Stacks[:len(before.Stacks)])
			child := after.Stacks[len(before.Stacks)]
			assert.Equal(t, stack.BranchRef{Branch: tt.current, Head: "parent-sha"}, child.Trunk)
			assert.Equal(t, []string{name}, child.BranchNames())
			base := "parent-sha"
			if tt.adopted {
				base = "common-base"
			}
			assert.Equal(t, base, child.Branches[0].Base)
			assert.Equal(t, !tt.adopted, created)
			assert.Equal(t, tt.opts.stageAll || tt.opts.stageTracked, staged)
			assert.Equal(t, tt.opts.message != "" || staged, committed)
			if tt.foreign {
				assert.Equal(t, tt.current, current)
				assert.Contains(t, output, owner)
			} else {
				assert.Equal(t, name, current)
			}
			assert.Contains(t, output, "Started a new stack based on "+tt.current)
		})
	}
}

func TestAdd_MidStackForkPrompt(t *testing.T) {
	for _, mode := range []string{"yes", "no", "interrupt", "error", "noninteractive"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			saveStack(t, dir, stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
			})
			confirmed, created := false, false
			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return dir, nil },
				CurrentBranchFn: func() (string, error) { return "b1", nil },
				CreateBranchFn: func(string, string) error {
					assert.Equal(t, "yes", mode)
					created = true
					return nil
				},
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = mode != "noninteractive"
			cfg.ConfirmFn = func(prompt string, def bool) (bool, error) {
				assert.True(t, def)
				assert.Contains(t, prompt, "b1")
				assert.NotEqual(t, "noninteractive", mode)
				confirmed = true
				if mode == "interrupt" {
					return false, terminal.InterruptErr
				}
				if mode == "error" {
					return false, fmt.Errorf("prompt failed")
				}
				return mode == "yes", nil
			}
			cfg.InputFn = func(string) (string, error) {
				assert.True(t, confirmed, "confirm the fork before asking for a name")
				assert.Equal(t, "yes", mode)
				return "child", nil
			}
			err := runAdd(cfg, &addOptions{}, nil)
			_, output := commandOutput(t, cfg, outR, errR)
			sf, loadErr := stack.Load(dir)
			require.NoError(t, loadErr)
			if mode == "yes" {
				require.NoError(t, err)
				require.Len(t, sf.Stacks, 2)
				assert.Equal(t, "b1", sf.Stacks[1].Trunk.Branch)
			} else {
				require.Error(t, err)
				assert.Len(t, sf.Stacks, 1)
				if mode != "error" {
					assert.ErrorIs(t, err, ErrInvalidArgs)
					assert.Contains(t, output, "gh stack add --fork")
					assert.Contains(t, output, "gh stack modify")
				}
			}
			assert.Equal(t, mode == "yes", created)
		})
	}
}

func TestAdd_ForkRefusals(t *testing.T) {
	for _, mode := range []string{"merged", "duplicate", "self", "invalid name", "missing name", "detached", "base lookup", "foreign commit", "nothing staged"} {
		t.Run(mode, func(t *testing.T) {
			dir, owner := t.TempDir(), t.TempDir()
			s := stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "parent"}}}
			if mode == "merged" {
				s.Branches[0].PullRequest = &stack.PullRequestRef{Number: 1, Merged: true}
			}
			saveStack(t, dir, s)
			opts := &addOptions{fork: true}
			args := []string{"child"}
			switch mode {
			case "duplicate":
				args[0] = "main"
			case "self":
				args[0] = "parent"
			case "invalid name":
				args[0] = "invalid name"
			case "missing name":
				args = nil
			case "foreign commit", "nothing staged":
				opts.message = "commit"
			}
			restore := git.SetOps(&git.MockOps{
				GitDirFn: func() (string, error) { return dir, nil },
				ValidateRefNameFn: func(string) error {
					if mode == "invalid name" {
						return fmt.Errorf("invalid ref")
					}
					return nil
				},
				CurrentBranchFn: func() (string, error) {
					if mode == "detached" {
						return "", fmt.Errorf("detached HEAD")
					}
					return "parent", nil
				},
				RevParseFn: func(string) (string, error) {
					if mode == "base lookup" {
						return "", fmt.Errorf("base lookup failed")
					}
					return "parent-sha", nil
				},
				WorktreesFn: func() ([]git.Worktree, error) {
					if mode == "foreign commit" {
						return []git.Worktree{{Path: owner, Branch: "child"}}, nil
					}
					return nil, nil
				},
				CreateBranchFn:   func(string, string) error { t.Fatal("must not create a branch"); return nil },
				CheckoutBranchFn: func(string) error { t.Fatal("must not change checkout"); return nil },
				StageAllFn:       func() error { t.Fatal("must not stage"); return nil },
				CommitFn:         func(string) (string, error) { t.Fatal("must not commit"); return "", nil },
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			cfg.InputFn = func(string) (string, error) { t.Fatal("must not prompt"); return "", nil }
			require.Error(t, runAdd(cfg, opts, args))
			_, output := commandOutput(t, cfg, outR, errR)
			assert.NotContains(t, output, "Started a new stack")
			sf, err := stack.Load(dir)
			require.NoError(t, err)
			assert.Equal(t, []stack.Stack{s}, sf.Stacks)
		})
	}
}

func TestAdd_OnlyAllowedOnTopOfStack(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	})

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "top of the stack")
	assert.Contains(t, output, "gh stack modify")
}

func TestAdd_MutuallyExclusiveFlags(t *testing.T) {
	restore := git.SetOps(&git.MockOps{})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, stageTracked: true, message: "msg"}, []string{"branch"})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "mutually exclusive")
}

func TestAdd_StagingWithoutMessageUsesEditor(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	interactiveCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			return []string{"aaa", "bbb"}, nil
		},
		RevParseFn:         func(ref string) (string, error) { return "abc", nil },
		CreateBranchFn:     func(name, base string) error { return nil },
		CheckoutBranchFn:   func(name string) error { return nil },
		StageAllFn:         func() error { return nil },
		HasStagedChangesFn: func() (bool, error) { return true, nil },
		CommitInteractiveFn: func() (string, error) {
			interactiveCalled = true
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true}, []string{"new-branch"})

	assert.True(t, interactiveCalled, "expected CommitInteractive to be called when -m is omitted")
}

func TestAdd_EmptyBranchCommitsInPlace(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	createBranchCalled := false
	commitCalled := false
	stageAllCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			// Return same SHA for parent and current branch — branch has no unique commits
			return []string{"aaa111", "aaa111"}, nil
		},
		StageAllFn: func() error {
			stageAllCalled = true
			return nil
		},
		HasStagedChangesFn: func() (bool, error) { return true, nil },
		CommitFn: func(msg string) (string, error) {
			commitCalled = true
			return "abc1234567890", nil
		},
		CreateBranchFn: func(name, base string) error {
			createBranchCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "Auth middleware"}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.True(t, stageAllCalled, "expected StageAll to be called")
	assert.True(t, commitCalled, "expected Commit to be called")
	assert.False(t, createBranchCalled, "CreateBranch should NOT be called for empty branch commit-in-place")
}

func TestAdd_BranchWithCommitsCreatesNew(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	createCalled := false
	checkoutCalled := false
	commitCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			// Parent and current branch point to different commits (branch has commits)
			return []string{"aaa", "bbb"}, nil
		},
		CreateBranchFn: func(name, base string) error {
			createCalled = true
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkoutCalled = true
			return nil
		},
		HasStagedChangesFn: func() (bool, error) { return true, nil },
		CommitFn: func(msg string) (string, error) {
			commitCalled = true
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "API routes"}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.True(t, createCalled, "expected CreateBranch to be called")
	assert.True(t, checkoutCalled, "expected CheckoutBranch to be called")
	assert.True(t, commitCalled, "expected Commit to be called on the new branch")
}

func TestAdd_ExplicitNameUsedVerbatim(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"mybranch"})
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Equal(t, "mybranch", createdBranch)
}

func TestAdd_MessageAutoGeneratesDateSlug(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			return []string{"aaa", "bbb"}, nil
		},
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		HasStagedChangesFn: func() (bool, error) { return true, nil },
		CommitFn: func(msg string) (string, error) {
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "next feature"}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")
	today := time.Now().Format("01-02")
	assert.Equal(t, today+"-next_feature", createdBranch)
}

func TestAdd_FullyMergedStackBlocked(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	})

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "All branches in this stack have been merged")
}

func TestAdd_NothingToCommit(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			return []string{"aaa", "aaa"}, nil // same SHA = empty branch
		},
		StageAllFn:         func() error { return nil },
		HasStagedChangesFn: func() (bool, error) { return false, nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "msg"}, nil)
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "no changes to commit")
}

func TestAdd_PromptForBranchName(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(name string) error { return nil },
		RevParseFn:       func(ref string) (string, error) { return "abc", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()

	var gotPrompt string
	cfg.InputFn = func(prompt string) (string, error) {
		gotPrompt = prompt
		return "my-branch", nil
	}

	err := runAdd(cfg, &addOptions{}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Contains(t, gotPrompt, ":", "prompt should end with a colon")
	assert.Equal(t, "my-branch", createdBranch, "input should be used as-is")
}

func TestAdd_PromptInputUsedVerbatim(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(name string) error { return nil },
		RevParseFn:       func(ref string) (string, error) { return "abc", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()

	cfg.InputFn = func(prompt string) (string, error) {
		return "custom/other-name", nil
	}

	err := runAdd(cfg, &addOptions{}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Equal(t, "custom/other-name", createdBranch, "typed input should be used verbatim")
}

func TestAdd_FromTrunk(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch string
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) { return "sha-" + ref, nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	// When on trunk, idx < 0 so the middle-of-stack check passes.
	// Add should succeed and create the new branch.
	require.NoError(t, err)
	assert.Equal(t, "newbranch", createdBranch)
	assert.Equal(t, "newbranch", checkedOut)
	assert.NotContains(t, output, "\u2717")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	names := sf.Stacks[0].BranchNames()
	assert.Equal(t, "newbranch", names[len(names)-1], "new branch should be appended to stack")
}

func TestAdd_AdoptsExistingBranch(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	createBranchCalled := false
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		BranchExistsFn:  func(name string) (bool, error) { return name == "existing-branch", nil },
		MergeBaseFn: func(parent, branch string) (string, error) {
			assert.Equal(t, "b1", parent)
			assert.Equal(t, "existing-branch", branch)
			return "common-base", nil
		},
		CreateBranchFn: func(name, base string) error {
			createBranchCalled = true
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) { return "abc", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runAdd(cfg, &addOptions{}, []string{"existing-branch"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.False(t, createBranchCalled, "CreateBranch should NOT be called for existing branch")
	assert.Equal(t, "existing-branch", checkedOut, "should checkout the existing branch")
	assert.Contains(t, output, "Adopted")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	names := sf.Stacks[0].BranchNames()
	assert.Equal(t, "existing-branch", names[len(names)-1], "adopted branch appended to stack")
	assert.Equal(t, "common-base", sf.Stacks[0].Branches[len(sf.Stacks[0].Branches)-1].Base,
		"adopted branch should record the actual common ancestor")
}

func TestAdd_RejectsExistingBranchInStack(t *testing.T) {
	gitDir := t.TempDir()
	// Two stacks: the current one and another that owns "taken-branch"
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "b1"}},
			},
			{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "taken-branch"}},
			},
		},
	}
	require.NoError(t, stack.Save(gitDir, sf), "saving seed stacks")

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		BranchExistsFn:  func(name string) (bool, error) { return name == "taken-branch", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runAdd(cfg, &addOptions{}, []string{"taken-branch"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "already exists in the stack")
}

func TestAdd_AdoptsExistingBranchWithCommit(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	createBranchCalled := false
	commitCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		BranchExistsFn:  func(name string) (bool, error) { return name == "existing-branch", nil },
		MergeBaseFn:     func(string, string) (string, error) { return "common-base", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			return []string{"aaa", "bbb"}, nil // different SHAs = branch has commits
		},
		CreateBranchFn: func(name, base string) error {
			createBranchCalled = true
			return nil
		},
		CheckoutBranchFn:   func(name string) error { return nil },
		RevParseFn:         func(ref string) (string, error) { return "abc", nil },
		StageAllFn:         func() error { return nil },
		HasStagedChangesFn: func() (bool, error) { return true, nil },
		CommitFn: func(msg string) (string, error) {
			commitCalled = true
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runAdd(cfg, &addOptions{stageAll: true, message: "new commit"}, []string{"existing-branch"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.False(t, createBranchCalled, "CreateBranch should NOT be called")
	assert.True(t, commitCalled, "Commit should be called on the adopted branch")
	assert.Contains(t, output, "Adopted")
}

func TestAdd_AdoptExistingBranchWithoutCommonBaseFails(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	checkedOut := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		BranchExistsFn:  func(name string) (bool, error) { return name == "unrelated", nil },
		MergeBaseFn:     func(string, string) (string, error) { return "", assert.AnError },
		CheckoutBranchFn: func(string) error {
			checkedOut = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runAdd(cfg, &addOptions{}, []string{"unrelated"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrSilent)
	assert.False(t, checkedOut)
	assert.Contains(t, output, "failed to determine the common base")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	assert.Equal(t, []string{"b1"}, sf.Stacks[0].BranchNames())
}

func TestAdd_InitializesStackWithExplicitBranch(t *testing.T) {
	gitDir := t.TempDir()
	trunkExists := false
	var created [][2]string
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:          func() (string, error) { return gitDir, nil },
		CurrentBranchFn:   func() (string, error) { return "unstacked", nil },
		DefaultBranchFn:   func() (string, error) { return "main", nil },
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		BranchExistsFn:    func(name string) (bool, error) { return name == "main" && trunkExists, nil },
		RevParseFn: func(ref string) (string, error) {
			if ref == "main" && !trunkExists {
				return "", fmt.Errorf("unknown revision %s", ref)
			}
			return "sha-" + ref, nil
		},
		CreateBranchFn: func(name, base string) error {
			created = append(created, [2]string{name, base})
			if name == "main" {
				trunkExists = true
			}
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(prompt string, defaultValue bool) (bool, error) {
		assert.Equal(t, "Would you like to initialize a new stack?", prompt)
		assert.True(t, defaultValue)
		return true, nil
	}

	err := runAdd(cfg, &addOptions{}, []string{"first-layer"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.NotContains(t, output, "not part of a stack")
	assert.Equal(t, [][2]string{
		{"main", "origin/main"},
		{"first-layer", "refs/heads/main"},
	}, created)
	assert.Equal(t, "first-layer", checkedOut)

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"first-layer"}, sf.Stacks[0].BranchNames())
}

func TestAdd_InitializesStackWithPromptedBranch(t *testing.T) {
	gitDir := t.TempDir()
	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:          func() (string, error) { return gitDir, nil },
		CurrentBranchFn:   func() (string, error) { return "main", nil },
		DefaultBranchFn:   func() (string, error) { return "main", nil },
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) { return true, nil }
	cfg.InputFn = func(prompt string) (string, error) {
		assert.Equal(t, "What's the name of the first branch:", prompt)
		return "prompted-layer", nil
	}

	err := runAdd(cfg, &addOptions{}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Equal(t, "prompted-layer", createdBranch)

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"prompted-layer"}, sf.Stacks[0].BranchNames())
}

func TestAdd_InitializesGeneratedBranchAndCommits(t *testing.T) {
	gitDir := t.TempDir()
	currentBranch := "unstacked"
	stageAllCalled := false
	commitCalled := false
	expectedBranch := time.Now().Format("01-02") + "-first_layer"

	restore := git.SetOps(&git.MockOps{
		GitDirFn:          func() (string, error) { return gitDir, nil },
		CurrentBranchFn:   func() (string, error) { return currentBranch, nil },
		DefaultBranchFn:   func() (string, error) { return "main", nil },
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		BranchExistsFn:    func(name string) (bool, error) { return name == "main", nil },
		CreateBranchFn: func(name, base string) error {
			assert.Equal(t, expectedBranch, name)
			assert.Equal(t, "refs/heads/main", base)
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			require.True(t, stageAllCalled, "changes should be staged before initialization")
			currentBranch = name
			return nil
		},
		StageAllFn: func() error {
			stageAllCalled = true
			return nil
		},
		HasStagedChangesFn: func() (bool, error) { return true, nil },
		CommitFn: func(message string) (string, error) {
			assert.Equal(t, "First layer", message)
			assert.Equal(t, expectedBranch, currentBranch)
			commitCalled = true
			return "abc123", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) { return true, nil }

	err := runAdd(cfg, &addOptions{stageAll: true, message: "First layer"}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.True(t, stageAllCalled)
	assert.True(t, commitCalled)

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{expectedBranch}, sf.Stacks[0].BranchNames())
}

func TestAdd_MissingStackWithoutConfirmationReturnsNotInStack(t *testing.T) {
	tests := []struct {
		name        string
		interactive bool
		confirmed   bool
	}{
		{
			name:        "non-interactive",
			interactive: false,
		},
		{
			name:        "declined",
			interactive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := t.TempDir()
			createCalled := false
			confirmCalled := false
			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return gitDir, nil },
				CurrentBranchFn: func() (string, error) { return "unstacked", nil },
				CreateBranchFn: func(string, string) error {
					createCalled = true
					return nil
				},
			})
			defer restore()

			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = tt.interactive
			cfg.ConfirmFn = func(string, bool) (bool, error) {
				confirmCalled = true
				return tt.confirmed, nil
			}

			err := runAdd(cfg, &addOptions{}, []string{"first-layer"})
			output := collectOutput(cfg, outR, errR)

			assert.ErrorIs(t, err, ErrNotInStack)
			assert.Contains(t, output, `current branch "unstacked" is not part of a stack`)
			assert.Contains(t, output, "gh stack checkout")
			assert.Contains(t, output, "gh stack init")
			assert.False(t, createCalled)
			assert.Equal(t, tt.interactive, confirmCalled)
		})
	}
}

func TestAdd_InitConfirmationError(t *testing.T) {
	tests := []struct {
		name       string
		confirmErr error
		wantOutput string
	}{
		{
			name:       "interrupt",
			confirmErr: terminal.InterruptErr,
			wantOutput: "Received interrupt, aborting operation",
		},
		{
			name:       "prompt failure",
			confirmErr: assert.AnError,
			wantOutput: "failed to read confirmation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := t.TempDir()
			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return gitDir, nil },
				CurrentBranchFn: func() (string, error) { return "unstacked", nil },
			})
			defer restore()

			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = true
			cfg.ConfirmFn = func(string, bool) (bool, error) {
				return false, tt.confirmErr
			}

			err := runAdd(cfg, &addOptions{}, []string{"first-layer"})
			output := collectOutput(cfg, outR, errR)

			assert.ErrorIs(t, err, ErrSilent)
			assert.Contains(t, output, tt.wantOutput)
		})
	}
}

func TestAdd_LoaderFailureDoesNotOfferInitialization(t *testing.T) {
	restore := git.SetOps(&git.MockOps{
		GitDirFn: func() (string, error) { return "", assert.AnError },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) {
		t.Fatal("confirmation should not be requested for a loader failure")
		return false, nil
	}

	err := runAdd(cfg, &addOptions{}, []string{"first-layer"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "not a git repository")
	assert.NotContains(t, output, "gh stack init")
}
