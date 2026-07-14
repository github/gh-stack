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

func TestUnstack_ByStackNumber(t *testing.T) {
	// Target a specific stack by its number, regardless of the current branch.
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
		ID:       "99",
		Number:   7,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b3"}, {Branch: "b4"}},
	}
	writeTwoStacks(t, gitDir, s1, s2)

	var unstackedNumber int
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(n int) (*github.RemoteStack, bool, error) {
			unstackedNumber = n
			return nil, true, nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{stackNumber: 7})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 7, unstackedNumber)
	assert.Contains(t, output, "Stack removed from local tracking")

	// The targeted stack (number 7 / b3,b4) is removed; the other is kept.
	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"b1", "b2"}, sf.Stacks[0].BranchNames())
}

func TestUnstack_ByStackNumber_RemoteOnly_Dissolved(t *testing.T) {
	// A stack number that isn't tracked locally is unstacked directly on GitHub
	// (remote-first), leaving unrelated local tracking untouched.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "42",
		Number:   42,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	var unstackedNumber int
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(n int) (*github.RemoteStack, bool, error) {
			unstackedNumber = n
			return nil, true, nil // dissolved
		},
	}
	err := runUnstack(cfg, &unstackOptions{stackNumber: 999})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 999, unstackedNumber, "should unstack the requested number on GitHub")
	assert.Contains(t, output, "Stack removed on GitHub")
	// No local tracking was touched.
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, 42, sf.Stacks[0].Number, "the unrelated local stack is left intact")
}

func TestUnstack_ByStackNumber_RemoteOnly_NotFound(t *testing.T) {
	// With no local tracking to reconcile, a 404 means the targeted stack does
	// not exist on GitHub — a hard error, not an idempotent success.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "42",
		Number:   42,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			return nil, false, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{stackNumber: 999})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "stack #999 not found on GitHub")
	assert.NotContains(t, output, "Stack removed")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_ByStackNumber_RemoteOnly_Partial(t *testing.T) {
	// Some PRs (queued for merge / auto-merge) remain stacked. There is no local
	// tracking to keep, so the command reports the outcome and succeeds.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "42",
		Number:   42,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			return &github.RemoteStack{ID: 555, Number: 999, PullRequests: []int{102}}, false, nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{stackNumber: 999})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "remain stacked on GitHub")
	// No local tracking is involved, so no local-tracking messaging is shown.
	assert.NotContains(t, output, "local tracking is unchanged")
	assert.NotContains(t, output, "Stack removed from local tracking")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_ByStackNumber_RemoteOnly_AllLocked(t *testing.T) {
	// Every PR is queued for merge or has auto-merge enabled; the server rejects
	// the unstack with a 422 and the command surfaces the error.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "42",
		Number:   42,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			return nil, false, &api.HTTPError{StatusCode: 422, Message: "all pull requests are queued for merge or have auto-merge enabled"}
		},
	}
	err := runUnstack(cfg, &unstackOptions{stackNumber: 999})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "Unstacking not allowed")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_ByStackNumber_NotTracked_LocalFlag(t *testing.T) {
	// --local never contacts GitHub. Targeting a number that isn't tracked
	// locally with --local is an error: there is nothing to remove locally.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:       "42",
		Number:   42,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	})

	unstackCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			unstackCalled = true
			return nil, true, nil
		},
	}
	err := runUnstack(cfg, &unstackOptions{stackNumber: 999, local: true})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.False(t, unstackCalled, "--local must never contact GitHub")
	assert.Contains(t, output, "stack #999 is not tracked locally")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_ByStackNumber_LocalFlag_LegacyStack_NoRemoteCall(t *testing.T) {
	// --local must never contact GitHub. A legacy stack (Number == 0) can only
	// be matched by number via a remote backfill (ListStacks); under --local
	// that lookup must be skipped and the number reported as not tracked.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	// Legacy: internal ID present, Number unset (0).
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "99",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
		},
	})

	listCalled := false
	unstackCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			listCalled = true
			return []github.RemoteStack{{ID: 99, Number: 7, PullRequests: []int{101, 102}}}, nil
		},
		UnstackFn: func(int) (*github.RemoteStack, bool, error) {
			unstackCalled = true
			return nil, true, nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{stackNumber: 7, local: true})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.False(t, listCalled, "--local must not contact GitHub (no ListStacks backfill)")
	assert.False(t, unstackCalled, "--local must not contact GitHub")
	assert.Contains(t, output, "stack #7 is not tracked locally")

	// Local tracking is untouched.
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
}

func TestUnstack_ByStackNumber_LegacyStackResolvedByID(t *testing.T) {
	// A stack tracked before the number was recorded (Number == 0) is resolved
	// by mapping its internal ID to the remote stack number, and the backfilled
	// number is persisted.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:    "99", // legacy: internal ID present, Number unset (0)
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
			// Some PRs remain stacked, so local tracking is kept.
			return &github.RemoteStack{ID: 99, Number: 7, PullRequests: []int{102}}, false, nil
		},
	}

	err := runUnstack(cfg, &unstackOptions{stackNumber: 7})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 7, unstackedNumber, "should resolve the legacy stack and unstack by its remote number")
	assert.Contains(t, output, "remain stacked on GitHub")

	// The backfilled number is persisted to the stack file.
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, 7, sf.Stacks[0].Number, "the resolved stack number should be persisted")
}
