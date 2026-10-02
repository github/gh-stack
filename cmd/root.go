package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/theme"
	"github.com/github/gh-stack/internal/update"
	"github.com/spf13/cobra"
)

type updateResult struct {
	notify update.Notification
	err    error
}

type rootCommand struct {
	*cobra.Command
	finish func(error)
}

func RootCmd() *cobra.Command {
	return newRootCmd(config.New(), startUpdateCheck).Command
}

func newRootCmd(cfg *config.Config, startCheck func(context.Context, string) <-chan updateResult) *rootCommand {
	root := &cobra.Command{
		Use:   "stack <command>",
		Short: "Manage stacked branches and pull requests",
		Long: `Stacked PRs let you break a large change into a chain of pull requests
that build on each other. Use ` + "`gh stack`" + ` to create and manage your stack
locally, then push to GitHub to create your stack of PRs.`,
		Example: `  # Start a new stack targeting your default branch
  $ gh stack init

  # Or turn an existing set of branches into a stack
  $ gh stack init branch1 branch2 branch3

  # Make changes and commit, then add a branch to the stack
  $ gh stack add branch4

  # Push all branches and create/update PRs on GitHub
  $ gh stack submit

  # Keep your local in sync with remote
  $ gh stack sync`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	var updates <-chan updateResult
	root.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		theme.ApplyOverride()
		cfg.Canceled = false
		updates = nil
		if update.Enabled(root.Version) && !isHelpOrCompletionCommand(cmd) {
			updates = startCheck(cmd.Context(), root.Version)
		}
	}
	finish := func(err error) {
		results := updates
		updates = nil
		if cfg.Canceled || errors.Is(err, ErrInvalidArgs) ||
			errors.Is(err, context.Canceled) || errors.Is(err, errInterrupt) || isInterruptError(err) {
			return
		}
		select {
		case result := <-results:
			if result.notify != nil {
				result.err = errors.Join(result.err, result.notify(root.ErrOrStderr()))
			}
			if result.err != nil && os.Getenv("GH_DEBUG") != "" {
				fmt.Fprintf(root.ErrOrStderr(), "debug: gh-stack update notification: %v\n", result.err)
			}
		default:
			// Never wait for a release check if the command finishes quickly
		}
	}

	root.SetVersionTemplate("gh stack version {{.Version}}\n")

	root.SetOut(cfg.Out)
	root.SetErr(cfg.Err)

	root.AddGroup(
		&cobra.Group{ID: "stack", Title: "Stack management:"},
		&cobra.Group{ID: "remote", Title: "Remote operations:"},
		&cobra.Group{ID: "nav", Title: "Navigation:"},
		&cobra.Group{ID: "utils", Title: "Utilities:"},
	)

	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		if cmd.Name() == "stack" {
			out := cmd.OutOrStderr()
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Learn more:")
			fmt.Fprintln(out, "  Documentation: https://gh.io/stacks")
			fmt.Fprintln(out, "  Feedback: https://gh.io/stacks-feedback")
		}
	})

	// Stack management commands
	initCmd := InitCmd(cfg)
	initCmd.GroupID = "stack"
	root.AddCommand(initCmd)

	addCmd := AddCmd(cfg)
	addCmd.GroupID = "stack"
	root.AddCommand(addCmd)

	viewCmd := ViewCmd(cfg)
	viewCmd.GroupID = "stack"
	root.AddCommand(viewCmd)

	checkoutCmd := CheckoutCmd(cfg)
	checkoutCmd.GroupID = "stack"
	root.AddCommand(checkoutCmd)

	modifyCmd := ModifyCmd(cfg)
	modifyCmd.GroupID = "stack"
	root.AddCommand(modifyCmd)

	unstackCmd := UnstackCmd(cfg)
	unstackCmd.GroupID = "stack"
	root.AddCommand(unstackCmd)

	// Remote operations commands
	submitCmd := SubmitCmd(cfg)
	submitCmd.GroupID = "remote"
	root.AddCommand(submitCmd)

	syncCmd := SyncCmd(cfg)
	syncCmd.GroupID = "remote"
	root.AddCommand(syncCmd)

	rebaseCmd := RebaseCmd(cfg)
	rebaseCmd.GroupID = "remote"
	root.AddCommand(rebaseCmd)

	pushCmd := PushCmd(cfg)
	pushCmd.GroupID = "remote"
	root.AddCommand(pushCmd)

	linkCmd := LinkCmd(cfg)
	linkCmd.GroupID = "remote"
	root.AddCommand(linkCmd)

	mergeCmd := MergeCmd(cfg)
	mergeCmd.GroupID = "remote"
	root.AddCommand(mergeCmd)

	// Navigation commands
	switchCmd := SwitchCmd(cfg)
	switchCmd.GroupID = "nav"
	root.AddCommand(switchCmd)

	upCmd := UpCmd(cfg)
	upCmd.GroupID = "nav"
	root.AddCommand(upCmd)

	downCmd := DownCmd(cfg)
	downCmd.GroupID = "nav"
	root.AddCommand(downCmd)

	topCmd := TopCmd(cfg)
	topCmd.GroupID = "nav"
	root.AddCommand(topCmd)

	bottomCmd := BottomCmd(cfg)
	bottomCmd.GroupID = "nav"
	root.AddCommand(bottomCmd)

	trunkCmd := TrunkCmd(cfg)
	trunkCmd.GroupID = "nav"
	root.AddCommand(trunkCmd)

	// Utility commands
	aliasCmd := AliasCmd(cfg)
	aliasCmd.GroupID = "utils"
	root.AddCommand(aliasCmd)

	feedbackCmd := FeedbackCmd(cfg)
	feedbackCmd.GroupID = "utils"
	root.AddCommand(feedbackCmd)

	return &rootCommand{Command: root, finish: finish}
}

func startUpdateCheck(ctx context.Context, version string) <-chan updateResult {
	results := make(chan updateResult, 1)
	go func() {
		notify, err := update.Check(ctx, version)
		results <- updateResult{notify: notify, err: err}
	}()
	return results
}

func isHelpOrCompletionCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return true
		}
	}
	return false
}

func execute(cmd *rootCommand, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wrap in a "gh" parent so help output shows "gh stack" instead of just "stack".
	wrapCmd := &cobra.Command{Use: "gh", SilenceUsage: true, SilenceErrors: true}
	wrapCmd.AddCommand(cmd.Command)
	wrapCmd.SetArgs(append([]string{"stack"}, args...))
	err := wrapCmd.ExecuteContext(ctx)
	var exitErr *ExitError
	if err != nil && !errors.As(err, &exitErr) {
		fmt.Fprintln(cmd.ErrOrStderr(), err)
	}
	cmd.finish(err)
	return err
}

func Execute() {
	cmd := newRootCmd(config.New(), startUpdateCheck)
	if err := execute(cmd, os.Args[1:]); err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(1)
	}
}
