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

// PrefillTitle returns the default PR title for a new branch: the subject of its
// single commit when the branch has exactly one commit, otherwise the humanized
// branch name. This mirrors the non-TUI submit's defaultPRTitleBody.
func PrefillTitle(node stackview.BranchNode) string {
	if commits := node.Commits; len(commits) == 1 {
		if subject := strings.TrimSpace(commits[0].Subject); subject != "" {
			return subject
		}
	}
	return humanize(node.Ref.Branch)
}

// PrefillDescription returns the default PR description for a new branch: the
// repo PR template if one exists, otherwise the body of the branch's single
// commit, otherwise empty. Multi-commit branches with no template get an empty
// description (no bulleted commit list). This mirrors the non-TUI submit. The
// attribution footer is appended later, at submit time.
func PrefillDescription(node stackview.BranchNode, template string) string {
	if t := strings.TrimSpace(template); t != "" {
		return t
	}
	if commits := node.Commits; len(commits) == 1 {
		return strings.TrimSpace(commits[0].Body)
	}
	return ""
}

// NewSubmitNodes builds the per-branch UI state for the submit TUI from loaded
// branch display data. NEW branches default to included; new PRs have their
// title and description prefilled from commits and the PR template, while
// existing PRs show their real title and description fetched from the API. New
// PRs default to ready for review; the per-PR draft toggle starts off. The
// prefill snapshots are retained for edit detection.
func NewSubmitNodes(nodes []stackview.BranchNode, template string) []SubmitNode {
	out := make([]SubmitNode, len(nodes))
	for i, n := range nodes {
		state := DeriveState(n)
		title := PrefillTitle(n)
		desc := PrefillDescription(n, template)
		// Existing PRs render their real title/description from the API instead
		// of the commit/template-derived draft used for new PRs.
		if state != StateNew && n.PR != nil {
			desc = n.PR.Body
			if t := strings.TrimSpace(n.PR.Title); t != "" {
				title = t
			}
		}
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
// the stack and triggers the closed-branch callout.
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
