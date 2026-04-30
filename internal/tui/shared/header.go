package shared

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// HeaderHeight is the total number of lines the header occupies.
const HeaderHeight = 12

// MinHeightForHeader is the minimum terminal height to show the header.
const MinHeightForHeader = 25

// MinWidthForShortcuts is the minimum width to show keyboard shortcuts.
const MinWidthForShortcuts = 65

// MinWidthForHeader is the minimum width to show the header at all.
const MinWidthForHeader = 50

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

// ArtLines is the braille ASCII art for the View header.
var ArtLines = [10]string{
	"⠀⠀⠀⠀⠀⠀⣀⣤⣤⣤⣤⣤⣤⣀⠀⠀⠀⠀⠀⠀",
	"⠀⠀⠀⣠⣴⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣶⣄⠀⠀⠀",
	"⠀⢀⣼⣿⣿⠛⠛⠿⠿⠿⠿⠿⠿⠛⠛⣿⣿⣷⡀⠀",
	"⠀⣾⣿⣿⣿⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣿⣿⣿⣷⡀",
	"⢸⣿⣿⣿⡇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⣿⣿⣿⡇",
	"⢸⣿⣿⣿⡇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⣿⣿⣿⡇",
	"⠘⣿⣿⣿⣿⣦⡀⠀⠀⠀⠀⠀⠀⢀⣤⣿⣿⣿⣿⠇",
	"⠀⠹⣿⣦⡈⠻⢿⠟⠀⠀⠀⠀⢻⣿⣿⣿⣿⣿⠏⠀",
	"⠀⠀⠈⠻⣷⣤⣀⡀⠀⠀⠀⠀⢸⣿⣿⣿⡿⠃⠀⠀",
	"⠀⠀⠀⠀⠈⠙⠻⠇⠀⠀⠀⠀⠸⠟⠛⠁⠀⠀⠀⠀",
}

// ArtDisplayWidth is the visual column width of each art line.
const ArtDisplayWidth = 20

// HeaderConfig controls what the header displays.
type HeaderConfig struct {
	ShowArt          bool             // true for View (GitHub logo), false for Modify
	Title            string           // "GitHub Stacks" or "Modify Mode"
	Subtitle         string           // version string, or empty
	InfoLines        []HeaderInfoLine // info rows (stack info)
	Shortcuts        []ShortcutEntry  // keyboard shortcuts
	ShortcutColumns  int              // number of columns for shortcuts (default 1; set 2 for side-by-side)
}

// ShouldShowHeader returns whether the header should be displayed.
func ShouldShowHeader(width, height int) bool {
	return height >= MinHeightForHeader && width >= MinWidthForHeader
}

// ShouldShowShortcuts returns whether shortcuts should be displayed.
func ShouldShowShortcuts(width int) bool {
	return width >= MinWidthForShortcuts
}

// RenderHeader renders the full-width header box.
func RenderHeader(b *strings.Builder, cfg HeaderConfig, width, height int) {
	if width < 2 {
		return
	}
	innerWidth := width - 2

	showShortcuts := ShouldShowShortcuts(width)

	// Build shortcut lines (possibly multi-column)
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

	if showShortcuts && len(cfg.Shortcuts) > 0 {
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

	// Map info lines to row indices
	infoByRow := make(map[int]string)
	infoByRow[2] = HeaderTitleStyle.Render(cfg.Title)
	if cfg.Subtitle != "" {
		infoByRow[3] = HeaderInfoLabelStyle.Render(cfg.Subtitle)
	}
	for i, info := range cfg.InfoLines {
		row := 5 + i
		if row > 9 {
			break
		}
		iconStyle := HeaderInfoStyle
		if info.IconStyle != nil {
			iconStyle = *info.IconStyle
		}
		infoByRow[row] = iconStyle.Render(info.Icon) + HeaderInfoLabelStyle.Render(" "+info.Label)
	}

	// Left content base width
	leftContentBase := 1 // margin
	if cfg.ShowArt {
		leftContentBase += ArtDisplayWidth
	}

	// Vertically center shortcuts
	scStartRow := 0
	if len(shortcuts) > 0 {
		scStartRow = (10 - len(shortcuts)) / 2
	}

	gap := "  "

	// Top border
	b.WriteString(HeaderBorderStyle.Render("┌" + strings.Repeat("─", innerWidth) + "┐"))
	b.WriteString("\n")

	// Content rows
	for i := 0; i < 10; i++ {
		// Left column: art (optional) + info
		artText := ""
		if cfg.ShowArt {
			artText = ArtLines[i]
		}

		infoText := ""
		infoVisualLen := 0
		if info, ok := infoByRow[i]; ok {
			infoText = gap + info
			infoVisualLen = 2 + lipgloss.Width(info)
		}

		leftUsed := leftContentBase + infoVisualLen

		if showShortcuts && len(shortcuts) > 0 {
			shortcutCol := innerWidth - rightColWidth
			midPad := shortcutCol - leftUsed
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

			b.WriteString(HeaderBorderStyle.Render("│"))
			b.WriteString(" ")
			if cfg.ShowArt {
				b.WriteString(artText)
			}
			b.WriteString(infoText)
			b.WriteString(strings.Repeat(" ", midPad))
			b.WriteString(shortcutRendered)
			b.WriteString(strings.Repeat(" ", scTrailingPad))
			b.WriteString(HeaderBorderStyle.Render("│"))
		} else {
			trailingPad := innerWidth - leftUsed
			if trailingPad < 0 {
				trailingPad = 0
			}

			b.WriteString(HeaderBorderStyle.Render("│"))
			b.WriteString(" ")
			if cfg.ShowArt {
				b.WriteString(artText)
			}
			b.WriteString(infoText)
			b.WriteString(strings.Repeat(" ", trailingPad))
			b.WriteString(HeaderBorderStyle.Render("│"))
		}
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
