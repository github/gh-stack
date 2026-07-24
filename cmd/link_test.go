package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLinkGitMock creates a MockOps for link tests that involve branch args.
// BranchExists returns true for the given branches, Push is a no-op,
// and ResolveRemote returns "origin".
func newLinkGitMock(branches ...string) *git.MockOps {
	branchSet := make(map[string]bool, len(branches))
	for _, b := range branches {
		branchSet[b] = true
	}
	return &git.MockOps{
		BranchExistsFn:  func(name string) bool { return branchSet[name] },
		PushFn:          func(string, []string, bool, bool) error { return nil },
		ResolveRemoteFn: func(string) (string, error) { return "origin", nil },
	}
}

// --- PR-number tests ---

func TestLink_PRNumbers_CreateNewStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var createdPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			createdPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{10, 20, 30}, createdPRs)
	assert.Contains(t, output, "Created stack with 3 PRs")
}

func TestLink_PRNumbers_UpdateExistingStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var updatedNumber int
	var updatedPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{10, 20}},
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			updatedNumber = stackNumber
			updatedPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: stackNumber, PullRequests: []int{10, 20, 30}}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, 7, updatedNumber)
	assert.Equal(t, []int{30}, updatedPRs)
	assert.Contains(t, output, "Updated stack to 3 PRs")
}

func TestLink_PRNumbers_ExactMatch_NoOp(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{10, 20, 30}},
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called for exact match")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "already up to date")
}

func TestLink_PRNumbers_WouldRemovePRs(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{10, 20, 30}},
			}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "would remove")
	assert.Contains(t, output, "#10")
}

func TestLink_PRNumbers_MultipleStacks(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 1, Number: 1, PullRequests: []int{10, 20}},
				{ID: 2, Number: 2, PullRequests: []int{30, 40}},
			}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrDisambiguate)
	assert.Contains(t, output, "multiple stacks")
}

func TestLink_TooFewArgs(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	// cobra enforces MinimumNArgs(2) before RunE is called
	assert.Error(t, err)
}

func TestLink_DuplicateArgs(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feature-a", "feature-a"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "duplicate argument")
}

func TestLink_StacksUnavailable(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	setTestRepo(cfg)
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: "b", BaseRefName: "main"}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "not enabled")
}

func TestLink_Create422(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: "b", BaseRefName: "main"}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 0, Number: 0}, &api.HTTPError{
				StatusCode: 422,
				Message:    "Pull requests must form a stack",
			}
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "must form a stack")
}

// --- PR eligibility tests ---

func TestLink_RejectsMergedPR(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				State:       "MERGED",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
				Merged:      true,
			}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "merged")
}

func TestLink_RejectsClosedPR(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				State:       "CLOSED",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "closed")
}

func TestLink_RejectsQueuedPR(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			if n == 20 {
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_123"}
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			t.Fatal("CreateStack should not be called for ineligible PRs")
			return &github.RemoteStack{ID: 0, Number: 0}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "queued for merge")
}

func TestLink_RejectsAutoMergeEnabledPR(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			if n == 10 {
				pr.AutoMergeRequest = &github.AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"}
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			t.Fatal("CreateStack should not be called for ineligible PRs")
			return &github.RemoteStack{ID: 0, Number: 0}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "auto-merge")
}

func TestLink_RejectsQueuedPR_ByBranch(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feature-a", "feature-b"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      10,
				HeadRefName: branch,
				BaseRefName: "main",
				URL:         "https://github.com/o/r/pull/10",
			}
			if branch == "feature-b" {
				pr.Number = 20
				pr.URL = "https://github.com/o/r/pull/20"
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_456"}
			}
			return pr, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feature-a", "feature-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "queued for merge")
}

func TestLink_RejectsAutoMergePR_ByBranch(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feature-a", "feature-b"))
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if branch == "feature-a" {
				return &github.PullRequest{
					Number:           10,
					HeadRefName:      branch,
					BaseRefName:      "main",
					URL:              "https://github.com/o/r/pull/10",
					AutoMergeRequest: &github.AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"},
				}, nil
			}
			return &github.PullRequest{
				Number:      20,
				HeadRefName: branch,
				BaseRefName: "main",
				URL:         "https://github.com/o/r/pull/20",
			}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feature-a", "feature-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "auto-merge")
}

func TestLink_ReportsMultipleIneligiblePRs(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			switch n {
			case 10:
				pr.State = "MERGED"
				pr.Merged = true
			case 20:
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_789"}
			case 30:
				pr.AutoMergeRequest = &github.AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"}
			}
			return pr, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	// All three invalid PRs should be reported
	assert.Contains(t, output, "merged")
	assert.Contains(t, output, "queued for merge")
	assert.Contains(t, output, "auto-merge")
}

// Regression test to ensure a queued PR that is already a member of
// the target stack does not block adding new PRs to that same stack.
func TestLink_AllowsQueuedPRAlreadyInStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var updatedNumber int
	var updatedPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			// PR 100 is the queued bottom PR already in the stack.
			if n == 100 {
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_100"}
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{100}},
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			updatedNumber = stackNumber
			updatedPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: stackNumber, PullRequests: []int{100, 101, 102}}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			t.Fatal("CreateStack should not be called when updating an existing stack")
			return &github.RemoteStack{ID: 0, Number: 0}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"100", "101", "102"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	require.NoError(t, err)
	assert.Equal(t, 7, updatedNumber)
	assert.Equal(t, []int{101, 102}, updatedPRs)
	assert.NotContains(t, output, "cannot be added to a stack")
}

// TestLink_AllowsMergedPRAlreadyInStack verifies the exemption also covers
// state-based ineligibility (merged/closed) for PRs already in the stack.
func TestLink_AllowsMergedPRAlreadyInStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var updatedPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			if n == 100 {
				pr.State = "MERGED"
				pr.Merged = true
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 8, Number: 8, PullRequests: []int{100}},
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			updatedPRs = prNumbers
			return &github.RemoteStack{ID: 8, Number: stackNumber, PullRequests: []int{100, 101}}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"100", "101"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	require.NoError(t, err)
	assert.Equal(t, []int{101}, updatedPRs)
	assert.NotContains(t, output, "cannot be added to a stack")
}

// TestLink_AllowsAutoMergePRAlreadyInStack verifies the exemption also covers
// auto-merge-enabled PRs already in the stack.
func TestLink_AllowsAutoMergePRAlreadyInStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var updatedPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			if n == 100 {
				pr.AutoMergeRequest = &github.AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"}
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 9, Number: 9, PullRequests: []int{100}},
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			updatedPRs = prNumbers
			return &github.RemoteStack{ID: 9, Number: stackNumber, PullRequests: []int{100, 101}}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"100", "101"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	require.NoError(t, err)
	assert.Equal(t, []int{101}, updatedPRs)
	assert.NotContains(t, output, "cannot be added to a stack")
}

// TestLink_RejectsQueuedPRNotInStack_WhenAddingToExistingStack confirms the
// exemption is scoped correctly: a queued PR that is NOT already a member of the
// matched stack is still rejected, even when the command targets that stack.
func TestLink_RejectsQueuedPRNotInStack_WhenAddingToExistingStack(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number:      n,
				State:       "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			// PR 200 is queued and is NOT part of the existing stack.
			if n == 200 {
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_200"}
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{100}},
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called when a new PR is ineligible")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"100", "200"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "queued for merge")
}

// --- Branch name tests ---

func TestLink_BranchNames_AllHavePRs(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feature-a", "feature-b"))
	defer restore()

	var stackedPRs []int
	prMap := map[string]*github.PullRequest{
		"feature-a": {Number: 10, HeadRefName: "feature-a", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
		"feature-b": {Number: 20, HeadRefName: "feature-b", BaseRefName: "feature-a", URL: "https://github.com/o/r/pull/20"},
	}

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return prMap[branch], nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			for _, pr := range prMap {
				if pr.Number == n {
					return pr, nil
				}
			}
			return nil, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			stackedPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feature-a", "feature-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{10, 20}, stackedPRs)
	assert.Contains(t, output, "Created stack with 2 PRs")
}

func TestLink_BranchNames_CreatesMissingPRs(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feature-a", "feature-b"))
	defer restore()

	var createdPRs []struct{ base, head string }
	var stackedPRs []int

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if branch == "feature-a" {
				return &github.PullRequest{
					Number: 10, HeadRefName: "feature-a", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/10",
				}, nil
			}
			return nil, nil // feature-b has no PR
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			if n == 10 {
				return &github.PullRequest{
					Number: 10, HeadRefName: "feature-a", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/10",
				}, nil
			}
			if n == 20 {
				return &github.PullRequest{
					Number: 20, HeadRefName: "feature-b", BaseRefName: "feature-a",
					URL: "https://github.com/o/r/pull/20",
				}, nil
			}
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdPRs = append(createdPRs, struct{ base, head string }{base, head})
			return &github.PullRequest{
				Number: 20, HeadRefName: head, BaseRefName: base,
				URL: "https://github.com/o/r/pull/20",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			stackedPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feature-a", "feature-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	require.Len(t, createdPRs, 1)
	assert.Equal(t, "feature-a", createdPRs[0].base) // base should chain to previous branch
	assert.Equal(t, "feature-b", createdPRs[0].head)
	assert.Equal(t, []int{10, 20}, stackedPRs)
	assert.Contains(t, output, "Created PR")
	assert.Contains(t, output, "Created stack with 2 PRs")
}

func TestLink_BranchNames_AllNeedPRs(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b", "feat-c"))
	defer restore()

	prCounter := 0
	var createdPRs []struct{ base, head string }

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil // no open PRs for any branch
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			bases := map[int]string{1: "main", 2: "feat-a", 3: "feat-b"}
			heads := map[int]string{1: "feat-a", 2: "feat-b", 3: "feat-c"}
			if h, ok := heads[n]; ok {
				return &github.PullRequest{
					Number: n, HeadRefName: h, BaseRefName: bases[n],
				}, nil
			}
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			prCounter++
			createdPRs = append(createdPRs, struct{ base, head string }{base, head})
			return &github.PullRequest{
				Number:      prCounter,
				HeadRefName: head,
				BaseRefName: base,
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", prCounter),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--base", "develop", "feat-a", "feat-b", "feat-c"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	require.Len(t, createdPRs, 3)
	// First PR base should be the --base flag value
	assert.Equal(t, "develop", createdPRs[0].base)
	assert.Equal(t, "feat-a", createdPRs[0].head)
	// Second PR base should be previous branch
	assert.Equal(t, "feat-a", createdPRs[1].base)
	assert.Equal(t, "feat-b", createdPRs[1].head)
	// Third PR base should be previous branch
	assert.Equal(t, "feat-b", createdPRs[2].base)
	assert.Equal(t, "feat-c", createdPRs[2].head)
	assert.Contains(t, output, "Created stack with 3 PRs")
}

func TestLink_BranchNames_DefaultDraft(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b"))
	defer restore()

	var createdDraft bool
	prCounter := 0

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			heads := map[int]string{1: "feat-a", 2: "feat-b"}
			bases := map[int]string{1: "main", 2: "feat-a"}
			if h, ok := heads[n]; ok {
				return &github.PullRequest{Number: n, HeadRefName: h, BaseRefName: bases[n]}, nil
			}
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdDraft = draft
			prCounter++
			return &github.PullRequest{
				Number: prCounter, HeadRefName: head, BaseRefName: base,
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", prCounter),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.True(t, createdDraft, "PRs should be created as drafts by default")
}

func TestLink_BranchNames_OpenFlag(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b"))
	defer restore()

	var createdDraft bool
	prCounter := 0

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			heads := map[int]string{1: "feat-a", 2: "feat-b"}
			bases := map[int]string{1: "main", 2: "feat-a"}
			if h, ok := heads[n]; ok {
				return &github.PullRequest{Number: n, HeadRefName: h, BaseRefName: bases[n]}, nil
			}
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdDraft = draft
			prCounter++
			return &github.PullRequest{
				Number: prCounter, HeadRefName: head, BaseRefName: base,
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", prCounter),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--open", "feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.False(t, createdDraft, "PRs should not be created as drafts when --open is set")
}

func TestLink_OpenFlag_ConvertsDraftPRs(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b"))
	defer restore()

	var markedReady []string

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "feat-a":
				return &github.PullRequest{
					Number: 1, ID: "PR_1", HeadRefName: "feat-a", BaseRefName: "main",
					IsDraft: true, URL: "https://github.com/o/r/pull/1",
				}, nil
			case "feat-b":
				return &github.PullRequest{
					Number: 2, ID: "PR_2", HeadRefName: "feat-b", BaseRefName: "feat-a",
					IsDraft: true, URL: "https://github.com/o/r/pull/2",
				}, nil
			}
			return nil, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			switch n {
			case 1:
				return &github.PullRequest{
					Number: 1, ID: "PR_1", HeadRefName: "feat-a", BaseRefName: "main",
					IsDraft: true, URL: "https://github.com/o/r/pull/1",
				}, nil
			case 2:
				return &github.PullRequest{
					Number: 2, ID: "PR_2", HeadRefName: "feat-b", BaseRefName: "feat-a",
					IsDraft: true, URL: "https://github.com/o/r/pull/2",
				}, nil
			}
			return nil, nil
		},
		MarkPRReadyForReviewFn: func(prID string) error {
			markedReady = append(markedReady, prID)
			return nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--open", "feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"PR_1", "PR_2"}, markedReady, "both draft PRs should be marked ready")
	assert.Contains(t, output, "Marked PR")
}

func TestLink_MixedArgs_PRNumberAndBranch(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("new-feature"))
	defer restore()

	var stackedPRs []int

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			if n == 42 {
				return &github.PullRequest{
					Number: 42, HeadRefName: "existing-branch", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/42",
				}, nil
			}
			return nil, nil
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if branch == "new-feature" {
				return &github.PullRequest{
					Number: 99, HeadRefName: "new-feature", BaseRefName: "existing-branch",
					URL: "https://github.com/o/r/pull/99",
				}, nil
			}
			return nil, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			stackedPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"42", "new-feature"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{42, 99}, stackedPRs)
	assert.Contains(t, output, "Created stack with 2 PRs")
}

func TestLink_NumericArg_PRNotFound_TreatedAsBranch(t *testing.T) {
	// Numeric branches "123" and "456" exist locally
	restore := git.SetOps(newLinkGitMock("123", "456"))
	defer restore()

	var stackedPRs []int

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return nil, nil // PR not found
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			// Treat "123" as a branch name
			if branch == "123" {
				return &github.PullRequest{
					Number: 50, HeadRefName: "123", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/50",
				}, nil
			}
			if branch == "456" {
				return &github.PullRequest{
					Number: 51, HeadRefName: "456", BaseRefName: "123",
					URL: "https://github.com/o/r/pull/51",
				}, nil
			}
			return nil, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			stackedPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"123", "456"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []int{50, 51}, stackedPRs)
}

func TestLink_NumericFirstArgIsLocalBranch_NotAddMode(t *testing.T) {
	// Branch "123" exists locally and stack #123 also exists. The numeric
	// branch name must win over add mode, so the args form a new stack rather
	// than appending #456 to the unrelated stack #123.
	restore := git.SetOps(newLinkGitMock("123", "456"))
	defer restore()

	var createdPRs []int
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(int) (*github.PullRequest, error) { return nil, nil },
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "123":
				return &github.PullRequest{Number: 50, HeadRefName: "123", BaseRefName: "main", URL: "https://github.com/o/r/pull/50"}, nil
			case "456":
				return &github.PullRequest{Number: 51, HeadRefName: "456", BaseRefName: "123", URL: "https://github.com/o/r/pull/51"}, nil
			}
			return nil, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(123, linkPR(90, "unrelated-a"), linkPR(91, "unrelated-b")),
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack must not be called: a numeric branch name should not trigger add mode")
			return nil, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			createdPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"123", "456"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []int{50, 51}, createdPRs)
}

func TestLink_FixesBaseBranches(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b"))
	defer restore()

	var baseUpdates []struct {
		number int
		base   string
	}

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			switch branch {
			case "feat-a":
				return &github.PullRequest{
					Number: 10, HeadRefName: "feat-a", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/10",
				}, nil
			case "feat-b":
				// This PR has the wrong base — should be feat-a, not main
				return &github.PullRequest{
					Number: 20, HeadRefName: "feat-b", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/20",
				}, nil
			}
			return nil, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			switch n {
			case 10:
				return &github.PullRequest{
					Number: 10, HeadRefName: "feat-a", BaseRefName: "main",
				}, nil
			case 20:
				return &github.PullRequest{
					Number: 20, HeadRefName: "feat-b", BaseRefName: "main",
				}, nil
			}
			return nil, nil
		},
		UpdatePRBaseFn: func(number int, base string) error {
			baseUpdates = append(baseUpdates, struct {
				number int
				base   string
			}{number, base})
			return nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 42, Number: 42}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	// PR #20's base should be updated from "main" to "feat-a"
	require.Len(t, baseUpdates, 1)
	assert.Equal(t, 20, baseUpdates[0].number)
	assert.Equal(t, "feat-a", baseUpdates[0].base)
	assert.Contains(t, output, "Updated base branch")
}

// TestLink_DefaultBase_RetargetsBottomPRToDefaultBranch is a regression test
// for #260: when --base is omitted, the bottom of the stack must be based on
// the repository's default branch (resolved via git.DefaultBranch), not a
// hardcoded "main". Here the default branch is "develop", so the bottom PR —
// which currently targets "main" — should be retargeted to "develop".
func TestLink_DefaultBase_RetargetsBottomPRToDefaultBranch(t *testing.T) {
	defaultBranchCalled := false
	restore := git.SetOps(&git.MockOps{
		BranchExistsFn: func(string) bool { return false },
		DefaultBranchFn: func() (string, error) {
			defaultBranchCalled = true
			return "develop", nil
		},
	})
	defer restore()

	var baseUpdates []struct {
		number int
		base   string
	}

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			switch n {
			case 10:
				// Bottom PR currently targets "main"; it should be retargeted
				// to the repository default branch ("develop").
				return &github.PullRequest{
					Number: 10, HeadRefName: "feat-a", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/10",
				}, nil
			case 20:
				return &github.PullRequest{
					Number: 20, HeadRefName: "feat-b", BaseRefName: "feat-a",
					URL: "https://github.com/o/r/pull/20",
				}, nil
			}
			return nil, nil
		},
		UpdatePRBaseFn: func(number int, base string) error {
			baseUpdates = append(baseUpdates, struct {
				number int
				base   string
			}{number, base})
			return nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.True(t, defaultBranchCalled, "git.DefaultBranch should be consulted when --base is omitted")
	// The bottom PR (#10) should be retargeted to the default branch, not "main".
	require.Len(t, baseUpdates, 1)
	assert.Equal(t, 10, baseUpdates[0].number)
	assert.Equal(t, "develop", baseUpdates[0].base)
}

// TestLink_DefaultBase_CreatesBottomPROnDefaultBranch verifies that a newly
// created bottom PR is based on the repository default branch when --base is
// omitted, rather than a hardcoded "main".
func TestLink_DefaultBase_CreatesBottomPROnDefaultBranch(t *testing.T) {
	restore := git.SetOps(&git.MockOps{
		BranchExistsFn:  func(name string) bool { return name == "feat-a" || name == "feat-b" },
		PushFn:          func(string, []string, bool, bool) error { return nil },
		ResolveRemoteFn: func(string) (string, error) { return "origin", nil },
		DefaultBranchFn: func() (string, error) { return "develop", nil },
	})
	defer restore()

	var createdPRs []struct{ base, head string }
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			createdPRs = append(createdPRs, struct{ base, head string }{base, head})
			n := len(createdPRs)
			return &github.PullRequest{
				Number: n, HeadRefName: head, BaseRefName: base,
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	require.Len(t, createdPRs, 2)
	// Bottom PR based on the repository default branch, not "main".
	assert.Equal(t, "develop", createdPRs[0].base)
	// Second PR chains off the previous branch.
	assert.Equal(t, "feat-a", createdPRs[1].base)
}

// TestLink_DefaultBase_ErrorWhenUnresolvable verifies that link fails with a
// helpful message when --base is omitted and the default branch cannot be
// determined.
func TestLink_DefaultBase_ErrorWhenUnresolvable(t *testing.T) {
	restore := git.SetOps(&git.MockOps{
		BranchExistsFn:  func(string) bool { return false },
		DefaultBranchFn: func() (string, error) { return "", fmt.Errorf("no default branch") },
	})
	defer restore()

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, HeadRefName: fmt.Sprintf("b%d", n), BaseRefName: "main",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "unable to determine default branch")
}

// TestLink_ExplicitBase_SkipsDefaultBranchResolution verifies that passing
// --base bypasses default branch resolution and is used as the bottom base.
func TestLink_ExplicitBase_SkipsDefaultBranchResolution(t *testing.T) {
	defaultBranchCalled := false
	restore := git.SetOps(&git.MockOps{
		BranchExistsFn: func(string) bool { return false },
		DefaultBranchFn: func() (string, error) {
			defaultBranchCalled = true
			return "develop", nil
		},
	})
	defer restore()

	var baseUpdates []struct {
		number int
		base   string
	}

	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			switch n {
			case 10:
				return &github.PullRequest{
					Number: 10, HeadRefName: "feat-a", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/10",
				}, nil
			case 20:
				return &github.PullRequest{
					Number: 20, HeadRefName: "feat-b", BaseRefName: "feat-a",
					URL: "https://github.com/o/r/pull/20",
				}, nil
			}
			return nil, nil
		},
		UpdatePRBaseFn: func(number int, base string) error {
			baseUpdates = append(baseUpdates, struct {
				number int
				base   string
			}{number, base})
			return nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--base", "release", "10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.False(t, defaultBranchCalled, "git.DefaultBranch must not be consulted when --base is given")
	// The bottom PR (#10) should be retargeted to the explicit base, not "main".
	require.Len(t, baseUpdates, 1)
	assert.Equal(t, 10, baseUpdates[0].number)
	assert.Equal(t, "release", baseUpdates[0].base)
}

func TestLink_DuplicateBranchResolvesToSamePR(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10, HeadRefName: branch, BaseRefName: "main",
			}, nil
		},
	}

	// Different args that resolve to the same PR
	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-a"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "duplicate argument")
}

func TestLink_UpdateDeletedStack_FallsBackToCreate(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var created bool
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: "b", BaseRefName: "main"}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{10}},
			}, nil
		},
		AddToStackFn: func(stackNumber int, _ []int) (*github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			created = true
			return &github.RemoteStack{ID: 99, Number: 99}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.True(t, created)
	assert.Contains(t, output, "Created stack with 2 PRs")
}

func TestLink_PushesBranchesBeforeResolution(t *testing.T) {
	var pushedBranches []string
	var pushedRemote string

	restore := git.SetOps(&git.MockOps{
		BranchExistsFn:  func(name string) bool { return name == "feat-a" || name == "feat-b" },
		ResolveRemoteFn: func(string) (string, error) { return "origin", nil },
		PushFn: func(remote string, branches []string, force, atomic bool) error {
			pushedRemote = remote
			pushedBranches = branches
			return nil
		},
	})
	defer restore()

	prCounter := 0
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			prCounter++
			return &github.PullRequest{
				Number: prCounter, HeadRefName: branch, BaseRefName: "main",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", prCounter),
			}, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: fmt.Sprintf("b%d", n), BaseRefName: "main"}, nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, "origin", pushedRemote)
	assert.Equal(t, []string{"feat-a", "feat-b"}, pushedBranches)
	assert.Contains(t, output, "Pushing 2 branches")
}

func TestLink_RemoteFlag(t *testing.T) {
	var pushedRemote string

	restore := git.SetOps(&git.MockOps{
		BranchExistsFn: func(string) bool { return true },
		PushFn: func(remote string, branches []string, force, atomic bool) error {
			pushedRemote = remote
			return nil
		},
	})
	defer restore()

	prCounter := 0
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			prCounter++
			return &github.PullRequest{
				Number: prCounter, HeadRefName: branch, BaseRefName: "main",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", prCounter),
			}, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: fmt.Sprintf("b%d", n), BaseRefName: "main"}, nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--remote", "upstream", "feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, "upstream", pushedRemote)
}

func TestLink_SkipsPushForPRNumbersOnly(t *testing.T) {
	pushCalled := false

	restore := git.SetOps(&git.MockOps{
		BranchExistsFn: func(string) bool { return false }, // PR numbers aren't local branches
		PushFn: func(string, []string, bool, bool) error {
			pushCalled = true
			return nil
		},
	})
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: "b", BaseRefName: "main"}, nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.False(t, pushCalled, "push should not be called when all args are PR numbers")
}

func TestLink_PrevalidatesBeforeCreatingPRs(t *testing.T) {
	// Scenario: branch feat-b has an existing PR #106 in a stack with [104, 105, 106].
	// User runs: gh stack link feat-a feat-b
	// feat-a has no PR yet, but the stack pre-validation should catch that
	// #104 and #105 would be dropped — and fail BEFORE creating a PR for feat-a.
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b"))
	defer restore()

	prCreated := false
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if branch == "feat-b" {
				return &github.PullRequest{
					Number: 106, HeadRefName: "feat-b", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/106",
				}, nil
			}
			return nil, nil // feat-a has no PR
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			prCreated = true
			return &github.PullRequest{Number: 200, HeadRefName: head}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, Number: 7, PullRequests: []int{104, 105, 106}},
			}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.False(t, prCreated, "should NOT create PRs before validating stack")
	assert.Contains(t, output, "would remove")
	assert.Contains(t, output, "#104")
	assert.Contains(t, output, "#105")
}

// --- Unit tests for helpers ---

func TestFindMatchingStack(t *testing.T) {
	tests := []struct {
		name      string
		stacks    []github.RemoteStack
		prNumbers []int
		wantID    int
		wantNil   bool
		wantErr   bool
	}{
		{
			name:      "no stacks",
			stacks:    []github.RemoteStack{},
			prNumbers: []int{10, 20},
			wantNil:   true,
		},
		{
			name: "no match",
			stacks: []github.RemoteStack{
				{ID: 1, PullRequests: []int{30, 40}},
			},
			prNumbers: []int{10, 20},
			wantNil:   true,
		},
		{
			name: "single match",
			stacks: []github.RemoteStack{
				{ID: 5, PullRequests: []int{10, 20}},
			},
			prNumbers: []int{10, 30},
			wantID:    5,
		},
		{
			name: "multiple matches",
			stacks: []github.RemoteStack{
				{ID: 1, PullRequests: []int{10}},
				{ID: 2, PullRequests: []int{20}},
			},
			prNumbers: []int{10, 20},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findMatchingStack(tt.stacks, tt.prNumbers)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.wantNil {
					assert.Nil(t, got)
				} else {
					assert.Equal(t, tt.wantID, got.ID)
				}
			}
		})
	}
}

func TestFormatPRList(t *testing.T) {
	assert.Equal(t, "#10", formatPRList([]int{10}))
	assert.Equal(t, "#10, #20, #30", formatPRList([]int{10, 20, 30}))
	assert.Equal(t, "", formatPRList([]int{}))
}

func TestSlicesEqual(t *testing.T) {
	assert.True(t, slicesEqual([]int{1, 2, 3}, []int{1, 2, 3}))
	assert.False(t, slicesEqual([]int{1, 2, 3}, []int{1, 2}))
	assert.False(t, slicesEqual([]int{1, 2}, []int{1, 3}))
	assert.True(t, slicesEqual([]int{}, []int{}))
}

func TestValidateArgs(t *testing.T) {
	assert.NoError(t, validateArgs([]string{"a", "b", "c"}))
	assert.NoError(t, validateArgs([]string{"10", "20"}))
	assert.Error(t, validateArgs([]string{"a", "a"}))
	assert.Error(t, validateArgs([]string{"10", "10"}))
}

func TestFormatAPIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "HTTP error with message",
			err:  &api.HTTPError{StatusCode: 422, Message: "Validation Failed"},
			want: "HTTP 422: Validation Failed",
		},
		{
			name: "HTTP error without message",
			err:  &api.HTTPError{StatusCode: 500},
			want: "HTTP 500",
		},
		{
			name: "non-HTTP error",
			err:  fmt.Errorf("network timeout"),
			want: "network timeout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatAPIError(tt.err))
		})
	}
}

func TestLink_FindPRByNumber_ErrorIsFatal(t *testing.T) {
	// When FindPRByNumber returns an error (not just nil), it should NOT
	// silently fall through to branch-name lookup.
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return nil, fmt.Errorf("network error")
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			t.Fatal("FindPRForBranch should NOT be called when FindPRByNumber errors")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"42", "43"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "failed to look up PR #42")
}

func TestLink_SkipsBaseFix_ForNewlyCreatedPRs(t *testing.T) {
	// When PRs are created by the command, fixBaseBranches should skip them
	// (no re-fetch needed since they already have the correct base).
	restore := git.SetOps(newLinkGitMock("feat-a", "feat-b"))
	defer restore()

	findByNumberCalls := 0
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil // no existing PRs
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			findByNumberCalls++
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 100, HeadRefName: head, BaseRefName: base,
				URL: "https://github.com/o/r/pull/100",
			}, nil
		},
		ListStacksFn:  func() ([]github.RemoteStack, error) { return []github.RemoteStack{}, nil },
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 1, Number: 1}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	// FindPRByNumber is called during findExistingPRs (phase 2) for numeric
	// args only, but NOT during fixBaseBranches for newly created PRs.
	// Since "feat-a" and "feat-b" are not numeric, FindPRByNumber should
	// not be called at all.
	assert.Equal(t, 0, findByNumberCalls, "FindPRByNumber should not be called for newly created PRs")
}

// Silence "imported and not used" for fmt in case test helpers use it.
var _ = fmt.Sprintf

func TestLink_BranchNames_UsesPRTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	ghDir := filepath.Join(tmpDir, ".github")
	require.NoError(t, os.MkdirAll(ghDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ghDir, "pull_request_template.md"),
		[]byte("## Summary\n\nDescribe your changes."),
		0o644,
	))

	mock := newLinkGitMock("feat-a", "feat-b")
	mock.RootDirFn = func() (string, error) { return tmpDir, nil }
	restore := git.SetOps(mock)
	defer restore()

	var capturedBody string
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) {
			return nil, nil // No existing PRs
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			capturedBody = body
			return &github.PullRequest{
				Number: 1, HeadRefName: head, BaseRefName: base,
				URL: "https://github.com/o/r/pull/1",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 42, Number: 42}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.Contains(t, capturedBody, "## Summary")
	assert.Contains(t, capturedBody, "Describe your changes.")
	assert.NotContains(t, capturedBody, "GitHub Stacks CLI", "footer should not be present when template is used")
}

// TestLink_IgnoresSymlinkedPRTemplate verifies that `gh stack link`, when it
// creates missing PRs, does not follow a symlinked PR template.
func TestLink_IgnoresSymlinkedPRTemplate(t *testing.T) {
	tmpDir := t.TempDir()

	// The repo's PR template is a symlink to a file outside the repository;
	// gh-stack must not follow it.
	linked := filepath.Join(t.TempDir(), "linked.txt")
	require.NoError(t, os.WriteFile(linked, []byte("LINKED_FILE_CONTENTS"), 0o600))

	ghDir := filepath.Join(tmpDir, ".github")
	require.NoError(t, os.MkdirAll(ghDir, 0o755))
	if err := os.Symlink(linked, filepath.Join(ghDir, "pull_request_template.md")); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	mock := newLinkGitMock("feat-a", "feat-b")
	mock.RootDirFn = func() (string, error) { return tmpDir, nil }
	restore := git.SetOps(mock)
	defer restore()

	var capturedBody string
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) {
			return nil, nil // No existing PRs
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			capturedBody = body
			return &github.PullRequest{
				Number: 1, HeadRefName: head, BaseRefName: base,
				URL: "https://github.com/o/r/pull/1",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 42, Number: 42}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.NotContains(t, capturedBody, "LINKED_FILE_CONTENTS", "symlinked template contents must not be included in the PR body")
	assert.Contains(t, capturedBody, "GitHub Stacks CLI", "template ignored, footer fallback should be used")
}

func TestLink_PRNumbers_NoTemplateUsesFooter(t *testing.T) {
	// When using PR numbers (no local repo context), no template is found
	// and the footer should be present for newly created PRs.
	mock := &git.MockOps{
		RootDirFn: func() (string, error) {
			return "", fmt.Errorf("not in a git repo")
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	var capturedBody string
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			if n == 10 {
				return &github.PullRequest{
					Number: 10, HeadRefName: "feat-a", BaseRefName: "main",
					URL: "https://github.com/o/r/pull/10",
				}, nil
			}
			return nil, nil // PR 20 doesn't exist → will create
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			capturedBody = body
			return &github.PullRequest{
				Number: 20, HeadRefName: head, BaseRefName: base,
				URL: "https://github.com/o/r/pull/20",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) { return &github.RemoteStack{ID: 42, Number: 42}, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	_, _ = io.ReadAll(errR)

	assert.NoError(t, err)
	assert.Contains(t, capturedBody, "GitHub Stacks CLI", "footer should be present when no template")
}

// --- PR URL tests ---

func TestLink_PRURLs_CreateNewStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var createdPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			createdPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{
		"https://github.com/o/r/pull/10",
		"https://github.com/o/r/pull/20",
		"https://github.com/o/r/pull/30",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{10, 20, 30}, createdPRs)
	assert.Contains(t, output, "Created stack with 3 PRs")
}

func TestLink_PRURLs_NotFound(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return nil, nil // PR not found
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{
		"https://github.com/o/r/pull/999",
		"https://github.com/o/r/pull/1000",
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "PR #999 not found")
}

func TestLink_MixedURLsAndNumbers(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var createdPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number:      n,
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "main",
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			createdPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "https://github.com/o/r/pull/20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{10, 20, 30}, createdPRs)
	assert.Contains(t, output, "Created stack with 3 PRs")
}

// --- add-mode tests (first arg is a stack number) ---

// linkPR builds an open RemoteStackPR with the given number and head ref.
func linkPR(num int, head string) github.RemoteStackPR {
	return github.RemoteStackPR{
		Number: num,
		State:  "open",
		Head:   github.RemoteStackPRHead{Ref: head},
	}
}

// linkRemoteStack builds a RemoteStack with matching PullRequests and PRDetails
// (bottom to top) for add-mode tests, so stackTopBranch can resolve the top ref.
func linkRemoteStack(number int, details ...github.RemoteStackPR) github.RemoteStack {
	rs := github.RemoteStack{ID: number, Number: number}
	for _, d := range details {
		rs.PullRequests = append(rs.PullRequests, d.Number)
		rs.PRDetails = append(rs.PRDetails, d)
	}
	return rs
}

func TestLink_AddMode_AppendsPRNumberToStack(t *testing.T) {
	var addNumber int
	var addPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n),
				BaseRefName: "branch-20", // already chained on the stack top
				URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addNumber = stackNumber
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
		CreateStackFn: func([]int) (*github.RemoteStack, error) {
			t.Fatal("CreateStack should not be called in add mode")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, 7, addNumber)
	assert.Equal(t, []int{30}, addPRs)
	assert.Contains(t, output, "Added 1 PR to stack #7")
}

func TestLink_AddMode_CreatesPRForBranchOnTopOfStack(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feature-c"))
	defer restore()

	var created []struct{ base, head string }
	var addPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			created = append(created, struct{ base, head string }{base, head})
			return &github.PullRequest{
				Number: 99, State: "OPEN", HeadRefName: head, BaseRefName: base,
				URL: "https://github.com/o/r/pull/99",
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "feature-c"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	require.Len(t, created, 1)
	assert.Equal(t, "branch-20", created[0].base) // chains on the stack top branch
	assert.Equal(t, "feature-c", created[0].head)
	assert.Equal(t, []int{99}, addPRs)
	assert.Contains(t, output, "Added 1 PR to stack #7")
}

func TestLink_AddMode_IdempotentWhenAllPresent(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-10",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called when nothing new to append")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Contains(t, output, "already in stack #7")
	assert.Contains(t, output, "Stack #7 is already up to date")
}

func TestLink_AddMode_SkipsPresentAppendsNew(t *testing.T) {
	var addPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			base := "branch-20" // new PR chains on the stack top
			if n == 20 {
				base = "branch-10"
			}
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: base,
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{30}, addPRs)
	assert.Contains(t, output, "already in stack #7")
	assert.Contains(t, output, "Added 1 PR to stack #7")
}

func TestLink_AddMode_RejectsPRFromAnotherStack(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "main",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
				linkRemoteStack(8, linkPR(30, "branch-30"), linkPR(40, "branch-40")),
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called when a PR is in another stack")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "already belongs to stack #8")
}

func TestLink_AddMode_RejectsIneligibleNewPR(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-20",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			if n == 30 {
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_1"}
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called for an ineligible PR")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "cannot be added to a stack")
	assert.Contains(t, output, "queued for merge")
}

func TestLink_AddMode_ExemptsIneligibleExistingMember(t *testing.T) {
	var addPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			pr := &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-20",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}
			if n == 20 {
				// A queued PR that is already a stack member: it is skipped,
				// so its ineligibility must not block appending PR 30.
				pr.MergeQueueEntry = &github.MergeQueueEntry{ID: "MQE_1"}
				pr.BaseRefName = "branch-10"
			}
			return pr, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "20", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{30}, addPRs)
	assert.Contains(t, output, "already in stack #7")
}

func TestLink_NumericFirstArgNotAStack_UsesCreateMode(t *testing.T) {
	restore := git.SetOps(newLinkGitMock())
	defer restore()

	var createdPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "main",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			// A stack exists but its number (7) does not match arg[0] (10).
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(50, "branch-50"), linkPR(60, "branch-60")),
			}, nil
		},
		CreateStackFn: func(prNumbers []int) (*github.RemoteStack, error) {
			createdPRs = prNumbers
			return &github.RemoteStack{ID: 42, Number: 42}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			t.Fatal("AddToStack should not be called in create mode")
			return nil, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{10, 20}, createdPRs)
	assert.Contains(t, output, "Created stack with 2 PRs")
}

func TestLink_AddMode_WarnsWhenBaseFlagSet(t *testing.T) {
	var addPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-20",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--base", "develop", "7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []int{30}, addPRs)
	assert.Contains(t, output, "--base is ignored")
}

func TestLink_AddMode_ChainsMultipleCreatedPRs(t *testing.T) {
	restore := git.SetOps(newLinkGitMock("feat-c", "feat-d"))
	defer restore()

	var created []struct{ base, head string }
	var addPRs []int
	prNum := 100
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(string) (*github.PullRequest, error) { return nil, nil },
		CreatePRFn: func(base, head, title, body string, draft bool) (*github.PullRequest, error) {
			prNum++
			created = append(created, struct{ base, head string }{base, head})
			return &github.PullRequest{
				Number: prNum, State: "OPEN", HeadRefName: head, BaseRefName: base,
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", prNum),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "feat-c", "feat-d"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	require.Len(t, created, 2)
	assert.Equal(t, "branch-20", created[0].base) // first new PR on the stack top
	assert.Equal(t, "feat-c", created[0].head)
	assert.Equal(t, "feat-c", created[1].base) // second new PR chains off the first
	assert.Equal(t, "feat-d", created[1].head)
	assert.Equal(t, []int{101, 102}, addPRs)
	assert.Contains(t, output, "Added 2 PRs to stack #7")
}

func TestLink_AddMode_AddToStack422(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-20",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 422, Message: "cannot append"}
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "Cannot add to stack")
	assert.Contains(t, output, "cannot append")
}

func TestLink_AddMode_AddToStack404_StackGone(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-20",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20")),
			}, nil
		},
		AddToStackFn: func(int, []int) (*github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "no longer exists")
}

func TestLink_AddMode_FetchesFullStackWhenListLacksHeadRefs(t *testing.T) {
	var addPRs []int
	var getStackCalls int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: n, State: "OPEN",
				HeadRefName: fmt.Sprintf("branch-%d", n), BaseRefName: "branch-20",
				URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n),
			}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			// The list response carries PR numbers but omits per-PR head refs.
			return []github.RemoteStack{{
				ID: 7, Number: 7,
				PullRequests: []int{10, 20},
				PRDetails: []github.RemoteStackPR{
					{Number: 10, State: "open"},
					{Number: 20, State: "open"},
				},
			}}, nil
		},
		GetStackFn: func(number int) (*github.RemoteStack, error) {
			getStackCalls++
			s := linkRemoteStack(7, linkPR(10, "branch-10"), linkPR(20, "branch-20"))
			return &s, nil
		},
		AddToStackFn: func(stackNumber int, prNumbers []int) (*github.RemoteStack, error) {
			addPRs = prNumbers
			return &github.RemoteStack{ID: 7, Number: 7}, nil
		},
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"7", "30"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, 1, getStackCalls) // fell back to fetch the full stack
	assert.Equal(t, []int{30}, addPRs)
	assert.Contains(t, output, "Added 1 PR to stack #7")
}
