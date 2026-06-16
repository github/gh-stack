package submitview

import (
	"strings"

	"github.com/github/gh-stack/internal/tui/stackview"
)

// DeriveState classifies a branch node into a BranchState using both the stack's
// tracked PR reference and any freshly fetched PR details. Merged and queued
// states take priority, followed by closed, draft, and open. A branch with no
// PR at all is NEW. A branch that tracks a PR but for which no fresh details are
// available is treated as open (locked) rather than NEW, so it is never
// presented as editable.
func DeriveState(node stackview.BranchNode) BranchState {
	pr := node.PR
	ref := node.Ref

	if ref.IsMerged() || (pr != nil && pr.Merged) {
		return StateMerged
	}
	if ref.IsQueued() || (pr != nil && pr.IsQueued) {
		return StateQueued
	}
	if pr != nil {
		switch {
		case pr.State == "CLOSED":
			return StateClosed
		case pr.IsDraft:
			return StateDraft
		default:
			return StateOpen
		}
	}
	// A tracked PR reference without fresh details: treat as open (locked),
	// never NEW, so we don't offer to create a duplicate PR.
	if ref.PullRequest != nil && ref.PullRequest.Number != 0 {
		return StateOpen
	}
	return StateNew
}

// PrefillTitle returns the default PR title for a node: the subject of the
// branch's first (oldest) commit when it has any commits, otherwise the
// humanized branch name. git.LogRange returns commits newest-first, so the
// oldest commit — the one that established the branch — is the last element.
func PrefillTitle(node stackview.BranchNode) string {
	if n := len(node.Commits); n > 0 {
		if subject := strings.TrimSpace(node.Commits[n-1].Subject); subject != "" {
			return subject
		}
	}
	return humanize(node.Ref.Branch)
}

// PrefillDescription returns the default PR description following the spec's
// priority order: the repo PR template if one exists, otherwise the single
// commit body, otherwise a bulleted list of commit subjects for multi-commit
// branches. The attribution footer is appended later, at submit time.
func PrefillDescription(node stackview.BranchNode, template string) string {
	if t := strings.TrimSpace(template); t != "" {
		return t
	}

	commits := node.Commits
	switch {
	case len(commits) == 1:
		return strings.TrimSpace(commits[0].Body)
	case len(commits) > 1:
		var b strings.Builder
		// List oldest commit first so the body reads like a changelog.
		for i := len(commits) - 1; i >= 0; i-- {
			subject := strings.TrimSpace(commits[i].Subject)
			if subject == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(subject)
			b.WriteString("\n")
		}
		return strings.TrimSpace(b.String())
	default:
		return ""
	}
}

// NewSubmitNodes builds the per-branch UI state for the submit TUI from loaded
// branch display data. NEW branches default to included; every node's title and
// description are prefilled. New PRs default to ready for review; the per-PR
// draft toggle starts off. The prefill snapshots are retained for edit
// detection.
func NewSubmitNodes(nodes []stackview.BranchNode, template string) []SubmitNode {
	out := make([]SubmitNode, len(nodes))
	for i, n := range nodes {
		state := DeriveState(n)
		title := PrefillTitle(n)
		desc := PrefillDescription(n, template)
		out[i] = SubmitNode{
			BranchNode:   n,
			State:        state,
			Included:     state == StateNew,
			Title:        title,
			Description:  desc,
			titlePrefill: title,
			descPrefill:  desc,
		}
	}
	return out
}

// CountNew returns the number of NEW (creatable) branches in the list.
func CountNew(nodes []SubmitNode) int {
	n := 0
	for _, node := range nodes {
		if node.State == StateNew {
			n++
		}
	}
	return n
}

// CountSelected returns the number of NEW branches currently marked for
// inclusion.
func CountSelected(nodes []SubmitNode) int {
	n := 0
	for _, node := range nodes {
		if node.State == StateNew && node.Included {
			n++
		}
	}
	return n
}

// HasClosed reports whether any branch in the list has a closed PR, which blocks
// the stack and triggers the Step 1 callout.
func HasClosed(nodes []SubmitNode) bool {
	for _, node := range nodes {
		if node.State == StateClosed {
			return true
		}
	}
	return false
}

// ClosedBranches returns the names of branches with a closed PR, in list order.
func ClosedBranches(nodes []SubmitNode) []string {
	var names []string
	for _, node := range nodes {
		if node.State == StateClosed {
			names = append(names, node.Ref.Branch)
		}
	}
	return names
}

// CommonPrefix returns the longest shared slash-delimited path prefix across the
// given branch names, including a trailing slash. It returns "" when fewer than
// two names are given or there is no shared prefix. This is used to render the
// stack map with short branch names.
func CommonPrefix(names []string) string {
	if len(names) < 2 {
		return ""
	}

	// Split each name into slash-delimited segments and find the longest run of
	// leading segments common to all names.
	segs := make([][]string, len(names))
	minLen := -1
	for i, n := range names {
		segs[i] = strings.Split(n, "/")
		// The last segment is the leaf name; only path segments before it can
		// be part of a shared prefix.
		pathLen := len(segs[i]) - 1
		if minLen == -1 || pathLen < minLen {
			minLen = pathLen
		}
	}
	if minLen <= 0 {
		return ""
	}

	common := 0
	for i := 0; i < minLen; i++ {
		seg := segs[0][i]
		same := true
		for j := 1; j < len(segs); j++ {
			if segs[j][i] != seg {
				same = false
				break
			}
		}
		if !same {
			break
		}
		common++
	}
	if common == 0 {
		return ""
	}
	return strings.Join(segs[0][:common], "/") + "/"
}

// Shortname strips prefix from branch when present, returning the remainder. If
// branch does not start with prefix (or prefix is empty), branch is returned
// unchanged.
func Shortname(branch, prefix string) string {
	if prefix == "" {
		return branch
	}
	return strings.TrimPrefix(branch, prefix)
}

// humanize replaces hyphens and underscores with spaces. It mirrors the helper
// used by the submit command so auto-generated titles match across paths.
func humanize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, s)
}
