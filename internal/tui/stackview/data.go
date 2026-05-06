package stackview

import (
	"sync"

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

// maxGitConcurrency limits the number of concurrent git subprocess spawns.
const maxGitConcurrency = 4

// LoadBranchNodes populates branch display data from a stack.
// prDetails, when non-nil, provides pre-fetched PR data from syncStackPRs,
// avoiding redundant GitHub API calls.
func LoadBranchNodes(cfg *config.Config, s *stack.Stack, currentBranch string, prDetails map[string]*ghapi.PRDetails) []BranchNode {
	// Fall back to API-based PR fetching if no pre-fetched details are available.
	var client ghapi.ClientOps
	var clientErr error
	if prDetails == nil {
		client, clientErr = cfg.GitHubClient()
	}

	// Pre-compute base branches (reads s.Branches, so must happen before goroutines).
	type branchInput struct {
		ref        stack.BranchRef
		baseBranch string
		isCurrent  bool
	}
	inputs := make([]branchInput, len(s.Branches))
	for i, b := range s.Branches {
		inputs[i] = branchInput{
			ref:        b,
			baseBranch: s.ActiveBaseBranch(b.Branch),
			isCurrent:  b.Branch == currentBranch,
		}
	}

	nodes := make([]BranchNode, len(s.Branches))

	// Load branch data concurrently using a WaitGroup for completion and a
	// semaphore channel to cap the number of in-flight git subprocesses
	// (see maxGitConcurrency).
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxGitConcurrency)

	for i, inp := range inputs {
		wg.Add(1)
		go func(idx int, inp branchInput) {
			defer wg.Done()

			// Acquire a semaphore slot to limit concurrent git calls.
			sem <- struct{}{}
			defer func() { <-sem }()

			node := BranchNode{
				Ref:        inp.ref,
				IsCurrent:  inp.isCurrent,
				BaseBranch: inp.baseBranch,
				IsLinear:   true,
			}

			// Check linearity (is base an ancestor of this branch?)
			if isAncestor, err := git.IsAncestor(inp.baseBranch, inp.ref.Branch); err == nil {
				node.IsLinear = isAncestor
			}

			// Use the merge-base (fork point) as the diff anchor so that we
			// only show changes introduced on this branch. Without this, a
			// diverged base (e.g. local main ahead of the branch's fork point)
			// would inflate the diff with unrelated files.
			diffBase := inp.baseBranch
			if mb, err := git.MergeBase(inp.baseBranch, inp.ref.Branch); err == nil {
				diffBase = mb
			}

			// Fetch commit range
			if commits, err := git.LogRange(diffBase, inp.ref.Branch); err == nil {
				node.Commits = commits
			}

			// Compute per-file diff stats from local git
			if files, err := git.DiffStatFiles(diffBase, inp.ref.Branch); err == nil {
				node.FilesChanged = files
				for _, f := range files {
					node.Additions += f.Additions
					node.Deletions += f.Deletions
				}
			}

			// Use pre-fetched PR details if available, otherwise fall back to API.
			if prDetails != nil {
				if details, ok := prDetails[inp.ref.Branch]; ok {
					tracked := inp.ref.PullRequest != nil && inp.ref.PullRequest.Number == details.Number
					if tracked || details.State == "OPEN" {
						node.PR = details
					}
				}
			} else if clientErr == nil {
				if pr, err := client.FindPRDetailsForBranch(inp.ref.Branch); err == nil && pr != nil {
					tracked := inp.ref.PullRequest != nil && inp.ref.PullRequest.Number == pr.Number
					if tracked || pr.State == "OPEN" {
						node.PR = pr
					}
				}
			}

			// Each goroutine writes to its own index — no lock needed.
			nodes[idx] = node
		}(i, inp)
	}
	wg.Wait()

	return nodes
}
