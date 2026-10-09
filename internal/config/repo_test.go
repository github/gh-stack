package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	runFakeSSHIfRequested()
	os.Exit(m.Run())
}

func TestRepoResolvesRemoteUsingSSHHostAlias(t *testing.T) {
	// Given ssh maps github.com-work to github.com
	stubSSH(t, map[string]string{"github.com-work": "github.com"})
	// And github.com is a known host
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("GH_REPO", "")
	// And origin uses the alias
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "remote", "add", "origin", "git@github.com-work:org/repo.git")
	t.Chdir(dir)

	// When the repository is resolved
	config := &Config{}
	repo, err := config.Repo()

	// Then it is the GitHub repository behind the alias
	require.NoError(t, err)
	assert.Equal(t, "github.com", repo.Host)
	assert.Equal(t, "org", repo.Owner)
	assert.Equal(t, "repo", repo.Name)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// The fake ssh below is adapted from github.com/cli/go-gh/v2 internal/testutils/ssh_stub.go
// Maybe go-gh can export a similar ssh stub for testing purposes, but for now we include our own.

const fakeSSHHostnamesEnv = "GH_STACK_TEST_FAKE_SSH_HOSTNAMES"

// stubSSH puts a fake ssh first on PATH. Its `ssh -G HOST` reports hostnames[HOST], or HOST itself
// when unmapped, like ssh without a matching config. The fake is the running test binary, so
// TestMain must call runFakeSSHIfRequested.
func stubSSH(t *testing.T, hostnames map[string]string) {
	t.Helper()
	encoded, err := json.Marshal(hostnames)
	require.NoError(t, err, "encoding fake ssh hostnames")
	dir := t.TempDir()
	name := "ssh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	copyTestBinary(t, filepath.Join(dir, name))
	t.Setenv(fakeSSHHostnamesEnv, string(encoded))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runFakeSSHIfRequested acts as `ssh -G` and exits when this test binary was started as the fake
// ssh installed by stubSSH. Otherwise it returns.
func runFakeSSHIfRequested() {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") != "ssh" {
		return
	}
	encoded, ok := os.LookupEnv(fakeSSHHostnamesEnv)
	if !ok {
		return
	}
	var hostnames map[string]string
	if err := json.Unmarshal([]byte(encoded), &hostnames); err != nil {
		fmt.Fprintf(os.Stderr, "fake ssh: decoding hostnames: %v\n", err)
		os.Exit(1)
	}
	args := os.Args[1:]
	if len(args) != 2 || args[0] != "-G" {
		fmt.Fprintf(os.Stderr, "fake ssh: unexpected arguments %q\n", args)
		os.Exit(1)
	}
	host := args[1]
	hostname, mapped := hostnames[host]
	if !mapped {
		hostname = host
	}
	fmt.Printf("hostname %s\n", hostname)
	os.Exit(0)
}

func copyTestBinary(t *testing.T, dst string) {
	t.Helper()
	src, err := os.Executable()
	require.NoError(t, err, "locating test binary")
	in, err := os.Open(src)
	require.NoError(t, err, "opening test binary")
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o755)
	require.NoError(t, err, "creating fake ssh")
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	require.NoError(t, err, "copying test binary")
	require.NoError(t, closeErr, "closing fake ssh")
}
