package cmd

import (
	"fmt"
	"io"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
)

func TestTrunk_FromMiddleBranch(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"main"}, checkedOut)
}

func TestTrunk_AlreadyOnTrunk(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	output := readCfgOutput(cfg, outR, errR)

	assert.NoError(t, err)
	assert.Empty(t, checkedOut, "should not checkout any branch")
	assert.Contains(t, output, "Already on trunk branch main")
}

func TestTrunk_FromTopOfStack(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b3", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"main"}, checkedOut)
}

func TestTrunk_NotInStack(t *testing.T) {
	tmpDir := t.TempDir()
	// No stack file written — empty git dir

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "some-branch", nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.ErrorIs(t, err, ErrNotInStack)
}

func TestTrunk_CheckoutFailure(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			return fmt.Errorf("checkout failed: uncommitted changes")
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.Error(t, err)
	assert.ErrorContains(t, err, "checkout failed")
}

func TestTrunk_CustomTrunkBranch(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "develop"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"develop"}, checkedOut)
}

func TestTrunk_RejectsArgs(t *testing.T) {
	// Ensure trunk does not accept arguments
	tmpDir := t.TempDir()
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	}
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetArgs([]string{"unexpected-arg"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.Error(t, err, "should reject positional arguments")
}

func TestTrunk_MissingLocallyCreatedFromRemote(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	var checkedOut []string
	var createdBranch string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		BranchExistsFn: func(name string) bool {
			// trunk does not exist locally
			return name != "main"
		},
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			return nil
		},
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cmd := TrunkCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	output := readCfgOutput(cfg, outR, errR)

	assert.NoError(t, err)
	assert.Equal(t, "main", createdBranch, "should create trunk from remote")
	assert.Equal(t, []string{"main"}, checkedOut)
	assert.Contains(t, output, "Created local trunk branch main from origin/main")
}
