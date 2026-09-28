package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/github/gh-stack/internal/git"
)

// Location records a worktree's stable administration-directory identity.
// Path is refreshed from Git if the worktree has moved.
type Location struct {
	Path string `json:"path"`
	ID   string `json:"id,omitempty"`
}

// Context binds a multi-branch operation to its original worktrees. Touched
// records the last ref value written by the operation, not merely a snapshot:
// recovery must not overwrite commits made after the operation stopped.
type Context struct {
	Origin        Location             `json:"origin"`
	Owners        map[string]*Location `json:"owners,omitempty"`
	Touched       map[string]string    `json:"touched,omitempty"`
	Pending       string               `json:"pending,omitempty"`
	PendingBefore string               `json:"pendingBefore,omitempty"`
}

func New() (*Context, error) {
	root, err := git.RootDir()
	if err != nil {
		return nil, fmt.Errorf("finding initiating worktree: %w", err)
	}
	ctx := &Context{Origin: Location{Path: root}, Owners: make(map[string]*Location), Touched: make(map[string]string)}
	if _, err := ctx.resolve(&ctx.Origin); err != nil {
		return nil, err
	}
	trees, err := git.Worktrees()
	if err != nil {
		return nil, fmt.Errorf("listing worktrees: %w", err)
	}
	for _, tree := range trees {
		if tree.Bare || tree.Branch == "" {
			continue
		}
		if previous, ok := ctx.Owners[tree.Branch]; ok && !SamePath(previous.Path, tree.Path) {
			return nil, fmt.Errorf("branch %s is checked out in multiple worktrees (%s and %s)", tree.Branch, previous.Path, tree.Path)
		}
		ctx.Owners[tree.Branch] = &Location{Path: tree.Path}
	}
	return ctx, nil
}

func SamePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

func locationID(ops git.Ops, common string) (string, error) {
	actualCommon, err := ops.CommonDir()
	if err != nil {
		return "", err
	}
	if !SamePath(actualCommon, common) {
		return "", fmt.Errorf("worktree belongs to a different repository")
	}
	dir, err := ops.GitDir()
	if err != nil {
		return "", err
	}
	if SamePath(common, dir) {
		return ".", nil
	}
	id, err := filepath.Rel(common, dir)
	if err != nil {
		return "", err
	}
	if id != "." && !filepath.IsLocal(id) {
		return "", fmt.Errorf("worktree Git directory %s is outside %s", dir, common)
	}
	return id, nil
}

func (c *Context) resolve(location *Location) (git.Ops, error) {
	if location == nil || !filepath.IsAbs(location.Path) {
		return nil, fmt.Errorf("recovery record has no absolute worktree path")
	}
	if location.ID != "" && location.ID != "." && !filepath.IsLocal(location.ID) {
		return nil, fmt.Errorf("invalid worktree identity %q", location.ID)
	}
	common, err := git.CommonDir()
	if err != nil {
		return nil, err
	}
	ops, resolveErr := git.ForWorktree(location.Path)
	var id string
	if resolveErr == nil {
		id, resolveErr = locationID(ops, common)
	}
	if resolveErr == nil && (location.ID == "" || location.ID == id) {
		location.ID = id
		return ops, nil
	}
	if resolveErr == nil {
		resolveErr = fmt.Errorf("worktree identity changed")
	}
	if location.ID != "" {
		trees, err := git.Worktrees()
		if err != nil {
			return nil, fmt.Errorf("locating moved worktree %s: %w", location.Path, err)
		}
		for _, tree := range trees {
			if tree.Bare {
				continue
			}
			candidate, err := git.ForWorktree(tree.Path)
			if err != nil {
				resolveErr = errors.Join(resolveErr, fmt.Errorf("opening candidate worktree %s: %w", tree.Path, err))
				continue
			}
			candidateID, err := locationID(candidate, common)
			if err == nil && candidateID == location.ID {
				location.Path = tree.Path
				return candidate, nil
			}
			if err != nil {
				resolveErr = errors.Join(resolveErr, fmt.Errorf("identifying candidate worktree %s: %w", tree.Path, err))
			}
		}
	}
	return nil, fmt.Errorf("cannot use worktree %s: %w; restore or repair the worktree before continuing", location.Path, resolveErr)
}

func (c *Context) Location(branch string) *Location {
	if owner := c.Owners[branch]; owner != nil {
		return owner
	}
	return &c.Origin
}

func (c *Context) Ops(branch string) (git.Ops, error) {
	return c.resolve(c.Location(branch))
}

func (c *Context) OriginOps() (git.Ops, error) {
	return c.resolve(&c.Origin)
}

// Busy includes clean but unfinished merge/sequencer operations, which a
// porcelain cleanliness check alone would miss.
func Busy(ops git.Ops) (bool, error) {
	dir, err := ops.GitDir()
	if err != nil {
		return false, err
	}
	rebasing, err := ops.IsRebaseInProgress()
	if err != nil {
		return false, fmt.Errorf("checking rebase state: %w", err)
	}
	picking, err := ops.IsCherryPickInProgress()
	if err != nil {
		return false, fmt.Errorf("checking cherry-pick state: %w", err)
	}
	if rebasing || picking {
		return true, nil
	}
	for _, marker := range []string{"MERGE_HEAD", "REVERT_HEAD", "sequencer", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func CheckClean(ops git.Ops, path string) error {
	busy, err := Busy(ops)
	if err != nil {
		return fmt.Errorf("checking worktree %s: %w", path, err)
	}
	if busy {
		return fmt.Errorf("a Git operation is already in progress in worktree %s; complete or abort it first", path)
	}
	dirty, err := ops.HasUncommittedChanges()
	if err != nil {
		return fmt.Errorf("checking worktree %s: %w", path, err)
	}
	if dirty {
		return fmt.Errorf("uncommitted changes in worktree %s; commit or stash them before continuing", path)
	}
	return nil
}

func (c *Context) Preflight(branches []string) error {
	checked := make(map[string]bool)
	for _, branch := range branches {
		ops, err := c.Ops(branch)
		if err != nil {
			return err
		}
		location := c.Location(branch)
		if !checked[location.Path] {
			if err := CheckClean(ops, location.Path); err != nil {
				return err
			}
			checked[location.Path] = true
		}
		if !SamePath(location.Path, c.Origin.Path) {
			current, err := ops.CurrentBranch()
			if err != nil {
				return fmt.Errorf("checking current branch in worktree %s: %w", location.Path, err)
			}
			if current != branch {
				return fmt.Errorf("worktree %s no longer has branch %s checked out", location.Path, branch)
			}
		}
	}
	return nil
}

// Prepare may switch the initiating worktree, but never a different worktree.
func (c *Context) Prepare(branch string) (git.Ops, error) {
	if err := c.Preflight([]string{branch}); err != nil {
		return nil, err
	}
	ops, err := c.Ops(branch)
	if err != nil {
		return nil, err
	}
	current, err := ops.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if current != branch {
		if err := ops.CheckoutBranch(branch); err != nil {
			return nil, err
		}
	}
	return ops, nil
}

func (c *Context) Start(branch string, expected ...string) error {
	ops, err := c.Ops(branch)
	if err != nil {
		return err
	}
	sha, err := ops.RevParse(branch)
	if err != nil {
		return err
	}
	if len(expected) > 0 && sha != expected[0] {
		return fmt.Errorf("%s changed since this operation's snapshot; leaving it untouched", branch)
	}
	c.Pending, c.PendingBefore = branch, sha
	return nil
}

func (c *Context) Record(branch string) error {
	ops, err := c.Ops(branch)
	if err != nil {
		return err
	}
	sha, err := ops.RevParse(branch)
	if err != nil {
		return err
	}
	if c.Touched == nil {
		c.Touched = make(map[string]string)
	}
	c.Touched[branch] = sha
	c.Pending, c.PendingBefore = "", ""
	return nil
}

func (c *Context) Rename(oldName, newName string) {
	if owner := c.Owners[oldName]; owner != nil {
		c.Owners[newName] = owner
		delete(c.Owners, oldName)
	}
	if sha, ok := c.Touched[oldName]; ok {
		c.Touched[newName] = sha
		delete(c.Touched, oldName)
	}
	if c.Pending == oldName {
		c.Pending = newName
	}
}

func (c *Context) Restore(originalRefs map[string]string) error {
	var failures []string
	if c.Pending != "" {
		ops, err := c.Ops(c.Pending)
		if err != nil {
			failures = append(failures, err.Error())
		} else if sha, err := ops.RevParse(c.Pending); err != nil || sha != c.PendingBefore {
			failures = append(failures, fmt.Sprintf("cannot prove the last update of %s completed safely; restore it manually before retrying", c.Pending))
		} else {
			c.Pending, c.PendingBefore = "", ""
		}
	}
	names := make([]string, 0, len(c.Touched))
	for name := range c.Touched {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, branch := range names {
		original, ok := originalRefs[branch]
		if !ok {
			failures = append(failures, fmt.Sprintf("no original ref recorded for %s", branch))
			continue
		}
		ops, err := c.Ops(branch)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		current, err := ops.RevParse(branch)
		if err != nil {
			failures = append(failures, fmt.Sprintf("reading %s: %v", branch, err))
			continue
		}
		if current == original {
			delete(c.Touched, branch)
			continue
		}
		if current != c.Touched[branch] {
			failures = append(failures, fmt.Sprintf("%s changed after this operation; leaving it untouched", branch))
			continue
		}
		checkedOut, branchErr := ops.CurrentBranch()
		if branchErr == nil && checkedOut == branch {
			if err = CheckClean(ops, c.Location(branch).Path); err == nil {
				err = ops.ResetHard(original)
			}
		} else {
			err = ops.UpdateBranchRef(branch, original)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("restoring %s in %s: %v", branch, c.Location(branch).Path, err))
			continue
		}
		delete(c.Touched, branch)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "\n"))
	}
	return nil
}

func (c *Context) RestoreOrigin(branch string) error {
	ops, err := c.OriginOps()
	if err != nil {
		return err
	}
	current, err := ops.CurrentBranch()
	if err == nil && current == branch {
		return nil
	}
	if err := CheckClean(ops, c.Origin.Path); err != nil {
		return err
	}
	if err := ops.CheckoutBranch(branch); err != nil {
		return fmt.Errorf("restoring checkout %s in %s: %w", branch, c.Origin.Path, err)
	}
	return nil
}
