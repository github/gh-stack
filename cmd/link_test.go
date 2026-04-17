package cmd

import (
	"fmt"
	"io"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- PR-number tests ---

func TestLink_PRNumbers_CreateNewStack(t *testing.T) {
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
		CreateStackFn: func(prNumbers []int) (int, error) {
			createdPRs = prNumbers
			return 42, nil
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
	var updatedID string
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
				{ID: 7, PullRequests: []int{10, 20}},
			}, nil
		},
		UpdateStackFn: func(stackID string, prNumbers []int) error {
			updatedID = stackID
			updatedPRs = prNumbers
			return nil
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
	assert.Equal(t, "7", updatedID)
	assert.Equal(t, []int{10, 20, 30}, updatedPRs)
	assert.Contains(t, output, "Updated stack to 3 PRs")
}

func TestLink_PRNumbers_ExactMatch_NoOp(t *testing.T) {
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
				{ID: 7, PullRequests: []int{10, 20, 30}},
			}, nil
		},
		UpdateStackFn: func(string, []int) error {
			t.Fatal("UpdateStack should not be called for exact match")
			return nil
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
				{ID: 7, PullRequests: []int{10, 20, 30}},
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
				{ID: 1, PullRequests: []int{10, 20}},
				{ID: 2, PullRequests: []int{30, 40}},
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
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: "b", BaseRefName: "main"}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 0, &api.HTTPError{
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

// --- Branch name tests ---

func TestLink_BranchNames_AllHavePRs(t *testing.T) {
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
		CreateStackFn: func(prNumbers []int) (int, error) {
			stackedPRs = prNumbers
			return 42, nil
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
		CreateStackFn: func(prNumbers []int) (int, error) {
			stackedPRs = prNumbers
			return 42, nil
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
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 42, nil
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

func TestLink_BranchNames_DraftFlag(t *testing.T) {
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
		CreateStackFn: func([]int) (int, error) { return 1, nil },
	}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"--draft", "feat-a", "feat-b"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.True(t, createdDraft, "PRs should be created as drafts when --draft is set")
}

func TestLink_MixedArgs_PRNumberAndBranch(t *testing.T) {
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
		CreateStackFn: func(prNumbers []int) (int, error) {
			stackedPRs = prNumbers
			return 42, nil
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
		CreateStackFn: func(prNumbers []int) (int, error) {
			stackedPRs = prNumbers
			return 42, nil
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

func TestLink_FixesBaseBranches(t *testing.T) {
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
		CreateStackFn: func([]int) (int, error) { return 42, nil },
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
	var created bool
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: n, HeadRefName: "b", BaseRefName: "main"}, nil
		},
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 7, PullRequests: []int{10}},
			}, nil
		},
		UpdateStackFn: func(string, []int) error {
			return &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			created = true
			return 99, nil
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

// Silence "imported and not used" for fmt in case test helpers use it.
var _ = fmt.Sprintf
