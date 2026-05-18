package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/branch"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type initOptions struct {
	branches []string
	base     string
	prefix   string
	numbered bool
	adopt    bool // deprecated, kept for backward compat
}

func InitCmd(cfg *config.Config) *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init [branches...]",
		Short: "Initialize a new stack",
		Long: `Initialize a new stack of branches in the current repository.

You can pass multiple branch names to create a multi-layer stack in one
command. Existing branches are adopted automatically; missing branches are
created. By default, the first branch is based on the default branch, and
each subsequent branch is based on the previous one.

Use --base to specify a different trunk branch.`,
		Example: `  # Create a stack with a new branch
  $ gh stack init my-feature

  # Create a multi-layer stack at once
  $ gh stack init auth-layer api-routes ui-components

  # Adopt existing branches into a stack (bottom to top)
  $ gh stack init feat/auth feat/api feat/ui

  # Create a stack with auto-numbered branches (feat/01, feat/02, etc.)
  $ gh stack init --prefix feat --numbered

  # Specify a different trunk branch
  $ gh stack init --base develop my-feature`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.branches = args
			return runInit(cfg, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.base, "base", "b", "", "Trunk branch for stack (defaults to default branch)")
	cmd.Flags().StringVarP(&opts.prefix, "prefix", "p", "", "Branch name prefix for the stack")
	cmd.Flags().BoolVarP(&opts.numbered, "numbered", "n", false, "Use auto-incrementing numbered branch names (requires --prefix)")
	cmd.Flags().BoolVarP(&opts.adopt, "adopt", "a", false, "Deprecated: existing branches are now adopted automatically")
	_ = cmd.Flags().MarkHidden("adopt")

	return cmd
}

func runInit(cfg *config.Config, opts *initOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	// Determine trunk branch
	trunk := opts.base

	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return ErrSilent
	}

	if trunk == "" {
		trunk, err = git.DefaultBranch()
		if err != nil {
			cfg.Errorf("unable to determine default branch\nUse -b to specify the trunk branch")
			return ErrNotInStack
		}
	}

	// Load existing stack file
	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	// Set repository context
	repo, err := cfg.Repo()
	if err == nil {
		sf.Repository = repo.Host + ":" + repo.Owner + "/" + repo.Name
	}

	currentBranch, _ := git.CurrentBranch()

	// Don't allow initializing a stack if the current branch is a non-trunk
	// member of another stack. Trunk branches (e.g. "main") can be shared
	// across multiple stacks.
	if currentBranch != "" {
		for _, s := range sf.FindAllStacksForBranch(currentBranch) {
			if s.IndexOf(currentBranch) >= 0 {
				cfg.Errorf("current branch %q is already part of a stack", currentBranch)
				return ErrInvalidArgs
			}
		}
	}

	// --- Flag validation ---

	// --adopt is deprecated; print a notice and continue normally.
	if opts.adopt {
		cfg.Warningf("The --adopt flag is deprecated. Existing branches are now adopted automatically.")
		cfg.Printf("You can simply run: %s",
			cfg.ColorCyan("gh stack init <branch1> <branch2> ..."))
	}

	// --numbered requires a prefix (either from flag or interactive input).
	if opts.numbered && opts.prefix == "" && !cfg.IsInteractive() {
		cfg.Errorf("--numbered requires --prefix")
		return ErrInvalidArgs
	}

	// Validate explicit --prefix before branch creation.
	if opts.prefix != "" {
		if err := git.ValidateRefName(opts.prefix); err != nil {
			cfg.Errorf("invalid prefix %q: must be a valid git ref component", opts.prefix)
			return ErrInvalidArgs
		}
	}

	// --- Branch collection ---

	var branches []string
	adopted := make(map[string]bool) // tracks which branches were adopted (existed already)

	if len(opts.branches) > 0 {
		// === ARGS PATH ===
		branches, adopted, err = resolveArgBranches(cfg, opts, sf, trunk)
		if err != nil {
			return err
		}

		// Prefix detection (only when --prefix not explicitly set)
		if opts.prefix == "" {
			if detected := detectPrefix(branches); detected != "" {
				opts.prefix = detected
			}
		}

	} else if opts.numbered {
		// === NUMBERED PATH (unchanged) ===
		if opts.prefix == "" && cfg.IsInteractive() {
			prefixInput, err := inputWithPrefill(cfg, "Enter a branch prefix (required for --numbered):", "")
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return ErrSilent
				}
				cfg.Errorf("failed to read prefix: %s", err)
				return ErrSilent
			}
			opts.prefix = strings.TrimSpace(prefixInput)
			if opts.prefix == "" {
				cfg.Errorf("--numbered requires a prefix")
				return ErrInvalidArgs
			}
		}
		branchName := branch.NextNumberedName(opts.prefix, nil)
		if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
			cfg.Errorf("branch %q already exists in a stack", branchName)
			return ErrInvalidArgs
		}
		if git.BranchExists(branchName) {
			adopted[branchName] = true
		} else {
			if err := git.CreateBranch(branchName, trunk); err != nil {
				cfg.Errorf("creating branch %s: %s", branchName, err)
				return ErrSilent
			}
		}
		branches = []string{branchName}

	} else {
		// === INTERACTIVE PATH ===
		if !cfg.IsInteractive() {
			cfg.Errorf("interactive input required; provide branch names as arguments")
			return ErrInvalidArgs
		}

		var interactiveAdopted bool
		branches, interactiveAdopted, err = runInteractiveInit(cfg, sf, trunk, currentBranch, opts)
		if err != nil {
			return err
		}
		if interactiveAdopted {
			adopted[branches[0]] = true
		}
	}

	// --- Build stack ---

	trunkSHA, _ := git.RevParse(trunk)
	branchRefs := make([]stack.BranchRef, len(branches))
	for i, b := range branches {
		parent := trunk
		if i > 0 {
			parent = branches[i-1]
		}
		base, _ := git.MergeBase(b, parent)
		branchRefs[i] = stack.BranchRef{Branch: b, Base: base}
	}

	newStack := stack.Stack{
		Prefix:   opts.prefix,
		Numbered: opts.numbered,
		Trunk: stack.BranchRef{
			Branch: trunk,
			Head:   trunkSHA,
		},
		Branches: branchRefs,
	}

	sf.AddStack(newStack)

	// --- PR detection ---
	// Use FindPRForBranch for all branches. For adopted branches this
	// finds existing PRs; for created branches it harmlessly returns nil.
	latestStack := &sf.Stacks[len(sf.Stacks)-1]
	prCount := 0
	if client, clientErr := cfg.GitHubClient(); clientErr == nil {
		for i := range latestStack.Branches {
			b := &latestStack.Branches[i]
			pr, err := client.FindPRForBranch(b.Branch)
			if err != nil || pr == nil {
				continue
			}
			b.PullRequest = &stack.PullRequestRef{
				Number: pr.Number,
				ID:     pr.ID,
				URL:    pr.URL,
			}
			prCount++
		}
	}

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	// --- Output: switch to top branch + "What's next" ---

	lastBranch := branches[len(branches)-1]
	if currentBranch != lastBranch {
		if err := git.CheckoutBranch(lastBranch); err != nil {
			cfg.Errorf("switching to branch %s: %s", lastBranch, err)
			return ErrSilent
		}
	}

	hasAdopted := len(adopted) > 0

	printWhatsNext(cfg, &newStack, branches, hasAdopted, prCount)

	return nil
}

// resolveArgBranches handles the args path: classifies each branch as
// adopted (exists) or created (missing), validates all before creating any.
func resolveArgBranches(cfg *config.Config, opts *initOptions, sf *stack.StackFile, trunk string) ([]string, map[string]bool, error) {
	adopted := make(map[string]bool)

	// Phase 1: resolve final names, classify, validate
	type branchInfo struct {
		name   string
		exists bool
	}
	resolved := make([]branchInfo, 0, len(opts.branches))

	for _, b := range opts.branches {
		// Apply explicit --prefix (not detected prefix)
		if opts.prefix != "" {
			b = opts.prefix + "/" + b
		}

		// Validate ref name before checking existence or creating
		if err := git.ValidateRefName(b); err != nil {
			cfg.Errorf("invalid branch name %q: must be a valid git ref", b)
			return nil, nil, ErrInvalidArgs
		}

		exists := git.BranchExists(b)

		if err := sf.ValidateNoDuplicateBranch(b); err != nil {
			cfg.Errorf("branch %q already exists in a stack", b)
			return nil, nil, ErrInvalidArgs
		}

		resolved = append(resolved, branchInfo{name: b, exists: exists})
	}

	// Phase 2: create missing branches
	branches := make([]string, 0, len(resolved))
	for i, bi := range resolved {
		if bi.exists {
			adopted[bi.name] = true
		} else {
			parent := trunk
			if i > 0 {
				parent = resolved[i-1].name
			}
			if err := git.CreateBranch(bi.name, parent); err != nil {
				cfg.Errorf("creating branch %s: %s", bi.name, err)
				return nil, nil, ErrSilent
			}
		}
		branches = append(branches, bi.name)
	}

	return branches, adopted, nil
}

// runInteractiveInit runs the interactive init flow: prints hint about
// multi-branch args, offers current branch or new branch, then runs
// prefix detection. Returns the branches and whether the branch was adopted
// (already existed).
func runInteractiveInit(cfg *config.Config, sf *stack.StackFile, trunk, currentBranch string, opts *initOptions) ([]string, bool, error) {
	p := prompter.New(cfg.In, cfg.Out, cfg.Err)

	cfg.Printf("Initializing a stack from %s.", trunk)
	cfg.Printf("Have multiple branches already? Run: %s",
		cfg.ColorCyan("gh stack init <branch1> <branch2> ..."))
	cfg.Printf("")

	var branchName string

	if currentBranch != "" && currentBranch != trunk {
		// On a non-trunk branch — offer select
		options := []string{
			fmt.Sprintf("Use current branch (%s) as the first layer", currentBranch),
			"Create a new branch",
		}
		selectFn := func(prompt, def string, opts []string) (int, error) {
			if cfg.SelectFn != nil {
				return cfg.SelectFn(prompt, def, opts)
			}
			return p.Select(prompt, def, opts)
		}
		selected, err := selectFn("What do you want to start with?", "", options)
		if err != nil {
			if isInterruptError(err) {
				if cfg.SelectFn == nil {
					clearSelectPrompt(cfg, len(options))
				}
				printInterrupt(cfg)
				return nil, false, ErrSilent
			}
			cfg.Errorf("failed to read selection: %s", err)
			return nil, false, ErrSilent
		}

		if selected == 0 {
			// Use current branch
			if err := sf.ValidateNoDuplicateBranch(currentBranch); err != nil {
				cfg.Errorf("branch %q already exists in a stack", currentBranch)
				return nil, false, ErrInvalidArgs
			}
			branchName = currentBranch
		} else {
			// Create a new branch — fall through to input prompt
			name, err := promptBranchName(cfg, opts.prefix)
			if err != nil {
				return nil, false, err
			}
			branchName = name
		}
	} else {
		// On trunk or detached HEAD — prompt for name directly
		name, err := promptBranchName(cfg, opts.prefix)
		if err != nil {
			return nil, false, err
		}
		branchName = name
	}

	// Validate and create branch (track whether it was adopted)
	wasAdopted := false
	if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
		cfg.Errorf("branch %q already exists in a stack", branchName)
		return nil, false, ErrInvalidArgs
	}
	if git.BranchExists(branchName) {
		wasAdopted = true
	} else {
		if err := git.CreateBranch(branchName, trunk); err != nil {
			cfg.Errorf("creating branch %s: %s", branchName, err)
			return nil, false, ErrSilent
		}
	}

	// Prefix detection (interactive path, no --prefix flag)
	if opts.prefix == "" {
		if lastSlash := strings.LastIndex(branchName, "/"); lastSlash > 0 {
			detected := branchName[:lastSlash]
			usePrefix, err := p.Confirm(
				fmt.Sprintf("Use %q as a prefix for new branches in this stack?", detected+"/"),
				true,
			)
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return nil, false, ErrSilent
				}
				// Not fatal — just skip prefix
			} else if usePrefix {
				opts.prefix = detected
			}
		}
	}

	return []string{branchName}, wasAdopted, nil
}

// promptBranchName prompts the user for a branch name, pre-filling the
// prefix in the input when set so the user can see and edit the full name.
func promptBranchName(cfg *config.Config, prefix string) (string, error) {
	prefill := ""
	prompt := "What's the name of the first branch:"
	if prefix != "" {
		prompt = "Enter a name for the first branch:"
		prefill = prefix + "/"
	}
	branchName, err := inputWithPrefill(cfg, prompt, prefill)
	if err != nil {
		if isInterruptError(err) {
			printInterrupt(cfg)
			return "", ErrSilent
		}
		cfg.Errorf("failed to read branch name: %s", err)
		return "", ErrSilent
	}
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		cfg.Errorf("branch name cannot be empty")
		return "", ErrInvalidArgs
	}
	return branchName, nil
}

// detectPrefix finds a common prefix across branches by splitting each
// at its last slash. Returns the prefix (without trailing slash) if all
// branches share the same one, or "" otherwise.
func detectPrefix(branches []string) string {
	if len(branches) == 0 {
		return ""
	}
	var common string
	for i, b := range branches {
		lastSlash := strings.LastIndex(b, "/")
		if lastSlash <= 0 {
			return "" // no slash or leading slash — no prefix
		}
		prefix := b[:lastSlash]
		if i == 0 {
			common = prefix
		} else if prefix != common {
			return "" // different prefixes
		}
	}
	return common
}

// printWhatsNext prints the scenario-aware "What's next" block after init.
func printWhatsNext(cfg *config.Config, s *stack.Stack, branches []string, hasAdopted bool, prCount int) {
	lastBranch := branches[len(branches)-1]

	// Build the chain: main → branch1 → branch2
	parts := []string{s.Trunk.Branch}
	for _, b := range s.Branches {
		parts = append(parts, b.Branch)
	}
	chain := strings.Join(parts, " → ")

	// Success line
	if hasAdopted {
		cfg.Successf("Adopted %d %s: %s",
			len(branches), plural(len(branches), "branch", "branches"), chain)
	} else {
		cfg.Successf("Created stack: %s", chain)
	}

	// Position
	cfg.Printf("  You're on %s (top of stack).", lastBranch)

	// PR summary (only when adopting and at least one PR found)
	if hasAdopted && prCount > 0 {
		cfg.Printf("  Found PRs for %d of %d %s.",
			prCount, len(branches), plural(len(branches), "branch", "branches"))
	}

	cfg.Printf("")
	cfg.Printf("What's next:")
	if hasAdopted {
		cfg.Printf("  • see the full stack:                          %s", cfg.ColorCyan("gh stack view"))
		cfg.Printf("  • move between branches:                       %s", cfg.ColorCyan("gh stack switch"))
		cfg.Printf("  • link these PRs into a Stack on GitHub:       %s", cfg.ColorCyan("gh stack submit"))
	} else {
		cfg.Printf("  • commit your work as usual, then add a layer:  %s", cfg.ColorCyan("gh stack add"))
		cfg.Printf("  • see your stack any time:                      %s", cfg.ColorCyan("gh stack view"))
		cfg.Printf("  • when ready to open PRs:                       %s", cfg.ColorCyan("gh stack submit"))
	}
}
