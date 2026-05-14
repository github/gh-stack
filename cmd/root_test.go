package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmd_SubcommandRegistration(t *testing.T) {
	root := RootCmd()
	expected := []string{"init", "add", "checkout", "push", "sync", "unstack", "merge", "view", "rebase", "up", "down", "top", "bottom", "alias", "feedback", "submit"}

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
