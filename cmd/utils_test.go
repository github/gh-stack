package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func commandOutput(t *testing.T, cfg *config.Config, outR, errR *os.File) (string, string) {
	t.Helper()
	require.NoError(t, cfg.Out.Close())
	require.NoError(t, cfg.Err.Close())
	defer outR.Close()
	defer errR.Close()
	out, err := io.ReadAll(outR)
	require.NoError(t, err)
	diagnostics, err := io.ReadAll(errR)
	require.NoError(t, err)
	return string(out), string(diagnostics)
}

func requireWorktree(t *testing.T, parent git.Ops, path string) git.Ops {
	t.Helper()
	ops, err := parent.ForWorktree(path)
	require.NoError(t, err)
	require.NotNil(t, ops)
	return ops
}

func requireGitState(t *testing.T, query func() (bool, error)) bool {
	t.Helper()
	state, err := query()
	require.NoError(t, err)
	return state
}

func mockRemoteOnlyGit() func() {
	return git.SetOps(&git.MockOps{
		GitDirFn: func() (string, error) { return "", errors.New("not a git repository") },
	})
}

func TestResolveStack_ReadOnlySelectionDoesNotCheckout(t *testing.T) {
	sf := &stack.StackFile{Stacks: []stack.Stack{
		{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "one"}}},
		{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "two"}}},
	}}
	restore := git.SetOps(&git.MockOps{CheckoutBranchFn: func(string) error {
		t.Fatal("read-only stack selection must not acquire a mutation or change checkout")
		return nil
	}})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	cfg.ForceInteractive = true
	cfg.SelectFn = func(_, _ string, options []string) (int, error) {
		require.Len(t, options, 2)
		return 1, nil
	}

	selected, err := resolveStack(sf, "main", cfg)

	require.NoError(t, err)
	assert.Same(t, &sf.Stacks[1], selected)
	commandOutput(t, cfg, outR, errR)
}

func TestResolveStack_RewriteSelectionPreservesCheckout(t *testing.T) {
	for _, kind := range []string{"rebase", "rebase-continue", "rebase-abort", "sync", "modify", "modify-continue", "modify-abort", "add", "checkout", "submit"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			writeStackFileMulti(t, dir,
				stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "b1"}, {Branch: "b2"}}},
				stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "independent"}}},
			)
			current := "main"
			var checkouts []string
			mock := newRebaseMock(dir, current)
			mock.CurrentBranchFn = func() (string, error) { return current, nil }
			mock.CheckoutBranchFn = func(branch string) error {
				checkouts = append(checkouts, branch)
				current = branch
				return nil
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg := issue250TestConfig(t)
			cfg.ForceInteractive = true
			cfg.SelectFn = func(_, _ string, choices []string) (int, error) {
				require.Len(t, choices, 2)
				return 0, nil
			}
			release, err := beginStackMutation(cfg, kind)
			require.NoError(t, err)
			defer release()

			result, err := loadStackOptional(cfg, "")

			require.NoError(t, err)
			assert.Equal(t, []string{"b1", "b2"}, result.Stack.BranchNames())
			if kind == "add" || kind == "checkout" || kind == "submit" {
				assert.Equal(t, []string{"b2"}, checkouts, "intentional selection checkout must remain available")
				assert.Equal(t, "b2", result.CurrentBranch)
			} else {
				assert.Empty(t, checkouts, "rewrite selection must not move the origin before preflight")
				assert.Equal(t, "main", result.CurrentBranch)
			}
			assert.Equal(t, result.CurrentBranch, current)
		})
	}
}

func TestStackSelection_RewritePreflightPreservesCheckout(t *testing.T) {
	for _, command := range []struct {
		name string
		run  func(*config.Config) error
	}{
		{"rebase", func(cfg *config.Config) error { return runRebase(cfg, &rebaseOptions{remote: "origin"}) }},
		{"sync", func(cfg *config.Config) error { return runSync(cfg, &syncOptions{remote: "origin"}) }},
		{"modify", runModify},
	} {
		t.Run(command.name, func(t *testing.T) {
			repo := setupSharedTrunkRebaseRepo(t, false)
			issue250WriteFile(t, repo.parentDir, "unfinished.txt", "preserve this work\n")
			beforeRefs := issue250Git(t, repo.dir, "show-ref")
			beforeCatalog, err := os.ReadFile(filepath.Join(repo.gitDir, "gh-stack"))
			require.NoError(t, err)
			withIssue250Repo(t, repo.dir)
			cfg := issue250TestConfig(t)
			cfg.ForceInteractive = true
			cfg.SelectFn = func(_, _ string, _ []string) (int, error) { return 0, nil }
			cfg.ConfirmFn = func(string, bool) (bool, error) { return false, nil }

			require.Error(t, command.run(cfg))

			assert.Equal(t, "main", issue250Git(t, repo.dir, "branch", "--show-current"))
			assert.Equal(t, "parent", issue250Git(t, repo.parentDir, "branch", "--show-current"))
			assert.Equal(t, beforeRefs, issue250Git(t, repo.dir, "show-ref"))
			afterCatalog, err := os.ReadFile(filepath.Join(repo.gitDir, "gh-stack"))
			require.NoError(t, err)
			assert.Equal(t, beforeCatalog, afterCatalog)
			assert.FileExists(t, filepath.Join(repo.parentDir, "unfinished.txt"))
			assert.NoFileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
		})
	}
}

func TestStackMutation_NestedAndReadOnly(t *testing.T) {
	common := t.TempDir()
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()

	release, err := beginStackMutation(cfg, "add")
	require.NoError(t, err)
	defer release()
	state := cfg.StackMutation
	nested, err := beginStackMutation(cfg, "init")
	require.NoError(t, err)
	nested()
	assert.Same(t, state, cfg.StackMutation)
	dir, err := stackStateDir(cfg)
	require.NoError(t, err)
	assert.Equal(t, common, dir)

	lock, acquired, err := stack.TryLockOperation(common)
	require.NoError(t, err)
	if lock != nil {
		defer lock.Unlock()
	}
	assert.False(t, acquired)

	reader := *cfg
	reader.StackMutation = nil
	dir, err = stackStateDir(&reader)
	require.NoError(t, err, "a reader must not wait on the active operation lock")
	assert.Equal(t, common, dir)

	release()
	assert.Nil(t, cfg.StackMutation)
	lock, acquired, err = stack.TryLockOperation(common)
	require.NoError(t, err)
	require.True(t, acquired)
	lock.Unlock()
	out, _ := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
}

func TestStackMutation_RecoveryGuards(t *testing.T) {
	tests := []struct {
		name, file, data, kind string
		want                   error
	}{
		{"common rebase", "gh-stack-rebase-state", `{"worktrees":{}}`, "submit", ErrRebaseActive},
		{"common rebase continue", "gh-stack-rebase-state", `{"worktrees":{}}`, "rebase-continue", nil},
		{"common rebase abort", "gh-stack-rebase-state", `{"worktrees":{}}`, "rebase-abort", nil},
		{"modify applying", "gh-stack-modify-state", `{"worktrees":{},"phase":"applying"}`, "push", ErrModifyRecovery},
		{"modify conflict", "gh-stack-modify-state", `{"worktrees":{},"phase":"conflict"}`, "link", ErrModifyRecovery},
		{"modify recovery", "gh-stack-modify-state", `{"worktrees":{},"phase":"conflict"}`, "modify-continue", nil},
		{"pending does not block", "gh-stack-modify-state", `{"worktrees":{},"phase":"pending_submit"}`, "init", nil},
		{"corrupt rebase", "gh-stack-rebase-state", `{`, "rebase-abort", ErrRebaseActive},
		{"corrupt modify", "gh-stack-modify-state", `{`, "submit", ErrModifyRecovery},
		{"invalid pending snapshot", "gh-stack-modify-state", `{"phase":"pending_submit","snapshot":"invalid"}`, "push", ErrModifyRecovery},
		{"unknown modify phase", "gh-stack-modify-state", `{"phase":"unknown"}`, "push", ErrModifyRecovery},
		{"null rebase", "gh-stack-rebase-state", `null`, "push", ErrRebaseActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common, local := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(common, tt.file), []byte(tt.data), 0600))
			restore := git.SetOps(&git.MockOps{
				GitDirFn:    func() (string, error) { return local, nil },
				CommonDirFn: func() (string, error) { return common, nil },
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			release, err := beginStackMutation(cfg, tt.kind)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
				assert.Nil(t, cfg.StackMutation)
			} else {
				require.NoError(t, err)
				assert.Equal(t, common, cfg.StackMutation.StateDir)
				release()
			}
			out, _ := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
		})
	}
}

func TestStackMutation_LegacyRecoveryUsesOriginalCatalog(t *testing.T) {
	for _, original := range []bool{true, false} {
		t.Run(fmt.Sprintf("original=%t", original), func(t *testing.T) {
			common := t.TempDir()
			legacy := filepath.Join(common, "worktrees", "original")
			require.NoError(t, os.MkdirAll(legacy, 0700))
			writeStackFile(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "shared"}}})
			writeStackFile(t, legacy, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "original"}}})
			require.NoError(t, os.WriteFile(filepath.Join(legacy, rebaseStateFile), []byte(`{"originalBranch":"original"}`), 0600))
			local := common
			if original {
				local = legacy
			}
			restore := git.SetOps(&git.MockOps{
				GitDirFn:    func() (string, error) { return local, nil },
				CommonDirFn: func() (string, error) { return common, nil },
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			release, err := beginStackMutation(cfg, "rebase-abort")
			if original {
				require.NoError(t, err)
				defer release()
				dir, err := stackStateDir(cfg)
				require.NoError(t, err)
				assert.Equal(t, legacy, dir)
				sf, err := stack.Load(dir)
				require.NoError(t, err)
				assert.Equal(t, []string{"original"}, sf.Stacks[0].BranchNames())
			} else {
				require.ErrorIs(t, err, ErrRebaseActive)
			}
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			if !original {
				assert.Contains(t, diagnostics, "original worktree")
				assert.Contains(t, diagnostics, legacy)
			}
		})
	}
}

func TestStackMutation_UpgradedPrivateJournalUsesOriginalCatalog(t *testing.T) {
	for _, operation := range []string{"rebase", "modify"} {
		for _, action := range []string{"continue", "abort"} {
			for _, original := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s-%s/original=%t", operation, action, original), func(t *testing.T) {
					common := t.TempDir()
					private := filepath.Join(common, "worktrees", "original")
					require.NoError(t, os.MkdirAll(private, 0700))
					writeStackFile(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "shared"}}})
					writeStackFile(t, private, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "original"}}})
					journal := filepath.Join(private, "gh-stack-"+operation+"-state")
					require.NoError(t, os.WriteFile(journal, []byte(`{"phase":"conflict","worktrees":{}}`), 0600))
					local := common
					if original {
						local = private
					}
					restore := git.SetOps(&git.MockOps{
						GitDirFn:    func() (string, error) { return local, nil },
						CommonDirFn: func() (string, error) { return common, nil },
					})
					defer restore()
					cfg, outR, errR := config.NewTestConfig()
					release, err := beginStackMutation(cfg, operation+"-"+action)
					if original {
						require.NoError(t, err)
						defer release()
						dir, err := stackStateDir(cfg)
						require.NoError(t, err)
						assert.Equal(t, private, dir)
						sf, err := stack.Load(dir)
						require.NoError(t, err)
						assert.Equal(t, []string{"original"}, sf.Stacks[0].BranchNames())
					} else if operation == "rebase" {
						assert.ErrorIs(t, err, ErrRebaseActive)
					} else {
						assert.ErrorIs(t, err, ErrModifyRecovery)
					}
					shared, err := stack.Load(common)
					require.NoError(t, err)
					require.Len(t, shared.Stacks, 1)
					assert.Equal(t, []string{"shared"}, shared.Stacks[0].BranchNames())
					assert.FileExists(t, filepath.Join(private, "gh-stack"))
					assert.FileExists(t, journal)
					out, _ := commandOutput(t, cfg, outR, errR)
					assert.Empty(t, out)
				})
			}
		}
	}
}

func TestStackMutation_NullWorktreeContextIsLegacy(t *testing.T) {
	for _, operation := range []string{"rebase", "modify"} {
		for _, original := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/original=%t", operation, original), func(t *testing.T) {
				common, local := t.TempDir(), t.TempDir()
				if original {
					local = common
				}
				writeStackFile(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "original"}}})
				journal := filepath.Join(common, "gh-stack-"+operation+"-state")
				data := []byte(`{"phase":"conflict","worktrees":null}`)
				require.NoError(t, os.WriteFile(journal, data, 0600))
				legacyCatalogs, err := stack.HasLegacyState(common)
				require.NoError(t, err)
				assert.False(t, legacyCatalogs, "journal detection is independent of catalog migration")
				restore := git.SetOps(&git.MockOps{
					GitDirFn:    func() (string, error) { return local, nil },
					CommonDirFn: func() (string, error) { return common, nil },
				})
				defer restore()
				cfg, outR, errR := config.NewTestConfig()
				release, err := beginStackMutation(cfg, operation+"-continue")
				if original {
					require.NoError(t, err)
					assert.Equal(t, common, cfg.StackMutation.StateDir)
					release()
				} else if operation == "rebase" {
					assert.ErrorIs(t, err, ErrRebaseActive)
				} else {
					assert.ErrorIs(t, err, ErrModifyRecovery)
				}
				after, err := os.ReadFile(journal)
				require.NoError(t, err)
				assert.Equal(t, data, after)
				out, diagnostics := commandOutput(t, cfg, outR, errR)
				assert.Empty(t, out)
				if !original {
					assert.Contains(t, diagnostics, "original worktree")
				}
			})
		}
	}
}

func TestStackMutation_CommonRecoveryDefersLegacyMigration(t *testing.T) {
	common := t.TempDir()
	private := filepath.Join(common, "worktrees", "other")
	require.NoError(t, os.MkdirAll(private, 0700))
	writeStackFile(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "original"}}})
	writeStackFile(t, private, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "other"}}})
	require.NoError(t, os.WriteFile(filepath.Join(common, rebaseStateFile), []byte(`{"phase":"conflict","worktrees":{}}`), 0600))
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	release, err := beginStackMutation(cfg, "rebase-continue")
	require.NoError(t, err)
	defer release()
	dir, err := stackStateDir(cfg)
	require.NoError(t, err)
	assert.Equal(t, common, dir)
	sf, err := stack.Load(common)
	require.NoError(t, err)
	require.Len(t, sf.Stacks, 1)
	assert.Equal(t, []string{"original"}, sf.Stacks[0].BranchNames())
	assert.FileExists(t, filepath.Join(private, "gh-stack"))
	commandOutput(t, cfg, outR, errR)
}

func TestStackMutation_PrivateAndCommonRecoveryAreAmbiguous(t *testing.T) {
	common := t.TempDir()
	private := filepath.Join(common, "worktrees", "original")
	require.NoError(t, os.MkdirAll(private, 0700))
	for _, dir := range []string{common, private} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, rebaseStateFile), []byte(`{"phase":"conflict","worktrees":{}}`), 0600))
	}
	restore := git.SetOps(&git.MockOps{
		GitDirFn:    func() (string, error) { return private, nil },
		CommonDirFn: func() (string, error) { return common, nil },
	})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	release, err := beginStackMutation(cfg, "rebase-continue")
	assert.ErrorIs(t, err, ErrRebaseActive)
	assert.Nil(t, release)
	assert.Nil(t, cfg.StackMutation)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, "multiple rebase recovery journals")
	assert.Contains(t, diagnostics, common)
	assert.Contains(t, diagnostics, private)
}

func TestStackMutation_MigrationErrorsLeaveCatalogsIntact(t *testing.T) {
	common := t.TempDir()
	legacy := filepath.Join(common, "worktrees", "other")
	require.NoError(t, os.MkdirAll(legacy, 0700))
	writeStackFile(t, common, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "same", Base: "one"}}})
	writeStackFile(t, legacy, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "same", Base: "two"}}})
	before, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	require.NoError(t, err)
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	_, err = stackStateDir(cfg)
	require.Error(t, err)
	var migrationErr *stack.MigrationConflictError
	require.ErrorAs(t, err, &migrationErr)
	assert.NotEmpty(t, migrationErr.Sources)
	assert.NotEmpty(t, migrationErr.Reason)
	release, err := beginStackMutation(cfg, "push")
	require.Error(t, err)
	migrationErr = nil
	require.ErrorAs(t, err, &migrationErr)
	assert.Nil(t, release)
	after, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.FileExists(t, filepath.Join(legacy, "gh-stack"))
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, "migrat")
}

func TestStackStateDir_PreservesMigrationBlockedError(t *testing.T) {
	common := t.TempDir()
	private := filepath.Join(common, "worktrees", "original")
	require.NoError(t, os.MkdirAll(private, 0700))
	writeStackFile(t, private, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "original"}}})
	journal := filepath.Join(private, rebaseStateFile)
	require.NoError(t, os.WriteFile(journal, []byte(`{"originalBranch":"original"}`), 0600))
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	_, err := stackStateDir(cfg)
	assert.ErrorIs(t, err, ErrSilent)
	var blocked *stack.MigrationBlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Contains(t, blocked.RecoveryPaths, journal)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, fmt.Sprintf("%q", journal))
}

func TestStackStateError_PreservesWindowsRecoveryPath(t *testing.T) {
	journal := `C:\Users\Example Worktree\.git\worktrees\original\gh-stack-rebase-state`
	cause := &stack.MigrationBlockedError{RecoveryPaths: []string{journal}}
	cfg, outR, errR := config.NewTestConfig()

	err := stackStateError(cfg, "migrating stack state", cause)

	assert.ErrorIs(t, err, ErrSilent)
	var blocked *stack.MigrationBlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Same(t, cause, blocked)
	assert.Equal(t, []string{journal}, blocked.RecoveryPaths)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, `"C:\\Users\\Example Worktree\\.git\\worktrees\\original\\gh-stack-rebase-state"`)
}

func TestStackStateHelpers_PreserveLockError(t *testing.T) {
	for _, operationLock := range []bool{true, false} {
		t.Run(fmt.Sprintf("operationLock=%t", operationLock), func(t *testing.T) {
			common := t.TempDir()
			private := filepath.Join(common, "worktrees", "other")
			require.NoError(t, os.MkdirAll(private, 0700))
			writeStackFile(t, private, stack.Stack{Trunk: stack.BranchRef{Branch: "main"}, Branches: []stack.BranchRef{{Branch: "other"}}})
			restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
			defer restore()
			lockFn := stack.Lock
			if operationLock {
				lockFn = stack.LockOperation
			}
			lock, err := lockFn(common)
			require.NoError(t, err)
			defer lock.Unlock()
			timeout := stack.LockTimeout
			stack.LockTimeout = 0
			defer func() { stack.LockTimeout = timeout }()
			cfg, outR, errR := config.NewTestConfig()
			_, err = stackStateDir(cfg)
			assert.ErrorIs(t, err, ErrLockFailed)
			var lockErr *stack.LockError
			require.ErrorAs(t, err, &lockErr)
			release, err := beginStackMutation(cfg, "push")
			assert.ErrorIs(t, err, ErrLockFailed)
			assert.Nil(t, release)
			lockErr = nil
			require.ErrorAs(t, err, &lockErr)
			assert.Nil(t, cfg.StackMutation)
			out, _ := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
		})
	}
}

func TestStackSaveError_PreservesTypedCause(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cause error
		exit  error
	}{
		{"lock", &stack.LockError{Err: assert.AnError}, ErrLockFailed},
		{"stale", &stack.StaleError{Err: assert.AnError}, ErrLockFailed},
		{"migration conflict", &stack.MigrationConflictError{Sources: []string{"original"}, Reason: "different definitions"}, ErrSilent},
		{"migration blocked", &stack.MigrationBlockedError{RecoveryPaths: []string{"journal"}}, ErrSilent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, outR, errR := config.NewTestConfig()
			err := stackSaveError(cfg, tt.cause)
			assert.ErrorIs(t, err, tt.exit)
			assert.ErrorIs(t, err, tt.cause)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			assert.NotEmpty(t, diagnostics)
		})
	}
}

func TestStackLookupError_CommandCallerMapping(t *testing.T) {
	storageErr := &stack.LockError{Err: assert.AnError}
	for _, tt := range []struct {
		name   string
		input  error
		want   error
		retain bool
	}{
		{"typed exit", ErrRebaseActive, ErrRebaseActive, true},
		{"wrapped typed exit", errors.Join(ErrLockFailed, storageErr), ErrLockFailed, true},
		{"interrupt", errInterrupt, ErrSilent, false},
		{"wrapped interrupt", fmt.Errorf("selection: %w", errInterrupt), ErrSilent, false},
		{"untyped lookup failure", assert.AnError, ErrNotInStack, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mapped := stackLookupError(tt.input)
			assert.ErrorIs(t, mapped, tt.want)
			if tt.retain {
				assert.Same(t, tt.input, mapped)
			}
		})
	}
}

func TestReportWorktreeOwner_Command(t *testing.T) {
	for _, tt := range []struct {
		name, path, quoted string
	}{
		{"clean path", "/Users/skarim/github/copilot-worktrees/gh-stack/skarim-solid-dollop", "/Users/skarim/github/copilot-worktrees/gh-stack/skarim-solid-dollop"},
		{"safe punctuation", "/tmp/worktree_1.2-3+tag@org=branch%20,:", "/tmp/worktree_1.2-3+tag@org=branch%20,:"},
		{"spaces", "/tmp/my worktree", "'/tmp/my worktree'"},
		{"apostrophe", "/tmp/owner's worktree", `'/tmp/owner'\''s worktree'`},
		{"double quotes", `/tmp/"quoted"`, `'/tmp/"quoted"'`},
		{"dollar expansion", "/tmp/$HOME", "'/tmp/$HOME'"},
		{"command substitution", "/tmp/$(pwd)", "'/tmp/$(pwd)'"},
		{"backticks", "/tmp/`pwd`", "'/tmp/`pwd`'"},
		{"operators", "/tmp/a;b&c|d<e>f", "'/tmp/a;b&c|d<e>f'"},
		{"globs", "/tmp/[abc]*?{x,y}", "'/tmp/[abc]*?{x,y}'"},
		{"shell specials", "/tmp/!^~#(name)", "'/tmp/!^~#(name)'"},
		{"backslash", `/tmp/a\b`, `'/tmp/a\b'`},
		{"tab", "/tmp/a\tb", "'/tmp/a\tb'"},
		{"newline", "/tmp/a\nb", "'/tmp/a\nb'"},
	} {
		for _, colored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/colored=%t", tt.name, colored), func(t *testing.T) {
				cfg, outR, errR := config.NewTestConfig()
				color := func(s string) string { return s }
				if colored {
					color = func(s string) string { return "\x1b[36m" + s + "\x1b[0m" }
				}
				cfg.ColorCyan = color
				target := "skarim/worktrees-distributed-rebase-sync"

				reportWorktreeOwner(cfg, target, tt.path)

				out, diagnostics := commandOutput(t, cfg, outR, errR)
				assert.Empty(t, out)
				assert.Equal(t, fmt.Sprintf(
					"%s Branch %q is already checked out in another worktree.\n"+
						"  Your current checkout is unchanged.\n\n"+
						"To work on this branch, run:\n  %s\n",
					color("\u2139"), target, color("cd "+tt.quoted),
				), diagnostics)
				assert.Equal(t, 1, strings.Count(diagnostics, tt.quoted), "show the complete path only in the command")
				assert.NotContains(t, diagnostics, "cd --")
				assert.NotContains(t, diagnostics, "$ cd")
				assert.NotContains(t, diagnostics, "Switched")
			})
		}
	}
}

func TestCheckoutWorktreeBranch_OutputContract(t *testing.T) {
	tests := []struct {
		name, current     string
		foreign, pathMode bool
		checkoutError     bool
		rootError         bool
		wantError         bool
	}{
		{"foreign path", "b1", true, true, false, false, false},
		{"foreign normal", "b1", true, false, false, false, true},
		{"unoccupied path", "b1", false, true, false, false, false},
		{"current path", "b2", false, true, false, false, false},
		{"failed checkout", "b1", false, true, true, false, true},
		{"failed root", "b1", false, true, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common, root := t.TempDir(), t.TempDir()
			owner := filepath.Join(t.TempDir(), "owner's worktree")
			var checkedOut []string
			restore := git.SetOps(&git.MockOps{
				GitDirFn:        func() (string, error) { return common, nil },
				CurrentBranchFn: func() (string, error) { return tt.current, nil },
				RootDirFn: func() (string, error) {
					if tt.rootError {
						return "", assert.AnError
					}
					return root, nil
				},
				WorktreesFn: func() ([]git.Worktree, error) {
					if tt.foreign {
						return []git.Worktree{{Path: owner, Branch: "b2"}}, nil
					}
					return nil, nil
				},
				CheckoutBranchFn: func(branch string) error {
					checkedOut = append(checkedOut, branch)
					if tt.checkoutError {
						return assert.AnError
					}
					return nil
				},
			})
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			err := checkoutWorktreeBranch(cfg, "b2", tt.pathMode)
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			if tt.wantError {
				require.Error(t, err)
				assert.Empty(t, out)
			} else {
				require.NoError(t, err)
				want := root
				if tt.foreign {
					want = owner
				}
				assert.Equal(t, want+"\n", out)
			}
			if tt.foreign || tt.current == "b2" || tt.rootError {
				assert.Empty(t, checkedOut)
			} else {
				assert.Equal(t, []string{"b2"}, checkedOut)
			}
			if tt.foreign && !tt.pathMode {
				assert.ErrorIs(t, err, ErrInvalidArgs)
				quotedOwner := "'" + strings.ReplaceAll(owner, "'", "'\\''") + "'"
				assert.Contains(t, diagnostics, "\n  cd "+quotedOwner+"\n")
				assert.Equal(t, 1, strings.Count(diagnostics, quotedOwner))
				assert.NotContains(t, diagnostics, "Switched")
			} else if tt.foreign {
				assert.Empty(t, diagnostics, "path mode must not print ownership guidance")
			}
		})
	}
}

func TestCheckoutWorktreeBranch_ForeignLookupDuringPausedOperation(t *testing.T) {
	common, root, owner := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(common, rebaseStateFile), []byte(`{"worktrees":{}}`), 0600))
	restore := git.SetOps(&git.MockOps{
		GitDirFn:    func() (string, error) { return common, nil },
		RootDirFn:   func() (string, error) { return root, nil },
		WorktreesFn: func() ([]git.Worktree, error) { return []git.Worktree{{Path: owner, Branch: "b1"}}, nil },
		CheckoutBranchFn: func(string) error {
			t.Fatal("foreign lookup must not change a checkout")
			return nil
		},
	})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	lock, err := stack.LockOperation(common)
	require.NoError(t, err)
	require.NoError(t, checkoutWorktreeBranch(cfg, "b1", true))
	lock.Unlock()
	out, _ := commandOutput(t, cfg, outR, errR)
	assert.Equal(t, owner+"\n", out)

	cfg, outR, errR = config.NewTestConfig()
	assert.ErrorIs(t, checkoutWorktreeBranch(cfg, "b2", true), ErrRebaseActive)
	out, _ = commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
}

func TestCheckoutWorktreeBranch_OwnershipErrors(t *testing.T) {
	for _, reason := range []string{"list", "duplicate", "prunable", "different repository", "relative path", "factory"} {
		t.Run(reason, func(t *testing.T) {
			common, root, owner := t.TempDir(), t.TempDir(), t.TempDir()
			mock := &git.MockOps{
				GitDirFn:  func() (string, error) { return common, nil },
				RootDirFn: func() (string, error) { return root, nil },
				WorktreesFn: func() ([]git.Worktree, error) {
					switch reason {
					case "list":
						return nil, assert.AnError
					case "duplicate":
						return []git.Worktree{{Path: owner, Branch: "b1"}, {Path: root, Branch: "b1"}}, nil
					case "prunable":
						return []git.Worktree{{Path: owner, Branch: "b1", Prunable: true}}, nil
					case "relative path":
						return []git.Worktree{{Path: "relative", Branch: "b1"}}, nil
					default:
						return []git.Worktree{{Path: owner, Branch: "b1"}}, nil
					}
				},
				CheckoutBranchFn: func(string) error {
					t.Fatal("ownership errors must not fall back to checkout")
					return nil
				},
			}
			if reason == "different repository" {
				mock.ForWorktreeFn = func(string) (git.Ops, error) {
					return &git.MockOps{CommonDirFn: func() (string, error) { return root, nil }}, nil
				}
			}
			if reason == "factory" {
				mock.ForWorktreeFn = func(string) (git.Ops, error) { return nil, assert.AnError }
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			require.Error(t, checkoutWorktreeBranch(cfg, "b1", true))
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			assert.NotEmpty(t, diagnostics)
			if reason == "factory" {
				assert.Contains(t, diagnostics, assert.AnError.Error())
			}
		})
	}
}

func TestCheckoutWorktreeBranch_StateLookupErrorsBeforeSwitch(t *testing.T) {
	for _, query := range []string{"rebase", "cherry-pick"} {
		t.Run(query, func(t *testing.T) {
			common, root := t.TempDir(), t.TempDir()
			mock := &git.MockOps{
				GitDirFn:  func() (string, error) { return common, nil },
				RootDirFn: func() (string, error) { return root, nil },
				CheckoutBranchFn: func(string) error {
					t.Fatal("state lookup failure must not change checkout")
					return nil
				},
			}
			fail := func() (bool, error) { return false, assert.AnError }
			if query == "rebase" {
				mock.IsRebaseInProgressFn = fail
			} else {
				mock.IsCherryPickInProgressFn = fail
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, outR, errR := config.NewTestConfig()

			require.ErrorIs(t, checkoutWorktreeBranch(cfg, "branch", true), ErrSilent)

			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			assert.Contains(t, diagnostics, assert.AnError.Error())
			assert.NoFileExists(t, filepath.Join(common, "gh-stack"))
		})
	}
}

func TestReadStackJournals_AtomicPublication(t *testing.T) {
	for _, kind := range []string{"rebase", "modify"} {
		t.Run(kind, func(t *testing.T) {
			common, origin := t.TempDir(), t.TempDir()
			path := filepath.Join(common, "gh-stack-"+kind+"-state")
			cfg, outR, errR := config.NewTestConfig()
			for _, phase := range []string{"applying", "conflict"} {
				data := []byte(fmt.Sprintf(`{"phase":%q,"worktrees":{"origin":{"path":%q}}}`, phase, origin))
				require.NoError(t, stack.WriteAtomic(path, data))

				journals, err := readStackJournals(cfg, common, common)

				require.NoError(t, err)
				require.Len(t, journals, 1)
				assert.Equal(t, stackJournal{
					dir: common, path: path, kind: kind, phase: phase, origin: origin,
				}, journals[0])
			}
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			assert.Empty(t, diagnostics)
		})
	}
}

func TestStackMutation_ReadErrorsFailClosed(t *testing.T) {
	common := t.TempDir()
	require.NoError(t, os.Mkdir(modify.StatePath(common), 0700))
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	release, err := beginStackMutation(cfg, "submit")
	assert.ErrorIs(t, err, ErrModifyRecovery)
	assert.Nil(t, release)
	out, _ := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
}

func TestStackMutation_ReportsJournalOrigin(t *testing.T) {
	common, origin := t.TempDir(), t.TempDir()
	journal := fmt.Sprintf(`{"worktrees":{"origin":{"path":%q,"id":"worktrees/origin"}}}`, origin)
	require.NoError(t, os.WriteFile(filepath.Join(common, rebaseStateFile), []byte(journal), 0600))
	restore := git.SetOps(&git.MockOps{GitDirFn: func() (string, error) { return common, nil }})
	defer restore()
	cfg, outR, errR := config.NewTestConfig()
	release, err := beginStackMutation(cfg, "push")
	assert.ErrorIs(t, err, ErrRebaseActive)
	assert.Nil(t, release)
	out, diagnostics := commandOutput(t, cfg, outR, errR)
	assert.Empty(t, out)
	assert.Contains(t, diagnostics, origin)
	assert.Contains(t, diagnostics, "gh stack rebase --continue")
}

func TestStackMutation_RebaseJournalPhasesBlockOtherMutations(t *testing.T) {
	for _, phase := range []string{"applying", "conflict", "complete", "restoring"} {
		t.Run(phase, func(t *testing.T) {
			common, local := t.TempDir(), t.TempDir()
			data := fmt.Sprintf(`{"phase":%q,"worktrees":{},"originalBranch":"b1"}`, phase)
			require.NoError(t, os.WriteFile(filepath.Join(common, rebaseStateFile), []byte(data), 0600))
			restore := git.SetOps(&git.MockOps{
				GitDirFn:    func() (string, error) { return local, nil },
				CommonDirFn: func() (string, error) { return common, nil },
			})
			defer restore()
			for _, kind := range []string{"init", "add", "checkout", "push", "submit", "link", "merge", "unstack", "rebase", "sync", "modify", "modify-abort"} {
				t.Run(kind, func(t *testing.T) {
					cfg, outR, errR := config.NewTestConfig()
					release, err := beginStackMutation(cfg, kind)
					assert.ErrorIs(t, err, ErrRebaseActive)
					assert.Nil(t, release)
					assert.Nil(t, cfg.StackMutation)
					out, _ := commandOutput(t, cfg, outR, errR)
					assert.Empty(t, out)
				})
			}
			for _, kind := range []string{"rebase-continue", "rebase-abort"} {
				t.Run(kind, func(t *testing.T) {
					cfg, outR, errR := config.NewTestConfig()
					release, err := beginStackMutation(cfg, kind)
					require.NoError(t, err)
					release()
					commandOutput(t, cfg, outR, errR)
				})
			}
		})
	}
}

func TestStackMutatingCommands_RecoveryBeforeSideEffects(t *testing.T) {
	commands := []struct {
		name string
		run  func(*config.Config) error
	}{
		{"init", func(cfg *config.Config) error {
			return runInit(cfg, &initOptions{base: "main", branches: []string{"new"}})
		}},
		{"add", func(cfg *config.Config) error {
			return runAdd(cfg, &addOptions{stageAll: true, message: "commit"}, []string{"new"})
		}},
		{"push", func(cfg *config.Config) error { return runPush(cfg, &pushOptions{}) }},
		{"submit", func(cfg *config.Config) error { return runSubmit(cfg, &submitOptions{auto: true}) }},
		{"link", func(cfg *config.Config) error { return runLink(cfg, &linkOptions{}, []string{"1", "2"}) }},
		{"merge", func(cfg *config.Config) error { return runMerge(cfg, &mergeOptions{}, []string{"7"}) }},
		{"unstack", func(cfg *config.Config) error { return runUnstack(cfg, &unstackOptions{stackNumber: 7}) }},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			for _, journal := range []struct {
				file, data string
				want       error
			}{
				{rebaseStateFile, `{"worktrees":{}}`, ErrRebaseActive},
				{"gh-stack-modify-state", `{"worktrees":{},"phase":"applying"}`, ErrModifyRecovery},
				{"gh-stack-modify-state", `{"worktrees":{},"phase":"conflict"}`, ErrModifyRecovery},
			} {
				t.Run(journal.file+journal.data, func(t *testing.T) {
					common, local := t.TempDir(), t.TempDir()
					require.NoError(t, os.WriteFile(filepath.Join(common, journal.file), []byte(journal.data), 0600))
					restore := git.SetOps(&git.MockOps{
						GitDirFn:    func() (string, error) { return local, nil },
						CommonDirFn: func() (string, error) { return common, nil },
						PushFn: func(string, []string, bool, bool) error {
							t.Fatal("recovery guard must run before any push")
							return nil
						},
						StageAllFn: func() error {
							t.Fatal("recovery guard must run before staging")
							return nil
						},
						CreateBranchFn: func(string, string) error {
							t.Fatal("recovery guard must run before branch creation")
							return nil
						},
					})
					defer restore()
					cfg, outR, errR := config.NewTestConfig()
					cfg.GitHubClientOverride = &github.MockClient{
						ListStacksFn: func() ([]github.RemoteStack, error) {
							t.Fatal("guard must run before remote operations")
							return nil, nil
						},
					}
					assert.ErrorIs(t, command.run(cfg), journal.want)
					out, _ := commandOutput(t, cfg, outR, errR)
					assert.Empty(t, out)
				})
			}
		})
	}
}

func TestStackStateDir_VersionAndCommonDirFailures(t *testing.T) {
	for _, oldVersion := range []bool{true, false} {
		t.Run(fmt.Sprintf("oldVersion=%t", oldVersion), func(t *testing.T) {
			common := t.TempDir()
			mock := &git.MockOps{GitDirFn: func() (string, error) { return common, nil }}
			if oldVersion {
				mock.CheckVersionFn = func() error { return fmt.Errorf("Git 2.36 or newer is required; upgrade Git") }
			} else {
				mock.CommonDirFn = func() (string, error) { return "", assert.AnError }
			}
			restore := git.SetOps(mock)
			defer restore()
			cfg, outR, errR := config.NewTestConfig()
			release, err := beginOptionalStackMutation(cfg, "link")
			require.Error(t, err)
			assert.Nil(t, release)
			assert.NoFileExists(t, filepath.Join(common, "gh-stack-operation.lock"))
			out, diagnostics := commandOutput(t, cfg, outR, errR)
			assert.Empty(t, out)
			if oldVersion {
				assert.Contains(t, diagnostics, "upgrade Git")
			}
		})
	}
}

func TestIsInterruptError_DirectMatch(t *testing.T) {
	if !isInterruptError(terminal.InterruptErr) {
		t.Error("expected true for terminal.InterruptErr")
	}
}

func TestIsInterruptError_Wrapped(t *testing.T) {
	// This is how the prompter library wraps the interrupt error.
	wrapped := fmt.Errorf("could not prompt: %w", terminal.InterruptErr)
	if !isInterruptError(wrapped) {
		t.Error("expected true for wrapped interrupt error")
	}
}

func TestIsInterruptError_DoubleWrapped(t *testing.T) {
	// Simulate additional wrapping by callers.
	inner := fmt.Errorf("could not prompt: %w", terminal.InterruptErr)
	outer := fmt.Errorf("stack selection: %w", inner)
	if !isInterruptError(outer) {
		t.Error("expected true for double-wrapped interrupt error")
	}
}

func TestIsInterruptError_NonInterrupt(t *testing.T) {
	if isInterruptError(errors.New("some other error")) {
		t.Error("expected false for non-interrupt error")
	}
}

func TestIsInterruptError_Nil(t *testing.T) {
	if isInterruptError(nil) {
		t.Error("expected false for nil error")
	}
}

func TestPrintInterrupt_Output(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	printInterrupt(cfg)
	output := collectOutput(cfg, outR, errR)

	if !strings.Contains(output, "Received interrupt, aborting operation") {
		t.Errorf("expected interrupt message, got: %s", output)
	}
	// Should NOT contain error marker (✗)
	if strings.Contains(output, "\u2717") {
		t.Errorf("interrupt message should not use error format, got: %s", output)
	}
}

func TestErrInterrupt_IsDistinct(t *testing.T) {
	if errors.Is(errInterrupt, terminal.InterruptErr) {
		t.Error("errInterrupt sentinel should not match terminal.InterruptErr")
	}
	if !errors.Is(errInterrupt, errInterrupt) {
		t.Error("errInterrupt should match itself")
	}
}

func TestEnsureRerere_SkipsWhenAlreadyEnabled(t *testing.T) {
	enableCalled := false
	restore := git.SetOps(&git.MockOps{
		IsRerereEnabledFn: func() (bool, error) { return true, nil },
		EnableRerereFn: func() error {
			enableCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	_ = ensureRerere(cfg)
	collectOutput(cfg, outR, errR)

	if enableCalled {
		t.Error("EnableRerere should not be called when already enabled")
	}
}

func TestEnsureRerere_SkipsWhenDeclined(t *testing.T) {
	enableCalled := false
	restore := git.SetOps(&git.MockOps{
		IsRerereEnabledFn:  func() (bool, error) { return false, nil },
		IsRerereDeclinedFn: func() (bool, error) { return true, nil },
		EnableRerereFn: func() error {
			enableCalled = true
			return nil
		},
	})
	defer restore()

	cfg, outR, errR := config.NewTestConfig()
	_ = ensureRerere(cfg)
	collectOutput(cfg, outR, errR)

	if enableCalled {
		t.Error("EnableRerere should not be called when user previously declined")
	}
}

func TestEnsureRerere_SkipsWhenNonInteractive(t *testing.T) {
	enableCalled := false
	declinedSaved := false
	restore := git.SetOps(&git.MockOps{
		IsRerereEnabledFn:  func() (bool, error) { return false, nil },
		IsRerereDeclinedFn: func() (bool, error) { return false, nil },
		EnableRerereFn: func() error {
			enableCalled = true
			return nil
		},
		SaveRerereDeclinedFn: func() error {
			declinedSaved = true
			return nil
		},
	})
	defer restore()

	// NewTestConfig is non-interactive (pipes, not a TTY).
	cfg, outR, errR := config.NewTestConfig()
	_ = ensureRerere(cfg)
	collectOutput(cfg, outR, errR)

	if enableCalled {
		t.Error("EnableRerere should not be called in non-interactive mode")
	}
	if declinedSaved {
		t.Error("SaveRerereDeclined should not be called in non-interactive mode")
	}
}

func TestResolvePR_ByPRNumber(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
					{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 43, URL: "https://github.com/o/r/pull/43"}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	s, br, err := resolvePR(cfg, sf, "42")
	assert.NoError(t, err)
	assert.Equal(t, "feat-1", br.Branch)
	assert.Equal(t, 42, br.PullRequest.Number)
	assert.Equal(t, "main", s.Trunk.Branch)
}

func TestResolvePR_ByPRURL(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 42, URL: "https://github.com/o/r/pull/42"}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	s, br, err := resolvePR(cfg, sf, "https://github.com/o/r/pull/42")
	assert.NoError(t, err)
	assert.Equal(t, "feat-1", br.Branch)
	assert.Equal(t, "main", s.Trunk.Branch)
}

func TestResolvePR_ByBranchName(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 42}},
					{Branch: "feat-2", PullRequest: &stack.PullRequestRef{Number: 43}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	s, br, err := resolvePR(cfg, sf, "feat-2")
	assert.NoError(t, err)
	assert.Equal(t, "feat-2", br.Branch)
	assert.Equal(t, 43, br.PullRequest.Number)
	assert.Equal(t, "main", s.Trunk.Branch)
}

func TestResolvePR_NotFound(t *testing.T) {
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "feat-1"}},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	_, _, err := resolvePR(cfg, sf, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no locally tracked stack found")
}

func TestResolvePR_URLPrecedesNumber(t *testing.T) {
	// A PR URL that contains number 99 should resolve via URL parsing,
	// even if PR #99 doesn't exist — the URL parser extracts the number.
	sf := &stack.StackFile{
		SchemaVersion: 1,
		Stacks: []stack.Stack{
			{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "feat-1", PullRequest: &stack.PullRequestRef{Number: 99, URL: "https://github.com/o/r/pull/99"}},
				},
			},
		},
	}

	cfg, _, _ := config.NewTestConfig()
	_, br, err := resolvePR(cfg, sf, "https://github.com/o/r/pull/99")
	assert.NoError(t, err)
	assert.Equal(t, 99, br.PullRequest.Number)
}

func TestSyncStackPRs_NoTrackedPR_OnlyAdoptsOpenPRs(t *testing.T) {
	// A branch with no tracked PR should only adopt OPEN PRs,
	// not stale merged/closed PRs from a previous branch name usage.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "reused-branch"}, // no PullRequest
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		// FindPRForBranch (OPEN only) returns nil — no open PR.
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// Branch should still have no PR tracked.
	assert.Nil(t, s.Branches[0].PullRequest)
}

func TestSyncStackPRs_NoTrackedPR_AdoptsOpenPR(t *testing.T) {
	// A branch with no tracked PR should adopt an OPEN PR it discovers.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature"}, // no PullRequest
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 99,
				ID:     "PR_99",
				URL:    "https://github.com/o/r/pull/99",
				State:  "OPEN",
			}, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 99, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_TrackedPR_DetectsMerge(t *testing.T) {
	// A branch with a tracked PR should detect when that PR gets merged.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 42,
					ID:     "PR_42",
					URL:    "https://github.com/o/r/pull/42",
				},
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 42,
				ID:     "PR_42",
				URL:    "https://github.com/o/r/pull/42",
				State:  "MERGED",
				Merged: true,
			}, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 42, s.Branches[0].PullRequest.Number)
	assert.True(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_MergedBranch_StaysMerged(t *testing.T) {
	// A merged branch should stay merged — no API calls, no changes.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "merged-branch",
				PullRequest: &stack.PullRequestRef{
					Number: 20,
					ID:     "PR_20",
					URL:    "https://github.com/o/r/pull/20",
					Merged: true,
				},
			},
		},
	}

	apiCalled := false
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			apiCalled = true
			return nil, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			apiCalled = true
			return nil, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 20, s.Branches[0].PullRequest.Number)
	assert.True(t, s.Branches[0].PullRequest.Merged)
	assert.False(t, apiCalled, "no API calls should be made for merged branches")
}

func TestSyncStackPRs_ClosedPR_ReplacedByOpenPR(t *testing.T) {
	// A tracked PR that was closed (not merged) should be replaced
	// by a new OPEN PR if one exists.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 10,
					ID:     "PR_10",
					URL:    "https://github.com/o/r/pull/10",
				},
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10,
				State:  "CLOSED",
				Merged: false,
			}, nil
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 15,
				ID:     "PR_15",
				URL:    "https://github.com/o/r/pull/15",
				State:  "OPEN",
			}, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 15, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_TrackedOpenPR_UpdatesQueued(t *testing.T) {
	// A tracked OPEN PR that enters a merge queue should have Queued set.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 42,
					ID:     "PR_42",
					URL:    "https://github.com/o/r/pull/42",
				},
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 42,
				State:  "OPEN",
				MergeQueueEntry: &github.MergeQueueEntry{
					ID: "MQ_1",
				},
			}, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	assert.True(t, s.Branches[0].Queued)
}

func TestSyncStackPRs_ClosedPR_NoReplacement_ClearsPR(t *testing.T) {
	// A tracked PR that was closed with no replacement OPEN PR should
	// have its PR ref cleared so it doesn't appear as an active PR.
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{
				Branch: "feature",
				PullRequest: &stack.PullRequestRef{
					Number: 10,
					ID:     "PR_10",
					URL:    "https://github.com/o/r/pull/10",
				},
				Queued: true,
			},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{
				Number: 10,
				State:  "CLOSED",
				Merged: false,
			}, nil
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return nil, nil // no open replacement
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	assert.Nil(t, s.Branches[0].PullRequest)
	assert.False(t, s.Branches[0].Queued)
}

func TestSyncStackPRs_RemoteStack_UsesStackAPI(t *testing.T) {
	// When the stack has a remote ID, sync should use the stack API
	// as source of truth, matching PRs to branches by head ref name.
	s := &stack.Stack{
		ID:    "100",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 100, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 10:
				return &github.PullRequest{Number: 10, ID: "PR_10", URL: "https://github.com/o/r/pull/10", HeadRefName: "b1", State: "OPEN"}, nil
			case 11:
				return &github.PullRequest{Number: 11, ID: "PR_11", URL: "https://github.com/o/r/pull/11", HeadRefName: "b2", State: "MERGED", Merged: true}, nil
			}
			return nil, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// b1 should be tracked with open PR
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 10, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)

	// b2 should be tracked with merged PR (stack API keeps closed/merged PRs)
	require.NotNil(t, s.Branches[1].PullRequest)
	assert.Equal(t, 11, s.Branches[1].PullRequest.Number)
	assert.True(t, s.Branches[1].PullRequest.Merged)
}

func TestSyncStackPRs_BackfillsStackNumber(t *testing.T) {
	// A stack tracked before the number was recorded (Number == 0) gets its
	// number backfilled from the remote during the shared sync, so callers can
	// display it.
	s := &stack.Stack{
		ID:    "100", // legacy: Number unset
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 100, Number: 5, PullRequests: []int{10, 11}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			switch number {
			case 10:
				return &github.PullRequest{Number: 10, HeadRefName: "b1", State: "OPEN"}, nil
			case 11:
				return &github.PullRequest{Number: 11, HeadRefName: "b2", State: "OPEN"}, nil
			}
			return nil, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	assert.Equal(t, 5, s.Number, "the stack number should be backfilled from the remote")
}

func TestSyncStackPRs_RemoteStack_ClosedPRStaysAssociated(t *testing.T) {
	// When using the stack API, a closed (not merged) PR should remain
	// associated — the stack API is the source of truth, not PR state.
	s := &stack.Stack{
		ID:    "200",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature", PullRequest: &stack.PullRequestRef{Number: 5}},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return []github.RemoteStack{
				{ID: 200, PullRequests: []int{5}},
			}, nil
		},
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 5, ID: "PR_5", URL: "https://github.com/o/r/pull/5", HeadRefName: "feature", State: "CLOSED"}, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// PR should still be associated (not cleared), because the stack API says it's part of the stack.
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 5, s.Branches[0].PullRequest.Number)
	assert.False(t, s.Branches[0].PullRequest.Merged)
}

func TestSyncStackPRs_RemoteStack_FallsBackOnAPIError(t *testing.T) {
	// If the stack API fails, fall back to local discovery.
	s := &stack.Stack{
		ID:    "300",
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feature"},
		},
	}

	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		ListStacksFn: func() ([]github.RemoteStack, error) {
			return nil, fmt.Errorf("API error")
		},
		FindPRForBranchFn: func(branch string) (*github.PullRequest, error) {
			return &github.PullRequest{Number: 77, ID: "PR_77", URL: "https://github.com/o/r/pull/77", State: "OPEN"}, nil
		},
	}

	_ = syncStackPRs(cfg, s)
	collectOutput(cfg, outR, errR)

	// Should have fallen back to local discovery and found the open PR.
	require.NotNil(t, s.Branches[0].PullRequest)
	assert.Equal(t, 77, s.Branches[0].PullRequest.Number)
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantN  int
		wantOK bool
	}{
		{"standard URL", "https://github.com/owner/repo/pull/42", 42, true},
		{"with trailing slash", "https://github.com/owner/repo/pull/42/", 42, true},
		{"with files tab", "https://github.com/owner/repo/pull/42/files", 42, true},
		{"GHES URL", "https://ghes.example.com/owner/repo/pull/99", 99, true},
		{"GHES URL with trailing slash", "https://ghes.example.com/owner/repo/pull/7/", 7, true},
		{"not a PR URL", "https://github.com/owner/repo/issues/42", 0, false},
		{"plain number", "42", 0, false},
		{"branch name", "feat-1", 0, false},
		{"empty", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := parsePRURL(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantN, n)
			}
		})
	}
}

func TestStackNeedsRebase_AllCurrent(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	mock := &git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			return true, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	assert.False(t, stackNeedsRebase(s, ""), "stack should not need rebase when all branches are current")
}

func TestStackNeedsRebase_FirstBranchStale(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1"},
			{Branch: "b2"},
		},
	}

	mock := &git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			if a == "main" && d == "b1" {
				return false, nil
			}
			return true, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	assert.True(t, stackNeedsRebase(s, ""), "stack should need rebase when first branch is stale")
}

func TestStackNeedsRebase_SkipsMergedBranches(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Merged: true}},
			{Branch: "b2"},
		},
	}

	mock := &git.MockOps{
		IsAncestorFn: func(a, d string) (bool, error) {
			return true, nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	assert.False(t, stackNeedsRebase(s, ""), "should skip merged branches and find stack up to date")
}

// setTestRepo sets RepoOverride so tests don't depend on real git context.
func setTestRepo(cfg *config.Config) {
	cfg.RepoOverride = &repository.Repository{Host: "github.com", Owner: "o", Name: "r"}
}

func TestWarnStacksUnavailable_ShowsNotEnabled(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()

	warnStacksUnavailable(cfg)

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.Contains(t, output, "Stacked PRs are not enabled for this repository")
}

func TestEnsureLocalTrunk_AlreadyExists(t *testing.T) {
	mock := &git.MockOps{
		BranchExistsFn: func(name string) (bool, error) {
			return name == "main", nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")
	assert.NoError(t, err)
}

func TestTrunkLookupFailureStopsBeforeMutation(t *testing.T) {
	lookupErr := fmt.Errorf("branch lookup failed")
	restore := git.SetOps(&git.MockOps{
		BranchExistsFn: func(string) (bool, error) { return false, lookupErr },
		FetchBranchFn: func(string, string) error {
			t.Fatal("must not fetch after a failed lookup")
			return nil
		},
		FetchBranchesFn: func(string, []string) error {
			t.Fatal("must not fetch after a failed lookup")
			return nil
		},
		CreateBranchFn: func(string, string) error {
			t.Fatal("must not create a trunk after a failed lookup")
			return nil
		},
	})
	defer restore()
	cfg, _, _ := config.NewTestConfig()
	defer cfg.Out.Close()
	defer cfg.Err.Close()
	require.ErrorIs(t, ensureLocalTrunk(cfg, "main", "origin"), lookupErr)
	s := &stack.Stack{Trunk: stack.BranchRef{Branch: "origin/main"}}
	require.ErrorIs(t, normalizeStackTrunk(cfg, s, "origin"), lookupErr)
	assert.Equal(t, "origin/main", s.Trunk.Branch)
	_, err := resolveTrunkTarget(cfg, s, "origin", "feature")
	require.ErrorIs(t, err, lookupErr)
}

func TestEnsureLocalTrunk_FetchesAndCreates(t *testing.T) {
	var fetchedBranches []string
	var createdBranch, createdBase string

	mock := &git.MockOps{
		BranchExistsFn: func(name string) (bool, error) {
			return false, nil
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			fetchedBranches = branches
			return nil
		},
		CreateBranchFn: func(name, base string) error {
			createdBranch = name
			createdBase = base
			return nil
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")

	assert.NoError(t, err)
	assert.Equal(t, []string{"main"}, fetchedBranches)
	assert.Equal(t, "main", createdBranch)
	assert.Equal(t, "origin/main", createdBase)
}

func TestEnsureLocalTrunk_FetchFails(t *testing.T) {
	mock := &git.MockOps{
		BranchExistsFn: func(name string) (bool, error) {
			return false, nil
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			return fmt.Errorf("network error")
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not fetch trunk branch main from origin")
}

func TestEnsureLocalTrunk_CreateFails(t *testing.T) {
	mock := &git.MockOps{
		BranchExistsFn: func(name string) (bool, error) {
			return false, nil
		},
		FetchBranchesFn: func(remote string, branches []string) error {
			return nil
		},
		CreateBranchFn: func(name, base string) error {
			return fmt.Errorf("ref not found")
		},
	}
	restore := git.SetOps(mock)
	defer restore()

	cfg, _, _ := config.NewTestConfig()
	err := ensureLocalTrunk(cfg, "main", "origin")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not create local trunk branch main")
}

func TestEnrichPRContent(t *testing.T) {
	calls := 0
	client := &github.MockClient{
		FindPRByNumberFn: func(number int) (*github.PullRequest, error) {
			calls++
			return &github.PullRequest{Number: number, Title: "Fetched title", Body: "Fetched body"}, nil
		},
	}
	details := map[string]*github.PRDetails{
		"merged": {Number: 10, State: "MERGED"},                // missing title -> fetched
		"open":   {Number: 11, State: "OPEN", Title: "Has it"}, // already has a title -> skipped
		"nonum":  {Number: 0, State: "OPEN"},                   // no number -> skipped
	}

	enrichPRContent(client, details)

	assert.Equal(t, 1, calls, "only the title-less PR with a number is fetched")
	assert.Equal(t, "Fetched title", details["merged"].Title)
	assert.Equal(t, "Fetched body", details["merged"].Body)
	assert.Equal(t, "Has it", details["open"].Title, "PRs that already have a title are untouched")
}

func TestUpdateBaseSHAsPreservesLastValidBase(t *testing.T) {
	s := &stack.Stack{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "parent", Base: "main-tip"},
			{Branch: "child", Base: "old-parent"},
		},
	}

	restore := git.SetOps(&git.MockOps{
		RevParseFn: func(ref string) (string, error) {
			switch ref {
			case "main":
				return "main-tip", nil
			case "parent":
				return "amended-parent", nil
			case "child":
				return "child-tip", nil
			default:
				return "", errors.New("unknown ref")
			}
		},
		IsAncestorFn: func(ancestor, branch string) (bool, error) {
			return !(ancestor == "amended-parent" && branch == "child"), nil
		},
	})
	defer restore()

	updateBaseSHAs(s)

	assert.Equal(t, "old-parent", s.Branches[1].Base)
	assert.Equal(t, "child-tip", s.Branches[1].Head)
}
