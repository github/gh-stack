package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func releaseResponse(tag string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
			`{"tag_name":%q,"assets":[{"name":"linux-amd64"}]}`, tag))),
		Header: make(http.Header),
	}
}

func newTestChecker(t *testing.T) checker {
	t.Helper()
	t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", "")
	dir := t.TempDir()
	executable := filepath.Join(dir, "gh-stack")
	require.NoError(t, os.WriteFile(executable, nil, 0600))
	c := checker{
		statePath:  filepath.Join(dir, "state", "state.yml"),
		executable: func() (string, error) { return executable, nil },
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return releaseResponse("v0.2.0"), nil
		})},
		token:       func() string { return "test-token" },
		now:         func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) },
		assetSuffix: "linux-amd64",
	}
	writeManifest(t, c, "v0.1.1", "")
	return c
}

func writeManifest(t *testing.T, c checker, tag, extra string) {
	t.Helper()
	executable, err := c.executable()
	require.NoError(t, err)
	data := fmt.Sprintf("owner: github\nname: gh-stack\nhost: github.com\ntag: %s\n%s", tag, extra)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(executable), "manifest.yml"), []byte(data), 0600))
}

func TestCheck_Versions(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		latest     string
		wantNotice bool
		wantCheck  bool
	}{
		{"newer release", "0.1.1", "v0.2.0", true, true},
		{"current", "0.2.0", "v0.2.0", false, true},
		{"installed ahead", "0.3.0", "v0.2.0", false, true},
		{"numeric ordering", "0.9.0", "v0.10.0", true, true},
		{"not lexical ordering", "0.10.0", "v0.9.0", false, true},
		{"installed v prefix", "v0.1.1", "v0.2.0", true, true},
		{"release without v prefix", "0.1.1", "0.2.0", true, true},
		{"build metadata", "0.2.0+local", "v0.2.0", false, true},
		{"development build", "dev", "v0.2.0", false, false},
		{"prerelease build", "0.2.0-rc.1", "v0.2.0", false, false},
		{"invalid build", "garbage", "v0.2.0", false, false},
		{"git describe build", "0.1.1-4-g12345678", "v0.2.0", false, false},
		{"invalid leading zeros", "00.1.1", "v0.2.0", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestChecker(t)
			writeManifest(t, c, tt.current, "")
			requests := 0
			c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return releaseResponse(tt.latest), nil
			})
			notice, err := c.check(context.Background(), tt.current)
			require.NoError(t, err)
			assert.Equal(t, tt.wantNotice, notice != nil)
			assert.Equal(t, tt.wantCheck, requests == 1)
		})
	}
}

func TestCheck_Request(t *testing.T) {
	for _, token := range []string{"", "test-token"} {
		t.Run("token="+token, func(t *testing.T) {
			c := newTestChecker(t)
			t.Setenv("GH_HOST", "enterprise.example.com")
			c.token = func() string { return token }
			c.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, http.MethodGet, req.Method)
				assert.Equal(t, latestReleaseURL, req.URL.String())
				assert.Equal(t, "application/vnd.github+json", req.Header.Get("Accept"))
				assert.Equal(t, "2022-11-28", req.Header.Get("X-GitHub-Api-Version"))
				assert.Equal(t, "gh-stack/0.1.1", req.Header.Get("User-Agent"))
				if token == "" {
					assert.Empty(t, req.Header.Get("Authorization"))
				} else {
					assert.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))
				}
				deadline, ok := req.Context().Deadline()
				assert.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), requestTimeout)
				assert.Greater(t, time.Until(deadline), time.Duration(0))
				return releaseResponse("v0.2.0"), nil
			})
			notice, err := c.check(context.Background(), "0.1.1")
			require.NoError(t, err)
			require.NotNil(t, notice)
		})
	}
}

func TestCheck_Installation(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{"local installation", "", ""},
		{"pinned", "owner: github\nname: gh-stack\nhost: github.com\ntag: v0.1.1\nispinned: true\n", ""},
		{"fork", "owner: someone\nname: gh-stack\nhost: github.com\ntag: v0.1.1\n", ""},
		{"another extension", "owner: github\nname: gh-other\nhost: github.com\ntag: v0.1.1\n", ""},
		{"enterprise host", "owner: github\nname: gh-stack\nhost: example.com\ntag: v0.1.1\n", ""},
		{"mismatched version", "owner: github\nname: gh-stack\nhost: github.com\ntag: v0.0.1\n", "does not match"},
		{"malformed manifest", "ispinned: [", "decoding extension manifest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestChecker(t)
			executable, err := c.executable()
			require.NoError(t, err)
			path := filepath.Join(filepath.Dir(executable), "manifest.yml")
			if tt.manifest == "" {
				require.NoError(t, os.Remove(path))
			} else {
				require.NoError(t, os.WriteFile(path, []byte(tt.manifest), 0600))
			}
			c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("ineligible installation made a network request")
				return nil, errors.New("unexpected request")
			})
			notice, err := c.check(context.Background(), "0.1.1")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Nil(t, notice)
			assert.NoFileExists(t, c.statePath)
		})
	}
}

func TestCheck_InstallationErrors(t *testing.T) {
	for _, kind := range []string{"executable lookup", "missing executable", "unreadable manifest"} {
		t.Run(kind, func(t *testing.T) {
			c := newTestChecker(t)
			switch kind {
			case "executable lookup":
				c.executable = func() (string, error) { return "", errors.New("lookup failed") }
			case "missing executable":
				c.executable = func() (string, error) { return filepath.Join(t.TempDir(), "missing"), nil }
			case "unreadable manifest":
				executable, err := c.executable()
				require.NoError(t, err)
				path := filepath.Join(filepath.Dir(executable), "manifest.yml")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0700))
			}
			notice, err := c.check(context.Background(), "0.1.1")
			require.Error(t, err)
			assert.Nil(t, notice)
			assert.NoFileExists(t, c.statePath)
		})
	}
}

func TestCheck_OptOut(t *testing.T) {
	for _, value := range []string{"1", "true", "0"} {
		t.Run(value, func(t *testing.T) {
			c := newTestChecker(t)
			t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", value)
			c.executable = func() (string, error) {
				t.Error("opt-out must not inspect the installation")
				return "", errors.New("unexpected lookup")
			}
			notice, err := c.check(context.Background(), "0.1.1")
			require.NoError(t, err)
			assert.Nil(t, notice)
			assert.NoFileExists(t, c.statePath)
			notice, err = Check(context.Background(), "0.1.1")
			require.NoError(t, err)
			assert.Nil(t, notice)
		})
	}
}

func TestCheck_DailyCadence(t *testing.T) {
	c := newTestChecker(t)
	now := c.now()
	start := now
	c.now = func() time.Time { return now }
	requests := 0
	c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return releaseResponse("v0.2.0"), nil
	})

	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	var out bytes.Buffer
	require.NoError(t, notice(&out))
	assert.Equal(t, "\nA new release of gh-stack is available: 0.1.1 -> 0.2.0\nTo upgrade, run: gh extension upgrade stack\n", out.String())

	for _, elapsed := range []time.Duration{time.Minute, checkInterval - time.Nanosecond} {
		now = start.Add(elapsed)
		freshChecker := c
		notice, err = freshChecker.check(context.Background(), "0.1.1")
		require.NoError(t, err)
		assert.Nil(t, notice)
		assert.Equal(t, 1, requests)
	}

	now = start.Add(checkInterval)
	notice, err = c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	assert.Equal(t, 2, requests)
	require.NoError(t, notice(io.Discard))
	s, err := readState(c.statePath, now)
	require.NoError(t, err)
	assert.Equal(t, now, s.LastCheckedAt)
	assert.Equal(t, now, s.LastNotifiedAt)
}

func TestCheck_PendingNotice(t *testing.T) {
	c := newTestChecker(t)
	now := c.now()
	start := now
	c.now = func() time.Time { return now }
	firstNotice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, firstNotice)
	s, err := readState(c.statePath, now)
	require.NoError(t, err)
	assert.True(t, s.LastNotifiedAt.IsZero())

	c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("pending notice must use cached metadata")
		return nil, errors.New("unexpected request")
	})
	now = now.Add(time.Hour)
	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	require.NoError(t, notice(io.Discard))
	s, err = readState(c.statePath, now)
	require.NoError(t, err)
	assert.Equal(t, start, s.LastCheckedAt)
	assert.Equal(t, now, s.LastNotifiedAt)

	var out bytes.Buffer
	require.NoError(t, firstNotice(&out))
	assert.Empty(t, out.String(), "a prepared notice must recheck the last notification")
}

func TestCheck_NotificationCadenceIndependentOfChecks(t *testing.T) {
	c := newTestChecker(t)
	now := c.now()
	require.NoError(t, writeState(c.statePath, state{
		LastCheckedAt:  now.Add(-checkInterval),
		LastNotifiedAt: now.Add(-time.Hour),
		LatestVersion:  "v0.1.2",
	}))
	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	assert.Nil(t, notice, "a newly checked release must not bypass the reminder cooldown")
	s, err := readState(c.statePath, now)
	require.NoError(t, err)
	assert.Equal(t, now, s.LastCheckedAt)
	assert.Equal(t, "v0.2.0", s.LatestVersion)

	c.now = func() time.Time { return now.Add(23 * time.Hour) }
	notice, err = c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	require.NoError(t, notice(io.Discard))
}

func TestCheck_UpgradedInstallation(t *testing.T) {
	c := newTestChecker(t)
	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	writeManifest(t, c, "v0.2.0", "")
	notice, err = c.check(context.Background(), "0.2.0")
	require.NoError(t, err)
	assert.Nil(t, notice)
}

func TestNotification_ExpiredCandidate(t *testing.T) {
	c := newTestChecker(t)
	now := c.now()
	c.now = func() time.Time { return now }
	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	now = now.Add(checkInterval)
	var out bytes.Buffer
	require.NoError(t, notice(&out))
	assert.Empty(t, out.String())
	s, err := readState(c.statePath, c.now())
	require.NoError(t, err)
	assert.True(t, s.LastNotifiedAt.IsZero())
}

func TestCheck_ReleaseMetadata(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		suffix     string
		wantNotice bool
		wantErr    bool
	}{
		{"draft", `{"tag_name":"v0.2.0","draft":true,"assets":[{"name":"linux-amd64"}]}`, "linux-amd64", false, false},
		{"prerelease", `{"tag_name":"v0.2.0-rc.1","prerelease":true,"assets":[{"name":"linux-amd64"}]}`, "linux-amd64", false, false},
		{"invalid tag", `{"tag_name":"latest"}`, "linux-amd64", false, true},
		{"prerelease tag", `{"tag_name":"v0.2.0-rc.1"}`, "linux-amd64", false, true},
		{"missing tag", `{}`, "linux-amd64", false, true},
		{"invalid JSON", `{`, "linux-amd64", false, true},
		{"no assets", `{"tag_name":"v0.2.0"}`, "linux-amd64", false, false},
		{"wrong platform", `{"tag_name":"v0.2.0","assets":[{"name":"darwin-arm64"}]}`, "linux-amd64", false, false},
		{"prefixed asset", `{"tag_name":"v0.2.0","assets":[{"name":"gh-stack-linux-amd64"}]}`, "linux-amd64", true, false},
		{"windows executable", `{"tag_name":"v0.2.0","assets":[{"name":"windows-amd64.exe"}]}`, "windows-amd64.exe", true, false},
		{"not a windows executable", `{"tag_name":"v0.2.0","assets":[{"name":"windows-amd64"}]}`, "windows-amd64.exe", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestChecker(t)
			c.assetSuffix = tt.suffix
			c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})
			notice, err := c.check(context.Background(), "0.1.1")
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantNotice, notice != nil)
			s, err := readState(c.statePath, c.now())
			require.NoError(t, err)
			assert.Equal(t, c.now(), s.LastAttemptedAt)
			if tt.wantErr {
				assert.True(t, s.LastCheckedAt.IsZero())
			} else {
				assert.Equal(t, c.now(), s.LastCheckedAt)
			}
			if !tt.wantNotice {
				assert.Empty(t, s.LatestVersion)
			}
		})
	}
}

func TestCheck_FailedAttemptsAreThrottled(t *testing.T) {
	for _, status := range []int{0, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := newTestChecker(t)
			now := c.now()
			start := now
			c.now = func() time.Time { return now }
			require.NoError(t, writeState(c.statePath, state{
				LastCheckedAt: start.Add(-checkInterval),
				LatestVersion: "v0.1.2",
			}))
			requests := 0
			c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				if status == 0 {
					return nil, errors.New("offline")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			notice, err := c.check(context.Background(), "0.1.1")
			require.Error(t, err)
			assert.Nil(t, notice)
			s, err := readState(c.statePath, c.now())
			require.NoError(t, err)
			assert.Equal(t, start, s.LastAttemptedAt)
			assert.Equal(t, start.Add(-checkInterval), s.LastCheckedAt)
			assert.Equal(t, "v0.1.2", s.LatestVersion)

			for _, elapsed := range []time.Duration{0, retryInterval - time.Nanosecond} {
				now = start.Add(elapsed)
				notice, err = c.check(context.Background(), "0.1.1")
				require.NoError(t, err)
				assert.Nil(t, notice, "expired metadata must not be delivered while waiting to retry")
				assert.Equal(t, 1, requests)
			}

			now = start.Add(retryInterval)
			c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				now = now.Add(time.Second)
				return releaseResponse("v0.2.0"), nil
			})
			notice, err = c.check(context.Background(), "0.1.1")
			require.NoError(t, err)
			require.NotNil(t, notice)
			assert.Equal(t, 2, requests)
			s, err = readState(c.statePath, now)
			require.NoError(t, err)
			assert.Equal(t, start.Add(retryInterval), s.LastAttemptedAt)
			assert.Equal(t, now, s.LastCheckedAt, "record completion, not the start of the request")
			assert.Equal(t, "v0.2.0", s.LatestVersion)
			require.NoError(t, notice(io.Discard))
		})
	}
}

func TestCheck_CanceledAttemptIsThrottled(t *testing.T) {
	c := newTestChecker(t)
	now := c.now()
	c.now = func() time.Time { return now }
	started := make(chan struct{})
	c.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.check(ctx, "0.1.1")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("check did not cancel")
	}
	c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("canceled attempt must still be throttled")
		return nil, errors.New("unexpected request")
	})
	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	assert.Nil(t, notice)
	s, err := readState(c.statePath, now)
	require.NoError(t, err)
	assert.Equal(t, now, s.LastAttemptedAt)
	assert.True(t, s.LastCheckedAt.IsZero())

	now = now.Add(retryInterval)
	c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return releaseResponse("v0.2.0"), nil
	})
	notice, err = c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
}

func TestCheck_InvalidStateRecovery(t *testing.T) {
	for _, data := range []string{
		"last_checked_at: [",
		"last_checked_at: invalid\n",
		"last_attempted_at: 2030-01-01T00:00:00Z\n",
		"last_checked_at: 2030-01-01T00:00:00Z\n",
		"last_notified_at: 2030-01-01T00:00:00Z\n",
		"latest_version: v0.2.0\n",
		"last_checked_at: 2026-01-15T12:00:00Z\nlatest_version: garbage\n",
	} {
		t.Run(data, func(t *testing.T) {
			c := newTestChecker(t)
			require.NoError(t, os.MkdirAll(filepath.Dir(c.statePath), 0700))
			require.NoError(t, os.WriteFile(c.statePath, []byte(data), 0600))
			notice, err := c.check(context.Background(), "0.1.1")
			require.ErrorIs(t, err, errInvalidState)
			require.NotNil(t, notice)
			require.NoError(t, notice(io.Discard))
			s, err := readState(c.statePath, c.now())
			require.NoError(t, err)
			assert.Equal(t, c.now(), s.LastNotifiedAt)
		})
	}
}

func TestCheck_StateErrors(t *testing.T) {
	for _, kind := range []string{"unreadable", "unwritable", "relative path"} {
		t.Run(kind, func(t *testing.T) {
			c := newTestChecker(t)
			switch kind {
			case "unreadable":
				require.NoError(t, os.MkdirAll(c.statePath, 0700))
			case "unwritable":
				require.NoError(t, os.WriteFile(filepath.Dir(c.statePath), nil, 0600))
			case "relative path":
				c.statePath = "state.yml"
			}
			c.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unusable state must not cause unthrottled network requests")
				return nil, errors.New("unexpected request")
			})
			notice, err := c.check(context.Background(), "0.1.1")
			require.Error(t, err)
			assert.Nil(t, notice)
		})
	}
}

func TestNotification_StateFailure(t *testing.T) {
	c := newTestChecker(t)
	notice, err := c.check(context.Background(), "0.1.1")
	require.NoError(t, err)
	require.NotNil(t, notice)
	require.NoError(t, os.Remove(c.statePath))
	require.NoError(t, os.Mkdir(c.statePath, 0700))
	var out bytes.Buffer
	require.Error(t, notice(&out))
	assert.Empty(t, out.String())
}

func TestReadState_YAML(t *testing.T) {
	c := newTestChecker(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(c.statePath), 0700))
	require.NoError(t, os.WriteFile(c.statePath, []byte(
		"last_attempted_at: 2026-01-15T11:59:00Z\n"+
			"last_checked_at: 2026-01-15T12:00:00Z\n"+
			"last_notified_at: 2026-01-15T11:00:00Z\n"+
			"latest_version: v0.2.0\n"), 0600))

	got, err := readState(c.statePath, c.now())
	require.NoError(t, err)
	assert.Equal(t, state{
		LastAttemptedAt: c.now().Add(-time.Minute),
		LastCheckedAt:   c.now(),
		LastNotifiedAt:  c.now().Add(-time.Hour),
		LatestVersion:   "v0.2.0",
	}, got)
}

func TestWriteState_ReplacesAndCleansUp(t *testing.T) {
	c := newTestChecker(t)
	for _, version := range []string{"v0.2.0", "v0.3.0"} {
		s := state{LastAttemptedAt: c.now(), LastCheckedAt: c.now(), LatestVersion: version}
		require.NoError(t, writeState(c.statePath, s))
		data, err := os.ReadFile(c.statePath)
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf(
			"last_attempted_at: 2026-01-15T12:00:00Z\nlast_checked_at: 2026-01-15T12:00:00Z\nlast_notified_at: 0001-01-01T00:00:00Z\nlatest_version: %s\n",
			version), string(data))
		got, err := readState(c.statePath, c.now())
		require.NoError(t, err)
		assert.Equal(t, s, got)
	}
	entries, err := os.ReadDir(filepath.Dir(c.statePath))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "state.yml", entries[0].Name())
	if runtime.GOOS != "windows" {
		info, err := os.Stat(c.statePath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestCheck_InstalledBinary(t *testing.T) {
	if os.Getenv("GH_STACK_UPDATE_TEST_HELPER") == "1" {
		notice, err := Check(context.Background(), "0.1.1")
		require.NoError(t, err)
		if notice != nil {
			require.NoError(t, notice(os.Stdout))
		}
		return
	}

	t.Setenv("GH_STACK_NO_UPDATE_NOTIFIER", "")
	t.Setenv("GH_STACK_UPDATE_TEST_HELPER", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GH_HOST", "enterprise.example.com")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:0")
	t.Setenv("NO_PROXY", "")

	executable, err := os.Executable()
	require.NoError(t, err)
	binary, err := os.ReadFile(executable)
	require.NoError(t, err)
	installed := filepath.Join(t.TempDir(), "gh-stack")
	if runtime.GOOS == "windows" {
		installed += ".exe"
	}
	require.NoError(t, os.WriteFile(installed, binary, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(installed), "manifest.yml"),
		[]byte("owner: github\nname: gh-stack\nhost: github.com\ntag: v0.1.1\nispinned: false\n"), 0600))

	statePath := filepath.Join(os.Getenv("XDG_STATE_HOME"), "gh", "gh-stack", "state.yml")
	require.NoError(t, writeState(statePath, state{LastCheckedAt: time.Now(), LatestVersion: "v0.2.0"}))

	for _, wantNotice := range []bool{true, false} {
		command := exec.Command(installed, "-test.run=^TestCheck_InstalledBinary$")
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
		assert.Equal(t, wantNotice, strings.Contains(string(output), "gh extension upgrade stack"), "%s", output)
	}
	s, err := readState(statePath, time.Now())
	require.NoError(t, err)
	assert.False(t, s.LastNotifiedAt.IsZero())
}
