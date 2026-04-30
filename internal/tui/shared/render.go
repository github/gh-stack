package shared

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/github/gh-stack/internal/git"
	ghapi "github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
)

// BranchNodeData is the interface for branch data that can be rendered.
// Both stackview.BranchNode and modifyview.ModifyBranchNode satisfy this.
type BranchNodeData struct {
	Ref             stack.BranchRef
	IsCurrent       bool
	IsLinear        bool
	BaseBranch      string
	Commits         []git.CommitInfo
	FilesChanged    []git.FileDiffStat
	PR              *ghapi.PRDetails
	Additions       int
	Deletions       int
	CommitsExpanded bool
	FilesExpanded   bool

	// ShowCurrentLabel controls whether "(current)" is appended and cyan
	// styling is used for the current branch. View sets this true; Modify
	// leaves it false so all branches look uniform.
	ShowCurrentLabel bool

	// BranchNameStyleOverride, when non-nil, overrides the default branch
	// name style. Used by Modify to render dropped branches in red
	// strikethrough and folded branches in yellow strikethrough.
	BranchNameStyleOverride *lipgloss.Style

	// ForceDashedConnector, when true, forces the connector line to use
	// the dashed style (┊) regardless of linearity. Used by Modify for
	// branches marked for drop or fold.
	ForceDashedConnector bool

	// ConnectorStyleOverride, when non-nil, overrides the default connector
	// color for dashed lines. Used to make drop connectors red and fold
	// connectors yellow.
	ConnectorStyleOverride *lipgloss.Style
}

// NodeAnnotation is an optional annotation to display after the branch info.
type NodeAnnotation struct {
	Text  string
	Style lipgloss.Style
}

// ResolveConnectorStyle determines the connector character and style for a node.
func ResolveConnectorStyle(node BranchNodeData, isFocused bool) (string, lipgloss.Style) {
	connector := "│"
	connStyle := ConnectorStyle
	isMerged := node.Ref.IsMerged()
	isQueued := node.Ref.IsQueued()
	if node.ForceDashedConnector || (!node.IsLinear && !isMerged && !isQueued) {
		connector = "┊"
		if node.ConnectorStyleOverride == nil {
			connStyle = ConnectorDashedStyle
		}
	}
	// Apply explicit connector color override (drop=red, fold=yellow, moved=magenta)
	if node.ConnectorStyleOverride != nil {
		connStyle = *node.ConnectorStyleOverride
	}
	if isFocused && node.ConnectorStyleOverride == nil {
		if node.IsCurrent && node.ShowCurrentLabel {
			connStyle = ConnectorCurrentStyle
		} else if isMerged {
			connStyle = ConnectorMergedStyle
		} else if isQueued {
			connStyle = ConnectorQueuedStyle
		} else {
			connStyle = ConnectorFocusedStyle
		}
	}
	return connector, connStyle
}

// StatusIcon returns the appropriate status icon for a branch.
func StatusIcon(node BranchNodeData) string {
	if node.Ref.IsMerged() {
		return MergedIcon
	}
	if node.Ref.IsQueued() {
		return QueuedIcon
	}
	if !node.IsLinear {
		return WarningIcon
	}
	if node.PR != nil && node.PR.Number != 0 {
		return OpenIcon
	}
	return ""
}

// RenderNode renders a single branch node with all its sub-sections.
// annotation is optional — pass nil for plain view, or a NodeAnnotation to add a badge.
func RenderNode(b *strings.Builder, node BranchNodeData, isFocused bool, width int, annotation *NodeAnnotation) {
	connector, connStyle := ResolveConnectorStyle(node, isFocused)

	if node.PR != nil {
		RenderPRHeader(b, node, isFocused, connStyle, annotation)
		RenderBranchLine(b, node, connector, connStyle, nil) // annotation already on PR line
	} else {
		RenderBranchHeader(b, node, isFocused, connStyle, annotation)
	}

	if len(node.FilesChanged) > 0 {
		RenderFiles(b, node, connector, connStyle, width)
	}

	if len(node.Commits) > 0 {
		RenderCommits(b, node, connector, connStyle, width)
	}

	// Connector/spacer
	b.WriteString(connStyle.Render(connector))
	b.WriteString("\n")
}

// RenderPRHeader renders the top line with PR info: bullet + status icon + PR# + state + optional annotation.
func RenderPRHeader(b *strings.Builder, node BranchNodeData, isFocused bool, connStyle lipgloss.Style, annotation *NodeAnnotation) {
	bullet := "├"
	if isFocused {
		bullet = "▶"
	}
	b.WriteString(connStyle.Render(bullet + " "))

	icon := StatusIcon(node)
	if icon != "" {
		b.WriteString(icon + " ")
	}

	pr := node.PR
	prLabel := fmt.Sprintf("#%d", pr.Number)
	stateLabel := ""
	style := PROpenStyle
	switch {
	case pr.Merged:
		stateLabel = " MERGED"
		style = PRMergedStyle
	case pr.IsQueued:
		stateLabel = " QUEUED"
		style = PRQueuedStyle
	case pr.State == "CLOSED":
		stateLabel = " CLOSED"
		style = PRClosedStyle
	case pr.IsDraft:
		stateLabel = " DRAFT"
		style = PRDraftStyle
	default:
		stateLabel = " OPEN"
	}
	b.WriteString(style.Underline(true).Render(prLabel) + style.Render(stateLabel))

	if annotation != nil {
		b.WriteString("  ")
		b.WriteString(annotation.Style.Render(annotation.Text))
	}

	b.WriteString("\n")
}

// RenderBranchLine renders branch name + diff stats below a PR header.
func RenderBranchLine(b *strings.Builder, node BranchNodeData, connector string, connStyle lipgloss.Style, annotation *NodeAnnotation) {
	b.WriteString(connStyle.Render(connector))
	b.WriteString(" ")

	b.WriteString(renderBranchName(node))

	RenderDiffStats(b, node)

	if annotation != nil {
		b.WriteString("  ")
		b.WriteString(annotation.Style.Render(annotation.Text))
	}

	b.WriteString("\n")
}

// RenderBranchHeader renders header when no PR exists: bullet + icon + branch + stats + annotation.
func RenderBranchHeader(b *strings.Builder, node BranchNodeData, isFocused bool, connStyle lipgloss.Style, annotation *NodeAnnotation) {
	bullet := "├"
	if isFocused {
		bullet = "▶"
	}
	b.WriteString(connStyle.Render(bullet + " "))

	icon := StatusIcon(node)
	if icon != "" {
		b.WriteString(icon + " ")
	}

	b.WriteString(renderBranchName(node))

	RenderDiffStats(b, node)

	if annotation != nil {
		b.WriteString("  ")
		b.WriteString(annotation.Style.Render(annotation.Text))
	}

	b.WriteString("\n")
}

// RenderDiffStats appends +N -N diff stats.
func RenderDiffStats(b *strings.Builder, node BranchNodeData) {
	if node.Additions > 0 || node.Deletions > 0 {
		b.WriteString("  ")
		b.WriteString(AdditionsStyle.Render(fmt.Sprintf("+%d", node.Additions)))
		b.WriteString(" ")
		b.WriteString(DeletionsStyle.Render(fmt.Sprintf("-%d", node.Deletions)))
	}
}

// renderBranchName returns the styled branch name string based on node settings.
func renderBranchName(node BranchNodeData) string {
	name := node.Ref.Branch
	if node.BranchNameStyleOverride != nil {
		return node.BranchNameStyleOverride.Render(name)
	}
	if node.IsCurrent && node.ShowCurrentLabel {
		return CurrentBranchStyle.Render(name + " (current)")
	}
	return NormalBranchStyle.Render(name)
}

// RenderFiles renders the files toggle and optionally expanded file list.
func RenderFiles(b *strings.Builder, node BranchNodeData, connector string, connStyle lipgloss.Style, width int) {
	b.WriteString(connStyle.Render(connector))
	b.WriteString("  ")

	icon := CollapsedIcon
	if node.FilesExpanded {
		icon = ExpandedIcon
	}
	fileLabel := "files changed"
	if len(node.FilesChanged) == 1 {
		fileLabel = "file changed"
	}
	b.WriteString(CommitTimeStyle.Render(fmt.Sprintf("%s %d %s", icon, len(node.FilesChanged), fileLabel)))
	b.WriteString("\n")

	if !node.FilesExpanded {
		return
	}

	for _, f := range node.FilesChanged {
		b.WriteString(connStyle.Render(connector))
		b.WriteString("    ")

		path := f.Path
		maxLen := width - 30
		if maxLen < 20 {
			maxLen = 20
		}
		if len(path) > maxLen {
			path = "…" + path[len(path)-maxLen+1:]
		}
		b.WriteString(NormalBranchStyle.Render(path))
		b.WriteString("  ")
		b.WriteString(AdditionsStyle.Render(fmt.Sprintf("+%d", f.Additions)))
		b.WriteString(" ")
		b.WriteString(DeletionsStyle.Render(fmt.Sprintf("-%d", f.Deletions)))
		b.WriteString("\n")
	}
}

// RenderCommits renders the commits toggle and optionally expanded commits.
func RenderCommits(b *strings.Builder, node BranchNodeData, connector string, connStyle lipgloss.Style, width int) {
	b.WriteString(connStyle.Render(connector))
	b.WriteString("  ")

	icon := CollapsedIcon
	if node.CommitsExpanded {
		icon = ExpandedIcon
	}
	commitLabel := "commits"
	if len(node.Commits) == 1 {
		commitLabel = "commit"
	}
	b.WriteString(CommitTimeStyle.Render(fmt.Sprintf("%s %d %s", icon, len(node.Commits), commitLabel)))
	b.WriteString("\n")

	if !node.CommitsExpanded {
		return
	}

	for _, c := range node.Commits {
		b.WriteString(connStyle.Render(connector))
		b.WriteString("    ")

		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		b.WriteString(CommitSHAStyle.Render(sha))
		b.WriteString(" ")

		subject := c.Subject
		maxLen := width - 35
		if maxLen < 20 {
			maxLen = 20
		}
		if len(subject) > maxLen {
			subject = subject[:maxLen-1] + "…"
		}
		b.WriteString(CommitSubjectStyle.Render(subject))
		b.WriteString("  ")
		b.WriteString(CommitTimeStyle.Render(TimeAgo(c.Time)))
		b.WriteString("\n")
	}
}

// NodeLineCount returns how many rendered lines a node occupies.
func NodeLineCount(node BranchNodeData) int {
	lines := 1 // header line
	if node.PR != nil {
		lines++ // branch + diff stats line below PR header
	}
	if len(node.FilesChanged) > 0 {
		lines++ // files toggle
		if node.FilesExpanded {
			lines += len(node.FilesChanged)
		}
	}
	if len(node.Commits) > 0 {
		lines++ // commits toggle
		if node.CommitsExpanded {
			lines += len(node.Commits)
		}
	}
	lines++ // connector/spacer
	return lines
}

// RenderTrunk renders the trunk line.
func RenderTrunk(b *strings.Builder, trunkBranch string) {
	b.WriteString(ConnectorStyle.Render("└ "))
	b.WriteString(TrunkStyle.Render(trunkBranch))
	b.WriteString("\n")
}

// RenderMergedSeparator renders the merged section separator.
func RenderMergedSeparator(b *strings.Builder) {
	b.WriteString(ConnectorStyle.Render("────") + DimStyle.Render(" merged ") + ConnectorStyle.Render("─────") + "\n")
}

// RenderQueuedSeparator renders the queued section separator.
func RenderQueuedSeparator(b *strings.Builder) {
	b.WriteString(ConnectorStyle.Render("────") + DimStyle.Render(" queued ") + ConnectorStyle.Render("─────") + "\n")
}

// TimeAgo returns a human-readable time-ago string.
func TimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		secs := int(d.Seconds())
		if secs == 1 {
			return "1 second ago"
		}
		return fmt.Sprintf("%d seconds ago", secs)
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months <= 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}
