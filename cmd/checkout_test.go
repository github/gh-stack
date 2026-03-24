package cmd

import (
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckout_ByBranchName(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{target: "b2"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b2", checkedOut)
	assert.Contains(t, output, "Switched to b2")
}

func TestCheckout_ByPRNumber(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{target: "42"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b1", checkedOut)
	assert.Contains(t, output, "Switched to b1")
}

func TestCheckout_AlreadyOnTarget(t *testing.T) {
	gitDir := t.TempDir()
	checkoutCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkoutCalled = true
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{target: "b1"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, checkoutCalled, "CheckoutBranch should not be called when already on target")
	assert.Contains(t, output, "Already on b1")
}

func TestCheckout_NoStacks_NonInteractive(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	// Write an empty stack file (no stacks)
	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{}) // no target arg
	output := collectOutput(cfg, outR, errR)

	assert.Error(t, err)
	assert.Contains(t, output, "no target specified")
}

func TestCheckout_BranchNotFound(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{target: "nonexistent"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "no locally tracked stack found")
}
