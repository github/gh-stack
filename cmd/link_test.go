package cmd

import (
	"fmt"
	"io"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/github"
	"github.com/stretchr/testify/assert"
)

func TestLink_CreateNewStack(t *testing.T) {
	var createdPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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

func TestLink_UpdateExistingStack_Superset(t *testing.T) {
	var updatedID string
	var updatedPRs []int
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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

func TestLink_ExactMatch_NoOp(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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

func TestLink_WouldRemovePRs(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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

func TestLink_MultipleStacks(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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

func TestLink_TooFewPRs(t *testing.T) {
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

func TestLink_InvalidArgs(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"abc", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "invalid PR number")
}

func TestLink_DuplicatePRNumbers(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"10", "10"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "duplicate PR number")
}

func TestLink_StacksUnavailable(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{}, nil
		},
		CreateStackFn: func(prNumbers []int) (int, error) {
			return 0, &api.HTTPError{
				StatusCode: 422,
				Message:    "Pull requests must form a stack, where each PR's base ref is the previous PR's head ref",
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

func TestLink_UpdateDeletedStack_FallsBackToCreate(t *testing.T) {
	var created bool
	cfg, _, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
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

func TestParsePRNumbers(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []int
		wantErr string
	}{
		{
			name: "valid numbers",
			args: []string{"1", "2", "3"},
			want: []int{1, 2, 3},
		},
		{
			name:    "non-numeric",
			args:    []string{"abc"},
			wantErr: "invalid PR number",
		},
		{
			name:    "zero",
			args:    []string{"0"},
			wantErr: "invalid PR number",
		},
		{
			name:    "negative",
			args:    []string{"-1"},
			wantErr: "invalid PR number",
		},
		{
			name:    "duplicate",
			args:    []string{"5", "5"},
			wantErr: "duplicate PR number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePRNumbers(tt.args)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

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

func TestLink_NegativePRNumber(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	cmd := LinkCmd(cfg)
	cmd.SetArgs([]string{"-1", "20"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	// -1 is treated as a flag by cobra and will error before RunE
	assert.Error(t, err)
}

// Silence "imported and not used" for fmt in case test helpers use it.
var _ = fmt.Sprintf
