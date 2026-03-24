package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTwoStacks(t *testing.T, dir string, s1, s2 stack.Stack) {
	t.Helper()
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks:        []stack.Stack{s1, s2},
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh-stack"), data, 0644))
}

func TestUnstack_RemovesStack(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	s1 := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	s2 := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b3"}, {Branch: "b4"}},
	}
	writeTwoStacks(t, gitDir, s1, s2)

	cfg, outR, errR := config.NewTestConfig()
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	// The GitHub API call will fail (no real repo), but the command should not
	// return a fatal error — only a warning is printed.
	require.NoError(t, err)
	assert.Contains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"b3", "b4"}, sf.Stacks[0].BranchNames())
}

func TestUnstack_Local(t *testing.T) {
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
	err := runUnstack(cfg, &unstackOptions{local: true})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Stack removed")
	// With --local, the GitHub API error message should NOT appear.
	assert.NotContains(t, output, "failed to create GitHub client")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Empty(t, sf.Stacks)
}

func TestUnstack_WithTarget(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "unrelated", nil },
	})
	defer restore()

	s1 := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	s2 := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b3"}, {Branch: "b4"}},
	}
	writeTwoStacks(t, gitDir, s1, s2)

	cfg, outR, errR := config.NewTestConfig()
	err := runUnstack(cfg, &unstackOptions{target: "b3", local: true})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Stack removed")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"b1", "b2"}, sf.Stacks[0].BranchNames())
}
