package cmd

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"seconds", 30 * time.Second, "30 seconds ago"},
		{"one second", 1 * time.Second, "1 second ago"},
		{"minutes", 5 * time.Minute, "5 minutes ago"},
		{"one minute", 1 * time.Minute, "1 minute ago"},
		{"hours", 3 * time.Hour, "3 hours ago"},
		{"one hour", 1 * time.Hour, "1 hour ago"},
		{"days", 2 * 24 * time.Hour, "2 days ago"},
		{"one day", 24 * time.Hour, "1 day ago"},
		{"months", 60 * 24 * time.Hour, "2 months ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := timeAgo(time.Now().Add(-tt.duration))
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestViewJSON(t *testing.T) {
	git.SetOps(&git.MockOps{
		IsAncestorFn: func(ancestor, descendant string) (bool, error) {
			return true, nil // all branches are linear
		},
	})

	tests := []struct {
		name          string
		stack         *stack.Stack
		currentBranch string
		wantTrunk     string
		wantBranches  int
		wantCurrent   string
	}{
		{
			name: "basic stack with PRs",
			stack: &stack.Stack{
				Prefix: "feat",
				Trunk:  stack.BranchRef{Branch: "main", Head: "aaa"},
				Branches: []stack.BranchRef{
					{
						Branch:      "feat/01",
						Head:        "bbb",
						Base:        "aaa",
						PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"},
					},
					{
						Branch:      "feat/02",
						Head:        "ccc",
						Base:        "bbb",
						PullRequest: &stack.PullRequestRef{Number: 43, URL: "https://github.com/o/r/pull/43"},
					},
				},
			},
			currentBranch: "feat/02",
			wantTrunk:     "main",
			wantBranches:  2,
			wantCurrent:   "feat/02",
		},
		{
			name: "stack with merged branch",
			stack: &stack.Stack{
				Trunk: stack.BranchRef{Branch: "main", Head: "aaa"},
				Branches: []stack.BranchRef{
					{
						Branch:      "layer-1",
						Head:        "bbb",
						Base:        "aaa",
						PullRequest: &stack.PullRequestRef{Number: 10, Merged: true},
					},
					{
						Branch: "layer-2",
						Head:   "ccc",
						Base:   "bbb",
					},
				},
			},
			currentBranch: "layer-2",
			wantTrunk:     "main",
			wantBranches:  2,
			wantCurrent:   "layer-2",
		},
		{
			name: "empty stack",
			stack: &stack.Stack{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{},
			},
			currentBranch: "main",
			wantTrunk:     "main",
			wantBranches:  0,
			wantCurrent:   "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, outR, _ := config.NewTestConfig()
			defer outR.Close()

			err := viewJSON(cfg, tt.stack, tt.currentBranch)
			require.NoError(t, err)
			cfg.Out.Close()

			raw, err := io.ReadAll(outR)
			require.NoError(t, err)

			var got viewJSONOutput
			err = json.Unmarshal(raw, &got)
			require.NoError(t, err, "output should be valid JSON: %s", string(raw))

			assert.Equal(t, tt.wantTrunk, got.Trunk)
			assert.Equal(t, tt.wantCurrent, got.CurrentBranch)
			assert.Len(t, got.Branches, tt.wantBranches)
		})
	}
}

func TestViewJSON_BranchFields(t *testing.T) {
	git.SetOps(&git.MockOps{
		IsAncestorFn: func(ancestor, descendant string) (bool, error) {
			// feat/02 needs rebase
			if descendant == "feat/02" {
				return false, nil
			}
			return true, nil
		},
	})

	s := &stack.Stack{
		Prefix: "feat",
		Trunk:  stack.BranchRef{Branch: "main", Head: "aaa111"},
		Branches: []stack.BranchRef{
			{
				Branch:      "feat/01",
				Head:        "bbb222",
				Base:        "aaa111",
				PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42", Merged: true},
			},
			{
				Branch:      "feat/02",
				Head:        "ccc333",
				Base:        "bbb222",
				PullRequest: &stack.PullRequestRef{Number: 43, URL: "https://github.com/o/r/pull/43"},
			},
		},
	}

	cfg, outR, _ := config.NewTestConfig()
	defer outR.Close()

	err := viewJSON(cfg, s, "feat/02")
	require.NoError(t, err)
	cfg.Out.Close()

	raw, err := io.ReadAll(outR)
	require.NoError(t, err)

	var got viewJSONOutput
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, "feat", got.Prefix)

	// First branch: merged
	b0 := got.Branches[0]
	assert.Equal(t, "feat/01", b0.Name)
	assert.Equal(t, "bbb222", b0.Head)
	assert.Equal(t, "aaa111", b0.Base)
	assert.False(t, b0.IsCurrent)
	assert.True(t, b0.IsMerged)
	assert.False(t, b0.NeedsRebase, "merged branches should not need rebase")
	require.NotNil(t, b0.PR)
	assert.Equal(t, 42, b0.PR.Number)
	assert.Equal(t, "MERGED", b0.PR.State)
	assert.Equal(t, "https://github.com/o/r/pull/42", b0.PR.URL)

	// Second branch: current, needs rebase
	b1 := got.Branches[1]
	assert.Equal(t, "feat/02", b1.Name)
	assert.True(t, b1.IsCurrent)
	assert.False(t, b1.IsMerged)
	assert.True(t, b1.NeedsRebase)
	require.NotNil(t, b1.PR)
	assert.Equal(t, 43, b1.PR.Number)
	assert.Equal(t, "OPEN", b1.PR.State)
}

// TestViewShort_ActiveStack verifies that --short output contains all branch
// names and the trunk for an active stack.
func TestViewShort_ActiveStack(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
		IsAncestorFn:    func(string, string) (bool, error) { return true, nil },
		RevParseFn:      func(ref string) (string, error) { return "sha-" + ref, nil },
	})
	defer restore()

	cfg, outR, _ := config.NewTestConfig()
	cmd := ViewCmd(cfg)
	cmd.SetArgs([]string{"--short"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	raw, _ := io.ReadAll(outR)
	output := string(raw)

	assert.NoError(t, err)
	assert.Contains(t, output, "b1")
	assert.Contains(t, output, "b2")
	assert.Contains(t, output, "b3")
	assert.Contains(t, output, "main")
}

// TestViewShort_FullyMergedStack verifies that --short output shows merged
// branches correctly when all branches in the stack are merged.
func TestViewShort_FullyMergedStack(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	}

	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		IsAncestorFn:    func(string, string) (bool, error) { return true, nil },
		RevParseFn:      func(ref string) (string, error) { return "sha-" + ref, nil },
	})
	defer restore()

	cfg, outR, _ := config.NewTestConfig()
	cmd := ViewCmd(cfg)
	cmd.SetArgs([]string{"--short"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	raw, _ := io.ReadAll(outR)
	output := string(raw)

	assert.NoError(t, err)
	assert.Contains(t, output, "b1")
	assert.Contains(t, output, "b2")
}
