package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

const (
	defaultAliasName     = "gs"
	wrapperMarkerLine    = "# installed by github/gh-stack" // used to identify our own scripts
	markedWrapperContent = "#!/bin/sh\n# installed by github/gh-stack\nexec gh stack \"$@\"\n"
)

var validAliasName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

func AliasCmd(cfg *config.Config) *cobra.Command {
	var remove bool

	cmd := &cobra.Command{
		Use:   "alias [name]",
		Short: "Create a shell alias for gh stack",
		Long: `Create a short command alias so you can run "gs [command]" instead of "gh stack [command]".

This installs a small wrapper script into ~/.local/bin/ that forwards all
arguments to "gh stack". The default alias name is "gs", but you can choose
any name by passing it as an argument.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := defaultAliasName
			if len(args) > 0 {
				name = args[0]
			}
			if err := validateAliasName(cfg, name); err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				return handleWindowsAlias(cfg, name, remove)
			}
			binDir, err := localBinDirFunc()
			if err != nil {
				cfg.Errorf("%s", err)
				return ErrSilent
			}
			if remove {
				return runAliasRemove(cfg, name, binDir)
			}
			return runAlias(cfg, name, binDir)
		},
	}

	cmd.Flags().BoolVar(&remove, "remove", false, "Remove a previously created alias")

	return cmd
}

// validateAliasName checks that name is a valid alias identifier.
func validateAliasName(cfg *config.Config, name string) error {
	if !validAliasName.MatchString(name) {
		cfg.Errorf("invalid alias name %q: must start with a letter and contain only letters, digits, hyphens, or underscores", name)
		return ErrInvalidArgs
	}
	return nil
}

// handleWindowsAlias prints manual instructions since automatic alias
// management is not supported on Windows.
func handleWindowsAlias(cfg *config.Config, name string, remove bool) error {
	if remove {
		cfg.Infof("Automatic alias removal is not supported on Windows.")
		cfg.Printf("Remove the %s.cmd file from your PATH manually.", name)
	} else {
		cfg.Infof("Automatic alias creation is not supported on Windows.")
		cfg.Printf("You can create the alias manually by adding a batch file or PowerShell function.")
		cfg.Printf("For example, create a file named %s.cmd on your PATH with:", name)
		cfg.Printf("  @echo off")
		cfg.Printf("  gh stack %%*")
	}
	return ErrSilent
}

func runAlias(cfg *config.Config, name string, binDir string) error {
	scriptPath := filepath.Join(binDir, name)

	// Check if our wrapper already exists at this path.
	if isOurWrapper(scriptPath) {
		cfg.Successf("Alias %q is already installed at %s", name, scriptPath)
		return nil
	}

	// Check for an existing command with this name.
	if existing, err := exec.LookPath(name); err == nil {
		cfg.Errorf("a command named %q already exists at %s", name, existing)
		cfg.Printf("Choose a different alias name, for example: %s", cfg.ColorCyan("gh stack alias gst"))
		return ErrInvalidArgs
	}

	// Guard against overwriting an existing file that isn't on PATH
	if _, err := os.Stat(scriptPath); err == nil {
		cfg.Errorf("a file already exists at %s", scriptPath)
		cfg.Printf("Choose a different alias name, for example: %s", cfg.ColorCyan("gh stack alias gst"))
		return ErrInvalidArgs
	}

	// Ensure the bin directory exists.
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		cfg.Errorf("failed to create directory %s: %s", binDir, err)
		return ErrSilent
	}

	// Write the wrapper script.
	if err := os.WriteFile(scriptPath, []byte(markedWrapperContent), 0o755); err != nil {
		cfg.Errorf("failed to write %s: %s", scriptPath, err)
		return ErrSilent
	}

	cfg.Successf("Created alias %q at %s", name, scriptPath)
	cfg.Printf("You can now use %s instead of %s", cfg.ColorCyan(name+" <command>"), cfg.ColorCyan("gh stack <command>"))

	// Warn if the bin directory is not in PATH.
	if !dirInPath(binDir) {
		cfg.Warningf("%s is not in your PATH", binDir)
		cfg.Printf("Add it by appending this to your shell profile (~/.bashrc, ~/.zshrc, etc.):")
		cfg.Printf("  export PATH=\"%s:$PATH\"", binDir)
	}

	return nil
}

func runAliasRemove(cfg *config.Config, name string, binDir string) error {
	scriptPath := filepath.Join(binDir, name)

	if !isOurWrapper(scriptPath) {
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			cfg.Errorf("no alias %q found at %s", name, scriptPath)
		} else {
			cfg.Errorf("%s exists but was not created by gh-stack; refusing to remove", scriptPath)
		}
		return ErrSilent
	}

	if err := os.Remove(scriptPath); err != nil {
		cfg.Errorf("failed to remove %s: %s", scriptPath, err)
		return ErrSilent
	}

	cfg.Successf("Removed alias %q from %s", name, scriptPath)
	return nil
}

// localBinDirFunc returns the user-local binary directory (~/.local/bin).
// It is a variable so tests can override it.
var localBinDirFunc = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// dirInPath reports whether dir is present in the system PATH.
func dirInPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// isOurWrapper checks if the file at path is a wrapper script that we created.
func isOurWrapper(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), wrapperMarkerLine)
}
