package cmd

import (
	"io"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSwitch_SwitchesToSelectedBranch(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
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
			{Branch: "b3"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true

	// Simulate selecting the first option (index 0) which is "3. b3" (top of stack)
	cfg.SelectFn = func(prompt, def string, options []string) (int, error) {
		// Verify prompt text
		assert.Equal(t, "Select a branch in the stack to switch to:", prompt)
		// Verify options are in reverse order with numbering
		assert.Equal(t, []string{"3. b3", "2. b2", "1. b1"}, options)
		return 0, nil // select "3. b3"
	}

	err := runSwitch(cfg)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b3", checkedOut)
	assert.Contains(t, output, "Switched to b3")
}

func TestSwitch_SelectMiddleBranch(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
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
			{Branch: "b3"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true

	// Select index 1 which is "2. b2"
	cfg.SelectFn = func(prompt, def string, options []string) (int, error) {
		return 1, nil // select "2. b2"
	}

	err := runSwitch(cfg)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b2", checkedOut)
	assert.Contains(t, output, "Switched to b2")
}

func TestSwitch_AlreadyOnSelectedBranch(t *testing.T) {
	gitDir := t.TempDir()
	checkoutCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
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
			{Branch: "b2"},
			{Branch: "b3"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true

	// Select "2. b2" which is option index 1 — the branch we're already on
	cfg.SelectFn = func(prompt, def string, options []string) (int, error) {
		return 1, nil // select "2. b2"
	}

	err := runSwitch(cfg)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, checkoutCalled, "CheckoutBranch should not be called when already on target")
	assert.Contains(t, output, "Already on b2")
}

func TestSwitch_NotInStack(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "orphan", nil },
	})
	defer restore()

	// Write a stack that doesn't contain "orphan"
	writeStackFile(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true

	err := runSwitch(cfg)
	assert.ErrorIs(t, err, ErrNotInStack)
}

func TestSwitch_NonInteractive(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	// ForceInteractive not set — non-interactive mode

	err := runSwitch(cfg)
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "switch requires an interactive terminal")
}

func TestSwitch_DisplayOrder(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "first", nil },
		CheckoutBranchFn: func(name string) error {
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "first"},
			{Branch: "second"},
			{Branch: "third"},
			{Branch: "fourth"},
			{Branch: "fifth"},
		},
	})

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true

	var capturedOptions []string
	cfg.SelectFn = func(prompt, def string, options []string) (int, error) {
		capturedOptions = options
		return 0, nil // select top
	}

	err := runSwitch(cfg)
	require.NoError(t, err)

	expected := []string{
		"5. fifth",
		"4. fourth",
		"3. third",
		"2. second",
		"1. first",
	}
	assert.Equal(t, expected, capturedOptions)
}

func TestSwitch_NoBranches(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{},
	})

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true

	err := runSwitch(cfg)
	assert.ErrorIs(t, err, ErrNotInStack)
}

func TestSwitch_CmdIntegration(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
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

	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.SelectFn = func(prompt, def string, options []string) (int, error) {
		return 0, nil // select top
	}

	cmd := SwitchCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	assert.NoError(t, err)
}
