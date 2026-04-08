package cmd

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectOutput closes the write ends of the test config pipes and returns
// the captured stderr content. Shared across cmd test files.
func collectOutput(cfg *config.Config, outR, errR *os.File) string {
	cfg.Out.Close()
	cfg.Err.Close()
	stderr, _ := io.ReadAll(errR)
	outR.Close()
	errR.Close()
	return string(stderr)
}

func TestInit_CreatesStackWithCorrectTrunk(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"myBranch"}})
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error in output")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	require.Len(t, sf.Stacks, 1)
	s := sf.Stacks[0]
	assert.Equal(t, "main", s.Trunk.Branch)
	names := s.BranchNames()
	require.Len(t, names, 1)
	assert.Equal(t, "myBranch", names[0])
}

func TestInit_CustomTrunk(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"myBranch"}, base: "develop"})
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	assert.Equal(t, "develop", sf.Stacks[0].Trunk.Branch)
}

func TestInit_AdoptExistingBranches(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return true },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{
		branches: []string{"b1", "b2", "b3"},
		adopt:    true,
	})
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	names := sf.Stacks[0].BranchNames()
	assert.Equal(t, []string{"b1", "b2", "b3"}, names)
}

func TestInit_PrefixStoredInStack(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"myBranch"}, prefix: "feat"})
	collectOutput(cfg, outR, errR)

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	assert.Equal(t, "feat", sf.Stacks[0].Prefix)
}

func TestInit_PrefixAppliedToExplicitBranches(t *testing.T) {
	gitDir := t.TempDir()
	var created []string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn: func(name, base string) error {
			created = append(created, name)
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"b1", "b2"}, prefix: "feat"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err, "runInit should succeed")
	require.NotContains(t, output, "\u2717", "unexpected error")
	assert.Equal(t, []string{"feat/b1", "feat/b2"}, created, "branches should be created with prefix")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	names := sf.Stacks[0].BranchNames()
	assert.Equal(t, []string{"feat/b1", "feat/b2"}, names, "stack should store prefixed branch names")
}

func TestInit_InvalidPrefixRejectedBeforeBranchCreation(t *testing.T) {
	gitDir := t.TempDir()
	var created []string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		ValidateRefNameFn: func(name string) error {
			return fmt.Errorf("invalid ref name: %s", name)
		},
		CreateBranchFn: func(name, base string) error {
			created = append(created, name)
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"mybranch"}, prefix: "bad..prefix"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs, "should reject invalid prefix")
	assert.Contains(t, output, "invalid prefix")
	assert.Empty(t, created, "no branches should be created when prefix is invalid")
}

func TestInit_AdoptRejectsPrefix(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{adopt: true, branches: []string{"b1"}, prefix: "feat"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "--adopt cannot be combined with --prefix or --numbered")
}

func TestInit_AdoptRejectsNumbered(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{adopt: true, branches: []string{"b1"}, numbered: true})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "--adopt cannot be combined with --prefix or --numbered")
}

func TestInit_RerereAlreadyEnabled(t *testing.T) {
	gitDir := t.TempDir()
	enableRerereCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		EnableRerereFn: func() error {
			enableRerereCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"b1"}})
	collectOutput(cfg, outR, errR)

	assert.False(t, enableRerereCalled, "EnableRerere should not be called when rerere is already enabled")
}

func TestInit_RefuseIfBranchAlreadyInStack(t *testing.T) {
	gitDir := t.TempDir()

	// Pre-create stack file with "feature-1" as a non-trunk branch
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{{
			Trunk:    stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{{Branch: "feature-1"}},
		}},
	}
	require.NoError(t, stack.Save(gitDir, sf), "saving seed stack")

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "feature-1", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"newBranch"}})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "already part of a stack")
}

func TestInit_AdoptNonexistentBranch(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return false },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"nonexistent"}, adopt: true})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "does not exist")
}

func TestInit_MultipleBranches_CreatesAll(t *testing.T) {
	gitDir := t.TempDir()
	var created []string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn: func(name, base string) error {
			created = append(created, name)
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"b1", "b2", "b3"}})
	output := collectOutput(cfg, outR, errR)

	require.NotContains(t, output, "\u2717", "unexpected error")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	names := sf.Stacks[0].BranchNames()
	assert.Equal(t, []string{"b1", "b2", "b3"}, names)
}

func TestInit_AdoptWithExistingOpenPR(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return true },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if branch == "b1" {
				return &github.PullRequest{
					Number:      42,
					ID:          "PR_42",
					URL:         "https://github.com/owner/repo/pull/42",
					State:       "OPEN",
					HeadRefName: "b1",
				}, nil
			}
			return nil, nil
		},
	}

	err := runInit(cfg, &initOptions{
		branches: []string{"b1", "b2"},
		adopt:    true,
	})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err, "adopt should succeed even when branch has an open PR")
	require.NotContains(t, output, "\u2717", "unexpected error in output")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	require.Len(t, sf.Stacks, 1)

	// b1 should have the open PR recorded
	b1 := sf.Stacks[0].Branches[0]
	require.NotNil(t, b1.PullRequest, "open PR should be recorded")
	assert.Equal(t, 42, b1.PullRequest.Number)
	assert.Equal(t, "https://github.com/owner/repo/pull/42", b1.PullRequest.URL)

	// b2 should have no PR
	b2 := sf.Stacks[0].Branches[1]
	assert.Nil(t, b2.PullRequest, "branch without PR should have nil PullRequest")
}

func TestInit_AdoptIgnoresClosedAndMergedPRs(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return true },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	// FindPRForBranch only returns OPEN PRs — closed/merged PRs won't be
	// returned by the API, so the mock returns nil for all branches.
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
	}

	err := runInit(cfg, &initOptions{
		branches: []string{"b1", "b2"},
		adopt:    true,
	})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err, "adopt should succeed when branches have closed/merged PRs")
	require.NotContains(t, output, "\u2717", "unexpected error in output")

	sf, err := stack.Load(gitDir)
	require.NoError(t, err, "loading stack")
	require.Len(t, sf.Stacks, 1)

	// Neither branch should have a PR recorded (closed/merged are filtered out)
	for _, b := range sf.Stacks[0].Branches {
		assert.Nil(t, b.PullRequest, "closed/merged PRs should not be recorded for branch %s", b.Branch)
	}
}
