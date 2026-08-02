//go:build ghostty

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// --- The account read ---------------------------------------------------------

func TestFetchAccountUsageParsesWindows(t *testing.T) {
	var gotAuth, gotBeta, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotUA = r.Header.Get("User-Agent")
		fmt.Fprint(w, `{
			"five_hour":   {"utilization": 42.5, "resets_at": "2026-07-31T20:00:00Z"},
			"seven_day":   {"utilization": 7,    "resets_at": "2026-08-04T00:00:00Z"},
			"seven_day_opus": null
		}`)
	}))
	defer srv.Close()

	got, err := fetchAccountUsage(context.Background(), srv.Client(), srv.URL, "tok-abc")
	if err != nil {
		t.Fatalf("fetchAccountUsage: %v", err)
	}
	if got.fiveHour.Pct != 42.5 || got.fiveHour.ResetsAt != "2026-07-31T20:00:00Z" {
		t.Errorf("five hour = %+v", got.fiveHour)
	}
	if got.weekly.Pct != 7 || got.weekly.ResetsAt != "2026-08-04T00:00:00Z" {
		t.Errorf("weekly = %+v", got.weekly)
	}
	// The three headers are load-bearing: without the beta the endpoint refuses
	// the credential, and without the claude-code UA the request is served from
	// a far tighter rate-limit bucket.
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBeta != usageOAuthBeta {
		t.Errorf("anthropic-beta = %q, want %q", gotBeta, usageOAuthBeta)
	}
	if !strings.HasPrefix(gotUA, "claude-code/") {
		t.Errorf("User-Agent = %q, want a claude-code/… UA", gotUA)
	}
}

func TestFetchAccountUsageErrorsStaySilentAboutTheCredential(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"bad token tok-secret"}`},
		{"garbage", http.StatusOK, `not json`},
		{"no windows", http.StatusOK, `{"extra_usage":{"is_enabled":false}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			_, err := fetchAccountUsage(context.Background(), srv.Client(), srv.URL, "tok-secret")
			if err == nil {
				t.Fatal("want an error")
			}
			// The message is rendered into the sidebar, so it must carry neither
			// the credential nor the endpoint's own response text.
			if strings.Contains(err.Error(), "tok-secret") {
				t.Errorf("error leaks the credential: %v", err)
			}
		})
	}
}

// The per-model week comes out of the limits array, not the named
// seven_day_<model> fields — those come back null on accounts that do have one.
func TestFetchAccountUsageReadsScopedWeeklyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"five_hour": {"utilization": 1, "resets_at": "2026-08-01T18:00:00Z"},
			"seven_day": {"utilization": 24, "resets_at": "2026-08-05T21:00:00Z"},
			"seven_day_fable": null,
			"limits": [
				{"kind": "session",     "percent": 1,  "resets_at": "2026-08-01T18:00:00Z"},
				{"kind": "weekly_all",  "percent": 24, "resets_at": "2026-08-05T21:00:00Z"},
				{"kind": "weekly_scoped", "percent": 12, "resets_at": "2026-08-05T21:00:00Z",
					"scope": {"model": {"id": null, "display_name": "Opus"}}},
				{"kind": "weekly_scoped", "percent": 29, "resets_at": "2026-08-05T21:00:01Z",
					"scope": {"model": {"id": null, "display_name": "Fable"}}},
				{"kind": "weekly_scoped", "percent": 99, "resets_at": "2026-08-05T21:00:00Z",
					"scope": {"surface": "cowork"}}
			]
		}`)
	}))
	defer srv.Close()

	got, err := fetchAccountUsage(context.Background(), srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("fetchAccountUsage: %v", err)
	}
	// Fable over Opus because it is nearer its ceiling, and over the
	// surface-scoped 99% because that one has no model to label a row with.
	if got.weeklyModelName != "Fable" {
		t.Errorf("weekly model name = %q, want Fable", got.weeklyModelName)
	}
	if got.weeklyModel.Pct != 29 || got.weeklyModel.ResetsAt != "2026-08-05T21:00:01Z" {
		t.Errorf("weekly model window = %+v", got.weeklyModel)
	}
	// The plan-wide windows still come from the top-level fields.
	if got.weekly.Pct != 24 {
		t.Errorf("weekly = %+v", got.weekly)
	}
}

// A plan with no separately metered model reports no scoped limit, and the
// sidebar must be able to tell that from a window that read as zero.
func TestFetchAccountUsageWithoutScopedWeeklyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"five_hour": {"utilization": 1},
			"seven_day": {"utilization": 2},
			"limits": [{"kind": "weekly_all", "percent": 2}]
		}`)
	}))
	defer srv.Close()

	got, err := fetchAccountUsage(context.Background(), srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("fetchAccountUsage: %v", err)
	}
	if got.weeklyModelName != "" {
		t.Errorf("weekly model name = %q, want empty", got.weeklyModelName)
	}
	if got.weeklyModel.Pct != browserproto.UsagePctUnknown {
		t.Errorf("weekly model pct = %v, want unknown", got.weeklyModel.Pct)
	}
	// And it must not reach the browser as a row at all.
	msg := browserproto.NewUsage("account", got.fiveHour, got.weekly, "").
		WithWeeklyModel(got.weeklyModelName, got.weeklyModel)
	if msg.WeeklyModelName != "" {
		t.Errorf("published a nameless per-model row: %+v", msg)
	}
}

// A 200 whose windows are all zero is an account that has spent nothing, not a
// broken read — it must not fall through to the estimate.
func TestFetchAccountUsageAcceptsZeroedWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"five_hour":{"utilization":0,"resets_at":""},"seven_day":{"utilization":0}}`)
	}))
	defer srv.Close()

	got, err := fetchAccountUsage(context.Background(), srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("fetchAccountUsage: %v", err)
	}
	if got.fiveHour.Pct != 0 || got.weekly.Pct != 0 {
		t.Errorf("got %+v", got)
	}
}

// --- The credential -----------------------------------------------------------

func TestParseClaudeCreds(t *testing.T) {
	future := time.Now().Add(time.Hour).UnixMilli()
	past := time.Now().Add(-time.Hour).UnixMilli()

	tok, err := parseClaudeCreds([]byte(fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"sk-live","expiresAt":%d}}`, future)))
	if err != nil || tok != "sk-live" {
		t.Errorf("live credential: tok=%q err=%v", tok, err)
	}

	// No expiry recorded is treated as usable — the endpoint is the authority
	// on that, and refusing locally would be a guess.
	if tok, err := parseClaudeCreds([]byte(`{"claudeAiOauth":{"accessToken":"sk-live"}}`)); err != nil || tok != "sk-live" {
		t.Errorf("undated credential: tok=%q err=%v", tok, err)
	}

	// An expired token is named as expired rather than sent to be rejected: the
	// sidebar can then say "claude login expired" instead of "HTTP 401".
	if _, err := parseClaudeCreds([]byte(fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"sk-old","expiresAt":%d}}`, past))); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Errorf("expired credential: err=%v", err)
	}

	for _, raw := range []string{`{}`, `{"claudeAiOauth":{}}`, `nonsense`} {
		if _, err := parseClaudeCreds([]byte(raw)); err == nil {
			t.Errorf("%s: want an error", raw)
		}
	}
}

// On macOS more than one keychain item shares claude's service name — the login
// credential sits under the account's username, and a sibling under account
// "unknown" holds the OAuth tokens claude's MCP plugins collected. Both parse
// as JSON, so a store that yields no claudeAiOauth has to be skipped rather
// than accepted as the answer.
func TestFirstClaudeTokenSkipsForeignCredentialStores(t *testing.T) {
	mcpStore := `{"mcpOAuth":{"plugin:1234:GitHub|abcd":{"accessToken":"gho_notours"}}}`
	live := `{"claudeAiOauth":{"accessToken":"sk-live"}}`

	store := func(body string, err error) func() ([]byte, error) {
		return func() ([]byte, error) { return []byte(body), err }
	}

	got, err := firstClaudeToken([]func() ([]byte, error){
		store("", os.ErrNotExist), // no credentials file
		store(mcpStore, nil),      // the plugin item, first out of the keychain
		store(live, nil),          // claude's own
	})
	if err != nil || got != "sk-live" {
		t.Fatalf("got %q, %v — want the claudeAiOauth store", got, err)
	}

	// Nothing anywhere is "no credential", not a parse failure.
	if _, err := firstClaudeToken([]func() ([]byte, error){store(mcpStore, nil)}); !errors.Is(err, errNoCreds) {
		t.Errorf("err = %v, want errNoCreds", err)
	}

	// An expired credential must not be flattened into "no credential" by a
	// later store that simply has nothing — the reason is the useful part.
	expiredAt := time.Now().Add(-time.Hour).UnixMilli()
	_, err = firstClaudeToken([]func() ([]byte, error){
		store(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"sk-old","expiresAt":%d}}`, expiredAt), nil),
		store(mcpStore, nil),
	})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %v, want the expiry reason to survive", err)
	}
}

// --- The local fallback -------------------------------------------------------

// usageLine builds one assistant transcript record with the given id, age and
// token split.
func usageLine(id string, age time.Duration, in, out, cacheWrite, cacheRead int64) string {
	rec := map[string]any{
		"type":      "assistant",
		"timestamp": time.Now().Add(-age).UTC().Format(time.RFC3339),
		"message": map[string]any{
			"id": id,
			"usage": map[string]any{
				"input_tokens":                in,
				"output_tokens":               out,
				"cache_creation_input_tokens": cacheWrite,
				"cache_read_input_tokens":     cacheRead,
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUsageEstimatorSumsTheTwoWindows(t *testing.T) {
	projects := t.TempDir()
	writeLines(t, filepath.Join(projects, "proj", "a.jsonl"),
		usageLine("msg_recent", time.Hour, 1, 2, 3, 4),         // inside both windows
		usageLine("msg_older", 30*time.Hour, 0, 0, 0, 100),     // inside the week only
		usageLine("msg_ancient", 9*24*time.Hour, 0, 0, 0, 999), // outside both
	)
	e := newUsageEstimator(projects)
	fiveHour, weekly := e.windows(time.Now())

	if fiveHour.Pct != browserproto.UsagePctUnknown || weekly.Pct != browserproto.UsagePctUnknown {
		t.Errorf("an estimate has no denominator; got %v / %v", fiveHour.Pct, weekly.Pct)
	}
	if fiveHour.Detail != "10 tok" {
		t.Errorf("five hour = %q, want the 1+2+3+4 record only", fiveHour.Detail)
	}
	if weekly.Detail != "110 tok" {
		t.Errorf("weekly = %q, want the two in-week records", weekly.Detail)
	}
}

// A resumed or forked session writes a new transcript carrying the earlier
// turns. Counting those again would inflate the windows by however many times
// the user has resumed, so the same assistant message id counts once.
func TestUsageEstimatorDedupesResumedTranscripts(t *testing.T) {
	projects := t.TempDir()
	shared := usageLine("msg_shared", time.Hour, 0, 0, 0, 50)
	writeLines(t, filepath.Join(projects, "proj", "a.jsonl"), shared)
	writeLines(t, filepath.Join(projects, "proj", "b.jsonl"), shared,
		usageLine("msg_new", time.Hour, 0, 0, 0, 7))

	e := newUsageEstimator(projects)
	fiveHour, _ := e.windows(time.Now())
	if fiveHour.Detail != "57 tok" {
		t.Errorf("five hour = %q, want the shared record counted once", fiveHour.Detail)
	}
}

// The whole point of the offsets is that a sweep re-reads only what was
// appended — and that re-reading a boundary record cannot double-count it.
func TestUsageEstimatorReadsOnlyAppendedBytes(t *testing.T) {
	projects := t.TempDir()
	path := filepath.Join(projects, "proj", "a.jsonl")
	writeLines(t, path, usageLine("msg_1", time.Minute, 0, 0, 0, 10))

	e := newUsageEstimator(projects)
	if _, weekly := e.windows(time.Now()); weekly.Detail != "10 tok" {
		t.Fatalf("first sweep = %q", weekly.Detail)
	}
	firstOffset := e.offsets[path]
	if firstOffset == 0 {
		t.Fatal("no offset recorded for a file that was read")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, usageLine("msg_2", time.Minute, 0, 0, 0, 5))
	f.Close()

	if _, weekly := e.windows(time.Now()); weekly.Detail != "15 tok" {
		t.Errorf("second sweep = %q, want both records once", weekly.Detail)
	}
	if e.offsets[path] <= firstOffset {
		t.Error("offset did not advance past the appended record")
	}
	// A third sweep with nothing appended must change nothing.
	if _, weekly := e.windows(time.Now()); weekly.Detail != "15 tok" {
		t.Errorf("idle sweep = %q", weekly.Detail)
	}
}

// Buckets and dedup keys are pruned to the week, so the structure stays sized
// to one week of activity rather than to the whole transcript archive.
func TestUsageEstimatorPrunesPastTheWeek(t *testing.T) {
	projects := t.TempDir()
	writeLines(t, filepath.Join(projects, "proj", "a.jsonl"),
		usageLine("msg_old", 6*24*time.Hour, 0, 0, 0, 10))

	e := newUsageEstimator(projects)
	e.windows(time.Now())
	if len(e.sortedMinutes()) != 1 {
		t.Fatalf("want one bucket, got %d", len(e.sortedMinutes()))
	}
	// Sweep again from a vantage point two days later: the record has aged out.
	e.windows(time.Now().Add(2 * 24 * time.Hour))
	if got := len(e.sortedMinutes()); got != 0 {
		t.Errorf("want the bucket pruned, got %d", got)
	}
	if got := len(e.seen); got != 0 {
		t.Errorf("want the dedup key pruned, got %d", got)
	}
}

func TestUsageEstimatorWithoutProjectsDirIsUnknown(t *testing.T) {
	e := newUsageEstimator("")
	fiveHour, weekly := e.windows(time.Now())
	if fiveHour.Pct != browserproto.UsagePctUnknown || fiveHour.Detail != "" {
		t.Errorf("five hour = %+v", fiveHour)
	}
	if weekly.Pct != browserproto.UsagePctUnknown || weekly.Detail != "" {
		t.Errorf("weekly = %+v", weekly)
	}
}

// Non-assistant records, records with no usage, and unparseable lines are all
// ordinary transcript content — none of them may derail a sweep.
func TestUsageEstimatorSkipsNoiseRecords(t *testing.T) {
	projects := t.TempDir()
	writeLines(t, filepath.Join(projects, "proj", "a.jsonl"),
		`{"type":"user","timestamp":"`+time.Now().UTC().Format(time.RFC3339)+`"}`,
		`{"type":"assistant","message":{"id":"msg_nousage"}}`,
		`{"type":"summary","usage":"not an object"}`,
		`{ broken json "usage"`,
		usageLine("msg_good", time.Minute, 1, 1, 0, 0),
	)
	e := newUsageEstimator(projects)
	if _, weekly := e.windows(time.Now()); weekly.Detail != "2 tok" {
		t.Errorf("weekly = %q, want only the one real record", weekly.Detail)
	}
}

func TestFormatTokens(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 tok"}, {999, "999 tok"}, {1500, "2K tok"},
		{2_400_000, "2.4M tok"}, {3_000_000_000, "3.0B tok"},
	} {
		if got := formatTokens(tc.n); got != tc.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// --- Publication --------------------------------------------------------------

// The reading is stored so a browser connecting between polls is sent the
// current numbers (serveInit), and the store always holds the latest one —
// including a reading whose numbers are unchanged but whose timestamp moved,
// which is what keeps the sidebar's "n ago" honest.
func TestSetUsageStoresLatest(t *testing.T) {
	o := &orch{}
	at := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	m := browserproto.NewUsage("account",
		browserproto.UsageWindow{Pct: 10}, browserproto.UsageWindow{Pct: 20}, "").WithReadAt(at)

	o.setUsage(m)
	if o.usage == nil || o.usage.FiveHour.Pct != 10 {
		t.Fatalf("usage not stored: %+v", o.usage)
	}
	if o.usage.ReadAt != "2026-08-02T10:00:00Z" {
		t.Errorf("read_at = %q, want the stamped instant", o.usage.ReadAt)
	}

	// Same numbers, later poll: the store must take the newer stamp.
	again := m.WithReadAt(at.Add(2 * time.Minute))
	o.setUsage(again)
	if o.usage.ReadAt != "2026-08-02T10:02:00Z" {
		t.Errorf("read_at = %q, want the re-read instant", o.usage.ReadAt)
	}

	moved := browserproto.NewUsage("account",
		browserproto.UsageWindow{Pct: 11}, browserproto.UsageWindow{Pct: 20}, "")
	o.setUsage(moved)
	if o.usage.FiveHour.Pct != 11 {
		t.Errorf("moved reading not stored: %+v", o.usage)
	}
}

// A nudge is a signal, not a queue: RefreshUsage never blocks the loop
// goroutine, and repeated asks before the poller wakes collapse into one.
func TestRefreshUsageNudgeCoalesces(t *testing.T) {
	o := &orch{usageNudge: make(chan struct{}, 1)}
	o.RefreshUsage()
	o.RefreshUsage() // the buffer is full — must be dropped, not block

	if len(o.usageNudge) != 1 {
		t.Fatalf("pending nudges = %d, want 1", len(o.usageNudge))
	}
	<-o.usageNudge
	select {
	case <-o.usageNudge:
		t.Error("a second nudge was queued behind the first")
	default:
	}
}
