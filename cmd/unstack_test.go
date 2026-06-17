package cmd

import (
	"encoding/json"
	"errors"
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

func TestUnstack_API404_TreatedAsIdempotentSuccess(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		DeleteStackFn: func(stackID string) error {
			return &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	// 404 means already deleted — should succeed and remove locally
	require.NoError(t, err)
	assert.Contains(t, output, "continuing with local unstack")
	assert.Contains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Empty(t, sf.Stacks)
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
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
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

func TestUnstack_RemovesCorrectStackByPointer(t *testing.T) {
	// Two stacks share the same trunk "main". Current branch "b3" should remove
	// only the second stack (b3,b4), leaving the first (b1,b2) intact.
	// This verifies pointer-based removal instead of branch-name-based.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b3", nil },
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
	err := runUnstack(cfg, &unstackOptions{local: true})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1, "should remove exactly one stack")
	assert.Equal(t, []string{"b1", "b2"}, sf.Stacks[0].BranchNames(), "should keep the OTHER stack intact")
}

func TestUnstack_PreflightBlocksDelete_WhenAllPRsIneligible(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	deleteCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 101:
				return &github.PullRequest{Number: 101, State: "MERGED"}, nil
			case 102:
				return &github.PullRequest{Number: 102, State: "OPEN", MergeQueueEntry: &github.MergeQueueEntry{ID: "MQE_1"}}, nil
			default:
				return nil, nil
			}
		},
		DeleteStackFn: func(stackID string) error {
			deleteCalled = true
			return nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.False(t, deleteCalled, "DeleteStack should not be called when all PRs are ineligible")
	assert.Contains(t, output, "Unstacking not allowed")
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_PreflightAllowsDelete_WhenMixedEligibility(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	deleteCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 101:
				return &github.PullRequest{Number: 101, State: "MERGED"}, nil
			case 102:
				return &github.PullRequest{Number: 102, State: "OPEN"}, nil
			default:
				return nil, nil
			}
		},
		DeleteStackFn: func(stackID string) error {
			deleteCalled = true
			return nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.True(t, deleteCalled, "DeleteStack should be called when at least one PR is eligible")
	assert.Contains(t, output, "Stack deleted on GitHub")
	assert.Contains(t, output, "Stack removed from local tracking")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	assert.Empty(t, sf.Stacks)
}

func TestUnstack_PreflightLookupFailure_StopsDeletion(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}}},
	})

	deleteCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return nil, errors.New("graphql timeout")
		},
		DeleteStackFn: func(stackID string) error {
			deleteCalled = true
			return nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.False(t, deleteCalled, "DeleteStack should not be called if preflight fails")
	assert.Contains(t, output, "failed to check pull request states before unstack")
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_API422_ShowsInformativeErrorAndStopsLocalDeletion(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "99",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: number, State: "OPEN"}, nil
		},
		DeleteStackFn: func(stackID string) error {
			return &api.HTTPError{StatusCode: 422, Message: "some pull requests cannot be removed from stack"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "Cannot delete stack on GitHub")
	assert.Contains(t, output, "cannot be removed")
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}
