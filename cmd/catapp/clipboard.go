//go:build darwin

package main

import (
	"log"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/rohanthewiz/cats/internal/clipboard"
)

// Native pasteboard access for the webview page. WKWebView restricts
// navigator.clipboard — reads resolve empty and writes demand a user
// activation that WebSocket-driven copies (OSC 52 from a pane, §7 reads)
// never have — so catapp binds these into the page (see newWindow) and the
// UI prefers them over the browser API when present. pbcopy/pbpaste are
// used instead of NSPasteboard to keep this file cgo-free.
//
// Both run with the locale pinned to UTF-8 (clipboard.UTF8Env). Cats.app is
// started by launchd, which sets no LANG, and without one pbcopy/pbpaste
// transcode through MacRoman. That broke both directions for any non-ASCII
// text — smart quotes, em dashes, accents, emoji, i.e. most text copied from
// a browser, Slack or a doc:
//
//	read   pasteboard ─pbpaste─▶ MacRoman bytes ─▶ [NSString stringWithUTF8String:]
//	                                               returns nil on invalid UTF-8
//	                                               ─▶ page gets null ─▶ "clipboard has no text"
//	write  pane "“é”" ─pbcopy─▶ UTF-8 bytes read as MacRoman ─▶ pasteboard holds "‚Äú√©‚Äù"

func clipboardWrite(text string) error {
	cmd := exec.Command("/usr/bin/pbcopy")
	cmd.Env = clipboard.UTF8Env()
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func clipboardRead() (string, error) {
	cmd := exec.Command("/usr/bin/pbpaste")
	cmd.Env = clipboard.UTF8Env()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return validUTF8(string(out)), nil
}

// validUTF8 guarantees the read hands the ObjC bridge valid UTF-8, whatever
// pbpaste produced. The bridge converts with [NSString stringWithUTF8String:],
// which returns nil for a single bad byte, and the page then reports "clipboard
// has no text" — the wrong diagnosis, for text that is plainly on the
// clipboard. With the locale pinned this should never trigger; if some future
// pbpaste or locale quirk does produce bad bytes, pasting the text with a
// U+FFFD where a character was lost beats refusing all of it, and the log line
// says why.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	log.Printf("clipboard read: pbpaste returned invalid UTF-8 (%d bytes); replacing the bad sequences", len(s))
	return strings.ToValidUTF8(s, "�")
}
