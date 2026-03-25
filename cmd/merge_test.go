package cmd

import (
	"io"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
)

func newMergeMock(tmpDir, currentBranch string) *git.MockOps {
	return &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return currentBranch, nil },
	}
}

func TestMerge_NoPullRequest(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(newMergeMock(tmpDir, "feat-1"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "no pull request found")
	assert.Contains(t, output, "gh stack push")
}

func TestMerge_AlreadyMerged(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 42,
				URL:    "https://github.com/owner/repo/pull/42",
				Merged: true,
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(newMergeMock(tmpDir, "feat-1"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "already been merged")
	assert.Contains(t, output, "https://github.com/owner/repo/pull/42")
}

func TestMerge_FullyMergedStack(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 10,
				URL:    "https://github.com/owner/repo/pull/10",
				Merged: true,
			}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{
				Number: 11,
				URL:    "https://github.com/owner/repo/pull/11",
				Merged: true,
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// On trunk with all PRs merged → fully merged message.
	restore := git.SetOps(newMergeMock(tmpDir, "main"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "All PRs in this stack have already been merged")
}

func TestMerge_OnTrunk(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 42,
				URL:    "https://github.com/owner/repo/pull/42",
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// Current branch is trunk, not a stack branch.
	restore := git.SetOps(newMergeMock(tmpDir, "main"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "not a stack branch")
}

func TestMerge_NonInteractive_PrintsURL(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 42,
				URL:    "https://github.com/owner/repo/pull/42",
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(newMergeMock(tmpDir, "feat-1"))
	defer restore()

	// NewTestConfig is non-interactive (piped output), so no confirm prompt.
	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "https://github.com/owner/repo/pull/42")
}

func TestMerge_NoArgs(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(newMergeMock(tmpDir, "feat-1"))
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"extra-arg", "another"})
	err := cmd.Execute()

	// MaximumNArgs(1) should reject two positional arguments.
	assert.Error(t, err)
}

func TestMerge_ByPRNumber(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 42,
				URL:    "https://github.com/owner/repo/pull/42",
			}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{
				Number: 43,
				URL:    "https://github.com/owner/repo/pull/43",
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// Current branch is feat-2, but we target PR #42 (feat-1) via arg.
	restore := git.SetOps(newMergeMock(tmpDir, "feat-2"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"42"})
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "https://github.com/owner/repo/pull/42")
}

func TestMerge_ByPRURL(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 42,
				URL:    "https://github.com/owner/repo/pull/42",
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(newMergeMock(tmpDir, "feat-1"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"https://github.com/owner/repo/pull/42"})
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "https://github.com/owner/repo/pull/42")
}

func TestMerge_ByBranchName(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{
				Number: 42,
				URL:    "https://github.com/owner/repo/pull/42",
			}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(newMergeMock(tmpDir, "main"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cmd := MergeCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"feat-1"})
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "https://github.com/owner/repo/pull/42")
}
