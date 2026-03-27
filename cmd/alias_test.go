package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAliasCmd_ValidatesName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"default", "gs", false},
		{"alphanumeric", "gst2", false},
		{"with-hyphen", "my-stack", false},
		{"with-underscore", "my_stack", false},
		{"starts-with-digit", "2gs", true},
		{"has-spaces", "my stack", true},
		{"has-slash", "my/stack", true},
		{"empty", "", true},
		{"special-chars", "gs!", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, !tt.wantErr, validAliasName.MatchString(tt.input))
		})
	}
}

// skipWindows skips the current test on Windows since the alias command
// creates Unix shell scripts.
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("alias command uses shell scripts; not supported on Windows")
	}
}

// withTmpBinDir skips on Windows, overrides localBinDirFunc to use a temp
// directory, and restores it when the test completes.
func withTmpBinDir(t *testing.T) string {
	t.Helper()
	skipWindows(t)
	tmpDir := t.TempDir()
	orig := localBinDirFunc
	localBinDirFunc = func() (string, error) { return tmpDir, nil }
	t.Cleanup(func() { localBinDirFunc = orig })
	return tmpDir
}

// testAliasName is a name unlikely to collide with real commands on PATH.
const testAliasName = "ghstacktest"

func TestRunAlias_CreatesWrapperScript(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	err := runAlias(cfg, testAliasName, tmpDir)
	require.NoError(t, err)

	scriptPath := filepath.Join(tmpDir, testAliasName)
	data, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	assert.Equal(t, markedWrapperContent, string(data))

	info, err := os.Stat(scriptPath)
	require.NoError(t, err)
	assert.True(t, info.Mode()&0o111 != 0, "script should be executable")
}

func TestRunAlias_Idempotent(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	// First install
	require.NoError(t, runAlias(cfg, testAliasName, tmpDir))
	// Second install should succeed (idempotent)
	require.NoError(t, runAlias(cfg, testAliasName, tmpDir))
}

func TestRunAlias_RejectsExistingCommand(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	// "ls" exists on every Unix system
	err := runAlias(cfg, "ls", tmpDir)
	assert.ErrorIs(t, err, ErrInvalidArgs)
}

func TestRunAliasRemove_RemovesWrapper(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	require.NoError(t, runAlias(cfg, testAliasName, tmpDir))

	scriptPath := filepath.Join(tmpDir, testAliasName)
	require.FileExists(t, scriptPath)

	require.NoError(t, runAliasRemove(cfg, testAliasName, tmpDir))
	assert.NoFileExists(t, scriptPath)
}

func TestRunAliasRemove_RefusesNonOurScript(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	// Create a file that isn't our wrapper
	scriptPath := filepath.Join(tmpDir, testAliasName)
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hello\n"), 0o755))

	err := runAliasRemove(cfg, testAliasName, tmpDir)
	assert.Error(t, err)
	assert.FileExists(t, scriptPath)
}

func TestRunAliasRemove_ErrorsWhenNotFound(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	err := runAliasRemove(cfg, testAliasName, tmpDir)
	assert.Error(t, err)
}

func TestIsOurWrapper(t *testing.T) {
	tmpDir := t.TempDir()

	ourPath := filepath.Join(tmpDir, "ours")
	require.NoError(t, os.WriteFile(ourPath, []byte(markedWrapperContent), 0o755))
	assert.True(t, isOurWrapper(ourPath))

	otherPath := filepath.Join(tmpDir, "other")
	require.NoError(t, os.WriteFile(otherPath, []byte("#!/bin/sh\necho hi\n"), 0o755))
	assert.False(t, isOurWrapper(otherPath))

	assert.False(t, isOurWrapper(filepath.Join(tmpDir, "nope")))
}

func TestDirInPath(t *testing.T) {
	// Use a directory we know is in PATH on any platform.
	found := false
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dirInPath(dir) {
			found = true
			break
		}
	}
	assert.True(t, found, "expected at least one PATH entry to be found by dirInPath")
	assert.False(t, dirInPath("/nonexistent/path/that/should/not/exist"))
}

func TestAliasCmd_RemoveFlagWiring(t *testing.T) {
	tmpDir := withTmpBinDir(t)
	cfg, _, _ := config.NewTestConfig()

	// Install the alias first via runAlias so there's something to remove.
	require.NoError(t, runAlias(cfg, testAliasName, tmpDir))
	require.FileExists(t, filepath.Join(tmpDir, testAliasName))

	// Now exercise the cobra command with --remove to verify flag plumbing.
	cmd := AliasCmd(cfg)
	cmd.SetArgs([]string{"--remove", testAliasName})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(tmpDir, testAliasName))
}

func TestAliasCmd_WindowsReturnsError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}

	cfg, _, _ := config.NewTestConfig()

	cmd := AliasCmd(cfg)
	cmd.SetArgs([]string{testAliasName})
	assert.Error(t, cmd.Execute())
}

func TestValidateAliasName(t *testing.T) {
	cfg, _, _ := config.NewTestConfig()

	assert.NoError(t, validateAliasName(cfg, "gs"))
	assert.NoError(t, validateAliasName(cfg, "my-stack"))
	assert.ErrorIs(t, validateAliasName(cfg, ""), ErrInvalidArgs)
	assert.ErrorIs(t, validateAliasName(cfg, "2bad"), ErrInvalidArgs)
	assert.ErrorIs(t, validateAliasName(cfg, "has space"), ErrInvalidArgs)
}
