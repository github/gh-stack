package stackview

import (
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBranchNodes_UsesMergeBaseForDivergedBranch(t *testing.T) {
	// Scenario: local main has diverged from the branch's history.
	// Without merge-base, diff would be computed against main directly,
	// inflating the file count with unrelated changes.
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "feature"}},
	}

	var diffBase string
	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(ancestor, descendant string) (bool, error) {
			// main is NOT an ancestor of feature (diverged)
			return false, nil
		},
		MergeBaseFn: func(a, b string) (string, error) {
			return "abc123", nil
		},
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return []git.CommitInfo{{SHA: "def456", Subject: "only commit"}}, nil
		},
		DiffStatFilesFn: func(base, head string) ([]git.FileDiffStat, error) {
			diffBase = base
			return []git.FileDiffStat{
				{Path: "file1.go", Additions: 5, Deletions: 2},
				{Path: "file2.go", Additions: 3, Deletions: 1},
			}, nil
		},
	})
	defer restore()

	cfg, outW, errW := config.NewTestConfig()
	defer outW.Close()
	defer errW.Close()
	// Ensure no real GitHub API calls
	cfg.GitHubClientOverride = &ghapi.MockClient{}

	nodes := LoadBranchNodes(cfg, s, "feature")

	require.Len(t, nodes, 1)
	// Diff must be computed from merge-base, not from "main" directly.
	assert.Equal(t, "abc123", diffBase, "diff should use merge-base as base, not the branch name")
	assert.Len(t, nodes[0].FilesChanged, 2)
	assert.Equal(t, 8, nodes[0].Additions)
	assert.Equal(t, 3, nodes[0].Deletions)
	assert.False(t, nodes[0].IsLinear)
}

func TestLoadBranchNodes_LinearBranchStillUsesMergeBase(t *testing.T) {
	// When base IS an ancestor (linear history), merge-base returns the
	// base tip, so behavior is unchanged.
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "feature"}},
	}

	var diffBase string
	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(ancestor, descendant string) (bool, error) {
			return true, nil
		},
		MergeBaseFn: func(a, b string) (string, error) {
			// For linear history, merge-base returns the base tip
			return "main-tip-sha", nil
		},
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return nil, nil
		},
		DiffStatFilesFn: func(base, head string) ([]git.FileDiffStat, error) {
			diffBase = base
			return []git.FileDiffStat{
				{Path: "only.go", Additions: 1, Deletions: 0},
			}, nil
		},
	})
	defer restore()

	cfg, outW, errW := config.NewTestConfig()
	defer outW.Close()
	defer errW.Close()
	cfg.GitHubClientOverride = &ghapi.MockClient{}

	nodes := LoadBranchNodes(cfg, s, "other")

	require.Len(t, nodes, 1)
	assert.Equal(t, "main-tip-sha", diffBase)
	assert.Len(t, nodes[0].FilesChanged, 1)
	assert.True(t, nodes[0].IsLinear)
}

func TestLoadBranchNodes_IgnoresStaleMergedPRDetails(t *testing.T) {
	// When FindPRDetailsForBranch returns a merged PR that doesn't match
	// the branch's tracked PR, it should be ignored (stale from branch reuse).
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "reused-branch"}, // no tracked PR
		},
	}

	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, b string) (bool, error) { return true, nil },
		MergeBaseFn:  func(a, b string) (string, error) { return "abc", nil },
		LogRangeFn:   func(a, b string) ([]git.CommitInfo, error) { return nil, nil },
		DiffStatFilesFn: func(a, b string) ([]git.FileDiffStat, error) {
			return nil, nil
		},
	})
	defer restore()

	cfg, outW, errW := config.NewTestConfig()
	defer outW.Close()
	defer errW.Close()
	cfg.GitHubClientOverride = &ghapi.MockClient{
		FindPRDetailsForBranchFn: func(branch string) (*ghapi.PRDetails, error) {
			return &ghapi.PRDetails{
				Number: 20,
				Title:  "Old merged PR",
				State:  "MERGED",
				Merged: true,
			}, nil
		},
	}

	nodes := LoadBranchNodes(cfg, s, "other")

	require.Len(t, nodes, 1)
	assert.Nil(t, nodes[0].PR, "stale merged PR should not be adopted")
}

func TestLoadBranchNodes_ShowsTrackedMergedPRDetails(t *testing.T) {
	// When FindPRDetailsForBranch returns a merged PR that matches the
	// branch's tracked PR number, it should be shown (legitimately merged).
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "merged-branch",
				PullRequest: &stack.PullRequestRef{
					Number: 20,
					Merged: true,
				},
			},
		},
	}

	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, b string) (bool, error) { return true, nil },
		MergeBaseFn:  func(a, b string) (string, error) { return "abc", nil },
		LogRangeFn:   func(a, b string) ([]git.CommitInfo, error) { return nil, nil },
		DiffStatFilesFn: func(a, b string) ([]git.FileDiffStat, error) {
			return nil, nil
		},
	})
	defer restore()

	cfg, outW, errW := config.NewTestConfig()
	defer outW.Close()
	defer errW.Close()
	cfg.GitHubClientOverride = &ghapi.MockClient{
		FindPRDetailsForBranchFn: func(branch string) (*ghapi.PRDetails, error) {
			return &ghapi.PRDetails{
				Number: 20,
				Title:  "Legitimately merged PR",
				State:  "MERGED",
				Merged: true,
			}, nil
		},
	}

	nodes := LoadBranchNodes(cfg, s, "other")

	require.Len(t, nodes, 1)
	require.NotNil(t, nodes[0].PR, "tracked merged PR should be shown")
	assert.Equal(t, 20, nodes[0].PR.Number)
}

func TestLoadBranchNodes_ShowsOpenPRDetails(t *testing.T) {
	// An OPEN PR should always be shown, even without a tracked PR.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature"}, // no tracked PR
		},
	}

	restore := git.SetOps(&git.MockOps{
		IsAncestorFn: func(a, b string) (bool, error) { return true, nil },
		MergeBaseFn:  func(a, b string) (string, error) { return "abc", nil },
		LogRangeFn:   func(a, b string) ([]git.CommitInfo, error) { return nil, nil },
		DiffStatFilesFn: func(a, b string) ([]git.FileDiffStat, error) {
			return nil, nil
		},
	})
	defer restore()

	cfg, outW, errW := config.NewTestConfig()
	defer outW.Close()
	defer errW.Close()
	cfg.GitHubClientOverride = &ghapi.MockClient{
		FindPRDetailsForBranchFn: func(branch string) (*ghapi.PRDetails, error) {
			return &ghapi.PRDetails{
				Number: 50,
				Title:  "Active PR",
				State:  "OPEN",
			}, nil
		},
	}

	nodes := LoadBranchNodes(cfg, s, "other")

	require.Len(t, nodes, 1)
	require.NotNil(t, nodes[0].PR, "OPEN PR should be shown")
	assert.Equal(t, 50, nodes[0].PR.Number)
}
