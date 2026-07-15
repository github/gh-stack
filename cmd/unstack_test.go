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
		Number:   42,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}
	s2 := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b3"}, {Branch: "b4"}},
	}
	writeTwoStacks(t, gitDir, s1, s2)

	var unstackedNumber int
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(n int) (*github.RemoteStack, bool, error) {
			unstackedNumber = n
			return nil, true, nil // dissolved
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Stack removed from local tracking")
	assert.Contains(t, output, "Stack removed on GitHub")
	assert.Equal(t, 42, unstackedNumber)

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
	assert.NotContains(t, output, "Stack removed on GitHub")

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

	// Stack with no ID/Number (never synced to GitHub)
	writeStackFile(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	apiCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			apiCalled = true
			return nil, true, nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, apiCalled, "API should not be called when stack has no ID")
	assert.Contains(t, output, "no remote ID")
	assert.Contains(t, output, "Stack removed from local tracking")
	assert.NotContains(t, output, "Stack removed on GitHub")
}

func TestUnstack_ResolvesNumberFromID(t *testing.T) {
	// A local stack that predates the Number field (only ID stored) resolves
	// its stack number from the remote list before unstacking.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:    "99",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	var unstackedNumber int
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 99, Number: 7, PullRequests: []int{101, 102}}}, nil
		},
		UnstackFn: func(n int) (*github.RemoteStack, bool, error) {
			unstackedNumber = n
			return nil, true, nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 7, unstackedNumber, "should resolve the stack number from the internal ID")
	assert.Contains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Empty(t, sf.Stacks)
}

func TestUnstack_API404_TreatedAsIdempotentSuccess(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:     "99",
		Number: 99,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			return nil, false, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	// 404 means already gone — should succeed and remove locally
	require.NoError(t, err)
	assert.Contains(t, output, "continuing with local unstack")
	assert.Contains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	assert.Empty(t, sf.Stacks)
}

func TestUnstack_ServerError_StopsLocalDeletion(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:     "99",
		Number: 99,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			return nil, false, &api.HTTPError{StatusCode: 409, Message: "Stack is currently being modified"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "Failed to unstack on GitHub (HTTP 409)")
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

func TestUnstack_AllLocked_ServerRejects(t *testing.T) {
	// Every PR is queued for merge or has auto-merge enabled. The server
	// (not the client) rejects the unstack with a 422; the command surfaces the
	// error and leaves local tracking in place.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:     "99",
		Number: 99,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	unstackCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			unstackCalled = true
			return nil, false, &api.HTTPError{StatusCode: 422, Message: "all pull requests are queued for merge or have auto-merge enabled"}
		},
	}

	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.True(t, unstackCalled, "the server decides eligibility, so Unstack is called")
	assert.Contains(t, output, "Unstacking not allowed")
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_PartialUnstack_KeepsLocalTracking(t *testing.T) {
	// Some PRs (queued for merge / auto-merge) remain stacked, so the server
	// returns the surviving stack (dissolved=false). Local tracking is kept.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:     "99",
		Number: 99,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			return &github.RemoteStack{ID: 99, Number: 99, PullRequests: []int{102}}, false, nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "remain stacked on GitHub")
	assert.Contains(t, output, "local tracking is unchanged")
	assert.NotContains(t, output, "Stack removed from local tracking")

	// The stack still exists remotely, so local tracking is preserved.
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_NumberLookupFailure_StopsDeletion(t *testing.T) {
	// Resolving the stack number from its ID fails (list API error), so the
	// command aborts without touching local tracking.
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

	unstackCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, errors.New("network error")
		},
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			unstackCalled = true
			return nil, true, nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.False(t, unstackCalled, "Unstack should not be called if number lookup fails")
	assert.Contains(t, output, "failed to look up stack on GitHub")
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}
