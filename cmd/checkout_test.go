package cmd

import (
	"fmt"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
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

func TestCheckout_ByPRNumber_Local(t *testing.T) {
	// When a PR number exists locally, no API call should be made
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
	// No GitHubClientOverride — should resolve locally without API
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

// --- Remote checkout tests (numeric target, local miss → API fallback) ---

func TestCheckout_NumericTarget_StacksNotAvailable(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "123"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "not enabled")
}

func TestCheckout_NumericTarget_PRNotInStack(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 1, PullRequests: []int{10, 11}},
			}, nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "99"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "PR #99 is not part of a stack")
}

func TestCheckout_NumericTarget_NewStack(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	var createdBranches []string
	var trackingSet []string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) bool {
			return name == "main" // only trunk exists
		},
		FetchFn: func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			createdBranches = append(createdBranches, name)
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error {
			trackingSet = append(trackingSet, branch)
			return nil
		},
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 42, PullRequests: []int{10, 11, 12}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
				12: {ID: "PR_12", Number: 12, HeadRefName: "feat-3", BaseRefName: "feat-2", URL: "https://github.com/o/r/pull/12"},
			}
			pr, ok := prs[number]
			if !ok {
				return nil, fmt.Errorf("PR #%d not found", number)
			}
			return pr, nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)

	// Should create the 3 branches (trunk "main" already exists)
	assert.Equal(t, []string{"feat-1", "feat-2", "feat-3"}, createdBranches)
	assert.Equal(t, []string{"feat-1", "feat-2", "feat-3"}, trackingSet)

	// Should checkout the target PR's branch
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 3 branches")
	assert.Contains(t, output, "Switched to feat-2")

	// Verify stack was saved
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, "42", sf.Stacks[0].ID)
	assert.Equal(t, "main", sf.Stacks[0].Trunk.Branch)
	assert.Len(t, sf.Stacks[0].Branches, 3)
	assert.Equal(t, 10, sf.Stacks[0].Branches[0].PullRequest.Number)
	assert.Equal(t, 11, sf.Stacks[0].Branches[1].PullRequest.Number)
	assert.Equal(t, 12, sf.Stacks[0].Branches[2].PullRequest.Number)
}

func TestCheckout_NumericTarget_BranchExistsNoStack(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	var createdBranches []string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) bool {
			// feat-1 exists locally but feat-2 does not
			return name == "main" || name == "feat-1"
		},
		FetchFn: func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			createdBranches = append(createdBranches, name)
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// No stacks exist locally
	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 99, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)

	// Only feat-2 should be created (feat-1 and main already exist)
	assert.Equal(t, []string{"feat-2"}, createdBranches)
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 2 branches")
}

func TestCheckout_NumericTarget_AlreadyInMatchingStack(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// Stack already exists locally with matching PRs
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10, URL: "https://github.com/o/r/pull/10"}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11, URL: "https://github.com/o/r/pull/11"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// PR 10 is found locally → no API call needed
	// No GitHubClientOverride means API calls would panic
	err := runCheckout(cfg, &checkoutOptions{target: "10"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-1", checkedOut)
	assert.Contains(t, output, "Switched to feat-1")
}

func TestCheckout_NumericTarget_LocalMiss_RemoteMatch(t *testing.T) {
	// PR 11 is NOT in any local stack, but IS in a remote stack.
	// The API should be called as a fallback.
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) bool {
			return name == "main"
		},
		FetchFn:               func(remote string) error { return nil },
		CreateBranchFn:        func(name, base string) error { return nil },
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn:       func(branch string) (string, error) { return "origin", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// Local stack has PR 42 only — PR 11 is not tracked
	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "other-branch", PullRequest: &stack.PullRequestRef{Number: 42}},
		},
	})

	apiCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			apiCalled = true
			return []github.RemoteStack{
				{ID: 99, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	_ = collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.True(t, apiCalled, "should have called ListStacks API when local miss")
	assert.Equal(t, "feat-2", checkedOut)
}

func TestCheckout_NumericTarget_FallbackToBranchName(t *testing.T) {
	// PR 999 is not in any local stack and not in any remote stack,
	// but "999" happens to be a branch name in a local stack
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
			{Branch: "999", PullRequest: &stack.PullRequestRef{Number: 50}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil // no remote stacks
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "999"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "999", checkedOut)
	assert.Contains(t, output, "Switched to 999")
}

func TestCheckout_NumericTarget_CompositionMismatch_NonInteractive(t *testing.T) {
	gitDir := t.TempDir()

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	// Local stack has PRs 10, 11
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			// Remote stack has PRs 10, 11, 12 (extra PR added)
			return []github.RemoteStack{
				{ID: 42, PullRequests: []int{10, 11, 12}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1"},
				12: {ID: "PR_12", Number: 12, HeadRefName: "feat-3", BaseRefName: "feat-2"},
			}
			return prs[number], nil
		},
	}

	// PR 12 not found locally → remote lookup → finds stack → mismatch with local
	err := runCheckout(cfg, &checkoutOptions{target: "12"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, output, "local stack composition differs from remote")
	assert.Contains(t, output, "Local:")
	assert.Contains(t, output, "Remote:")
}

func TestCheckout_NumericTarget_ClosedMergedPR(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) bool {
			return name == "main"
		},
		FetchFn:               func(remote string) error { return nil },
		CreateBranchFn:        func(name, base string) error { return nil },
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 50, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", Merged: true, State: "MERGED", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", State: "OPEN", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 2 branches")

	// Verify merged state is saved
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.True(t, sf.Stacks[0].Branches[0].PullRequest.Merged)
	assert.False(t, sf.Stacks[0].Branches[1].PullRequest.Merged)
}

func TestCheckout_NumericTarget_APIError(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "123"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "failed to list stacks")
}

func TestCheckout_NumericTarget_SyncsState(t *testing.T) {
	gitDir := t.TempDir()

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// Existing stack with stale PR data — PR 10 found locally
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10, URL: "old-url"}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11, URL: "old-url"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// PR 10 is found locally → no API call needed, resolved directly
	err := runCheckout(cfg, &checkoutOptions{target: "10"})
	_ = collectOutput(cfg, outR, errR)

	require.NoError(t, err)
}

func TestCheckout_NumericTarget_EmptyStacks(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil // no stacks at all
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "123"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "PR #123 is not part of a stack")
}

func TestCheckout_NumericTarget_AlreadyOnTarget(t *testing.T) {
	gitDir := t.TempDir()
	checkoutCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "feat-1", nil },
		CheckoutBranchFn: func(name string) error {
			checkoutCalled = true
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10, URL: "https://github.com/o/r/pull/10"}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11, URL: "https://github.com/o/r/pull/11"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// PR 10 found locally → resolved without API
	err := runCheckout(cfg, &checkoutOptions{target: "10"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, checkoutCalled, "should not call CheckoutBranch when already on target")
	assert.Contains(t, output, "Already on feat-1")
}

// --- Helper tests ---

func TestStackCompositionMatches(t *testing.T) {
	tests := []struct {
		name    string
		local   *stack.Stack
		remote  []int
		matches bool
	}{
		{
			name: "exact match",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10}},
					{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 11}},
				},
			},
			remote:  []int{10, 11},
			matches: true,
		},
		{
			name: "different order",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 11}},
					{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 10}},
				},
			},
			remote:  []int{10, 11},
			matches: false,
		},
		{
			name: "remote has more",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10}},
				},
			},
			remote:  []int{10, 11},
			matches: false,
		},
		{
			name: "local has branch without PR",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10}},
					{Branch: "b"}, // no PR
				},
			},
			remote:  []int{10, 11},
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stackCompositionMatches(tt.local, tt.remote)
			assert.Equal(t, tt.matches, result)
		})
	}
}

func TestFindRemoteStackForPR(t *testing.T) {
	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 1, PullRequests: []int{10, 11}},
				{ID: 2, PullRequests: []int{20, 21, 22}},
			}, nil
		},
	}

	// Found in first stack
	rs, err := findRemoteStackForPR(mock, 11)
	require.NoError(t, err)
	require.NotNil(t, rs)
	assert.Equal(t, 1, rs.ID)

	// Found in second stack
	rs, err = findRemoteStackForPR(mock, 21)
	require.NoError(t, err)
	require.NotNil(t, rs)
	assert.Equal(t, 2, rs.ID)

	// Not found
	rs, err = findRemoteStackForPR(mock, 99)
	require.NoError(t, err)
	assert.Nil(t, rs)
}
