// Package push is catway's outbound notification bridge: it turns an agent
// notification into an HTTP POST at an ntfy-shaped webhook, so a phone with its
// screen off gets a real system push rather than a toast on a screen nobody is
// looking at.
//
// This is the out-of-band half of the notification story. The in-band half —
// the browserproto notify broadcast — only reaches clients that are connected
// right now, which by definition excludes the case the bridge exists for: you
// are away from the desk and an agent just hit a permission prompt. Because the
// POST is an ordinary outbound request from the machine catway runs on, it also
// keeps working when no client is connected at all, and (if a relay is ever put
// in front of catway) when that relay is down.
//
// Two properties are the whole design.
//
// It never blocks its caller. Send does a debounce check under a small mutex
// and hands the request to a goroutine, so catway's single-threaded
// orchestrator loop pays one map lookup and nothing else. A webhook that hangs
// for its full timeout must not stall the terminal.
//
// It never calls back. Unlike the usage poller (cmd/catway/usage.go), which
// posts its result onto the loop, a push has no result the session needs — so
// there is no post half here, and nothing in this package knows what an
// orchestrator is. That keeps the package untagged (no libghostty), so it and
// its tests build with a plain `go test`.
//
// The credential rides one Authorization header. It is never logged, never
// broadcast, and never reaches a client — the same discipline usage.go keeps
// with claude's OAuth token.
//
// House style (matching the rest of the repo): stdlib errors/fmt and prefixed
// log messages, no serr.
package push

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// postTimeout bounds one delivery. Mirrors usageTimeout: the request is
	// already off the loop, but a hung dial should not pin a goroutine
	// indefinitely, and a notification that arrives a minute late has missed
	// the point anyway.
	postTimeout = 10 * time.Second

	// maxBodyBytes clamps the rendered message. ntfy and Pushover both cap the
	// body server-side; truncating here keeps a runaway agent title (a pane
	// title can be arbitrary terminal output) from turning every push into a
	// rejected request.
	maxBodyBytes = 2048
)

// Kind values, mirroring browserproto notify kinds. Duplicated as plain strings
// rather than imported so this package stays free of the wire types — the
// caller translates.
const (
	KindAttention = "attention"
	KindFinished  = "finished"
	// KindInfo is ui.notify's default. It is never in the default Kinds set —
	// see config.PushKindInfo — so reaching a phone with one is an explicit
	// operator choice.
	KindInfo = "info"
)

// Event is one notification to deliver, already resolved against the pane's
// runtime context. The caller owns that lookup so this package stays free of
// the session model.
type Event struct {
	Kind  string // KindAttention | KindFinished | KindInfo
	Title string // e.g. "claude needs attention"
	Body  string // the workspace/tab context line
	Pane  uint32
	Pub   string // public handle "w1:p3" — the stable id a deep link should carry
	Agent string
	Cwd   string
}

// Config is the resolved push configuration. Token is separate from the rest
// because it comes from the environment, never the config file — see the
// config.Push doc comment for why that distinction is load-bearing here.
type Config struct {
	URL   string
	Token string
	// Kinds is the set of notify kinds forwarded. A kind absent from the map is
	// dropped before any work happens.
	Kinds map[string]bool
	// Priority maps a kind onto the endpoint's priority header value.
	Priority map[string]string
	// ClickURL is the deep-link base a notification tap opens; the event's
	// public handle is appended. Empty ⇒ no click action.
	ClickURL string
	// MinInterval debounces deliveries per (pane, kind).
	MinInterval time.Duration
}

// dedupeKey is the debounce granularity: one pane's transitions into one kind.
// Deliberately not keyed on the message, so a pane whose title changes between
// two blocks still debounces as one agent flapping rather than two events.
type dedupeKey struct {
	pane uint32
	kind string
}

// Bridge delivers events to a webhook. The zero value is not usable; New builds
// one, or returns nil when there is nothing configured. Every method is
// nil-safe so the caller can hold a possibly-nil *Bridge and never branch.
type Bridge struct {
	cfg    Config
	client *http.Client

	mu     sync.Mutex
	last   map[dedupeKey]time.Time
	logged map[string]bool

	// now is injectable so the debounce is testable without sleeping.
	now func() time.Time
}

// New builds a bridge for cfg, or returns nil when no URL is configured. A nil
// *Bridge is a working no-op, so callers assign the result unconditionally.
func New(cfg Config) *Bridge {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil
	}
	if cfg.Kinds == nil {
		cfg.Kinds = map[string]bool{KindAttention: true}
	}
	return &Bridge{
		cfg:    cfg,
		client: &http.Client{Timeout: postTimeout},
		last:   map[dedupeKey]time.Time{},
		logged: map[string]bool{},
		now:    time.Now,
	}
}

// Send delivers ev, unless its kind is not forwarded or an identical
// (pane, kind) went out within MinInterval. It returns immediately: the HTTP
// round trip happens on its own goroutine under a timeout.
//
// The debounce is the difference between a useful bridge and one its owner
// silences. An agent that flaps working↔blocked while a tool retries would
// otherwise vibrate the phone every few seconds; one push per minute per pane
// still tells you everything you need to act on.
func (b *Bridge) Send(ev Event) {
	if b == nil || !b.cfg.Kinds[ev.Kind] {
		return
	}
	b.mu.Lock()
	key := dedupeKey{pane: ev.Pane, kind: ev.Kind}
	now := b.now()
	if last, seen := b.last[key]; seen && now.Sub(last) < b.cfg.MinInterval {
		b.mu.Unlock()
		return
	}
	b.last[key] = now
	b.mu.Unlock()

	go b.post(ev)
}

// post performs one delivery. Errors are reported through logOnce rather than
// returned: nothing upstream can act on a failed push, and a permanently
// misconfigured webhook must not narrate itself into the log on every agent
// transition.
func (b *Bridge) post(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), postTimeout)
	defer cancel()

	body := clamp(renderBody(ev), maxBodyBytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.URL, bytes.NewReader(body))
	if err != nil {
		b.logOnce(fmt.Sprintf("bad url: %v", err))
		return
	}
	b.applyHeaders(req, ev)

	resp, err := b.client.Do(req)
	if err != nil {
		b.logOnce(fmt.Sprintf("post: %v", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b.logOnce(fmt.Sprintf("status %d", resp.StatusCode))
		return
	}
	// A success clears the log-once ledger, so a webhook that recovers and then
	// breaks again reports the second outage instead of staying silent forever.
	b.mu.Lock()
	clear(b.logged)
	b.mu.Unlock()
}

// applyHeaders sets ntfy's header API. Pushover accepts the same POST with its
// own field names; operators pointing at Pushover use a topic URL that encodes
// their key, which is why nothing here is ntfy-specific beyond the header names.
func (b *Bridge) applyHeaders(req *http.Request, ev Event) {
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if b.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.Token)
	}
	if ev.Title != "" {
		req.Header.Set("Title", clampHeader(ev.Title))
	}
	if p := b.cfg.Priority[ev.Kind]; p != "" {
		req.Header.Set("Priority", p)
	}
	if tags := renderTags(ev); tags != "" {
		req.Header.Set("Tags", tags)
	}
	// The public handle, not the raw pane id: it is the stable, human-meaningful
	// address ("w1:p3") that the app's router and `catctl` both already speak.
	if b.cfg.ClickURL != "" && ev.Pub != "" {
		req.Header.Set("Click", b.cfg.ClickURL+ev.Pub)
	}
}

// renderBody is what a lock screen shows under the title. Two lines: the
// workspace/tab context the toast already carries, then the pane's cwd — which
// is usually the fastest way to tell two identically-named agents apart.
func renderBody(ev Event) []byte {
	var sb strings.Builder
	if ev.Body != "" {
		sb.WriteString(ev.Body)
	}
	if ev.Cwd != "" {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(ev.Cwd)
	}
	if sb.Len() == 0 {
		// ntfy rejects an empty body; the handle is the least useless filler and
		// still tells you which pane to open.
		sb.WriteString(ev.Pub)
	}
	return []byte(sb.String())
}

// renderTags picks the emoji ntfy renders beside the title, plus the agent name
// as a filter handle so a phone can mute one agent without muting the topic.
func renderTags(ev Event) string {
	tags := make([]string, 0, 2)
	switch ev.Kind {
	case KindAttention:
		tags = append(tags, "warning")
	case KindFinished:
		tags = append(tags, "white_check_mark")
	case KindInfo:
		tags = append(tags, "bell")
	}
	// ntfy treats an unknown tag as a literal label, which is exactly what we
	// want for the agent name — but a tag containing a comma would split into
	// two, so agents with odd names are skipped rather than mangled.
	if ev.Agent != "" && !strings.ContainsAny(ev.Agent, ",\r\n") {
		tags = append(tags, ev.Agent)
	}
	return strings.Join(tags, ",")
}

// logOnce reports a persistent delivery failure exactly once per reason,
// following usage.go's logUsageOnce: a webhook that is wrong stays wrong, and
// an agent-heavy session would otherwise fill the log with identical lines.
func (b *Bridge) logOnce(reason string) {
	if reason == "" {
		return
	}
	b.mu.Lock()
	seen := b.logged[reason]
	b.logged[reason] = true
	b.mu.Unlock()
	if !seen {
		log.Printf("catway: push notification failed (%s)", reason)
	}
}

// clamp truncates a body to n bytes on a rune boundary, so a very long pane
// title cannot produce an oversized (and therefore rejected) request.
func clamp(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	// Back off to the last byte that does not begin a UTF-8 continuation, so the
	// truncation never splits a rune.
	cut := n
	for cut > 0 && b[cut]&0xC0 == 0x80 {
		cut--
	}
	return b[:cut]
}

// clampHeader makes a value safe for an HTTP header: header fields cannot carry
// newlines, and terminal-derived text (a pane title is whatever the foreground
// app set) very much can.
func clampHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = string(clamp([]byte(s), 200))
	}
	return strings.TrimSpace(s)
}
