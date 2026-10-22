package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	cligit "github.com/cli/cli/v2/git"
)

// Worktree describes a registered checkout, including the main worktree.
type Worktree struct {
	Path     string
	Branch   string // Short branch name, including a branch reserved by a rebase.
	Detached bool
	Bare     bool
	Locked   bool
	Prunable bool
}

func (d *defaultOps) path(args ...string) (string, error) {
	out, err := d.runRaw(args...)
	if err != nil {
		return "", err
	}
	// Remove only Git's terminator: whitespace and newlines can belong to paths.
	return strings.TrimSuffix(out, "\n"), nil
}

func (d *defaultOps) CommonDir() (string, error) {
	return d.path("rev-parse", "--path-format=absolute", "--git-common-dir")
}

func (d *defaultOps) gitClient() (*cligit.Client, error) {
	c := d.client
	if c == nil {
		c = client
	}
	for _, directory := range []struct {
		option string
		info   os.FileInfo
	}{
		{"--absolute-git-dir", d.gitDir},
		{"--git-common-dir", d.commonDir},
	} {
		if directory.info == nil {
			continue
		}
		cmd, err := c.Command(context.Background(), "rev-parse", "--path-format=absolute", directory.option)
		if err != nil {
			return nil, err
		}
		d.configureCommand(cmd)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("inspecting worktree %q: %w", c.RepoDir, err)
		}
		info, err := os.Stat(strings.TrimSuffix(string(out), "\n"))
		if err != nil {
			return nil, fmt.Errorf("inspecting worktree %q: %w", c.RepoDir, err)
		}
		if !os.SameFile(directory.info, info) {
			return nil, fmt.Errorf("worktree %q no longer refers to the selected Git directory; rediscover worktrees before continuing", c.RepoDir)
		}
	}
	return c, nil
}

// ForWorktree resolves relative paths against this receiver's execution
// directory. It never changes the process directory or the shared Git client.
func (d *defaultOps) ForWorktree(path string) (Ops, error) {
	if path == "" {
		return nil, errors.New("worktree path must not be empty")
	}
	c, err := d.gitClient()
	if err != nil {
		return nil, err
	}
	common, err := d.CommonDir()
	if err != nil {
		return nil, fmt.Errorf("locating the source repository: %w", err)
	}
	expectedCommon, err := os.Stat(common)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) && c.RepoDir != "" {
		path = filepath.Join(c.RepoDir, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	scoped := &defaultOps{client: c.Copy(), scoped: true}
	scoped.client.RepoDir = path

	pathInfo, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("opening worktree %q: %w", path, err)
	}
	if os.SameFile(pathInfo, expectedCommon) {
		bare, err := scoped.run("--git-dir="+common, "rev-parse", "--is-bare-repository")
		if err != nil {
			return nil, err
		}
		if bare != "true" {
			return nil, fmt.Errorf("cannot use Git administration directory %q as the main worktree; run this command from the main worktree or configure its core.worktree backlink", path)
		}
	}
	selectedCommon, err := scoped.CommonDir()
	if err != nil {
		return nil, fmt.Errorf("opening worktree %q: %w", path, err)
	}
	commonInfo, err := os.Stat(selectedCommon)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(expectedCommon, commonInfo) {
		return nil, fmt.Errorf("worktree %q belongs to a different Git repository", path)
	}
	gitDir, err := scoped.GitDir()
	if err != nil {
		return nil, err
	}
	scoped.gitDir, err = os.Stat(gitDir)
	if err != nil {
		return nil, err
	}
	scoped.commonDir = commonInfo
	return scoped, nil
}

func (d *defaultOps) CheckVersion() error {
	version, err := d.run("--version")
	if err != nil {
		return fmt.Errorf("cannot determine Git version: %w; install Git 2.36 or newer and ensure it is on PATH", err)
	}
	return checkGitVersion(version)
}

func checkGitVersion(version string) error {
	number, found := strings.CutPrefix(version, "git version ")
	fields := strings.Fields(number)
	if !found || len(fields) == 0 {
		return fmt.Errorf("cannot parse Git version %q; install Git 2.36 or newer and check `git --version`", version)
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) < 2 {
		return fmt.Errorf("cannot parse Git version %q; install Git 2.36 or newer and check `git --version`", version)
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major < 0 || minor < 0 {
		return fmt.Errorf("cannot parse Git version %q; install Git 2.36 or newer and check `git --version`", version)
	}
	if major < 2 || (major == 2 && minor < 36) {
		return fmt.Errorf("Git 2.36 or newer is required for worktree support (found %s); upgrade Git and check `git --version`", version)
	}
	return nil
}

func (d *defaultOps) Worktrees() ([]Worktree, error) {
	if err := d.CheckVersion(); err != nil {
		return nil, err
	}
	out, err := d.runRaw("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	worktrees, err := parseWorktrees(out)
	if err != nil {
		return nil, err
	}
	if len(worktrees) > 0 && !worktrees[0].Bare {
		if err := d.resolveMainWorktree(&worktrees[0]); err != nil {
			return nil, err
		}
	}
	var gitDirs map[string]string
	for i := range worktrees {
		wt := &worktrees[i]
		if !wt.Detached || wt.Bare {
			continue
		}
		if gitDirs == nil {
			gitDirs, err = d.worktreeGitDirs(worktrees)
			if err != nil {
				return nil, err
			}
		}
		gitDir, ok := gitDirs[filepath.Clean(wt.Path)]
		if !ok {
			return nil, fmt.Errorf("cannot locate Git directory for worktree %q; retry after worktree changes finish", wt.Path)
		}
		wt.Branch, err = rebaseBranch(gitDir)
		if err != nil {
			return nil, fmt.Errorf("inspecting rebase in worktree %q: %w", wt.Path, err)
		}
	}
	return worktrees, nil
}

func (d *defaultOps) resolveMainWorktree(main *Worktree) error {
	common, err := d.CommonDir()
	if err != nil {
		return err
	}
	gitDir, err := d.GitDir()
	if err != nil {
		return err
	}
	commonInfo, err := os.Stat(common)
	if err != nil {
		return err
	}
	gitDirInfo, err := os.Stat(gitDir)
	if err != nil {
		return err
	}
	if os.SameFile(commonInfo, gitDirInfo) {
		main.Path, err = d.RootDir()
		return err
	}

	// Porcelain infers the main path from the common directory, which is
	// wrong for --separate-git-dir. Only use a backlink Git actually supplies.
	backlink, err := d.mainWorktreeBacklink(common)
	if err != nil {
		return err
	}
	if backlink != "" {
		main.Path = backlink
	}
	return nil
}

func (d *defaultOps) mainWorktreeBacklink(common string) (string, error) {
	c, err := d.gitClient()
	if err != nil {
		return "", err
	}
	main := &defaultOps{client: c.Copy(), scoped: true}
	main.client.RepoDir = common
	// Select the main Git directory explicitly so extensions.worktreeConfig
	// reads its config.worktree, not the invoking linked worktree's config.
	out, err := main.runRaw("--git-dir="+common, "config", "--null", "--path", "--get", "core.worktree")
	if err != nil {
		var gitErr *cligit.GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return "", nil
		}
		return "", fmt.Errorf("reading main worktree backlink in %q: %w", common, err)
	}
	path := strings.TrimSuffix(out, "\x00")
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(common, path)
	}
	return filepath.ToSlash(filepath.Clean(path)), nil
}

func parseWorktrees(output string) ([]Worktree, error) {
	var worktrees []Worktree
	var current *Worktree
	for output != "" {
		field, rest, terminated := strings.Cut(output, "\x00")
		if !terminated {
			return nil, errors.New("invalid git worktree output: missing NUL terminator")
		}
		output = rest
		if field == "" {
			if current != nil {
				worktrees = append(worktrees, *current)
				current = nil
			}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			if current != nil || value == "" {
				return nil, errors.New("invalid git worktree output: missing record separator or path")
			}
			current = &Worktree{Path: value}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("invalid git worktree output: %q precedes worktree path", key)
		}
		switch key {
		case "branch":
			branch, ok := strings.CutPrefix(value, "refs/heads/")
			if !ok || branch == "" {
				return nil, fmt.Errorf("invalid git worktree branch %q", value)
			}
			current.Branch = branch
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	if current != nil {
		return nil, errors.New("invalid git worktree output: unterminated worktree record")
	}
	return worktrees, nil
}

// Read retained administration directories too: a missing/prunable checkout can
// still reserve a branch while its native rebase state exists.
func (d *defaultOps) worktreeGitDirs(worktrees []Worktree) (map[string]string, error) {
	common, err := d.CommonDir()
	if err != nil {
		return nil, err
	}
	dirs := map[string]string{filepath.Clean(worktrees[0].Path): common}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return dirs, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		gitDir := filepath.Join(common, "worktrees", entry.Name())
		data, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
		if err != nil {
			return nil, fmt.Errorf("reading worktree administration directory %q: %w", gitDir, err)
		}
		gitFile := strings.TrimSuffix(string(data), "\n")
		if !filepath.IsAbs(gitFile) {
			gitFile = filepath.Join(gitDir, gitFile)
		}
		dirs[filepath.Dir(gitFile)] = gitDir
	}
	return dirs, nil
}

func rebaseBranch(gitDir string) (string, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		data, err := os.ReadFile(filepath.Join(gitDir, name, "head-name"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if branch, ok := strings.CutPrefix(strings.TrimSuffix(string(data), "\n"), "refs/heads/"); ok {
			return branch, nil
		}
	}
	return "", nil
}
