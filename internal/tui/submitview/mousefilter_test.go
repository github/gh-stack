package submitview

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runeMsg builds the multi-rune key message Bubble Tea emits for a leaked
// fragment of an escape sequence.
func runeMsg(s string, alt bool) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: alt}
}

// splitSGRBurst reproduces the exact key messages Bubble Tea's input parser
// emits when it splits the SGR mouse sequence seq ("\x1b[<Cb;Cx;Cy(M|m)") at
// byte offset cut (2..len-1): an Alt+"[" for the consumed "\x1b[", an optional
// middle run for the bytes before the cut, then the tail run after it.
func splitSGRBurst(seq string, cut int) []tea.KeyMsg {
	burst := []tea.KeyMsg{runeMsg("[", true)}
	if cut > 2 {
		burst = append(burst, runeMsg(seq[2:cut], false))
	}
	burst = append(burst, runeMsg(seq[cut:], false))
	return burst
}

func TestConsumeLeakedMouseKey_SwallowsSplitSequences(t *testing.T) {
	for _, seq := range []string{"\x1b[<65;54;51M", "\x1b[<64;10;20m", "\x1b[<0;1;1M"} {
		for cut := 2; cut < len(seq); cut++ {
			t.Run(fmt.Sprintf("%q@%d", seq, cut), func(t *testing.T) {
				m := testModel(t, newNodes())
				require.Equal(t, fieldTitle, m.focusedField)
				before := m.titleArea.Value()

				for _, msg := range splitSGRBurst(seq, cut) {
					updated, _ := m.Update(msg)
					m = updated.(Model)
				}

				assert.Equal(t, before, m.titleArea.Value(), "no fragment should reach the title field")
				assert.False(t, m.mouseLeakActive, "the sequence resets once its terminator is consumed")

				// A normal keystroke afterwards must still register.
				m = sendKey(t, m, runeKey('x'))
				assert.Equal(t, before+"x", m.titleArea.Value(), "typing works after a swallowed sequence")
			})
		}
	}
}

func TestConsumeLeakedMouseKey_SwallowsSingleRunTail(t *testing.T) {
	// When the parser consumes "\x1b" as a lone Escape, the rest of the body can
	// arrive as one run.
	m := testModel(t, newNodes())
	before := m.titleArea.Value()
	m = sendKey(t, m, runeMsg("[<65;54;51M", false))
	assert.Equal(t, before, m.titleArea.Value(), "a whole leaked body in one run is dropped")
}

func TestConsumeLeakedMouseKey_PreservesRealTyping(t *testing.T) {
	m := testModel(t, newNodes())
	before := m.titleArea.Value()

	// Characters from the mouse alphabet typed normally must pass through. Each
	// real keystroke is a single-rune message, never a complete SGR body.
	for _, r := range []rune{'<', '3', ';', '5', 'M', 'm'} {
		m = sendKey(t, m, runeKey(r))
	}
	assert.Equal(t, before+"<3;5Mm", m.titleArea.Value(), "ordinary characters are never filtered")
}

func TestConsumeLeakedMouseKey_StrayAltBracketDoesNotEatNextKey(t *testing.T) {
	m := testModel(t, newNodes())
	before := m.titleArea.Value()

	// A lone Alt+"[" opens a sequence, but the following key is not a mouse body,
	// so it must still be handled (here: typed into the field).
	m = sendKey(t, m, runeMsg("[", true))
	require.True(t, m.mouseLeakActive)
	m = sendKey(t, m, runeKey('z'))
	assert.False(t, m.mouseLeakActive, "a non-body rune ends the sequence")
	assert.Equal(t, before+"z", m.titleArea.Value(), "the following real key is not eaten")
}

func TestConsumeLeakedMouseKey_BracketedPasteIsNotFiltered(t *testing.T) {
	m := testModel(t, newNodes())
	before := m.titleArea.Value()

	// A genuine paste that happens to look like a mouse body is preserved.
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<65;54;51M"), Paste: true}
	updated, _ := m.Update(paste)
	m = updated.(Model)
	assert.Equal(t, before+"<65;54;51M", m.titleArea.Value(), "pasted content is never filtered")
}

func TestConsumeLeakedMouseKey_DescriptionField(t *testing.T) {
	m := testModel(t, newNodes())
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyTab}) // focus description
	require.Equal(t, fieldDescription, m.focusedField)
	before := m.descArea.Value()

	for _, msg := range splitSGRBurst("\x1b[<65;54;51M", 10) {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	assert.Equal(t, before, m.descArea.Value(), "no fragment should reach the description field")
}
