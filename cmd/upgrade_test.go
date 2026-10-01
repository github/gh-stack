package cmd

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunUpgrade(t *testing.T) {
	tests := []struct {
		name    string
		dryRun  bool
		force   bool
		wantRun bool
		want    []string
	}{
		{
			name: "upgrade",
			want: []string{"extension", "upgrade", "stack"},
		},
		{
			name:    "dry run",
			dryRun:  true,
			wantRun: true,
			want:    []string{"extension", "upgrade", "stack", "--dry-run"},
		},
		{
			name:  "force bypasses pin",
			force: true,
			want:  []string{"extension", "upgrade", "stack", "--force"},
		},
		{
			name:    "dry run with force",
			dryRun:  true,
			force:   true,
			wantRun: true,
			want:    []string{"extension", "upgrade", "stack", "--dry-run", "--force"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, _ := config.NewTestConfig()
			restoreUpgradeSeams(t)

			upgradeGOOS = "linux"
			upgradeGHPath = func() (string, error) { return "/test/bin/gh", nil }

			var gotPath string
			var gotArgs []string
			usedRun := false
			upgradeRun = func(_ *config.Config, path string, args []string) error {
				usedRun = true
				gotPath = path
				gotArgs = append([]string(nil), args...)
				return nil
			}
			upgradeExec = func(path string, argv, env []string) error {
				gotPath = path
				gotArgs = append([]string(nil), argv[1:]...)
				assert.Equal(t, path, argv[0])
				assert.Equal(t, os.Environ(), env)
				return nil
			}

			require.NoError(t, runUpgrade(cfg, tt.dryRun, tt.force))
			assert.Equal(t, tt.wantRun, usedRun)
			assert.Equal(t, "/test/bin/gh", gotPath)
			assert.Equal(t, tt.want, gotArgs)
		})
	}
}

func TestRunUpgradeWindows(t *testing.T) {
	cfg, _, errR := config.NewTestConfig()
	restoreUpgradeSeams(t)

	upgradeGOOS = "windows"
	upgradeGHPath = func() (string, error) {
		t.Fatal("actual Windows upgrade must not locate gh")
		return "", nil
	}

	err := runUpgrade(cfg, false, true)
	assert.ErrorIs(t, err, ErrSilent)

	require.NoError(t, cfg.Err.Close())
	output, readErr := io.ReadAll(errR)
	require.NoError(t, readErr)
	assert.Contains(t, string(output), "Run `gh extension upgrade stack` directly.")
}

func TestRunUpgradeWindowsDryRun(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	restoreUpgradeSeams(t)

	upgradeGOOS = "windows"
	upgradeGHPath = func() (string, error) { return `C:\bin\gh.exe`, nil }
	upgradeRun = func(_ *config.Config, path string, args []string) error {
		assert.Equal(t, `C:\bin\gh.exe`, path)
		assert.Equal(t, []string{"extension", "upgrade", "stack", "--dry-run"}, args)
		return nil
	}

	require.NoError(t, runUpgrade(cfg, true, false))
}

func TestRunUpgradeGHPathError(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()
	restoreUpgradeSeams(t)

	upgradeGOOS = "linux"
	upgradeGHPath = func() (string, error) { return "", errors.New("not found") }

	err := runUpgrade(cfg, false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "locating gh executable")
}

func restoreUpgradeSeams(t *testing.T) {
	t.Helper()
	originalGOOS := upgradeGOOS
	originalGHPath := upgradeGHPath
	originalRun := upgradeRun
	originalExec := upgradeExec
	t.Cleanup(func() {
		upgradeGOOS = originalGOOS
		upgradeGHPath = originalGHPath
		upgradeRun = originalRun
		upgradeExec = originalExec
	})
}
