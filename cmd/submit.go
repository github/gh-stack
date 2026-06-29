package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/pr"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/github/gh-stack/internal/tui/submitview"
	"github.com/spf13/cobra"
)

type submitOptions struct {
	auto   bool
	open   bool
	remote string
}

func SubmitCmd(cfg *config.Config) *cobra.Command {
	opts := &submitOptions{}

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Create a stack of PRs on GitHub",
		Long: `Push all branches and create or update a stack of PRs on GitHub.

In an interactive terminal, a single-screen editor opens. Every branch without a
PR is included by default; deselect any you don't want with the checkbox or ^x,
and draft each PR's title, description, and draft state, then submit them all at
once with Ctrl+S. Pass --auto (or run in a non-interactive terminal) to skip the
editor and use auto-generated titles.

This command performs several steps:
  1. Pushes all branches to the remote
  2. Creates new PRs for the included branches
  3. Updates base branches for existing PRs
  4. Creates or updates the stack on GitHub

In the editor, new PRs default to ready for review; switch any to draft with the
"CREATE AS" toggle. With --auto, new PRs are created as drafts unless you pass
--open.`,
		Example: `  # Push and create/update PRs (opens the interactive editor)
  $ gh stack submit

  # Skip the editor and use auto-generated PR titles
  $ gh stack submit --auto

  # Mark new and existing PRs as ready for review
  $ gh stack submit --open`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubmit(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.auto, "auto", false, "Use auto-generated PR titles without prompting")
	cmd.Flags().BoolVar(&opts.open, "open", false, "Mark new and existing PRs as ready for review")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to push to (defaults to auto-detected remote)")

	return cmd
}

func runSubmit(cfg *config.Config, opts *submitOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return ErrNotInStack
	}

	cfg.Printf("Checking stack state...")

	// Find the stack for the current branch without switching branches.
	// Submit should never change the user's checked-out branch.
	stacks := sf.FindAllStacksForBranch(currentBranch)
	if len(stacks) == 0 {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return ErrNotInStack
	}
	if len(stacks) > 1 {
		cfg.Errorf("branch %q belongs to multiple stacks; checkout a non-trunk branch first", currentBranch)
		return ErrDisambiguate
	}
	s := stacks[0]

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}

	// Pre-flight: abort early if the user is authenticating with a PAT.
	if cfg.WarnIfPAT() {
		return ErrStacksUnavailable
	}

	// Verify that the repository has stacked PRs enabled.
	stacksAvailable := s.ID != ""
	if !stacksAvailable {
		if _, err := client.ListStacks(); err != nil {
			warnStacksUnavailableOrPAT(cfg)
			if cfg.IsInteractive() {
				p := prompter.New(cfg.In, cfg.Out, cfg.Err)
				proceed, promptErr := p.Confirm("Would you still like to create regular PRs?", false)
				if promptErr != nil {
					if isInterruptError(promptErr) {
						printInterrupt(cfg)
						return ErrSilent
					}
					return ErrStacksUnavailable
				}
				if !proceed {
					return ErrStacksUnavailable
				}
			} else {
				return ErrStacksUnavailable
			}
		} else {
			stacksAvailable = true
		}
	}

	// Sync PR state to detect merged/queued PRs before pushing.
	prDetails := syncStackPRs(cfg, s)

	// If the active branches now sit on top of a fully-merged base, they can no
	// longer extend the existing remote stack. Fork them into a fresh stack
	// rooted at the trunk and continue the submit with that new stack.
	if stacksAvailable {
		s = maybeForkFromMergedBase(cfg, client, sf, s, gitDir)
	}

	// Resolve remote for pushing
	remote, err := pickRemote(cfg, currentBranch, opts.remote)
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return ErrSilent
	}
	merged := s.MergedBranches()
	if len(merged) > 0 {
		cfg.Printf("Skipping %d merged %s", len(merged), plural(len(merged), "branch", "branches"))
	}
	queued := s.QueuedBranches()
	if len(queued) > 0 {
		cfg.Printf("Skipping %d queued %s", len(queued), plural(len(queued), "branch", "branches"))
	}
	activeBranches := activeBranchNames(s)
	if len(activeBranches) == 0 {
		cfg.Printf("All branches are merged or queued, nothing to submit")
		return nil
	}

	// If a modification is pending, delete the old remote stack first so that
	// PR base updates are allowed and force-pushes don't trigger auto-merges.
	if stacksAvailable {
		if err := handlePendingModify(cfg, client, s, gitDir); err != nil {
			if errors.Is(err, errInterrupt) {
				return ErrSilent
			}
			// DeleteStack or other failure — don't continue with stale state
			return ErrSilent
		}
	}

	// Best-effort fetch to update tracking refs (helps --force-with-lease
	// in shallow clones). Silently ignored if branches don't exist on the
	// remote yet.
	_ = git.FetchBranches(remote, activeBranches)

	// Look up the repository's PR template once before creating any PRs.
	var templateContent string
	if repoRoot, err := git.RootDir(); err == nil {
		templateContent = pr.FindTemplate(repoRoot)
	}

	// In an interactive terminal, open the TUI so the user can pick which new
	// branches become PRs and draft each PR's title, description, and draft
	// state. The drafts feed the create path below. On the --auto /
	// non-interactive path drafts stays nil and ensurePR/createPR fall back to
	// auto-generated titles and bodies (today's behavior).
	var drafts map[string]*submitview.PRDraft
	if cfg.IsInteractive() && !opts.auto {
		collected, cancelled, tuiErr := collectPRDrafts(cfg, client, s, currentBranch, prDetails, templateContent)
		if tuiErr != nil {
			cfg.Errorf("failed to run the submit editor: %s", tuiErr)
			return ErrSilent
		}
		if cancelled {
			cfg.Printf("Submit cancelled — no branches were pushed")
			return nil
		}
		drafts = collected
	}

	// Push each branch and create/update its PR in stack order (bottom to top).
	// Sequential pushing ensures each branch's base is up-to-date on the
	// remote before the next branch is pushed, preventing race conditions.
	cfg.Printf("Pushing to %s...", remote)
	for i, b := range s.Branches {
		if s.Branches[i].IsMerged() || s.Branches[i].IsQueued() {
			continue
		}

		// Push this branch
		if err := git.Push(remote, []string{b.Branch}, true, false); err != nil {
			cfg.Errorf("failed to push %s: %s", b.Branch, err)
			return ErrSilent
		}

		// Find or create PR, and fix base if needed
		baseBranch := s.ActiveBaseBranch(b.Branch)
		if err := ensurePR(cfg, client, s, i, baseBranch, opts, templateContent, drafts); err != nil {
			if errors.Is(err, errInterrupt) {
				printInterrupt(cfg)
				return ErrSilent
			}
			// Non-fatal — continue with remaining branches
		}
	}

	// Create or update the stack on GitHub
	if stacksAvailable {
		syncStack(cfg, client, s)
		clearPendingModifyState(cfg, gitDir)
	}

	// Update base commit hashes and sync PR state
	updateBaseSHAs(s)
	_ = syncStackPRs(cfg, s)

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	cfg.Successf("Pushed and synced %d branches", len(s.ActiveBranches()))
	return nil
}

// collectPRDrafts loads branch display data and runs the interactive submit TUI
// so the user can choose which new branches become PRs and draft each one. It
// returns the per-branch overrides, whether the user cancelled, and any error.
// When the stack contains no branches without a PR, it skips the TUI and
// returns nil drafts so the normal push/relink path runs.
func collectPRDrafts(cfg *config.Config, client github.ClientOps, s *stack.Stack, currentBranch string, prDetails map[string]*github.PRDetails, templateContent string) (map[string]*submitview.PRDraft, bool, error) {
	// Fill in the real title/description for existing PRs that were synced
	// without them (e.g. merged branches) so the read-only cards show API data.
	enrichPRContent(client, prDetails)

	fmt.Fprintf(cfg.Err, "Loading stack...")
	viewNodes := stackview.LoadBranchNodes(cfg, s, currentBranch, prDetails)
	fmt.Fprintf(cfg.Err, "\r\033[2K")

	// Reverse so index 0 = top of stack (matches the visual order).
	reversed := make([]stackview.BranchNode, len(viewNodes))
	for i, n := range viewNodes {
		reversed[len(viewNodes)-1-i] = n
	}
	nodes := submitview.NewSubmitNodes(reversed, templateContent)

	// Nothing to create — skip the TUI and run the normal push/relink path.
	if submitview.CountNew(nodes) == 0 {
		return nil, false, nil
	}

	repoLabel := ""
	if repo, err := cfg.Repo(); err == nil {
		repoLabel = repo.Owner + "/" + repo.Name
	}

	model := submitview.New(submitview.Options{
		Nodes:     nodes,
		Trunk:     s.Trunk,
		RepoLabel: repoLabel,
		Version:   Version,
	})

	// Use cell-motion mouse mode (clicks, drag, and wheel) rather than all-motion.
	// All-motion (mode 1003) reports an event on every pointer move, flooding the
	// input; under that volume bubbletea can split an SGR mouse sequence across
	// reads, leaking its bytes as text into a focused title/description field
	// while scrolling. We don't use idle-hover, so cell-motion loses nothing.
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		return nil, false, fmt.Errorf("running submit TUI: %w", err)
	}

	m, ok := final.(submitview.Model)
	if !ok {
		return nil, false, fmt.Errorf("unexpected model type %T", final)
	}
	if m.Cancelled() || !m.SubmitRequested() {
		return nil, true, nil
	}
	return submitview.BuildDrafts(m.Nodes()), false, nil
}

// ensurePR finds or creates a PR for the branch at index i, and updates
// its base branch if needed. This is the single place where PR state is
// reconciled during submit.
//
// drafts holds optional per-branch overrides from the interactive editor. When
// a NEW branch has been deselected in the editor, it is pushed for stack
// consistency but no PR is created for it.
func ensurePR(cfg *config.Config, client github.ClientOps, s *stack.Stack, i int, baseBranch string, opts *submitOptions, templateContent string, drafts map[string]*submitview.PRDraft) error {
	b := s.Branches[i]

	pr, err := client.FindPRForBranch(b.Branch)
	if err != nil {
		cfg.Warningf("failed to check PR for %s: %v", b.Branch, err)
		return nil
	}

	if pr == nil {
		// A NEW branch the user deselected in the editor: pushed for stack
		// consistency, but intentionally left without a PR.
		if d := drafts[b.Branch]; d != nil && !d.Include {
			return nil
		}
		return createPR(cfg, client, s, i, baseBranch, opts, templateContent, drafts)
	}

	// PR exists — record it and fix base if needed.
	if s.Branches[i].PullRequest == nil {
		s.Branches[i].PullRequest = &stack.PullRequestRef{
			Number: pr.Number,
			ID:     pr.ID,
			URL:    pr.URL,
		}
	}

	// Disable auto-merge before adding this PR to a stack. A PR with
	// auto-merge enabled would merge on its own, breaking the stack.
	if pr.IsAutoMergeEnabled() {
		if err := client.DisableAutoMerge(pr.ID); err != nil {
			cfg.Warningf("failed to disable auto-merge for PR %s: %v",
				cfg.PRLink(pr.Number, pr.URL), err)
		} else {
			cfg.Warningf("Disabled auto-merge for PR %s (incompatible with stacked PRs)",
				cfg.PRLink(pr.Number, pr.URL))
		}
	}

	if pr.BaseRefName != baseBranch {
		if s.ID != "" {
			// Stack API owns base relationships — can't update directly.
			cfg.Warningf("PR %s has base %q (expected %q) but cannot update while stacked",
				cfg.PRLink(pr.Number, pr.URL), pr.BaseRefName, baseBranch)
		} else {
			if err := client.UpdatePRBase(pr.Number, baseBranch); err != nil {
				cfg.Warningf("failed to update base branch for PR %s: %v",
					cfg.PRLink(pr.Number, pr.URL), err)
			} else {
				cfg.Successf("Updated base branch for PR %s to %s",
					cfg.PRLink(pr.Number, pr.URL), baseBranch)
			}
		}
	} else {
		cfg.Printf("PR %s for %s is up to date", cfg.PRLink(pr.Number, pr.URL), b.Branch)
	}

	// Convert draft PR to ready for review when --open is set.
	if opts.open && pr.IsDraft {
		if err := client.MarkPRReadyForReview(pr.ID); err != nil {
			cfg.Warningf("failed to mark PR %s as ready for review: %v",
				cfg.PRLink(pr.Number, pr.URL), err)
		} else {
			cfg.Successf("Marked PR %s as ready for review",
				cfg.PRLink(pr.Number, pr.URL))
		}
	}

	return nil
}

// createPR creates a new PR for the branch at index i.
//
// When the interactive editor has supplied a draft override for this branch
// (drafts[branch] != nil), its title, body, and draft state are used verbatim
// — the attribution footer is appended via generatePRBody. Otherwise the
// auto-generated title/body path (with an optional line prompt in interactive
// mode) is used, preserving today's --auto / non-interactive behavior.
func createPR(cfg *config.Config, client github.ClientOps, s *stack.Stack, i int, baseBranch string, opts *submitOptions, templateContent string, drafts map[string]*submitview.PRDraft) error {
	b := s.Branches[i]

	var title, body string
	isDraft := !opts.open

	if d := drafts[b.Branch]; d != nil {
		// Interactive editor override. The user already edited the description
		// in the TUI (prefilled from the repo template when one exists), so
		// d.Body is the final body. Pass no template so generatePRBody keeps the
		// user's text and only appends the attribution footer, rather than
		// discarding their edits in favor of the raw template.
		title = d.Title
		body = generatePRBody(d.Body, "")
		isDraft = d.Draft
	} else {
		// Auto / non-interactive default path: an auto-generated title and a
		// body built from the branch's commits (the interactive title is
		// drafted in the submit TUI instead).
		var commitBody string
		title, commitBody = defaultPRTitleBody(baseBranch, b.Branch)
		body = generatePRBody(commitBody, templateContent)
	}

	newPR, createErr := client.CreatePR(baseBranch, b.Branch, title, body, isDraft)
	if createErr != nil {
		cfg.Warningf("failed to create PR for %s: %v", b.Branch, createErr)
		return nil
	}
	cfg.Successf("Created PR %s for %s", cfg.PRLink(newPR.Number, newPR.URL), b.Branch)
	s.Branches[i].PullRequest = &stack.PullRequestRef{
		Number: newPR.Number,
		ID:     newPR.ID,
		URL:    newPR.URL,
	}
	return nil
}

// defaultPRTitleBody generates a PR title and body from the branch's commits.
// If there is exactly one commit, use its subject as the title and its body
// (if any) as the PR body. Otherwise, humanize the branch name for the title.
func defaultPRTitleBody(base, head string) (string, string) {
	commits, err := git.LogRange(base, head)
	if err == nil && len(commits) == 1 {
		return commits[0].Subject, strings.TrimSpace(commits[0].Body)
	}
	return humanize(head), ""
}

// generatePRBody builds a PR description. When a templateContent is provided,
// it is used as the body and the attribution footer is omitted. Otherwise the
// body is built from the commit body with a footer linking to the CLI.
func generatePRBody(commitBody string, templateContent string) string {
	if templateContent != "" {
		return templateContent
	}

	var parts []string

	if commitBody != "" {
		parts = append(parts, commitBody)
	}

	footer := fmt.Sprintf(
		"<sub>Stack created with <a href=\"https://github.com/github/gh-stack\">GitHub Stacks CLI</a> • <a href=\"%s\">Give Feedback 💬</a></sub>",
		feedbackURL,
	)
	parts = append(parts, footer)

	return strings.Join(parts, "\n\n---\n\n")
}

// humanize replaces hyphens and underscores with spaces.
func humanize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, s)
}

// maybeForkFromMergedBase detects when every PR that is officially part of the
// stack on GitHub has already been merged, and forks the remaining local
// branches (new branches, or open PRs that were never part of that remote stack)
// into a brand-new stack rooted at the trunk.
//
// Once all of a stack's remote PRs are merged — especially after their branches
// are deleted upstream — you can no longer add to that stack: a new PR on top
// would target the trunk, breaking the remote stack's "each PR's base ref ==
// previous PR's head ref" chain. Rather than failing the stack update on GitHub,
// we lift the survivors into a new local stack with no remote ID so the
// subsequent submit creates a fresh stack on GitHub. The original (fully merged)
// remote stack is left untouched on GitHub.
//
// It returns the stack submit should continue with: the new forked stack when a
// fork happens, or the original stack otherwise.
func maybeForkFromMergedBase(cfg *config.Config, client github.ClientOps, sf *stack.StackFile, s *stack.Stack, gitDir string) *stack.Stack {
	// Only meaningful when there is a tracked remote stack to evaluate. A fork
	// can only happen if every remote-stack PR is merged, which implies at least
	// one locally tracked branch is merged — checking that first avoids an extra
	// ListStacks call on the common path.
	if s.ID == "" || len(s.MergedBranches()) == 0 {
		return s
	}

	remotePRs := remoteStackPRs(client, s.ID)
	if len(remotePRs) == 0 {
		return s
	}

	// Every PR officially in the remote stack must be merged. Open PRs that are
	// not part of the remote stack do not count.
	merged := mergedPRNumbers(s)
	for _, n := range remotePRs {
		if !merged[n] {
			return s // a remote-stack PR is still open — not a fork situation
		}
	}

	stackIdx := sf.IndexOfStack(s)
	if stackIdx < 0 {
		return s
	}

	// Partition the local branches: those that are part of the merged remote
	// stack stay behind; everything else (new branches and open PRs that were
	// never part of the remote stack) is forked into a new stack.
	remoteSet := make(map[int]bool, len(remotePRs))
	for _, n := range remotePRs {
		remoteSet[n] = true
	}
	var keepBranches, forkBranches []stack.BranchRef
	for _, b := range s.Branches {
		if b.PullRequest != nil && remoteSet[b.PullRequest.Number] {
			keepBranches = append(keepBranches, b)
		} else {
			forkBranches = append(forkBranches, b)
		}
	}
	if len(forkBranches) == 0 {
		return s // nothing new to fork — the whole stack is merged and done
	}

	// Capture trunk/prefix before mutating sf.Stacks (RemoveStack/AddStack can
	// reallocate the slice and invalidate the s pointer).
	trunk := s.Trunk
	prefix := s.Prefix
	numbered := s.Numbered

	// The bottom surviving branch re-bases onto the trunk.
	if base, err := git.MergeBase(forkBranches[0].Branch, trunk.Branch); err == nil {
		forkBranches[0].Base = base
	}

	cfg.Warningf("Every PR in this stack has already been merged on GitHub")
	cfg.Printf("Adding to a fully merged stack isn't supported — starting a new stack for your %d unmerged %s based on %s",
		len(forkBranches), plural(len(forkBranches), "branch", "branches"), cfg.ColorCyan(trunk.Branch))

	// Decide the fate of the original (fully merged) stack: keep it as a record
	// only if at least one of its branches still exists locally; otherwise drop
	// it. The merged stack is left intact on GitHub either way.
	removeOld := true
	for _, b := range keepBranches {
		if git.BranchExists(b.Branch) {
			removeOld = false
			break
		}
	}

	if removeOld {
		sf.RemoveStack(stackIdx)
	} else {
		sf.Stacks[stackIdx].Branches = keepBranches
	}

	sf.AddStack(stack.Stack{
		Prefix:   prefix,
		Numbered: numbered,
		Trunk:    trunk,
		Branches: forkBranches,
	})

	if err := stack.Save(gitDir, sf); err != nil {
		// Persisting the split failed, but the in-memory model is correct;
		// surface the error and continue so the PRs still get submitted.
		_ = handleSaveError(cfg, err)
	}

	return &sf.Stacks[len(sf.Stacks)-1]
}

// remoteStackPRs returns the PR numbers that are officially part of the remote
// stack identified by stackID, or nil if it can't be determined.
func remoteStackPRs(client github.ClientOps, stackID string) []int {
	stacks, err := client.ListStacks()
	if err != nil {
		return nil
	}
	for _, rs := range stacks {
		if strconv.Itoa(rs.ID) == stackID {
			return rs.PullRequests
		}
	}
	return nil
}

// mergedPRNumbers returns the set of PR numbers whose local branch is marked
// merged. Call after syncStackPRs so the merge flags reflect the remote state.
func mergedPRNumbers(s *stack.Stack) map[int]bool {
	merged := make(map[int]bool)
	for i := range s.Branches {
		b := &s.Branches[i]
		if b.IsMerged() && b.PullRequest != nil {
			merged[b.PullRequest.Number] = true
		}
	}
	return merged
}

// handlePendingModify handles the stack recreation after a modify operation.
// It deletes the old remote stack and clears s.ID so syncStack creates a new
// one. The state file is NOT cleared here — it is cleared after syncStack
// succeeds, ensuring retry safety.
func handlePendingModify(cfg *config.Config, client github.ClientOps, s *stack.Stack, gitDir string) error {
	state, err := modify.LoadState(gitDir)
	if err != nil || state == nil {
		return nil // No modify state — nothing to do
	}
	if state.Phase != modify.PhasePendingSubmit {
		return nil // Not in pending_submit phase
	}

	// Prompt for confirmation before overwriting the remote stack
	if cfg.IsInteractive() {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		proceed, promptErr := p.Confirm("The local stack has been modified. Overwrite the existing stack on GitHub?", true)
		if promptErr != nil {
			if isInterruptError(promptErr) {
				printInterrupt(cfg)
				return errInterrupt
			}
			return promptErr
		}
		if !proceed {
			cfg.Printf("Skipping stack recreation — run `%s` when ready",
				cfg.ColorCyan("gh stack submit"))
			return errInterrupt
		}
	}

	// Delete the old remote stack
	if state.PriorRemoteStackID != "" {
		if err := client.DeleteStack(state.PriorRemoteStackID); err != nil {
			var httpErr *api.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
				cfg.Printf("Previous stack already deleted on GitHub")
			} else {
				cfg.Warningf("Failed to delete existing stack: %v", err)
				cfg.Printf("Run `%s` again to retry", cfg.ColorCyan("gh stack submit"))
				return err
			}
		} else {
			cfg.Successf("Cleared existing stack on GitHub")
		}
		// Clear the old stack ID so syncStack creates a new one
		s.ID = ""
	}

	return nil
}

// clearPendingModifyState clears the modify state file after a successful submit.
// Called after syncStack succeeds to ensure retry safety.
func clearPendingModifyState(cfg *config.Config, gitDir string) {
	if !modify.StateExists(gitDir) {
		return
	}
	modify.ClearState(gitDir)
	cfg.Successf("Stack recreated on GitHub to match local state")
}

// syncStack creates or updates a stack on GitHub from the active PRs.
// If the stack already exists (s.ID is set), it calls the PUT endpoint with
// the full list of PRs to keep the remote stack in sync. If no stack exists
// yet, it calls POST to create one.
// This is a best-effort operation: failures are reported as warnings but do
// not cause the submit command to fail (the PRs are already created).
//
// It returns true when the remote stack object reflects the local stack
// (created, updated, or already in sync) and false otherwise (fewer than two
// PRs, an unresolved divergence, stacked PRs unavailable, or an API failure).
func syncStack(cfg *config.Config, client github.ClientOps, s *stack.Stack) bool {
	// Collect PR numbers in stack order (bottom to top), including merged PRs.
	// The API expects the full list — omitting merged PRs causes a
	// "Stack contents have changed" rejection.
	var prNumbers []int
	for _, b := range s.Branches {
		if b.PullRequest != nil {
			prNumbers = append(prNumbers, b.PullRequest.Number)
		}
	}

	// The API requires at least 2 PRs to form a stack.
	if len(prNumbers) < 2 {
		return false
	}

	if s.ID != "" {
		return updateStack(cfg, client, s, prNumbers)
	}

	// No locally tracked stack ID. The stack may already exist on GitHub
	// (created from the web UI or another clone) without being recorded
	// locally. Adopt it instead of blindly creating a new one, which the API
	// rejects because the PRs are already part of a stack.
	return reconcileUntrackedStack(cfg, client, s, prNumbers)
}

// reconcileUntrackedStack reconciles a locally untracked stack (s.ID == "")
// with the stacks that already exist on GitHub. The PRs in s may already belong
// to a remote stack created from the web UI or another clone; in that case we
// adopt that stack rather than POST a new one (which the API rejects because the
// PRs are already stacked). It creates a new stack when none match, refuses to
// modify a divergent or PR-dropping stack, adopts a matching stack, or updates a
// partially-formed one. It returns true when the remote stack object now
// reflects the local stack.
func reconcileUntrackedStack(cfg *config.Config, client github.ClientOps, s *stack.Stack, prNumbers []int) bool {
	stacks, err := client.ListStacks()
	if err != nil {
		// Couldn't inspect remote state — fall back to the create path, which
		// reports its own errors (handleCreate422 covers "already stacked").
		return createNewStack(cfg, client, s, prNumbers)
	}

	matched, err := findMatchingStack(stacks, prNumbers)
	if err != nil {
		// Our PRs are spread across more than one remote stack. A PR can only
		// belong to one stack, so this is a genuine divergence we can't resolve
		// automatically.
		cfg.Warningf("Your PRs belong to multiple stacks on GitHub — reconcile them first")
		cfg.Printf("  Run `%s` to import a stack, or unstack the PRs from the web",
			cfg.ColorCyan("gh stack checkout <pr>"))
		return false
	}

	if matched == nil {
		// No existing stack contains any of our PRs — create a new one.
		return createNewStack(cfg, client, s, prNumbers)
	}

	// A remote stack already contains some of our PRs. Refuse to silently drop
	// any PRs it holds that we aren't tracking locally; let the user reconcile.
	if dropped := prsMissingFrom(matched.PullRequests, prNumbers); len(dropped) > 0 {
		cfg.Warningf("A stack on GitHub already contains %s, which %s not in your local stack",
			formatPRList(dropped), plural(len(dropped), "is", "are"))
		cfg.Printf("  Run `%s` to import the full stack",
			cfg.ColorCyan("gh stack checkout <pr>"))
		return false
	}

	// Every PR in the remote stack is tracked locally (and we may have added
	// more on top). Adopt the remote stack ID — recording it locally — and
	// update the stack with our full, ordered PR list to append any new PRs.
	s.ID = strconv.Itoa(matched.ID)

	if slicesEqual(matched.PullRequests, prNumbers) {
		cfg.Successf("Linked to the existing stack on GitHub (%d PRs, already up to date)", len(prNumbers))
		return true
	}

	cfg.Infof("Found the stack on GitHub — updating it to match your local stack")
	return updateStack(cfg, client, s, prNumbers)
}

// prsMissingFrom returns the numbers in remote that do not appear in local,
// preserving remote order.
func prsMissingFrom(remote, local []int) []int {
	localSet := make(map[int]bool, len(local))
	for _, n := range local {
		localSet[n] = true
	}
	var missing []int
	for _, n := range remote {
		if !localSet[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

// updateStack calls the PUT endpoint to sync the full PR list for an existing stack.
// If the remote stack was deleted (404), it clears the local ID and falls through
// to createNewStack so the user doesn't need to re-run the command.
// Returns true when the remote stack was updated (or recreated) successfully.
func updateStack(cfg *config.Config, client github.ClientOps, s *stack.Stack, prNumbers []int) bool {
	if err := client.UpdateStack(s.ID, prNumbers); err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case 404:
				// Stack was deleted on GitHub — clear the stale ID and
				// immediately try to re-create it.
				s.ID = ""
				return createNewStack(cfg, client, s, prNumbers)
			case 422:
				// A merged branch whose ref has been deleted upstream breaks the
				// stack's base→head chain, so the update is rejected. This is
				// expected once part of the stack has landed; the unmerged PRs
				// were still pushed and re-based, so explain it calmly rather
				// than alarming the user with a raw API error.
				if strings.Contains(httpErr.Message, "must form a stack") && len(s.MergedBranches()) > 0 {
					cfg.Infof("Merged PRs have left the stack on GitHub, so it wasn't updated — your unmerged PRs were pushed and re-based onto the trunk")
					return false
				}
				cfg.Warningf("Failed to update stack on GitHub: %s", httpErr.Message)
			default:
				cfg.Warningf("Failed to update stack on GitHub: %s", httpErr.Message)
			}
		} else {
			cfg.Warningf("Failed to update stack on GitHub: %v", err)
		}
		return false
	}
	cfg.Successf("Stack updated on GitHub with %d PRs", len(prNumbers))
	return true
}

// createNewStack calls the POST endpoint to create a new stack, handling the
// three types of 422 errors the API may return.
// Returns true when the stack was created or is confirmed already in sync.
func createNewStack(cfg *config.Config, client github.ClientOps, s *stack.Stack, prNumbers []int) bool {
	stackID, err := client.CreateStack(prNumbers)
	if err == nil {
		s.ID = strconv.Itoa(stackID)
		cfg.Successf("Stack created on GitHub with %d PRs", len(prNumbers))
		return true
	}

	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		cfg.Warningf("Failed to create stack on GitHub: %v", err)
		return false
	}

	switch httpErr.StatusCode {
	case 422:
		return handleCreate422(cfg, httpErr, prNumbers)
	case 404:
		warnStacksUnavailableOrPAT(cfg)
		return false
	default:
		cfg.Warningf("Failed to create stack on GitHub: %s", httpErr.Message)
		return false
	}
}

// handleCreate422 handles 422 errors from the create stack endpoint.
// The three known error messages are:
//   - "Stack must contain at least two pull requests"
//   - "Pull requests must form a stack, where each PR's base ref is the previous PR's head ref"
//   - "Pull requests #123, #124, #125 are already stacked"
//
// Returns true only when the PRs are already stacked together (i.e. the remote
// stack already matches), which counts as in sync.
func handleCreate422(cfg *config.Config, httpErr *api.HTTPError, prNumbers []int) bool {
	msg := httpErr.Message

	if isAlreadyStackedError(msg) {
		// Check if the error lists exactly the same PRs we're trying to
		// stack. If so, they're already in a stack together — nothing to do.
		// If only a subset matches, the PRs are in a different stack.
		if allPRsInMessage(msg, prNumbers) {
			cfg.Successf("Stack with %d PRs is up to date", len(prNumbers))
			return true
		}
		cfg.Warningf("One or more PRs are already part of a different stack on GitHub")
		cfg.Printf("  Run `%s` to import the existing stack, or unstack the PRs from the web",
			cfg.ColorCyan("gh stack checkout <pr>"))
		return false
	}

	if strings.Contains(msg, "must form a stack") {
		cfg.Warningf("Cannot create stack: %s", msg)
		cfg.Printf("  Each PR's base branch must match the previous PR's head branch.")
		return false
	}

	// "at least two" or any other validation error
	cfg.Warningf("Could not create stack: %s", msg)
	return false
}

// allPRsInMessage checks whether every PR number in prNumbers appears
// in the error message (e.g. as "#65"). This distinguishes "our PRs are
// already stacked together" from "some PRs are in a different stack."
func allPRsInMessage(msg string, prNumbers []int) bool {
	for _, n := range prNumbers {
		if !strings.Contains(msg, fmt.Sprintf("#%d", n)) {
			return false
		}
	}
	return true
}

// isAlreadyStackedError reports whether a create-stack 422 message indicates
// the PRs already belong to a stack. The server has used more than one phrasing
// ("Pull requests #1, #2 are already stacked", "Pull requests are already part
// of a stack"), so match on the stable substrings rather than an exact string.
func isAlreadyStackedError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already stacked") ||
		strings.Contains(m, "already part of a stack")
}
