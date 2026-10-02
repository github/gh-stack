package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmd_SubcommandRegistration(t *testing.T) {
	root := RootCmd()
	expected := []string{"init", "add", "checkout", "push", "sync", "unstack", "view", "rebase", "up", "down", "top", "bottom", "alias", "feedback", "submit", "merge"}

	registered := make(map[string]bool)
	for _, cmd := range root.Commands() {
		registered[cmd.Name()] = true
	}

	for _, name := range expected {
		assert.True(t, registered[name], "expected subcommand %q to be registered", name)
	}
}

func TestRootCmd_HelpOutput(t *testing.T) {
	root := RootCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help"})

	err := root.Execute()
	require.NoError(t, err)

	output := stdout.String() + stderr.String()
	assert.Contains(t, output, "Stacked PRs")
	assert.Contains(t, output, "Stack management:")
	assert.Contains(t, output, "Learn more:")
	assert.Contains(t, output, "https://gh.io/stacks")
}

func newUpdateTestRoot(t *testing.T, startCheck func(context.Context, string) <-chan updateResult) (*rootCommand, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", "")
	t.Setenv("GH_DEBUG", "")
	cfg, outR, errR := config.NewTestConfig()
	t.Cleanup(func() {
		cfg.Out.Close()
		cfg.Err.Close()
		outR.Close()
		errR.Close()
	})
	root := newRootCmd(cfg, startCheck)
	root.Version = "0.1.1"
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root, stdout, stderr
}

func readyUpdate(result updateResult) <-chan updateResult {
	results := make(chan updateResult, 1)
	results <- result
	return results
}

func testUpdateNotification(out io.Writer) error {
	_, err := fmt.Fprint(out, "\nA new release of gh-stack is available: 0.1.1 -> 0.2.0\nTo upgrade, run: gh extension upgrade stack\n")
	return err
}

func TestRootCmd_UpdateNotice(t *testing.T) {
	var checkContext context.Context
	checks := 0
	root, stdout, stderr := newUpdateTestRoot(t, func(ctx context.Context, version string) <-chan updateResult {
		checks++
		checkContext = ctx
		assert.Equal(t, "0.1.1", version)
		return readyUpdate(updateResult{notify: testUpdateNotification})
	})
	t.Setenv("CI", "true")
	t.Setenv("GH_NO_EXTENSION_UPDATE_NOTIFIER", "1")
	root.AddCommand(&cobra.Command{
		Use: "probe",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), `{"ok":true}`)
			fmt.Fprintln(cmd.ErrOrStderr(), "Command complete.")
			return nil
		},
	})

	require.NoError(t, execute(root, []string{"probe"}))
	assert.Equal(t, 1, checks)
	assert.Equal(t, "{\"ok\":true}\n", stdout.String())
	assert.Equal(t, "Command complete.\n\nA new release of gh-stack is available: 0.1.1 -> 0.2.0\nTo upgrade, run: gh extension upgrade stack\n", stderr.String())
	require.ErrorIs(t, checkContext.Err(), context.Canceled)
}

func TestRootCmd_UpdateNoticeExcludedCommands(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		version  string
		disabled bool
		wantErr  bool
	}{
		{name: "no command"},
		{name: "help flag", args: []string{"--help"}},
		{name: "help command", args: []string{"help", "probe"}},
		{name: "subcommand help", args: []string{"probe", "--help"}},
		{name: "version", args: []string{"--version"}},
		{name: "completion generation", args: []string{"completion", "bash"}},
		{name: "completion request", args: []string{cobra.ShellCompRequestCmd, "probe", ""}},
		{name: "completion without descriptions", args: []string{cobra.ShellCompNoDescRequestCmd, "probe", ""}},
		{name: "invalid command", args: []string{"unknown"}, wantErr: true},
		{name: "invalid flag", args: []string{"probe", "--unknown"}, wantErr: true},
		{name: "invalid argument", args: []string{"probe", "extra"}, wantErr: true},
		{name: "opt-out", args: []string{"probe"}, disabled: true},
		{name: "development build", args: []string{"probe"}, version: "dev"},
		{name: "prerelease build", args: []string{"probe"}, version: "0.2.0-rc.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := 0
			root, stdout, stderr := newUpdateTestRoot(t, func(context.Context, string) <-chan updateResult {
				checks++
				return readyUpdate(updateResult{notify: testUpdateNotification})
			})
			if tt.version != "" {
				root.Version = tt.version
			}
			if tt.disabled {
				t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", "1")
			}
			root.AddCommand(&cobra.Command{
				Use:  "probe",
				Args: cobra.NoArgs,
				RunE: func(*cobra.Command, []string) error { return nil },
			})
			root.SetArgs(append([]string{}, tt.args...))
			err := root.Execute()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Zero(t, checks)
			assert.NotContains(t, stdout.String()+stderr.String(), "A new release")
			if tt.name == "version" {
				assert.Equal(t, "gh stack version 0.1.1\n", stdout.String())
			}
		})
	}
}

func TestRootCmd_UpdateNoticePreservesCommandErrors(t *testing.T) {
	for _, tt := range []struct {
		name       string
		commandErr error
		errorText  string
		wantNotice bool
	}{
		{"untyped failure", errors.New("command failed"), "command failed\n", true},
		{"conflict", ErrConflict, "", true},
		{"API failure", ErrAPIFailure, "", true},
		{"wrapped failure", fmt.Errorf("wrapped: %w", ErrAPIFailure), "", true},
		{"already reported failure", ErrSilent, "", true},
		{"usage error", fmt.Errorf("wrapped: %w", ErrInvalidArgs), "", false},
		{"canceled context", context.Canceled, "context canceled\n", false},
		{"interrupt", errInterrupt, "interrupt\n", false},
		{"terminal interrupt", terminal.InterruptErr, terminal.InterruptErr.Error() + "\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var checkContext context.Context
			root, stdout, stderr := newUpdateTestRoot(t, func(ctx context.Context, _ string) <-chan updateResult {
				checkContext = ctx
				return readyUpdate(updateResult{notify: testUpdateNotification})
			})
			root.AddCommand(&cobra.Command{
				Use: "probe",
				RunE: func(cmd *cobra.Command, _ []string) error {
					fmt.Fprintln(cmd.ErrOrStderr(), "Original diagnostic.")
					return tt.commandErr
				},
			})
			err := execute(root, []string{"probe"})
			require.ErrorIs(t, err, tt.commandErr)
			assert.Empty(t, stdout.String())
			var want bytes.Buffer
			fmt.Fprint(&want, "Original diagnostic.\n"+tt.errorText)
			if tt.wantNotice {
				require.NoError(t, testUpdateNotification(&want))
			}
			assert.Equal(t, want.String(), stderr.String())
			require.ErrorIs(t, checkContext.Err(), context.Canceled)
		})
	}
}

func TestRootCmd_CanceledCommandSuppressesNotice(t *testing.T) {
	for _, tt := range []struct {
		name      string
		interrupt bool
		err       error
	}{
		{"successful cancellation", false, nil},
		{"silent cancellation", false, ErrSilent},
		{"reported interrupt", true, ErrSilent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", "")
			cfg, outR, errR := config.NewTestConfig()
			root := newRootCmd(cfg, func(context.Context, string) <-chan updateResult {
				return readyUpdate(updateResult{notify: testUpdateNotification})
			})
			root.Version = "0.1.1"
			root.AddCommand(&cobra.Command{
				Use: "probe",
				RunE: func(*cobra.Command, []string) error {
					if tt.interrupt {
						printInterrupt(cfg)
					} else {
						cfg.Canceled = true
					}
					return tt.err
				},
			})
			err := execute(root, []string{"probe"})
			if tt.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.err)
			}
			output := collectOutput(cfg, outR, errR)
			assert.NotContains(t, output, "gh extension upgrade stack")
			if tt.interrupt {
				assert.Contains(t, output, "Received interrupt, aborting operation")
			}
		})
	}
}

func TestRootCmd_UpdateDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		debug  bool
		result updateResult
	}{
		{name: "quiet failure", result: updateResult{err: errors.New("offline")}},
		{name: "debug failure", debug: true, result: updateResult{err: errors.New("offline")}},
		{name: "recovered cache", debug: true, result: updateResult{notify: testUpdateNotification, err: errors.New("invalid cache")}},
		{name: "notification failure", debug: true, result: updateResult{notify: func(io.Writer) error { return errors.New("state is unwritable") }}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, stdout, stderr := newUpdateTestRoot(t, func(context.Context, string) <-chan updateResult {
				return readyUpdate(tt.result)
			})
			if tt.debug {
				t.Setenv("GH_DEBUG", "1")
			}
			root.AddCommand(&cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { return nil }})
			require.NoError(t, execute(root, []string{"probe"}))
			assert.Empty(t, stdout.String())
			if tt.debug {
				assert.Contains(t, stderr.String(), "debug: gh-stack update notification:")
			} else {
				assert.Empty(t, stderr.String())
			}
			if tt.name == "recovered cache" {
				assert.Contains(t, stderr.String(), "gh extension upgrade stack")
				assert.Contains(t, stderr.String(), "invalid cache")
			}
		})
	}
}

func TestRootCmd_DoesNotWaitForUpdate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("command failure=%t", fail), func(t *testing.T) {
			results := make(chan updateResult, 1)
			workerDone := make(chan struct{})
			root, stdout, stderr := newUpdateTestRoot(t, func(ctx context.Context, _ string) <-chan updateResult {
				go func() {
					<-ctx.Done()
					results <- updateResult{err: ctx.Err()}
					close(workerDone)
				}()
				return results
			})
			root.AddCommand(&cobra.Command{
				Use: "probe",
				RunE: func(*cobra.Command, []string) error {
					if fail {
						return ErrConflict
					}
					return nil
				},
			})
			done := make(chan error, 1)
			go func() { done <- execute(root, []string{"probe"}) }()
			select {
			case err := <-done:
				if fail {
					require.ErrorIs(t, err, ErrConflict)
				} else {
					require.NoError(t, err)
				}
			case <-time.After(5 * time.Second):
				// Unblock a regressed finalizer before failing the test.
				results <- updateResult{}
				<-done
				<-workerDone
				t.Fatal("command waited for an unfinished update check")
			}
			select {
			case <-workerDone:
			case <-time.After(5 * time.Second):
				t.Fatal("check was not canceled or its result sender was blocked")
			}
			assert.Empty(t, stdout.String())
			assert.Empty(t, stderr.String())
		})
	}
}

func TestStartUpdateCheck_BufferedResult(t *testing.T) {
	t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", "")
	results := startUpdateCheck(context.Background(), "dev")
	assert.Equal(t, 1, cap(results), "a late result must not block its sender")
	select {
	case result := <-results:
		assert.Nil(t, result.notify)
		require.NoError(t, result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("development build check did not return")
	}
}
