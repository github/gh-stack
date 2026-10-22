package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/checkoutview"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckoutPickerStackLinks(t *testing.T) {
	profile := lipgloss.DefaultRenderer().ColorProfile()
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(profile)

	for _, tt := range []struct {
		name                       string
		number                     int
		empty, failed, missingRepo bool
		force                      string
		wantLink                   bool
	}{
		{name: "matched stacks", number: 7, force: "1", wantLink: true},
		{name: "successful empty listing", number: 7, empty: true, force: "1"},
		{name: "failed listing", number: 7, failed: true, force: "1", wantLink: true},
		{name: "legacy ID match", force: "1", wantLink: true},
		{name: "links disabled", number: 7, force: "0"},
		{name: "missing repository", number: 7, missingRepo: true, force: "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_STACK_HYPERLINKS", tt.force)
			cfg := config.New()
			setTestRepo(cfg)
			cfg.RepoOverride.Host = "github.example.com"
			if tt.missingRepo {
				cfg.RepoOverride.Owner = ""
			}
			calls := 0
			cfg.GitHubClientOverride = &github.MockClient{
				ListStacksFn: func() ([]github.RemoteStack, error) {
					calls++
					if tt.failed {
						return nil, fmt.Errorf("offline")
					}
					if tt.empty {
						return nil, nil
					}
					return []github.RemoteStack{
						{ID: 6154, Number: 7, PRDetails: []github.RemoteStackPR{linkPR(10, "tracked")}},
						{ID: 808, Number: 8, PRDetails: []github.RemoteStackPR{linkPR(11, "remote")}},
					}, nil
				},
				GetStackFn: func(int) (*github.RemoteStack, error) {
					t.Fatal("formatting picker links must not add a lookup")
					return nil, nil
				},
			}
			sf := &stack.StackFile{Stacks: []stack.Stack{
				{Branches: []stack.BranchRef{{Branch: "unsubmitted"}}},
				{ID: "6154", Number: tt.number, Branches: []stack.BranchRef{{Branch: "tracked"}}},
			}}
			rows, _, links := gatherCheckoutRows(cfg, sf)
			m := checkoutview.New(rows).WithNumberLinks(links)
			sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
			output := sized.View()
			if tt.wantLink {
				assert.Contains(t, output, "\x1b]8;;https://github.example.com/o/r/stack/7\x1b\\7\x1b]8;;\x1b\\")
			} else {
				assert.NotContains(t, output, "\x1b]8")
				assert.NotContains(t, output, "https://")
			}
			if !tt.empty && !tt.failed && tt.wantLink {
				assert.Contains(t, output, "/stack/8")
			}
			assert.NotContains(t, output, "/stack/0")
			assert.Equal(t, 1, calls)
			assert.Equal(t, tt.number, sf.Stacks[1].Number, "presentation must not mutate tracking")
		})
	}
}

func TestCheckout_PrintPathRequiresTarget(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cmd := CheckoutCmd(cfg)
	cmd.SetArgs([]string{"--print-path"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	assert.ErrorIs(t, cmd.Execute(), ErrInvalidArgs)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, "explicit")
}

func TestCheckout_PrintPathAmbiguousBranch(t *testing.T) {
	common := t.TempDir()
	writeStackFileMulti(t, common,
		stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "one"}}},
		stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "two"}}},
	)
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	assert.ErrorIs(t, runCheckout(cfg, &checkoutOptions{target: "main", printPath: true}), ErrDisambiguate)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, "multiple stacks")
}

func TestCheckout_RemotePrintPathDoesNotImportDuringRecovery(t *testing.T) {
	t.Setenv("GH_STACK_HYPERLINKS", "1")
	common, root, owner := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(common, rebaseStateFile), []byte(`{"worktrees":{}}`), 0600))
	restore := git.SetOps(&git.MockOps{
		GitDirFn:    func() (string, error) { return common, nil },
		RootDirFn:   func() (string, error) { return root, nil },
		WorktreesFn: func() ([]git.Worktree, error) { return []git.Worktree{{Path: owner, Branch: "remote"}}, nil },
		FetchFn: func(string) error {
			t.Fatal("read-only path resolution must not fetch")
			return nil
		},
		CheckoutBranchFn: func(string) error {
			t.Fatal("read-only path resolution must not check out")
			return nil
		},
	})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{Number: 7, PullRequests: []int{10}}, nil
		},
		FindPRByNumberFn: func(int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 10, BaseRefName: "main", HeadRefName: "remote"}, nil
		},
	}
	require.NoError(t, runCheckout(cfg, &checkoutOptions{target: "7", printPath: true}))
	out, _ := commandOutput(t, cfg, outR, errR)
	assert.Equal(t, owner+"\n", out)
	assert.NoFileExists(t, filepath.Join(common, "gh-stack"))
}

func TestCheckout_RealLinkedWorktreeFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	owner := filepath.Join(t.TempDir(), "linked owner's tree")
	issue250Git(t, root, "init", "-b", "main")
	issue250Git(t, root, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	issue250Git(t, root, "branch", "local")
	issue250Git(t, root, "branch", "unoccupied")
	issue250Git(t, root, "worktree", "add", "-b", "foreign", owner)
	issue250Git(t, root, "checkout", "local")
	subdir := filepath.Join(root, "nested", "directory")
	require.NoError(t, os.MkdirAll(subdir, 0700))
	withIssue250Repo(t, subdir)
	common, err := git.CommonDir()
	require.NoError(t, err)
	writeStackFile(t, common, stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "local"}, {Branch: "foreign"}, {Branch: "unoccupied"}},
	})
	actualRoot, err := git.RootDir()
	require.NoError(t, err)
	actualOwner, err := requireWorktree(t, git.CurrentOps(), owner).RootDir()
	require.NoError(t, err)

	cfg, outR, errR := config.NewTestConfig()
	assert.ErrorIs(t, runCheckout(cfg, &checkoutOptions{target: "foreign"}), ErrInvalidArgs)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	quotedOwner := "'" + strings.ReplaceAll(actualOwner, "'", "'\\''") + "'"
	assert.Contains(t, diagnostics, "\n  cd "+quotedOwner+"\n")
	assert.Equal(t, 1, strings.Count(diagnostics, quotedOwner))
	assert.Equal(t, "local", issue250Git(t, root, "branch", "--show-current"))
	assert.Equal(t, "foreign", issue250Git(t, owner, "branch", "--show-current"))

	cfg, outR, errR = config.NewTestConfig()
	require.NoError(t, runCheckout(cfg, &checkoutOptions{target: "foreign", printPath: true}))
	out, _ = commandOutput(t, cfg, outR, errR)
	assert.Equal(t, actualOwner+"\n", out)
	assert.Equal(t, "local", issue250Git(t, root, "branch", "--show-current"))

	cfg, outR, errR = config.NewTestConfig()
	require.NoError(t, runCheckout(cfg, &checkoutOptions{target: "unoccupied", printPath: true}))
	out, _ = commandOutput(t, cfg, outR, errR)
	assert.Equal(t, actualRoot+"\n", out)
	assert.Equal(t, "unoccupied", issue250Git(t, root, "branch", "--show-current"))
	assert.Equal(t, "foreign", issue250Git(t, owner, "branch", "--show-current"))
}

func TestCheckout_SeparateGitDirMainOwner(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
	root, admin := t.TempDir(), filepath.Join(t.TempDir(), "git administration")
	linked := filepath.Join(t.TempDir(), "linked worktree")
	issue250Git(t, root, "init", "--separate-git-dir", admin, "-b", "main")
	issue250Git(t, root, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	issue250Git(t, root, "worktree", "add", "-b", "linked", linked)
	withIssue250Repo(t, root)
	common, err := git.CommonDir()
	require.NoError(t, err)
	actualRoot, err := git.RootDir()
	require.NoError(t, err)
	writeStackFile(t, common, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "linked"}},
	})

	cfg, outR, errR := config.NewTestConfig()
	require.NoError(t, runTrunkWithPath(cfg, true))
	out, _ := commandOutput(t, cfg, outR, errR)
	assert.Equal(t, actualRoot+"\n", out, "the main worktree can use its observed root")

	withIssue250Repo(t, linked)
	trees, err := git.Worktrees()
	require.NoError(t, err)
	var mainPath string
	for _, tree := range trees {
		if tree.Branch == "main" {
			mainPath = tree.Path
		}
	}
	require.NotEmpty(t, mainPath)
	commonInfo, err := os.Stat(common)
	require.NoError(t, err)
	mainInfo, err := os.Stat(mainPath)
	require.NoError(t, err)
	adminOnly := os.SameFile(commonInfo, mainInfo)

	cfg, outR, errR = config.NewTestConfig()
	err = runTrunkWithPath(cfg, true)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	if adminOnly {
		assert.Error(t, err)
		assert.Empty(t, out, "an administrative directory is not a navigable worktree")
		assert.Contains(t, diagnostics, "separate Git directory")
		assert.Contains(t, diagnostics, "main worktree")
	} else {
		require.NoError(t, err, diagnostics)
		assert.Equal(t, mainPath+"\n", out)
	}
	assert.Equal(t, "main", issue250Git(t, root, "branch", "--show-current"))
	assert.Equal(t, "linked", issue250Git(t, linked, "branch", "--show-current"))

	issue250Git(t, root, "config", "core.worktree", actualRoot)
	cfg, outR, errR = config.NewTestConfig()
	require.NoError(t, runTrunkWithPath(cfg, true))
	out, _ = commandOutput(t, cfg, outR, errR)
	assert.Equal(t, actualRoot+"\n", out, "a configured backlink identifies the main worktree from a linked caller")
	assert.Equal(t, "main", issue250Git(t, root, "branch", "--show-current"))
	assert.Equal(t, "linked", issue250Git(t, linked, "branch", "--show-current"))
}

func TestCheckout_ByBranchName(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			t.Fatal("local branch lookup should not call GitHub")
			return nil, nil
		},
	}
	err := runCheckout(cfg, &checkoutOptions{target: "b2"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b2", checkedOut)
	assert.Contains(t, output, "Switched to b2")
}

func TestCheckout_ByRemoteBranchName(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "named branch", target: "feature"},
		{name: "numeric branch", target: "999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := t.TempDir()
			var checkedOut string
			var createdBranches []string

			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return gitDir, nil },
				CurrentBranchFn: func() (string, error) { return "main", nil },
				BranchExistsFn:  func(name string) (bool, error) { return name == "main", nil },
				FetchFn:         func(string) error { return nil },
				CreateBranchFn: func(name, _ string) error {
					createdBranches = append(createdBranches, name)
					return nil
				},
				SetUpstreamTrackingFn: func(string, string) error { return nil },
				ResolveRemoteFn:       func(string) (string, error) { return "origin", nil },
				CheckoutBranchFn: func(name string) error {
					checkedOut = name
					return nil
				},
				RevParseFn: func(string) (string, error) { return "abc123", nil },
				RevParseMultiFn: func(refs []string) ([]string, error) {
					shas := make([]string, len(refs))
					for i := range refs {
						shas[i] = "abc123"
					}
					return shas, nil
				},
			})
			defer restore()

			require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

			remoteStack := &github.RemoteStack{
				ID:           42,
				Number:       7,
				PullRequests: []int{10, 11, 12},
				PRDetails: []github.RemoteStackPR{
					{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "base-layer"}},
					{Number: 11, State: "open", Head: github.RemoteStackPRHead{Ref: tt.target}},
					{Number: 12, State: "open", Head: github.RemoteStackPRHead{Ref: "top-layer"}},
				},
			}
			var lookedUpPRs []int
			cfg, outR, errR := config.NewTestConfig()
			cfg.GitHubClientOverride = &github.MockClient{
				ListStacksFn: func() ([]github.RemoteStack, error) {
					return []github.RemoteStack{*remoteStack}, nil
				},
				FindStackForPRFn: func(number int) (*github.RemoteStack, error) {
					lookedUpPRs = append(lookedUpPRs, number)
					if number == 11 {
						return remoteStack, nil
					}
					return nil, nil
				},
				FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
					prs := map[int]*github.PullRequest{
						10: {ID: "PR_10", Number: 10, HeadRefName: "base-layer", BaseRefName: "main"},
						11: {ID: "PR_11", Number: 11, HeadRefName: tt.target, BaseRefName: "base-layer"},
						12: {ID: "PR_12", Number: 12, HeadRefName: "top-layer", BaseRefName: tt.target},
					}
					return prs[number], nil
				},
			}

			err := runCheckout(cfg, &checkoutOptions{target: tt.target})
			output := collectOutput(cfg, outR, errR)

			require.NoError(t, err)
			assert.Contains(t, lookedUpPRs, 11)
			assert.Equal(t, []string{"base-layer", tt.target, "top-layer"}, createdBranches)
			assert.Equal(t, tt.target, checkedOut, "should check out the requested layer, not the top branch")
			assert.Contains(t, output, "Imported stack with 3 branches")
			assert.Contains(t, output, "Switched to "+tt.target)
			assert.NotContains(t, output, "not part of a stack")

			sf, loadErr := stack.Load(gitDir)
			require.NoError(t, loadErr)
			require.Len(t, sf.Stacks, 1)
			assert.Equal(t, []string{"base-layer", tt.target, "top-layer"}, sf.Stacks[0].BranchNames())
		})
	}
}

func TestCheckout_ByPRNumber_Local(t *testing.T) {
	// When a PR number exists locally, no API call should be made
	gitDir := t.TempDir()
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// No GitHubClientOverride — should resolve locally without API
	err := runCheckout(cfg, &checkoutOptions{target: "42"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b1", checkedOut)
	assert.Contains(t, output, "Switched to b1")
}

func TestCheckout_AlreadyOnTarget(t *testing.T) {
	gitDir := t.TempDir()
	checkoutCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkoutCalled = true
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{target: "b1"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, checkoutCalled, "CheckoutBranch should not be called when already on target")
	assert.Contains(t, output, "Already on b1")
}

func TestCheckout_NoStacks_NonInteractive(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	// Write an empty stack file (no stacks)
	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	err := runCheckout(cfg, &checkoutOptions{}) // no target arg
	output := collectOutput(cfg, outR, errR)

	assert.Error(t, err)
	assert.Contains(t, output, "no target specified")
}

func TestCheckout_BranchNotFound(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, nil
		},
	}
	err := runCheckout(cfg, &checkoutOptions{target: "nonexistent"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, `no local or remote stack found for "nonexistent"`)
}

func TestCheckout_RemoteBranchName_MultipleStacks(t *testing.T) {
	t.Setenv("GH_STACK_HYPERLINKS", "0")
	gitDir := t.TempDir()
	checkoutCalled := false
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(string) error {
			checkoutCalled = true
			return nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	setTestRepo(cfg)
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{
					Number: 7,
					PRDetails: []github.RemoteStackPR{
						{Number: 10, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
					},
				},
				{
					Number: 8,
					PRDetails: []github.RemoteStackPR{
						{Number: 11, State: "open", Head: github.RemoteStackPRHead{Ref: "feature"}},
					},
				},
			}, nil
		},
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			t.Fatal("ambiguous branch should not select a stack")
			return nil, nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "feature"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrDisambiguate)
	assert.False(t, checkoutCalled)
	assert.Contains(t, output, `branch "feature" belongs to multiple stacks on GitHub (7 (https://github.com/o/r/stack/7), 8 (https://github.com/o/r/stack/8))`)
	assert.Contains(t, output, "stack or PR number")
}

func TestCheckout_RemoteBranchName_APIError(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "feature"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "failed to list stacks: network error")
}

func TestCheckout_RemoteBranchName_StacksUnavailable(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "feature"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "not enabled")
}

// --- Remote checkout tests (numeric target, local miss → API fallback) ---

func TestCheckout_NumericTarget_StacksNotAvailable(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	setTestRepo(cfg)
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "123"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "not enabled")
}

func TestCheckout_NumericTarget_PRNotInStack(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return nil, nil // PR 99 is not part of any stack
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "99"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "PR #99 is not part of a stack")
}

func TestCheckout_NumericTarget_NewStack(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	var createdBranches []string
	var trackingSet []string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) (bool, error) {
			return name == "main", nil // only trunk exists
		},
		FetchFn: func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			createdBranches = append(createdBranches, name)
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error {
			trackingSet = append(trackingSet, branch)
			return nil
		},
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 42, Number: 42, PullRequests: []int{10, 11, 12}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
				12: {ID: "PR_12", Number: 12, HeadRefName: "feat-3", BaseRefName: "feat-2", URL: "https://github.com/o/r/pull/12"},
			}
			pr, ok := prs[number]
			if !ok {
				return nil, fmt.Errorf("PR #%d not found", number)
			}
			return pr, nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)

	// Should create the 3 branches (trunk "main" already exists)
	assert.Equal(t, []string{"feat-1", "feat-2", "feat-3"}, createdBranches)
	assert.Equal(t, []string{"feat-1", "feat-2", "feat-3"}, trackingSet)

	// Should checkout the target PR's branch
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 3 branches")
	assert.Contains(t, output, "Switched to feat-2")

	// Verify stack was saved
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, "42", sf.Stacks[0].ID)
	assert.Equal(t, "main", sf.Stacks[0].Trunk.Branch)
	assert.Len(t, sf.Stacks[0].Branches, 3)
	assert.Equal(t, 10, sf.Stacks[0].Branches[0].PullRequest.Number)
	assert.Equal(t, 11, sf.Stacks[0].Branches[1].PullRequest.Number)
	assert.Equal(t, 12, sf.Stacks[0].Branches[2].PullRequest.Number)
}

func TestCheckout_ByStackNumber(t *testing.T) {
	t.Setenv("GH_STACK_HYPERLINKS", "1")
	gitDir := t.TempDir()
	var checkedOut string
	var createdBranches []string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn:  func(name string) (bool, error) { return name == "main", nil },
		FetchFn:         func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			createdBranches = append(createdBranches, name)
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn:       func(branch string) (string, error) { return "origin", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) { return "abc123", nil },
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	var gotStackNumber int
	cfg, outR, errR := config.NewTestConfig()
	setTestRepo(cfg)
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			gotStackNumber = n
			if n == 7 {
				return &github.RemoteStack{ID: 42, Number: 7, PullRequests: []int{10, 11, 12}}, nil
			}
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
				12: {ID: "PR_12", Number: 12, HeadRefName: "feat-3", BaseRefName: "feat-2", URL: "https://github.com/o/r/pull/12"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "7"})
	require.NoError(t, err)
	require.NoError(t, runCheckout(cfg, &checkoutOptions{target: "7"}))
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 7, gotStackNumber, "should look the stack up by its number")
	// The top-most (last) branch of the stack is checked out.
	assert.Equal(t, "feat-3", checkedOut)
	assert.Contains(t, output, "Imported stack with 3 branches")
	link := "\x1b]8;;https://github.com/o/r/stack/7\x1b\\#7\x1b]8;;\x1b\\"
	assert.Contains(t, output, "from GitHub (stack "+link+")")
	assert.Contains(t, output, "Local stack matches remote (stack "+link+")")
	assert.NotContains(t, output, "/stack/42")

	// Verify the stack was imported with both its internal id and stack number.
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, "42", sf.Stacks[0].ID)
	assert.Equal(t, 7, sf.Stacks[0].Number)
}

func TestCheckout_ByStackNumber_404FallsThroughToPR(t *testing.T) {
	// A 404 from GetStack means no such stack, so the number is tried as a PR.
	gitDir := t.TempDir()
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:              func() (string, error) { return gitDir, nil },
		CurrentBranchFn:       func() (string, error) { return "main", nil },
		BranchExistsFn:        func(name string) (bool, error) { return name == "main", nil },
		FetchFn:               func(string) error { return nil },
		CreateBranchFn:        func(string, string) error { return nil },
		SetUpstreamTrackingFn: func(string, string) error { return nil },
		RevParseFn:            func(string) (string, error) { return "abc123", nil },
		ResolveRemoteFn:       func(string) (string, error) { return "origin", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(int) (*github.RemoteStack, error) {
			return nil, &api.HTTPError{StatusCode: 404, Message: "Not Found"}
		},
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 1, Number: 1, PullRequests: []int{11, 12}}, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "main", URL: "https://github.com/o/r/pull/11"},
				12: {ID: "PR_12", Number: 12, HeadRefName: "feat-3", BaseRefName: "feat-2", URL: "https://github.com/o/r/pull/12"},
			}
			return prs[n], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-2", checkedOut, "the number should resolve as PR #11 after a stack 404")
	assert.Contains(t, output, "Imported stack with 2 branches")
}

func TestCheckout_NumericTarget_BranchExistsNoStack(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string
	var createdBranches []string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) (bool, error) {
			// feat-1 exists locally but feat-2 does not
			return name == "main" || name == "feat-1", nil
		},
		FetchFn: func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			createdBranches = append(createdBranches, name)
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// No stacks exist locally
	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 99, Number: 99, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)

	// Only feat-2 should be created (feat-1 and main already exist)
	assert.Equal(t, []string{"feat-2"}, createdBranches)
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 2 branches")
}

func TestCheckout_NumericTarget_AlreadyInMatchingStack(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// Stack already exists locally with matching PRs
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10, URL: "https://github.com/o/r/pull/10"}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11, URL: "https://github.com/o/r/pull/11"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// PR 10 is found locally → no API call needed
	// No GitHubClientOverride means API calls would panic
	err := runCheckout(cfg, &checkoutOptions{target: "10"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-1", checkedOut)
	assert.Contains(t, output, "Switched to feat-1")
}

func TestCheckout_NumericTarget_LocalMiss_RemoteMatch(t *testing.T) {
	// PR 11 is NOT in any local stack, but IS in a remote stack.
	// The API should be called as a fallback.
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) (bool, error) {
			return name == "main", nil
		},
		FetchFn:               func(remote string) error { return nil },
		CreateBranchFn:        func(name, base string) error { return nil },
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn:       func(branch string) (string, error) { return "origin", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// Local stack has PR 42 only — PR 11 is not tracked
	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "other-branch", PullRequest: &stack.PullRequestRef{Number: 42}},
		},
	})

	apiCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			apiCalled = true
			return &github.RemoteStack{ID: 99, Number: 99, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	_ = collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.True(t, apiCalled, "should have queried the remote stack API when local miss")
	assert.Equal(t, "feat-2", checkedOut)
}

func TestCheckout_NumericTarget_FallbackToBranchName(t *testing.T) {
	// PR 999 is not in any local stack and not in any remote stack,
	// but "999" happens to be a branch name in a local stack
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "999", PullRequest: &stack.PullRequestRef{Number: 50}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return nil, nil // no remote stack contains this PR
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "999"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "999", checkedOut)
	assert.Contains(t, output, "Switched to 999")
	assert.NotContains(t, output, "not part of a stack")
}

func TestCheckout_NumericTarget_CompositionMismatch_NonInteractive(t *testing.T) {
	gitDir := t.TempDir()

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	// Local stack has PRs 10, 11
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			// Remote stack has PRs 10, 11, 12 (extra PR added)
			return &github.RemoteStack{ID: 42, Number: 42, PullRequests: []int{10, 11, 12}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1"},
				12: {ID: "PR_12", Number: 12, HeadRefName: "feat-3", BaseRefName: "feat-2"},
			}
			return prs[number], nil
		},
	}

	// PR 12 not found locally → remote lookup → finds stack → mismatch with local
	err := runCheckout(cfg, &checkoutOptions{target: "12"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, output, "local stack composition differs from remote")
	assert.Contains(t, output, "Local:")
	assert.Contains(t, output, "Remote:")
}

func TestCheckout_NumericTarget_ClosedMergedPR(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) (bool, error) {
			return name == "main", nil
		},
		FetchFn:               func(remote string) error { return nil },
		CreateBranchFn:        func(name, base string) error { return nil },
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 50, Number: 50, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", Merged: true, State: "MERGED", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", State: "OPEN", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 2 branches")

	// Verify merged state is saved
	sf, loadErr := stack.Load(gitDir)
	require.NoError(t, loadErr)
	require.Len(t, sf.Stacks, 1)
	assert.True(t, sf.Stacks[0].Branches[0].PullRequest.Merged)
	assert.False(t, sf.Stacks[0].Branches[1].PullRequest.Merged)
}

func TestCheckout_NumericTarget_MergedBranchDeletedFromRemote(t *testing.T) {
	gitDir := t.TempDir()
	var checkedOut string

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		BranchExistsFn: func(name string) (bool, error) {
			return name == "main", nil
		},
		FetchFn: func(remote string) error { return nil },
		CreateBranchFn: func(name, base string) error {
			// Simulate merged branch deleted from remote: origin/feat-1 doesn't exist
			if base == "origin/feat-1" {
				return fmt.Errorf("failed to run git: fatal: not a valid object name: 'origin/feat-1'")
			}
			return nil
		},
		SetUpstreamTrackingFn: func(branch, remote string) error { return nil },
		ResolveRemoteFn: func(branch string) (string, error) {
			return "origin", nil
		},
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 60, Number: 60, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", Merged: true, State: "MERGED", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", State: "OPEN", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Skipping merged branch feat-1")
	assert.Contains(t, output, "Imported stack with 2 branches")
}

func TestCheckout_NumericTarget_AllPRsMerged(t *testing.T) {
	gitDir := t.TempDir()

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 70, Number: 70, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			prs := map[int]*github.PullRequest{
				10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", Merged: true, State: "MERGED", URL: "https://github.com/o/r/pull/10"},
				11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", Merged: true, State: "MERGED", URL: "https://github.com/o/r/pull/11"},
			}
			return prs[number], nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "11"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrSilent)
	assert.Contains(t, output, "All PRs in this stack have been merged")
	assert.Contains(t, output, "gh stack init")
}

func TestCheckout_NumericTarget_APIError(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "123"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "failed to list stacks")
}

func TestCheckout_NumericTarget_SyncsState(t *testing.T) {
	gitDir := t.TempDir()

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			return nil
		},
		RevParseFn: func(ref string) (string, error) {
			return "abc123", nil
		},
		RevParseMultiFn: func(refs []string) ([]string, error) {
			shas := make([]string, len(refs))
			for i := range refs {
				shas[i] = "abc123"
			}
			return shas, nil
		},
	})
	defer restore()

	// Existing stack with stale PR data — PR 10 found locally
	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10, URL: "old-url"}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11, URL: "old-url"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// PR 10 is found locally → no API call needed, resolved directly
	err := runCheckout(cfg, &checkoutOptions{target: "10"})
	_ = collectOutput(cfg, outR, errR)

	require.NoError(t, err)
}

func TestCheckout_NumericTarget_EmptyStacks(t *testing.T) {
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
	})
	defer restore()

	require.NoError(t, stack.Save(gitDir, &stack.StackFile{SchemaVersion: 1, Stacks: []stack.Stack{}}))

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return nil, nil // no stacks at all
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "123"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "PR #123 is not part of a stack")
}

func TestCheckout_NumericTarget_AlreadyOnTarget(t *testing.T) {
	gitDir := t.TempDir()
	checkoutCalled := false

	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "feat-1", nil },
		CheckoutBranchFn: func(name string) error {
			checkoutCalled = true
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		ID:    "42",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 10, URL: "https://github.com/o/r/pull/10"}},
			{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 11, URL: "https://github.com/o/r/pull/11"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// PR 10 found locally → resolved without API
	err := runCheckout(cfg, &checkoutOptions{target: "10"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.False(t, checkoutCalled, "should not call CheckoutBranch when already on target")
	assert.Contains(t, output, "Already on feat-1")
}

// --- Helper tests ---

func TestCheckout_CompositionConflict_KeepLocal(t *testing.T) {
	for _, tc := range []struct {
		name             string
		remaining        *github.RemoteStack
		apiErr           error
		alreadyUnstacked bool
		wantErr          error
	}{
		{name: "dissolved"},
		{name: "merged", remaining: &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101}}},
		{name: "already unstacked", remaining: &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101}}, alreadyUnstacked: true},
		{name: "queued", remaining: &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{102}}, wantErr: ErrConflict},
		{name: "server rejection", apiErr: &api.HTTPError{StatusCode: 422, Message: "Pull requests cannot be removed"}, wantErr: ErrAPIFailure},
		{name: "already gone", apiErr: &api.HTTPError{StatusCode: 404}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStackFileMulti(t, dir, stack.Stack{
				ID: "9", Number: 9, Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 101, Merged: true}},
					{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 102}},
					{Branch: "b3", PullRequest: &stack.PullRequestRef{Number: 103}},
				},
			}, stack.Stack{
				ID: "20", Number: 20, Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "other"}},
			})
			sf, err := stack.Load(dir)
			require.NoError(t, err)
			local := &sf.Stacks[0]
			remote := &github.RemoteStack{ID: 9, Number: 9, PullRequests: []int{101, 102, 104}}
			if tc.alreadyUnstacked {
				remote = tc.remaining
			}
			unstacked := false
			client := &github.MockClient{
				UnstackFn: func(number int) (*github.RemoteStack, bool, error) {
					assert.Equal(t, 9, number)
					unstacked = true
					return tc.remaining, tc.remaining == nil, tc.apiErr
				},
			}
			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = true
			cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 1, nil }
			prs := []*github.PullRequest{{HeadRefName: "b1"}, {HeadRefName: "b2"}, {HeadRefName: "b4"}}
			result, err := handleCompositionConflict(cfg, client, sf, local, remote, prs, dir, "main")
			_, output := commandOutput(t, cfg, outR, errR)
			assert.Equal(t, !tc.alreadyUnstacked, unstacked)
			saved, loadErr := stack.Load(dir)
			require.NoError(t, loadErr)
			require.Len(t, saved.Stacks, 2)
			assert.Equal(t, []string{"b1", "b2", "b3"}, saved.Stacks[0].BranchNames())
			assert.Equal(t, "20", saved.Stacks[1].ID)
			assert.Equal(t, []string{"other"}, saved.Stacks[1].BranchNames())
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, result)
				assert.Equal(t, "9", saved.Stacks[0].ID)
				assert.Equal(t, 9, saved.Stacks[0].Number)
			} else {
				require.NoError(t, err)
				assert.Same(t, local, result)
				assert.Empty(t, saved.Stacks[0].ID)
				assert.Zero(t, saved.Stacks[0].Number)
				if tc.remaining != nil {
					assert.Contains(t, output, "remain stacked on GitHub")
					assert.NotContains(t, output, "Remote stack removed")
					assert.NotContains(t, output, "could not be unstacked")
				}
			}
		})
	}
}

func TestStackCompositionMatches(t *testing.T) {
	tests := []struct {
		name    string
		local   *stack.Stack
		remote  []int
		matches bool
	}{
		{
			name: "exact match",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10}},
					{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 11}},
				},
			},
			remote:  []int{10, 11},
			matches: true,
		},
		{
			name: "merged history from a previous stack",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
					{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 11}},
				},
			},
			remote:  []int{11},
			matches: true,
		},
		{
			name: "merged member of the current stack",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10, Merged: true}},
					{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 11}},
				},
			},
			remote:  []int{10, 11},
			matches: true,
		},
		{
			name: "different order",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 11}},
					{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 10}},
				},
			},
			remote:  []int{10, 11},
			matches: false,
		},
		{
			name: "remote has more",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10}},
				},
			},
			remote:  []int{10, 11},
			matches: false,
		},
		{
			name: "local has branch without PR",
			local: &stack.Stack{
				Branches: []stack.BranchRef{
					{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 10}},
					{Branch: "b"}, // no PR
				},
			},
			remote:  []int{10, 11},
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stackCompositionMatches(tt.local, tt.remote)
			assert.Equal(t, tt.matches, result)
		})
	}
}

func TestCheckout_ByPRURL_Local(t *testing.T) {
	// When a PR URL resolves to a locally tracked stack, no API call needed
	gitDir := t.TempDir()
	var checkedOut string
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
		},
	})

	cfg, outR, errR := config.NewTestConfig()
	// No GitHubClientOverride — should resolve locally without API
	err := runCheckout(cfg, &checkoutOptions{target: "https://github.com/o/r/pull/42"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "b1", checkedOut)
	assert.Contains(t, output, "Switched to b1")
}

func TestCheckout_ByPRURL_Remote(t *testing.T) {
	// When a PR URL is not tracked locally, fall back to remote API
	gitDir := t.TempDir()
	var checkedOut string

	prDB := map[int]*github.PullRequest{
		10: {ID: "PR_10", Number: 10, HeadRefName: "feat-1", BaseRefName: "main", URL: "https://github.com/o/r/pull/10"},
		11: {ID: "PR_11", Number: 11, HeadRefName: "feat-2", BaseRefName: "feat-1", URL: "https://github.com/o/r/pull/11"},
	}

	restore := git.SetOps(&git.MockOps{
		GitDirFn:              func() (string, error) { return gitDir, nil },
		CurrentBranchFn:       func() (string, error) { return "main", nil },
		BranchExistsFn:        func(name string) (bool, error) { return name == "main", nil },
		FetchFn:               func(string) error { return nil },
		CreateBranchFn:        func(string, string) error { return nil },
		SetUpstreamTrackingFn: func(string, string) error { return nil },
		RevParseFn:            func(string) (string, error) { return "abc123", nil },
		ResolveRemoteFn:       func(string) (string, error) { return "origin", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = name
			return nil
		},
	})
	defer restore()

	// Empty stack file — nothing local
	writeStackFile(t, gitDir, stack.Stack{})

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindStackForPRFn: func(int) (*github.RemoteStack, error) {
			return &github.RemoteStack{ID: 1, Number: 1, PullRequests: []int{10, 11}}, nil
		},
		FindPRByNumberFn: func(n int) (*github.PullRequest, error) {
			if pr, ok := prDB[n]; ok {
				return pr, nil
			}
			return nil, nil
		},
	}

	err := runCheckout(cfg, &checkoutOptions{target: "https://github.com/o/r/pull/11"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "feat-2", checkedOut)
	assert.Contains(t, output, "Imported stack with 2 branches")
}
