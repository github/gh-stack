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

type linkOptions struct {
	base  string
	draft bool
}

func LinkCmd(cfg *config.Config) *cobra.Command {
	opts := &linkOptions{}

	cmd := &cobra.Command{
		Use:   "link <branch-or-pr> <branch-or-pr> [<branch-or-pr>...]",
		Short: "Link PRs into a stack on GitHub without local tracking",
		Long: `Create or update a stack on GitHub from branch names or PR numbers.

This command works entirely via the GitHub API and does not modify
any local state. It is designed for users who manage branches with
external tools (e.g. jj) and want to use GitHub stacked PRs without
adopting local stack tracking.

Arguments are provided in stack order (bottom to top). Each argument
can be a branch name or a PR number. For numeric arguments, the
command first checks if a PR with that number exists; if not, it
treats the argument as a branch name.

For branches that already have open PRs, those PRs are used. For
branches without PRs, new PRs are created automatically with the
correct base branch chaining.

If the PRs are not yet in a stack, a new stack is created. If some of
the PRs are already in a stack, the existing stack is updated to include
the new PRs (existing PRs are never removed).`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLink(cfg, opts, args)
		},
	}

	cmd.Flags().StringVar(&opts.base, "base", "main", "Base branch for the bottom of the stack")
	cmd.Flags().BoolVar(&opts.draft, "draft", false, "Create new PRs as drafts")

	return cmd
}

// resolvedArg holds the result of resolving a single CLI argument to a PR.
type resolvedArg struct {
	branch   string // head branch name
	prNumber int    // PR number
	prURL    string // PR URL (for display)
}

func runLink(cfg *config.Config, opts *linkOptions, args []string) error {
	if err := validateArgs(args); err != nil {
		cfg.Errorf("%s", err)
		return ErrInvalidArgs
	}

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}

	// Phase 1: Resolve each arg to a PR
	resolved, err := resolveAllArgs(cfg, client, opts, args)
	if err != nil {
		return err
	}

	// Phase 2: Fix base branches for existing PRs with wrong bases
	if err := fixBaseBranches(cfg, client, opts, resolved); err != nil {
		return err
	}

	// Phase 3: Upsert the stack
	prNumbers := make([]int, len(resolved))
	for i, r := range resolved {
		prNumbers[i] = r.prNumber
	}

	return upsertStack(cfg, client, prNumbers)
}

// validateArgs checks for duplicates in the arg list.
func validateArgs(args []string) error {
	seen := make(map[string]bool, len(args))
	for _, arg := range args {
		if seen[arg] {
			return fmt.Errorf("duplicate argument: %q", arg)
		}
		seen[arg] = true
	}
	return nil
}

// resolveAllArgs resolves each CLI argument to a PR.
// Numeric args are tried as PR numbers first, then as branch names.
// Non-numeric args are treated as branch names. If no open PR exists
// for a branch, a new PR is created.
func resolveAllArgs(cfg *config.Config, client github.ClientOps, opts *linkOptions, args []string) ([]resolvedArg, error) {
	resolved := make([]resolvedArg, 0, len(args))

	for i, arg := range args {
		r, err := resolveArg(cfg, client, opts, arg, i, resolved)
		if err != nil {
			return nil, err
		}

		// Check for duplicate PR numbers (different args resolving to same PR)
		for _, prev := range resolved {
			if prev.prNumber == r.prNumber {
				cfg.Errorf("arguments %q and %q resolve to the same PR #%d", prev.branch, r.branch, r.prNumber)
				return nil, ErrInvalidArgs
			}
		}

		resolved = append(resolved, *r)
	}

	return resolved, nil
}

// resolveArg resolves a single argument to a PR.
func resolveArg(cfg *config.Config, client github.ClientOps, opts *linkOptions, arg string, index int, previous []resolvedArg) (*resolvedArg, error) {
	// If numeric, try as PR number first
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		pr, err := client.FindPRByNumber(n)
		if err != nil {
			cfg.Warningf("failed to look up PR #%d: %v", n, err)
			// Fall through to branch lookup
		} else if pr != nil {
			return &resolvedArg{
				branch:   pr.HeadRefName,
				prNumber: pr.Number,
				prURL:    pr.URL,
			}, nil
		}
		// PR not found — fall through to treat as branch name
	}

	// Treat as branch name: look for an open PR
	return resolveAsBranch(cfg, client, opts, arg, index, previous)
}

// resolveAsBranch looks up an open PR for a branch name. If none exists,
// creates a new PR with the correct base branch.
func resolveAsBranch(cfg *config.Config, client github.ClientOps, opts *linkOptions, branch string, index int, previous []resolvedArg) (*resolvedArg, error) {
	pr, err := client.FindPRForBranch(branch)
	if err != nil {
		cfg.Errorf("failed to look up PR for branch %s: %v", branch, err)
		return nil, ErrAPIFailure
	}

	if pr != nil {
		cfg.Printf("Found PR %s for branch %s", cfg.PRLink(pr.Number, pr.URL), branch)
		return &resolvedArg{
			branch:   branch,
			prNumber: pr.Number,
			prURL:    pr.URL,
		}, nil
	}

	// No PR exists — create one
	baseBranch := opts.base
	if index > 0 {
		baseBranch = previous[index-1].branch
	}

	title := humanize(branch)
	body := generatePRBody("")

	newPR, err := client.CreatePR(baseBranch, branch, title, body, opts.draft)
	if err != nil {
		cfg.Errorf("failed to create PR for branch %s: %v", branch, err)
		return nil, ErrAPIFailure
	}

	cfg.Successf("Created PR %s for %s (base: %s)", cfg.PRLink(newPR.Number, newPR.URL), branch, baseBranch)
	return &resolvedArg{
		branch:   branch,
		prNumber: newPR.Number,
		prURL:    newPR.URL,
	}, nil
}

// fixBaseBranches updates the base branch of existing PRs to match the
// expected stack chain. The first PR should have base = opts.base,
// each subsequent PR should have base = previous PR's head branch.
func fixBaseBranches(cfg *config.Config, client github.ClientOps, opts *linkOptions, resolved []resolvedArg) error {
	for i, r := range resolved {
		expectedBase := opts.base
		if i > 0 {
			expectedBase = resolved[i-1].branch
		}

		// Look up the PR to check its current base
		pr, err := client.FindPRByNumber(r.prNumber)
		if err != nil {
			cfg.Warningf("could not verify base branch for PR %s: %v",
				cfg.PRLink(r.prNumber, r.prURL), err)
			continue
		}
		if pr == nil {
			continue
		}

		if pr.BaseRefName != expectedBase {
			if err := client.UpdatePRBase(r.prNumber, expectedBase); err != nil {
				cfg.Warningf("failed to update base branch for PR %s: %v",
					cfg.PRLink(r.prNumber, r.prURL), err)
			} else {
				cfg.Successf("Updated base branch for PR %s to %s",
					cfg.PRLink(r.prNumber, r.prURL), expectedBase)
			}
		}
	}
	return nil
}

// upsertStack lists existing stacks and creates or updates as needed.
func upsertStack(cfg *config.Config, client github.ClientOps, prNumbers []int) error {
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
