package config

import (
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"
)

// tokenForHost returns the auth token for the given host, using the
// test override if set or falling back to the real auth.TokenForHost.
func (cfg *Config) tokenForHost(host string) (string, string) {
	if cfg.TokenForHostFn != nil {
		return cfg.TokenForHostFn(host)
	}
	return auth.TokenForHost(host)
}

// IsPersonalAccessToken reports whether the active token for the current
// repository's host is a personal access token (classic or fine-grained)
// rather than an OAuth token from `gh auth login`.
//
// Token prefix conventions:
//
//	gho_        → OAuth token (supported)
//	ghs_        → GitHub App installation token (supported)
//	ghp_        → Classic personal access token (NOT supported)
//	github_pat_ → Fine-grained personal access token (NOT supported)
func (cfg *Config) IsPersonalAccessToken() bool {
	host := cfg.RepoHost()
	if host == "" {
		return false
	}
	return cfg.isPersonalAccessTokenForHost(host)
}

// isPersonalAccessTokenForHost checks the token prefix for the given host.
func (cfg *Config) isPersonalAccessTokenForHost(host string) bool {
	token, _ := cfg.tokenForHost(host)
	if token == "" {
		return false
	}
	return strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "github_pat_")
}

// RepoHost returns the GitHub host for the current repository, or an empty
// string if it cannot be determined (e.g. not inside a git repo).
func (cfg *Config) RepoHost() string {
	repo, err := cfg.Repo()
	if err != nil {
		return ""
	}
	return repo.Host
}

// WarnIfPAT checks whether the active token is a personal access token and,
// if so, prints a warning explaining that PATs are not supported by gh stack.
// Returns true when a PAT is detected.
func (cfg *Config) WarnIfPAT() bool {
	if !cfg.IsPersonalAccessToken() {
		return false
	}
	cfg.Warningf("Personal access tokens are not supported by gh stack during private preview")
	cfg.Printf("  Run %s to authenticate with OAuth instead.", cfg.ColorCyan("gh auth login"))
	return true
}
