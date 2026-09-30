package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
)

func commonStackDir(cfg *config.Config) (string, error) {
	if err := git.CheckVersion(); err != nil {
		cfg.Errorf("%s", err)
		return "", ErrSilent
	}
	dir, err := git.CommonDir()
	if err != nil {
		cfg.Errorf("not a git repository: %s", err)
		return "", ErrNotInStack
	}
	if !filepath.IsAbs(dir) {
		cfg.Errorf("could not determine an absolute common Git directory: %q", dir)
		return "", ErrSilent
	}
	return dir, nil
}

// Readers only take the operation lock when legacy state needs migration.
func stackStateDir(cfg *config.Config) (string, error) {
	if cfg.StackMutation != nil {
		return cfg.StackMutation.StateDir, nil
	}
	commonDir, err := commonStackDir(cfg)
	if err != nil {
		return "", err
	}
	legacy, err := stack.HasLegacyState(commonDir)
	if err != nil {
		return "", stackStateError(cfg, "checking legacy stack state", err)
	}
	if !legacy {
		return commonDir, nil
	}
	lock, err := stack.LockOperation(commonDir)
	if err != nil {
		return "", stackStateError(cfg, "acquiring stack operation lock", err)
	}
	defer lock.Unlock()
	if err := stack.MigrateLegacyState(commonDir); err != nil {
		return "", stackStateError(cfg, "migrating stack state", err)
	}
	return commonDir, nil
}

// Nested command calls share the outer command's lock and catalog selection.
func beginStackMutation(cfg *config.Config, kind string) (func(), error) {
	if cfg.StackMutation != nil {
		return func() {}, nil
	}
	commonDir, err := commonStackDir(cfg)
	if err != nil {
		return nil, err
	}
	lock, err := stack.LockOperation(commonDir)
	if err != nil {
		return nil, stackStateError(cfg, "acquiring stack operation lock", err)
	}
	stateDir, err := mutationStateDir(cfg, commonDir, kind)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	cfg.StackMutation = &config.StackMutationContext{CommonDir: commonDir, StateDir: stateDir, Kind: kind}
	released := false
	return func() {
		if !released {
			released = true
			cfg.StackMutation = nil
			lock.Unlock()
		}
	}, nil
}

func stackStateError(cfg *config.Config, action string, err error) error {
	var lockErr *stack.LockError
	if errors.As(err, &lockErr) {
		return stackSaveError(cfg, err)
	}
	cfg.Errorf("%s: %s", action, err)
	return errors.Join(ErrSilent, err)
}

func stackSaveError(cfg *config.Config, err error) error {
	mapped := handleSaveError(cfg, err)
	if errors.Is(mapped, err) {
		return mapped
	}
	return errors.Join(mapped, err)
}

// API-only commands still coordinate when invoked inside a local repository.
func beginOptionalStackMutation(cfg *config.Config, kind string) (func(), error) {
	if cfg.StackMutation == nil {
		if _, err := git.GitDir(); err != nil {
			return func() {}, nil
		}
	}
	return beginStackMutation(cfg, kind)
}

type stackJournal struct {
	dir    string
	path   string
	kind   string
	phase  string
	origin string
	legacy bool
}

func readStackJournals(cfg *config.Config, commonDir, localDir string) ([]stackJournal, error) {
	dirs := []string{commonDir}
	if !worktree.SamePath(commonDir, localDir) {
		dirs = append(dirs, localDir)
	}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		cfg.Errorf("reading worktree recovery directories: %s", err)
		return nil, ErrSilent
	}
	for _, entry := range entries {
		dir := filepath.Join(commonDir, "worktrees", entry.Name())
		if entry.IsDir() && !worktree.SamePath(dir, localDir) {
			dirs = append(dirs, dir)
		}
	}
	var journals []stackJournal
	for _, dir := range dirs {
		for _, kind := range []string{"rebase", "modify"} {
			path := filepath.Join(dir, "gh-stack-"+kind+"-state")
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err == nil && !info.Mode().IsRegular() {
				err = fmt.Errorf("recovery state is not a regular file")
			}
			exitErr := ErrRebaseActive
			if kind == "modify" {
				exitErr = ErrModifyRecovery
			}
			journal := stackJournal{dir: dir, path: path, kind: kind}
			if err == nil {
				var data []byte
				data, err = stack.ReadStateFile(path)
				if err == nil {
					if kind == "rebase" {
						var state *rebaseState
						err = json.Unmarshal(data, &state)
						if err == nil && state == nil {
							err = fmt.Errorf("invalid rebase recovery record")
						}
						if err == nil {
							journal.phase, journal.legacy = state.Phase, state.Worktrees == nil
							if state.Worktrees != nil {
								journal.origin = state.Worktrees.Origin.Path
							}
						}
					} else {
						var state *modify.StateFile
						err = json.Unmarshal(data, &state)
						if err == nil && (state == nil ||
							(state.Phase != modify.PhaseApplying && state.Phase != modify.PhaseConflict && state.Phase != modify.PhasePendingSubmit)) {
							err = fmt.Errorf("invalid modify recovery record")
						}
						if err == nil {
							journal.phase, journal.legacy = state.Phase, state.Worktrees == nil
							if state.Worktrees != nil {
								journal.origin = state.Worktrees.Origin.Path
							}
						}
					}
				}
			}
			if err != nil {
				cfg.Errorf("reading recovery state %s: %s", path, err)
				return nil, exitErr
			}
			// Upgrading a legacy journal does not move its private catalog.
			journal.legacy = journal.legacy || !worktree.SamePath(dir, commonDir)
			journals = append(journals, journal)
		}
	}
	return journals, nil
}

func mutationStateDir(cfg *config.Config, commonDir, kind string) (string, error) {
	localDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("finding worktree Git directory: %s", err)
		return "", ErrNotInStack
	}
	if !filepath.IsAbs(localDir) {
		cfg.Errorf("could not determine an absolute worktree Git directory: %q", localDir)
		return "", ErrSilent
	}
	journals, err := readStackJournals(cfg, commonDir, localDir)
	if err != nil {
		return "", err
	}
	legacyRecovery := false
	for _, journal := range journals {
		if journal.legacy && worktree.SamePath(journal.dir, localDir) && journalAllowsRecovery(journal, kind) {
			legacyRecovery = true
		}
	}
	recoveryDir := ""
	for _, journal := range journals {
		// Independent old-version sessions must be recovered one at a time,
		// each against its own catalog, never against a merged stack index.
		if legacyRecovery && journal.legacy && !worktree.SamePath(journal.dir, localDir) {
			continue
		}
		if journal.kind == "modify" && journal.phase == modify.PhasePendingSubmit {
			continue
		}
		if journalAllowsRecovery(journal, kind) &&
			(!journal.legacy || worktree.SamePath(journal.dir, localDir)) {
			if recoveryDir != "" && !worktree.SamePath(recoveryDir, journal.dir) {
				cfg.Errorf("multiple %s recovery journals found in %s and %s; preserve both catalogs and resolve the conflicting sessions before continuing", journal.kind, recoveryDir, journal.dir)
				if journal.kind == "rebase" {
					return "", ErrRebaseActive
				}
				return "", ErrModifyRecovery
			}
			recoveryDir = journal.dir
			continue
		}
		if journal.legacy {
			cfg.Errorf("a legacy %s session needs recovery in its original worktree (Git directory %s)", journal.kind, journal.dir)
		} else {
			cfg.Errorf("a %s operation is already in progress (%s)", journal.kind, journal.path)
			if journal.origin != "" {
				cfg.Printf("Operation started in worktree %s", journal.origin)
			}
		}
		cfg.Printf("Run `gh stack %s --continue` or `gh stack %s --abort` before another mutation", journal.kind, journal.kind)
		if journal.kind == "rebase" {
			return "", ErrRebaseActive
		}
		return "", ErrModifyRecovery
	}
	if legacyRecovery {
		return localDir, nil
	}
	if recoveryDir != "" {
		// Recovery stays with its saved catalog, even if a legacy main-worktree
		// journal gained a Context while other private catalogs still exist.
		return recoveryDir, nil
	}
	if err := stack.MigrateLegacyState(commonDir); err != nil {
		return "", stackStateError(cfg, "migrating stack state", err)
	}
	return commonDir, nil
}

func journalAllowsRecovery(journal stackJournal, kind string) bool {
	if journal.kind == "modify" && journal.phase == modify.PhasePendingSubmit {
		return kind == "submit" || kind == "modify-abort"
	}
	return kind == journal.kind+"-continue" || kind == journal.kind+"-abort"
}

func noninteractiveConfig(cfg *config.Config) *config.Config {
	copy := *cfg
	copy.NonInteractive = true
	return &copy
}

func stackLookupError(err error) error {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return err
	}
	if errors.Is(err, errInterrupt) {
		return ErrSilent
	}
	return ErrNotInStack
}

func loadNavigationStack(cfg *config.Config, printPath bool) (*loadStackResult, error) {
	if !printPath {
		return loadStack(cfg, "")
	}
	dir, err := stackStateDir(cfg)
	if err != nil {
		return nil, err
	}
	sf, err := stack.Load(dir)
	if err != nil {
		cfg.Errorf("loading stack state: %s", err)
		return nil, ErrNotInStack
	}
	current, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("finding current branch: %s", err)
		return nil, ErrNotInStack
	}
	stacks := sf.FindAllStacksForBranch(current)
	if len(stacks) == 0 {
		reportBranchNotInStack(cfg, current, false)
		return nil, ErrNotInStack
	}
	if len(stacks) > 1 {
		cfg.Errorf("branch %q belongs to multiple stacks; checkout a non-trunk branch first", current)
		return nil, ErrDisambiguate
	}
	return &loadStackResult{GitDir: dir, StackFile: sf, Stack: stacks[0], CurrentBranch: current}, nil
}

func foreignWorktreePath(target string) (string, error) {
	trees, err := git.Worktrees()
	if err != nil {
		return "", fmt.Errorf("listing worktrees: %w", err)
	}
	var owner string
	for _, tree := range trees {
		if tree.Bare || tree.Branch != target {
			continue
		}
		if tree.Path == "" || !filepath.IsAbs(tree.Path) {
			return "", fmt.Errorf("branch %q has an invalid worktree path %q", target, tree.Path)
		}
		if owner != "" && !worktree.SamePath(owner, tree.Path) {
			return "", fmt.Errorf("branch %q is checked out in multiple worktrees (%s and %s)", target, owner, tree.Path)
		}
		if tree.Prunable {
			return "", fmt.Errorf("branch %q is held by unavailable worktree %s; repair the worktree before continuing", target, tree.Path)
		}
		owner = tree.Path
	}
	if owner == "" {
		return "", nil
	}
	root, err := git.RootDir()
	if err != nil {
		return "", fmt.Errorf("finding current worktree: %w", err)
	}
	if worktree.SamePath(root, owner) {
		return "", nil
	}
	common, err := git.CommonDir()
	if err != nil {
		return "", fmt.Errorf("finding common Git directory: %w", err)
	}
	if worktree.SamePath(owner, common) {
		// Git can report the administration directory as the main worktree
		// path for --separate-git-dir. Only that worktree knows its real root.
		localDir, err := git.GitDir()
		if err != nil {
			return "", fmt.Errorf("finding current worktree Git directory: %w", err)
		}
		if worktree.SamePath(localDir, common) {
			return "", nil
		}
		return "", fmt.Errorf("branch %q is held by the main worktree, but Git reports only its separate Git directory %s; run this command from the main worktree", target, common)
	}
	ownerOps, err := git.ForWorktree(owner)
	if err != nil {
		return "", fmt.Errorf("opening worktree %s: %w", owner, err)
	}
	ownerCommon, err := ownerOps.CommonDir()
	if err != nil {
		return "", fmt.Errorf("inspecting worktree %s: %w", owner, err)
	}
	if !worktree.SamePath(common, ownerCommon) {
		return "", fmt.Errorf("worktree %s no longer belongs to this repository", owner)
	}
	return owner, nil
}

// Until owner-scoped rebase/sync execution is enabled, include every possible
// write and rollback target, not just the requested rebase range.
func requireLocalBranches(cfg *config.Config, branches []string) error {
	seen := make(map[string]bool)
	for _, branch := range branches {
		if branch == "" || seen[branch] {
			continue
		}
		seen[branch] = true
		owner, err := foreignWorktreePath(branch)
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
		if owner != "" {
			reportWorktreeOwner(cfg, branch, owner)
			cfg.Errorf("cross-worktree rebase and sync are not supported yet; all affected branches must be unoccupied or checked out in this worktree")
			return ErrInvalidArgs
		}
	}
	return nil
}

func reportWorktreeOwner(cfg *config.Config, target, path string) {
	commandPath := path
	if strings.ContainsFunc(path, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("/._-+@%=,:", r))
	}) {
		commandPath = "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
	}
	cfg.Infof("Branch %q is already checked out in another worktree.", target)
	cfg.Printf("  Your current checkout is unchanged.")
	cfg.Printf("")
	cfg.Printf("To work on this branch, run:")
	cfg.Printf("  %s", cfg.ColorCyan("cd "+commandPath))
}

func checkoutWorktreeBranch(cfg *config.Config, target string, printPath bool) error {
	if target == "" {
		cfg.Errorf("a target branch is required")
		return ErrInvalidArgs
	}
	if err := git.CheckVersion(); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	owner, err := foreignWorktreePath(target)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	if owner != "" {
		if printPath {
			_, err := fmt.Fprintln(cfg.Out, owner)
			return err
		}
		reportWorktreeOwner(cfg, target, owner)
		return ErrInvalidArgs
	}
	current, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("finding current branch: %s", err)
		return ErrNotInStack
	}
	var root string
	if printPath {
		root, err = git.RootDir()
		if err != nil || !filepath.IsAbs(root) {
			cfg.Errorf("could not determine the absolute current worktree path: %v", err)
			return ErrSilent
		}
	}
	if current != target {
		release, err := beginStackMutation(cfg, "checkout")
		if err != nil {
			return err
		}
		defer release()
		// A separate process may have checked out the target while we waited.
		owner, err = foreignWorktreePath(target)
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
		if owner != "" {
			if printPath {
				_, err := fmt.Fprintln(cfg.Out, owner)
				return err
			}
			reportWorktreeOwner(cfg, target, owner)
			return ErrInvalidArgs
		}
		rebasing, err := git.IsRebaseInProgress()
		if err != nil {
			cfg.Errorf("checking rebase state before checkout: %s", err)
			return ErrSilent
		}
		picking, err := git.IsCherryPickInProgress()
		if err != nil {
			cfg.Errorf("checking cherry-pick state before checkout: %s", err)
			return ErrSilent
		}
		if rebasing || picking {
			cfg.Errorf("a Git operation is in progress in the current worktree; complete or abort it before switching branches")
			return ErrRebaseActive
		}
		if err := git.CheckoutBranch(target); err != nil {
			return err
		}
	}
	if printPath {
		_, err := fmt.Fprintln(cfg.Out, root)
		return err
	}
	return nil
}
