package cmd

import (
	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

type checkoutOptions struct {
	target   string
	noSwitch bool
}

func CheckoutCmd(cfg *config.Config) *cobra.Command {
	opts := &checkoutOptions{}

	cmd := &cobra.Command{
		Use:   "checkout <pr-or-branch>",
		Short: "Checkout a stack from a PR number or branch name",
		Long:  "Discover and check out an entire stack from a pull request number, URL, or branch name.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.target = args[0]
			return runCheckout(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.noSwitch, "no-switch", false, "Fetch and track the stack without switching branches")

	return cmd
}

// runCheckout is a placeholder for the stack checkout workflow.
//
// The intended behavior is:
//  1. Resolve the target (PR number, URL, or branch name) to a PR
//  2. If the PR is part of a stack, discover the full set of PRs in the stack
//  3. Fetch and create local tracking branches for every branch in the stack
//  4. Save the stack to local tracking (.git/gh-stack, similar to gh stack init --adopt)
//  5. Switch to the target branch (unless --no-switch is set)
func runCheckout(cfg *config.Config, opts *checkoutOptions) error {
	cfg.Warningf("gh stack checkout is not yet implemented\n")
	return nil
}
