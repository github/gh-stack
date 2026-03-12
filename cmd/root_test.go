package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootCmd_SubcommandRegistration(t *testing.T) {
	root := RootCmd()
	expected := []string{"init", "add", "checkout", "push", "sync", "unstack", "merge", "view", "rebase", "up", "down", "top", "bottom", "feedback"}

	registered := make(map[string]bool)
	for _, cmd := range root.Commands() {
		registered[cmd.Name()] = true
	}

	for _, name := range expected {
		assert.True(t, registered[name], "expected subcommand %q to be registered", name)
	}
}
