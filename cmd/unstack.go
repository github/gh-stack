package cmd

import (
	"errors"
	"strconv"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type unstackOptions struct {
	local       bool
	stackNumber int
}

func UnstackCmd(cfg *config.Config) *cobra.Command {
	opts := &unstackOptions{}

	cmd := &cobra.Command{
		Use:     "unstack [<stack-number>]",
		Aliases: []string{"delete"},
		Short:   "Remove a stack locally and on GitHub",
		Long: `Remove a stack from local tracking and unstack it on GitHub.

With no argument, the current active stack is used. Provide a stack number (the
identifier shown in the github.com stack UI) to unstack a specific locally
tracked stack instead. Use --local to only remove local tracking.

GitHub decides which pull requests can be unstacked: PRs that are queued for
merge or have auto-merge enabled are left stacked. When some pull requests
remain stacked, local tracking is kept.`,
		Example: `  # Unstack the current stack locally and on GitHub
  $ gh stack unstack

  # Unstack a specific stack by its stack number
  $ gh stack unstack 7

  # Only remove local tracking (keep the stack on GitHub)
  $ gh stack unstack --local`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				n, err := strconv.Atoi(args[0])
				if err != nil || n <= 0 {
					cfg.Errorf("invalid stack number %q", args[0])
					return ErrInvalidArgs
				}
				opts.stackNumber = n
			}
			return runUnstack(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.local, "local", false, "Only delete the stack locally")

	return cmd
}

func runUnstack(cfg *config.Config, opts *unstackOptions) error {
	var result *loadStackResult
	var err error
	if opts.stackNumber > 0 {
		result, err = loadStackByNumber(cfg, opts.stackNumber)
	} else {
		result, err = loadStack(cfg, "")
	}
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir

	if err := modify.CheckStateGuard(gitDir); err != nil {
		cfg.Errorf("%s", err)
		return ErrModifyRecovery
	}

	sf := result.StackFile
	s := result.Stack

	// Unstack on GitHub first (unless --local). The server decides which PRs
	// can be unstacked; PRs that are queued for merge or have auto-merge enabled
	// are left in place and the stack is kept. Local tracking is only removed
	// when the remote stack is fully dissolved.
	if !opts.local {
		if s.ID == "" && s.Number == 0 {
			cfg.Warningf("Stack has no remote ID — skipping server-side unstack")
		} else {
			client, err := cfg.GitHubClient()
			if err != nil {
				cfg.Errorf("failed to create GitHub client: %s", err)
				return ErrAPIFailure
			}

			number, err := ensureStackNumber(client, s)
			if err != nil {
				cfg.Errorf("failed to look up stack on GitHub: %s", err)
				return ErrAPIFailure
			}

			if number == 0 {
				cfg.Warningf("Stack not found on GitHub — continuing with local unstack")
			} else if _, dissolved, err := client.Unstack(number); err != nil {
				var httpErr *api.HTTPError
				if errors.As(err, &httpErr) {
					switch httpErr.StatusCode {
					case 404:
						// Stack already gone on GitHub — treat as success.
						cfg.Warningf("Stack not found on GitHub — continuing with local unstack")
					case 422:
						// The server refused: every PR is queued for merge or has
						// auto-merge enabled, so nothing can be unstacked.
						cfg.Errorf("Unstacking not allowed: %s", httpErr.Message)
						return ErrInvalidArgs
					default:
						cfg.Errorf("Failed to unstack on GitHub (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
						return ErrAPIFailure
					}
				} else {
					cfg.Errorf("Failed to unstack on GitHub: %v", err)
					return ErrAPIFailure
				}
			} else if !dissolved {
				// Some PRs (queued for merge or with auto-merge enabled) remain
				// stacked on GitHub, so the stack still exists. Keep local
				// tracking so it continues to reflect the remote stack.
				cfg.Warningf("Some pull requests are queued for merge or have auto-merge enabled and remain stacked on GitHub")
				cfg.Printf("The stack was left in place — local tracking is unchanged")
				return nil
			} else {
				cfg.Successf("Stack removed on GitHub%s", stackLabel(number))
			}
		}
	}

	// Remove the exact resolved stack from local tracking by pointer identity,
	// not by branch name — avoids removing the wrong stack when a trunk
	// branch is shared across multiple stacks.
	for i := range sf.Stacks {
		if &sf.Stacks[i] == s {
			sf.RemoveStack(i)
			break
		}
	}
	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}
	cfg.Successf("Stack removed from local tracking")

	return nil
}
