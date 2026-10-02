package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	ghconfig "github.com/cli/go-gh/v2/pkg/config"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	checkInterval    = 24 * time.Hour
	retryInterval    = 15 * time.Minute
	requestTimeout   = 2 * time.Second
	latestReleaseURL = "https://api.github.com/repos/github/gh-stack/releases/latest"
)

// Notification records and writes a pending notice after a command finishes.
type Notification func(io.Writer) error

// Enabled excludes opt-outs and builds that cannot be compared to stable releases.
func Enabled(version string) bool {
	return os.Getenv("GH_STACK_NO_UPDATE_NOTIFIER") == "" && stableVersion(version) != ""
}

// Check returns a pending notice without displaying it. A recovered state error
// may accompany a notice so callers can report diagnostics without losing it.
func Check(ctx context.Context, version string) (Notification, error) {
	if !Enabled(version) {
		return nil, nil
	}

	assetSuffix := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		assetSuffix += ".exe"
	}
	c := checker{
		statePath:  filepath.Join(ghconfig.StateDir(), "gh-stack", "state.yml"),
		executable: os.Executable,
		client:     &http.Client{Timeout: requestTimeout},
		token: func() string {
			token, _ := auth.TokenForHost("github.com")
			return token
		},
		now:         time.Now,
		assetSuffix: assetSuffix,
	}
	return c.check(ctx, version)
}

type checker struct {
	statePath   string
	executable  func() (string, error)
	client      *http.Client
	token       func() string
	now         func() time.Time
	assetSuffix string
}

type state struct {
	LastAttemptedAt time.Time `yaml:"last_attempted_at,omitempty"`
	LastCheckedAt   time.Time `yaml:"last_checked_at"`
	LastNotifiedAt  time.Time `yaml:"last_notified_at"`
	LatestVersion   string    `yaml:"latest_version,omitempty"`
}

var errInvalidState = errors.New("invalid update notification state")

func (c checker) check(ctx context.Context, version string) (Notification, error) {
	if !Enabled(version) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := stableVersion(version)
	eligible, err := c.eligible(current)
	if err != nil || !eligible {
		return nil, err
	}
	if !filepath.IsAbs(c.statePath) {
		return nil, fmt.Errorf("update state path must be absolute: %s", c.statePath)
	}

	now := c.now()
	s, err := readState(c.statePath, now)
	var diagnostic error
	switch {
	case err == nil, errors.Is(err, os.ErrNotExist):
	case errors.Is(err, errInvalidState):
		diagnostic = err
		s = state{}
	default:
		return nil, err
	}

	if (s.LastCheckedAt.IsZero() || now.Sub(s.LastCheckedAt) >= checkInterval) &&
		(s.LastAttemptedAt.IsZero() || now.Sub(s.LastAttemptedAt) >= retryInterval) {
		// Throttle interrupted attempts without refreshing or discarding
		// the last successful result.
		s.LastAttemptedAt = now
		if err := writeState(c.statePath, s); err != nil {
			return nil, errors.Join(diagnostic, err)
		}
		latest, err := c.fetchLatest(ctx, current)
		if err != nil {
			return nil, errors.Join(diagnostic, err)
		}
		now = c.now()
		s.LastCheckedAt = now
		s.LatestVersion = latest
		if err := writeState(c.statePath, s); err != nil {
			return nil, errors.Join(diagnostic, err)
		}
	}

	if !s.noticeDue(current, now) {
		return nil, diagnostic
	}
	return func(out io.Writer) error {
		now := c.now()
		// Re-read before delivery so a previously prepared notice cannot
		// overwrite a later check or reminder.
		s, err := readState(c.statePath, now)
		if err != nil {
			return err
		}
		if !s.noticeDue(current, now) {
			return nil
		}
		s.LastNotifiedAt = now
		if err := writeState(c.statePath, s); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out,
			"\nA new release of gh-stack is available: %s -> %s\nTo upgrade, run: gh extension upgrade stack\n",
			strings.TrimPrefix(current, "v"), strings.TrimPrefix(s.LatestVersion, "v"))
		return err
	}, diagnostic
}

func (s state) noticeDue(current string, now time.Time) bool {
	return s.LatestVersion != "" &&
		semver.Compare(s.LatestVersion, current) > 0 &&
		now.Sub(s.LastCheckedAt) < checkInterval &&
		(s.LastNotifiedAt.IsZero() || now.Sub(s.LastNotifiedAt) >= checkInterval)
}

func stableVersion(version string) string {
	version = "v" + strings.TrimPrefix(version, "v")
	if !semver.IsValid(version) || semver.Prerelease(version) != "" {
		return ""
	}
	return version
}

func (c checker) eligible(current string) (bool, error) {
	executable, err := c.executable()
	if err != nil {
		return false, fmt.Errorf("locating installed executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return false, fmt.Errorf("resolving installed executable: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "manifest.yml"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading extension manifest: %w", err)
	}
	var manifest struct {
		Owner    string
		Name     string
		Host     string
		Tag      string
		IsPinned bool
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("decoding extension manifest: %w", err)
	}
	if !strings.EqualFold(manifest.Host, "github.com") ||
		!strings.EqualFold(manifest.Owner, "github") ||
		!strings.EqualFold(manifest.Name, "gh-stack") || manifest.IsPinned {
		return false, nil
	}
	installed := stableVersion(manifest.Tag)
	if installed == "" || semver.Compare(installed, current) != 0 {
		return false, fmt.Errorf("extension manifest tag %q does not match running version %q", manifest.Tag, current)
	}
	return true, nil
}

func (c checker) fetchLatest(ctx context.Context, current string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gh-stack/"+strings.TrimPrefix(current, "v"))
	if token := c.token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest gh-stack release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching latest gh-stack release: HTTP %d", resp.StatusCode)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool
		Prerelease bool
		Assets     []struct{ Name string }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding latest gh-stack release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return "", nil
	}
	latest := stableVersion(release.Tag)
	if latest == "" {
		return "", fmt.Errorf("latest gh-stack release has an invalid stable version: %q", release.Tag)
	}
	for _, asset := range release.Assets {
		if strings.HasSuffix(asset.Name, c.assetSuffix) {
			return latest, nil
		}
	}
	return "", nil
}

func readState(path string, now time.Time) (state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return state{}, fmt.Errorf("reading update state: %w", err)
	}
	var s state
	if err := yaml.Unmarshal(data, &s); err != nil {
		return state{}, fmt.Errorf("%w: %w", errInvalidState, err)
	}
	if s.LastAttemptedAt.After(now) || s.LastCheckedAt.After(now) || s.LastNotifiedAt.After(now) {
		return state{}, fmt.Errorf("%w: timestamp is in the future", errInvalidState)
	}
	if s.LatestVersion != "" && (stableVersion(s.LatestVersion) != s.LatestVersion || s.LastCheckedAt.IsZero()) {
		return state{}, fmt.Errorf("%w: invalid cached release", errInvalidState)
	}
	return s, nil
}

func writeState(path string, s state) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding update state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating update state directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*.yml")
	if err != nil {
		return fmt.Errorf("creating update state file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing update state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing update state: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replacing update state: %w", err)
	}
	return nil
}
