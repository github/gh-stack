package submitview

import (
	"regexp"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	// sgrMouseTailRe matches a complete SGR mouse-sequence body: the bytes that
	// follow the "\x1b[" prefix, optionally including a leftover "[" that the
	// parser surfaced separately. Examples: "[<65;54;51M", "<0;12;7m".
	sgrMouseTailRe = regexp.MustCompile(`^\[?<\d+;\d+;\d+[Mm]$`)
	// sgrMouseBodyRe matches a partial SGR mouse body that is still accumulating
	// its parameters, with no terminator yet. Examples: "<65;54;5", "<65;", "<".
	sgrMouseBodyRe = regexp.MustCompile(`^<?[\d;]*$`)
)

// mouseLeakBufCap bounds how many runes we will swallow for a single suspected
// mouse sequence before giving up, so a stray Alt+"[" can never eat an unbounded
// run of real keystrokes. Real SGR mouse bodies are far shorter than this.
const mouseLeakBufCap = 32

// consumeLeakedMouseKey reports whether key is a fragment of a terminal mouse
// escape sequence that Bubble Tea's input parser split across reads and emitted
// as key runes instead of a tea.MouseMsg. Such fragments must be dropped so the
// user does not see them typed into the focused title/description field while
// scrolling the mouse wheel.
//
// A split SGR mouse sequence ("\x1b[<Cb;Cx;Cy(M|m)") surfaces in one of two
// shapes:
//
//   - the whole body in a single run, e.g. "[<65;54;51M"; or
//   - (the common case under a scroll flood) the consumed "\x1b[" as an Alt+"["
//     key, followed by body fragments such as "<65;54;5" then "1M".
//
// We recognise the start, then swallow the body up to its "M"/"m" terminator.
// Anything that is not part of an SGR body — a control key, a bracketed paste,
// or a rune that breaks the pattern — ends the sequence and is handled normally,
// so real typing is never eaten.
func (m *Model) consumeLeakedMouseKey(key tea.KeyMsg) bool {
	// Only printable, non-pasted runes can be part of a leaked mouse body. Any
	// other key (control key, navigation, bracketed paste) means the leak, if
	// any, is over: reset and let the key be handled normally.
	if key.Type != tea.KeyRunes || key.Paste {
		m.mouseLeakActive = false
		m.mouseLeakBuf = ""
		return false
	}
	s := string(key.Runes)

	if !m.mouseLeakActive {
		// The consumed "\x1b[" prefix surfaces as Alt+"[": the start of a split
		// sequence whose body fragments follow.
		if key.Alt && s == "[" {
			m.mouseLeakActive = true
			m.mouseLeakBuf = ""
			return true
		}
		// The entire body can also arrive as a single run.
		if !key.Alt && sgrMouseTailRe.MatchString(s) {
			return true
		}
		return false
	}

	// Mid-sequence: accumulate body fragments until the terminator.
	cand := m.mouseLeakBuf + s
	switch {
	case sgrMouseTailRe.MatchString(cand):
		// Reached the "M"/"m" terminator: the sequence is complete.
		m.mouseLeakActive = false
		m.mouseLeakBuf = ""
		return true
	case len(cand) <= mouseLeakBufCap && sgrMouseBodyRe.MatchString(cand):
		// Still inside the parameter list; keep swallowing.
		m.mouseLeakBuf = cand
		return true
	default:
		// Not a mouse body after all (e.g. real typing after a stray Alt+"["):
		// stop swallowing and let this key be handled normally.
		m.mouseLeakActive = false
		m.mouseLeakBuf = ""
		return false
	}
}
