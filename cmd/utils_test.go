package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/cli/go-gh/v2/pkg/repository"
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

func TestSyncStackPRs_BackfillsStackNumber(t *testing.T) {
	// A stack tracked before the number was recorded (Number == 0) gets its
	// number backfilled from the remote during the shared sync, so callers can
	// display it.
	s := &stack.Stack{
		ID:    "100", // legacy: Number unset
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
				{ID: 100, Number: 5, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 10:
				return &github.PullRequest{Number: 10, HeadRefName: "b1", State: "OPEN"}, nil
			case 11:
				return &github.PullRequest{Number: 11, HeadRefName: "b2", State: "OPEN"}, nil
			}
			return nil, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	assert.Equal(t, 5, s.Number, "the stack number should be backfilled from the remote")
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

	_ = syncStackPRs(cfg, s)
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

	_ = syncStackPRs(cfg, s)
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

func TestStackNeedsRebase_AllCurrent(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	mock := &git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			return true, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	assert.False(t, stackNeedsRebase(s), "stack should not need rebase when all branches are current")
}

func TestStackNeedsRebase_FirstBranchStale(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	mock := &git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			if a == "main" && d == "b1" {
				return false, nil
			}
			return true, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	assert.True(t, stackNeedsRebase(s), "stack should need rebase when first branch is stale")
}

func TestStackNeedsRebase_SkipsMergedBranches(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Merged: true}},
			{Branch: "b2"},
		},
	}

	mock := &git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			return true, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	assert.False(t, stackNeedsRebase(s), "should skip merged branches and find stack up to date")
}

// setTestRepo sets RepoOverride so tests don't depend on real git context.
func setTestRepo(cfg *config.Config) {
	cfg.RepoOverride = &repository.Repository{Host: "github.com", Owner: "o", Name: "r"}
}

func TestWarnStacksUnavailable_ShowsNotEnabled(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()

	warnStacksUnavailable(cfg)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "Stacked PRs are not enabled for this repository")
}

func TestEnsureLocalTrunk_AlreadyExists(t *testing.T) {
	mock := &git.MockOps{
		BranchExistsFn: func(name string) bool {
			return name == "main"
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")
	assert.NoError(t, err)
}

func TestEnsureLocalTrunk_FetchesAndCreates(t *testing.T) {
	var fetchedBranches []string
	var createdBranch, createdBase string

	mock := &git.MockOps{
		BranchExistsFn: func(name string) bool {
			return false
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			fetchedBranches = branches
			return nil
		},
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			createdBase = base
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")

	assert.NoError(t, err)
	assert.Equal(t, []string{"main"}, fetchedBranches)
	assert.Equal(t, "main", createdBranch)
	assert.Equal(t, "origin/main", createdBase)
}

func TestEnsureLocalTrunk_FetchFails(t *testing.T) {
	mock := &git.MockOps{
		BranchExistsFn: func(name string) bool {
			return false
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			return fmt.Errorf("network error")
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not fetch trunk branch main from origin")
}

func TestEnsureLocalTrunk_CreateFails(t *testing.T) {
	mock := &git.MockOps{
		BranchExistsFn: func(name string) bool {
			return false
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			return nil
		},
		CreateBranchFn: func(name, base string) error {
			return fmt.Errorf("ref not found")
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not create local trunk branch main")
}

func TestEnrichPRContent(t *testing.T) {
	calls := 0
	client := &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			calls++
			return &github.PullRequest{Number: number, Title: "Fetched title", Body: "Fetched body"}, nil
		},
	}
	details := map[string]*github.PRDetails{
		"merged": {Number: 10, State: "MERGED"},                // missing title -> fetched
		"open":   {Number: 11, State: "OPEN", Title: "Has it"}, // already has a title -> skipped
		"nonum":  {Number: 0, State: "OPEN"},                   // no number -> skipped
	}

	enrichPRContent(client, details)

	assert.Equal(t, 1, calls, "only the title-less PR with a number is fetched")
	assert.Equal(t, "Fetched title", details["merged"].Title)
	assert.Equal(t, "Fetched body", details["merged"].Body)
	assert.Equal(t, "Has it", details["open"].Title, "PRs that already have a title are untouched")
}

// defaultStackBranches lists the branch names used by most cascade tests.
var defaultStackBranches = []string{"main", "trunk", "b1", "b2", "b3", "b4"}

// stackedAncestry wraps an IsAncestor mock so that ancestry questions about
// plain branch names answer true, while questions about the fake SHAs used to
// drive fast-forward detection fall through to fn.
//
// Mock rebases never move refs, so without this the post-rebase verification
// (verifyStacked) would always see an unstacked stack.
func stackedAncestry(branches []string, fn func(a, d string) (bool, error)) func(string, string) (bool, error) {
	known := make(map[string]bool, len(branches))
	for _, b := range branches {
		known[b] = true
	}
	return func(a, d string) (bool, error) {
		if known[a] && known[d] {
			return true, nil
		}
		if fn == nil {
			return false, nil
		}
		return fn(a, d)
	}
}

// TestResolveOntoOldBase covers the `git rebase --onto <newBase> <upstream>`
// upstream selection. Passing a commit the branch does not contain makes git
// replay commits that are already in the new base, which is how a cascade
// rebase ends up duplicating a parent's commits.
func TestResolveOntoOldBase(t *testing.T) {
	// History used throughout: fork <- p1 <- p2 (parent), and the child was
	// built on p1 before the parent moved on to p2.
	ancestorsOfChild := map[string]bool{"fork": true, "p1": true}

	tests := []struct {
		name            string
		recordedOldBase string
		metadataBase    string
		newBase         string
		mergeBases      map[string]string // "a|b" -> merge-base
		want            string
		wantReason      string
	}{
		{
			name:            "recorded old base is used when the branch contains it",
			recordedOldBase: "p1",
			metadataBase:    "fork",
			newBase:         "parent",
			want:            "p1",
			wantReason:      "the common case must not pay for extra lookups",
		},
		{
			name:            "falls back to the recorded metadata base when the parent moved",
			recordedOldBase: "p2",
			metadataBase:    "p1",
			newBase:         "parent",
			mergeBases:      map[string]string{"p2|child": "fork", "parent|child": "fork"},
			want:            "p1",
			wantReason:      "p1 is the latest commit the child actually contains",
		},
		{
			name:            "uses the latest merge-base when no metadata base is recorded",
			recordedOldBase: "p2",
			metadataBase:    "",
			newBase:         "parent",
			mergeBases:      map[string]string{"p2|child": "p1", "parent|child": "fork"},
			want:            "p1",
			wantReason:      "replaying from fork would duplicate the parent's commits",
		},
		{
			name:            "prefers the new base merge-base when it is later",
			recordedOldBase: "p2",
			metadataBase:    "fork",
			newBase:         "parent",
			mergeBases:      map[string]string{"p2|child": "fork", "parent|child": "p1"},
			want:            "p1",
			wantReason:      "the child is already on top of part of the new base",
		},
		{
			name:            "keeps the recorded old base when nothing better is an ancestor",
			recordedOldBase: "p2",
			metadataBase:    "unrelated",
			newBase:         "parent",
			mergeBases:      map[string]string{"p2|child": "unrelated", "parent|child": "unrelated"},
			want:            "p2",
			wantReason:      "never invent an upstream out of thin air",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := git.SetOps(&git.MockOps{
				IsAncestorFn: func(a, d string) (bool, error) {
					if d == "child" {
						return ancestorsOfChild[a], nil
					}
					// Ordering between two ancestors of the child.
					return a == "fork" && d == "p1", nil
				},
				MergeBaseFn: func(a, b string) (string, error) {
					mb, ok := tt.mergeBases[a+"|"+b]
					if !ok {
						return "", fmt.Errorf("no merge base for %s and %s", a, b)
					}
					return mb, nil
				},
			})
			defer restore()

			got := resolveOntoOldBase(tt.recordedOldBase, tt.metadataBase, tt.newBase, "child")
			assert.Equal(t, tt.want, got, tt.wantReason)
		})
	}
}

func TestVerifyStacked(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}
	// b1 is not on main and b3 is not on b2.
	broken := map[string]bool{"main|b1": true, "b2|b3": true}
	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) { return !broken[a+"|"+d], nil },
	})
	defer restore()

	tests := []struct {
		name    string
		start   int
		end     int
		want    []string
		wantWhy string
	}{
		{name: "whole stack", start: 0, end: 3, want: []string{"b1", "b3"}},
		{name: "no-trunk range skips the bottom branch", start: 1, end: 3, want: []string{"b3"},
			wantWhy: "b1 is deliberately left off trunk by --no-trunk"},
		{name: "downstack range", start: 0, end: 2, want: []string{"b1"}},
		{name: "upstack range", start: 2, end: 3, want: []string{"b3"}},
		{name: "out of range end is clamped", start: 0, end: 99, want: []string{"b1", "b3"}},
		{name: "negative start is clamped", start: -5, end: 1, want: []string{"b1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, verifyStacked(s, tt.start, tt.end), tt.wantWhy)
		})
	}
}

// TestVerifyStacked_SkipsMergedAndQueued verifies that merged and queued
// branches are excluded and that their children are checked against the
// nearest active ancestor instead.
func TestVerifyStacked_SkipsMergedAndQueued(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", Queued: true},
			{Branch: "b3"},
		},
	}
	var checked []string
	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			checked = append(checked, a+"->"+d)
			return true, nil
		},
	})
	defer restore()

	assert.Empty(t, verifyStacked(s, 0, 3))
	assert.Equal(t, []string{"main->b3"}, checked,
		"b3's nearest active ancestor is trunk, since b1 is merged and b2 is queued")
}
