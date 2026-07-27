package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	cligit "github.com/cli/cli/v2/git"
)

// client is a shared git client used by all package-level functions.
var client = &cligit.Client{}

// ErrMultipleRemotes is returned by ResolveRemote when multiple remotes
// are configured and none is designated as the push target.
type ErrMultipleRemotes struct {
	Remotes []string
}

func (e *ErrMultipleRemotes) Error() string {
	return fmt.Sprintf("multiple remotes configured: %s", strings.Join(e.Remotes, ", "))
}

// CommitInfo holds metadata about a single commit.
type CommitInfo struct {
	SHA     string
	Subject string
	Body    string
	Time    time.Time
}

// run executes an arbitrary git command via the client and returns trimmed stdout.
func run(args ...string) (string, error) {
	cmd, err := client.Command(context.Background(), args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runSilent executes a git command via the client and only returns an error.
func runSilent(args ...string) error {
	cmd, err := client.Command(context.Background(), args...)
	if err != nil {
		return err
	}
	return cmd.Run()
}

// runInteractive runs a git command with stdin/stdout/stderr connected to
// the terminal, allowing interactive programs like editors to work.
func runInteractive(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// errRebaseAlreadyInProgress is returned when a rebase is started while an
// earlier one is still unresolved.
var errRebaseAlreadyInProgress = errors.New("a rebase is already in progress — resolve or abort it first")

// RebaseStartError indicates that a git rebase command failed before it created
// any rebase state, meaning nothing was rebased at all. Common causes are a
// dirty working tree, a branch that is checked out in another worktree, an
// upstream ref that does not resolve, or a rebase that is already in progress.
//
// It is distinct from a rebase that started and stopped on a conflict: there is
// nothing to --continue or --abort, so callers must treat it as a hard failure
// rather than a recoverable conflict.
type RebaseStartError struct {
	Err error
}

func (e *RebaseStartError) Error() string {
	// cli/cli's GitError prefixes its stderr with "failed to run git: ", which
	// reads as noise once the message is wrapped in gh-stack's own context.
	return strings.TrimSpace(strings.TrimPrefix(e.Err.Error(), "failed to run git: "))
}

func (e *RebaseStartError) Unwrap() error {
	return e.Err
}

// IsRebaseStartError reports whether err indicates a rebase that never started.
func IsRebaseStartError(err error) bool {
	var rse *RebaseStartError
	return errors.As(err, &rse)
}

// runRebaseCommand starts a rebase and classifies the outcome.
//
// A rebase that is already in progress is reported as a *RebaseStartError up
// front: git would refuse the new rebase, and the leftover state belongs to the
// earlier one, so its conflicted files must not be mistaken for this rebase's
// conflicts.
func runRebaseCommand(args []string, opts RebaseOpts) error {
	if IsRebaseInProgress() {
		return &RebaseStartError{Err: errRebaseAlreadyInProgress}
	}
	err := runSilent(args...)
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err, opts)
}

// rebaseContinueOnce runs a single git rebase --continue without auto-resolve.
//
// No rebase options are passed: git rejects "git rebase <option> --continue"
// with a usage error, and it already persists the options the rebase was
// started with (e.g. cdate_is_adate for --committer-date-is-author-date) in the
// rebase state directory, so they are honored automatically.
func rebaseContinueOnce() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	return cmd.Run()
}

// tryAutoResolveRebase checks whether rerere has resolved all conflicts
// from a failed rebase. If so, it auto-continues the rebase (potentially
// multiple times for multi-commit rebases). Returns originalErr if any
// conflicts remain that need manual resolution.
//
// If no rebase is in progress on the very first check, the rebase never started
// and originalErr is returned wrapped in a *RebaseStartError. Treating that
// case as success is what previously made a rebase that did nothing report
// "rebased successfully". On later iterations the same condition legitimately
// means the auto-continued rebase finished.
func tryAutoResolveRebase(originalErr error, opts RebaseOpts) error {
	for i := 0; i < 1000; i++ {
		if !IsRebaseInProgress() {
			if i == 0 {
				return &RebaseStartError{Err: originalErr}
			}
			return nil
		}
		conflicts, err := ConflictedFiles()
		if err != nil {
			return originalErr
		}
		if len(conflicts) > 0 {
			return originalErr
		}
		// Rerere resolved all conflicts — auto-continue.
		if rebaseContinueOnce() == nil {
			return nil
		}
		// Continue hit another conflicting commit; loop to check
		// if rerere resolved that one too.
	}
	return originalErr
}

// --- Public functions delegate through the ops interface ---

// GitDir returns the path to the .git directory.
func GitDir() (string, error) {
	return ops.GitDir()
}

// RootDir returns the repository's root directory.
func RootDir() (string, error) {
	return ops.RootDir()
}

// CurrentBranch returns the name of the current branch.
func CurrentBranch() (string, error) {
	return ops.CurrentBranch()
}

// BranchExists returns whether a local branch with the given name exists.
func BranchExists(name string) bool {
	return ops.BranchExists(name)
}

// CheckoutBranch switches to the specified branch.
func CheckoutBranch(name string) error {
	return ops.CheckoutBranch(name)
}

// Fetch fetches from the given remote.
func Fetch(remote string) error {
	return ops.Fetch(remote)
}

// FetchBranches fetches specific branches from a remote,
// updating their tracking refs.
func FetchBranches(remote string, branches []string) error {
	return ops.FetchBranches(remote, branches)
}

// DefaultBranch returns the HEAD branch from origin.
func DefaultBranch() (string, error) {
	return ops.DefaultBranch()
}

// CreateBranch creates a new branch from the given base.
func CreateBranch(name, base string) error {
	return ops.CreateBranch(name, base)
}

// Push pushes branches to a remote with optional force and atomic flags.
func Push(remote string, branches []string, force, atomic bool) error {
	return ops.Push(remote, branches, force, atomic)
}

// ResolveRemote determines the remote for pushing a branch. Checks git
// config in priority order, falls back to listing remotes. Returns
// *ErrMultipleRemotes if multiple remotes exist with no configured default.
func ResolveRemote(branch string) (string, error) {
	return ops.ResolveRemote(branch)
}

// Rebase rebases the current branch onto the given base.
// If rerere resolves all conflicts automatically, the rebase continues
// without user intervention.
func Rebase(base string, opts RebaseOpts) error {
	return ops.Rebase(base, opts)
}

// EnableRerere enables git rerere (reuse recorded resolution) and
// rerere.autoupdate (auto-stage resolved files) for the repository.
func EnableRerere() error {
	return ops.EnableRerere()
}

// IsRerereEnabled returns whether rerere.enabled is set to "true" in git config.
func IsRerereEnabled() (bool, error) {
	return ops.IsRerereEnabled()
}

// IsRerereDeclined returns whether the user previously declined the rerere prompt.
func IsRerereDeclined() (bool, error) {
	return ops.IsRerereDeclined()
}

// SaveRerereDeclined records that the user declined the rerere prompt.
func SaveRerereDeclined() error {
	return ops.SaveRerereDeclined()
}

// GetSavedRemote returns the remote saved via gh-stack.remote git config,
// or an error if none is configured.
func GetSavedRemote() (string, error) {
	return ops.GetSavedRemote()
}

// SaveRemote persists the given remote name to gh-stack.remote git config.
func SaveRemote(remote string) error {
	return ops.SaveRemote(remote)
}

// ClearRemote removes the gh-stack.remote git config entry.
func ClearRemote() error {
	return ops.ClearRemote()
}

// RebaseOnto rebases a branch using the three-argument form:
//
//	git rebase --onto <newBase> <oldBase> <branch>
//
// This replays commits after oldBase from branch onto newBase. It is used
// when a prior branch was merged and the normal rebase cannot detect
// which commits have already been applied.
// If rerere resolves all conflicts automatically, the rebase continues
// without user intervention.
func RebaseOnto(newBase, oldBase, branch string, opts RebaseOpts) error {
	return ops.RebaseOnto(newBase, oldBase, branch, opts)
}

// RebaseContinue continues an in-progress rebase.
// It sets GIT_EDITOR=true to prevent git from opening an interactive editor
// for the commit message, which would cause the command to hang.
// If rerere resolves subsequent conflicts automatically, the rebase continues
// without user intervention.
func RebaseContinue(opts RebaseOpts) error {
	return ops.RebaseContinue(opts)
}

// RebaseAbort aborts an in-progress rebase.
func RebaseAbort() error {
	return ops.RebaseAbort()
}

// IsRebaseInProgress checks whether a rebase is currently in progress.
func IsRebaseInProgress() bool {
	return ops.IsRebaseInProgress()
}

// ConflictedFiles returns the list of files that have merge conflicts.
func ConflictedFiles() ([]string, error) {
	return ops.ConflictedFiles()
}

// ConflictMarkerInfo holds the location of conflict markers in a file.
type ConflictMarkerInfo struct {
	File     string
	Sections []ConflictSection
}

// ConflictSection represents a single conflict hunk in a file.
type ConflictSection struct {
	StartLine int // line number of <<<<<<<
	EndLine   int // line number of >>>>>>>
}

// FindConflictMarkers scans a file for conflict markers and returns their locations.
func FindConflictMarkers(filePath string) (*ConflictMarkerInfo, error) {
	return ops.FindConflictMarkers(filePath)
}

// IsAncestor returns whether ancestor is an ancestor of descendant.
// This is useful to check if a fast-forward merge is possible.
func IsAncestor(ancestor, descendant string) (bool, error) {
	return ops.IsAncestor(ancestor, descendant)
}

// RevParse resolves a ref to its full SHA via git rev-parse.
func RevParse(ref string) (string, error) {
	return ops.RevParse(ref)
}

// RevParseMulti resolves multiple refs to their full SHAs in a single
// git rev-parse invocation. Returns SHAs in the same order as the input refs.
func RevParseMulti(refs []string) ([]string, error) {
	return ops.RevParseMulti(refs)
}

// RevParseMap resolves multiple refs and returns a ref→SHA map.
func RevParseMap(refs []string) (map[string]string, error) {
	shas, err := ops.RevParseMulti(refs)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(refs))
	for i, ref := range refs {
		m[ref] = shas[i]
	}
	return m, nil
}

// MergeBase returns the best common ancestor commit between two refs.
func MergeBase(a, b string) (string, error) {
	return ops.MergeBase(a, b)
}

// Log returns recent commits for the given branch.
func Log(ref string, maxCount int) ([]CommitInfo, error) {
	return ops.Log(ref, maxCount)
}

// LogRange returns commits in the range base..head (commits reachable from head
// but not from base). This is useful for seeing all commits unique to a branch.
func LogRange(base, head string) ([]CommitInfo, error) {
	return ops.LogRange(base, head)
}

// DiffStatRange returns the total additions and deletions between two refs.
func DiffStatRange(base, head string) (additions, deletions int, err error) {
	return ops.DiffStatRange(base, head)
}

// FileDiffStat holds per-file diff statistics.
type FileDiffStat struct {
	Path      string
	Additions int
	Deletions int
}

// DiffStatFiles returns per-file additions and deletions between two refs.
func DiffStatFiles(base, head string) ([]FileDiffStat, error) {
	return ops.DiffStatFiles(base, head)
}

// DeleteBranch deletes a local branch.
func DeleteBranch(name string, force bool) error {
	return ops.DeleteBranch(name, force)
}

// DeleteRemoteBranch deletes a branch on the remote.
func DeleteRemoteBranch(remote, branch string) error {
	return ops.DeleteRemoteBranch(remote, branch)
}

// DeleteTrackingRef deletes a local remote-tracking ref (e.g. refs/remotes/origin/branch).
func DeleteTrackingRef(remote, branch string) error {
	return ops.DeleteTrackingRef(remote, branch)
}

// ResetHard resets the current branch to the given ref.
func ResetHard(ref string) error {
	return ops.ResetHard(ref)
}

// SetUpstreamTracking sets the upstream tracking branch.
func SetUpstreamTracking(branch, remote string) error {
	return ops.SetUpstreamTracking(branch, remote)
}

// MergeFF fast-forwards the currently checked-out branch using a merge.
func MergeFF(target string) error {
	return ops.MergeFF(target)
}

// UpdateBranchRef moves a branch pointer to a new commit (for branches not currently checked out).
func UpdateBranchRef(branch, sha string) error {
	return ops.UpdateBranchRef(branch, sha)
}

// StageAll stages all changes including untracked files (git add -A).
func StageAll() error {
	return ops.StageAll()
}

// StageTracked stages changes to tracked files only (git add -u).
func StageTracked() error {
	return ops.StageTracked()
}

// HasStagedChanges returns true if there are staged changes ready to commit.
func HasStagedChanges() bool {
	return ops.HasStagedChanges()
}

// Commit creates a commit with the given message and returns the new HEAD SHA.
func Commit(message string) (string, error) {
	return ops.Commit(message)
}

// CommitInteractive launches the user's configured editor for the commit
// message, equivalent to running `git commit` without `-m`.
func CommitInteractive() (string, error) {
	return ops.CommitInteractive()
}

// ValidateRefName checks whether name is a valid git branch name.
func ValidateRefName(name string) error {
	return ops.ValidateRefName(name)
}

// RenameBranch renames a local branch.
func RenameBranch(oldName, newName string) error {
	return ops.RenameBranch(oldName, newName)
}

// CherryPick applies the given commits to the current branch.
func CherryPick(commits []string) error {
	return ops.CherryPick(commits)
}

// CherryPickQuit clears any in-progress cherry-pick sequencer state without
// touching the working tree or index. Errors are silently ignored (no-op if no
// cherry-pick is in progress).
func CherryPickQuit() {
	_ = ops.CherryPickQuit()
}

// CherryPickAbort cancels an in-progress cherry-pick, restoring the working
// tree and index to the state before the cherry-pick began. Callers should
// gate this with IsCherryPickInProgress, as it errors when none is in progress.
func CherryPickAbort() error {
	return ops.CherryPickAbort()
}

// IsCherryPickInProgress reports whether a cherry-pick is currently in progress.
func IsCherryPickInProgress() bool {
	return ops.IsCherryPickInProgress()
}

// CherryPickContinue continues an in-progress cherry-pick after conflicts are resolved.
func CherryPickContinue() error {
	return ops.CherryPickContinue()
}

// HasUncommittedChanges returns true if the working tree has uncommitted changes.
func HasUncommittedChanges() (bool, error) {
	return ops.HasUncommittedChanges()
}

// HasUncommittedTrackedChanges returns true if the working tree has uncommitted
// changes to tracked files. Untracked files are ignored, since git rebase runs
// fine with them present.
func HasUncommittedTrackedChanges() (bool, error) {
	return ops.HasUncommittedTrackedChanges()
}

// StashPush stashes uncommitted changes to tracked files under the given
// message. Untracked files are left in place.
func StashPush(message string) error {
	return ops.StashPush(message)
}

// StashPop restores and drops the most recent stash entry.
func StashPop() error {
	return ops.StashPop()
}

// LogMerges returns merge commits in the range base..head.
func LogMerges(base, head string) ([]CommitInfo, error) {
	return ops.LogMerges(base, head)
}
