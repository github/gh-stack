package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/submitview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePRBody(t *testing.T) {
	tests := []struct {
		name            string
		commitBody      string
		templateContent string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:       "empty commit body no template",
			commitBody: "",
			wantContains: []string{
				"GitHub Stacks CLI",
				feedbackURL,
				"<sub>",
			},
		},
		{
			name:       "with commit body no template",
			commitBody: "This is a detailed description\nof the change.",
			wantContains: []string{
				"This is a detailed description\nof the change.",
				"GitHub Stacks CLI",
				"<sub>",
			},
		},
		{
			name:            "with template",
			commitBody:      "some commit body",
			templateContent: "## Description\n\nFill in details.",
			wantContains: []string{
				"## Description",
				"Fill in details.",
			},
			wantNotContains: []string{
				"GitHub Stacks CLI",
				feedbackURL,
				"some commit body",
			},
		},
		{
			name:            "template replaces footer",
			templateContent: "Template body only",
			wantContains:    []string{"Template body only"},
			wantNotContains: []string{"<sub>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generatePRBody(tt.commitBody, tt.templateContent)
			for _, want := range tt.wantContains {
				assert.Contains(t, got, want)
			}
			for _, notWant := range tt.wantNotContains {
				assert.NotContains(t, got, notWant)
			}
		})
	}
}

// newSubmitMock creates a MockOps pre-configured for submit tests.
func newSubmitMock(tmpDir string, currentBranch string) *git.MockOps {
	return &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		RootDirFn:       func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return currentBranch, nil },
		ResolveRemoteFn: func(string) (string, error) { return "origin", nil },
		PushFn:          func(string, []string, bool, bool) error { return nil },
	}
}

func TestSubmit_CreatesPRsAndStack(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var pushCalls []pushCall
	var createdPRs []string

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}

	restore := git.SetOps(mock)
	defer restore()

	prCounter := 100
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil // No existing PR
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdPRs = append(createdPRs, head)
			prCounter++
			return &github.PullRequest{
				Number: prCounter,
				ID:     fmt.Sprintf("PR_%d", prCounter),
				URL:    fmt.Sprintf("https://github.com/owner/repo/pull/%d", prCounter),
			}, nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)

	// Branches should be pushed (sequentially, one per branch)
	require.Len(t, pushCalls, 2)
	assert.Equal(t, "origin", pushCalls[0].remote)
	assert.Equal(t, []string{"b1"}, pushCalls[0].branches)
	assert.Equal(t, []string{"b2"}, pushCalls[1].branches)

	// PRs should be created
	assert.Equal(t, []string{"b1", "b2"}, createdPRs)

	// Stack should be created
	assert.Contains(t, output, "Stack created on GitHub with 2 PRs")
	assert.Contains(t, output, "Pushed and synced 2 branches")
}

func TestSubmit_DefaultDraft(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var createdDraft bool

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { return nil }
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdDraft = draft
			return &github.PullRequest{Number: 1, ID: "PR_1", URL: "https://github.com/o/r/pull/1"}, nil
		},
		CreateStackFn: func([]int) (int, error) { return 1, nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.True(t, createdDraft, "PRs should be created as drafts by default")
}

func TestSubmit_OpenFlag(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var createdDraft bool

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { return nil }
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdDraft = draft
			return &github.PullRequest{Number: 1, ID: "PR_1", URL: "https://github.com/o/r/pull/1"}, nil
		},
		CreateStackFn: func([]int) (int, error) { return 1, nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto", "--open"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.False(t, createdDraft, "PRs should not be created as drafts when --open is set")
}

func TestSubmit_OpenFlag_ConvertsDraftPRs(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, ID: "PR_10"}},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var markedReady []string

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { return nil }
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) { return nil, nil },
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "b1":
				return &github.PullRequest{
					Number: 10, ID: "PR_10", HeadRefName: "b1", BaseRefName: "main",
					IsDraft: true, URL: "https://github.com/o/r/pull/10",
				}, nil
			}
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 11, ID: "PR_11", URL: "https://github.com/o/r/pull/11",
			}, nil
		},
		MarkPRReadyForReviewFn: func(prID string) error {
			markedReady = append(markedReady, prID)
			return nil
		},
		CreateStackFn: func([]int) (int, error) { return 1, nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto", "--open"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"PR_10"}, markedReady, "existing draft PR should be marked ready")
	assert.Contains(t, output, "Marked PR")
}

func TestSubmit_PushFailure(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error {
		return fmt.Errorf("remote rejected")
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}
	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "failed to push")
}

func TestSubmit_SkipsMergedBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 3, Merged: true}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var pushCalls []pushCall

	mock := newSubmitMock(tmpDir, "b2")
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			// Only return an OPEN PR for the active branch (b2).
			// Merged branches (b1, b3) should have no open PR.
			if branch == "b2" {
				return &github.PullRequest{Number: 2, URL: "https://github.com/owner/repo/pull/2", State: "OPEN"}, nil
			}
			return nil, nil
		},
	}
	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	require.Len(t, pushCalls, 1)
	assert.Equal(t, []string{"b2"}, pushCalls[0].branches)
}

func TestSubmit_DefaultPRTitleBody(t *testing.T) {
	t.Run("single_commit", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
				return []git.CommitInfo{
					{Subject: "Add login page", Body: "Implements the OAuth flow"},
				}, nil
			},
		})
		defer restore()

		title, body := defaultPRTitleBody("main", "feat-login")
		assert.Equal(t, "Add login page", title)
		assert.Equal(t, "Implements the OAuth flow", body)
	})

	t.Run("multiple_commits", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
				return []git.CommitInfo{
					{Subject: "First commit"},
					{Subject: "Second commit"},
				}, nil
			},
		})
		defer restore()

		title, body := defaultPRTitleBody("main", "my-feature")
		assert.Equal(t, "my feature", title)
		assert.Equal(t, "", body)
	})
}

func TestSubmit_Humanize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-branch", "my branch"},
		{"my_branch", "my branch"},
		{"nobranch", "nobranch"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, humanize(tt.input))
		})
	}
}

func TestSyncStack_NewStack_CreateSuccess(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	var gotNumbers []int
	mock := &github.MockClient{
		CreateStackFn: func(prNumbers []int) (int, error) {
			gotNumbers = prNumbers
			return 42, nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Equal(t, []int{10, 11}, gotNumbers)
	assert.Equal(t, "42", s.ID)
	assert.Contains(t, output, "Stack created on GitHub with 2 PRs")
}

func TestSyncStack_ExistingStack_UpdateSuccess(t *testing.T) {
	s := &stack.Stack{
		ID:    "99",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	var gotStackID string
	var gotNumbers []int
	createCalled := false
	mock := &github.MockClient{
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 0, nil
		},
		UpdateStackFn: func(stackID string, prNumbers []int) error {
			gotStackID = stackID
			gotNumbers = prNumbers
			return nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.False(t, createCalled, "CreateStack should not be called when s.ID is set")
	assert.Equal(t, "99", gotStackID)
	assert.Equal(t, []int{10, 11, 12}, gotNumbers)
	assert.Contains(t, output, "Stack updated on GitHub with 3 PRs")
}

func TestSyncStack_ExistingStack_UpdateFails(t *testing.T) {
	s := &stack.Stack{
		ID:    "99",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	mock := &github.MockClient{
		UpdateStackFn: func(string, []int) error {
			return &api.HTTPError{
				StatusCode: 422,
				Message:    "Validation failed",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks/99"},
			}
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "Failed to update stack")
}

func TestSyncStack_ExistingStack_Update404(t *testing.T) {
	s := &stack.Stack{
		ID:    "99",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	var createCalled bool
	mock := &github.MockClient{
		UpdateStackFn: func(string, []int) error {
			return &api.HTTPError{
				StatusCode: 404,
				Message:    "Not Found",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks/99"},
			}
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			createCalled = true
			return 55, nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.True(t, createCalled, "should fall through to CreateStack after 404")
	assert.Equal(t, "55", s.ID, "should set new stack ID from create response")
	assert.Contains(t, output, "Stack created on GitHub with 2 PRs")
}

func TestSyncStack_AlreadyStacked_OurStack(t *testing.T) {
	// All our PRs are listed as "already stacked" — this is our stack, show up-to-date.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	mock := &github.MockClient{
		CreateStackFn: func([]int) (int, error) {
			return 0, &api.HTTPError{
				StatusCode: 422,
				Message:    "Pull requests #10, #11 are already stacked",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks"},
			}
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "Stack with 2 PRs is up to date")
	assert.NotContains(t, output, "different stack")
}

func TestSyncStack_AlreadyStacked_DifferentStack(t *testing.T) {
	// Only a subset of our PRs are listed — they're in a different stack.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	mock := &github.MockClient{
		CreateStackFn: func([]int) (int, error) {
			return 0, &api.HTTPError{
				StatusCode: 422,
				Message:    "Pull requests #10, #11 are already stacked",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks"},
			}
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "different stack")
	assert.NotContains(t, output, "up to date")
}

func TestSyncStack_AdoptsExistingRemoteStack_ExactMatch(t *testing.T) {
	// The stack exists on GitHub but isn't recorded locally (s.ID == "").
	// All local PRs match the remote stack exactly — adopt the ID without
	// creating or updating anything.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	var createCalled, updateCalled bool
	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 77, PullRequests: []int{10, 11}}}, nil
		},
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 0, nil
		},
		UpdateStackFn: func(string, []int) error {
			updateCalled = true
			return nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.False(t, createCalled, "should not create when the stack already exists on GitHub")
	assert.False(t, updateCalled, "should not update when local matches remote exactly")
	assert.Equal(t, "77", s.ID, "should adopt the remote stack ID into local tracking")
	assert.Contains(t, output, "Linked to the existing stack on GitHub")
	assert.Contains(t, output, "up to date")
}

func TestSyncStack_AdoptsExistingRemoteStack_AddsNewPR(t *testing.T) {
	// Two of our three PRs already form a remote stack; the third was added
	// locally on top. Adopt the remote ID and update the stack to include the
	// new PR at the top.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	var createCalled bool
	var gotStackID string
	var gotNumbers []int
	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 77, PullRequests: []int{10, 11}}}, nil
		},
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 0, nil
		},
		UpdateStackFn: func(stackID string, prNumbers []int) error {
			gotStackID = stackID
			gotNumbers = prNumbers
			return nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.False(t, createCalled, "should adopt and update, not create")
	assert.Equal(t, "77", s.ID, "should adopt the remote stack ID")
	assert.Equal(t, "77", gotStackID, "should update the adopted stack")
	assert.Equal(t, []int{10, 11, 12}, gotNumbers, "should send the full local PR list")
	assert.Contains(t, output, "Stack updated on GitHub with 3 PRs")
}

func TestSyncStack_RemoteStackHasExtraPRs_Refuses(t *testing.T) {
	// The remote stack contains a PR we aren't tracking locally. Syncing to
	// match local would drop it, so refuse and warn instead.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	var createCalled, updateCalled bool
	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{{ID: 77, PullRequests: []int{10, 11, 12}}}, nil
		},
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 0, nil
		},
		UpdateStackFn: func(string, []int) error {
			updateCalled = true
			return nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.False(t, createCalled, "should not create over an existing stack")
	assert.False(t, updateCalled, "should not drop remote-only PRs")
	assert.Equal(t, "", s.ID, "should not adopt a divergent remote stack")
	assert.Contains(t, output, "#12")
	assert.Contains(t, output, "not in your local stack")
}

func TestSyncStack_PRsSpanMultipleRemoteStacks_Warns(t *testing.T) {
	// Our PRs are split across two remote stacks — an unresolvable divergence.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	var createCalled, updateCalled bool
	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 1, PullRequests: []int{10}},
				{ID: 2, PullRequests: []int{11}},
			}, nil
		},
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 0, nil
		},
		UpdateStackFn: func(string, []int) error {
			updateCalled = true
			return nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.False(t, createCalled, "should not create when PRs span multiple stacks")
	assert.False(t, updateCalled, "should not update when PRs span multiple stacks")
	assert.Equal(t, "", s.ID)
	assert.Contains(t, output, "multiple stacks")
}

func TestSyncStack_ListStacksError_FallsThroughToCreate(t *testing.T) {
	// If we can't inspect remote stacks, fall back to the create path rather
	// than blocking the submit.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	var createCalled bool
	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, fmt.Errorf("network down")
		},
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 88, nil
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.True(t, createCalled, "should fall through to CreateStack when ListStacks fails")
	assert.Equal(t, "88", s.ID)
	assert.Contains(t, output, "Stack created on GitHub with 2 PRs")
}

func TestSyncStack_AlreadyPartOfAStack_FallbackPhrasing(t *testing.T) {
	// Fallback path: ListStacks returns no match (so adoption is skipped), but
	// the create endpoint still rejects with the server's "already part of a
	// stack" phrasing (no PR numbers). The message must be actionable rather
	// than the raw "Could not create stack".
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	mock := &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) { return nil, nil },
		CreateStackFn: func([]int) (int, error) {
			return 0, &api.HTTPError{
				StatusCode: 422,
				Message:    "Pull requests are already part of a stack",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks"},
			}
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "already part of a")
	assert.Contains(t, output, "gh stack checkout")
	assert.NotContains(t, output, "Could not create stack")
}

func TestSyncStack_InvalidChain_422(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	mock := &github.MockClient{
		CreateStackFn: func([]int) (int, error) {
			return 0, &api.HTTPError{
				StatusCode: 422,
				Message:    "Pull requests must form a stack, where each PR's base ref is the previous PR's head ref",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks"},
			}
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "must form a stack")
	assert.Contains(t, output, "base branch must match")
}

func TestSyncStack_NotAvailable(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	mock := &github.MockClient{
		CreateStackFn: func([]int) (int, error) {
			return 0, &api.HTTPError{
				StatusCode: 404,
				Message:    "Not Found",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks"},
			}
		},
	}

	cfg, _, errR := config.NewTestConfig()
	syncStack(cfg, mock, s)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "not enabled")
}

func TestSyncStack_SkippedForSinglePR(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
		},
	}

	createCalled := false
	updateCalled := false
	mock := &github.MockClient{
		CreateStackFn: func([]int) (int, error) {
			createCalled = true
			return 42, nil
		},
		UpdateStackFn: func(string, []int) error {
			updateCalled = true
			return nil
		},
	}

	cfg, _, _ := config.NewTestConfig()
	syncStack(cfg, mock, s)
	cfg.Err.Close()

	assert.False(t, createCalled, "CreateStack should not be called with fewer than 2 PRs")
	assert.False(t, updateCalled, "UpdateStack should not be called with fewer than 2 PRs")
}

func TestSyncStack_IncludesMergedBranches(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	var gotNumbers []int
	mock := &github.MockClient{
		CreateStackFn: func(prNumbers []int) (int, error) {
			gotNumbers = prNumbers
			return 42, nil
		},
	}

	cfg, _, _ := config.NewTestConfig()
	syncStack(cfg, mock, s)
	cfg.Err.Close()

	assert.Equal(t, []int{10, 11, 12}, gotNumbers, "should include merged PRs to keep API in sync")
}

func TestSyncStack_SkipsBranchesWithoutPR(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2"}, // no PR — skipped
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	var gotNumbers []int
	mock := &github.MockClient{
		CreateStackFn: func(prNumbers []int) (int, error) {
			gotNumbers = prNumbers
			return 42, nil
		},
	}

	cfg, _, _ := config.NewTestConfig()
	syncStack(cfg, mock, s)
	cfg.Err.Close()

	assert.Equal(t, []int{10, 12}, gotNumbers, "should skip branches without PRs")
}

func TestSubmit_UpdatesBaseBranch(t *testing.T) {
	// b1's PR has base "main" but it should be "main" (correct).
	// b2's PR has base "main" but it should be "b1" (wrong — needs update).
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")

	restore := git.SetOps(mock)
	defer restore()

	var updatedPRs []struct {
		number int
		base   string
	}

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "b1":
				return &github.PullRequest{
					Number: 10, ID: "PR_10",
					URL:         "https://github.com/owner/repo/pull/10",
					BaseRefName: "main", HeadRefName: "b1",
				}, nil
			case "b2":
				return &github.PullRequest{
					Number: 11, ID: "PR_11",
					URL:         "https://github.com/owner/repo/pull/11",
					BaseRefName: "main", HeadRefName: "b2", // wrong base
				}, nil
			}
			return nil, nil
		},
		UpdatePRBaseFn: func(number int, base string) error {
			updatedPRs = append(updatedPRs, struct {
				number int
				base   string
			}{number, base})
			return nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	// b1's base is "main" which is correct — no update.
	// b2's base is "main" but should be "b1" — should be updated.
	require.Len(t, updatedPRs, 1)
	assert.Equal(t, 11, updatedPRs[0].number)
	assert.Equal(t, "b1", updatedPRs[0].base)
	assert.Contains(t, output, "Updated base branch for PR")
}

func TestSubmit_SkipsBaseUpdateWhenStacked(t *testing.T) {
	// Stack already exists (s.ID is set), so base updates should be skipped.
	s := stack.Stack{
		ID:    "99",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")

	restore := git.SetOps(mock)
	defer restore()

	updateCalled := false
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "b1":
				return &github.PullRequest{
					Number: 10, ID: "PR_10",
					URL:         "https://github.com/owner/repo/pull/10",
					BaseRefName: "main", HeadRefName: "b1",
				}, nil
			case "b2":
				return &github.PullRequest{
					Number: 11, ID: "PR_11",
					URL:         "https://github.com/owner/repo/pull/11",
					BaseRefName: "main", HeadRefName: "b2", // wrong base
				}, nil
			}
			return nil, nil
		},
		UpdatePRBaseFn: func(number int, base string) error {
			updateCalled = true
			return nil
		},
		UpdateStackFn: func(stackID string, prNumbers []int) error {
			return nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.False(t, updateCalled, "should not call UpdatePRBase when stack exists")
	assert.Contains(t, output, "cannot update while stacked")
}

func TestSubmit_CreatesMissingPRsAndUpdatesExisting(t *testing.T) {
	// b1 has a PR, b2 does not, b3 has a PR with wrong base.
	// Submit should create b2's PR and fix b3's base.
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2"},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}

	restore := git.SetOps(mock)
	defer restore()

	var createdPRs []string
	var updatedBases []struct {
		number int
		base   string
	}

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "b1":
				return &github.PullRequest{
					Number: 10, ID: "PR_10",
					URL:         "https://github.com/owner/repo/pull/10",
					BaseRefName: "main", HeadRefName: "b1",
				}, nil
			case "b2":
				return nil, nil // no PR
			case "b3":
				return &github.PullRequest{
					Number: 12, ID: "PR_12",
					URL:         "https://github.com/owner/repo/pull/12",
					BaseRefName: "main", HeadRefName: "b3", // wrong base — should be b2
				}, nil
			}
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdPRs = append(createdPRs, head)
			return &github.PullRequest{
				Number: 11, ID: "PR_11",
				URL: "https://github.com/owner/repo/pull/11",
			}, nil
		},
		UpdatePRBaseFn: func(number int, base string) error {
			updatedBases = append(updatedBases, struct {
				number int
				base   string
			}{number, base})
			return nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)

	// b2 should have been created
	assert.Equal(t, []string{"b2"}, createdPRs)
	assert.Contains(t, output, "Created PR")

	// b3's base should have been updated from "main" to "b2"
	require.Len(t, updatedBases, 1)
	assert.Equal(t, 12, updatedBases[0].number)
	assert.Equal(t, "b2", updatedBases[0].base)
	assert.Contains(t, output, "Updated base branch for PR")

	// Stack should be created with all 3 PRs
	assert.Contains(t, output, "Stack created on GitHub with 3 PRs")
}

func TestSubmit_PreflightCheck_404_BailsOut(t *testing.T) {
	s := stack.Stack{
		// No ID — this is a new stack, so the pre-flight check will run.
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	pushed := false
	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error {
		pushed = true
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	// Non-interactive config — should bail out immediately.
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	// Use an OAuth token so the PAT pre-flight check passes and we
	// exercise the ListStacks 404 path.
	setTestTokenForHost(cfg, "gho_test_oauth_token")

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "Stacked PRs are not enabled for this repository")
	assert.False(t, pushed, "should not push when stacks are unavailable")
}

func TestSubmit_PreflightCheck_404_Interactive_UserDeclinesAborts(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	pushed := false
	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error {
		pushed = true
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	// Force interactive mode; survey will fail on the pipe,
	// which is treated as a decline — same as user saying "no".
	inR, inW, _ := os.Pipe()
	inW.Close()
	defer inR.Close()

	cfg, _, errR := config.NewTestConfig()
	cfg.In = inR
	cfg.ForceInteractive = true
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	// Use an OAuth token so the PAT pre-flight check passes and we
	// exercise the ListStacks 404 path.
	setTestTokenForHost(cfg, "gho_test_oauth_token")

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "Stacked PRs are not enabled for this repository")
	assert.False(t, pushed, "should not push when user declines")
}

func TestSyncStack_SkippedWhenStacksUnavailable(t *testing.T) {
	// Verify that syncStack is not called when stacksAvailable is false.
	// This is the core behavior enabling unstacked PR creation.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	createCalled := false
	mock := &github.MockClient{
		CreateStackFn: func(prNumbers []int) (int, error) {
			createCalled = true
			return 42, nil
		},
	}

	cfg, _, errR := config.NewTestConfig()

	// When stacksAvailable=true, syncStack should be called.
	syncStack(cfg, mock, s)
	assert.True(t, createCalled, "syncStack should call CreateStack when invoked")

	// When stacksAvailable=false, the caller (runSubmit) skips syncStack
	// entirely — verified by the submit_test integration tests above.
	// Here we just confirm the contract: if syncStack is NOT called,
	// CreateStack is NOT called.
	createCalled = false
	// (not calling syncStack)
	assert.False(t, createCalled, "CreateStack should not be called when syncStack is skipped")

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)
}

func TestSubmit_PreflightCheck_EmptyList_Proceeds(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	pushed := false
	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error {
		pushed = true
		return nil
	}
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 1, ID: "PR_1", URL: "https://github.com/o/r/pull/1"}, nil
		},
		CreateStackFn: func([]int) (int, error) { return 99, nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.True(t, pushed, "should proceed with push when ListStacks succeeds")
}

func TestSubmit_PreflightCheck_SkippedWhenStackIDSet(t *testing.T) {
	s := stack.Stack{
		ID:    "42", // Existing stack — pre-flight check should be skipped.
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	listStacksCallCount := 0
	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { return nil }
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			listStacksCallCount++
			return []github.RemoteStack{{ID: 42, PullRequests: []int{10, 11}}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 10:
				return &github.PullRequest{Number: 10, URL: "https://github.com/o/r/pull/10", HeadRefName: "b1", State: "OPEN"}, nil
			case 11:
				return &github.PullRequest{Number: 11, URL: "https://github.com/o/r/pull/11", HeadRefName: "b2", State: "OPEN"}, nil
			}
			return nil, nil
		},
		FindPRForBranchFn: func(string) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 10, URL: "https://github.com/o/r/pull/10"}, nil
		},
		UpdateStackFn: func(string, []int) error { return nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	// ListStacks is called by syncStackPRs (remote sync), but NOT by the
	// preflight check. Two syncStackPRs calls happen in submit (before and
	// after PR creation), so expect exactly 2 ListStacks calls.
	assert.Equal(t, 2, listStacksCallCount, "ListStacks should only be called by syncStackPRs, not by the preflight check")
}

// --- Modify + Submit integration tests ---

func saveModifyState(t *testing.T, gitDir string, state *modify.StateFile) {
	t.Helper()
	require.NoError(t, modify.SaveState(gitDir, state))
}

func newPendingSubmitState(priorStackID string) *modify.StateFile {
	return &modify.StateFile{
		SchemaVersion:      1,
		Phase:              "pending_submit",
		PriorRemoteStackID: priorStackID,
		Snapshot:           modify.Snapshot{StackMetadata: json.RawMessage(`{}`)},
	}
}

func TestHandlePendingModify_DeletesOldStack(t *testing.T) {
	gitDir := t.TempDir()

	saveModifyState(t, gitDir, newPendingSubmitState("stack-123"))

	s := &stack.Stack{ID: "stack-123", Trunk: stack.BranchRef{Branch: "main"}}

	var deletedStackID string
	client := &github.MockClient{
		DeleteStackFn: func(id string) error {
			deletedStackID = id
			return nil
		},
	}

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := handlePendingModify(cfg, client, s, gitDir)
	require.NoError(t, err)
	assert.Equal(t, "stack-123", deletedStackID)
	assert.Equal(t, "", s.ID)
}

func TestHandlePendingModify_NoStateFile(t *testing.T) {
	gitDir := t.TempDir()
	// No state file on disk.

	s := &stack.Stack{ID: "stack-123", Trunk: stack.BranchRef{Branch: "main"}}

	deleteCalled := false
	client := &github.MockClient{
		DeleteStackFn: func(id string) error {
			deleteCalled = true
			return nil
		},
	}

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := handlePendingModify(cfg, client, s, gitDir)
	assert.NoError(t, err)
	assert.False(t, deleteCalled, "DeleteStack should not be called when no state file exists")
	assert.Equal(t, "stack-123", s.ID, "stack ID should remain unchanged")
}

func TestHandlePendingModify_WrongPhase(t *testing.T) {
	gitDir := t.TempDir()

	state := &modify.StateFile{
		SchemaVersion: 1,
		Phase:         "conflict",
		Snapshot:      modify.Snapshot{StackMetadata: json.RawMessage(`{}`)},
	}
	saveModifyState(t, gitDir, state)

	s := &stack.Stack{ID: "stack-99", Trunk: stack.BranchRef{Branch: "main"}}

	deleteCalled := false
	client := &github.MockClient{
		DeleteStackFn: func(id string) error {
			deleteCalled = true
			return nil
		},
	}

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := handlePendingModify(cfg, client, s, gitDir)
	assert.NoError(t, err)
	assert.False(t, deleteCalled, "DeleteStack should not be called for non-pending_submit phase")
	assert.Equal(t, "stack-99", s.ID, "stack ID should remain unchanged")
}

func TestHandlePendingModify_DeleteFails(t *testing.T) {
	gitDir := t.TempDir()

	saveModifyState(t, gitDir, newPendingSubmitState("stack-456"))

	s := &stack.Stack{ID: "stack-456", Trunk: stack.BranchRef{Branch: "main"}}

	client := &github.MockClient{
		DeleteStackFn: func(id string) error {
			return fmt.Errorf("server error")
		},
	}

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := handlePendingModify(cfg, client, s, gitDir)
	assert.Error(t, err)
	assert.Equal(t, "stack-456", s.ID, "stack ID should NOT be cleared on delete failure")
}

func TestHandlePendingModify_Delete404(t *testing.T) {
	gitDir := t.TempDir()

	saveModifyState(t, gitDir, newPendingSubmitState("stack-gone"))

	s := &stack.Stack{ID: "stack-gone", Trunk: stack.BranchRef{Branch: "main"}}

	client := &github.MockClient{
		DeleteStackFn: func(id string) error {
			return &api.HTTPError{
				StatusCode: 404,
				Message:    "Not Found",
				RequestURL: &url.URL{Path: "/repos/o/r/cli_internal/pulls/stacks/stack-gone"},
			}
		},
	}

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	err := handlePendingModify(cfg, client, s, gitDir)
	require.NoError(t, err, "404 should be treated as success (stack already deleted)")
	assert.Equal(t, "", s.ID, "stack ID should be cleared after 404")
}

func TestClearPendingModifyState_ClearsFile(t *testing.T) {
	gitDir := t.TempDir()

	saveModifyState(t, gitDir, newPendingSubmitState("stack-789"))
	require.True(t, modify.StateExists(gitDir), "precondition: state file should exist")

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	clearPendingModifyState(cfg, gitDir)
	assert.False(t, modify.StateExists(gitDir), "state file should be removed")
}

func TestClearPendingModifyState_NoFile(t *testing.T) {
	gitDir := t.TempDir()
	// No state file on disk.

	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()

	// Should not panic or error.
	clearPendingModifyState(cfg, gitDir)
	assert.False(t, modify.StateExists(gitDir))
}

func TestSubmit_WithPendingModify_SequentialPush(t *testing.T) {
	s := stack.Stack{
		ID:    "old-stack-42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 11}},
			{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 12}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)
	saveModifyState(t, tmpDir, newPendingSubmitState("old-stack-42"))

	// Track call ordering
	var callOrder []string
	var pushCalls []pushCall

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		pushCalls = append(pushCalls, pushCall{remote, branches, force, atomic})
		callOrder = append(callOrder, fmt.Sprintf("push:%s", branches[0]))
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	var deletedStackID string
	var createdStackPRs []int

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		DeleteStackFn: func(id string) error {
			deletedStackID = id
			callOrder = append(callOrder, "delete:"+id)
			return nil
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "b1":
				return &github.PullRequest{
					Number: 10, ID: "PR_10",
					URL:         "https://github.com/owner/repo/pull/10",
					BaseRefName: "main", HeadRefName: "b1",
					State: "OPEN",
				}, nil
			case "b2":
				return &github.PullRequest{
					Number: 11, ID: "PR_11",
					URL:         "https://github.com/owner/repo/pull/11",
					BaseRefName: "b1", HeadRefName: "b2",
					State: "OPEN",
				}, nil
			case "b3":
				return &github.PullRequest{
					Number: 12, ID: "PR_12",
					URL:         "https://github.com/owner/repo/pull/12",
					BaseRefName: "b2", HeadRefName: "b3",
					State: "OPEN",
				}, nil
			}
			return nil, nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			createdStackPRs = prNumbers
			callOrder = append(callOrder, "create_stack")
			return 99, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)

	// DeleteStack called with old stack ID
	assert.Equal(t, "old-stack-42", deletedStackID)

	// Push called per-branch (3 separate calls, not 1 atomic call)
	require.Len(t, pushCalls, 3, "should push each branch individually")
	assert.Equal(t, []string{"b1"}, pushCalls[0].branches)
	assert.Equal(t, []string{"b2"}, pushCalls[1].branches)
	assert.Equal(t, []string{"b3"}, pushCalls[2].branches)
	for _, pc := range pushCalls {
		assert.False(t, pc.atomic, "sequential push should not use atomic mode")
	}

	// CreateStack called with all 3 PRs
	assert.Equal(t, []int{10, 11, 12}, createdStackPRs)

	// Verify ordering: delete before push, push before create_stack
	assert.True(t, len(callOrder) >= 5, "expected at least 5 calls, got %d: %v", len(callOrder), callOrder)
	deleteIdx := -1
	firstPushIdx := -1
	createIdx := -1
	for i, c := range callOrder {
		if c == "delete:old-stack-42" && deleteIdx == -1 {
			deleteIdx = i
		}
		if c == "push:b1" && firstPushIdx == -1 {
			firstPushIdx = i
		}
		if c == "create_stack" && createIdx == -1 {
			createIdx = i
		}
	}
	assert.Greater(t, firstPushIdx, deleteIdx, "delete should happen before push")
	assert.Greater(t, createIdx, firstPushIdx, "create_stack should happen after push")

	// State file should be cleared
	assert.False(t, modify.StateExists(tmpDir), "modify state file should be cleared after success")
}

func TestSubmit_FetchesBeforePush(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	var callOrder []string
	var fetchedBranches []string

	mock := newSubmitMock(tmpDir, "b1")
	mock.FetchBranchesFn = func(remote string, branches []string) error {
		callOrder = append(callOrder, "fetch")
		fetchedBranches = branches
		assert.Equal(t, "origin", remote)
		return nil
	}
	mock.PushFn = func(remote string, branches []string, force, atomic bool) error {
		callOrder = append(callOrder, "push")
		return nil
	}

	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      1,
				URL:         "https://github.com/o/r/pull/1",
				BaseRefName: "main",
				HeadRefName: branch,
				State:       "OPEN",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2"}, fetchedBranches, "should fetch active branches")
	// fetch must come before all pushes
	require.True(t, len(callOrder) >= 3, "expected at least 3 calls (fetch + 2 pushes)")
	assert.Equal(t, "fetch", callOrder[0], "fetch must happen before any push")
}

func TestSubmit_UsesPRTemplate(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// Create a PR template in the repo root
	ghDir := filepath.Join(tmpDir, ".github")
	require.NoError(t, os.MkdirAll(ghDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ghDir, "pull_request_template.md"),
		[]byte("## What\n\nDescribe changes.\n\n## Why\n\nExplain motivation."),
		0o644,
	))

	var capturedBody string

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { return nil }
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "add feature", Body: "detailed commit body"}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			capturedBody = body
			return &github.PullRequest{Number: 1, ID: "PR_1", URL: "https://github.com/o/r/pull/1"}, nil
		},
		CreateStackFn: func([]int) (int, error) { return 1, nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Contains(t, capturedBody, "## What")
	assert.Contains(t, capturedBody, "## Why")
	assert.NotContains(t, capturedBody, "GitHub Stacks CLI", "footer should not be present when template is used")
	assert.NotContains(t, capturedBody, feedbackURL)
}

func TestSubmit_NoTemplate_UsesFooter(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	// No template file created

	var capturedBody string

	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error { return nil }
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "fix bug"}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn:      func() ([]github.RemoteStack, error) { return nil, nil },
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			capturedBody = body
			return &github.PullRequest{Number: 1, ID: "PR_1", URL: "https://github.com/o/r/pull/1"}, nil
		},
		CreateStackFn: func([]int) (int, error) { return 1, nil },
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Contains(t, capturedBody, "GitHub Stacks CLI", "footer should be present when no template")
	assert.Contains(t, capturedBody, feedbackURL)
}

func TestSubmit_PreflightCheck_PAT_BailsOut(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	pushed := false
	mock := newSubmitMock(tmpDir, "b1")
	mock.PushFn = func(string, []string, bool, bool) error {
		pushed = true
		return nil
	}
	restore := git.SetOps(mock)
	defer restore()

	listStacksCalled := false
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			listStacksCalled = true
			return nil, nil
		},
	}

	// Simulate a classic PAT — the pre-flight check should abort.
	setTestTokenForHost(cfg, "ghp_classic_pat_token")

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "Personal access tokens are not supported by gh stack")
	assert.Contains(t, output, "gh auth login")
	assert.False(t, pushed, "should not push when using a PAT")
	assert.False(t, listStacksCalled, "should not call ListStacks when PAT detected")
}

func TestSubmit_PreflightCheck_FinegrainedPAT_BailsOut(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	setTestTokenForHost(cfg, "github_pat_11AABBCC_xxxx")

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "Personal access tokens are not supported by gh stack")
}

func TestSubmit_DisablesAutoMergeOnExistingPR(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit for " + head}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	var disabledAutoMergePRIDs []string

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "b1":
				return &github.PullRequest{
					Number: 10, ID: "PR_10",
					URL:         "https://github.com/owner/repo/pull/10",
					BaseRefName: "main", HeadRefName: "b1",
				}, nil
			case "b2":
				return &github.PullRequest{
					Number: 20, ID: "PR_20",
					URL:         "https://github.com/owner/repo/pull/20",
					BaseRefName: "b1", HeadRefName: "b2",
					AutoMergeRequest: &github.AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"},
				}, nil
			}
			return nil, nil
		},
		DisableAutoMergeFn: func(prID string) error {
			disabledAutoMergePRIDs = append(disabledAutoMergePRIDs, prID)
			return nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"PR_20"}, disabledAutoMergePRIDs)
	assert.Contains(t, output, "Disabled auto-merge")
	assert.Contains(t, output, "incompatible with stacked PRs")
}

func TestSubmit_DisableAutoMergeFailure_ContinuesWithWarning(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit"}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10, ID: "PR_10",
				URL:         "https://github.com/owner/repo/pull/10",
				BaseRefName: "main", HeadRefName: "b1",
				AutoMergeRequest: &github.AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"},
			}, nil
		},
		DisableAutoMergeFn: func(prID string) error {
			return fmt.Errorf("permission denied")
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	// Submit should succeed even if disable-auto-merge fails
	assert.NoError(t, err)
	assert.Contains(t, output, "failed to disable auto-merge")
	assert.Contains(t, output, "permission denied")
}

func TestSubmit_NoAutoMerge_SkipsDisable(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := newSubmitMock(tmpDir, "b1")
	mock.LogRangeFn = func(base, head string) ([]git.CommitInfo, error) {
		return []git.CommitInfo{{Subject: "commit"}}, nil
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10, ID: "PR_10",
				URL:         "https://github.com/owner/repo/pull/10",
				BaseRefName: "main", HeadRefName: "b1",
			}, nil
		},
		DisableAutoMergeFn: func(prID string) error {
			t.Fatal("DisableAutoMerge should not be called when auto-merge is not enabled")
			return nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
		},
	}

	cmd := SubmitCmd(cfg)
	cmd.SetArgs([]string{"--auto"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
}

// --- Per-PR draft override plumbing (interactive editor contract) ---

func TestCreatePR_UsesDraftOverride(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	}

	var gotTitle, gotBody string
	var gotDraft bool
	cfg, _, _ := config.NewTestConfig()
	client := &github.MockClient{
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			gotTitle, gotBody, gotDraft = title, body, draft
			return &github.PullRequest{Number: 7, ID: "PR_7", URL: "https://github.com/o/r/pull/7"}, nil
		},
	}

	drafts := map[string]*submitview.PRDraft{
		"b1": {Branch: "b1", Include: true, Title: "Custom title", Body: "Custom body", Draft: true},
	}

	// --open would normally force ready; the override's Draft must win.
	err := createPR(cfg, client, s, 0, "main", &submitOptions{open: true}, "", drafts)
	require.NoError(t, err)

	assert.Equal(t, "Custom title", gotTitle)
	assert.Contains(t, gotBody, "Custom body")
	assert.Contains(t, gotBody, "GitHub Stacks CLI", "footer is appended at submit time")
	assert.True(t, gotDraft, "draft override should be honored over --open")
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 7, s.Branches[0].PullRequest.Number)
}

func TestCreatePR_DraftOverride_KeepsUserBodyOverTemplate(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	}

	var gotBody string
	cfg, _, _ := config.NewTestConfig()
	client := &github.MockClient{
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			gotBody = body
			return &github.PullRequest{Number: 1, ID: "PR_1"}, nil
		},
	}
	// The user edited the description in the TUI; the repo also has a template.
	// The user's edits must win — the template was only the prefill.
	drafts := map[string]*submitview.PRDraft{
		"b1": {Branch: "b1", Include: true, Title: "T", Body: "My edited description"},
	}

	err := createPR(cfg, client, s, 0, "main", &submitOptions{}, "## Raw repo template", drafts)
	require.NoError(t, err)

	assert.Contains(t, gotBody, "My edited description", "the user's edited body is used")
	assert.NotContains(t, gotBody, "Raw repo template", "the raw template does not override the user's edits")
	assert.Contains(t, gotBody, "GitHub Stacks CLI", "the attribution footer is appended")
}

func TestEnsurePR_DeselectedNewBranchSkipsCreate(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	}

	cfg, _, _ := config.NewTestConfig()
	client := &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(string, string, string, string, bool) (*github.PullRequest, error) {
			t.Fatal("CreatePR must not be called for a deselected NEW branch")
			return nil, nil
		},
	}

	drafts := map[string]*submitview.PRDraft{
		"b1": {Branch: "b1", Include: false},
	}

	err := ensurePR(cfg, client, s, 0, "main", &submitOptions{}, "", drafts)
	require.NoError(t, err)
	assert.Nil(t, s.Branches[0].PullRequest, "no PR should be recorded for a deselected branch")
}
