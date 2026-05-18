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

func TestInit_AdoptFlagShowsDeprecationWarning(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return true },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{adopt: true, branches: []string{"b1"}})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "--adopt flag is deprecated")
}

func TestInit_RerereAlreadyEnabled(t *testing.T) {
	gitDir := t.TempDir()
	enableRerereCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:          func() (string, error) { return gitDir, nil },
		DefaultBranchFn:   func() (string, error) { return "main", nil },
		CurrentBranchFn:   func() (string, error) { return "main", nil },
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

func TestInit_AdoptNonexistentBranch_CreatesIt(t *testing.T) {
	// --adopt with missing branch now creates it (no error, just a deprecation warning)
	gitDir := t.TempDir()
	var created []string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return false },
		CreateBranchFn: func(name, base string) error {
			created = append(created, name)
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"nonexistent"}, adopt: true})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "--adopt flag is deprecated")
	assert.Equal(t, []string{"nonexistent"}, created)
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

// --- Tests for spec scenarios ---

func TestInit_ImplicitAdopt_AllExist(t *testing.T) {
	// Scenario 8: all branches exist → implicit adopt, PR detection runs
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
				return &github.PullRequest{Number: 10, ID: "PR_10", URL: "https://example.com/10"}, nil
			}
			return nil, nil
		},
	}

	err := runInit(cfg, &initOptions{branches: []string{"b1", "b2", "b3"}})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717")
	assert.Contains(t, output, "Adopted")
	assert.Contains(t, output, "Found PRs for 1 of 3")

	sf, _ := stack.Load(gitDir)
	assert.Equal(t, []string{"b1", "b2", "b3"}, sf.Stacks[0].BranchNames())
	assert.NotNil(t, sf.Stacks[0].Branches[0].PullRequest)
}

func TestInit_ImplicitAdopt_AllMissing(t *testing.T) {
	// Scenario 7: all branches missing → create all
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
	err := runInit(cfg, &initOptions{branches: []string{"b1", "b2", "b3"}})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717")
	assert.Contains(t, output, "Created stack")
	assert.NotContains(t, output, "Adopted")
	assert.Equal(t, []string{"b1", "b2", "b3"}, created)
}

func TestInit_ImplicitAdopt_Mixed(t *testing.T) {
	// Scenario 11: mixed → adopts existing, creates missing
	gitDir := t.TempDir()
	existing := map[string]bool{"existing1": true, "existing2": true}
	var created []string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(name string) bool { return existing[name] },
		CreateBranchFn: func(name, base string) error {
			created = append(created, name)
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"existing1", "new1", "existing2"}})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	require.NotContains(t, output, "\u2717")
	assert.Contains(t, output, "Adopted")
	assert.Equal(t, []string{"new1"}, created)

	sf, _ := stack.Load(gitDir)
	assert.Equal(t, []string{"existing1", "new1", "existing2"}, sf.Stacks[0].BranchNames())
}

func TestInit_PrefixDetection_ArgsCommonPrefix(t *testing.T) {
	// Scenario 9: args all share prefix → set silently
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"feat/a", "feat/b", "feat/c"}})
	collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	sf, _ := stack.Load(gitDir)
	assert.Equal(t, "feat", sf.Stacks[0].Prefix)
}

func TestInit_PrefixDetection_ArgsMixedPrefix(t *testing.T) {
	// Scenario 10: args mixed prefixes → no prefix
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"feat/a", "bug/b"}})
	collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	sf, _ := stack.Load(gitDir)
	assert.Equal(t, "", sf.Stacks[0].Prefix)
}

func TestInit_PrefixDetection_ArgsNoSlash(t *testing.T) {
	// No slashes → no prefix
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"auth", "api", "ui"}})
	collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	sf, _ := stack.Load(gitDir)
	assert.Equal(t, "", sf.Stacks[0].Prefix)
}

func TestInit_PrefixDetection_NestedPrefix(t *testing.T) {
	// Scenario 6: sameen/feat/x → prefix "sameen/feat"
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"sameen/feat/a", "sameen/feat/b"}})
	collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	sf, _ := stack.Load(gitDir)
	assert.Equal(t, "sameen/feat", sf.Stacks[0].Prefix)
}

func TestInit_ExplicitPrefixSkipsDetection(t *testing.T) {
	// Scenario 14: --prefix with args → explicit wins
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"b1", "b2"}, prefix: "foo"})
	collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	sf, _ := stack.Load(gitDir)
	assert.Equal(t, "foo", sf.Stacks[0].Prefix)
	assert.Equal(t, []string{"foo/b1", "foo/b2"}, sf.Stacks[0].BranchNames())
}

func TestInit_WhatsNext_Fresh(t *testing.T) {
	// Scenario 17: fresh single-branch → fresh format
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"my-feature"}})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "Created stack")
	assert.Contains(t, output, "main ← my-feature")
	assert.Contains(t, output, "top of stack")
	assert.Contains(t, output, "What's next:")
	assert.Contains(t, output, "gh stack add")
	assert.Contains(t, output, "gh stack view")
	assert.Contains(t, output, "gh stack submit")
	assert.NotContains(t, output, "Adopted")
}

func TestInit_WhatsNext_AdoptedWithPRs(t *testing.T) {
	// Scenario 18: adopted multi-branch, 2 of 3 PRs → adopt format with PR count
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(string) bool { return true },
	})
	defer restore()

	prBranches := map[string]bool{"b1": true, "b3": true}
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if prBranches[branch] {
				return &github.PullRequest{Number: 1, ID: "PR_1", URL: "https://example.com/1"}, nil
			}
			return nil, nil
		},
	}

	runInit(cfg, &initOptions{branches: []string{"b1", "b2", "b3"}})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "Adopted 3 branches")
	assert.Contains(t, output, "Found PRs for 2 of 3")
	assert.Contains(t, output, "What's next:")
	assert.Contains(t, output, "gh stack view")
	assert.Contains(t, output, "gh stack switch")
	assert.Contains(t, output, "gh stack submit")
}

func TestInit_WhatsNext_AdoptedNoPRs(t *testing.T) {
	// Scenario 19: adopted, 0 PRs → no PR summary line
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
			return nil, nil
		},
	}

	runInit(cfg, &initOptions{branches: []string{"b1", "b2"}})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "Adopted 2 branches")
	assert.NotContains(t, output, "Found PRs")
}

func TestInit_WhatsNext_MixedWithPR(t *testing.T) {
	// Scenario 20: mixed (1 adopted, 1 created), 1 PR → adopt format
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(name string) bool { return name == "existing" },
		CreateBranchFn:  func(name, base string) error { return nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			if branch == "existing" {
				return &github.PullRequest{Number: 5, ID: "PR_5", URL: "https://example.com/5"}, nil
			}
			return nil, nil
		},
	}

	runInit(cfg, &initOptions{branches: []string{"existing", "new-branch"}})
	output := collectOutput(cfg, outR, errR)

	assert.Contains(t, output, "Adopted")
	assert.Contains(t, output, "Found PRs for 1 of 2")
}

func TestInit_Interactive_OnTrunk(t *testing.T) {
	// Scenario 1: on trunk → shows hint about multi-branch args
	// Note: full interactive Input() prompt requires TTY; test via args path instead.
	// Here we verify that the hint line appears and non-interactive errors correctly.
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	// Not interactive → should error with guidance
	err := runInit(cfg, &initOptions{})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "interactive input required")
}

func TestInit_Interactive_OnFeatureBranch_UseCurrent(t *testing.T) {
	// Scenario 2: on feature branch → select, choose "use current"
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "feat/auth", nil },
		BranchExistsFn:  func(name string) bool { return name == "feat/auth" },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	// Select option 0 = "Use current branch"
	cfg.SelectFn = func(prompt, def string, options []string) (int, error) {
		return 0, nil
	}

	err := runInit(cfg, &initOptions{})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Initializing a stack from main")
	// Branch already exists → should be treated as adopted
	assert.Contains(t, output, "Adopted")

	sf, _ := stack.Load(gitDir)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"feat/auth"}, sf.Stacks[0].BranchNames())
	// Prefix detection Y/n prompt fails gracefully without a TTY,
	// so prefix is not set. The args-path prefix detection is tested separately.
}

func TestInit_TwoPassValidation_NoBranchCreatedOnError(t *testing.T) {
	// Verify that if arg 3 fails validation, args 1 and 2 are NOT created
	gitDir := t.TempDir()

	// Pre-create a stack with "dup" branch
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{{
			Trunk:    stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{{Branch: "dup"}},
		}},
	}
	require.NoError(t, stack.Save(gitDir, sf))

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
	err := runInit(cfg, &initOptions{branches: []string{"new1", "new2", "dup"}})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "already exists in a stack")
	assert.Empty(t, created, "no branches should be created when later arg fails validation")
}

func TestInit_TwoPassValidation_InvalidRefName(t *testing.T) {
	// Verify that an invalid ref name in the args list prevents any branch creation
	gitDir := t.TempDir()
	var created []string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		ValidateRefNameFn: func(name string) error {
			if name == "invalid..name" {
				return fmt.Errorf("invalid ref name: %s", name)
			}
			return nil
		},
		CreateBranchFn: func(name, base string) error {
			created = append(created, name)
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	err := runInit(cfg, &initOptions{branches: []string{"valid-branch", "invalid..name", "another-branch"}})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "invalid branch name")
	assert.Empty(t, created, "no branches should be created when an arg has an invalid ref name")
}

func TestDetectPrefix(t *testing.T) {
	tests := []struct {
		name     string
		branches []string
		want     string
	}{
		{"common prefix", []string{"feat/a", "feat/b", "feat/c"}, "feat"},
		{"nested prefix", []string{"sameen/feat/a", "sameen/feat/b"}, "sameen/feat"},
		{"mixed prefixes", []string{"feat/a", "bug/b"}, ""},
		{"no slashes", []string{"auth", "api", "ui"}, ""},
		{"empty list", []string{}, ""},
		{"single branch with slash", []string{"feat/x"}, "feat"},
		{"single branch no slash", []string{"auth"}, ""},
		{"leading slash only", []string{"/x"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectPrefix(tt.branches)
			assert.Equal(t, tt.want, got)
		})
	}
}
