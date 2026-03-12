package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// For stacked branches, Base stores the merge-base commit SHA
// (the last common commit before divergence from the parent branch).
type BranchRef struct {
	Branch      string          `json:"branch"`
	Head        string          `json:"head,omitempty"`
	Base        string          `json:"base,omitempty"`
	PullRequest *PullRequestRef `json:"pullRequest,omitempty"`
}

// Stack represents a single stack of branches.
type Stack struct {
	ID       string      `json:"id,omitempty"`
	Trunk    BranchRef   `json:"trunk"`
	Branches []BranchRef `json:"branches"`
}

// DisplayName returns a human-readable chain representation of the stack.
// Format: (trunk) <- branch1 <- branch2 <- branch3
func (s *Stack) DisplayName() string {
	result := "(" + s.Trunk.Branch + ")"
	for _, b := range s.Branches {
		result += " <- " + b.Branch
	}
	return result
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

// ActiveBranches returns only non-merged branches, preserving order.
func (s *Stack) ActiveBranches() []BranchRef {
	var active []BranchRef
	for _, b := range s.Branches {
		if !b.IsMerged() {
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

// FirstActiveBranchIndex returns the index of the first non-merged branch, or -1.
func (s *Stack) FirstActiveBranchIndex() int {
	for i, b := range s.Branches {
		if !b.IsMerged() {
			return i
		}
	}
	return -1
}

// ActiveBaseBranch returns the effective parent for a branch, skipping merged
// ancestors. For the first active branch (or any branch whose downstack is all
// merged), this returns the trunk.
func (s *Stack) ActiveBaseBranch(branch string) string {
	idx := s.IndexOf(branch)
	if idx <= 0 {
		return s.Trunk.Branch
	}
	for j := idx - 1; j >= 0; j-- {
		if !s.Branches[j].IsMerged() {
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
func Load(gitDir string) (*StackFile, error) {
	path := stackFilePath(gitDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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

	return &sf, nil
}

// Save writes the stack file to the given git directory.
func Save(gitDir string, sf *StackFile) error {
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
	return nil
}
