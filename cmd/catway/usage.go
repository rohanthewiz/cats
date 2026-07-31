//go:build ghostty

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// The account's standing against Claude's rate-limit windows, in the sidebar's
// USAGE section: how much of the rolling 5-hour window and of the week is
// already spent. It is the one number a long agent session keeps wanting and
// cannot get without stopping to ask claude itself — cats already watches the
// agents, so it may as well watch what they are spending.
//
// Two sources, in that order:
//
//   - The account. claude's own /usage screen reads per-window utilization from
//     an endpoint on the user's OAuth credential, and so do we. This is the only
//     source that yields a *percentage*, because it is the only one that knows
//     what the tokens were spent against.
//   - The transcripts on this machine (usageEstimator). When there is no usable
//     credential — no claude login, an expired token, a machine where the
//     keychain is not readable — the same JSONL files agentmodel.go reads for
//     the per-pane model are summed over the two windows instead. That yields a
//     token count and no percentage, and it is reported as such: a figure, not a
//     bar. An estimate dressed as a limit would be worse than no answer.
//
// The credential is read into this process and put in one Authorization header.
// It is never logged, never broadcast, and never reaches the browser — the
// browser only ever sees the two numbers. Nothing here refreshes the token
// either: the refresh token is claude's to spend, and rotating it underneath a
// running claude to draw a progress bar is not a trade worth making. An expired
// credential simply falls through to the transcript estimate until claude
// itself refreshes it.

const (
	// usageInterval paces the poll. The windows move slowly (a 5-hour window
	// advances 0.3% a minute at full tilt), so this is about keeping a
	// glanceable number roughly current, not about tracking it live.
	usageInterval = 2 * time.Minute
	// usageTimeout bounds one account read. The pacer is a background
	// goroutine, but a hung dial should not hold a poll slot until the next
	// tick.
	usageTimeout = 10 * time.Second

	usageEndpoint = "https://api.anthropic.com/api/oauth/usage"
	// usageOAuthBeta gates the OAuth-credentialed endpoints.
	usageOAuthBeta = "oauth-2025-04-20"
	// usageUserAgent identifies us as claude-code. This is load-bearing rather
	// than cosmetic: requests that do not carry it are served from a much
	// tighter rate-limit bucket and start failing. The version is a recent
	// claude release — we send the header claude sends, from the machine
	// claude's own credential belongs to.
	usageUserAgent = "claude-code/2.1.220"

	// usageKeychainService is the macOS keychain item claude stores its OAuth
	// credential under.
	usageKeychainService = "Claude Code-credentials"
)

// --- Pacer + publication (loop goroutine for the store, own goroutine for I/O) ---

// runUsage is the poll pacer (own goroutine, started by main). Both sources do
// blocking I/O — one HTTPS round trip, or a sweep over transcript files — so
// the whole read happens here and only the finished message is posted back onto
// the loop. The first poll is immediate: a section that says nothing until two
// minutes after launch reads as broken rather than as pending.
func (o *orch) runUsage() {
	est := newUsageEstimator(o.claudeProjects)
	for {
		msg := readUsage(est)
		o.post(func() { o.setUsage(msg) })
		time.Sleep(usageInterval)
	}
}

// setUsage stores the latest reading and pushes it when it changed. The store
// is what a browser connecting mid-interval is sent (serveInit), so a fresh
// page gets the current numbers rather than a blank section until the next
// tick.
func (o *orch) setUsage(m browserproto.Usage) {
	if o.usage != nil && *o.usage == m {
		return
	}
	o.usage = &m
	o.broadcast(m)
}

// readUsage produces one reading: the account when its credential and endpoint
// both cooperate, the local estimate otherwise. A failed account read is not
// silent — its reason rides along with the fallback numbers so the sidebar can
// say why it is showing an estimate.
func readUsage(est *usageEstimator) browserproto.Usage {
	report, err := readAccountUsage()
	if err == nil {
		return browserproto.NewUsage("account", report.fiveHour, report.weekly, "")
	}
	logUsageOnce(err.Error())
	fiveHour, weekly := est.windows(time.Now())
	return browserproto.NewUsage("local", fiveHour, weekly, err.Error())
}

// --- The account read ---------------------------------------------------------

type accountUsage struct {
	fiveHour browserproto.UsageWindow
	weekly   browserproto.UsageWindow
}

func readAccountUsage() (accountUsage, error) {
	token, err := claudeOAuthToken()
	if err != nil {
		return accountUsage{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageTimeout)
	defer cancel()
	return fetchAccountUsage(ctx, http.DefaultClient, usageEndpoint, token)
}

// usageAPIWindow is the endpoint's per-window shape. Utilization is a
// percentage already (0–100), ResetsAt an RFC 3339 instant.
type usageAPIWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// usageAPIResponse is the slice of the endpoint's reply this cares about. It
// also reports per-model weekly windows and extra-usage credits; the sidebar
// shows the two windows that apply to every plan.
type usageAPIResponse struct {
	FiveHour *usageAPIWindow `json:"five_hour"`
	SevenDay *usageAPIWindow `json:"seven_day"`
}

// fetchAccountUsage performs one authenticated read. Errors are deliberately
// short and free of response text: they are shown in the sidebar, and an
// endpoint's error body is not something to paint into a browser.
func fetchAccountUsage(ctx context.Context, hc *http.Client, endpoint, token string) (accountUsage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return accountUsage{}, fmt.Errorf("usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", usageOAuthBeta)
	req.Header.Set("User-Agent", usageUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		// The URL is in the message and the token is not, but a transport error
		// can quote the request; keep only the operation.
		return accountUsage{}, fmt.Errorf("usage endpoint unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return accountUsage{}, fmt.Errorf("usage endpoint: HTTP %d", resp.StatusCode)
	}
	var out usageAPIResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return accountUsage{}, fmt.Errorf("usage endpoint: unreadable reply")
	}
	if out.FiveHour == nil && out.SevenDay == nil {
		// A 200 with neither window is a shape change, not an account with no
		// usage — zeroed windows are still objects.
		return accountUsage{}, fmt.Errorf("usage endpoint: no windows in reply")
	}
	return accountUsage{
		fiveHour: apiWindow(out.FiveHour),
		weekly:   apiWindow(out.SevenDay),
	}, nil
}

func apiWindow(w *usageAPIWindow) browserproto.UsageWindow {
	if w == nil {
		return browserproto.UsageWindow{Pct: browserproto.UsagePctUnknown}
	}
	return browserproto.UsageWindow{Pct: clampPct(w.Utilization), ResetsAt: w.ResetsAt}
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// --- The credential -----------------------------------------------------------

// claudeCreds is the stored shape claude writes, in the keychain item and in
// the credentials file alike.
type claudeCreds struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"` // ms since epoch; 0 when absent
	} `json:"claudeAiOauth"`
}

// claudeOAuthToken returns claude's current OAuth access token, from the first
// store that actually holds one.
//
// Every store is tried rather than the first that answers, because on macOS
// more than one keychain item shares claude's service name: the login
// credential is filed under the account's username, while a sibling item under
// account "unknown" holds the OAuth tokens claude's MCP plugins have collected.
// The two are the same shape at a glance and only one carries claudeAiOauth, so
// what selects the right one is parsing it — an item that yields no token is
// skipped rather than treated as the answer.
func claudeOAuthToken() (string, error) {
	return firstClaudeToken(claudeCredentialStores())
}

func firstClaudeToken(stores []func() ([]byte, error)) (string, error) {
	// An expired credential is a finding, not a miss: it is remembered so that
	// "claude login expired" survives as the reported reason if no other store
	// has a live one, rather than being flattened into "no credential".
	var expired error
	for _, read := range stores {
		raw, err := read()
		if err != nil {
			continue
		}
		switch tok, err := parseClaudeCreds(raw); {
		case err == nil:
			return tok, nil
		case errors.Is(err, errNoCreds):
			continue // some other client's credential lives here
		default:
			expired = err
		}
	}
	if expired != nil {
		return "", expired
	}
	return "", errNoCreds
}

// claudeCredentialStores is where a claude credential can live, cheapest first.
// The file costs no subprocess and is where Linux keeps it; the keychain is the
// macOS home, queried by account before falling back to the bare service name
// (which returns whichever matching item the keychain happens to hand back).
func claudeCredentialStores() []func() ([]byte, error) {
	stores := []func() ([]byte, error){
		func() ([]byte, error) { return os.ReadFile(claudeCredentialsPath()) },
	}
	if runtime.GOOS != "darwin" {
		return stores
	}
	keychain := func(args ...string) func() ([]byte, error) {
		return func() ([]byte, error) {
			return exec.Command("security", append([]string{"find-generic-password",
				"-s", usageKeychainService}, args...)...).Output()
		}
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		stores = append(stores, keychain("-a", u.Username, "-w"))
	}
	return append(stores, keychain("-w"))
}

// claudeCredentialsPath is where claude keeps its credential file.
// CLAUDE_CONFIG_DIR overrides the ~/.claude default, as claude itself honours
// (and as agentmodel.go already does for transcripts).
func claudeCredentialsPath() string {
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, ".claude")
	}
	return filepath.Join(root, ".credentials.json")
}

// errNoCreds is "nothing here holds a claude credential" — the one failure that
// is worth looking somewhere else for, and the message the sidebar shows when
// nowhere has one.
var errNoCreds = errors.New("no claude credential")

func parseClaudeCreds(raw []byte) (string, error) {
	var creds claudeCreds
	if err := json.Unmarshal(raw, &creds); err != nil {
		return "", errNoCreds
	}
	tok := creds.ClaudeAiOauth.AccessToken
	if tok == "" {
		return "", errNoCreds
	}
	// An expired token is reported as expired rather than sent: the endpoint
	// would reject it, and "claude login expired" is the more useful thing for
	// the sidebar to say than "HTTP 401". Nothing here refreshes it — see the
	// file header.
	if at := creds.ClaudeAiOauth.ExpiresAt; at > 0 && time.Now().After(time.UnixMilli(at)) {
		return "", fmt.Errorf("claude login expired")
	}
	return tok, nil
}

// --- The local fallback -------------------------------------------------------

const (
	// usageWeek and usageFiveHour are the two windows summed from transcripts.
	// They mirror the account's windows in length so the fallback answers the
	// same question, less precisely.
	usageWeek     = 7 * 24 * time.Hour
	usageFiveHour = 5 * time.Hour
	// usageFirstReadCap bounds the tail read of a transcript the estimator has
	// not seen before. A week of transcripts runs to tens of megabytes and the
	// first sweep would otherwise read all of it at once; later sweeps read
	// only what was appended, so this cap applies to the first pass alone.
	usageFirstReadCap = 16 << 20
	// usageMaxLine bounds one JSONL record. Transcript lines carry pasted files
	// and image payloads, so the scanner needs real headroom.
	usageMaxLine = 8 << 20
)

// usageEstimator sums token spend out of claude's transcripts, incrementally.
//
// Cost is the reason for the bookkeeping: a full re-read of a week of
// transcripts every two minutes would be tens of megabytes of JSON parsing for
// two numbers. Instead each file is read once from where the last sweep stopped
// (offsets), and the spend is folded into per-minute buckets (minutes) that
// answer both windows by summing a suffix. Buckets and dedup keys older than
// the week are dropped each sweep, so the whole structure stays proportional to
// one week of activity rather than to the transcript archive.
//
// seen dedups by assistant message id: a resumed or forked session starts a new
// transcript carrying the earlier turns, and counting those again would inflate
// both windows by however many times the user has resumed.
//
// Loop-goroutine-free: only runUsage touches this.
type usageEstimator struct {
	projects string
	offsets  map[string]int64 // transcript path → bytes already folded in
	minutes  map[int64]int64  // unix minute → tokens first seen in it
	seen     map[string]int64 // assistant message id → unix minute
	capped   bool             // a first read hit usageFirstReadCap
}

func newUsageEstimator(projects string) *usageEstimator {
	return &usageEstimator{
		projects: projects,
		offsets:  map[string]int64{},
		minutes:  map[int64]int64{},
		seen:     map[string]int64{},
	}
}

// windows sweeps for new records and returns the two windows as of now. Both
// come back with no percentage — the estimator counts tokens and has no
// allowance to measure them against (see browserproto.UsageWindow).
func (e *usageEstimator) windows(now time.Time) (fiveHour, weekly browserproto.UsageWindow) {
	if e.projects == "" {
		unknown := browserproto.UsageWindow{Pct: browserproto.UsagePctUnknown}
		return unknown, unknown
	}
	e.sweep(now)
	return e.window(now, usageFiveHour), e.window(now, usageWeek)
}

func (e *usageEstimator) window(now time.Time, d time.Duration) browserproto.UsageWindow {
	cutoff := now.Add(-d).Unix() / 60
	var total int64
	for min, tokens := range e.minutes {
		if min >= cutoff {
			total += tokens
		}
	}
	detail := formatTokens(total)
	if e.capped {
		// The first sweep read only the tail of at least one transcript, so the
		// oldest end of the week is short. Say so rather than let a number that
		// is quietly low read as authoritative.
		detail += "+"
	}
	return browserproto.UsageWindow{Pct: browserproto.UsagePctUnknown, Detail: detail}
}

// sweep folds every transcript's new bytes into the buckets, then prunes.
func (e *usageEstimator) sweep(now time.Time) {
	cutoff := now.Add(-usageWeek)
	paths, err := filepath.Glob(filepath.Join(e.projects, "*", "*.jsonl"))
	if err != nil {
		return
	}
	live := make(map[string]bool, len(paths))
	for _, path := range paths {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		live[path] = true
		// A file untouched since before the window can hold nothing that counts.
		// Its offset is left alone so that if it is appended to later, only the
		// new bytes are read.
		if fi.ModTime().Before(cutoff) {
			continue
		}
		if off, ok := e.offsets[path]; ok && off == fi.Size() {
			continue // nothing appended
		}
		e.foldFile(path, fi.Size(), cutoff)
	}
	for path := range e.offsets {
		if !live[path] {
			delete(e.offsets, path) // transcript deleted
		}
	}
	e.prune(now)
}

// foldFile reads path from where the last sweep stopped and adds what it finds.
func (e *usageEstimator) foldFile(path string, size int64, cutoff time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	off, seen := e.offsets[path]
	// A recorded offset is always a record boundary, so reading resumes at it
	// exactly. Only the capped first read lands mid-record, and only that read
	// has a partial line to throw away.
	partial := false
	switch {
	case !seen && size > usageFirstReadCap:
		// First look at a large transcript: read the tail only.
		off, partial, e.capped = size-usageFirstReadCap, true, true
	case off > size:
		off = 0 // truncated or replaced under us
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 64<<10)
	if partial {
		if _, err := r.ReadBytes('\n'); err != nil { // discard the partial record
			e.offsets[path] = size
			return
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), usageMaxLine)
	for sc.Scan() {
		e.foldLine(sc.Bytes(), cutoff)
	}
	// Record the size we stat'ed, not where the scanner stopped: a record still
	// being written is re-read next sweep from the last full line, and dedup by
	// message id keeps a re-read from counting twice. A scanner error (an
	// over-long line) is not fatal for the same reason.
	e.offsets[path] = size
}

func (e *usageEstimator) foldLine(line []byte, cutoff time.Time) {
	// Cheap gate: user turns, summaries and meta records carry no usage, and
	// they are most of the file.
	if !strings.Contains(string(line), `"usage"`) {
		return
	}
	var rec usageTranscriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}
	if rec.Type != "assistant" || rec.Message.ID == "" {
		return
	}
	at, err := time.Parse(time.RFC3339, rec.Timestamp)
	if err != nil || at.Before(cutoff) {
		return
	}
	if _, dup := e.seen[rec.Message.ID]; dup {
		return
	}
	u := rec.Message.Usage
	total := u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	if total <= 0 {
		return
	}
	min := at.Unix() / 60
	e.seen[rec.Message.ID] = min
	e.minutes[min] += total
}

// prune drops everything that has fallen out of the week.
func (e *usageEstimator) prune(now time.Time) {
	cutoff := now.Add(-usageWeek).Unix() / 60
	for min := range e.minutes {
		if min < cutoff {
			delete(e.minutes, min)
		}
	}
	for id, min := range e.seen {
		if min < cutoff {
			delete(e.seen, id)
		}
	}
}

// usageTranscriptRecord is the slice of a transcript line the estimator reads.
// Sidechain records are counted like any other: a sub-agent's tokens are spent
// against the same account.
type usageTranscriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// formatTokens renders a token count for a one-line sidebar label.
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB tok", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM tok", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fK tok", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d tok", n)
	}
}

// logUsageOnce reports a persistent account-read failure exactly once per
// reason, so a machine with no claude login does not narrate it every two
// minutes into the server log.
var usageLogged = map[string]bool{}

func logUsageOnce(reason string) {
	if reason == "" || usageLogged[reason] {
		return
	}
	usageLogged[reason] = true
	log.Printf("catway: account usage unavailable (%s) — showing local estimate", reason)
}

// sortedMinutes is test support: the bucket keys in order.
func (e *usageEstimator) sortedMinutes() []int64 {
	out := make([]int64, 0, len(e.minutes))
	for min := range e.minutes {
		out = append(out, min)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
