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
	"text/tabwriter"
	"time"

	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/peersync"
	"github.com/rohanthewiz/cats/internal/qr"
)

// pairScheme is the deep link the cats mobile client registers. Changing it
// breaks every installed app, so it is a constant with a name rather than an
// inline string.
const pairScheme = "cats://pair"

// runPair fetches a pairing grant and renders it. rawJSON prints the control
// response verbatim instead, which is the scripting path (and the only one that
// is stable to parse).
//
// args are the operands after `pair`: none for a device grant, or `peer
// [label...]` for a peer-sync grant another catway redeems.
func runPair(socket, id string, timeout time.Duration, rawJSON bool, args []string) int {
	params, err := pairParams(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "catctl: %v\n", err)
		return 2
	}
	resp, err := ctlproto.Call(socket, ctlproto.Request{ID: id, Method: ctlproto.MethodPair, Params: params}, timeout)
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
	if info.Kind == ctlproto.PairKindPeer {
		fmt.Print(renderPeerPairing(info, time.Now()))
		return 0
	}
	fmt.Print(renderPairing(info, time.Now(), stdoutIsTerminal()))
	return 0
}

// pairParams turns `pair`'s operands into its params. nil for a device grant,
// so the request is byte-for-byte what `catctl pair` has always sent.
func pairParams(args []string) (json.RawMessage, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if args[0] != "peer" {
		return nil, fmt.Errorf("pair: unexpected %q (usage: catctl pair [peer [label...]])", args[0])
	}
	b, err := json.Marshal(ctlproto.PairParams{Peer: true, Label: strings.Join(args[1:], " ")})
	return b, err
}

// renderPeerPairing prints a peer grant as the command to run on the other
// machine, ready to paste. No QR: the reader is a person at another terminal,
// not a phone camera, and a link is what a terminal can carry.
//
// The link is single-quoted in the suggested command because it contains '&':
// unquoted, a shell cuts it at the first one and runs the rest as a
// background job, and the attach fails with a missing token.
func renderPeerPairing(info ctlproto.PairInfo, now time.Time) string {
	var b strings.Builder
	link := peersync.PeerLink(info.URL, info.Token, info.Fingerprint)
	b.WriteString("\n  On the other machine, attach this catway as a peer (pick any <id>):\n\n")
	fmt.Fprintf(&b, "    catctl attach-peer <id> '%s'\n\n", link)
	fmt.Fprintf(&b, "  Server        %s\n", info.URL)
	if info.Fingerprint != "" {
		fmt.Fprintf(&b, "  Cert SHA-256  %s\n", info.Fingerprint)
	}
	fmt.Fprintf(&b, "  Expires       %s — single use\n", expiresIn(info.ExpiresAt, now))
	b.WriteString("\n  The link is not the access password. Redeeming it gives that catway a\n" +
		"  peer-sync grant: it survives restarts, opens only the /peer/v1 routes (no\n" +
		"  terminals), and `catctl peer-grants` / `revoke-peer-grant` manage it here.\n\n")
	return b.String()
}

// printPeerGrants renders peer.grants as a table. Times are local and to the
// minute; the exact Unix seconds are in --json for anyone who needs them.
func printPeerGrants(resp ctlproto.Response) {
	var list ctlproto.PeerGrantList
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		fmt.Fprintf(os.Stderr, "catctl: unreadable peer.grants response: %v\n", err)
		return
	}
	fmt.Print(renderPeerGrants(list))
}

func renderPeerGrants(list ctlproto.PeerGrantList) string {
	if len(list.Grants) == 0 {
		return "no peer grants issued (catctl pair peer mints one)\n"
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tPEER\tCREATED\tLAST USED")
	for _, g := range list.Grants {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", g.ID, dash(g.Label), dash(g.Peer), stamp(g.Created), stamp(g.LastUsed))
	}
	_ = tw.Flush()
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func stamp(unix int64) string {
	if unix == 0 {
		return "never"
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
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
