package cmd

import (
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saveStack is a helper to pre-create a stack file for add tests.
func saveStack(t *testing.T, gitDir string, s stack.Stack) {
	t.Helper()
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks:        []stack.Stack{s},
	}
	require.NoError(t, stack.Save(gitDir, sf), "saving seed stack")
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
		HasStagedChangesFn: func() bool { return true },
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
		HasStagedChangesFn: func() bool { return true },
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
		HasStagedChangesFn: func() bool { return true },
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

func TestAdd_PrefixAppliedWithSlash(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Prefix:   "feat",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "feat/01"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "feat/01", nil },
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
	assert.Equal(t, "feat/mybranch", createdBranch)
}

func TestAdd_NumberedNaming(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Prefix:   "feat",
		Numbered: true,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "feat/01"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "feat/01", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			return []string{"aaa", "bbb"}, nil
		},
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		HasStagedChangesFn: func() bool { return true },
		CommitFn: func(msg string) (string, error) {
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "next feature"}, nil)
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Equal(t, "feat/02", createdBranch)
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
		HasStagedChangesFn: func() bool { return false },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "msg"}, nil)
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "no changes to commit")
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
