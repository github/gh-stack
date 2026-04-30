package cmd

import (
	"errors"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type unstackOptions struct {
	target string
	local  bool
}

func UnstackCmd(cfg *config.Config) *cobra.Command {
	opts := &unstackOptions{}

	cmd := &cobra.Command{
		Use:     "unstack [branch]",
		Aliases: []string{"delete"},
		Short:   "Delete a stack locally and on GitHub",
		Long:    "Remove a stack from local tracking and delete it on GitHub. Use --local to only remove local tracking.",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.target = args[0]
			}
			return runUnstack(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.local, "local", false, "Only delete the stack locally")

	return cmd
}

func runUnstack(cfg *config.Config, opts *unstackOptions) error {
	result, err := loadStack(cfg, opts.target)
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

	// Delete the stack on GitHub first (unless --local).
	// Only proceed with local deletion after the remote operation succeeds.
	if !opts.local {
		if s.ID == "" {
			cfg.Warningf("Stack has no remote ID — skipping server-side deletion")
		} else {
			client, err := cfg.GitHubClient()
			if err != nil {
				cfg.Errorf("failed to create GitHub client: %s", err)
				return ErrAPIFailure
			}
			if err := client.DeleteStack(s.ID); err != nil {
				var httpErr *api.HTTPError
				if errors.As(err, &httpErr) {
					switch httpErr.StatusCode {
					case 404:
						// Stack already deleted on GitHub — treat as success.
						cfg.Warningf("Stack not found on GitHub — continuing with local unstack")
					default:
						cfg.Errorf("Failed to delete stack on GitHub (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
						return ErrAPIFailure
					}
				} else {
					cfg.Errorf("Failed to delete stack on GitHub: %v", err)
					return ErrAPIFailure
				}
			} else {
				cfg.Successf("Stack deleted on GitHub")
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
