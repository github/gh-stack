// Package checkoutview renders the interactive stack picker shown by
// `gh stack checkout` when no target is given. It reconciles the locally
// tracked stacks with the stacks returned by the Stacks REST API into a single
// table, labels each as Local or Remote, and lets the user filter, search, and
// select one to check out.
package checkoutview

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
)

// StackType classifies where a stack lives.
type StackType int

const (
	// TypeLocal means the stack is present in the local stack file. It may also
	// be tracked on the remote; per the picker's simplification, anything
	// available locally is "Local".
	TypeLocal StackType = iota
	// TypeRemote means the stack exists only on the remote.
	TypeRemote
)

// String returns the human-readable label for the type column.
func (t StackType) String() string {
	if t == TypeRemote {
		return "Remote"
	}
	return "Local"
}

// StatusCounts summarizes a stack's composition for the status bar. Each field
// is a count of branches/PRs in that state.
type StatusCounts struct {
	Merged   int // merged PRs (purple)
	Open     int // open, non-merged PRs (green)
	Closed   int // closed, non-merged PRs (red)
	Unpushed int // branches with no PR yet (gray)
}

// Total returns the number of branches/PRs represented.
func (c StatusCounts) Total() int {
	return c.Merged + c.Open + c.Closed + c.Unpushed
}

// fullyMerged reports whether every entry is a merged PR (nothing open, closed,
// or unpushed). Such stacks can no longer be added to and are filtered out.
func (c StatusCounts) fullyMerged() bool {
	return c.Merged > 0 && c.Open == 0 && c.Closed == 0 && c.Unpushed == 0
}

// StackRow is a single reconciled entry shown in the checkout picker.
type StackRow struct {
	Number       int // stack number; 0 when unknown (local-only, not pushed)
	Type         StackType
	BottomBranch string
	TopBranch    string
	Base         string
	Status       StatusCounts
	Created      time.Time
	HasCreated   bool

	// LocalStack points at the local stack when Type == TypeLocal, so the caller
	// can check out its branches directly without cloning. It is nil for
	// remote-only rows, which are cloned by stack number instead.
	LocalStack *stack.Stack
}

// branchSep joins the bottom and top branch names in the Branches column,
// mirroring Git's A...B compare syntax.
const branchSep = "..."

// Summary returns "bottom...top" (or a single branch when the stack has one
// branch, or the ends coincide).
func (r StackRow) Summary() string {
	switch {
	case r.BottomBranch == "" && r.TopBranch == "":
		return ""
	case r.TopBranch == "" || r.BottomBranch == r.TopBranch:
		return r.BottomBranch
	case r.BottomBranch == "":
		return r.TopBranch
	default:
		return r.BottomBranch + branchSep + r.TopBranch
	}
}

// NumberDisplay renders the stack number, or "—" when it is unknown.
func (r StackRow) NumberDisplay() string {
	if r.Number == 0 {
		return "—"
	}
	return strconv.Itoa(r.Number)
}

// CreatedDisplay renders the creation time as a compact age, or "—" when
// unknown.
func (r StackRow) CreatedDisplay() string {
	if !r.HasCreated {
		return "—"
	}
	return relativeTime(r.Created)
}

// BuildRows reconciles the local stacks with the remote stacks into the ordered
// list shown in the picker. Local stacks own the "Local" type even when they
// are tracked on the remote; remote stacks with no local match are "Remote".
// Fully-merged stacks (every PR merged) are omitted. The local slice must be the
// caller's live stack slice, since rows retain pointers into it.
func BuildRows(local []stack.Stack, remote []github.RemoteStack) []StackRow {
	remoteByNumber := make(map[int]*github.RemoteStack, len(remote))
	remoteByID := make(map[int]*github.RemoteStack, len(remote))
	for i := range remote {
		rs := &remote[i]
		if rs.Number != 0 {
			remoteByNumber[rs.Number] = rs
		}
		remoteByID[rs.ID] = rs
	}

	matched := make(map[*github.RemoteStack]bool, len(remote))
	rows := make([]StackRow, 0, len(local)+len(remote))

	// Local stacks first — they own the "Local" type even when tracked.
	for i := range local {
		ls := &local[i]
		rs := matchRemote(ls, remoteByNumber, remoteByID)
		if rs != nil {
			matched[rs] = true
		}
		if row, ok := localRow(ls, rs); ok {
			rows = append(rows, row)
		}
	}

	// Remote-only stacks.
	for i := range remote {
		rs := &remote[i]
		if matched[rs] {
			continue
		}
		if row, ok := remoteRow(rs); ok {
			rows = append(rows, row)
		}
	}

	sortRows(rows)
	return rows
}

// matchRemote finds the remote stack that corresponds to a local stack, by
// stack number first and then by the remote id stored (as a string) locally.
func matchRemote(ls *stack.Stack, byNumber, byID map[int]*github.RemoteStack) *github.RemoteStack {
	if ls.Number != 0 {
		if rs := byNumber[ls.Number]; rs != nil {
			return rs
		}
	}
	if ls.ID != "" {
		if id, err := strconv.Atoi(ls.ID); err == nil {
			if rs := byID[id]; rs != nil {
				return rs
			}
		}
	}
	return nil
}

// localRow builds the row for a local stack, enriching it with live remote data
// (number, base, created, PR states) when a matching remote stack is provided.
func localRow(ls *stack.Stack, rs *github.RemoteStack) (StackRow, bool) {
	row := StackRow{
		Type:       TypeLocal,
		Number:     ls.Number,
		Base:       ls.Trunk.Branch,
		LocalStack: ls,
	}
	if len(ls.Branches) > 0 {
		row.BottomBranch = ls.Branches[0].Branch
		row.TopBranch = ls.Branches[len(ls.Branches)-1].Branch
	}

	if rs != nil {
		if rs.Number != 0 {
			row.Number = rs.Number
		}
		if rs.Base.Ref != "" {
			row.Base = rs.Base.Ref
		}
		if t, ok := parseTime(rs.CreatedAt); ok {
			row.Created, row.HasCreated = t, true
		}
		row.Status = statusFromLocalTracked(ls, rs)
	} else {
		row.Status = statusFromLocal(ls)
	}

	if row.Status.fullyMerged() {
		return StackRow{}, false
	}
	return row, true
}

// remoteRow builds the row for a remote-only stack from its API details.
func remoteRow(rs *github.RemoteStack) (StackRow, bool) {
	if len(rs.PRDetails) == 0 {
		return StackRow{}, false
	}
	row := StackRow{
		Number:       rs.Number,
		Type:         TypeRemote,
		Base:         rs.Base.Ref,
		BottomBranch: rs.PRDetails[0].Head.Ref,
		TopBranch:    rs.PRDetails[len(rs.PRDetails)-1].Head.Ref,
	}
	if t, ok := parseTime(rs.CreatedAt); ok {
		row.Created, row.HasCreated = t, true
	}

	var c StatusCounts
	for _, p := range rs.PRDetails {
		classifyRemotePR(p, &c)
	}
	row.Status = c

	if c.fullyMerged() {
		return StackRow{}, false
	}
	return row, true
}

// statusFromLocalTracked derives status counts for a tracked stack, preferring
// the remote's live PR states and counting local branches with no PR as
// unpushed. It produces one entry per local branch.
func statusFromLocalTracked(ls *stack.Stack, rs *github.RemoteStack) StatusCounts {
	remoteByNum := make(map[int]github.RemoteStackPR, len(rs.PRDetails))
	for _, p := range rs.PRDetails {
		remoteByNum[p.Number] = p
	}

	var c StatusCounts
	for i := range ls.Branches {
		b := &ls.Branches[i]
		if b.PullRequest != nil && b.PullRequest.Number != 0 {
			if p, ok := remoteByNum[b.PullRequest.Number]; ok {
				classifyRemotePR(p, &c)
				continue
			}
			// Tracked locally but absent from the remote stack — fall back to the
			// local merged flag.
			if b.IsMerged() {
				c.Merged++
			} else {
				c.Open++
			}
			continue
		}
		c.Unpushed++
	}
	return c
}

// statusFromLocal derives status counts for an untracked local stack from its
// local PR references alone.
func statusFromLocal(ls *stack.Stack) StatusCounts {
	var c StatusCounts
	for i := range ls.Branches {
		b := &ls.Branches[i]
		if b.PullRequest != nil && b.PullRequest.Number != 0 {
			if b.IsMerged() {
				c.Merged++
			} else {
				c.Open++
			}
			continue
		}
		c.Unpushed++
	}
	return c
}

// classifyRemotePR increments the count matching a remote PR's state.
func classifyRemotePR(p github.RemoteStackPR, c *StatusCounts) {
	switch {
	case p.IsMerged():
		c.Merged++
	case p.State == "closed":
		c.Closed++
	default:
		c.Open++
	}
}

// sortRows orders rows newest-first: local-only stacks with no number surface
// first (active in-progress work), then higher stack numbers (newer) before
// lower ones. Ties break by creation time then summary for determinism.
func sortRows(rows []StackRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Number == 0) != (b.Number == 0) {
			return a.Number == 0
		}
		if a.Number != b.Number {
			return a.Number > b.Number
		}
		if a.HasCreated != b.HasCreated {
			return a.HasCreated
		}
		if a.HasCreated && b.HasCreated && !a.Created.Equal(b.Created) {
			return a.Created.After(b.Created)
		}
		return a.Summary() < b.Summary()
	})
}

// parseTime parses an RFC3339 timestamp, reporting whether it succeeded.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// relativeTime formats t as a compact age like "15m ago", "3d ago", or
// "1mo ago".
func relativeTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}
