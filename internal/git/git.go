package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cligit "github.com/cli/cli/v2/git"
)

// client is a shared git client used by all package-level functions.
var client = &cligit.Client{}

// CommitInfo holds metadata about a single commit.
type CommitInfo struct {
	SHA     string
	Subject string
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

// --- Delegated to cligit.Client ---

// GitDir returns the path to the .git directory.
func GitDir() (string, error) {
	return client.GitDir(context.Background())
}

// CurrentBranch returns the name of the current branch.
func CurrentBranch() (string, error) {
	return client.CurrentBranch(context.Background())
}

// BranchExists returns whether a local branch with the given name exists.
func BranchExists(name string) bool {
	return client.HasLocalBranch(context.Background(), name)
}

// CheckoutBranch switches to the specified branch.
func CheckoutBranch(name string) error {
	return client.CheckoutBranch(context.Background(), name)
}

// Fetch fetches from the given remote.
func Fetch(remote string) error {
	return client.Fetch(context.Background(), remote, "")
}

// --- Custom operations not available in cligit ---

// DefaultBranch returns the HEAD branch from origin.
func DefaultBranch() (string, error) {
	ref, err := run("symbolic-ref", "refs/remotes/origin/HEAD")
	// fallback: if origin/HEAD doesn't exist, look for common default branch names
	if err != nil {
		for _, name := range []string{"main", "master"} {
			if BranchExists(name) {
				return name, nil
			}
		}
		return "", err
	}
	return strings.TrimPrefix(ref, "refs/remotes/origin/"), nil
}

// CreateBranch creates a new branch from the given base.
func CreateBranch(name, base string) error {
	return runSilent("branch", name, base)
}

// Push pushes branches to a remote with optional force and atomic flags.
func Push(remote string, branches []string, force, atomic bool) error {
	args := []string{"push", remote}
	if force {
		args = append(args, "--force-with-lease")
	}
	if atomic {
		args = append(args, "--atomic")
	}
	args = append(args, branches...)
	return runSilent(args...)
}

// Rebase rebases the current branch onto the given base.
// If rerere resolves all conflicts automatically, the rebase continues
// without user intervention.
func Rebase(base string) error {
	err := runSilent("rebase", base)
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err)
}

// EnableRerere enables git rerere (reuse recorded resolution) and
// rerere.autoupdate (auto-stage resolved files) for the repository.
func EnableRerere() error {
	if err := runSilent("config", "rerere.enabled", "true"); err != nil {
		return err
	}
	return runSilent("config", "rerere.autoupdate", "true")
}

// RebaseOnto rebases a branch using the three-argument form:
//
//	git rebase --onto <newBase> <oldBase> <branch>
//
// This replays commits after oldBase from branch onto newBase. It is used
// when a prior branch was squash-merged and the normal rebase cannot detect
// which commits have already been applied.
// If rerere resolves all conflicts automatically, the rebase continues
// without user intervention.
func RebaseOnto(newBase, oldBase, branch string) error {
	err := runSilent("rebase", "--onto", newBase, oldBase, branch)
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err)
}

// RebaseContinue continues an in-progress rebase.
// It sets GIT_EDITOR=true to prevent git from opening an interactive editor
// for the commit message, which would cause the command to hang.
// If rerere resolves subsequent conflicts automatically, the rebase continues
// without user intervention.
func RebaseContinue() error {
	err := rebaseContinueOnce()
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err)
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

// RebaseAbort aborts an in-progress rebase.
func RebaseAbort() error {
	return runSilent("rebase", "--abort")
}

// IsRebaseInProgress checks whether a rebase is currently in progress.
func IsRebaseInProgress() bool {
	gitDir, err := GitDir()
	if err != nil {
		return false
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		rebasePath := filepath.Join(gitDir, dir)
		if info, err := os.Stat(rebasePath); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// ConflictedFiles returns the list of files that have merge conflicts.
func ConflictedFiles() ([]string, error) {
	output, err := run("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
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
	output, err := run("diff", "--check", "--", filePath)
	// git diff --check exits non-zero when conflicts exist, so we parse even on error
	if output == "" && err != nil {
		return nil, err
	}

	info := &ConflictMarkerInfo{File: filePath}
	var currentSection *ConflictSection

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "filename:lineno: leftover conflict marker"
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		lineNo, parseErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if parseErr != nil {
			continue
		}
		marker := strings.TrimSpace(parts[2])
		if strings.Contains(marker, "leftover conflict marker") {
			if currentSection == nil || currentSection.EndLine != 0 {
				currentSection = &ConflictSection{StartLine: lineNo}
				info.Sections = append(info.Sections, *currentSection)
			}
			// Update the end line of the last section
			info.Sections[len(info.Sections)-1].EndLine = lineNo
		}
	}

	return info, nil
}

// IsAncestor returns whether ancestor is an ancestor of descendant.
// This is useful to check if a fast-forward merge is possible.
func IsAncestor(ancestor, descendant string) (bool, error) {
	err := runSilent("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	// Exit code 1 means "not an ancestor", which is not an error condition.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// HeadSHA returns the full SHA of the given ref.
func HeadSHA(ref string) (string, error) {
	return run("rev-parse", ref)
}

// MergeBase returns the best common ancestor commit between two refs.
func MergeBase(a, b string) (string, error) {
	return run("merge-base", a, b)
}

// Log returns recent commits for the given branch.
func Log(ref string, maxCount int) ([]CommitInfo, error) {
	format := "%H\t%s\t%at"
	output, err := run("log", ref, "--format="+format, "-n", strconv.Itoa(maxCount))
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}

	var commits []CommitInfo
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		ts, _ := strconv.ParseInt(parts[2], 10, 64)
		commits = append(commits, CommitInfo{
			SHA:     parts[0],
			Subject: parts[1],
			Time:    time.Unix(ts, 0),
		})
	}
	return commits, nil
}

// DeleteBranch deletes a local branch.
func DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	return runSilent("branch", flag, name)
}

// DeleteRemoteBranch deletes a branch on the remote.
func DeleteRemoteBranch(remote, branch string) error {
	return runSilent("push", remote, "--delete", branch)
}

// ResetHard resets the current branch to the given ref.
func ResetHard(ref string) error {
	return runSilent("reset", "--hard", ref)
}

// SetUpstreamTracking sets the upstream tracking branch.
func SetUpstreamTracking(branch, remote string) error {
	return runSilent("branch", "--set-upstream-to="+remote+"/"+branch, branch)
}

// MergeFF fast-forwards the currently checked-out branch using a merge.
func MergeFF(target string) error {
	return runSilent("merge", "--ff-only", target)
}

// UpdateBranchRef moves a branch pointer to a new commit (for branches not currently checked out).
func UpdateBranchRef(branch, sha string) error {
	return runSilent("branch", "-f", branch, sha)
}
