package shared

import "strings"

// ClampScroll ensures scrollOffset doesn't exceed content bounds.
func ClampScroll(totalLines, viewHeight, scrollOffset int) int {
	maxScroll := totalLines - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scrollOffset > maxScroll {
		scrollOffset = maxScroll
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	return scrollOffset
}

// EnsureVisible adjusts scrollOffset so the cursor's line range is visible.
func EnsureVisible(startLine, endLine, scrollOffset, viewHeight int) int {
	if viewHeight < 1 {
		viewHeight = 1
	}
	if startLine < scrollOffset {
		scrollOffset = startLine
	}
	if endLine > scrollOffset+viewHeight {
		scrollOffset = endLine - viewHeight
	}
	return scrollOffset
}

// ApplyScrollToContent takes rendered content, splits into lines, applies
// scroll offset, and returns the visible portion as a string.
func ApplyScrollToContent(content string, scrollOffset, viewHeight int) string {
	lines := strings.Split(content, "\n")
	maxScroll := len(lines) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	start := scrollOffset
	if start > maxScroll {
		start = maxScroll
	}
	end := start + viewHeight
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}
