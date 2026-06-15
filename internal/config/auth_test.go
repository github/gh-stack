package config

import (
	"io"
	"testing"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/stretchr/testify/assert"
)

// testRepo is a fake repository used in tests to avoid depending on the
// real git repo context (which may not exist in CI).
var testRepo = &repository.Repository{Host: "github.com", Owner: "o", Name: "r"}

func TestIsPersonalAccessToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"oauth token", "gho_abc123", false},
		{"app installation token", "ghs_abc123", false},
		{"classic PAT", "ghp_abc123", true},
		{"fine-grained PAT", "github_pat_abc123", true},
		{"empty token", "", false},
		{"unknown prefix", "some_other_token", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				TokenForHostFn: func(string) (string, string) { return tt.token, "test" },
			}
			got := cfg.isPersonalAccessTokenForHost("github.com")
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWarnIfPAT_DetectsPAT(t *testing.T) {
	cfg, _, errR := NewTestConfig()
	cfg.RepoOverride = testRepo
	cfg.TokenForHostFn = func(string) (string, string) { return "ghp_classic_pat_token", "test" }

	result := cfg.WarnIfPAT()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.True(t, result)
	assert.Contains(t, output, "Personal access tokens are not supported by gh stack")
	assert.Contains(t, output, "gh auth login")
}

func TestWarnIfPAT_IgnoresOAuth(t *testing.T) {
	cfg, _, errR := NewTestConfig()
	cfg.RepoOverride = testRepo
	cfg.TokenForHostFn = func(string) (string, string) { return "gho_oauth_token", "test" }

	result := cfg.WarnIfPAT()

	cfg.Err.Close()
	errOut, _ := io.ReadAll(errR)
	output := string(errOut)

	assert.False(t, result)
	assert.Empty(t, output)
}
