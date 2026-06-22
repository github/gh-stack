package shared

import (
	_ "embed"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/BourgeoisBear/rasterm"
	"golang.org/x/term"
)

// invertocatPNG is the white GitHub Invertocat mark, embedded so it never has to
// be fetched at runtime. It is drawn via an inline-image protocol; there is no
// character-art fallback.
//
//go:embed assets/invertocat-white.png
var invertocatPNG []byte

// headerLogoID is a fixed kitty image id so re-emitting the header replaces the
// logo in place (and lets us delete it by id) instead of stacking copies.
const headerLogoID = 7

// Cursor save/restore (DECSC/DECRC). Drawing an inline image moves the terminal
// cursor; wrapping the escape in save/restore returns the cursor to where the
// renderer expects it, so the image does not push the rest of the frame down.
const (
	cursorSave    = "\x1b7"
	cursorRestore = "\x1b8"
)

// logoProto identifies the inline-image protocol to use for the logo.
type logoProto int

const (
	logoNone logoProto = iota
	logoKitty
	logoIterm
)

var (
	logoDetectOnce sync.Once
	logoProtocol   logoProto
	logoBase64     string
	logoBase64Once sync.Once
)

// detectLogoProtocol decides — once, at first use — whether an inline-image
// protocol is available. Detection is environment-based only (no terminal
// round-trip queries) so it never blocks or interferes with the alt-screen TUI.
// The logo is hidden unless stdout is a real TTY and a supported protocol is
// detected, and always hidden inside tmux/screen to avoid a garbled header.
func detectLogoProtocol() logoProto {
	logoDetectOnce.Do(func() {
		logoProtocol = logoNone
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			return
		}
		if rasterm.IsTmuxScreen() {
			return
		}
		switch {
		case rasterm.IsKittyCapable():
			logoProtocol = logoKitty
		case rasterm.IsItermCapable():
			logoProtocol = logoIterm
		}
	})
	return logoProtocol
}

func pngBase64() string {
	logoBase64Once.Do(func() {
		logoBase64 = base64.StdEncoding.EncodeToString(invertocatPNG)
	})
	return logoBase64
}

// LogoAvailable reports whether the inline-image logo can be drawn (a supported
// protocol is detected on a real TTY outside tmux/screen). When false, the
// header shows no logo at all — there is no ASCII fallback.
func LogoAvailable() bool {
	return detectLogoProtocol() != logoNone
}

// ClearLogo returns an escape that removes a previously-drawn logo. kitty
// graphics live in a layer the text renderer cannot clear, so callers must emit
// this when they stop drawing the header (e.g. the header is hidden) to keep the
// image from lingering. It returns "" when there is nothing to clear (iTerm2
// images occupy text cells and are overwritten normally; no protocol => nothing).
func ClearLogo() string {
	if detectLogoProtocol() == logoKitty {
		return "\x1b_Ga=d,d=i,i=" + strconv.Itoa(headerLogoID) + ",q=2\x1b\\"
	}
	return ""
}

// renderHeaderLogo returns the inline-image escape for the embedded Invertocat,
// sized to about cols cells wide (and, for the square mark, ~cols/2 cells tall,
// since a terminal cell is roughly twice as tall as it is wide). Both protocols
// preserve the mark's aspect: iTerm2 fits it within the cols x rows box and
// kitty scales it to cols cells wide. The escape is wrapped in cursor
// save/restore so it never displaces the surrounding text. Returns "" when no
// inline-image protocol is available.
func renderHeaderLogo(cols, rows int) string {
	if cols < 1 || rows < 1 {
		return ""
	}
	switch detectLogoProtocol() {
	case logoKitty:
		return cursorSave + kittyPlaceLogo(cols) + cursorRestore
	case logoIterm:
		return cursorSave + itermPlaceLogo(cols, rows) + cursorRestore
	default:
		return ""
	}
}

// kittyPlaceLogo builds a kitty graphics escape that transmits the embedded PNG
// and displays it cols cells wide. Only c is sent (not r), so kitty derives the
// height from the mark's square aspect (about cols/2 cells) and never stretches
// it. C=1 keeps the cursor from moving and q=2 suppresses the terminal's
// responses (which would otherwise corrupt the TUI). The base64 payload is
// chunked per the protocol.
func kittyPlaceLogo(cols int) string {
	const chunkSize = 4096
	data := pngBase64()
	var sb strings.Builder
	first := true
	for {
		n := chunkSize
		if n > len(data) {
			n = len(data)
		}
		chunk := data[:n]
		data = data[n:]
		more := 0
		if len(data) > 0 {
			more = 1
		}
		sb.WriteString("\x1b_G")
		if first {
			sb.WriteString("f=100,a=T,i=")
			sb.WriteString(strconv.Itoa(headerLogoID))
			sb.WriteString(",q=2,C=1,c=")
			sb.WriteString(strconv.Itoa(cols))
			sb.WriteString(",m=")
			sb.WriteString(strconv.Itoa(more))
			first = false
		} else {
			sb.WriteString("m=")
			sb.WriteString(strconv.Itoa(more))
		}
		sb.WriteString(";")
		sb.WriteString(chunk)
		sb.WriteString("\x1b\\")
		if more == 0 {
			break
		}
	}
	return sb.String()
}

// itermPlaceLogo builds an iTerm2 inline-image escape (OSC 1337) sized to
// cols x rows cells with the aspect ratio preserved.
func itermPlaceLogo(cols, rows int) string {
	return "\x1b]1337;File=inline=1;preserveAspectRatio=1;width=" +
		strconv.Itoa(cols) + ";height=" + strconv.Itoa(rows) + ":" +
		pngBase64() + "\x07"
}
