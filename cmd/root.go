package cmd

import (
	"os"

	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

func RootCmd() *cobra.Command {
	cfg := config.New()

	root := &cobra.Command{
		Use:           "stack <command>",
		Short:         "Manage stacked branches and pull requests",
		Long:          "Create, navigate, and manage stacks of branches and pull requests.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetOut(cfg.Out)
	root.SetErr(cfg.Err)

	// Local operations
	root.AddCommand(InitCmd(cfg))
	root.AddCommand(AddCmd(cfg))

	// Helper commands
	root.AddCommand(ViewCmd(cfg))

	// Placeholders for upcoming features
	for _, ph := range placeholderCommands {
		root.AddCommand(PlaceholderCmd(ph, cfg))
	}

	return root
}

func Execute() {
	cmd := RootCmd()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
