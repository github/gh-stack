package shared

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MinHeightForHeader is the minimum terminal height to show the header.
const MinHeightForHeader = 25

// MinWidthForShortcuts is the minimum width to show keyboard shortcuts.
const MinWidthForShortcuts = 65

// MinWidthForHeader is the minimum width to show the header at all.
const MinWidthForHeader = 53

// MinWidthForArt is the minimum width to show the logo in the header.
const MinWidthForArt = 96

// MinHeightForArt is the minimum terminal height to show the logo. It is a bit
// higher than MinHeightForHeader: at very short heights a vertical resize can
// leave a transient ghost of the inline image (kitty graphics live in a layer
// the text renderer can't repaint cleanly mid-resize), so the logo is dropped a
// little before the rest of the header to avoid the artifact.
const MinHeightForArt = 30

// ShortcutEntry represents a keyboard shortcut for the header.
type ShortcutEntry struct {
	Key      string
	Desc     string
	Disabled bool // when true, rendered in gray (dimmed)
}

// HeaderInfoLine represents an info line in the header (icon + label).
type HeaderInfoLine struct {
	Icon      string
	Label     string
	IconStyle *lipgloss.Style // optional override; nil uses default HeaderInfoStyle (cyan)
}

// headerLeftMargin is the left padding, in columns, before the logo and the
// info lines (which share this left edge). It is kept small so it visually
// matches the header's top and bottom padding.
const headerLeftMargin = 1

// The logo image sits in the top-left corner spanning the title and subtitle
// rows. logoImageCols is its width in cells, which drives the size: the mark is
// square and a terminal cell is about twice as tall as it is wide, so the logo
// renders about logoImageCols/2 cells tall. Width is the controlled dimension
// (kitty scales the square mark to logoImageCols cells wide; iTerm2 fits it
// within logoImageCols x logoImageRows), so the slot width is exact. 4 cols
// gives a ~2-cell-tall logo. logoImageRows bounds the height (and the layout
// slot's rows).
const (
	logoImageCols = 4
	logoImageRows = 2
)

// logoTextGap is the number of blank columns between the logo and the title /
// subtitle text, so the heading has a little room to breathe.
const logoTextGap = 2

// logoSlotWidth is the width reserved on the logo rows: the logo image plus the
// gap before the title and subtitle text.
const logoSlotWidth = logoImageCols + logoTextGap

// HeaderConfig controls what the header displays.
type HeaderConfig struct {
	ShowArt         bool             // whether to display GitHub logo
	Title           string           // heading next to logo art
	Subtitle        string           // version string, or empty
	InfoLines       []HeaderInfoLine // info rows (stack info)
	Shortcuts       []ShortcutEntry  // keyboard shortcuts
	ShortcutColumns int              // number of columns for shortcuts (default 1; set 2 for side-by-side)
}

// ShouldShowHeader returns whether the header should be displayed.
func ShouldShowHeader(width, height int) bool {
	return height >= MinHeightForHeader && width >= MinWidthForHeader
}

// ShouldShowShortcuts returns whether shortcuts should be displayed.
func ShouldShowShortcuts(width int) bool {
	return width >= MinWidthForShortcuts
}

// artFitsViewport reports whether the viewport is wide and tall enough to show
// the logo. The height bound (MinHeightForArt) is a little above
// MinHeightForHeader so the logo is dropped before the header itself at short
// heights, where a vertical resize can otherwise leave a transient ghost of the
// inline image.
func artFitsViewport(width, height int) bool {
	return width >= MinWidthForArt && height >= MinHeightForArt
}

// shortcutRowCount returns how many rows the shortcut block occupies for the
// config's column count.
func shortcutRowCount(cfg HeaderConfig) int {
	n := len(cfg.Shortcuts)
	if n == 0 {
		return 0
	}
	cols := cfg.ShortcutColumns
	if cols < 1 {
		cols = 1
	}
	return (n + cols - 1) / cols
}

// headerContentRows returns how many content rows the header needs: enough for
// the title/subtitle/info block or the shortcut block, whichever is taller. This
// keeps the box exactly as tall as its content, with no trailing empty row.
func headerContentRows(cfg HeaderConfig) int {
	// title (row 0), subtitle (row 1), a gap (row 2), then the info lines.
	info := 3 + len(cfg.InfoLines)
	sc := shortcutRowCount(cfg)
	if sc > info {
		return sc
	}
	return info
}

// HeaderHeightFor returns the number of screen lines the header occupies for the
// given config (its content rows plus the top and bottom borders).
func HeaderHeightFor(cfg HeaderConfig) int {
	return headerContentRows(cfg) + 2
}

// RenderHeader renders the full-width header box.
// Progressive disclosure as width narrows: first hides the art, then the
// info text, keeping keyboard shortcuts always visible.
func RenderHeader(b *strings.Builder, cfg HeaderConfig, width, height int) {
	if width < 2 {
		return
	}
	innerWidth := width - 2

	// Always build shortcut lines
	type shortcutLine struct {
		text     string
		visWidth int
	}
	var shortcuts []shortcutLine
	maxShortcutWidth := 0
	rightColWidth := 0

	cols := cfg.ShortcutColumns
	if cols < 1 {
		cols = 1
	}

	if len(cfg.Shortcuts) > 0 {
		if cols >= 2 {
			// Two-column layout with aligned keys and descriptions.
			// First pass: compute max visual key width per column.
			maxKeyLeft := 0
			maxKeyRight := 0
			for i := 0; i < len(cfg.Shortcuts); i += 2 {
				kw := lipgloss.Width(cfg.Shortcuts[i].Key)
				if kw > maxKeyLeft {
					maxKeyLeft = kw
				}
				if i+1 < len(cfg.Shortcuts) {
					kw = lipgloss.Width(cfg.Shortcuts[i+1].Key)
					if kw > maxKeyRight {
						maxKeyRight = kw
					}
				}
			}

			// Second pass: compute max visual width of the left column
			// so the right column starts at a consistent position.
			maxLeftWidth := 0
			for i := 0; i < len(cfg.Shortcuts); i += 2 {
				left := renderShortcutEntryPadded(cfg.Shortcuts[i], maxKeyLeft)
				lw := lipgloss.Width(left)
				if lw > maxLeftWidth {
					maxLeftWidth = lw
				}
			}

			colGap := "  "
			colGapWidth := lipgloss.Width(colGap)
			for i := 0; i < len(cfg.Shortcuts); i += 2 {
				left := renderShortcutEntryPadded(cfg.Shortcuts[i], maxKeyLeft)
				// Pad left entry to maxLeftWidth for consistent right column start
				leftPad := maxLeftWidth - lipgloss.Width(left)
				if leftPad < 0 {
					leftPad = 0
				}
				line := left + strings.Repeat(" ", leftPad)
				if i+1 < len(cfg.Shortcuts) {
					right := renderShortcutEntryPadded(cfg.Shortcuts[i+1], maxKeyRight)
					line = line + colGap + right
				}
				visW := lipgloss.Width(line)
				// Account for column gap width in case right column is missing
				if i+1 >= len(cfg.Shortcuts) {
					visW = maxLeftWidth + colGapWidth + maxKeyRight + 10 // approximate
				}
				shortcuts = append(shortcuts, shortcutLine{text: line, visWidth: visW})
				if visW > maxShortcutWidth {
					maxShortcutWidth = visW
				}
			}
		} else {
			// Single-column layout with aligned keys.
			maxKeyW := 0
			for _, sc := range cfg.Shortcuts {
				kw := lipgloss.Width(sc.Key)
				if kw > maxKeyW {
					maxKeyW = kw
				}
			}
			for _, sc := range cfg.Shortcuts {
				rendered := renderShortcutEntryPadded(sc, maxKeyW)
				visW := lipgloss.Width(rendered)
				shortcuts = append(shortcuts, shortcutLine{text: rendered, visWidth: visW})
				if visW > maxShortcutWidth {
					maxShortcutWidth = visW
				}
			}
		}
		rightColWidth = maxShortcutWidth + 2
	}

	// Determine what fits: shortcuts always shown, the logo and info are
	// progressive. The logo is image-or-nothing: it shows only when an
	// inline-image protocol is available and the viewport is wide enough.
	showArt := cfg.ShowArt && LogoAvailable()
	showInfo := true

	// Hide the logo when the viewport is too narrow or too short. The height
	// guard drops the logo a little before the rest of the header because a
	// vertical resize at very short heights can otherwise leave a transient
	// ghost of the inline image. The ClearLogo below removes any drawn logo.
	if showArt && !artFitsViewport(width, height) {
		showArt = false
	}

	// The logo image escape, emitted once on the first content row; it spans
	// logoImageRows rows and logoImageCols columns in the top-left corner.
	logoEsc := ""
	if showArt {
		logoEsc = renderHeaderLogo(logoImageCols, logoImageRows)
		if logoEsc == "" {
			showArt = false
		}
	}

	cr := headerContentRows(cfg)

	// If info + shortcuts don't fit, hide info
	infoMinWidth := 20 // rough minimum for title/info text
	if innerWidth < rightColWidth+infoMinWidth+4 {
		showInfo = false
	}

	// Map info lines to row indices
	infoByRow := make(map[int]string)
	if showInfo {
		infoByRow[0] = HeaderTitleStyle.Render(cfg.Title)
		if cfg.Subtitle != "" {
			infoByRow[1] = HeaderInfoLabelStyle.Render(cfg.Subtitle)
		}
		for i, info := range cfg.InfoLines {
			row := 3 + i
			if row > cr-1 {
				break
			}
			iconStyle := HeaderInfoStyle
			if info.IconStyle != nil {
				iconStyle = *info.IconStyle
			}
			infoByRow[row] = iconStyle.Render(info.Icon) + HeaderInfoLabelStyle.Render(" "+info.Label)
		}
	}

	// Vertically center shortcuts
	scStartRow := 0
	if len(shortcuts) > 0 {
		scStartRow = (cr - len(shortcuts)) / 2
		if scStartRow < 0 {
			scStartRow = 0
		}
	}

	// When the logo is hidden but the terminal could show one (e.g. resized too
	// narrow), remove any previously-drawn logo so it does not linger.
	if !showArt {
		b.WriteString(ClearLogo())
	}

	// Top border
	b.WriteString(HeaderBorderStyle.Render("┌" + strings.Repeat("─", innerWidth) + "┐"))
	b.WriteString("\n")

	// Content rows. The logo occupies the top-left corner across the title and
	// subtitle rows, which indent their text past the logo. Every other row (the
	// blank spacer and the info lines) starts at the shared left margin, so the
	// logo and the info icons line up on the same left edge.
	for i := 0; i < cr; i++ {
		var left strings.Builder
		left.WriteString(strings.Repeat(" ", headerLeftMargin))
		leftWidth := headerLeftMargin

		if showArt && i < logoImageRows {
			if i == 0 {
				left.WriteString(logoEsc)
			}
			left.WriteString(strings.Repeat(" ", logoSlotWidth))
			leftWidth += logoSlotWidth
		}

		if info, ok := infoByRow[i]; ok {
			left.WriteString(info)
			leftWidth += lipgloss.Width(info)
		}

		b.WriteString(HeaderBorderStyle.Render("│"))
		b.WriteString(left.String())

		if len(shortcuts) > 0 {
			shortcutCol := innerWidth - rightColWidth
			midPad := shortcutCol - leftWidth
			if midPad < 0 {
				midPad = 0
			}

			scIdx := i - scStartRow
			shortcutRendered := ""
			scVisWidth := 0
			if scIdx >= 0 && scIdx < len(shortcuts) {
				shortcutRendered = shortcuts[scIdx].text
				scVisWidth = shortcuts[scIdx].visWidth
			}
			scTrailingPad := rightColWidth - scVisWidth
			if scTrailingPad < 0 {
				scTrailingPad = 0
			}

			b.WriteString(strings.Repeat(" ", midPad))
			b.WriteString(shortcutRendered)
			b.WriteString(strings.Repeat(" ", scTrailingPad))
		} else {
			trailingPad := innerWidth - leftWidth
			if trailingPad < 0 {
				trailingPad = 0
			}
			b.WriteString(strings.Repeat(" ", trailingPad))
		}

		b.WriteString(HeaderBorderStyle.Render("│"))
		b.WriteString("\n")
	}

	// Bottom border
	b.WriteString(HeaderBorderStyle.Render("└" + strings.Repeat("─", innerWidth) + "┘"))
	b.WriteString("\n")
}

// disabledShortcutStyle renders both key and desc in dim gray.
var disabledShortcutStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// renderShortcutEntry renders a single shortcut, dimmed if disabled.
func renderShortcutEntry(sc ShortcutEntry) string {
	if sc.Disabled {
		return disabledShortcutStyle.Render(sc.Key + " " + sc.Desc)
	}
	return HeaderShortcutKey.Render(sc.Key) + HeaderShortcutDesc.Render(" "+sc.Desc)
}

// renderShortcutEntryPadded renders a shortcut with the key right-padded
// to keyWidth visual columns so descriptions align across rows.
func renderShortcutEntryPadded(sc ShortcutEntry, keyWidth int) string {
	keyVisWidth := lipgloss.Width(sc.Key)
	pad := ""
	if keyVisWidth < keyWidth {
		pad = strings.Repeat(" ", keyWidth-keyVisWidth)
	}
	if sc.Disabled {
		return disabledShortcutStyle.Render(sc.Key) + pad + disabledShortcutStyle.Render(" "+sc.Desc)
	}
	return HeaderShortcutKey.Render(sc.Key) + pad + HeaderShortcutDesc.Render(" "+sc.Desc)
}
