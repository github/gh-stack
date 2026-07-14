package checkoutview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
)

func mergedAt(t time.Time) *string {
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// findRow returns the row with the given number (or a zero-value row) for
// assertions.
func findRow(rows []StackRow, number int) (StackRow, bool) {
	for _, r := range rows {
		if r.Number == number {
			return r, true
		}
	}
	return StackRow{}, false
}

func TestBuildRows_LocalOnly_Unpushed(t *testing.T) {
	local := []stack.Stack{{
		Trunk: stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "feat-a"},
			{Branch: "feat-b"},
		},
	}}

	rows := BuildRows(local, nil)
	require.Len(t, rows, 1)

	r := rows[0]
	assert.Equal(t, TypeLocal, r.Type)
	assert.Equal(t, 0, r.Number)
	assert.Equal(t, "—", r.NumberDisplay())
	assert.Equal(t, "feat-a...feat-b", r.Summary())
	assert.Equal(t, "main", r.Base)
	assert.Equal(t, StatusCounts{Unpushed: 2}, r.Status)
	assert.False(t, r.HasCreated)
	assert.Equal(t, "—", r.CreatedDisplay())
	require.NotNil(t, r.LocalStack)
	assert.Same(t, &local[0], r.LocalStack)
}

func TestBuildRows_RemoteOnly(t *testing.T) {
	now := time.Now()
	remote := []github.RemoteStack{{
		ID:        200,
		Number:    55,
		Base:      github.RemoteStackBase{Ref: "main"},
		CreatedAt: rfc3339(now.Add(-3 * time.Hour)),
		PRDetails: []github.RemoteStackPR{
			{Number: 7, State: "open", Head: github.RemoteStackPRHead{Ref: "refactor-1"}},
			{Number: 8, State: "closed", Head: github.RemoteStackPRHead{Ref: "refactor-2"}},
			{Number: 9, State: "open", Head: github.RemoteStackPRHead{Ref: "refactor-3"}},
		},
	}}

	rows := BuildRows(nil, remote)
	require.Len(t, rows, 1)

	r := rows[0]
	assert.Equal(t, TypeRemote, r.Type)
	assert.Equal(t, 55, r.Number)
	assert.Equal(t, "refactor-1...refactor-3", r.Summary())
	assert.Equal(t, "main", r.Base)
	assert.Equal(t, StatusCounts{Open: 2, Closed: 1}, r.Status)
	assert.True(t, r.HasCreated)
	assert.Nil(t, r.LocalStack)
}

func TestBuildRows_TrackedStack_IsLocalWithLiveStatus(t *testing.T) {
	now := time.Now()
	// Local stack tracked on remote (matched by number and id). It has three
	// branches: one merged PR, one open PR, and one unpushed branch.
	local := []stack.Stack{{
		Number: 42,
		ID:     "100",
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "api-1", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "api-2", PullRequest: &stack.PullRequestRef{Number: 2}},
			{Branch: "api-3"},
		},
	}}
	remote := []github.RemoteStack{{
		ID:        100,
		Number:    42,
		Base:      github.RemoteStackBase{Ref: "trunk"},
		CreatedAt: rfc3339(now.Add(-72 * time.Hour)),
		PRDetails: []github.RemoteStackPR{
			{Number: 1, State: "closed", MergedAt: mergedAt(now), Head: github.RemoteStackPRHead{Ref: "api-1"}},
			{Number: 2, State: "open", Head: github.RemoteStackPRHead{Ref: "api-2"}},
		},
	}}

	rows := BuildRows(local, remote)
	require.Len(t, rows, 1, "tracked stack must appear once, not duplicated")

	r := rows[0]
	assert.Equal(t, TypeLocal, r.Type, "available locally means Local even when tracked")
	assert.Equal(t, 42, r.Number)
	// Base and created come from the live remote data.
	assert.Equal(t, "trunk", r.Base)
	assert.True(t, r.HasCreated)
	// One box per local branch: merged (remote), open (remote), unpushed (no PR).
	assert.Equal(t, StatusCounts{Merged: 1, Open: 1, Unpushed: 1}, r.Status)
}

func TestBuildRows_MatchByID_WhenNumberMissing(t *testing.T) {
	// Local stack has no number yet (0) but stores the remote id as a string.
	local := []stack.Stack{{
		Number: 0,
		ID:     "500",
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "b1", PullRequest: &stack.PullRequestRef{Number: 1}},
			{Branch: "b2", PullRequest: &stack.PullRequestRef{Number: 2}},
		},
	}}
	remote := []github.RemoteStack{{
		ID:     500,
		Number: 88,
		Base:   github.RemoteStackBase{Ref: "main"},
		PRDetails: []github.RemoteStackPR{
			{Number: 1, State: "open", Head: github.RemoteStackPRHead{Ref: "b1"}},
			{Number: 2, State: "open", Head: github.RemoteStackPRHead{Ref: "b2"}},
		},
	}}

	rows := BuildRows(local, remote)
	require.Len(t, rows, 1, "id match must dedupe local and remote")
	assert.Equal(t, TypeLocal, rows[0].Type)
	assert.Equal(t, 88, rows[0].Number, "number is backfilled from the matched remote")
}

func TestBuildRows_FiltersFullyMergedStacks(t *testing.T) {
	now := time.Now()
	remote := []github.RemoteStack{
		{
			ID: 1, Number: 1, Base: github.RemoteStackBase{Ref: "main"}, CreatedAt: rfc3339(now),
			PRDetails: []github.RemoteStackPR{
				{Number: 10, State: "closed", MergedAt: mergedAt(now), Head: github.RemoteStackPRHead{Ref: "m1"}},
				{Number: 11, State: "closed", MergedAt: mergedAt(now), Head: github.RemoteStackPRHead{Ref: "m2"}},
			},
		},
		{
			ID: 2, Number: 2, Base: github.RemoteStackBase{Ref: "main"}, CreatedAt: rfc3339(now),
			PRDetails: []github.RemoteStackPR{
				{Number: 20, State: "closed", MergedAt: mergedAt(now), Head: github.RemoteStackPRHead{Ref: "x"}},
				{Number: 21, State: "open", Head: github.RemoteStackPRHead{Ref: "y"}},
			},
		},
	}

	rows := BuildRows(nil, remote)
	require.Len(t, rows, 1, "fully-merged stack #1 must be filtered out")
	assert.Equal(t, 2, rows[0].Number)
}

func TestBuildRows_LocalFullyMergedIsFiltered(t *testing.T) {
	local := []stack.Stack{{
		Number: 9,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 2, Merged: true}},
		},
	}}

	rows := BuildRows(local, nil)
	assert.Empty(t, rows, "a local stack with every branch merged is filtered")
}

func TestBuildRows_SortOrder(t *testing.T) {
	now := time.Now()
	local := []stack.Stack{{
		Number:   0, // local-only, unpushed -> sorts first
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{{Branch: "wip"}},
	}}
	remote := []github.RemoteStack{
		{ID: 1, Number: 10, Base: github.RemoteStackBase{Ref: "main"}, CreatedAt: rfc3339(now),
			PRDetails: []github.RemoteStackPR{{Number: 1, State: "open", Head: github.RemoteStackPRHead{Ref: "a"}}}},
		{ID: 2, Number: 30, Base: github.RemoteStackBase{Ref: "main"}, CreatedAt: rfc3339(now),
			PRDetails: []github.RemoteStackPR{{Number: 2, State: "open", Head: github.RemoteStackPRHead{Ref: "b"}}}},
	}

	rows := BuildRows(local, remote)
	require.Len(t, rows, 3)
	// Local-only (number 0) first, then higher numbers before lower.
	assert.Equal(t, 0, rows[0].Number)
	assert.Equal(t, 30, rows[1].Number)
	assert.Equal(t, 10, rows[2].Number)
}

func TestBuildRows_ClosedNonMergedStackIsKept(t *testing.T) {
	remote := []github.RemoteStack{{
		ID: 1, Number: 1, Base: github.RemoteStackBase{Ref: "main"},
		PRDetails: []github.RemoteStackPR{
			{Number: 10, State: "closed", Head: github.RemoteStackPRHead{Ref: "x"}},
			{Number: 11, State: "closed", Head: github.RemoteStackPRHead{Ref: "y"}},
		},
	}}
	rows := BuildRows(nil, remote)
	require.Len(t, rows, 1, "closed-but-not-merged stacks stay visible")
	assert.Equal(t, StatusCounts{Closed: 2}, rows[0].Status)
}

func TestBuildRows_UntrackedLocalWithMergedPRFlag(t *testing.T) {
	local := []stack.Stack{{
		Number: 3,
		Trunk:  stack.BranchRef{Branch: "main"},
		Branches: []stack.BranchRef{
			{Branch: "a", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
			{Branch: "b", PullRequest: &stack.PullRequestRef{Number: 2}},
		},
	}}
	rows := BuildRows(local, nil)
	require.Len(t, rows, 1)
	assert.Equal(t, StatusCounts{Merged: 1, Open: 1}, rows[0].Status)
}

func TestStackRow_Summary(t *testing.T) {
	tests := []struct {
		name   string
		row    StackRow
		expect string
	}{
		{"two branches", StackRow{BottomBranch: "a", TopBranch: "b"}, "a...b"},
		{"single branch", StackRow{BottomBranch: "a", TopBranch: "a"}, "a"},
		{"only bottom", StackRow{BottomBranch: "a"}, "a"},
		{"empty", StackRow{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.row.Summary())
		})
	}
}

func TestStatusCounts_FullyMerged(t *testing.T) {
	assert.True(t, StatusCounts{Merged: 3}.fullyMerged())
	assert.False(t, StatusCounts{}.fullyMerged())
	assert.False(t, StatusCounts{Merged: 1, Open: 1}.fullyMerged())
	assert.False(t, StatusCounts{Unpushed: 2}.fullyMerged())
	assert.False(t, StatusCounts{Merged: 1, Closed: 1}.fullyMerged())
}

func TestDistribute(t *testing.T) {
	// Fits within max: unchanged (one square per branch).
	assert.Equal(t, []int{1, 2, 0, 1}, distribute([]int{1, 2, 0, 1}, 10))

	// Exceeds max: downsample to sum exactly max, preserving proportions.
	got := distribute([]int{10, 10, 0, 0}, 10)
	sum := 0
	for _, n := range got {
		sum += n
	}
	assert.Equal(t, 10, sum)
	assert.Equal(t, []int{5, 5, 0, 0}, got)
}

func TestParseTime(t *testing.T) {
	_, ok := parseTime("")
	assert.False(t, ok)
	_, ok = parseTime("not-a-time")
	assert.False(t, ok)
	tm, ok := parseTime("2024-01-02T03:04:05Z")
	require.True(t, ok)
	assert.Equal(t, 2024, tm.Year())
}

func TestRelativeTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		at     time.Time
		expect string
	}{
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-15 * time.Minute), "15m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-3 * 24 * time.Hour), "3d ago"},
		{"weeks", now.Add(-14 * 24 * time.Hour), "2w ago"},
		{"months", now.Add(-60 * 24 * time.Hour), "2mo ago"},
		{"future clamps", now.Add(1 * time.Hour), "just now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, relativeTime(tt.at))
		})
	}
}
