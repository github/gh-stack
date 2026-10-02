package cmd

import (
	"bytes"
	"errors"
	"testing"

	stackupdate "github.com/github/gh-stack/internal/update"
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

func TestFinalizeExecutionPrintsNoticeAfterCommandFailure(t *testing.T) {
	var stderr bytes.Buffer
	check := &fakeUpdateCheck{
		notice: &stackupdate.Notice{
			CurrentVersion: "0.1.0",
			LatestVersion:  "0.1.1",
			URL:            "https://github.com/github/gh-stack/releases/tag/v0.1.1",
		},
	}

	err := errors.New("command failed")
	assert.Equal(t, 1, finalizeExecution(err, check, &stderr))
	output := stderr.String()
	assert.Less(t, bytes.Index([]byte(output), []byte("command failed")), bytes.Index([]byte(output), []byte("A new version")))
	assert.Contains(t, output, "gh extension upgrade stack")
	assert.True(t, check.marked)
}

func TestFinalizeExecutionPreservesTypedExitCode(t *testing.T) {
	var stderr bytes.Buffer
	check := &fakeUpdateCheck{
		notice: &stackupdate.Notice{
			CurrentVersion: "0.1.0",
			LatestVersion:  "0.1.1",
		},
	}

	assert.Equal(t, ErrConflict.Code, finalizeExecution(ErrConflict, check, &stderr))
	assert.NotContains(t, stderr.String(), ErrConflict.Error())
	assert.Contains(t, stderr.String(), "gh extension upgrade stack")
}

func TestFinalizeExecutionLeavesSuccessfulUpdatesToGitHubCLI(t *testing.T) {
	var stderr bytes.Buffer
	check := &fakeUpdateCheck{
		notice: &stackupdate.Notice{
			CurrentVersion: "0.1.0",
			LatestVersion:  "0.1.1",
		},
	}

	assert.Zero(t, finalizeExecution(nil, check, &stderr))
	assert.Empty(t, stderr.String())
	assert.False(t, check.marked)
}

func TestEligibleUpdateInvocation(t *testing.T) {
	assert.True(t, eligibleUpdateInvocation([]string{"rebase"}))
	assert.True(t, eligibleUpdateInvocation([]string{"view", "--no-interactive"}))
	assert.False(t, eligibleUpdateInvocation(nil))
	assert.False(t, eligibleUpdateInvocation([]string{"--help"}))
	assert.False(t, eligibleUpdateInvocation([]string{"rebase", "--help"}))
	assert.False(t, eligibleUpdateInvocation([]string{"--version"}))
	assert.False(t, eligibleUpdateInvocation([]string{"version"}))
}

type fakeUpdateCheck struct {
	notice *stackupdate.Notice
	marked bool
}

func (f *fakeUpdateCheck) Result() *stackupdate.Notice {
	return f.notice
}

func (f *fakeUpdateCheck) MarkNotified(stackupdate.Notice) {
	f.marked = true
}
