package push

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a stand-in webhook: it captures every delivered request and lets
// a test wait for a known number of them without sleeping on a guess. Send is
// asynchronous by design, so every assertion here has to be gated on arrival
// rather than on wall-clock time.
type recorder struct {
	srv *httptest.Server

	mu   sync.Mutex
	got  []capture
	gate chan struct{}

	status int
	block  chan struct{} // when non-nil, handlers wait on it before replying
}

type capture struct {
	body   string
	header http.Header
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{gate: make(chan struct{}, 64), status: http.StatusOK}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, capture{body: string(body), header: req.Header.Clone()})
		block, status := r.block, r.status
		r.mu.Unlock()
		if block != nil {
			<-block
		}
		w.WriteHeader(status)
		r.gate <- struct{}{}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// wait blocks until n requests have been fully handled, or fails the test.
func (r *recorder) wait(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-r.gate:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for request %d of %d", i+1, n)
		}
	}
}

// quiet asserts no further request arrives within a short settle window. Used
// for the negative cases (debounced, unforwarded kind) where the only evidence
// is an absence.
func (r *recorder) quiet(t *testing.T) {
	t.Helper()
	select {
	case <-r.gate:
		t.Fatal("unexpected delivery")
	case <-time.After(150 * time.Millisecond):
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func (r *recorder) at(i int) capture {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[i]
}

// newBridge builds a bridge pointed at rec with a frozen, test-controlled clock.
func newBridge(rec *recorder, cfg Config) (*Bridge, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg.URL = rec.srv.URL
	b := New(cfg)
	b.now = clk.Now
	return b, clk
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestNewReturnsNilWithoutURL(t *testing.T) {
	for _, url := range []string{"", "   "} {
		if b := New(Config{URL: url}); b != nil {
			t.Fatalf("New(%q) = %v, want nil", url, b)
		}
	}
}

func TestNilBridgeSendIsNoOp(t *testing.T) {
	var b *Bridge
	b.Send(Event{Kind: KindAttention}) // must not panic
}

// The debounce is per (pane, kind): a flapping agent must collapse, but a
// different pane or a different kind is a genuinely different event.
func TestSendDebouncesPerPaneAndKind(t *testing.T) {
	rec := newRecorder(t)
	b, clk := newBridge(rec, Config{
		Kinds:       map[string]bool{KindAttention: true, KindFinished: true},
		MinInterval: time.Minute,
	})

	for range 5 {
		b.Send(Event{Kind: KindAttention, Pane: 1, Title: "claude needs attention", Pub: "w1:p1"})
	}
	rec.wait(t, 1)
	rec.quiet(t)
	if got := rec.count(); got != 1 {
		t.Fatalf("five identical attentions delivered %d times, want 1", got)
	}

	// Same pane, different kind ⇒ delivered.
	b.Send(Event{Kind: KindFinished, Pane: 1, Title: "claude finished", Pub: "w1:p1"})
	rec.wait(t, 1)

	// Different pane, same kind ⇒ delivered.
	b.Send(Event{Kind: KindAttention, Pane: 2, Title: "codex needs attention", Pub: "w1:p2"})
	rec.wait(t, 1)

	// Past the window, the original pane/kind is deliverable again.
	clk.advance(90 * time.Second)
	b.Send(Event{Kind: KindAttention, Pane: 1, Title: "claude needs attention", Pub: "w1:p1"})
	rec.wait(t, 1)

	if got := rec.count(); got != 4 {
		t.Fatalf("delivered %d, want 4", got)
	}
}

func TestSendSkipsUnforwardedKinds(t *testing.T) {
	rec := newRecorder(t)
	b, _ := newBridge(rec, Config{
		Kinds:       map[string]bool{KindAttention: true}, // "finished" deliberately absent
		MinInterval: time.Minute,
	})

	b.Send(Event{Kind: KindFinished, Pane: 1, Title: "claude finished"})
	rec.quiet(t)
	if got := rec.count(); got != 0 {
		t.Fatalf("unforwarded kind delivered %d times, want 0", got)
	}
}

func TestSendRendersNtfyRequest(t *testing.T) {
	rec := newRecorder(t)
	b, _ := newBridge(rec, Config{
		Token:       "sekrit",
		Kinds:       map[string]bool{KindAttention: true},
		Priority:    map[string]string{KindAttention: "high"},
		ClickURL:    "cats://pane/",
		MinInterval: time.Minute,
	})

	b.Send(Event{
		Kind:  KindAttention,
		Title: "claude needs attention",
		Body:  "cats · 1 · claude · cats",
		Pane:  7,
		Pub:   "w1:p3",
		Agent: "claude",
		Cwd:   "~/projs/go/cats",
	})
	rec.wait(t, 1)

	got := rec.at(0)
	for _, tc := range []struct{ header, want string }{
		{"Title", "claude needs attention"},
		{"Priority", "high"},
		{"Tags", "warning,claude"},
		{"Click", "cats://pane/w1:p3"},
		{"Authorization", "Bearer sekrit"},
	} {
		if v := got.header.Get(tc.header); v != tc.want {
			t.Errorf("header %s = %q, want %q", tc.header, v, tc.want)
		}
	}
	wantBody := "cats · 1 · claude · cats\n~/projs/go/cats"
	if got.body != wantBody {
		t.Errorf("body = %q, want %q", got.body, wantBody)
	}
}

func TestSendOmitsAuthorizationWithoutToken(t *testing.T) {
	rec := newRecorder(t)
	b, _ := newBridge(rec, Config{Kinds: map[string]bool{KindAttention: true}})

	b.Send(Event{Kind: KindAttention, Title: "claude needs attention", Pub: "w1:p1"})
	rec.wait(t, 1)

	if v := rec.at(0).header.Get("Authorization"); v != "" {
		t.Errorf("Authorization = %q, want empty", v)
	}
}

// A body is never empty (ntfy rejects that), and a title full of terminal
// output never breaks the header.
func TestSendSanitizesTerminalDerivedText(t *testing.T) {
	rec := newRecorder(t)
	b, _ := newBridge(rec, Config{Kinds: map[string]bool{KindAttention: true}})

	b.Send(Event{Kind: KindAttention, Title: "claude\r\nInjected: yes", Pub: "w1:p1"})
	rec.wait(t, 1)

	got := rec.at(0)
	if title := got.header.Get("Title"); strings.ContainsAny(title, "\r\n") {
		t.Errorf("Title %q still carries a newline", title)
	}
	if got.header.Get("Injected") != "" {
		t.Error("a newline in the title smuggled in a header")
	}
	if got.body != "w1:p1" {
		t.Errorf("body = %q, want the handle as filler", got.body)
	}
}

// The loop must never wait on the network: Send has to return promptly even
// while the endpoint is holding every request open.
func TestSendNeverBlocks(t *testing.T) {
	rec := newRecorder(t)
	block := make(chan struct{})
	rec.mu.Lock()
	rec.block = block
	rec.mu.Unlock()

	b, _ := newBridge(rec, Config{Kinds: map[string]bool{KindAttention: true}})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20 {
			b.Send(Event{Kind: KindAttention, Pane: uint32(i), Pub: "w1:p1"})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Send blocked on a stalled endpoint")
	}
	close(block)
}

func TestClampNeverSplitsARune(t *testing.T) {
	// "你好" is two 3-byte runes; clamping to 4 must drop the second entirely.
	got := clamp([]byte("你好"), 4)
	if string(got) != "你" {
		t.Fatalf("clamp = %q, want %q", got, "你")
	}
	if got := clamp([]byte("abc"), 10); string(got) != "abc" {
		t.Fatalf("clamp of a short value = %q, want unchanged", got)
	}
}

func TestRenderTagsSkipsCommaBearingAgent(t *testing.T) {
	if got := renderTags(Event{Kind: KindAttention, Agent: "a,b"}); got != "warning" {
		t.Fatalf("renderTags = %q, want just the kind tag", got)
	}
	if got := renderTags(Event{Kind: KindFinished, Agent: "codex"}); got != "white_check_mark,codex" {
		t.Fatalf("renderTags = %q", got)
	}
}

// The Actions header is ntfy's, and its shape is the whole contract with the
// phone: every label quoted (agent menu text is full of commas), POST, and
// clear=true so pressed buttons leave the lock screen instead of inviting the
// second tap catway will refuse.
func TestRenderActions(t *testing.T) {
	got := renderActions([]Action{
		{Label: "Yes", URL: "https://cats.example/api/notify-action/aaa"},
		{Label: "Yes, and don't ask again", URL: "https://cats.example/api/notify-action/bbb"},
	})
	want := `http, "Yes", https://cats.example/api/notify-action/aaa, method=POST, clear=true; ` +
		`http, "Yes, and don't ask again", https://cats.example/api/notify-action/bbb, method=POST, clear=true`
	if got != want {
		t.Errorf("renderActions =\n %s\nwant\n %s", got, want)
	}
}

// Every way a label or a list can be hostile to the header format.
func TestRenderActionsSanitizes(t *testing.T) {
	cases := []struct {
		name string
		in   []Action
		want string
	}{
		{"no actions", nil, ""},
		{"an action with no url is skipped", []Action{{Label: "X"}}, ""},
		{
			// A quote would end the label early and turn the rest into
			// positional parameters ntfy does not understand.
			name: "quotes and semicolons in the label",
			in:   []Action{{Label: `Say "no"; then stop`, URL: "https://x/a"}},
			want: `http, "Say 'no', then stop", https://x/a, method=POST, clear=true`,
		},
		{
			// Terminal-derived text can carry newlines, and an HTTP header
			// cannot.
			name: "newlines in the label",
			in:   []Action{{Label: "Yes\nplease", URL: "https://x/a"}},
			want: `http, "Yes please", https://x/a, method=POST, clear=true`,
		},
		{
			name: "an empty label still names its button",
			in:   []Action{{Label: "   ", URL: "https://x/a"}},
			want: `http, "OK", https://x/a, method=POST, clear=true`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderActions(c.in); got != c.want {
				t.Errorf("renderActions = %q, want %q", got, c.want)
			}
		})
	}

	// A fourth button is not rendered by ntfy, so sending one would be a choice
	// the user is silently denied — it is dropped here where that is visible.
	four := make([]Action, 4)
	for i := range four {
		four[i] = Action{Label: "b", URL: "https://x/a"}
	}
	if n := strings.Count(renderActions(four), "http, "); n != maxActions {
		t.Errorf("rendered %d actions, want %d", n, maxActions)
	}
}

// Actions ride the real delivery, and an event without them sets no header at
// all — the bridge's behaviour for every notification before this existed.
func TestSendCarriesActionsHeader(t *testing.T) {
	rec := newRecorder(t)
	b := New(Config{URL: rec.srv.URL, Kinds: map[string]bool{KindAttention: true}})

	b.Send(Event{Kind: KindAttention, Pane: 1, Title: "claude needs attention", Pub: "w1:p1",
		Actions: []Action{{Label: "Yes", URL: "https://cats.example/api/notify-action/tok"}}})
	rec.wait(t, 1)
	if got := rec.at(rec.count() - 1).header.Get("Actions"); !strings.Contains(got, "notify-action/tok") {
		t.Errorf("Actions header = %q", got)
	}

	b.Send(Event{Kind: KindAttention, Pane: 2, Title: "codex needs attention", Pub: "w1:p2"})
	rec.wait(t, 1)
	if got := rec.at(rec.count() - 1).header.Get("Actions"); got != "" {
		t.Errorf("a notification with no actions set Actions = %q", got)
	}
}
