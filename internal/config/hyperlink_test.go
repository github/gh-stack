package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSupportsHyperlinks(t *testing.T) {
	tests := []struct {
		name  string
		isTTY bool
		env   map[string]string
		want  bool
	}{
		{
			name: "force enabled without TTY",
			env:  map[string]string{"GH_STACK_HYPERLINKS": "1"},
			want: true,
		},
		{
			name:  "force disabled in supported terminal",
			isTTY: true,
			env:   map[string]string{"GH_STACK_HYPERLINKS": "0", "TERM_PROGRAM": "iTerm.app"},
		},
		{
			name: "supported terminal without TTY",
			env:  map[string]string{"TERM_PROGRAM": "iTerm.app"},
		},
		{
			name:  "tmux overrides outer terminal",
			isTTY: true,
			env:   map[string]string{"TMUX": "/tmp/tmux-501/default,1,0", "TERM_PROGRAM": "iTerm.app"},
		},
		{
			name:  "Apple Terminal unsupported",
			isTTY: true,
			env:   map[string]string{"TERM_PROGRAM": "Apple_Terminal"},
		},
		{
			name:  "unknown terminal unsupported",
			isTTY: true,
			env:   map[string]string{"TERM": "xterm-256color"},
		},
		{
			name:  "Konsole defaults unsupported",
			isTTY: true,
			env:   map[string]string{"KONSOLE_VERSION": "210401"},
		},
		{
			name:  "iTerm supported",
			isTTY: true,
			env:   map[string]string{"TERM_PROGRAM": "iTerm.app"},
			want:  true,
		},
		{
			name:  "VTE 0.50 supported",
			isTTY: true,
			env:   map[string]string{"VTE_VERSION": "5000"},
			want:  true,
		},
		{
			name:  "Windows Terminal supported",
			isTTY: true,
			env:   map[string]string{"WT_SESSION": "session-id"},
			want:  true,
		},
		{
			name:  "kitty supported",
			isTTY: true,
			env:   map[string]string{"TERM": "xterm-kitty"},
			want:  true,
		},
	}

	envVars := []string{
		"GH_STACK_HYPERLINKS",
		"KITTY_WINDOW_ID",
		"KONSOLE_VERSION",
		"STY",
		"TERM",
		"TERM_PROGRAM",
		"TMUX",
		"VTE_VERSION",
		"WT_SESSION",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range envVars {
				t.Setenv(name, "")
			}
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			assert.Equal(t, tt.want, supportsHyperlinks(tt.isTTY))
		})
	}
}

func TestPRLinkFormatting(t *testing.T) {
	const url = "https://github.com/o/r/pull/42"
	tests := []struct {
		name           string
		forceHyperlink string
		url            string
		want           string
	}{
		{
			name:           "OSC 8 hyperlink",
			forceHyperlink: "1",
			url:            url,
			want:           "\x1b]8;;https://github.com/o/r/pull/42\x1b\\#42\x1b]8;;\x1b\\",
		},
		{
			name:           "plain URL fallback",
			forceHyperlink: "0",
			url:            url,
			want:           "#42 (https://github.com/o/r/pull/42)",
		},
		{
			name:           "missing URL",
			forceHyperlink: "0",
			want:           "#42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_STACK_HYPERLINKS", tt.forceHyperlink)
			cfg := &Config{}
			assert.Equal(t, tt.want, cfg.PRLink(42, tt.url))
		})
	}
}
