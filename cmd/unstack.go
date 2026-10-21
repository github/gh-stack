package cmd

import (
	"errors"
	"strconv"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/github"
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

With no argument, the active stack (the one containing the currently checked out
branch) is unstacked on GitHub and removed from local tracking.

Provide a stack number (the identifier shown in the GitHub stack UI) to
unstack a specific stack on GitHub. This works from anywhere in the repository,
whether or not the stack is checked out locally — the number is unstacked
directly through the GitHub API. If the stack is also available locally, its
local tracking is removed as well.

Use --local to only remove local tracking without touching remote (GitHub).

GitHub decides which pull requests can be unstacked. Merged or queued PRs remain
stacked on GitHub. After a successful unstack, local tracking is removed even
when some PRs remain on GitHub.`,
		Example: `  # Unstack the current stack locally and on GitHub
  $ gh stack unstack

  # Unstack a specific stack by its number
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
	release, err := beginOptionalStackMutation(cfg, "unstack")
	if err != nil {
		return err
	}
	defer release()
	// A stack number targets a specific stack. It is unstacked directly on
	// GitHub by number (remote-first), so this works from anywhere in the
	// repository whether or not the stack is tracked locally.
	if opts.stackNumber > 0 {
		if cfg.StackMutation == nil {
			if !opts.local {
				return runRemoteUnstack(cfg, opts.stackNumber)
			}
			cfg.Errorf("stack #%d is not tracked locally", opts.stackNumber)
			return ErrNotInStack
		}
		// --local must never contact GitHub, so it uses a strictly local lookup
		result, ok, err := lookupStackByNumber(cfg, opts.stackNumber, !opts.local)
		if err != nil {
			return stackLookupError(err)
		}
		if !ok {
			// The stack number isn't tracked locally.
			if opts.local {
				// --local never contacts GitHub, and there is nothing to remove
				// locally, so there is nothing to do.
				cfg.Errorf("stack #%d is not tracked locally", opts.stackNumber)
				cfg.Printf("Omit %s to unstack it on GitHub", cfg.ColorCyan("--local"))
				return ErrNotInStack
			}
			return runRemoteUnstack(cfg, opts.stackNumber)
		}
		return unstackTrackedStack(cfg, opts, result)
	}

	// No argument: operate on the active stack for the current branch.
	result, err := loadStack(cfg, "")
	if err != nil {
		return stackLookupError(err)
	}
	return unstackTrackedStack(cfg, opts, result)
}

// unstackTrackedStack unstacks eligible PRs on GitHub (unless --local) and then
// removes the stack from local tracking.
func unstackTrackedStack(cfg *config.Config, opts *unstackOptions, result *loadStackResult) error {
	gitDir := result.GitDir

	sf := result.StackFile
	s := result.Stack

	// Unstack on GitHub first (unless --local). The server decides which PRs
	// can be removed; successful partial unstacking still removes local tracking.
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
			} else if err := unstackNumberOnGitHub(cfg, client, number, true); err != nil {
				return err
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
		return stackSaveError(cfg, err)
	}
	cfg.Successf("Stack removed from local tracking")

	return nil
}

// runRemoteUnstack unstacks a stack on GitHub purely by its number, without any
// local tracking. This is the remote-first path that lets `gh stack unstack
// <number>` run from anywhere in the repository (like `gh stack link`), whether
// or not the stack is checked out locally.
func runRemoteUnstack(cfg *config.Config, number int) error {
	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}
	return unstackNumberOnGitHub(cfg, client, number, false)
}

// unstackNumberOnGitHub calls the Unstack API for the given stack number and
// reports the outcome. With local tracking, a 404 is an idempotent success so
// the caller can finish local cleanup; otherwise it is a not-found error.
func unstackNumberOnGitHub(cfg *config.Config, client github.ClientOps, number int, hasLocalTracking bool) error {
	_, dissolved, err := client.Unstack(number)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				if hasLocalTracking {
					// Stack already gone on GitHub — treat as success and let
					// the caller finish removing local tracking.
					cfg.Warningf("Stack not found on GitHub — continuing with local unstack")
					return nil
				}
				// Remote-first: the targeted stack does not exist on GitHub.
				cfg.Errorf("stack #%d not found on GitHub", number)
				return ErrNotInStack
			case 422:
				cfg.Errorf("Unstacking not allowed: %s", httpErr.Message)
				return ErrInvalidArgs
			default:
				cfg.Errorf("Failed to unstack on GitHub (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to unstack on GitHub: %v", err)
		return ErrAPIFailure
	}

	if dissolved {
		cfg.Successf("Stack removed on GitHub%s", stackLabel(number))
	} else {
		cfg.Successf("Pull requests unstacked on GitHub%s", stackLabel(number))
		cfg.Infof("Some pull requests remain stacked on GitHub")
	}
	return nil
}
