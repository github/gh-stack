package submitview

import (
	"testing"

	"github.com/github/gh-stack/internal/git"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/stackview"
	"github.com/stretchr/testify/assert"
)

// node builds a stackview.BranchNode for a branch with no PR and the given
// commits.
func node(branch string, commits ...git.CommitInfo) stackview.BranchNode {
	return stackview.BranchNode{
		Ref:     stack.BranchRef{Branch: branch},
		Commits: commits,
	}
}

// withPR attaches fresh PR details to a node.
func withPR(n stackview.BranchNode, pr *ghapi.PRDetails) stackview.BranchNode {
	n.PR = pr
	return n
}

// withTrackedPR attaches a tracked PR reference (as persisted in the stack file)
// to a node.
func withTrackedPR(n stackview.BranchNode, ref *stack.PullRequestRef) stackview.BranchNode {
	n.Ref.PullRequest = ref
	return n
}

func commit(subject, body string) git.CommitInfo {
	return git.CommitInfo{Subject: subject, Body: body}
}

func TestDeriveState(t *testing.T) {
	tests := []struct {
		name string
		node stackview.BranchNode
		want BranchState
	}{
		{
			name: "no PR is new",
			node: node("feat/a"),
			want: StateNew,
		},
		{
			name: "open PR",
			node: withPR(node("feat/a"), &ghapi.PRDetails{Number: 1, State: "OPEN"}),
			want: StateOpen,
		},
		{
			name: "draft PR",
			node: withPR(node("feat/a"), &ghapi.PRDetails{Number: 1, State: "OPEN", IsDraft: true}),
			want: StateDraft,
		},
		{
			name: "closed PR",
			node: withPR(node("feat/a"), &ghapi.PRDetails{Number: 1, State: "CLOSED"}),
			want: StateClosed,
		},
		{
			name: "merged via PR details",
			node: withPR(node("feat/a"), &ghapi.PRDetails{Number: 1, State: "MERGED", Merged: true}),
			want: StateMerged,
		},
		{
			name: "queued via PR details",
			node: withPR(node("feat/a"), &ghapi.PRDetails{Number: 1, State: "OPEN", IsQueued: true}),
			want: StateQueued,
		},
		{
			name: "merged via tracked ref",
			node: withTrackedPR(node("feat/a"), &stack.PullRequestRef{Number: 1, Merged: true}),
			want: StateMerged,
		},
		{
			name: "tracked ref without details treated as open",
			node: withTrackedPR(node("feat/a"), &stack.PullRequestRef{Number: 7}),
			want: StateOpen,
		},
		{
			name: "merged takes priority over draft",
			node: withPR(node("feat/a"), &ghapi.PRDetails{Number: 1, State: "OPEN", IsDraft: true, Merged: true}),
			want: StateMerged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveState(tt.node))
		})
	}
}

func TestPrefillTitle(t *testing.T) {
	t.Run("single commit uses subject", func(t *testing.T) {
		n := node("feat/auth/middleware", commit("Add auth middleware", "body"))
		assert.Equal(t, "Add auth middleware", PrefillTitle(n))
	})

	t.Run("multiple commits use the first (oldest) commit subject", func(t *testing.T) {
		// git.LogRange returns commits newest-first, so the last element is the
		// oldest — the commit that established the branch.
		n := node("feat/auth-middleware", commit("Polish middleware", ""), commit("Add auth middleware", ""))
		assert.Equal(t, "Add auth middleware", PrefillTitle(n))
	})

	t.Run("zero commits humanize branch name", func(t *testing.T) {
		n := node("feat/new_feature")
		assert.Equal(t, "feat/new feature", PrefillTitle(n))
	})

	t.Run("blank subject falls back to branch name", func(t *testing.T) {
		n := node("feat/x", commit("   ", ""))
		assert.Equal(t, "feat/x", PrefillTitle(n))
	})
}

func TestPrefillDescription(t *testing.T) {
	t.Run("template takes priority", func(t *testing.T) {
		n := node("feat/a", commit("subject", "commit body"))
		assert.Equal(t, "## Description", PrefillDescription(n, "## Description"))
	})

	t.Run("single commit uses body", func(t *testing.T) {
		n := node("feat/a", commit("subject", "Detailed body\nsecond line"))
		assert.Equal(t, "Detailed body\nsecond line", PrefillDescription(n, ""))
	})

	t.Run("multi commit lists subjects oldest first", func(t *testing.T) {
		// LogRange returns newest first; the body should read oldest first.
		n := node("feat/a", commit("newest", ""), commit("middle", ""), commit("oldest", ""))
		got := PrefillDescription(n, "")
		assert.Equal(t, "- oldest\n- middle\n- newest", got)
	})

	t.Run("no commits no template is empty", func(t *testing.T) {
		n := node("feat/a")
		assert.Equal(t, "", PrefillDescription(n, ""))
	})
}

func TestNewSubmitNodes(t *testing.T) {
	nodes := []stackview.BranchNode{
		node("feat/a", commit("Add a", "body a")),
		withPR(node("feat/b"), &ghapi.PRDetails{Number: 2, State: "OPEN"}),
	}

	got := NewSubmitNodes(nodes, "")

	assert.Len(t, got, 2)

	// NEW branch: included by default, prefilled, ready (not draft).
	assert.Equal(t, StateNew, got[0].State)
	assert.True(t, got[0].Included)
	assert.Equal(t, "Add a", got[0].Title)
	assert.Equal(t, "body a", got[0].Description)
	assert.False(t, got[0].Draft, "new PRs default to ready for review")
	assert.False(t, got[0].Edited(), "freshly prefilled node is not edited")

	// OPEN branch: not included, locked.
	assert.Equal(t, StateOpen, got[1].State)
	assert.False(t, got[1].Included)
}

func TestCounts(t *testing.T) {
	nodes := []SubmitNode{
		{State: StateNew, Included: true},
		{State: StateNew, Included: false},
		{State: StateOpen},
		{State: StateClosed},
	}
	assert.Equal(t, 2, CountNew(nodes))
	assert.Equal(t, 1, CountSelected(nodes))
	assert.True(t, HasClosed(nodes))
}

func TestClosedBranches(t *testing.T) {
	nodes := []SubmitNode{
		{State: StateNew, BranchNode: node("feat/a")},
		{State: StateClosed, BranchNode: node("feat/legacy")},
	}
	assert.Equal(t, []string{"feat/legacy"}, ClosedBranches(nodes))
}

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"shared two-segment prefix", []string{"feat/auth/a", "feat/auth/b", "feat/auth/c"}, "feat/auth/"},
		{"shared one-segment prefix", []string{"feat/a", "feat/b"}, "feat/"},
		{"no shared prefix", []string{"feat/a", "fix/b"}, ""},
		{"single name has no prefix", []string{"feat/a"}, ""},
		{"empty list", nil, ""},
		{"flat names share nothing", []string{"a", "b"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CommonPrefix(tt.names))
		})
	}
}

func TestShortname(t *testing.T) {
	assert.Equal(t, "middleware", Shortname("feat/auth/middleware", "feat/auth/"))
	assert.Equal(t, "feat/auth/middleware", Shortname("feat/auth/middleware", ""))
	assert.Equal(t, "other/x", Shortname("other/x", "feat/auth/"))
}
