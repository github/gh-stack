package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/AlecAivazis/survey/v2/terminal"
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
		HasStagedChangesFn: func() bool { return false },
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
		BranchExistsFn:  func(name string) bool { return name == "existing-branch" },
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
		BranchExistsFn:  func(name string) bool { return name == "taken-branch" },
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
		BranchExistsFn:  func(name string) bool { return name == "existing-branch" },
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
		HasStagedChangesFn: func() bool { return true },
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
		BranchExistsFn:  func(name string) bool { return name == "unrelated" },
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
		BranchExistsFn:    func(name string) bool { return name == "main" && trunkExists },
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
		{"first-layer", "main"},
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
		BranchExistsFn:    func(name string) bool { return name == "main" },
		CreateBranchFn: func(name, base string) error {
			assert.Equal(t, expectedBranch, name)
			assert.Equal(t, "main", base)
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
		HasStagedChangesFn: func() bool { return true },
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
