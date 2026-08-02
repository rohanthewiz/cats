package main

// `catctl pair` — put a device-pairing grant on screen for a phone camera.
//
// The grant itself is minted server-side (cmd/catway/pair.go); everything here
// is presentation. That split matters: the CLI never sees the shared password,
// only a token that is single-use and expires in minutes, so the code below is
// free to print it, and a shell that logs its output has logged something worth
// nothing by the time anyone reads the log back.
//
// The deep-link URI is composed here rather than served by catway because the
// URI scheme belongs to the client, not the server: catway has no business
// knowing what the mobile app registered with the OS.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/qr"
)

// pairScheme is the deep link the cats mobile client registers. Changing it
// breaks every installed app, so it is a constant with a name rather than an
// inline string.
const pairScheme = "cats://pair"

// runPair fetches a pairing grant and renders it. rawJSON prints the control
// response verbatim instead, which is the scripting path (and the only one that
// is stable to parse).
func runPair(socket, id string, timeout time.Duration, rawJSON bool) int {
	resp, err := ctlproto.Call(socket, ctlproto.Request{ID: id, Method: ctlproto.MethodPair}, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "catctl: %v\n", err)
		return 2
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		return 1
	}
	if rawJSON {
		b, _ := json.Marshal(resp)
		fmt.Println(string(b))
		return 0
	}

	var info ctlproto.PairInfo
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		fmt.Fprintf(os.Stderr, "catctl: unreadable pair response: %v\n", err)
		return 2
	}
	fmt.Print(renderPairing(info, time.Now(), stdoutIsTerminal()))
	return 0
}

// pairURI builds the deep link a device opens to redeem the grant. The
// fingerprint rides along so the app can pin a self-signed certificate at the
// moment it first learns the address, rather than trusting whatever it is handed
// on a later connection.
func pairURI(info ctlproto.PairInfo) string {
	q := url.Values{}
	q.Set("u", info.URL)
	q.Set("t", info.Token)
	if info.Fingerprint != "" {
		q.Set("f", info.Fingerprint)
	}
	return pairScheme + "?" + q.Encode()
}

// renderPairing formats the whole block: the scannable code, the link behind it,
// and the fields a human might want to check by eye. now is passed in so the
// "expires in" line is testable.
//
// A code that will not encode is not an error — the URI below it is the actual
// payload, and the QR is a convenience. Saying so beats printing nothing and
// leaving the operator wondering whether pairing worked.
func renderPairing(info ctlproto.PairInfo, now time.Time, color bool) string {
	var b strings.Builder
	uri := pairURI(info)

	b.WriteByte('\n')
	if code, err := qr.Encode(uri); err == nil {
		b.WriteString(code.Terminal(qr.TerminalOptions{Color: color, Indent: "  "}))
		b.WriteByte('\n')
		b.WriteString("  Scan with the cats mobile app, or open this link on the device:\n\n")
	} else {
		b.WriteString("  Open this link on the device (too long to render as a code):\n\n")
	}
	fmt.Fprintf(&b, "    %s\n\n", uri)

	fmt.Fprintf(&b, "  Server        %s\n", info.URL)
	if info.Fingerprint != "" {
		fmt.Fprintf(&b, "  Cert SHA-256  %s\n", info.Fingerprint)
	}
	fmt.Fprintf(&b, "  Token         %s\n", info.Token)
	fmt.Fprintf(&b, "  Expires       %s — single use\n", expiresIn(info.ExpiresAt, now))
	b.WriteString("\n  This is a one-time grant, not the access password: redeeming it\n" +
		"  yields a session credential that a server restart revokes.\n\n")
	return b.String()
}

// expiresIn renders the remaining life of a grant. An already-expired token is
// reported plainly rather than as a negative duration — it means the operator's
// clock and the server's disagree, or they left the terminal for five minutes,
// and either way the answer is to run the command again.
func expiresIn(unix int64, now time.Time) string {
	d := time.Unix(unix, 0).Sub(now).Round(time.Second)
	if d <= 0 {
		return "already expired — run `catctl pair` again"
	}
	return "in " + d.String()
}

// stdoutIsTerminal reports whether colour escapes are worth emitting. Piped
// output falls back to the uncoloured render (see qr.Terminal), and NO_COLOR is
// honoured because a code drawn in explicit black and white is still the more
// intrusive of the two renders.
func stdoutIsTerminal() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
