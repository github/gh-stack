package cmd

import (
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectPRDrafts_SkipsWhenNoNewBranches verifies the TUI is skipped (no
// program launched) when every branch already has a PR, returning nil drafts so
// the normal push/relink path runs.
func TestCollectPRDrafts_SkipsWhenNoNewBranches(t *testing.T) {
	s := &stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	mock := &git.MockOps{
		RootDirFn:       func() (string, error) { return t.TempDir(), nil },
		IsAncestorFn:    func(a, b string) (bool, error) { return true, nil },
		MergeBaseFn:     func(a, b string) (string, error) { return a, nil },
		LogRangeFn:      func(base, head string) ([]git.CommitInfo, error) { return []git.CommitInfo{{Subject: "c"}}, nil },
		DiffStatFilesFn: func(base, head string) ([]git.FileDiffStat, error) { return nil, nil },
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	prDetails := map[string]*github.PRDetails{
		"b1": {Number: 1, State: "OPEN"},
		"b2": {Number: 2, State: "OPEN"},
	}

	drafts, cancelled, err := collectPRDrafts(cfg, nil, s, "b1", prDetails, "")
	require.NoError(t, err)
	assert.False(t, cancelled)
	assert.Nil(t, drafts, "no NEW branches means the TUI is skipped and drafts are nil")
}
