package cmd

import (
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type unstackOptions struct {
	local bool
}

func UnstackCmd(cfg *config.Config) *cobra.Command {
	opts := &unstackOptions{}

	cmd := &cobra.Command{
		Use:     "unstack",
		Aliases: []string{"delete"},
		Short:   "Delete a stack locally and on GitHub",
		Long:    "Remove the current active stack from local tracking and delete it on GitHub. Use --local to only remove local tracking.",
		Example: `  # Delete the stack locally and on GitHub
  $ gh stack unstack

  # Only remove local tracking (keep the stack on GitHub)
  $ gh stack unstack --local`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnstack(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.local, "local", false, "Only delete the stack locally")

	return cmd
}

func runUnstack(cfg *config.Config, opts *unstackOptions) error {
	result, err := loadStack(cfg, "")
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

			blocked, err := shouldBlockUnstackDelete(cfg, client, s)
			if err != nil {
				cfg.Errorf("failed to check pull request states before unstack: %s", err)
				return ErrAPIFailure
			}
			if blocked {
				cfg.Errorf("Unstacking not allowed. Pull requests that are queued for merge, are merging, or are already merged will remain in the stack.")
				return ErrInvalidArgs
			}

			if err := client.DeleteStack(s.ID); err != nil {
				var httpErr *api.HTTPError
				if errors.As(err, &httpErr) {
					switch httpErr.StatusCode {
					case 404:
						// Stack already deleted on GitHub — treat as success.
						cfg.Warningf("Stack not found on GitHub — continuing with local unstack")
					case 422:
						cfg.Errorf("Cannot delete stack on GitHub: %s", httpErr.Message)
						return ErrAPIFailure
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

func shouldBlockUnstackDelete(cfg *config.Config, client github.ClientOps, s *stack.Stack) (bool, error) {
	if s == nil || len(s.Branches) == 0 {
		return false, nil
	}

	eligible := 0
	ineligible := 0
	for _, b := range s.Branches {
		// Respect stored merged status when available in local stack metadata.
		if b.PullRequest != nil && b.PullRequest.Merged {
			ineligible++
			continue
		}

		var (
			pr  *github.PullRequest
			err error
		)

		if b.PullRequest != nil && b.PullRequest.Number > 0 {
			pr, err = client.FindPRByNumber(b.PullRequest.Number)
			if err != nil {
				return false, fmt.Errorf("checking PR #%d for branch %s: %w", b.PullRequest.Number, b.Branch, err)
			}
		} else {
			pr, err = client.FindPRForBranch(b.Branch)
			if err != nil {
				return false, fmt.Errorf("checking PR for branch %s: %w", b.Branch, err)
			}
		}

		// If the PR no longer exists (or branch has no open PR), do not block unstacking.
		if pr == nil {
			eligible++
			continue
		}

		switch {
		case pr.State == "MERGED":
			ineligible++
		case pr.IsQueued():
			ineligible++
		case pr.IsAutoMergeEnabled():
			ineligible++
		default:
			eligible++
		}
	}

	return ineligible > 0 && eligible == 0, nil
}
