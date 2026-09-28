package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNavigation_PrintPath(t *testing.T) {
	commands := []struct {
		name, current, target string
		command               func(*config.Config) *cobra.Command
		args                  []string
	}{
		{"up", "b1", "b2", UpCmd, nil},
		{"down", "b2", "b1", DownCmd, nil},
		{"top", "b1", "b2", TopCmd, nil},
		{"bottom", "b2", "b1", BottomCmd, nil},
		{"trunk", "b1", "main", TrunkCmd, nil},
		{"checkout", "b1", "b2", CheckoutCmd, []string{"b2"}},
		{"already top", "b2", "b2", TopCmd, nil},
		{"already bottom", "b1", "b1", BottomCmd, nil},
		{"already trunk", "main", "main", TrunkCmd, nil},
		{"clamped up", "b2", "b2", UpCmd, nil},
		{"clamped down", "b1", "b1", DownCmd, nil},
	}
	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			for _, foreign := range []bool{false, true} {
				if foreign && tt.current == tt.target {
					continue
				}
				t.Run(strconv.FormatBool(foreign), func(t *testing.T) {
					common, root, owner := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "other tree")
					writeStackFile(t, common, stack.Stack{
						Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
					})
					var checkouts []string
					restore := git.SetOps(&git.MockOps{
						GitDirFn:        func() (string, error) { return common, nil },
						RootDirFn:       func() (string, error) { return root, nil },
						CurrentBranchFn: func() (string, error) { return tt.current, nil },
						BranchExistsFn:  func(string) (bool, error) { return true, nil },
						WorktreesFn: func() ([]git.Worktree, error) {
							if foreign {
								return []git.Worktree{{Path: owner, Branch: tt.target}}, nil
							}
							return nil, nil
						},
						CheckoutBranchFn: func(branch string) error {
							checkouts = append(checkouts, branch)
							return nil
						},
					})
					defer restore()
					cfg, outR, errR := config.NewTestConfig()
					cfg.ForceInteractive = true
					cfg.SelectFn = func(string, string, []string) (int, error) {
						t.Fatal("path mode must not prompt")
						return 0, nil
					}
					cmd := tt.command(cfg)
					cmd.SetArgs(append(append([]string{}, tt.args...), "--print-path"))
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					require.NoError(t, cmd.Execute())
					out, _ := commandOutput(t, cfg, outR, errR)
					want := root
					if foreign {
						want = owner
					}
					assert.Equal(t, want+"\n", out)
					assert.True(t, cfg.ForceInteractive)
					assert.False(t, cfg.NonInteractive, "the caller's config must not be mutated")
					if foreign || tt.current == tt.target {
						assert.Empty(t, checkouts)
					} else {
						assert.Equal(t, []string{tt.target}, checkouts)
					}
				})
			}
		})
	}
}

func TestNavigation_PrintPathRejectsAmbiguity(t *testing.T) {
	for i, constructor := range []func(*config.Config) *cobra.Command{UpCmd, DownCmd, TopCmd, BottomCmd, TrunkCmd} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			common := t.TempDir()
			writeStackFileMulti(t, common,
				stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "one"}}},
				stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "two"}}},
			)
			restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			cfg.ForceInteractive = true
			cmd := constructor(cfg)
			cmd.SetArgs([]string{"--print-path"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.ErrorIs(t, err, ErrDisambiguate, cmd.Name())
			assert.Empty(t, out)
			assert.Contains(t, diagnostics, "multiple stacks")
		})
	}
}

// readCfgOutput closes cfg writers and reads all captured output.
func readCfgOutput(cfg *config.Config, outR, errR *os.File) string {
	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(errR)
	return string(out) + string(errOut)
}

func TestNavigate_UpOne(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := UpCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b2"}, checkedOut)
}

func TestNavigate_UpN(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := UpCmd(cfg)
	cmd.SetArgs([]string{"2"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b3"}, checkedOut)
}

func TestNavigate_DownOne(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b3", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := DownCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b2"}, checkedOut)
}

func TestNavigate_AtTopClamps(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cmd := UpCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(errR)
	output := string(out) + string(errOut)

	assert.NoError(t, err)
	assert.Empty(t, checkedOut, "should not checkout any branch")
	assert.Contains(t, output, "Already at the top")
}

func TestNavigate_AtBottomClamps(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cmd := DownCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(errR)
	output := string(out) + string(errOut)

	assert.NoError(t, err)
	assert.Empty(t, checkedOut, "should not checkout any branch")
	assert.Contains(t, output, "Already at the bottom")
}

func TestNavigate_FromTrunkGoesUp(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "main", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := UpCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1"}, checkedOut)
}

func TestNavigate_SkipsMergedBranches(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
			{Branch: "b3"},
		},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cmd := UpCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	cfg.Out.Close()
	cfg.Err.Close()
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(errR)
	output := string(out) + string(errOut)

	assert.NoError(t, err)
	assert.Equal(t, []string{"b3"}, checkedOut, "should skip merged b2")
	assert.Contains(t, output, "Skipped")
}

func TestNavigate_Top(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b1", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := TopCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b3"}, checkedOut)
}

func TestNavigate_Bottom(t *testing.T) {
	s := stack.Stack{
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}, {Branch: "b3"}},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b3", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := BottomCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b1"}, checkedOut)
}

func TestNavigate_BottomWithMergedFirst(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2"},
			{Branch: "b3"},
		},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b3", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	cmd := BottomCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	assert.NoError(t, err)
	assert.Equal(t, []string{"b2"}, checkedOut, "should skip merged b1")
}

func TestNavigate_AllMerged_Up(t *testing.T) {
	s := stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	}

	var checkedOut []string
	tmpDir := t.TempDir()
	writeStackFile(t, tmpDir, s)

	mock := &git.MockOps{
		GitDirFn:        func() (string, error) { return tmpDir, nil },
		CurrentBranchFn: func() (string, error) { return "b2", nil },
		CheckoutBranchFn: func(name string) error {
			checkedOut = append(checkedOut, name)
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	cmd := UpCmd(cfg)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()

	output := readCfgOutput(cfg, outR, errR)

	assert.NoError(t, err)
	assert.Empty(t, checkedOut, "should not checkout when already at top of all-merged stack")
	assert.Contains(t, output, "Already at the top")
	// On a merged branch, navigate prints a warning before the at-top message
	assert.Contains(t, output, "you are on merged branch")
}

// writeStackFile is a helper to write a stack file to a temp dir.
func writeStackFile(t *testing.T, dir string, s stack.Stack) {
	t.Helper()
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks:        []stack.Stack{s},
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh-stack"), data, 0644))
}
