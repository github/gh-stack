package git

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cligit "github.com/cli/cli/v2/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWorktrees(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   []Worktree
		errMsg string
	}{
		{name: "empty"},
		{
			name:  "main and linked",
			input: "worktree /main\x00HEAD abc\x00branch refs/heads/main\x00\x00worktree /linked\x00HEAD def\x00branch refs/heads/feature\x00\x00",
			want: []Worktree{
				{Path: "/main", Branch: "main"},
				{Path: "/linked", Branch: "feature"},
			},
		},
		{
			name:  "bare and detached",
			input: "worktree /bare.git\x00bare\x00\x00worktree /detached\x00HEAD abc\x00detached\x00\x00",
			want: []Worktree{
				{Path: "/bare.git", Bare: true},
				{Path: "/detached", Detached: true},
			},
		},
		{
			name:  "unquoted unusual paths and reasons",
			input: "worktree /a \"quoted\" \u03bb\tpath\nwith\nnewlines \x00HEAD abc\x00branch refs/heads/feature-\u03bb\x00locked reason\nworktree /not-a-record\x00prunable reason\nmore text\x00\x00",
			want: []Worktree{{
				Path: "/a \"quoted\" \u03bb\tpath\nwith\nnewlines ", Branch: "feature-\u03bb",
				Locked: true, Prunable: true,
			}},
		},
		{
			name:  "boolean attributes without reasons",
			input: "worktree /linked\x00HEAD abc\x00detached\x00locked\x00prunable\x00\x00",
			want:  []Worktree{{Path: "/linked", Detached: true, Locked: true, Prunable: true}},
		},
		{
			name:  "unknown attributes remain forward compatible",
			input: "worktree /main\x00new-attribute arbitrary value\x00branch refs/heads/main\x00\x00",
			want:  []Worktree{{Path: "/main", Branch: "main"}},
		},
		{
			name:  "long path is not scanner limited",
			input: "worktree /" + strings.Repeat("a", 100000) + "\x00bare\x00\x00",
			want:  []Worktree{{Path: "/" + strings.Repeat("a", 100000), Bare: true}},
		},
		{name: "newline porcelain is rejected", input: "worktree /main\nbranch refs/heads/main\n\n", errMsg: "missing NUL"},
		{name: "missing path", input: "worktree \x00\x00", errMsg: "path"},
		{name: "attribute before path", input: "HEAD abc\x00\x00", errMsg: "precedes worktree"},
		{name: "missing record separator", input: "worktree /main\x00worktree /linked\x00\x00", errMsg: "record separator"},
		{name: "truncated record", input: "worktree /main\x00branch refs/heads/main\x00", errMsg: "unterminated"},
		{name: "nonbranch ref", input: "worktree /main\x00branch refs/tags/tag\x00\x00", errMsg: "branch"},
		{name: "empty branch", input: "worktree /main\x00branch refs/heads/\x00\x00", errMsg: "branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorktrees(tt.input)
			if tt.errMsg != "" {
				require.ErrorContains(t, err, tt.errMsg)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckGitVersion(t *testing.T) {
	tests := []struct {
		version string
		errMsg  string
	}{
		{version: "git version 2.36.0"},
		{version: "git version 2.36"},
		{version: "git version 2.50.1 (Apple Git-155)"},
		{version: "git version 2.40.0.windows.1"},
		{version: "git version 2.36.0-rc2"},
		{version: "git version 3.0.0"},
		{version: "git version 2.35.99", errMsg: "upgrade Git"},
		{version: "git version 1.99.99", errMsg: "upgrade Git"},
		{version: "", errMsg: "cannot parse"},
		{version: "git version ", errMsg: "cannot parse"},
		{version: "2.36.0", errMsg: "cannot parse"},
		{version: "git version unknown", errMsg: "cannot parse"},
		{version: "git version 2.-36.0", errMsg: "cannot parse"},
		{version: "git version two.36.0", errMsg: "cannot parse"},
		{version: "git version 2.thirty-six.0", errMsg: "cannot parse"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			err := checkGitVersion(tt.version)
			if tt.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.errMsg)
			assert.Contains(t, err.Error(), "2.36")
			assert.Contains(t, err.Error(), "git --version")
		})
	}
}

func TestCheckVersionWithoutRepository(t *testing.T) {
	d := &defaultOps{client: &cligit.Client{RepoDir: t.TempDir()}}
	require.NoError(t, d.CheckVersion())

	d.client.GitPath = filepath.Join(t.TempDir(), "missing-git")
	err := d.CheckVersion()
	require.ErrorContains(t, err, "cannot determine Git version")
	assert.Contains(t, err.Error(), "install Git 2.36 or newer")
	assert.Contains(t, err.Error(), "PATH")
}

func TestMockWorktreeDefaults(t *testing.T) {
	m := &MockOps{GitDirFn: func() (string, error) { return "/fixture/git", nil }}
	common, err := m.CommonDir()
	require.NoError(t, err)
	assert.Equal(t, "/fixture/git", common)
	scoped, err := m.ForWorktree("/fixture/linked")
	require.NoError(t, err)
	assert.Same(t, m, scoped)
	worktrees, err := m.Worktrees()
	require.NoError(t, err)
	assert.Nil(t, worktrees)
	require.NoError(t, m.CheckVersion())

	wantErr := errors.New("fixture error")
	m.GitDirFn = func() (string, error) { return "", wantErr }
	_, err = m.CommonDir()
	require.ErrorIs(t, err, wantErr)
}

func TestWorktreeWrappersDelegate(t *testing.T) {
	wantErr := errors.New("hook error")
	wantWorktrees := []Worktree{{Path: "/main", Branch: "main"}}
	child := &MockOps{}
	m := &MockOps{
		CommonDirFn: func() (string, error) { return "/common", wantErr },
		WorktreesFn: func() ([]Worktree, error) { return wantWorktrees, wantErr },
		ForWorktreeFn: func(path string) (Ops, error) {
			assert.Equal(t, "/linked", path)
			return child, nil
		},
		CheckVersionFn: func() error { return wantErr },
	}
	restore := SetOps(m)
	defer restore()

	common, err := CommonDir()
	assert.Equal(t, "/common", common)
	require.ErrorIs(t, err, wantErr)
	worktrees, err := Worktrees()
	assert.Equal(t, wantWorktrees, worktrees)
	require.ErrorIs(t, err, wantErr)
	scoped, err := ForWorktree("/linked")
	require.NoError(t, err)
	assert.Same(t, child, scoped)
	m.ForWorktreeFn = func(string) (Ops, error) { return nil, wantErr }
	scoped, err = ForWorktree("/linked")
	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, scoped)
	require.ErrorIs(t, CheckVersion(), wantErr)
	assert.Same(t, m, CurrentOps())
}

func TestStateQueryWrappersDelegateErrors(t *testing.T) {
	wantErr := errors.New("state lookup failed")
	m := &MockOps{
		BranchExistsFn: func(name string) (bool, error) {
			assert.Equal(t, "feature", name)
			return false, wantErr
		},
		HasStagedChangesFn:       func() (bool, error) { return false, wantErr },
		IsRebaseInProgressFn:     func() (bool, error) { return false, wantErr },
		IsCherryPickInProgressFn: func() (bool, error) { return false, wantErr },
	}
	restore := SetOps(m)
	defer restore()
	for name, query := range map[string]func() (bool, error){
		"branch":      func() (bool, error) { return BranchExists("feature") },
		"staged":      HasStagedChanges,
		"rebase":      IsRebaseInProgress,
		"cherry-pick": IsCherryPickInProgress,
	} {
		t.Run(name, func(t *testing.T) {
			value, err := query()
			require.ErrorIs(t, err, wantErr)
			assert.False(t, value)
		})
	}
}

func TestRebaseArgs(t *testing.T) {
	for _, date := range []bool{false, true} {
		args := rebaseArgs(RebaseOpts{CommitterDateIsAuthorDate: date})
		want := []string{"-c", "rebase.updateRefs=false", "-c", "rebase.autoStash=false", "rebase"}
		if date {
			want = append(want, "--merge", "--committer-date-is-author-date")
		}
		assert.Equal(t, want, args)
	}
}

func TestScopedCommandEnvironment(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"GIT_DIR=/caller/git",
		"Git_Work_Tree=/caller",
		"git_index_file=/caller/index",
		"GIT_OBJECT_DIRECTORY=/caller/objects",
		"GIT_CONFIG=/caller/config",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=rebase.autoStash",
		"GIT_CONFIG_VALUE_0=true",
		"GIT_EDITOR=false",
	}
	for _, scoped := range []bool{false, true} {
		d := &defaultOps{scoped: scoped}
		cmd := &cligit.Command{Cmd: &exec.Cmd{Env: append([]string(nil), env...)}}
		d.configureCommand(cmd)
		if !scoped {
			assert.Equal(t, env, cmd.Env)
			continue
		}
		assert.Subset(t, cmd.Env, []string{
			"PATH=/bin",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=rebase.autoStash",
			"GIT_CONFIG_VALUE_0=true",
			"GIT_EDITOR=false",
		})
		for _, entry := range env[1:6] {
			assert.NotContains(t, cmd.Env, entry)
		}
	}
}
