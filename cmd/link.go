package cmd

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/github"
	"github.com/spf13/cobra"
)

func LinkCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link <pr-number> <pr-number> [<pr-number>...]",
		Short: "Link PRs into a stack on GitHub without local tracking",
		Long: `Create or update a stack on GitHub from a list of PR numbers.

This command works entirely via the GitHub API and does not modify
any local state. It is designed for users who manage branches with
external tools (e.g. jj) and want to use GitHub stacked PRs without
adopting local stack tracking.

PR numbers must be provided in stack order (bottom to top). The first
PR's base branch is the trunk of the stack, and each subsequent PR
should target the previous PR's head branch.

If the PRs are not yet in a stack, a new stack is created. If some of
the PRs are already in a stack, the existing stack is updated to include
the new PRs (existing PRs are never removed).`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLink(cfg, args)
		},
	}

	return cmd
}

func runLink(cfg *config.Config, args []string) error {
	prNumbers, err := parsePRNumbers(args)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrInvalidArgs
	}

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}

	stacks, err := client.ListStacks()
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			cfg.Warningf("Stacked PRs are not enabled for this repository")
			return ErrStacksUnavailable
		}
		cfg.Errorf("failed to list stacks: %v", err)
		return ErrAPIFailure
	}

	matchedStack, err := findMatchingStack(stacks, prNumbers)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrDisambiguate
	}

	if matchedStack == nil {
		return createLink(cfg, client, prNumbers)
	}

	return updateLink(cfg, client, matchedStack, prNumbers)
}

// parsePRNumbers converts string args to a validated list of PR numbers.
// Returns an error if any arg is not a positive integer or if there are duplicates.
func parsePRNumbers(args []string) ([]int, error) {
	prNumbers := make([]int, 0, len(args))
	seen := make(map[int]bool, len(args))

	for _, arg := range args {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid PR number: %q", arg)
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate PR number: %d", n)
		}
		seen[n] = true
		prNumbers = append(prNumbers, n)
	}

	return prNumbers, nil
}

// findMatchingStack finds a single stack that contains any of the given PR numbers.
// Returns nil if no stack matches. Returns an error if PRs span multiple stacks.
func findMatchingStack(stacks []github.RemoteStack, prNumbers []int) (*github.RemoteStack, error) {
	prSet := make(map[int]bool, len(prNumbers))
	for _, n := range prNumbers {
		prSet[n] = true
	}

	var matched *github.RemoteStack
	for i := range stacks {
		for _, n := range stacks[i].PullRequests {
			if prSet[n] {
				if matched != nil && matched.ID != stacks[i].ID {
					return nil, fmt.Errorf("PRs belong to multiple stacks — unstack them first, then re-link")
				}
				matched = &stacks[i]
				break
			}
		}
	}

	return matched, nil
}

// createLink creates a new stack with the given PR numbers.
func createLink(cfg *config.Config, client github.ClientOps, prNumbers []int) error {
	_, err := client.CreateStack(prNumbers)
	if err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 422:
				cfg.Errorf("Cannot create stack: %s", httpErr.Message)
				return ErrAPIFailure
			case 404:
				cfg.Warningf("Stacked PRs are not enabled for this repository")
				return ErrStacksUnavailable
			default:
				cfg.Errorf("Failed to create stack (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to create stack: %v", err)
		return ErrAPIFailure
	}

	cfg.Successf("Created stack with %d PRs", len(prNumbers))
	return nil
}

// updateLink updates an existing stack with the given PR numbers.
// The update is additive-only: it errors if any existing PRs would be removed.
func updateLink(cfg *config.Config, client github.ClientOps, existing *github.RemoteStack, prNumbers []int) error {
	// Check if the input exactly matches the existing stack.
	if slicesEqual(existing.PullRequests, prNumbers) {
		cfg.Successf("Stack with %d PRs is already up to date", len(prNumbers))
		return nil
	}

	// Check that no existing PRs would be removed (additive-only).
	newSet := make(map[int]bool, len(prNumbers))
	for _, n := range prNumbers {
		newSet[n] = true
	}

	var dropped []int
	for _, n := range existing.PullRequests {
		if !newSet[n] {
			dropped = append(dropped, n)
		}
	}

	if len(dropped) > 0 {
		cfg.Errorf("Cannot update stack: this would remove %s from the stack",
			formatPRList(dropped))
		cfg.Printf("Current stack: %s", formatPRList(existing.PullRequests))
		cfg.Printf("Include all existing PRs in the command to update the stack")
		return ErrInvalidArgs
	}

	stackID := strconv.Itoa(existing.ID)
	if err := client.UpdateStack(stackID, prNumbers); err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				// Stack was deleted between list and update — try creating instead.
				cfg.Warningf("Stack was deleted — creating a new one")
				return createLink(cfg, client, prNumbers)
			case 422:
				cfg.Errorf("Cannot update stack: %s", httpErr.Message)
				return ErrAPIFailure
			default:
				cfg.Errorf("Failed to update stack (HTTP %d): %s", httpErr.StatusCode, httpErr.Message)
				return ErrAPIFailure
			}
		}
		cfg.Errorf("Failed to update stack: %v", err)
		return ErrAPIFailure
	}

	cfg.Successf("Updated stack to %d PRs", len(prNumbers))
	return nil
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatPRList(numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	s := fmt.Sprintf("#%d", numbers[0])
	for _, n := range numbers[1:] {
		s += fmt.Sprintf(", #%d", n)
	}
	return s
}
