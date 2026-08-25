package cmd

import (
	"errors"
	"testing"

	"github.com/AlecAivazis/survey/v2/terminal"
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

	rows, remote := gatherCheckoutRows(cfg, sf)
	require.Len(t, rows, 1, "falls back to a local-only list when ListStacks fails")
	assert.Empty(t, remote)
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

	rows, remote := gatherCheckoutRows(cfg, sf)
	require.Len(t, rows, 2, "local and remote-only stacks are both listed")
	require.Len(t, remote, 1)

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

func TestMatchingRemoteStacksForBranch(t *testing.T) {
	mergedAt := "2026-08-24T12:00:00Z"
	activeMatch := github.RemoteStack{
		Number: 1,
		PRDetails: []github.RemoteStackPR{
			{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
			{Number: 11, State: "open", Head: github.RemoteStackPRHead{Ref: "top"}},
		},
	}
	secondActiveMatch := github.RemoteStack{
		Number: 2,
		PRDetails: []github.RemoteStackPR{
			{Number: 20, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
		},
	}
	fullyMergedMatch := github.RemoteStack{
		Number: 3,
		PRDetails: []github.RemoteStackPR{
			{Number: 30, State: "closed", MergedAt: &mergedAt, Head: github.RemoteStackPRHead{Ref: "feature"}},
		},
	}

	tests := []struct {
		name   string
		remote []github.RemoteStack
		branch string
		want   []int
	}{
		{
			name:   "one active match",
			remote: []github.RemoteStack{activeMatch},
			branch: "feature",
			want:   []int{1},
		},
		{
			name:   "multiple active matches",
			remote: []github.RemoteStack{activeMatch, secondActiveMatch},
			branch: "feature",
			want:   []int{1, 2},
		},
		{
			name: "exact branch name only",
			remote: []github.RemoteStack{{
				Number: 4,
				PRDetails: []github.RemoteStackPR{
					{Number: 40, State: "open", Head: github.RemoteStackPRHead{Ref: "origin/feature"}},
				},
			}},
			branch: "feature",
			want:   []int{},
		},
		{
			name:   "fully merged match omitted",
			remote: []github.RemoteStack{fullyMergedMatch},
			branch: "feature",
			want:   []int{},
		},
		{
			name: "empty and unaddressable stacks omitted",
			remote: []github.RemoteStack{
				{Number: 5},
				{
					PRDetails: []github.RemoteStackPR{
						{Number: 60, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
					},
				},
			},
			branch: "feature",
			want:   []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := matchingRemoteStacksForBranch(tt.remote, tt.branch)
			numbers := make([]int, len(matches))
			for i, match := range matches {
				numbers[i] = match.Number
			}
			assert.Equal(t, tt.want, numbers)
		})
	}
}

func TestOfferRemoteStackForBranch_Confirmed(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cfg.ConfirmFn = func(prompt string, defaultValue bool) (bool, error) {
		assert.Equal(t, `Found stack #7 that includes branch "feature". Check out stack #7?`, prompt)
		assert.True(t, defaultValue)
		return true, nil
	}

	number, confirmed, err := offerRemoteStackForBranch(cfg, &stack.StackFile{}, []github.RemoteStack{{
		Number: 7,
		PRDetails: []github.RemoteStackPR{
			{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
		},
	}}, "feature")

	require.NoError(t, err)
	assert.True(t, confirmed)
	assert.Equal(t, 7, number)
}

func TestOfferRemoteStackForBranch_FallsBack(t *testing.T) {
	activeMatch := github.RemoteStack{
		Number: 7,
		PRDetails: []github.RemoteStackPR{
			{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
		},
	}
	secondMatch := github.RemoteStack{
		Number: 8,
		PRDetails: []github.RemoteStackPR{
			{Number: 11, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
		},
	}

	tests := []struct {
		name         string
		sf           *stack.StackFile
		remote       []github.RemoteStack
		wantPrompt   bool
		confirmation bool
		confirmErr   error
	}{
		{
			name: "branch already belongs to local stack",
			sf: &stack.StackFile{Stacks: []stack.Stack{{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "feature"}},
			}}},
			remote: []github.RemoteStack{activeMatch},
		},
		{
			name: "branch is a local stack trunk",
			sf: &stack.StackFile{Stacks: []stack.Stack{{
				Trunk:    stack.BranchRef{Branch: "feature"},
				Branches: []stack.BranchRef{{Branch: "other"}},
			}}},
			remote: []github.RemoteStack{activeMatch},
		},
		{
			name:   "multiple remote matches",
			sf:     &stack.StackFile{},
			remote: []github.RemoteStack{activeMatch, secondMatch},
		},
		{
			name:       "user declines",
			sf:         &stack.StackFile{},
			remote:     []github.RemoteStack{activeMatch},
			wantPrompt: true,
		},
		{
			name:       "confirmation fails",
			sf:         &stack.StackFile{},
			remote:     []github.RemoteStack{activeMatch},
			wantPrompt: true,
			confirmErr: errors.New("prompt failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, _ := config.NewTestConfig()
			prompted := false
			cfg.ConfirmFn = func(string, bool) (bool, error) {
				prompted = true
				if !tt.wantPrompt {
					t.Fatal("confirmation should not be shown")
				}
				return tt.confirmation, tt.confirmErr
			}

			number, confirmed, err := offerRemoteStackForBranch(cfg, tt.sf, tt.remote, "feature")

			require.NoError(t, err)
			assert.False(t, confirmed)
			assert.Zero(t, number)
			assert.Equal(t, tt.wantPrompt, prompted)
		})
	}
}

func TestOfferRemoteStackForBranch_InterruptAborts(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.ConfirmFn = func(string, bool) (bool, error) {
		return false, terminal.InterruptErr
	}

	number, confirmed, err := offerRemoteStackForBranch(cfg, &stack.StackFile{}, []github.RemoteStack{{
		Number: 7,
		PRDetails: []github.RemoteStackPR{
			{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
		},
	}}, "feature")
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, errInterrupt)
	assert.False(t, confirmed)
	assert.Zero(t, number)
	assert.Contains(t, output, "Received interrupt, aborting operation")
}

func TestCheckout_NoTarget_ConfirmedRemoteMatch(t *testing.T) {
	gitDir := t.TempDir()
	currentBranch := "feature"
	checkedOut := ""
	branches := map[string]bool{"main": true, "feature": true}
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return currentBranch, nil },
		BranchExistsFn:  func(name string) bool { return branches[name] },
		FetchFn:         func(string) error { return nil },
		ResolveRemoteFn: func(string) (string, error) { return "origin", nil },
		CreateBranchFn: func(name, _ string) error {
			branches[name] = true
			return nil
		},
		SetUpstreamTrackingFn: func(string, string) error { return nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			currentBranch = name
			return nil
		},
		RevParseFn: func(string) (string, error) { return "abc123", nil },
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

	listCalls := 0
	cfg, _, _ := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.ConfirmFn = func(string, bool) (bool, error) { return true, nil }
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			listCalls++
			return []github.RemoteStack{{
				ID:     42,
				Number: 7,
				Base:   github.RemoteStackBase{Ref: "main"},
				PRDetails: []github.RemoteStackPR{
					{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
					{Number: 11, State: "open", Head: github.RemoteStackPRHead{Ref: "feature-top"}},
				},
			}}, nil
		},
		GetStackFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 42, Number: 7, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feature", BaseRefName: "main"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feature-top", BaseRefName: "feature"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{})

	require.NoError(t, err)
	assert.Equal(t, 1, listCalls, "auto-detection and picker data should share one remote list")
	assert.Equal(t, "feature-top", checkedOut)

	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, 7, sf.Stacks[0].Number)
	assert.Equal(t, []string{"feature", "feature-top"}, sf.Stacks[0].BranchNames())
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
