package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/mergeview"
	"github.com/spf13/cobra"
)

type mergeOptions struct {
	mergeMethod string
	squash      bool
	rebase      bool
	merge       bool
	yes         bool

	// pollInterval and maxPolls control the status polling loop in the
	// non-interactive path. Zero values fall back to sane defaults; tests set
	// them to keep runs fast.
	pollInterval time.Duration
	maxPolls     int
}

// mergeTarget describes an explicitly requested pull request to merge up to.
type mergeTarget struct {
	prNumber int
	hasPR    bool
}

// MergeCmd builds the `gh stack merge` command.
func MergeCmd(cfg *config.Config) *cobra.Command {
	opts := &mergeOptions{}

	cmd := &cobra.Command{
		Use:   "merge [<stack-number> | <pr-number>]",
		Short: "Merge a stack of pull requests",
		Long: `Merge some or all of a stack of pull requests using GitHub's atomic stack
merge. All members of the stack up to and including your chosen pull request are
merged into the base branch in a single, all-or-nothing operation: if any PR
cannot be merged, none are.

With no argument, the stack for the current branch is used. Pass a stack number
to merge a stack you don't have checked out, or a pull request number to merge
directly up to that PR. A bare number is treated first as a stack number, then
as a pull request number.

In an interactive terminal, a short wizard lets you choose how far up the stack
to merge (everything below your selection is always included), pick the merge
method, and confirm, then shows live progress. In a non-interactive terminal, or
with --yes, the whole stack (or everything up to the given PR) is merged without
prompting, using your last-used merge method unless one is specified.

Only basic pull request state is checked before merging (open and not a draft);
GitHub evaluates branch protection and repository rules when the merge runs, so
any such failure is reported back to you.

If the base branch uses a merge queue, this command isn't supported (it merges
directly, not through the queue); use "gh pr merge" or the web UI instead.`,
		Example: `  # Merge the current stack (interactive picker)
  $ gh stack merge

  # Merge a stack you don't have checked out, by stack number
  $ gh stack merge 7

  # Merge everything up to and including PR #42
  $ gh stack merge 42

  # Merge the whole current stack without prompting, squashing
  $ gh stack merge --yes --squash`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMerge(cfg, opts, args)
		},
	}

	cmd.Flags().StringVar(&opts.mergeMethod, "merge-method", "", "Merge method to use: merge, squash, or rebase")
	cmd.Flags().BoolVar(&opts.merge, "merge", false, "Merge with a merge commit")
	cmd.Flags().BoolVar(&opts.squash, "squash", false, "Squash and merge")
	cmd.Flags().BoolVar(&opts.rebase, "rebase", false, "Rebase and merge")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Merge without prompting for confirmation")

	return cmd
}

func runMerge(cfg *config.Config, opts *mergeOptions, args []string) error {
	method, err := resolveMergeMethodFlag(opts)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrInvalidArgs
	}

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return ErrAPIFailure
	}

	remoteStack, target, err := resolveMergeStack(cfg, client, args)
	if err != nil {
		return err
	}

	candidates, blocker := mergeCandidates(remoteStack)

	preselectIndex := -1
	targetPR := 0
	if target.hasPR {
		idx := indexOfPR(candidates, target.prNumber)
		if idx < 0 {
			return explainNonMergeableTarget(cfg, remoteStack, target.prNumber, blocker)
		}
		preselectIndex = idx
		targetPR = candidates[idx].Number
	} else if len(candidates) == 0 {
		return explainNothingToMerge(cfg, remoteStack, blocker)
	}

	mergeCfg, err := client.RepoMergeConfig()
	if err != nil {
		cfg.Errorf("failed to fetch repository merge settings: %s", err)
		return ErrAPIFailure
	}
	allowed := mergeCfg.AllowedMethods()
	if len(allowed) == 0 {
		cfg.Errorf("this repository does not allow any merge methods")
		return ErrAPIFailure
	}
	if method != "" && !mergeCfg.Allows(method) {
		cfg.Errorf("this repository does not allow %s merges", method)
		return ErrInvalidArgs
	}

	base := remoteStack.Base.Ref

	// Merge-queue and admin-bypass policy for the stack's base branch. The async
	// stack merge cannot use a merge queue, so bail out early (before any
	// prompting) when the base requires one.
	policy, err := client.BaseBranchPolicy(base)
	if err != nil {
		cfg.Errorf("failed to check base branch merge settings: %s", err)
		return ErrAPIFailure
	}
	if policy.RequiresMergeQueue {
		return explainMergeQueueUnsupported(cfg, base)
	}

	if cfg.IsInteractive() && !opts.yes {
		return runMergeInteractive(cfg, client, remoteStack.Number, base, candidates, allowed, mergeCfg.DefaultMethod, method, preselectIndex, opts)
	}

	// Non-interactive (or --yes): merge the whole stack (or up to the given PR)
	// without prompting.
	if !target.hasPR {
		targetPR = candidates[len(candidates)-1].Number
	}
	if method == "" {
		method = mergeCfg.DefaultMethod
		if !mergeCfg.Allows(method) {
			method = allowed[0]
		}
	}
	return runMergeHeadless(cfg, client, base, candidates, targetPR, method, opts)
}

// resolveMergeStack determines the remote stack (and any explicitly targeted PR)
// from the command arguments. It never reads local PR state: the local stack
// file is consulted only to discover the stack number when no argument is given.
func resolveMergeStack(cfg *config.Config, client github.ClientOps, args []string) (*github.RemoteStack, mergeTarget, error) {
	if len(args) == 0 {
		rs, err := resolveActiveRemoteStack(cfg, client)
		return rs, mergeTarget{}, err
	}

	n, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || n <= 0 {
		cfg.Errorf("invalid argument %q: expected a stack number or pull request number", args[0])
		return nil, mergeTarget{}, ErrInvalidArgs
	}

	// Try as a stack number first (mirrors `gh stack checkout`).
	rs, err := client.GetStack(n)
	if err == nil && rs != nil {
		return rs, mergeTarget{}, nil
	}
	if err != nil && !isNotFound(err) {
		cfg.Errorf("failed to fetch stack #%d: %s", n, err)
		return nil, mergeTarget{}, ErrAPIFailure
	}

	// Not a stack number: try as a pull request number.
	rs, err = client.FindStackForPR(n)
	if err != nil {
		if isNotFound(err) {
			warnStacksUnavailable(cfg)
			return nil, mergeTarget{}, ErrStacksUnavailable
		}
		cfg.Errorf("failed to look up pull request #%d: %s", n, err)
		return nil, mergeTarget{}, ErrAPIFailure
	}
	if rs == nil {
		cfg.Errorf("#%d is not a stack number or a stacked pull request", n)
		return nil, mergeTarget{}, ErrNotInStack
	}
	return rs, mergeTarget{prNumber: n, hasPR: true}, nil
}

// resolveActiveRemoteStack reads only the local stack number for the current
// branch, then fetches the full stack (and its PR states) from GitHub.
func resolveActiveRemoteStack(cfg *config.Config, client github.ClientOps) (*github.RemoteStack, error) {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil, ErrNotInStack
	}
	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil, ErrNotInStack
	}
	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil, ErrNotInStack
	}

	stacks := sf.FindAllStacksForBranch(currentBranch)
	if len(stacks) == 0 {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return nil, ErrNotInStack
	}
	if len(stacks) > 1 {
		cfg.Errorf("branch %q belongs to multiple stacks; check out a non-trunk branch first", currentBranch)
		return nil, ErrDisambiguate
	}
	s := stacks[0]
	if s.ID == "" && s.Number == 0 {
		cfg.Errorf("this stack has not been submitted to GitHub yet; run `gh stack submit` first")
		return nil, ErrNotInStack
	}

	number, err := ensureStackNumber(client, s)
	if err != nil {
		if isNotFound(err) {
			warnStacksUnavailable(cfg)
			return nil, ErrStacksUnavailable
		}
		cfg.Errorf("failed to resolve stack number: %s", err)
		return nil, ErrAPIFailure
	}
	if number == 0 {
		cfg.Errorf("could not determine the stack number for the current stack")
		return nil, ErrNotInStack
	}

	rs, err := client.GetStack(number)
	if err != nil {
		if isNotFound(err) {
			warnStacksUnavailable(cfg)
			return nil, ErrStacksUnavailable
		}
		cfg.Errorf("failed to fetch stack #%d: %s", number, err)
		return nil, ErrAPIFailure
	}
	return rs, nil
}

func runMergeInteractive(cfg *config.Config, client github.ClientOps, stackNumber int, base string, candidates []mergeview.PRItem, allowed []string, viewerDefault, methodFlag string, preselectIndex int, opts *mergeOptions) error {
	defaultMethod := viewerDefault
	if methodFlag != "" {
		defaultMethod = methodFlag
	}

	// Enrich the picker with PR titles (best-effort; the branch is shown either way).
	nums := make([]int, len(candidates))
	for i, c := range candidates {
		nums[i] = c.Number
	}
	if titles, err := client.PRTitles(nums); err == nil {
		for i := range candidates {
			if t := titles[candidates[i].Number]; t != "" {
				candidates[i].Title = t
			}
		}
	}

	submit, poll := mergeFuncs(client)

	model := mergeview.New(mergeview.Options{
		PRs:               candidates,
		StackNumber:       stackNumber,
		BaseRef:           base,
		AllowedMethods:    allowed,
		DefaultMethod:     defaultMethod,
		PreselectTopIndex: preselectIndex,
		Submit:            submit,
		Poll:              poll,
		PollInterval:      opts.pollInterval,
	})

	final, err := tea.NewProgram(model, tea.WithInput(cfg.In), tea.WithOutput(cfg.Out)).Run()
	if err != nil {
		cfg.Errorf("failed to run merge: %s", err)
		return ErrSilent
	}

	out := final.(mergeview.Model).Outcome()
	switch {
	case out.Err != nil:
		if errors.Is(out.Err, github.ErrAsyncMergeUnavailable) {
			warnAsyncMergeUnavailable(cfg)
			return ErrStacksUnavailable
		}
		cfg.Errorf("merge failed: %s", out.Err)
		return ErrAPIFailure
	case out.Merged:
		mergedSuccess(cfg, prNumberList(out.MergedPRs), base, out.SHA)
		return nil
	case out.Failed:
		cfg.Errorf("merge failed: %s", out.Message)
		cfg.Printf("Stack merges are atomic, so nothing was merged.")
		return mergeFailureExit(out.Message)
	case out.WatchStopped:
		cfg.Infof("Stopped watching. Merge is still in progress. Check the pull requests on GitHub.")
		return ErrSilent
	default:
		// Cancelled via esc/ctrl+c before submitting.
		cfg.Infof("Cancelled operation, nothing merged")
		return ErrSilent
	}
}

func runMergeHeadless(cfg *config.Config, client github.ClientOps, base string, candidates []mergeview.PRItem, targetPR int, method string, opts *mergeOptions) error {
	nums := numbersUpTo(candidates, targetPR)
	list := prNumberList(nums)

	cfg.Printf("Merging %s into %s via %s...", list, base, method)

	res, err := client.MergeStackAsync(targetPR, method)
	if err != nil {
		if errors.Is(err, github.ErrAsyncMergeUnavailable) {
			warnAsyncMergeUnavailable(cfg)
			return ErrStacksUnavailable
		}
		cfg.Errorf("failed to start merge: %s", err)
		return ErrAPIFailure
	}

	if res.IsMerged() {
		mergedSuccess(cfg, list, base, res.Details.SHA)
		return nil
	}
	if res.IsFailed() {
		cfg.Errorf("merge failed: %s", res.Details.Message)
		cfg.Printf("Stack merges are atomic, so nothing was merged.")
		return mergeFailureExit(res.Details.Message)
	}

	uuid := res.Details.UUID
	if uuid == "" {
		cfg.Errorf("merge did not start as expected")
		return ErrAPIFailure
	}
	interval := opts.pollInterval
	if interval <= 0 {
		interval = time.Second
	}
	maxPolls := opts.maxPolls
	if maxPolls <= 0 {
		maxPolls = 600
	}

	for i := 0; i < maxPolls; i++ {
		time.Sleep(interval)

		status, err := client.GetAsyncMergeResult(targetPR, uuid)
		if err != nil {
			cfg.Errorf("failed to check merge status: %s", err)
			return ErrAPIFailure
		}
		if status.IsMerged() {
			mergedSuccess(cfg, list, base, status.Details.SHA)
			return nil
		}
		if status.IsFailed() {
			cfg.Errorf("merge failed: %s", status.Details.Message)
			cfg.Printf("Stack merges are atomic, so nothing was merged.")
			return mergeFailureExit(status.Details.Message)
		}
	}

	cfg.Warningf("Merge is still in progress. Check the pull requests on GitHub.")
	return ErrAPIFailure
}

// mergeFuncs returns submit/poll closures that adapt the GitHub client to the
// mergeview injection points.
func mergeFuncs(client github.ClientOps) (mergeview.SubmitFunc, mergeview.PollFunc) {
	submit := func(targetPR int, method string) (mergeview.MergeStatus, error) {
		res, err := client.MergeStackAsync(targetPR, method)
		if err != nil {
			return mergeview.MergeStatus{}, err
		}
		return toMergeStatus(res), nil
	}
	poll := func(targetPR int, uuid string) (mergeview.MergeStatus, error) {
		res, err := client.GetAsyncMergeResult(targetPR, uuid)
		if err != nil {
			return mergeview.MergeStatus{}, err
		}
		return toMergeStatus(res), nil
	}
	return submit, poll
}

func toMergeStatus(res *github.AsyncMergeResult) mergeview.MergeStatus {
	status := mergeview.StatusPending
	switch {
	case res.IsMerged():
		status = mergeview.StatusMerged
	case res.IsFailed():
		status = mergeview.StatusFailed
	}
	return mergeview.MergeStatus{
		Status:  status,
		Message: res.Details.Message,
		UUID:    res.Details.UUID,
		SHA:     res.Details.SHA,
	}
}

// mergeCandidates returns the pull requests that can be merged, ordered bottom to
// top: the contiguous run of open, non-draft PRs starting from the bottom of the
// stack (already-merged PRs at the bottom are skipped). The first draft or
// closed PR blocks everything above it and is returned as the blocker.
func mergeCandidates(rs *github.RemoteStack) (items []mergeview.PRItem, blocker *github.RemoteStackPR) {
	if rs == nil {
		return nil, nil
	}
	for i := range rs.PRDetails {
		pr := rs.PRDetails[i]
		if pr.IsMerged() {
			continue
		}
		if pr.Draft || pr.State == "closed" {
			b := pr
			return items, &b
		}
		items = append(items, mergeview.PRItem{Number: pr.Number, Branch: pr.Head.Ref})
	}
	return items, nil
}

func explainNothingToMerge(cfg *config.Config, rs *github.RemoteStack, blocker *github.RemoteStackPR) error {
	if allMerged(rs) {
		cfg.Successf("This stack is already fully merged.")
		return nil
	}
	if blocker != nil {
		cfg.Errorf("nothing to merge: pull request #%d is %s", blocker.Number, blockerState(blocker))
		return ErrNotInStack
	}
	cfg.Errorf("this stack has no open pull requests to merge")
	return ErrNotInStack
}

func explainNonMergeableTarget(cfg *config.Config, rs *github.RemoteStack, prNumber int, blocker *github.RemoteStackPR) error {
	pr := findRemotePR(rs, prNumber)
	switch {
	case pr == nil:
		cfg.Errorf("pull request #%d is not part of this stack", prNumber)
		return ErrInvalidArgs
	case pr.IsMerged():
		cfg.Successf("pull request #%d is already merged", prNumber)
		return nil
	case pr.Draft:
		cfg.Errorf("pull request #%d is a draft; mark it ready for review before merging", prNumber)
		return ErrInvalidArgs
	case pr.State == "closed":
		cfg.Errorf("pull request #%d is closed", prNumber)
		return ErrInvalidArgs
	case blocker != nil:
		cfg.Errorf("pull request #%d cannot be merged yet: #%d below it is %s", prNumber, blocker.Number, blockerState(blocker))
		return ErrInvalidArgs
	default:
		cfg.Errorf("pull request #%d cannot be merged", prNumber)
		return ErrInvalidArgs
	}
}

func warnAsyncMergeUnavailable(cfg *config.Config) {
	cfg.Warningf("Async stack merge is not available for this repository")
}

// explainMergeQueueUnsupported reports that the stack's base branch merges
// through a merge queue, which the async stack merge cannot use, and points the
// user to the merge queue (via `gh pr merge` or the web UI) instead.
func explainMergeQueueUnsupported(cfg *config.Config, base string) error {
	cfg.Errorf("the base branch %q requires a merge queue, which \"gh stack merge\" does not support", base)
	cfg.Printf("Merge this stack using `%q` or from the GitHub web UI instead.", "gh pr merge")
	return ErrSilent
}

// mergeFailureExit maps a merge failure message to an exit code: rebase/merge
// conflicts get ErrConflict, everything else ErrAPIFailure.
func mergeFailureExit(message string) error {
	if strings.Contains(strings.ToLower(message), "conflict") {
		return ErrConflict
	}
	return ErrAPIFailure
}

func resolveMergeMethodFlag(opts *mergeOptions) (string, error) {
	var picks []string
	if opts.merge {
		picks = append(picks, github.MergeMethodMerge)
	}
	if opts.squash {
		picks = append(picks, github.MergeMethodSquash)
	}
	if opts.rebase {
		picks = append(picks, github.MergeMethodRebase)
	}
	if opts.mergeMethod != "" {
		mm := strings.ToLower(strings.TrimSpace(opts.mergeMethod))
		switch mm {
		case github.MergeMethodMerge, github.MergeMethodSquash, github.MergeMethodRebase:
			picks = append(picks, mm)
		default:
			return "", fmt.Errorf("invalid --merge-method %q: must be merge, squash, or rebase", opts.mergeMethod)
		}
	}

	distinct := map[string]struct{}{}
	for _, p := range picks {
		distinct[p] = struct{}{}
	}
	if len(distinct) > 1 {
		return "", errors.New("only one merge method may be specified")
	}
	for p := range distinct {
		return p, nil
	}
	return "", nil
}

func indexOfPR(items []mergeview.PRItem, number int) int {
	for i, it := range items {
		if it.Number == number {
			return i
		}
	}
	return -1
}

func numbersUpTo(items []mergeview.PRItem, targetPR int) []int {
	var nums []int
	for _, it := range items {
		nums = append(nums, it.Number)
		if it.Number == targetPR {
			break
		}
	}
	return nums
}

func prNumberList(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}

func findRemotePR(rs *github.RemoteStack, number int) *github.RemoteStackPR {
	if rs == nil {
		return nil
	}
	for i := range rs.PRDetails {
		if rs.PRDetails[i].Number == number {
			return &rs.PRDetails[i]
		}
	}
	return nil
}

func allMerged(rs *github.RemoteStack) bool {
	if rs == nil || len(rs.PRDetails) == 0 {
		return false
	}
	for i := range rs.PRDetails {
		if !rs.PRDetails[i].IsMerged() {
			return false
		}
	}
	return true
}

func blockerState(pr *github.RemoteStackPR) string {
	if pr.Draft {
		return "a draft"
	}
	if pr.State == "closed" {
		return "closed"
	}
	return "not mergeable"
}

func shortMergeSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// mergedSuccess prints the merge success line, appending the merge commit SHA in
// parentheses when known: "Merged #1, #2 into main (abc1234)".
func mergedSuccess(cfg *config.Config, list, base, sha string) {
	if sha != "" {
		cfg.Successf("Merged %s into %s (%s)", list, base, shortMergeSHA(sha))
		return
	}
	cfg.Successf("Merged %s into %s", list, base)
}

func isNotFound(err error) bool {
	var httpErr *api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}
