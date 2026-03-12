package cmd

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
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

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error in output: %s", output)
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		t.Fatalf("loading stack: %v", err)
	}
	if len(sf.Stacks) != 1 {
		t.Fatalf("got %d stacks, want 1", len(sf.Stacks))
	}
	s := sf.Stacks[0]
	if s.Trunk.Branch != "main" {
		t.Errorf("trunk = %q, want %q", s.Trunk.Branch, "main")
	}
	names := s.BranchNames()
	if len(names) != 1 || names[0] != "myBranch" {
		t.Errorf("branches = %v, want [myBranch]", names)
	}
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

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		t.Fatalf("loading stack: %v", err)
	}
	if got := sf.Stacks[0].Trunk.Branch; got != "develop" {
		t.Errorf("trunk = %q, want %q", got, "develop")
	}
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

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		t.Fatalf("loading stack: %v", err)
	}
	names := sf.Stacks[0].BranchNames()
	want := []string{"b1", "b2", "b3"}
	if len(names) != len(want) {
		t.Fatalf("branches = %v, want %v", names, want)
	}
	for i, name := range names {
		if name != want[i] {
			t.Errorf("branch[%d] = %q, want %q", i, name, want[i])
		}
	}
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
	if err != nil {
		t.Fatalf("loading stack: %v", err)
	}
	if got := sf.Stacks[0].Prefix; got != "feat" {
		t.Errorf("prefix = %q, want %q", got, "feat")
	}
}

func TestInit_EnablesRerere(t *testing.T) {
	gitDir := t.TempDir()
	rerereCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		EnableRerereFn: func() error {
			rerereCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"b1"}})
	collectOutput(cfg, outR, errR)

	if !rerereCalled {
		t.Error("expected EnableRerere to be called")
	}
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
	if err := stack.Save(gitDir, sf); err != nil {
		t.Fatalf("saving seed stack: %v", err)
	}

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		DefaultBranchFn: func() (string, error) { return "main", nil },
		CurrentBranchFn: func() (string, error) { return "feature-1", nil },
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	runInit(cfg, &initOptions{branches: []string{"newBranch"}})
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "already part of a stack") {
		t.Errorf("expected 'already part of a stack' error, got: %s", output)
	}
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

	if !strings.Contains(output, "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %s", output)
	}
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

	if strings.Contains(output, "\u2717") {
		t.Fatalf("unexpected error: %s", output)
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		t.Fatalf("loading stack: %v", err)
	}
	names := sf.Stacks[0].BranchNames()
	if len(names) != 3 {
		t.Fatalf("got %d branches, want 3: %v", len(names), names)
	}
	for i, want := range []string{"b1", "b2", "b3"} {
		if names[i] != want {
			t.Errorf("branch[%d] = %q, want %q", i, names[i], want)
		}
	}
}
