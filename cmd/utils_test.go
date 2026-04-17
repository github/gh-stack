package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsInterruptError_DirectMatch(t *testing.T) {
	if !isInterruptError(terminal.InterruptErr) {
		t.Error("expected true for terminal.InterruptErr")
	}
}

func TestIsInterruptError_Wrapped(t *testing.T) {
	// This is how the prompter library wraps the interrupt error.
	wrapped := fmt.Errorf("could not prompt: %w", terminal.InterruptErr)
	if !isInterruptError(wrapped) {
		t.Error("expected true for wrapped interrupt error")
	}
}

func TestIsInterruptError_DoubleWrapped(t *testing.T) {
	// Simulate additional wrapping by callers.
	inner := fmt.Errorf("could not prompt: %w", terminal.InterruptErr)
	outer := fmt.Errorf("stack selection: %w", inner)
	if !isInterruptError(outer) {
		t.Error("expected true for double-wrapped interrupt error")
	}
}

func TestIsInterruptError_NonInterrupt(t *testing.T) {
	if isInterruptError(errors.New("some other error")) {
		t.Error("expected false for non-interrupt error")
	}
}

func TestIsInterruptError_Nil(t *testing.T) {
	if isInterruptError(nil) {
		t.Error("expected false for nil error")
	}
}

func TestPrintInterrupt_Output(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	printInterrupt(cfg)
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "Received interrupt, aborting operation") {
		t.Errorf("expected interrupt message, got: %s", output)
	}
	// Should NOT contain error marker (✗)
	if strings.Contains(output, "\u2717") {
		t.Errorf("interrupt message should not use error format, got: %s", output)
	}
}

func TestErrInterrupt_IsDistinct(t *testing.T) {
	if errors.Is(errInterrupt, terminal.InterruptErr) {
		t.Error("errInterrupt sentinel should not match terminal.InterruptErr")
	}
	if !errors.Is(errInterrupt, errInterrupt) {
		t.Error("errInterrupt should match itself")
	}
}

func TestEnsureRerere_SkipsWhenAlreadyEnabled(t *testing.T) {
	enableCalled := false
	restore := git.SetOps(&git.MockOps{
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		EnableRerereFn: func() error {
			enableCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	_ = ensureRerere(cfg)
	collectOutput(cfg, outR, errR)

	if enableCalled {
		t.Error("EnableRerere should not be called when already enabled")
	}
}

func TestEnsureRerere_SkipsWhenDeclined(t *testing.T) {
	enableCalled := false
	restore := git.SetOps(&git.MockOps{
		IsRerereEnabledFn:  func() (bool, error) { return false, nil },
		IsRerereDeclinedFn: func() (bool, error) { return true, nil },
		EnableRerereFn: func() error {
			enableCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	_ = ensureRerere(cfg)
	collectOutput(cfg, outR, errR)

	if enableCalled {
		t.Error("EnableRerere should not be called when user previously declined")
	}
}

func TestEnsureRerere_SkipsWhenNonInteractive(t *testing.T) {
	enableCalled := false
	declinedSaved := false
	restore := git.SetOps(&git.MockOps{
		IsRerereEnabledFn:  func() (bool, error) { return false, nil },
		IsRerereDeclinedFn: func() (bool, error) { return false, nil },
		EnableRerereFn: func() error {
			enableCalled = true
			return nil
		},
		SaveRerereDeclinedFn: func() error {
			declinedSaved = true
			return nil
		},
	})
	defer restore()

	// NewTestConfig is non-interactive (pipes, not a TTY).
	cfg, outR, errR := config.NewTestConfig()
	_ = ensureRerere(cfg)
	collectOutput(cfg, outR, errR)

	if enableCalled {
		t.Error("EnableRerere should not be called in non-interactive mode")
	}
	if declinedSaved {
		t.Error("SaveRerereDeclined should not be called in non-interactive mode")
	}
}

func TestResolvePR_ByPRNumber(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
					{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 43, URL: "https://github.com/o/r/pull/43"}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	s, br, err := resolvePR(cfg, sf, "42")
	assert.NoError(t, err)
	assert.Equal(t, "feat-1", br.Branch)
	assert.Equal(t, 42, br.PullRequest.Number)
	assert.Equal(t, "main", s.Trunk.Branch)
}

func TestResolvePR_ByPRURL(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	s, br, err := resolvePR(cfg, sf, "https://github.com/o/r/pull/42")
	assert.NoError(t, err)
	assert.Equal(t, "feat-1", br.Branch)
	assert.Equal(t, "main", s.Trunk.Branch)
}

func TestResolvePR_ByBranchName(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 42}},
					{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 43}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	s, br, err := resolvePR(cfg, sf, "feat-2")
	assert.NoError(t, err)
	assert.Equal(t, "feat-2", br.Branch)
	assert.Equal(t, 43, br.PullRequest.Number)
	assert.Equal(t, "main", s.Trunk.Branch)
}

func TestResolvePR_NotFound(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "feat-1"}},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	_, _, err := resolvePR(cfg, sf, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no locally tracked stack found")
}

func TestResolvePR_URLPrecedesNumber(t *testing.T) {
	// A PR URL that contains number 99 should resolve via URL parsing,
	// even if PR #99 doesn't exist — the URL parser extracts the number.
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 99, URL: "https://github.com/o/r/pull/99"}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	_, br, err := resolvePR(cfg, sf, "https://github.com/o/r/pull/99")
	assert.NoError(t, err)
	assert.Equal(t, 99, br.PullRequest.Number)
}

func TestSyncStackPRs_NoTrackedPR_OnlyAdoptsOpenPRs(t *testing.T) {
	// A branch with no tracked PR should only adopt OPEN PRs,
	// not stale merged/closed PRs from a previous branch name usage.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "reused-branch"}, // no PullRequest
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		// FindPRForBranch (OPEN only) returns nil — no open PR.
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// Branch should still have no PR tracked.
	assert.Nil(t, s.Branches[0].PullRequest)
}

func TestSyncStackPRs_NoTrackedPR_AdoptsOpenPR(t *testing.T) {
	// A branch with no tracked PR should adopt an OPEN PR it discovers.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature"}, // no PullRequest
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 99,
				ID:     "PR_99",
				URL:    "https://github.com/o/r/pull/99",
				State:  "OPEN",
			}, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 99, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_TrackedPR_DetectsMerge(t *testing.T) {
	// A branch with a tracked PR should detect when that PR gets merged.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 42,
					ID:     "PR_42",
					URL:    "https://github.com/o/r/pull/42",
				},
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 42,
				ID:     "PR_42",
				URL:    "https://github.com/o/r/pull/42",
				State:  "MERGED",
				Merged: true,
			}, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 42, s.Branches[0].PullRequest.Number)
	assert.True(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_MergedBranch_StaysMerged(t *testing.T) {
	// A merged branch should stay merged — no API calls, no changes.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "merged-branch",
				PullRequest: &stack.PullRequestRef{
					Number: 20,
					ID:     "PR_20",
					URL:    "https://github.com/o/r/pull/20",
					Merged: true,
				},
			},
		},
	}

	apiCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			apiCalled = true
			return nil, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			apiCalled = true
			return nil, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 20, s.Branches[0].PullRequest.Number)
	assert.True(t, s.Branches[0].PullRequest.Merged)
	assert.False(t, apiCalled, "no API calls should be made for merged branches")
}

func TestSyncStackPRs_ClosedPR_ReplacedByOpenPR(t *testing.T) {
	// A tracked PR that was closed (not merged) should be replaced
	// by a new OPEN PR if one exists.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 10,
					ID:     "PR_10",
					URL:    "https://github.com/o/r/pull/10",
				},
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10,
				State:  "CLOSED",
				Merged: false,
			}, nil
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 15,
				ID:     "PR_15",
				URL:    "https://github.com/o/r/pull/15",
				State:  "OPEN",
			}, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 15, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_TrackedOpenPR_UpdatesQueued(t *testing.T) {
	// A tracked OPEN PR that enters a merge queue should have Queued set.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 42,
					ID:     "PR_42",
					URL:    "https://github.com/o/r/pull/42",
				},
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 42,
				State:  "OPEN",
				MergeQueueEntry: &github.MergeQueueEntry{
					ID: "MQ_1",
				},
			}, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	assert.True(t, s.Branches[0].Queued)
}

func TestSyncStackPRs_ClosedPR_NoReplacement_ClearsPR(t *testing.T) {
	// A tracked PR that was closed with no replacement OPEN PR should
	// have its PR ref cleared so it doesn't appear as an active PR.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 10,
					ID:     "PR_10",
					URL:    "https://github.com/o/r/pull/10",
				},
				Queued: true,
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10,
				State:  "CLOSED",
				Merged: false,
			}, nil
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil // no open replacement
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	assert.Nil(t, s.Branches[0].PullRequest)
	assert.False(t, s.Branches[0].Queued)
}

func TestSyncStackPRs_RemoteStack_UsesStackAPI(t *testing.T) {
	// When the stack has a remote ID, sync should use the stack API
	// as source of truth, matching PRs to branches by head ref name.
	s := &stack.Stack{
		ID:    "100",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 100, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 10:
				return &github.PullRequest{Number: 10, ID: "PR_10", URL: "https://github.com/o/r/pull/10", HeadRefName: "b1", State: "OPEN"}, nil
			case 11:
				return &github.PullRequest{Number: 11, ID: "PR_11", URL: "https://github.com/o/r/pull/11", HeadRefName: "b2", State: "MERGED", Merged: true}, nil
			}
			return nil, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// b1 should be tracked with open PR
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 10, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)

	// b2 should be tracked with merged PR (stack API keeps closed/merged PRs)
	require.NotNil(t, s.Branches[1].PullRequest)
	assert.Equal(t, 11, s.Branches[1].PullRequest.Number)
	assert.True(t, s.Branches[1].PullRequest.Merged)
}

func TestSyncStackPRs_RemoteStack_ClosedPRStaysAssociated(t *testing.T) {
	// When using the stack API, a closed (not merged) PR should remain
	// associated — the stack API is the source of truth, not PR state.
	s := &stack.Stack{
		ID:    "200",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature", PullRequest: &stack.PullRequestRef{Number: 5}},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 200, PullRequests: []int{5}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 5, ID: "PR_5", URL: "https://github.com/o/r/pull/5", HeadRefName: "feature", State: "CLOSED"}, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// PR should still be associated (not cleared), because the stack API says it's part of the stack.
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 5, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_RemoteStack_FallsBackOnAPIError(t *testing.T) {
	// If the stack API fails, fall back to local discovery.
	s := &stack.Stack{
		ID:    "300",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature"},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, fmt.Errorf("API error")
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 77, ID: "PR_77", URL: "https://github.com/o/r/pull/77", State: "OPEN"}, nil
		},
	}

	syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// Should have fallen back to local discovery and found the open PR.
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 77, s.Branches[0].PullRequest.Number)
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantN  int
		wantOK bool
	}{
		{"standard URL", "https://github.com/owner/repo/pull/42", 42, true},
		{"with trailing slash", "https://github.com/owner/repo/pull/42/", 42, true},
		{"with files tab", "https://github.com/owner/repo/pull/42/files", 42, true},
		{"GHES URL", "https://ghes.example.com/owner/repo/pull/99", 99, true},
		{"GHES URL with trailing slash", "https://ghes.example.com/owner/repo/pull/7/", 7, true},
		{"not a PR URL", "https://github.com/owner/repo/issues/42", 0, false},
		{"plain number", "42", 0, false},
		{"branch name", "feat-1", 0, false},
		{"empty", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := parsePRURL(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantN, n)
			}
		})
	}
}
