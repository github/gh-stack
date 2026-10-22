package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cligit "github.com/cli/cli/v2/git"
)

// RebaseOpts holds optional parameters for git rebase operations.
type RebaseOpts struct {
	CommitterDateIsAuthorDate bool
}

// ErrRemoteBranchNotFound indicates that a requested branch does not exist on
// the remote. It is distinct from transport, authentication, and other fetch
// failures.
var ErrRemoteBranchNotFound = errors.New("remote branch not found")

// ErrNotInRepository is returned by an unscoped branch lookup when Git cannot
// discover a repository. Scoped identity and invalid explicit Git paths are
// never classified as optional repository absence.
var ErrNotInRepository = errors.New("not in a Git repository")

// Ops defines the interface for git operations used by commands.
// The package-level functions are the default production implementation.
// Tests can substitute a mock via SetOps().
type Ops interface {
	GitDir() (string, error)
	CommonDir() (string, error)
	Worktrees() ([]Worktree, error)
	ForWorktree(path string) (Ops, error)
	CheckVersion() error
	RootDir() (string, error)
	CurrentBranch() (string, error)
	BranchExists(name string) (bool, error)
	CheckoutBranch(name string) error
	Fetch(remote string) error
	FetchBranch(remote, branch string) error
	FetchBranches(remote string, branches []string) error
	DefaultBranch() (string, error)
	CreateBranch(name, base string) error
	Push(remote string, branches []string, force, atomic bool) error
	ResolveRemote(branch string) (string, error)
	Rebase(base string, opts RebaseOpts) error
	EnableRerere() error
	IsRerereEnabled() (bool, error)
	IsRerereDeclined() (bool, error)
	SaveRerereDeclined() error
	GetSavedRemote() (string, error)
	SaveRemote(remote string) error
	ClearRemote() error
	RebaseOnto(newBase, oldBase, branch string, opts RebaseOpts) error
	RebaseContinue(opts RebaseOpts) error
	RebaseAbort() error
	IsRebaseInProgress() (bool, error)
	ConflictedFiles() ([]string, error)
	FindConflictMarkers(filePath string) (*ConflictMarkerInfo, error)
	IsAncestor(ancestor, descendant string) (bool, error)
	RevParse(ref string) (string, error)
	RevParseMulti(refs []string) ([]string, error)
	MergeBase(a, b string) (string, error)
	MergeBaseForkPoint(ref, branch string) (string, error)
	Log(ref string, maxCount int) ([]CommitInfo, error)
	LogRange(base, head string) ([]CommitInfo, error)
	DiffStatRange(base, head string) (additions, deletions int, err error)
	DiffStatFiles(base, head string) ([]FileDiffStat, error)
	DeleteBranch(name string, force bool) error
	DeleteRemoteBranch(remote, branch string) error
	DeleteTrackingRef(remote, branch string) error
	ResetHard(ref string) error
	SetUpstreamTracking(branch, remote string) error
	UpstreamRemote(branch string) (string, error)
	MergeFF(target string) error
	UpdateBranchRef(branch, sha string) error
	StageAll() error
	StageTracked() error
	HasStagedChanges() (bool, error)
	Commit(message string) (string, error)
	CommitInteractive() (string, error)
	ValidateRefName(name string) error
	RenameBranch(oldName, newName string) error
	CherryPick(commits []string) error
	CherryPickQuit() error
	CherryPickAbort() error
	CherryPickContinue() error
	IsCherryPickInProgress() (bool, error)
	HasUncommittedChanges() (bool, error)
	LogMerges(base, head string) ([]CommitInfo, error)
}

// defaultOps implements Ops by delegating to the real git client and helpers.
type defaultOps struct {
	client    *cligit.Client
	scoped    bool
	gitDir    os.FileInfo
	commonDir os.FileInfo
}

var _ Ops = (*defaultOps)(nil)

// ops is the current implementation. Tests replace this via SetOps().
var ops Ops = &defaultOps{}

// SetOps replaces the git operations implementation. Returns a restore function.
func SetOps(o Ops) func() {
	old := ops
	ops = o
	return func() { ops = old }
}

// CurrentOps returns the current Ops implementation.
func CurrentOps() Ops {
	return ops
}

// --- defaultOps method implementations ---

func (d *defaultOps) GitDir() (string, error) {
	return d.path("rev-parse", "--absolute-git-dir")
}

func (d *defaultOps) RootDir() (string, error) {
	return d.path("rev-parse", "--show-toplevel")
}

func (d *defaultOps) CurrentBranch() (string, error) {
	branch, err := d.run("symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		var gitErr *cligit.GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 && gitErr.Stderr == "" {
			return "", cligit.ErrNotOnAnyBranch
		}
		return "", err
	}
	return strings.TrimPrefix(branch, "refs/heads/"), nil
}

func (d *defaultOps) BranchExists(name string) (bool, error) {
	cmd, err := d.command("rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil {
		return false, err
	}
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	if err := cmd.Run(); err != nil {
		var gitErr *cligit.GitError
		if errors.As(err, &gitErr) {
			if gitErr.ExitCode == 1 && gitErr.Stderr == "" {
				return false, nil
			}
			if !d.scoped && gitErr.ExitCode == 128 && strings.HasPrefix(gitErr.Stderr, "fatal: not a git repository (or any ") {
				return false, fmt.Errorf("%w: %w", ErrNotInRepository, err)
			}
		}
		return false, err
	}
	return true, nil
}

func (d *defaultOps) CheckoutBranch(name string) error {
	return d.runSilent("checkout", name)
}

func (d *defaultOps) Fetch(remote string) error {
	c, err := d.gitClient()
	if err != nil {
		return err
	}
	cmd, err := c.AuthenticatedCommand(context.Background(), cligit.AllMatchingCredentialsPattern, "fetch", remote)
	if err != nil {
		return err
	}
	d.configureCommand(cmd)
	return cmd.Run()
}

func (d *defaultOps) FetchBranch(remote, branch string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remote, branch)
	if err := d.runSilent("fetch", remote, refspec); err != nil {
		if isMissingRemoteRefError(err) {
			return fmt.Errorf("%w: %s/%s", ErrRemoteBranchNotFound, remote, branch)
		}
		return err
	}
	return nil
}

func (d *defaultOps) FetchBranches(remote string, branches []string) error {
	if len(branches) == 0 {
		return nil
	}
	// Build explicit refspecs that create/update tracking refs for every
	// branch, regardless of whether a tracking ref already exists.
	// The + prefix allows non-fast-forward tracking-ref updates.
	refspecs := make([]string, len(branches))
	for i, b := range branches {
		refspecs[i] = fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", b, remote, b)
	}
	// Fast path: fetch all branches in a single call.
	args := []string{"fetch", remote}
	args = append(args, refspecs...)
	if err := d.runSilent(args...); err == nil {
		return nil
	}
	// Fallback: one branch may be absent on the remote or deleted since
	// the last fetch. Fetch individually so one missing branch doesn't
	// block the rest, while still surfacing real fetch failures.
	var fetchErr error
	for _, rs := range refspecs {
		err := d.runSilent("fetch", remote, rs)
		if err == nil || isMissingRemoteRefError(err) {
			continue
		}
		if fetchErr == nil {
			fetchErr = fmt.Errorf("fetching from %s: %w", remote, err)
		}
	}
	return fetchErr
}

func isMissingRemoteRefError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "couldn't find remote ref")
}

func (d *defaultOps) DefaultBranch() (string, error) {
	ref, err := d.run("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		var gitErr *cligit.GitError
		if !errors.As(err, &gitErr) || gitErr.ExitCode != 1 || gitErr.Stderr != "" {
			return "", err
		}
		for _, name := range []string{"main", "master"} {
			exists, lookupErr := d.BranchExists(name)
			if lookupErr != nil {
				return "", lookupErr
			}
			if exists {
				return name, nil
			}
		}
		return "", err
	}
	return strings.TrimPrefix(ref, "refs/remotes/origin/"), nil
}

func (d *defaultOps) CreateBranch(name, base string) error {
	return d.runSilent("branch", name, base)
}

func (d *defaultOps) Push(remote string, branches []string, force, atomic bool) error {
	args := []string{"push", remote}
	if force {
		// Build explicit per-branch leases and refspecs. This removes
		// dependence on push.default / upstream configuration and
		// ensures correct lease values for branches whose tracking ref
		// was missing before the preceding FetchBranches call.
		for _, b := range branches {
			trackingRef := fmt.Sprintf("refs/remotes/%s/%s", remote, b)
			sha, err := d.run("rev-parse", "--verify", "--quiet", trackingRef)
			if err == nil && sha != "" {
				// Tracking ref exists: lease against the known SHA.
				args = append(args, fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", b, sha))
			} else {
				// No tracking ref: branch is absent on remote (never
				// pushed). Empty expected value means "must not exist".
				args = append(args, fmt.Sprintf("--force-with-lease=refs/heads/%s:", b))
			}
		}
	}
	if atomic {
		args = append(args, "--atomic")
	}
	// Fully-qualified refspecs: refs/heads/<local>:refs/heads/<remote>.
	// Qualifying the source (not a bare branch name) ensures a branch name is
	// never reinterpreted as refspec syntax — e.g. a leading "+" is part of the
	// ref, not a force modifier. This form is identical whether or not the push
	// is forced; force is supplied out-of-band by the --force-with-lease flags
	// built above.
	for _, b := range branches {
		args = append(args, fmt.Sprintf("refs/heads/%s:refs/heads/%s", b, b))
	}
	return d.runSilent(args...)
}

// ResolveRemote determines the remote for pushing a branch. It checks git
// config keys in priority order (branch.<name>.pushRemote, remote.pushDefault,
// branch.<name>.remote), then checks the gh-stack.remote saved preference,
// then falls back to listing all remotes. If exactly one remote exists it is
// returned. If multiple exist, ErrMultipleRemotes is returned with the list
// attached. If none exist, a plain error is returned.
func (d *defaultOps) ResolveRemote(branch string) (string, error) {
	candidates := []string{
		"branch." + branch + ".pushRemote",
		"remote.pushDefault",
		"branch." + branch + ".remote",
	}
	for _, key := range candidates {
		out, err := d.run("config", "--get", key)
		if err == nil && out != "" {
			return out, nil
		}
	}

	// Check gh-stack saved remote preference.
	if saved, err := d.GetSavedRemote(); err == nil && saved != "" {
		return saved, nil
	}

	out, err := d.run("remote")
	if err != nil {
		return "", fmt.Errorf("could not list remotes: %w", err)
	}
	remotes := strings.Fields(strings.TrimSpace(out))
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	if len(remotes) > 1 {
		return "", &ErrMultipleRemotes{Remotes: remotes}
	}
	return "", fmt.Errorf("no remotes configured")
}

func (d *defaultOps) Rebase(base string, opts RebaseOpts) error {
	args := rebaseArgs(opts)
	args = append(args, base)
	return d.runRebaseCommand(args, opts)
}

func (d *defaultOps) EnableRerere() error {
	if err := d.runSilent("config", "rerere.enabled", "true"); err != nil {
		return err
	}
	return d.runSilent("config", "rerere.autoupdate", "true")
}

func (d *defaultOps) IsRerereEnabled() (bool, error) {
	out, err := d.run("config", "--get", "rerere.enabled")
	if err != nil {
		var gitErr *cligit.GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(out), "true"), nil
}

func (d *defaultOps) IsRerereDeclined() (bool, error) {
	out, err := d.run("config", "--get", "gh-stack.rerere-declined")
	if err != nil {
		var gitErr *cligit.GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(out), "true"), nil
}

func (d *defaultOps) SaveRerereDeclined() error {
	return d.runSilent("config", "gh-stack.rerere-declined", "true")
}

func (d *defaultOps) GetSavedRemote() (string, error) {
	out, err := d.run("config", "--get", "gh-stack.remote")
	if err != nil {
		return "", err
	}
	return out, nil
}

func (d *defaultOps) SaveRemote(remote string) error {
	return d.runSilent("config", "gh-stack.remote", remote)
}

func (d *defaultOps) ClearRemote() error {
	return d.runSilent("config", "--unset", "gh-stack.remote")
}

func (d *defaultOps) RebaseOnto(newBase, oldBase, branch string, opts RebaseOpts) error {
	args := rebaseArgs(opts)
	args = append(args, "--onto", newBase, oldBase, branch)
	return d.runRebaseCommand(args, opts)
}

func (d *defaultOps) RebaseContinue(opts RebaseOpts) error {
	err := d.rebaseContinueOnce(opts)
	if err == nil {
		return nil
	}
	return d.tryAutoResolveRebase(err, opts)
}

func (d *defaultOps) RebaseAbort() error {
	return d.runSilent(append(rebaseArgs(RebaseOpts{}), "--abort")...)
}

func (d *defaultOps) IsRebaseInProgress() (bool, error) {
	return d.rebaseInProgress()
}

func (d *defaultOps) rebaseInProgress() (bool, error) {
	gitDir, err := d.GitDir()
	if err != nil {
		return false, err
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		rebasePath := filepath.Join(gitDir, dir)
		info, err := os.Stat(rebasePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("checking rebase state in %q: %w", gitDir, err)
		}
		if err == nil && info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func (d *defaultOps) ConflictedFiles() ([]string, error) {
	output, err := d.runRaw("diff", "--no-relative", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(output, "\x00"), "\x00"), nil
}

func (d *defaultOps) FindConflictMarkers(filePath string) (*ConflictMarkerInfo, error) {
	root, err := d.RootDir()
	if err != nil {
		return nil, err
	}
	fullPath := filePath
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(root, fullPath)
	}
	fullPath, err = filepath.EvalSymlinks(fullPath)
	if err != nil {
		return nil, fmt.Errorf("resolving conflict file %q: %w", filePath, err)
	}
	relativePath, err := filepath.Rel(root, fullPath)
	if err != nil {
		return nil, err
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("conflict file %q is outside worktree %q", filePath, root)
	}
	cmd, err := d.command("diff", "--no-relative", "--check", "--", ":(top,literal)"+filepath.ToSlash(relativePath))
	if err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	output, err := cmd.Output()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 2) {
		return nil, err
	}

	info := &ConflictMarkerInfo{File: filePath}
	var lines []string
	for _, line := range strings.Split(string(output), "\n") {
		marker := strings.LastIndex(line, ": leftover conflict marker")
		if marker < 0 {
			continue
		}
		// Parse from the right: filenames may contain colons or newlines.
		prefix := line[:marker]
		colon := strings.LastIndexByte(prefix, ':')
		if colon < 0 {
			continue
		}
		lineNo, err := strconv.Atoi(prefix[colon+1:])
		if err != nil {
			return nil, fmt.Errorf("invalid conflict marker location %q: %w", line, err)
		}
		if lines == nil {
			content, err := os.ReadFile(fullPath)
			if err != nil {
				return nil, err
			}
			lines = strings.Split(string(content), "\n")
		}
		if lineNo < 1 || lineNo > len(lines) || lines[lineNo-1] == "" {
			return nil, fmt.Errorf("conflict file %q changed while reading markers", filePath)
		}
		switch lines[lineNo-1][0] {
		case '<':
			info.Sections = append(info.Sections, ConflictSection{StartLine: lineNo})
		case '>':
			if len(info.Sections) > 0 {
				info.Sections[len(info.Sections)-1].EndLine = lineNo
			}
		}
	}

	return info, nil
}

func (d *defaultOps) IsAncestor(ancestor, descendant string) (bool, error) {
	err := d.runSilent("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (d *defaultOps) RevParse(ref string) (string, error) {
	return d.run("rev-parse", ref)
}

func (d *defaultOps) RevParseMulti(refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	args := append([]string{"rev-parse"}, refs...)
	out, err := d.run(args...)
	if err != nil {
		return nil, err
	}
	shas := strings.Split(out, "\n")
	if len(shas) != len(refs) {
		return nil, fmt.Errorf("rev-parse returned %d SHAs for %d refs", len(shas), len(refs))
	}
	return shas, nil
}

func (d *defaultOps) MergeBase(a, b string) (string, error) {
	return d.run("merge-base", a, b)
}

func (d *defaultOps) MergeBaseForkPoint(ref, branch string) (string, error) {
	return d.run("merge-base", "--fork-point", ref, branch)
}

func (d *defaultOps) Log(ref string, maxCount int) ([]CommitInfo, error) {
	format := "%H\t%s\t%at"
	output, err := d.run("log", ref, "--format="+format, "-n", strconv.Itoa(maxCount))
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

func (d *defaultOps) LogRange(base, head string) ([]CommitInfo, error) {
	format := "%H%x01%B%x01%at%x00"
	rangeSpec := base + ".." + head
	output, err := d.run("log", rangeSpec, "--format="+format)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}

	var commits []CommitInfo
	for _, record := range strings.Split(output, "\x00") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x01", 3)
		if len(parts) < 3 {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		subject, body := splitCommitMessage(parts[1])
		commits = append(commits, CommitInfo{
			SHA:     parts[0],
			Subject: subject,
			Body:    body,
			Time:    time.Unix(ts, 0),
		})
	}
	return commits, nil
}

// splitCommitMessage splits a full commit message into subject (first line)
// and body (remaining lines with leading/trailing blank lines trimmed).
func splitCommitMessage(msg string) (subject, body string) {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		subject = msg[:i]
		body = strings.TrimSpace(msg[i+1:])
	} else {
		subject = msg
	}
	return
}

func (d *defaultOps) DiffStatRange(base, head string) (additions, deletions int, err error) {
	output, err := d.run("diff", "--numstat", base+".."+head)
	if err != nil {
		return 0, 0, err
	}
	if output == "" {
		return 0, 0, nil
	}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if parts[0] == "-" {
			continue
		}
		a, _ := strconv.Atoi(parts[0])
		d, _ := strconv.Atoi(parts[1])
		additions += a
		deletions += d
	}
	return additions, deletions, nil
}

func (d *defaultOps) DiffStatFiles(base, head string) ([]FileDiffStat, error) {
	output, err := d.run("diff", "--numstat", base+".."+head)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	var files []FileDiffStat
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		a, _ := strconv.Atoi(parts[0])
		d, _ := strconv.Atoi(parts[1])
		files = append(files, FileDiffStat{
			Path:      parts[2],
			Additions: a,
			Deletions: d,
		})
	}
	return files, nil
}

func (d *defaultOps) DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	return d.runSilent("branch", flag, name)
}

func (d *defaultOps) DeleteRemoteBranch(remote, branch string) error {
	// Fully-qualify the ref so a branch name is never reinterpreted as
	// refspec syntax.
	return d.runSilent("push", remote, "--delete", "refs/heads/"+branch)
}

func (d *defaultOps) DeleteTrackingRef(remote, branch string) error {
	return d.runSilent("branch", "-dr", remote+"/"+branch)
}

func (d *defaultOps) ResetHard(ref string) error {
	return d.runSilent("reset", "--hard", ref)
}

func (d *defaultOps) SetUpstreamTracking(branch, remote string) error {
	return d.runSilent("branch", "--set-upstream-to="+remote+"/"+branch, branch)
}

func (d *defaultOps) UpstreamRemote(branch string) (string, error) {
	return d.run("config", "--get", "branch."+branch+".remote")
}

func (d *defaultOps) MergeFF(target string) error {
	return d.runSilent("-c", "merge.autoStash=false", "merge", "--ff-only", target)
}

func (d *defaultOps) UpdateBranchRef(branch, sha string) error {
	return d.runSilent("branch", "-f", branch, sha)
}

func (d *defaultOps) StageAll() error {
	return d.runSilent("add", "-A")
}

func (d *defaultOps) StageTracked() error {
	return d.runSilent("add", "-u")
}

func (d *defaultOps) HasStagedChanges() (bool, error) {
	cmd, err := d.command("diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	if err := cmd.Run(); err != nil {
		var gitErr *cligit.GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 && gitErr.Stderr == "" {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (d *defaultOps) Commit(message string) (string, error) {
	if err := d.runSilent("commit", "-m", message); err != nil {
		return "", err
	}
	return d.run("rev-parse", "HEAD")
}

// CommitInteractive launches the user's editor for the commit message.
func (d *defaultOps) CommitInteractive() (string, error) {
	if err := d.runInteractive("commit"); err != nil {
		return "", err
	}
	return d.run("rev-parse", "HEAD")
}

func (d *defaultOps) ValidateRefName(name string) error {
	_, err := d.run("check-ref-format", "--branch", name)
	return err
}

func (d *defaultOps) RenameBranch(oldName, newName string) error {
	return d.runSilent("branch", "-m", oldName, newName)
}

func (d *defaultOps) CherryPick(commits []string) error {
	args := append([]string{"cherry-pick"}, commits...)
	return d.runSilent(args...)
}

// CherryPickQuit clears the in-progress cherry-pick sequencer state without
// touching the working tree or index (git cherry-pick --quit). Used to clear
// any stale sequencer state before starting a fresh cherry-pick.
func (d *defaultOps) CherryPickQuit() error {
	return d.runSilent("cherry-pick", "--quit")
}

// CherryPickAbort cancels an in-progress cherry-pick and restores the working
// tree and index to the state before the cherry-pick began
// (git cherry-pick --abort). Errors if no cherry-pick is in progress, so
// callers should gate this with IsCherryPickInProgress.
func (d *defaultOps) CherryPickAbort() error {
	return d.runSilent("cherry-pick", "--abort")
}

func (d *defaultOps) CherryPickContinue() error {
	cmd, err := d.command("cherry-pick", "--continue")
	if err != nil {
		return err
	}
	cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true")
	return cmd.Run()
}

// IsCherryPickInProgress reports whether a cherry-pick is currently in progress
// by checking its native marker and any remaining sequencer picks.
func (d *defaultOps) IsCherryPickInProgress() (bool, error) {
	gitDir, err := d.GitDir()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("checking cherry-pick state in %q: %w", gitDir, err)
	}
	// A manual commit can clear CHERRY_PICK_HEAD while a multi-commit
	// cherry-pick still has pending work.
	todo, err := os.ReadFile(filepath.Join(gitDir, "sequencer", "todo"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking cherry-pick sequencer in %q: %w", gitDir, err)
	}
	for _, line := range strings.Split(string(todo), "\n") {
		if strings.HasPrefix(line, "pick ") {
			return true, nil
		}
	}
	return false, nil
}

func (d *defaultOps) HasUncommittedChanges() (bool, error) {
	out, err := d.run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func (d *defaultOps) LogMerges(base, head string) ([]CommitInfo, error) {
	format := "%H%x01%B%x01%at%x00"
	rangeSpec := base + ".." + head
	output, err := d.run("log", "--merges", rangeSpec, "--format="+format)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}

	var commits []CommitInfo
	for _, record := range strings.Split(output, "\x00") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x01", 3)
		if len(parts) < 3 {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		subject, body := splitCommitMessage(parts[1])
		commits = append(commits, CommitInfo{
			SHA:     parts[0],
			Subject: subject,
			Body:    body,
			Time:    time.Unix(ts, 0),
		})
	}
	return commits, nil
}
