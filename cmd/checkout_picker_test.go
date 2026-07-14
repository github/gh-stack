package cmd

import (
	"errors"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/checkoutview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInteractiveCheckout_NonInteractive(t *testing.T) {
	cfg, _, _ := config.NewTestConfig() // ForceInteractive defaults to false
	sf := &stack.StackFile{SchemaVersion: 1}

	_, _, err := interactiveCheckout(cfg, sf, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no target specified")
}

func TestGatherCheckoutRows_FallbackToLocalOnListError(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "stacks not enabled"}
		},
	}

	sf := &stack.StackFile{Stacks: []stack.Stack{{
		Number: 5,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-a", PullRequest: &stack.PullRequestRef{Number: 1}},
			{Branch: "feat-b"},
		},
	}}}

	rows := gatherCheckoutRows(cfg, sf)
	require.Len(t, rows, 1, "falls back to a local-only list when ListStacks fails")
	assert.Equal(t, checkoutview.TypeLocal, rows[0].Type)
	assert.Equal(t, 5, rows[0].Number)
}

func TestGatherCheckoutRows_IncludesRemoteOnlyStacks(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{
				ID:     200,
				Number: 55,
				Base:   github.RemoteStackBase{Ref: "main"},
				PRDetails: []github.RemoteStackPR{
					{Number: 7, State: "open", Head: github.RemoteStackPRHead{Ref: "r1"}},
					{Number: 8, State: "open", Head: github.RemoteStackPRHead{Ref: "r2"}},
				},
			}}, nil
		},
	}

	sf := &stack.StackFile{Stacks: []stack.Stack{{
		Number:   3,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "local-a"}},
	}}}

	rows := gatherCheckoutRows(cfg, sf)
	require.Len(t, rows, 2, "local and remote-only stacks are both listed")

	var haveLocal, haveRemote bool
	for _, r := range rows {
		switch r.Type {
		case checkoutview.TypeLocal:
			haveLocal = true
		case checkoutview.TypeRemote:
			haveRemote = true
			assert.Equal(t, 55, r.Number)
		}
	}
	assert.True(t, haveLocal, "local stack present")
	assert.True(t, haveRemote, "remote-only stack present")
}

func TestResolveCheckoutSelection_Local(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	localStack := &stack.Stack{
		Number: 3,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 2}},
		},
	}
	sel := checkoutview.StackRow{Type: checkoutview.TypeLocal, Number: 3, LocalStack: localStack}

	s, branch, err := resolveCheckoutSelection(cfg, &stack.StackFile{}, t.TempDir(), sel)
	require.NoError(t, err)
	assert.Same(t, localStack, s)
	assert.Equal(t, "b", branch, "checks out the top unmerged branch")
}

func TestResolveCheckoutSelection_RemoteRoutesToClone(t *testing.T) {
	gitDir := t.TempDir()
	var createdBranches []string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(name string) bool { return name == "main" },
		FetchFn:         func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			createdBranches = append(createdBranches, name)
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn:       func(branch string) (string, error) { return "origin", nil },
		CheckoutBranchFn:      func(name string) error { return nil },
		RevParseFn:            func(ref string) (string, error) { return "abc123", nil },
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
	sf, err := stack.Load(gitDir)
	require.NoError(t, err)

	var gotStackNumber int
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			gotStackNumber = n
			return &github.RemoteStack{ID: 42, Number: 7, PullRequests: []int{10, 11, 12}}, nil
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

	sel := checkoutview.StackRow{Type: checkoutview.TypeRemote, Number: 7}
	s, branch, err := resolveCheckoutSelection(cfg, sf, gitDir, sel)
	require.NoError(t, err)
	assert.Equal(t, 7, gotStackNumber, "remote selection is cloned by stack number")
	assert.Equal(t, "feat-3", branch, "targets the top-most branch")
	require.NotNil(t, s)
	assert.Equal(t, "42", s.ID)
	assert.Equal(t, 7, s.Number)
}

func TestResolveCheckoutSelection_RemoteLoadFailure(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return nil, errors.New("boom")
		},
	}

	sel := checkoutview.StackRow{Type: checkoutview.TypeRemote, Number: 7}
	_, _, err := resolveCheckoutSelection(cfg, &stack.StackFile{}, t.TempDir(), sel)

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ErrAPIFailure, err)
}

func TestTopUnmergedBranch(t *testing.T) {
	tests := []struct {
		name     string
		branches []stack.BranchRef
		expect   string
	}{
		{"empty", nil, ""},
		{
			name: "some unmerged",
			branches: []stack.BranchRef{
				{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
				{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 2}},
				{Branch: "c"},
			},
			expect: "c",
		},
		{
			name: "all merged falls back to top",
			branches: []stack.BranchRef{
				{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
				{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
			},
			expect: "b",
		},
		{
			name: "merged on top of unmerged",
			branches: []stack.BranchRef{
				{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 1}},
				{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
			},
			expect: "a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stack.Stack{Branches: tt.branches}
			assert.Equal(t, tt.expect, topUnmergedBranch(s))
		})
	}
}
