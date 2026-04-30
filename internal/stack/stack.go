package stack

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	schemaVersion = 1
	stackFileName = "gh-stack"
)

// PullRequestRef holds relatively immutable metadata about an associated PR.
type PullRequestRef struct {
	Number int    `json:"number"`
	ID     string `json:"id,omitempty"`
	URL    string `json:"url,omitempty"`
	Merged bool   `json:"merged,omitempty"`
}

// BranchRef represents a branch and its associated commit hash.
// For the trunk, Head stores the HEAD commit SHA.
// For stacked branches, Base stores the parent branch's HEAD SHA
// at the time of last sync/rebase, used to identify unique commits.
type BranchRef struct {
	Branch      string          `json:"branch"`
	Head        string          `json:"head,omitempty"`
	Base        string          `json:"base,omitempty"`
	PullRequest *PullRequestRef `json:"pullRequest,omitempty"`

	// Queued is a transient (not persisted) flag indicating the branch's
	// PR is currently in a merge queue. It is populated by syncStackPRs
	// from the GitHub API on each command run.
	Queued bool `json:"-"`
}

// Stack represents a single stack of branches.
type Stack struct {
	ID       string      `json:"id,omitempty"`
	Prefix   string      `json:"prefix,omitempty"`
	Numbered bool        `json:"numbered,omitempty"`
	Trunk    BranchRef   `json:"trunk"`
	Branches []BranchRef `json:"branches"`
}

// DisplayChain returns a human-readable chain representation of the stack.
// Format: (trunk) <- branch1 <- branch2 <- branch3
func (s *Stack) DisplayChain() string {
	parts := []string{"(" + s.Trunk.Branch + ")"}
	for _, b := range s.Branches {
		parts = append(parts, b.Branch)
	}
	return strings.Join(parts, " <- ")
}

// BranchNames returns the list of branch names in order.
func (s *Stack) BranchNames() []string {
	names := make([]string, len(s.Branches))
	for i, b := range s.Branches {
		names[i] = b.Branch
	}
	return names
}

// IndexOf returns the index of the given branch in the stack, or -1 if not found.
func (s *Stack) IndexOf(branch string) int {
	for i, b := range s.Branches {
		if b.Branch == branch {
			return i
		}
	}
	return -1
}

// Contains returns true if the branch is part of this stack (including trunk).
func (s *Stack) Contains(branch string) bool {
	if s.Trunk.Branch == branch {
		return true
	}
	return s.IndexOf(branch) >= 0
}

// BaseBranch returns the base branch for the given branch in the stack.
// For the first branch, this is the trunk. For others, it's the previous branch.
func (s *Stack) BaseBranch(branch string) string {
	idx := s.IndexOf(branch)
	if idx <= 0 {
		return s.Trunk.Branch
	}
	return s.Branches[idx-1].Branch
}

// IsMerged returns whether a branch's PR has been merged.
func (b *BranchRef) IsMerged() bool {
	return b.PullRequest != nil && b.PullRequest.Merged
}

// IsQueued returns whether a branch's PR is currently in a merge queue.
// This is a transient state populated from the GitHub API on each run.
func (b *BranchRef) IsQueued() bool {
	return b.Queued
}

// IsSkipped returns whether a branch should be skipped during push/sync/submit.
// A branch is skipped if its PR has been merged or is currently queued.
func (b *BranchRef) IsSkipped() bool {
	return b.IsMerged() || b.IsQueued()
}

// ActiveBranches returns only branches that are pushable (not merged, not queued).
func (s *Stack) ActiveBranches() []BranchRef {
	var active []BranchRef
	for _, b := range s.Branches {
		if !b.IsSkipped() {
			active = append(active, b)
		}
	}
	return active
}

// MergedBranches returns only merged branches, preserving order.
func (s *Stack) MergedBranches() []BranchRef {
	var merged []BranchRef
	for _, b := range s.Branches {
		if b.IsMerged() {
			merged = append(merged, b)
		}
	}
	return merged
}

// QueuedBranches returns only queued branches, preserving order.
func (s *Stack) QueuedBranches() []BranchRef {
	var queued []BranchRef
	for _, b := range s.Branches {
		if b.IsQueued() {
			queued = append(queued, b)
		}
	}
	return queued
}

// FirstActiveBranchIndex returns the index of the first active (not merged, not queued) branch, or -1.
func (s *Stack) FirstActiveBranchIndex() int {
	for i, b := range s.Branches {
		if !b.IsSkipped() {
			return i
		}
	}
	return -1
}

// ActiveBranchIndices returns the indices of all active (not merged, not queued) branches.
func (s *Stack) ActiveBranchIndices() []int {
	var indices []int
	for i, b := range s.Branches {
		if !b.IsSkipped() {
			indices = append(indices, i)
		}
	}
	return indices
}

// ActiveBaseBranch returns the effective parent for a branch, skipping merged
// and queued ancestors. For the first active branch (or any branch whose
// downstack is all merged/queued), this returns the trunk.
func (s *Stack) ActiveBaseBranch(branch string) string {
	idx := s.IndexOf(branch)
	if idx <= 0 {
		return s.Trunk.Branch
	}
	for j := idx - 1; j >= 0; j-- {
		if !s.Branches[j].IsSkipped() {
			return s.Branches[j].Branch
		}
	}
	return s.Trunk.Branch
}

// IsFullyMerged returns true if all branches in the stack have been merged.
func (s *Stack) IsFullyMerged() bool {
	for _, b := range s.Branches {
		if !b.IsMerged() {
			return false
		}
	}
	return len(s.Branches) > 0
}

// StackFile represents the JSON file stored in .git/gh-stack.
type StackFile struct {
	SchemaVersion int     `json:"schemaVersion"`
	Repository    string  `json:"repository"`
	Stacks        []Stack `json:"stacks"`

	// loadChecksum is the SHA-256 of the raw file bytes at Load time.
	// Save uses it to detect concurrent modifications (optimistic concurrency).
	// nil means the file did not exist when loaded.
	loadChecksum []byte
}

// FindAllStacksForBranch returns all stacks that contain the given branch.
func (sf *StackFile) FindAllStacksForBranch(branch string) []*Stack {
	var stacks []*Stack
	for i := range sf.Stacks {
		if sf.Stacks[i].Contains(branch) {
			stacks = append(stacks, &sf.Stacks[i])
		}
	}
	return stacks
}

// FindStackByPRNumber returns the first stack and branch whose PR number matches.
// Returns nil, nil if no match is found.
func (sf *StackFile) FindStackByPRNumber(prNumber int) (*Stack, *BranchRef) {
	for i := range sf.Stacks {
		for j := range sf.Stacks[i].Branches {
			b := &sf.Stacks[i].Branches[j]
			if b.PullRequest != nil && b.PullRequest.Number == prNumber {
				return &sf.Stacks[i], b
			}
		}
	}
	return nil, nil
}

// ValidateNoDuplicateBranch checks that the branch is not already in any stack.
func (sf *StackFile) ValidateNoDuplicateBranch(branch string) error {
	for _, s := range sf.Stacks {
		if s.Contains(branch) {
			return fmt.Errorf("branch %q is already part of a stack", branch)
		}
	}
	return nil
}

// AddStack adds a new stack to the file.
func (sf *StackFile) AddStack(s Stack) {
	sf.Stacks = append(sf.Stacks, s)
}

// RemoveStack removes the stack at the given index.
func (sf *StackFile) RemoveStack(idx int) {
	sf.Stacks = append(sf.Stacks[:idx], sf.Stacks[idx+1:]...)
}

// RemoveStackForBranch removes the stack containing the given branch.
func (sf *StackFile) RemoveStackForBranch(branch string) bool {
	for i := range sf.Stacks {
		if sf.Stacks[i].Contains(branch) {
			sf.RemoveStack(i)
			return true
		}
	}
	return false
}

// stackFilePath returns the path to the gh-stack file.
func stackFilePath(gitDir string) string {
	return filepath.Join(gitDir, stackFileName)
}

// Load reads the stack file from the given git directory.
// Returns an empty StackFile if the file does not exist.
// The returned StackFile records a checksum of the on-disk content so that
// Save can detect concurrent modifications.
func Load(gitDir string) (*StackFile, error) {
	path := stackFilePath(gitDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// loadChecksum stays nil — sentinel for "file absent at load time".
			return &StackFile{
				SchemaVersion: schemaVersion,
				Stacks:        []Stack{},
			}, nil
		}
		return nil, fmt.Errorf("reading stack file: %w", err)
	}

	var sf StackFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parsing stack file: %w", err)
	}

	if sf.SchemaVersion > schemaVersion {
		return nil, fmt.Errorf("stack file has schema version %d, but this version of gh-stack only supports up to version %d — please upgrade gh-stack", sf.SchemaVersion, schemaVersion)
	}

	sum := sha256.Sum256(data)
	sf.loadChecksum = sum[:]
	return &sf, nil
}

// Save acquires an exclusive lock on the stack file, verifies the file hasn't
// been modified since Load (optimistic concurrency), writes sf as JSON, and
// releases the lock.  The lock is held only for the read-compare-write window.
// Returns *LockError if the lock times out, or *StaleError if another process
// modified the file since it was loaded.
func Save(gitDir string, sf *StackFile) error {
	lock, err := Lock(gitDir)
	if err != nil {
		return err // *LockError for contention, plain error for I/O failures
	}
	defer lock.Unlock()

	if err := checkStale(gitDir, sf); err != nil {
		return err
	}
	return writeStackFile(gitDir, sf)
}

// SaveWithLock writes the stack file while the caller already holds the lock.
// This avoids double-locking when the caller needs to hold the lock across
// multiple operations (e.g. modify apply).  The staleness check is skipped
// because the caller owns the lock and has been mutating the StackFile.
func SaveWithLock(gitDir string, sf *StackFile, _ *FileLock) error {
	return writeStackFile(gitDir, sf)
}

// SaveNonBlocking attempts to save without blocking.  If another process holds
// the lock or the file was modified since Load, the save is silently skipped.
// Use this for best-effort metadata persistence (e.g. syncing PR state in view).
func SaveNonBlocking(gitDir string, sf *StackFile) {
	path := filepath.Join(gitDir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return
	}
	if tryLockFile(f) != nil {
		f.Close()
		return
	}
	lock := &FileLock{f: f}
	defer lock.Unlock()

	if checkStale(gitDir, sf) != nil {
		return
	}
	_ = writeStackFile(gitDir, sf)
}

// checkStale compares the current on-disk content against the checksum
// captured at Load time.  Returns *StaleError if the file was modified
// by another process.  The caller must hold the lock.
func checkStale(gitDir string, sf *StackFile) error {
	path := stackFilePath(gitDir)
	data, err := os.ReadFile(path)

	if errors.Is(err, os.ErrNotExist) {
		// File absent on disk.
		if sf.loadChecksum == nil {
			return nil // was absent at Load time too — no conflict
		}
		// File existed at Load but is now gone.  Allow the write to
		// recreate it rather than erroring; this is not a lost-update.
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading stack file for staleness check: %w", err)
	}

	// File exists on disk.
	if sf.loadChecksum == nil {
		// File was absent at Load but another process created it.
		return &StaleError{Err: fmt.Errorf(
			"stack file was created by another process since it was loaded")}
	}

	sum := sha256.Sum256(data)
	if !bytes.Equal(sf.loadChecksum, sum[:]) {
		return &StaleError{Err: fmt.Errorf(
			"stack file was modified by another process since it was loaded")}
	}
	return nil
}

func writeStackFile(gitDir string, sf *StackFile) error {
	sf.SchemaVersion = schemaVersion
	if sf.Stacks == nil {
		sf.Stacks = []Stack{}
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling stack file: %w", err)
	}
	path := stackFilePath(gitDir)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing stack file: %w", err)
	}
	// Refresh checksum so a second Save on the same StackFile doesn't
	// spuriously fail the staleness check.
	sum := sha256.Sum256(data)
	sf.loadChecksum = sum[:]
	return nil
}
