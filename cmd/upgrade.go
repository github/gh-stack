package cmd

import (
	"fmt"
	"os/exec"
	"runtime"

	gh "github.com/cli/go-gh/v2"
	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

var (
	upgradeGOOS   = runtime.GOOS
	upgradeGHPath = gh.Path
	upgradeRun    = runUpgradeSubprocess
)

func UpgradeCmd(cfg *config.Config) *cobra.Command {
	var dryRun bool
	var force bool

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade gh stack to the latest version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUpgrade(cfg, dryRun, force)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Only display available upgrades")
	cmd.Flags().BoolVar(&force, "force", false, "Upgrade even if the extension is pinned")

	return cmd
}

func runUpgrade(cfg *config.Config, dryRun, force bool) error {
	args := []string{"extension", "upgrade", "stack"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if force {
		args = append(args, "--force")
	}

	if upgradeGOOS == "windows" && !dryRun {
		cfg.Errorf("automatic upgrade is not supported on Windows")
		cfg.Printf("Run `gh extension upgrade stack` directly.")
		return ErrSilent
	}

	path, err := upgradeGHPath()
	if err != nil {
		return fmt.Errorf("locating gh executable: %w", err)
	}
	if dryRun {
		return upgradeRun(cfg, path, args)
	}
	return replaceWithUpgrade(path, args)
}

func runUpgradeSubprocess(cfg *config.Config, path string, args []string) error {
	cmd := exec.Command(path, args...)
	cmd.Stdin = cfg.In
	cmd.Stdout = cfg.Out
	cmd.Stderr = cfg.Err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running gh extension upgrade stack: %w", err)
	}
	return nil
}
