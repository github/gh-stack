package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckerCheck(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		currentVersion string
		latestVersion  string
		wantNotice     bool
	}{
		{name: "older stable release", currentVersion: "0.1.0", latestVersion: "v0.1.1", wantNotice: true},
		{name: "current release", currentVersion: "0.1.1", latestVersion: "v0.1.1"},
		{name: "newer installed release", currentVersion: "0.2.0", latestVersion: "v0.1.1"},
		{name: "newer installed prerelease", currentVersion: "0.2.0-beta.1", latestVersion: "v0.1.1"},
		{name: "development build", currentVersion: "dev", latestVersion: "v0.1.1"},
		{name: "invalid installed version", currentVersion: "not-a-version", latestVersion: "v0.1.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetches := 0
			checker := NewChecker(Options{
				CurrentVersion: tt.currentVersion,
				StatePath:      filepath.Join(t.TempDir(), "update.json"),
				Now:            func() time.Time { return now },
				FetchLatest: func(context.Context) (Release, error) {
					fetches++
					return Release{Version: tt.latestVersion, URL: "https://github.com/github/gh-stack/releases/tag/v0.1.1"}, nil
				},
			})

			notice, err := checker.Check(context.Background())
			require.NoError(t, err)
			if tt.wantNotice {
				require.NotNil(t, notice)
				assert.Equal(t, "0.1.0", notice.CurrentVersion)
				assert.Equal(t, "0.1.1", notice.LatestVersion)
			} else {
				assert.Nil(t, notice)
			}

			if tt.currentVersion == "dev" || tt.currentVersion == "not-a-version" {
				assert.Zero(t, fetches)
			} else {
				assert.Equal(t, 1, fetches)
			}
		})
	}
}

func TestCheckerRateLimitsChecksAndReminders(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "update.json")
	fetches := 0
	newChecker := func() *Checker {
		return NewChecker(Options{
			CurrentVersion: "0.1.0",
			StatePath:      statePath,
			Now:            func() time.Time { return now },
			FetchLatest: func(context.Context) (Release, error) {
				fetches++
				return Release{Version: "v0.1.1", URL: "https://github.com/github/gh-stack/releases/tag/v0.1.1"}, nil
			},
		})
	}

	first, err := newChecker().Check(context.Background())
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := newChecker().Check(context.Background())
	require.NoError(t, err)
	require.NotNil(t, second, "an undisplayed notice must remain eligible")
	assert.Equal(t, 1, fetches)

	require.NoError(t, newChecker().MarkNotified(*first))
	acknowledged, err := newChecker().Check(context.Background())
	require.NoError(t, err)
	assert.Nil(t, acknowledged)

	now = now.Add(25 * time.Hour)
	third, err := newChecker().Check(context.Background())
	require.NoError(t, err)
	require.NotNil(t, third)
	assert.Equal(t, 2, fetches)
}

func TestCheckerIgnoresReleaseFailures(t *testing.T) {
	checker := NewChecker(Options{
		CurrentVersion: "0.1.0",
		StatePath:      filepath.Join(t.TempDir(), "update.json"),
		FetchLatest: func(context.Context) (Release, error) {
			return Release{}, errors.New("offline")
		},
	})

	notice, err := checker.Check(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, notice)
}

func TestShouldCheck(t *testing.T) {
	base := Eligibility{
		CurrentVersion: "0.1.0",
		StdoutTTY:      true,
		StderrTTY:      true,
	}

	assert.True(t, ShouldCheck(base))

	tests := []Eligibility{
		{CurrentVersion: "dev", StdoutTTY: true, StderrTTY: true},
		{CurrentVersion: "0.1.0", StdoutTTY: false, StderrTTY: true},
		{CurrentVersion: "0.1.0", StdoutTTY: true, StderrTTY: false},
		{CurrentVersion: "0.1.0", StdoutTTY: true, StderrTTY: true, CI: true},
		{CurrentVersion: "0.1.0", StdoutTTY: true, StderrTTY: true, Codespaces: true},
		{CurrentVersion: "0.1.0", StdoutTTY: true, StderrTTY: true, Disabled: true},
	}
	for _, eligibility := range tests {
		assert.False(t, ShouldCheck(eligibility))
	}
}

func TestFormatNotice(t *testing.T) {
	got := FormatNotice(Notice{
		CurrentVersion: "0.1.0",
		LatestVersion:  "0.1.1",
		URL:            "https://github.com/github/gh-stack/releases/tag/v0.1.1",
	})

	assert.Equal(t, "\nA new version of gh-stack is available: 0.1.0 -> 0.1.1.\nRun `gh extension upgrade stack` to update.\nhttps://github.com/github/gh-stack/releases/tag/v0.1.1\n", got)
}

func TestAsyncCheckDoesNotBlock(t *testing.T) {
	fetchStarted := make(chan struct{})
	releaseFetch := make(chan Release)
	checker := NewChecker(Options{
		CurrentVersion: "0.1.0",
		StatePath:      filepath.Join(t.TempDir(), "update.json"),
		FetchLatest: func(context.Context) (Release, error) {
			close(fetchStarted)
			return <-releaseFetch, nil
		},
	})

	check := Start(context.Background(), checker)
	<-fetchStarted
	assert.Nil(t, check.Result())
	close(releaseFetch)
}
