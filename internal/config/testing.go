package config

import (
	"os"

	"github.com/cli/go-gh/v2/pkg/term"
)

// NewTestConfig creates a Config suitable for testing with captured output buffers.
// Color functions are no-ops, and the config is non-interactive.
func NewTestConfig() (*Config, *os.File, *os.File) {
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	noop := func(s string) string { return s }

	cfg := &Config{
		Terminal:     term.FromEnv(),
		Out:          outW,
		Err:          errW,
		In:           os.Stdin,
		ColorSuccess: noop,
		ColorError:   noop,
		ColorWarning: noop,
		ColorBold:    noop,
		ColorBlue:    noop,
		ColorMagenta: noop,
		ColorCyan:    noop,
		ColorGray:    noop,
	}

	return cfg, outR, errR
}
