package stackview

import (
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
)

// BranchNode holds all display data for a single branch in the stack.
type BranchNode struct {
	Ref          stack.BranchRef
	IsCurrent    bool
	IsLinear     bool // whether history is linear with base branch
	BaseBranch   string
	Commits      []git.CommitInfo   // commits unique to this branch (base..head)
	FilesChanged []git.FileDiffStat // per-file diff stats
	PR           *ghapi.PRDetails
	Additions    int
	Deletions    int

	// UI state
	CommitsExpanded bool
	FilesExpanded   bool
}

// LoadBranchNodes populates branch display data from a stack.
func LoadBranchNodes(cfg *config.Config, s *stack.Stack, currentBranch string) []BranchNode {
	client, clientErr := cfg.GitHubClient()

	nodes := make([]BranchNode, len(s.Branches))

	for i, b := range s.Branches {
		baseBranch := s.ActiveBaseBranch(b.Branch)

		node := BranchNode{
			Ref:        b,
			IsCurrent:  b.Branch == currentBranch,
			BaseBranch: baseBranch,
			IsLinear:   true,
		}

		// Check linearity (is base an ancestor of this branch?)
		if isAncestor, err := git.IsAncestor(baseBranch, b.Branch); err == nil {
			node.IsLinear = isAncestor
		}

		// Use the merge-base (fork point) as the diff anchor so that we
		// only show changes introduced on this branch. Without this, a
		// diverged base (e.g. local main ahead of the branch's fork point)
		// would inflate the diff with unrelated files.
		diffBase := baseBranch
		if mb, err := git.MergeBase(baseBranch, b.Branch); err == nil {
			diffBase = mb
		}

		// Fetch commit range
		if commits, err := git.LogRange(diffBase, b.Branch); err == nil {
			node.Commits = commits
		}

		// Compute per-file diff stats from local git
		if files, err := git.DiffStatFiles(diffBase, b.Branch); err == nil {
			node.FilesChanged = files
			for _, f := range files {
				node.Additions += f.Additions
				node.Deletions += f.Deletions
			}
		}

		// Fetch enriched PR details
		if clientErr == nil {
			if pr, err := client.FindPRDetailsForBranch(b.Branch); err == nil && pr != nil {
				node.PR = pr
			}
		}

		nodes[i] = node
	}

	return nodes
}
