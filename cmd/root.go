package cmd

import (
	"errors"
	"fmt"
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
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetVersionTemplate("gh stack version {{.Version}}\n")

	root.SetOut(cfg.Out)
	root.SetErr(cfg.Err)

	// Local operations
	root.AddCommand(InitCmd(cfg))
	root.AddCommand(AddCmd(cfg))

	// Remote operations
	root.AddCommand(CheckoutCmd(cfg))
	root.AddCommand(PushCmd(cfg))
	root.AddCommand(SubmitCmd(cfg))
	root.AddCommand(SyncCmd(cfg))
	root.AddCommand(UnstackCmd(cfg))
	root.AddCommand(MergeCmd(cfg))
	root.AddCommand(LinkCmd(cfg))

	// Helper commands
	root.AddCommand(ViewCmd(cfg))
	root.AddCommand(RebaseCmd(cfg))

	// Navigation commands
	root.AddCommand(UpCmd(cfg))
	root.AddCommand(DownCmd(cfg))
	root.AddCommand(TopCmd(cfg))
	root.AddCommand(BottomCmd(cfg))
	root.AddCommand(SwitchCmd(cfg))

	// Alias
	root.AddCommand(AliasCmd(cfg))

	// Feedback
	root.AddCommand(FeedbackCmd(cfg))

	return root
}

func Execute() {
	cmd := RootCmd()

	// Wrap in a "gh" parent so help output shows "gh stack" instead of just "stack".
	wrapCmd := &cobra.Command{Use: "gh", SilenceUsage: true, SilenceErrors: true}
	wrapCmd.AddCommand(cmd)
	wrapCmd.SetArgs(append([]string{"stack"}, os.Args[1:]...))

	if err := wrapCmd.Execute(); err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(cmd.ErrOrStderr(), err)
		os.Exit(1)
	}
}
