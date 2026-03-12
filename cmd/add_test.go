package cmd

import (
	"strings"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
)

// saveStack is a helper to pre-create a stack file for add tests.
func saveStack(t *testing.T, gitDir string, s stack.Stack) {
	t.Helper()
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks:        []stack.Stack{s},
	}
	if err := stack.Save(gitDir, sf); err != nil {
		t.Fatalf("saving seed stack: %v", err)
	}
}

func TestAdd_CreatesNewBranch(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	var createdBranch, checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}
	if createdBranch != "newbranch" {
		t.Errorf("CreateBranch got %q, want %q", createdBranch, "newbranch")
	}
	if checkedOut != "newbranch" {
		t.Errorf("CheckoutBranch got %q, want %q", checkedOut, "newbranch")
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		t.Fatalf("loading stack: %v", err)
	}
	names := sf.Stacks[0].BranchNames()
	if names[len(names)-1] != "newbranch" {
		t.Errorf("top branch = %q, want %q", names[len(names)-1], "newbranch")
	}
}

func TestAdd_OnlyAllowedOnTopOfStack(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	})

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "top of the stack") {
		t.Errorf("expected 'top of the stack' error, got: %s", output)
	}
}

func TestAdd_MutuallyExclusiveFlags(t *testing.T) {
	restore := git.SetOps(&git.MockOps{})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, stageTracked: true, message: "msg"}, []string{"branch"})
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' error, got: %s", output)
	}
}

func TestAdd_StagingRequiresMessage(t *testing.T) {
	restore := git.SetOps(&git.MockOps{})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true}, []string{"branch"})
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "require -m") {
		t.Errorf("expected 'require -m' error, got: %s", output)
	}
}

func TestAdd_EmptyBranchCommitsInPlace(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	createBranchCalled := false
	commitCalled := false
	stageAllCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return nil, nil // no unique commits — branch is empty
		},
		StageAllFn: func() error {
			stageAllCalled = true
			return nil
		},
		HasStagedChangesFn: func() bool { return true },
		CommitFn: func(msg string) (string, error) {
			commitCalled = true
			return "abc1234567890", nil
		},
		CreateBranchFn: func(name, base string) error {
			createBranchCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "Auth middleware"}, nil)
	output := collectOutput(cfg, outR, errR)

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}
	if !stageAllCalled {
		t.Error("expected StageAll to be called")
	}
	if !commitCalled {
		t.Error("expected Commit to be called")
	}
	if createBranchCalled {
		t.Error("CreateBranch should NOT be called for empty branch commit-in-place")
	}
}

func TestAdd_BranchWithCommitsCreatesNew(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	createCalled := false
	checkoutCalled := false
	commitCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return []git.CommitInfo{{SHA: "abc1234567890", Subject: "existing"}}, nil
		},
		CreateBranchFn: func(name, base string) error {
			createCalled = true
			return nil
		},
		CheckoutBranchFn: func(name string) error {
			checkoutCalled = true
			return nil
		},
		HasStagedChangesFn: func() bool { return true },
		CommitFn: func(msg string) (string, error) {
			commitCalled = true
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "API routes"}, nil)
	output := collectOutput(cfg, outR, errR)

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}
	if !createCalled {
		t.Error("expected CreateBranch to be called")
	}
	if !checkoutCalled {
		t.Error("expected CheckoutBranch to be called")
	}
	if !commitCalled {
		t.Error("expected Commit to be called on the new branch")
	}
}

func TestAdd_PrefixAppliedWithSlash(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Prefix:   "feat",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "feat/01"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "feat/01", nil },
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"mybranch"})
	output := collectOutput(cfg, outR, errR)

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}
	if createdBranch != "feat/mybranch" {
		t.Errorf("created branch = %q, want %q", createdBranch, "feat/mybranch")
	}
}

func TestAdd_NumberedNaming(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Prefix:   "feat",
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "feat/01"}},
	})

	var createdBranch string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "feat/01", nil },
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return []git.CommitInfo{{SHA: "abc1234567890", Subject: "existing"}}, nil
		},
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			return nil
		},
		HasStagedChangesFn: func() bool { return true },
		CommitFn: func(msg string) (string, error) {
			return "def1234567890", nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "next feature"}, nil)
	output := collectOutput(cfg, outR, errR)

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}
	if createdBranch != "feat/02" {
		t.Errorf("created branch = %q, want %q", createdBranch, "feat/02")
	}
}

func TestAdd_FullyMergedStackBlocked(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	})

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{}, []string{"newbranch"})
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "All branches in this stack have been merged") {
		t.Errorf("expected merged warning, got: %s", output)
	}
}

func TestAdd_NothingToCommit(t *testing.T) {
	gitDir := t.TempDir()
	saveStack(t, gitDir, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}},
	})

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		LogRangeFn: func(base, head string) ([]git.CommitInfo, error) {
			return nil, nil // empty branch
		},
		HasStagedChangesFn: func() bool { return false },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runAdd(cfg, &addOptions{stageAll: true, message: "msg"}, nil)
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "nothing to commit") {
		t.Errorf("expected 'nothing to commit' error, got: %s", output)
	}
}
