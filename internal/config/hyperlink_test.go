package config

import (
	"testing"

	"github.com/cli/go-gh/v2/pkg/repository"
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

func TestStackLinkFormatting(t *testing.T) {
	tests := []struct {
		name, force, host, owner, repo string
		number                         int
		want                           string
	}{
		{"hyperlink", "1", "github.com", "o", "r", 7, "\x1b]8;;https://github.com/o/r/stack/7\x1b\\#7\x1b]8;;\x1b\\"},
		{"fallback", "0", "github.com", "o", "r", 7, "#7 (https://github.com/o/r/stack/7)"},
		{"custom host", "0", "github.example.com", "o", "r", 7, "#7 (https://github.example.com/o/r/stack/7)"},
		{"default host", "0", "", "o", "r", 7, "#7 (https://github.com/o/r/stack/7)"},
		{"missing owner", "1", "github.com", "", "r", 7, "#7"},
		{"missing repo", "1", "github.com", "o", "", 7, "#7"},
		{"unknown number", "1", "github.com", "o", "r", 0, "#0"},
		{"invalid number", "1", "github.com", "o", "r", -1, "#-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_STACK_HYPERLINKS", tt.force)
			cfg := &Config{RepoOverride: &repository.Repository{Host: tt.host, Owner: tt.owner, Name: tt.repo}}
			assert.Equal(t, tt.want, cfg.StackLink(tt.number))
			if tt.owner == "" || tt.repo == "" || tt.number <= 0 {
				assert.Empty(t, cfg.StackURL(tt.number))
			}
		})
	}
}

func TestStackURL_RepositoryUnavailable(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_STACK_HYPERLINKS", "1")
	cfg := &Config{}
	assert.Empty(t, cfg.StackURL(7))
	assert.Equal(t, "#7", cfg.StackLink(7))
}

func TestTerminalLinkColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("GH_STACK_HYPERLINKS", "1")
	assert.Equal(t,
		"\x1b[4m\x1b]8;;https://github.com/o/r/stack/7\x1b\\#7\x1b]8;;\x1b\\\x1b[24m",
		New().TerminalLink("#7", "https://github.com/o/r/stack/7"))
}

func TestTerminalLink(t *testing.T) {
	const url = "https://github.com/o/r/stack/7"
	tests := []struct {
		name, force, url, want string
	}{
		{"enabled", "1", url, "\x1b]8;;https://github.com/o/r/stack/7\x1b\\7\x1b]8;;\x1b\\"},
		{"disabled", "0", url, "7"},
		{"missing URL", "1", "", "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_STACK_HYPERLINKS", tt.force)
			assert.Equal(t, tt.want, (&Config{}).TerminalLink("7", tt.url))
		})
	}
}
