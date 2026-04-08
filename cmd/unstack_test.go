package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
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
		ID:       "42",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	s2 := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b3"}, {Branch: "b4"}},
	}
	writeTwoStacks(t, gitDir, s1, s2)

	var deletedStackID string
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		DeleteStackFn: func(stackID string) error {
			deletedStackID = stackID
			return nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Stack removed from local tracking")
	assert.Contains(t, output, "Stack deleted on GitHub")
	assert.Equal(t, "42", deletedStackID)

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
	// With --local, the GitHub API should NOT be called.
	assert.NotContains(t, output, "Stack deleted on GitHub")

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

func TestUnstack_NoStackID_WarnsAndSkipsAPI(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	// Stack with no ID (never synced to GitHub)
	writeStackFile(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	apiCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		DeleteStackFn: func(stackID string) error {
			apiCalled = true
			return nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, apiCalled, "API should not be called when stack has no ID")
	assert.Contains(t, output, "no remote ID")
	assert.Contains(t, output, "Stack removed from local tracking")
	assert.NotContains(t, output, "Stack deleted on GitHub")
}

func TestUnstack_API404_ShowsErrorAndStopsLocalDeletion(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		DeleteStackFn: func(stackID string) error {
			return &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "Failed to delete stack on GitHub (HTTP 404)")
	// Should NOT remove locally when remote fails
	assert.NotContains(t, output, "Stack removed from local tracking")

	// Stack should still exist locally
	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_API409_ShowsErrorAndStopsLocalDeletion(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		DeleteStackFn: func(stackID string) error {
			return &api.HTTPError{StatusCode: 409, Message: "Stack is currently being modified"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "Failed to delete stack on GitHub (HTTP 409)")
	// Should NOT remove locally when remote fails
	assert.NotContains(t, output, "Stack removed from local tracking")

	// Stack should still exist locally
	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
}
