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
