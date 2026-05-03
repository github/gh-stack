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
)

// Ops defines the interface for git operations used by commands.
// The package-level functions are the default production implementation.
// Tests can substitute a mock via SetOps().
type Ops interface {
	GitDir() (string, error)
	CurrentBranch() (string, error)
	BranchExists(name string) bool
	CheckoutBranch(name string) error
	Fetch(remote string) error
	DefaultBranch() (string, error)
	CreateBranch(name, base string) error
	Push(remote string, branches []string, force, atomic bool) error
	ResolveRemote(branch string) (string, error)
	Rebase(base string) error
	EnableRerere() error
	IsRerereEnabled() (bool, error)
	IsRerereDeclined() (bool, error)
	SaveRerereDeclined() error
	RebaseOnto(newBase, oldBase, branch string) error
	RebaseContinue() error
	RebaseAbort() error
	IsRebaseInProgress() bool
	ConflictedFiles() ([]string, error)
	FindConflictMarkers(filePath string) (*ConflictMarkerInfo, error)
	IsAncestor(ancestor, descendant string) (bool, error)
	RevParse(ref string) (string, error)
	RevParseMulti(refs []string) ([]string, error)
	MergeBase(a, b string) (string, error)
	Log(ref string, maxCount int) ([]CommitInfo, error)
	LogRange(base, head string) ([]CommitInfo, error)
	DiffStatRange(base, head string) (additions, deletions int, err error)
	DiffStatFiles(base, head string) ([]FileDiffStat, error)
	DeleteBranch(name string, force bool) error
	DeleteRemoteBranch(remote, branch string) error
	ResetHard(ref string) error
	SetUpstreamTracking(branch, remote string) error
	MergeFF(target string) error
	UpdateBranchRef(branch, sha string) error
	StageAll() error
	StageTracked() error
	HasStagedChanges() bool
	Commit(message string) (string, error)
	CommitInteractive() (string, error)
	ValidateRefName(name string) error
	RenameBranch(oldName, newName string) error
	CherryPick(commits []string) error
	CherryPickAbort() error
	HasUncommittedChanges() (bool, error)
	LogMerges(base, head string) ([]CommitInfo, error)
}

// defaultOps implements Ops by delegating to the real git client and helpers.
type defaultOps struct{}

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
	return client.GitDir(context.Background())
}

func (d *defaultOps) CurrentBranch() (string, error) {
	return client.CurrentBranch(context.Background())
}

func (d *defaultOps) BranchExists(name string) bool {
	return client.HasLocalBranch(context.Background(), name)
}

func (d *defaultOps) CheckoutBranch(name string) error {
	return client.CheckoutBranch(context.Background(), name)
}

func (d *defaultOps) Fetch(remote string) error {
	return client.Fetch(context.Background(), remote, "")
}

func (d *defaultOps) DefaultBranch() (string, error) {
	ref, err := run("symbolic-ref", "refs/remotes/origin/HEAD")
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

func (d *defaultOps) CreateBranch(name, base string) error {
	return runSilent("branch", name, base)
}

func (d *defaultOps) Push(remote string, branches []string, force, atomic bool) error {
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

// ResolveRemote determines the remote for pushing a branch. It checks git
// config keys in priority order (branch.<name>.pushRemote, remote.pushDefault,
// branch.<name>.remote), then falls back to listing all remotes. If exactly
// one remote exists it is returned. If multiple exist, ErrMultipleRemotes is
// returned with the list attached. If none exist, a plain error is returned.
func (d *defaultOps) ResolveRemote(branch string) (string, error) {
	candidates := []string{
		"branch." + branch + ".pushRemote",
		"remote.pushDefault",
		"branch." + branch + ".remote",
	}
	for _, key := range candidates {
		out, err := run("config", "--get", key)
		if err == nil && out != "" {
			return out, nil
		}
	}

	out, err := run("remote")
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

func (d *defaultOps) Rebase(base string) error {
	err := runSilent("rebase", base)
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err)
}

func (d *defaultOps) EnableRerere() error {
	if err := runSilent("config", "rerere.enabled", "true"); err != nil {
		return err
	}
	return runSilent("config", "rerere.autoupdate", "true")
}

func (d *defaultOps) IsRerereEnabled() (bool, error) {
	out, err := run("config", "--get", "rerere.enabled")
	if err != nil {
		// Missing key — not enabled.
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(out), "true"), nil
}

func (d *defaultOps) IsRerereDeclined() (bool, error) {
	out, err := run("config", "--get", "gh-stack.rerere-declined")
	if err != nil {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(out), "true"), nil
}

func (d *defaultOps) SaveRerereDeclined() error {
	return runSilent("config", "gh-stack.rerere-declined", "true")
}

func (d *defaultOps) RebaseOnto(newBase, oldBase, branch string) error {
	err := runSilent("rebase", "--onto", newBase, oldBase, branch)
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err)
}

func (d *defaultOps) RebaseContinue() error {
	err := rebaseContinueOnce()
	if err == nil {
		return nil
	}
	return tryAutoResolveRebase(err)
}

func (d *defaultOps) RebaseAbort() error {
	return runSilent("rebase", "--abort")
}

func (d *defaultOps) IsRebaseInProgress() bool {
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

func (d *defaultOps) ConflictedFiles() ([]string, error) {
	output, err := run("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}

func (d *defaultOps) FindConflictMarkers(filePath string) (*ConflictMarkerInfo, error) {
	output, err := run("diff", "--check", "--", filePath)
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
			info.Sections[len(info.Sections)-1].EndLine = lineNo
		}
	}

	return info, nil
}

func (d *defaultOps) IsAncestor(ancestor, descendant string) (bool, error) {
	err := runSilent("merge-base", "--is-ancestor", ancestor, descendant)
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
	return run("rev-parse", ref)
}

func (d *defaultOps) RevParseMulti(refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	args := append([]string{"rev-parse"}, refs...)
	out, err := run(args...)
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
	return run("merge-base", a, b)
}

func (d *defaultOps) Log(ref string, maxCount int) ([]CommitInfo, error) {
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

func (d *defaultOps) LogRange(base, head string) ([]CommitInfo, error) {
	format := "%H%x01%B%x01%at%x00"
	rangeSpec := base + ".." + head
	output, err := run("log", rangeSpec, "--format="+format)
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
	output, err := run("diff", "--numstat", base+".."+head)
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
	output, err := run("diff", "--numstat", base+".."+head)
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
	return runSilent("branch", flag, name)
}

func (d *defaultOps) DeleteRemoteBranch(remote, branch string) error {
	return runSilent("push", remote, "--delete", branch)
}

func (d *defaultOps) ResetHard(ref string) error {
	return runSilent("reset", "--hard", ref)
}

func (d *defaultOps) SetUpstreamTracking(branch, remote string) error {
	return runSilent("branch", "--set-upstream-to="+remote+"/"+branch, branch)
}

func (d *defaultOps) MergeFF(target string) error {
	return runSilent("merge", "--ff-only", target)
}

func (d *defaultOps) UpdateBranchRef(branch, sha string) error {
	return runSilent("branch", "-f", branch, sha)
}

func (d *defaultOps) StageAll() error {
	return runSilent("add", "-A")
}

func (d *defaultOps) StageTracked() error {
	return runSilent("add", "-u")
}

func (d *defaultOps) HasStagedChanges() bool {
	err := runSilent("diff", "--cached", "--quiet")
	return err != nil
}

func (d *defaultOps) Commit(message string) (string, error) {
	if err := runSilent("commit", "-m", message); err != nil {
		return "", err
	}
	return run("rev-parse", "HEAD")
}

// CommitInteractive launches the user's editor for the commit message.
func (d *defaultOps) CommitInteractive() (string, error) {
	if err := runInteractive("commit"); err != nil {
		return "", err
	}
	return run("rev-parse", "HEAD")
}

func (d *defaultOps) ValidateRefName(name string) error {
	_, err := run("check-ref-format", "--branch", name)
	return err
}

func (d *defaultOps) RenameBranch(oldName, newName string) error {
	return runSilent("branch", "-m", oldName, newName)
}

func (d *defaultOps) CherryPick(commits []string) error {
	args := append([]string{"cherry-pick"}, commits...)
	return runSilent(args...)
}

func (d *defaultOps) CherryPickAbort() error {
	return runSilent("cherry-pick", "--quit")
}

func (d *defaultOps) HasUncommittedChanges() (bool, error) {
	out, err := run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func (d *defaultOps) LogMerges(base, head string) ([]CommitInfo, error) {
	format := "%H%x01%B%x01%at%x00"
	rangeSpec := base + ".." + head
	output, err := run("log", "--merges", rangeSpec, "--format="+format)
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
