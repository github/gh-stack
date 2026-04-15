package git

import (
	"context"
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

// rebaseContinueOnce runs a single git rebase --continue without auto-resolve.
func rebaseContinueOnce() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	return cmd.Run()
}

// tryAutoResolveRebase checks whether rerere has resolved all conflicts
// from a failed rebase. If so, it auto-continues the rebase (potentially
// multiple times for multi-commit rebases). Returns originalErr if any
// conflicts remain that need manual resolution.
func tryAutoResolveRebase(originalErr error) error {
	for i := 0; i < 1000; i++ {
		if !IsRebaseInProgress() {
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
func Rebase(base string) error {
	return ops.Rebase(base)
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

// RebaseOnto rebases a branch using the three-argument form:
//
//	git rebase --onto <newBase> <oldBase> <branch>
//
// This replays commits after oldBase from branch onto newBase. It is used
// when a prior branch was merged and the normal rebase cannot detect
// which commits have already been applied.
// If rerere resolves all conflicts automatically, the rebase continues
// without user intervention.
func RebaseOnto(newBase, oldBase, branch string) error {
	return ops.RebaseOnto(newBase, oldBase, branch)
}

// RebaseContinue continues an in-progress rebase.
// It sets GIT_EDITOR=true to prevent git from opening an interactive editor
// for the commit message, which would cause the command to hang.
// If rerere resolves subsequent conflicts automatically, the rebase continues
// without user intervention.
func RebaseContinue() error {
	return ops.RebaseContinue()
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
