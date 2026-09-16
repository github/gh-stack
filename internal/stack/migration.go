package stack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

const (
	migrationFileName     = "gh-stack-migration"
	migrationBackupSuffix = ".pre-worktree-migration"
	migrationVersion      = 1
)

// MigrationConflictError reports catalog definitions or migration artifacts
// that cannot be reconciled without choosing which tracking state to keep.
type MigrationConflictError struct {
	Sources  []string
	Branches []string
	Reason   string
}

func (e *MigrationConflictError) Error() string {
	branches := ""
	if len(e.Branches) > 0 {
		branches = fmt.Sprintf("; branches: %q", e.Branches)
	}
	return fmt.Sprintf("cannot migrate stack catalogs: %s; sources: %q%s; reconcile or recreate the intended tracking state before retrying", e.Reason, e.Sources, branches)
}

// MigrationBlockedError identifies legacy recovery records whose original
// catalogs must remain available to the operation's original worktree.
type MigrationBlockedError struct {
	RecoveryPaths []string
}

func (e *MigrationBlockedError) Error() string {
	return fmt.Sprintf("stack migration is blocked by recovery state at %q; finish or abort rebase/modify in the original worktree before retrying migration", e.RecoveryPaths)
}

type migrationCatalog struct {
	Path string      `json:"path"`
	Data []byte      `json:"data"`
	Mode os.FileMode `json:"mode"`
}

// Original bytes, including the old common catalog, must survive publication
// before any named backup is created. Relative administration paths also keep
// this record usable if the repository itself moves during an interruption.
type migrationState struct {
	Version  int                `json:"version"`
	Catalogs []migrationCatalog `json:"catalogs"`
}

// HasLegacyState reports linked-worktree catalogs or an unfinished migration.
// It inspects every retained administration directory, regardless of whether
// its worktree still exists. It never invokes Git or changes worktree state.
func HasLegacyState(commonDir string) (bool, error) {
	catalogs, err := legacyCatalogs(commonDir)
	if err != nil {
		return false, err
	}
	_, _, pending, err := readMigrationFile(filepath.Join(commonDir, migrationFileName))
	if err != nil {
		return false, err
	}
	return pending || len(catalogs) > 0, nil
}

// MigrateLegacyState consolidates legacy catalogs into the common directory.
// The caller must hold LockOperation; this function takes the catalog lock.
// Only disjoint stacks and equivalent duplicates are merged. Original bytes
// are retained in *.pre-worktree-migration backups after common publication.
// Old and new gh-stack versions must not write catalogs concurrently.
func MigrateLegacyState(commonDir string) error {
	lock, err := Lock(commonDir)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	legacy, err := legacyCatalogs(commonDir)
	if err != nil {
		return err
	}
	journalPath := filepath.Join(commonDir, migrationFileName)
	journal, _, pending, err := readMigrationFile(journalPath)
	if err != nil {
		return err
	}
	if !pending && len(legacy) == 0 {
		return nil
	}

	state := migrationState{Version: migrationVersion}
	if pending {
		state.Version = 0
		if err := decodeMigrationJSON(journal, &state); err != nil {
			return fmt.Errorf("reading migration journal %q: %w", journalPath, err)
		}
		if state.Version != migrationVersion {
			return fmt.Errorf("migration journal %q has unsupported version %d; preserve it and use the matching gh-stack version", journalPath, state.Version)
		}
	} else {
		data, mode, exists, err := readMigrationFile(stackFilePath(commonDir))
		if err != nil {
			return err
		}
		if exists {
			state.Catalogs = append(state.Catalogs, migrationCatalog{Path: stackFileName, Data: data, Mode: mode})
		}
		state.Catalogs = append(state.Catalogs, legacy...)
	}
	if err := validateMigrationPaths(state.Catalogs); err != nil {
		return fmt.Errorf("invalid migration journal %q: %w", journalPath, err)
	}
	if err := checkMigrationRecovery(commonDir, state.Catalogs); err != nil {
		return err
	}
	merged, err := mergeMigrationCatalogs(commonDir, state.Catalogs)
	if err != nil {
		return err
	}
	mergedData, err := marshalStackFile(merged)
	if err != nil {
		return err
	}

	published, err := checkMigrationSnapshot(commonDir, state.Catalogs, legacy, mergedData)
	if err != nil {
		return err
	}
	if !pending {
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding migration journal: %w", err)
		}
		if err := writeFileAtomic(journalPath, data, 0600, false); err != nil {
			return fmt.Errorf("publishing migration journal: %w", err)
		}
	}
	if !published || !pending {
		if err := writeStackFile(commonDir, merged); err != nil {
			return err
		}
	}

	for _, catalog := range state.Catalogs {
		path := filepath.Join(commonDir, filepath.FromSlash(catalog.Path))
		if err := backupMigrationCatalog(path, catalog); err != nil {
			return err
		}
		if catalog.Path == stackFileName {
			continue
		}
		data, _, exists, err := readMigrationFile(path)
		if err != nil {
			return err
		}
		if !exists {
			continue // A prior attempt archived it; its backup was just checked.
		}
		if !bytes.Equal(data, catalog.Data) {
			return migrationConflict("legacy catalog changed during migration", []string{path, journalPath}, nil)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("archiving legacy catalog %q: %w", path, err)
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("syncing archived catalog directory: %w", err)
		}
	}
	if err := os.Remove(journalPath); err != nil {
		return fmt.Errorf("removing completed migration journal: %w", err)
	}
	return syncDirectory(commonDir)
}

func legacyCatalogs(commonDir string) ([]migrationCatalog, error) {
	info, err := os.Stat(commonDir)
	if err != nil {
		return nil, fmt.Errorf("inspecting common directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("common directory %q is not a directory", commonDir)
	}
	dir := filepath.Join(commonDir, "worktrees")
	info, err = os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspecting worktree administration directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("worktree administration path %q is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing worktree administration directories: %w", err)
	}
	var catalogs []migrationCatalog
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspecting worktree administration path %q: %w", path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("worktree administration path %q is not a directory", path)
		}
		data, mode, exists, err := readMigrationFile(filepath.Join(path, stackFileName))
		if err != nil {
			return nil, err
		}
		if exists {
			catalogs = append(catalogs, migrationCatalog{
				Path: filepath.ToSlash(filepath.Join("worktrees", entry.Name(), stackFileName)),
				Data: data,
				Mode: mode,
			})
		}
	}
	return catalogs, nil
}

func readMigrationFile(path string) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("inspecting migration source %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("migration source %q is not a regular file", path)
	}
	data, err := readStateFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("reading migration source %q: %w", path, err)
	}
	return data, info.Mode().Perm(), true, nil
}

func validateMigrationPaths(catalogs []migrationCatalog) error {
	seen := make(map[string]bool)
	hasLegacy := false
	for _, catalog := range catalogs {
		parts := strings.Split(catalog.Path, "/")
		if catalog.Path != stackFileName {
			if len(parts) != 3 || parts[0] != "worktrees" || parts[2] != stackFileName ||
				parts[1] == "" || parts[1] == "." || parts[1] == ".." ||
				filepath.Base(parts[1]) != parts[1] {
				return fmt.Errorf("unsafe catalog path %q", catalog.Path)
			}
			hasLegacy = true
		}
		if !filepath.IsLocal(filepath.FromSlash(catalog.Path)) || seen[catalog.Path] {
			return fmt.Errorf("invalid or duplicate catalog path %q", catalog.Path)
		}
		if catalog.Mode != catalog.Mode.Perm() {
			return fmt.Errorf("invalid catalog permissions for %q", catalog.Path)
		}
		seen[catalog.Path] = true
	}
	if !hasLegacy {
		return errors.New("migration contains no linked-worktree catalogs")
	}
	return nil
}

func checkMigrationRecovery(commonDir string, catalogs []migrationCatalog) error {
	dirs := []string{commonDir}
	for _, catalog := range catalogs {
		if catalog.Path != stackFileName {
			dirs = append(dirs, filepath.Dir(filepath.Join(commonDir, filepath.FromSlash(catalog.Path))))
		}
	}
	var paths []string
	for _, dir := range dirs {
		for _, name := range []string{"gh-stack-rebase-state", "gh-stack-modify-state"} {
			path := filepath.Join(dir, name)
			_, err := os.Lstat(path)
			if err == nil {
				paths = append(paths, path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspecting legacy recovery state %q: %w", path, err)
			}
		}
	}
	if len(paths) > 0 {
		return &MigrationBlockedError{RecoveryPaths: paths}
	}
	return nil
}

func decodeMigrationJSON(data []byte, value any) error {
	if !json.Valid(data) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func parseMigrationCatalog(catalog migrationCatalog) (*StackFile, error) {
	sf, err := parseStackFile(catalog.Data)
	if err != nil {
		return nil, err
	}
	// Refuse data that the normal catalog model would silently discard.
	if err := decodeMigrationJSON(catalog.Data, sf); err != nil {
		return nil, err
	}
	var catalogFields struct {
		SchemaVersion *int               `json:"schemaVersion"`
		Stacks        *[]json.RawMessage `json:"stacks"`
	}
	if err := json.Unmarshal(catalog.Data, &catalogFields); err != nil {
		return nil, err
	}
	if catalogFields.SchemaVersion == nil || *catalogFields.SchemaVersion < 0 || catalogFields.Stacks == nil {
		return nil, errors.New("catalog must contain schemaVersion and a stacks array")
	}
	for i, s := range sf.Stacks {
		var stackFields map[string]json.RawMessage
		if err := json.Unmarshal((*catalogFields.Stacks)[i], &stackFields); err != nil {
			return nil, err
		}
		if s.Trunk.Branch == "" || stackFields["branches"] == nil {
			return nil, errors.New("each stack must contain a named trunk and branches")
		}
		owned := map[string]bool{s.Trunk.Branch: true}
		for _, branch := range s.Branches {
			if branch.Branch == "" {
				return nil, errors.New("stack contains an unnamed branch")
			}
			if owned[branch.Branch] {
				return nil, &MigrationConflictError{Branches: []string{branch.Branch}, Reason: "branch is repeated within one stack"}
			}
			owned[branch.Branch] = true
		}
	}
	return sf, nil
}

func mergeMigrationCatalogs(commonDir string, catalogs []migrationCatalog) (*StackFile, error) {
	merged := &StackFile{SchemaVersion: schemaVersion, Stacks: []Stack{}}
	var sources []string
	repositorySource := ""
	for _, catalog := range catalogs {
		path := filepath.Join(commonDir, filepath.FromSlash(catalog.Path))
		sf, err := parseMigrationCatalog(catalog)
		if err != nil {
			var conflict *MigrationConflictError
			if errors.As(err, &conflict) {
				conflict.Sources = []string{path}
			}
			return nil, fmt.Errorf("parsing migration catalog %q: %w", path, err)
		}
		if sf.Repository != "" {
			if merged.Repository != "" && merged.Repository != sf.Repository {
				return nil, migrationConflict("repository identities differ", []string{repositorySource, path}, nil)
			}
			merged.Repository = sf.Repository
			repositorySource = path
		}
		for _, s := range sf.Stacks {
			duplicate := false
			for i, other := range merged.Stacks {
				if equivalentMigrationStacks(s, other) {
					duplicate = true
					break
				}
				var shared []string
				for _, branch := range s.Branches {
					if other.IndexOf(branch.Branch) >= 0 {
						shared = append(shared, branch.Branch)
					}
				}
				sameIdentity := (s.ID != "" && s.ID == other.ID) || (s.Number != 0 && s.Number == other.Number)
				if sameIdentity {
					branches := append(s.BranchNames(), other.BranchNames()...)
					return nil, migrationConflict("stack identity has differing definitions", []string{sources[i], path}, branches)
				}
				if len(shared) > 0 {
					return nil, migrationConflict("non-trunk branches belong to differing stack definitions", []string{sources[i], path}, shared)
				}
			}
			if !duplicate {
				merged.Stacks = append(merged.Stacks, s)
				sources = append(sources, path)
			}
		}
	}
	return merged, nil
}

func equivalentMigrationStacks(a, b Stack) bool {
	if len(a.Branches) == 0 {
		a.Branches = nil
	}
	if len(b.Branches) == 0 {
		b.Branches = nil
	}
	return reflect.DeepEqual(a, b)
}

func migrationConflict(reason string, sources, branches []string) error {
	slices.Sort(branches)
	branches = slices.Compact(branches)
	return &MigrationConflictError{Sources: sources, Branches: branches, Reason: reason}
}

func checkMigrationSnapshot(commonDir string, catalogs, legacy []migrationCatalog, mergedData []byte) (bool, error) {
	expected := make(map[string]migrationCatalog, len(catalogs))
	for _, catalog := range catalogs {
		expected[catalog.Path] = catalog
	}
	for _, catalog := range legacy {
		if _, ok := expected[catalog.Path]; !ok {
			return false, migrationConflict("a new legacy catalog appeared during migration", []string{filepath.Join(commonDir, filepath.FromSlash(catalog.Path)), filepath.Join(commonDir, migrationFileName)}, nil)
		}
	}

	commonPath := stackFilePath(commonDir)
	current, _, exists, err := readMigrationFile(commonPath)
	if err != nil {
		return false, err
	}
	published := exists && bytes.Equal(current, mergedData)
	original, hadCommon := expected[stackFileName]
	if !published && (exists != hadCommon || !bytes.Equal(current, original.Data)) {
		return false, migrationConflict("common catalog changed during migration", []string{commonPath, filepath.Join(commonDir, migrationFileName)}, nil)
	}

	for _, catalog := range catalogs {
		path := filepath.Join(commonDir, filepath.FromSlash(catalog.Path))
		backup, _, backedUp, err := readMigrationFile(path + migrationBackupSuffix)
		if err != nil {
			return false, err
		}
		if backedUp && !bytes.Equal(backup, catalog.Data) {
			return false, migrationConflict("existing backup differs from the original catalog", []string{path, path + migrationBackupSuffix}, nil)
		}
		if catalog.Path == stackFileName {
			continue
		}
		data, _, exists, err := readMigrationFile(path)
		if err != nil {
			return false, err
		}
		if exists && !bytes.Equal(data, catalog.Data) {
			return false, migrationConflict("legacy catalog changed during migration", []string{path, filepath.Join(commonDir, migrationFileName)}, nil)
		}
		if !exists && (!published || !backedUp) {
			return false, migrationConflict("legacy catalog disappeared without a published common catalog and matching backup", []string{path, path + migrationBackupSuffix}, nil)
		}
	}
	return published, nil
}

func backupMigrationCatalog(path string, catalog migrationCatalog) error {
	backupPath := path + migrationBackupSuffix
	data, _, exists, err := readMigrationFile(backupPath)
	if err != nil {
		return err
	}
	if exists {
		if !bytes.Equal(data, catalog.Data) {
			return migrationConflict("existing backup differs from the original catalog", []string{path, backupPath}, nil)
		}
		return nil
	}
	if err := writeFileAtomic(backupPath, catalog.Data, catalog.Mode, false); err != nil {
		return fmt.Errorf("preserving original catalog %q: %w", backupPath, err)
	}
	return nil
}
