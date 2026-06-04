package cmd

import (
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestWatchCmd_Registration(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	cmd := WatchCmd(cfg)

	assert.Equal(t, "watch", cmd.Name())
	assert.NotEmpty(t, cmd.Short)
}

func TestRunWatch_RequiresInteractive(t *testing.T) {
	// NewTestConfig produces a non-interactive config.
	cfg, outR, errR := config.NewTestConfig()

	err := runWatch(cfg)
	assert.ErrorIs(t, err, ErrSilent)

	output := collectOutput(cfg, outR, errR)
	assert.Contains(t, output, "interactive terminal")
}
