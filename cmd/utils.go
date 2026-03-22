package cmd

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
)

// resolveStack finds the stack for the given branch, handling ambiguity when
// a branch (typically a trunk) belongs to multiple stacks. If exactly one
// stack matches, it is returned directly. If multiple stacks match, the user
// is prompted to select one and the working tree is switched to the top branch
// of the selected stack. Returns nil with no error if no stack contains the
// branch.
func resolveStack(sf *stack.StackFile, branch string, cfg *config.Config) (*stack.Stack, error) {
	stacks := sf.FindAllStacksForBranch(branch)

	switch len(stacks) {
	case 0:
		return nil, nil
	case 1:
		return stacks[0], nil
	}

	if !cfg.IsInteractive() {
		return nil, fmt.Errorf("branch %q belongs to multiple stacks; use an interactive terminal to select one", branch)
	}

	cfg.Warningf("Branch %q is the trunk of multiple stacks", branch)

	options := make([]string, len(stacks))
	for i, s := range stacks {
		options[i] = s.DisplayName()
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selected, err := p.Select("Which stack would you like to use?", "", options)
	if err != nil {
		return nil, fmt.Errorf("stack selection: %w", err)
	}

	s := stacks[selected]

	if len(s.Branches) == 0 {
		return nil, fmt.Errorf("selected stack %q has no branches", s.DisplayName())
	}

	// Switch to the top branch of the selected stack so future commands
	// resolve unambiguously.
	topBranch := s.Branches[len(s.Branches)-1].Branch
	if topBranch != branch {
		if err := git.CheckoutBranch(topBranch); err != nil {
			return nil, fmt.Errorf("failed to checkout branch %s: %w", topBranch, err)
		}
		cfg.Successf("Switched to %s", topBranch)
	}

	return s, nil
}

// syncStackPRs discovers and updates pull request metadata for branches in a stack.
// For each branch, it queries GitHub for the most recent PR and updates the
// PullRequestRef including merge status. Branches with already-merged PRs are skipped.
func syncStackPRs(cfg *config.Config, s *stack.Stack) {
	client, err := cfg.GitHubClient()
	if err != nil {
		return
	}

	for i := range s.Branches {
		b := &s.Branches[i]

		if b.IsMerged() {
			continue
		}

		pr, err := client.FindAnyPRForBranch(b.Branch)
		if err != nil || pr == nil {
			continue
		}

		b.PullRequest = &stack.PullRequestRef{
			Number: pr.Number,
			ID:     pr.ID,
			URL:    pr.URL,
			Merged: pr.Merged,
		}
	}
}

// updateBaseSHAs refreshes the Base and Head SHAs for all active branches
// in a stack. Call this after any operation that may have moved branch refs
// (rebase, push, etc.).
func updateBaseSHAs(s *stack.Stack) {
	// Collect all refs we need to resolve, then batch into one git call.
	var refs []string
	type refPair struct {
		index  int
		parent string
		branch string
	}
	var pairs []refPair
	seen := make(map[string]bool)
	for i := range s.Branches {
		if s.Branches[i].IsMerged() {
			continue
		}
		parent := s.ActiveBaseBranch(s.Branches[i].Branch)
		branch := s.Branches[i].Branch
		pairs = append(pairs, refPair{i, parent, branch})
		if !seen[parent] {
			refs = append(refs, parent)
			seen[parent] = true
		}
		if !seen[branch] {
			refs = append(refs, branch)
			seen[branch] = true
		}
	}
	if len(refs) == 0 {
		return
	}
	shaMap, err := git.RevParseMap(refs)
	if err != nil {
		return
	}
	for _, p := range pairs {
		if base, ok := shaMap[p.parent]; ok {
			s.Branches[p.index].Base = base
		}
		if head, ok := shaMap[p.branch]; ok {
			s.Branches[p.index].Head = head
		}
	}
}

// activeBranchNames returns the branch names for all non-merged branches in a stack.
func activeBranchNames(s *stack.Stack) []string {
	active := s.ActiveBranches()
	names := make([]string, len(active))
	for i, b := range active {
		names[i] = b.Branch
	}
	return names
}
