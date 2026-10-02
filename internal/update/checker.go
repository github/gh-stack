package update

import (
	"context"
	"fmt"
	"strings"
	"time"

	version "github.com/hashicorp/go-version"
)

const defaultInterval = 24 * time.Hour

type Release struct {
	Version string
	URL     string
}

type Notice struct {
	CurrentVersion string
	LatestVersion  string
	URL            string
}

type Eligibility struct {
	CurrentVersion string
	StdoutTTY      bool
	StderrTTY      bool
	CI             bool
	Codespaces     bool
	Disabled       bool
}

func ShouldCheck(e Eligibility) bool {
	if e.CurrentVersion == "" || e.CurrentVersion == "dev" {
		return false
	}
	return e.StdoutTTY && e.StderrTTY && !e.CI && !e.Codespaces && !e.Disabled
}

type Options struct {
	CurrentVersion string
	StatePath      string
	Now            func() time.Time
	FetchLatest    func(context.Context) (Release, error)
	Interval       time.Duration
}

type Checker struct {
	currentVersion *version.Version
	currentDisplay string
	statePath      string
	now            func() time.Time
	fetchLatest    func(context.Context) (Release, error)
	interval       time.Duration
}

func NewChecker(opts Options) *Checker {
	current, _ := version.NewVersion(opts.CurrentVersion)
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Checker{
		currentVersion: current,
		currentDisplay: strings.TrimPrefix(opts.CurrentVersion, "v"),
		statePath:      opts.StatePath,
		now:            now,
		fetchLatest:    opts.FetchLatest,
		interval:       interval,
	}
}

func (c *Checker) Check(ctx context.Context) (*Notice, error) {
	if c.currentVersion == nil || c.fetchLatest == nil {
		return nil, nil
	}

	now := c.now()
	state, _ := loadState(c.statePath)
	if state == nil {
		state = &checkState{}
	}

	if state.CheckedAt.IsZero() || now.Sub(state.CheckedAt) >= c.interval {
		release, err := c.fetchLatest(ctx)
		if err != nil {
			return nil, nil
		}
		latest, err := version.NewVersion(release.Version)
		if err != nil {
			return nil, nil
		}
		state.CheckedAt = now
		state.LatestVersion = latest.String()
		state.ReleaseURL = release.URL
	}

	latest, err := version.NewVersion(state.LatestVersion)
	if err != nil || !latest.GreaterThan(c.currentVersion) {
		_ = saveState(c.statePath, state)
		return nil, nil
	}

	if state.NotifiedVersion == latest.String() &&
		!state.NotifiedAt.IsZero() &&
		now.Sub(state.NotifiedAt) < c.interval {
		_ = saveState(c.statePath, state)
		return nil, nil
	}

	_ = saveState(c.statePath, state)

	return &Notice{
		CurrentVersion: c.currentDisplay,
		LatestVersion:  latest.String(),
		URL:            state.ReleaseURL,
	}, nil
}

func (c *Checker) MarkNotified(notice Notice) error {
	state, err := loadState(c.statePath)
	if err != nil {
		return nil
	}
	state.NotifiedAt = c.now()
	state.NotifiedVersion = notice.LatestVersion
	return saveState(c.statePath, state)
}

func FormatNotice(notice Notice) string {
	return fmt.Sprintf(
		"\nA new version of gh-stack is available: %s -> %s.\nRun `gh extension upgrade stack` to update.\n%s\n",
		notice.CurrentVersion,
		notice.LatestVersion,
		notice.URL,
	)
}
