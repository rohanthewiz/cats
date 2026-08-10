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
	"strconv"
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
	// usageInterval paces the poll while somebody is looking. The windows move
	// slowly (a 5-hour window advances 0.3% a minute at full tilt), so this is
	// about keeping a glanceable number roughly current, not about tracking it
	// live.
	usageInterval = 2 * time.Minute
	// usageIdleInterval paces it while every connected window is in the
	// background, and usageDarkInterval while there is no browser at all.
	//
	// The cadence follows the reader rather than the data because the data has
	// no reader: this is a request to somebody else's endpoint, taken purely to
	// paint a sidebar, and a daemon left running overnight with no tab open was
	// spending 360 of them a night on a section nobody would see. Neither tier
	// stops outright, for different reasons. A background window is very often
	// a *visible* one — a second monitor, a tiled half-screen — and freezing a
	// number the user can plainly read is worse than reading it slowly. With no
	// client at all nothing is visible, but the stored reading is what the next
	// browser is handed on connect (serveInit), and half an hour is the most
	// staleness worth handing someone before their own refresh lands.
	usageIdleInterval = 10 * time.Minute
	usageDarkInterval = 30 * time.Minute
	// usageTimeout bounds one account read. The pacer is a background
	// goroutine, but a hung dial should not hold a poll slot until the next
	// tick.
	usageTimeout = 10 * time.Second

	// usageBackoffFirst is the pause after the SECOND consecutive failure of
	// the account read, doubling per failure to usageBackoffMax. The first
	// failure is forgiven at the normal cadence: endpoints hiccup, and a single
	// dropped read that healed by the next tick should not cost the user ten
	// minutes of a stale section to find out.
	//
	// It bounds the wrong-but-persistent cases instead — an endpoint returning
	// 429, a revoked credential, a laptop off the network — where the poll was
	// re-asking a settled question every two minutes and re-answering it the
	// same way. Doubling reaches the cap in four failures, so a genuine outage
	// costs a handful of requests rather than one every two minutes for its
	// whole duration.
	usageBackoffFirst = 5 * time.Minute
	usageBackoffMax   = 30 * time.Minute

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
//
// A nudge (usage.refresh) is a second way to reach the same read, and it
// re-bases the interval rather than adding to it: the timer is recreated after
// every poll, so a manual refresh is followed by a full quiet period instead of
// a tick that may be seconds away. Nothing here reads from the loop's state, so
// a nudge costs one goroutine wake-up and no synchronization.
// The CPU sampler is the exception to "the whole read happens here": utilization
// is a rate, so it has to be sampled on a cadence of its own rather than read on
// demand (see hostcpu.go). It is started alongside the poll and read from it —
// the poll takes whatever history the ring holds at the moment it fires.
func (o *orch) runUsage() {
	est := newUsageEstimator(o.claudeProjects)
	cop := newCopilotEstimator(copilotStateDir())
	cpu := newCPUSampler()
	go cpu.run()
	// Owned by this goroutine alone, like the estimators beside it: the poll is
	// the only thing that attempts the account read, so the only thing that can
	// have a run of failures to remember.
	var back usageBackoff
	for {
		msg := readUsage(est, cop, cpu, &back).WithReadAt(time.Now())
		o.post(func() { o.setUsage(msg) })

		tick := time.NewTimer(o.usageWait())
		select {
		case <-tick.C:
		case <-o.usageNudge:
			tick.Stop()
		}
	}
}

// How closely the USAGE section is being watched, and so how often it is worth
// re-reading. Ordered by how much a stale number costs the person in front of
// it, which is what the interval is really pricing.
const (
	usageAttentionDark    = iota // no browser connected
	usageAttentionIdle           // connected, every window in the background
	usageAttentionWatched        // at least one window in the foreground
)

// usageWait is how long to sleep before the next poll: the tier the loop
// goroutine last published (orch.usageAttention), read here without a lock.
//
// Called once per poll rather than watched, so a window coming to the front
// mid-sleep does not shorten the sleep already in progress — the nudge
// noteUsageAttention sends on that same edge is what cuts it short, and it does
// so through the select this returns into. A window going to the *back*
// deliberately has no such interrupt: the current sleep runs out at the old
// cadence and only the next one is slower, which costs at most one extra read
// and keeps a glance back at a just-blurred window from finding stale numbers.
func (o *orch) usageWait() time.Duration {
	switch o.usageAttention.Load() {
	case usageAttentionWatched:
		return usageInterval
	case usageAttentionIdle:
		return usageIdleInterval
	default:
		return usageDarkInterval
	}
}

// usageBackoff is the account read's memory of consecutive failures: how long
// to stay away from the endpoint, and what to tell the sidebar meanwhile.
//
// It paces only the account read, not the poll. The poll keeps its cadence and
// keeps publishing — the host rows are local reads that have nothing to do with
// a failing endpoint, and the CLAUDE group falls to the transcript estimate,
// which is exactly what it does for an unreadable credential today. Backing off
// the whole poll would have let one endpoint take the memory meter down with
// it.
type usageBackoff struct {
	fails int       // consecutive REMOTE failures; local ones do not count
	next  time.Time // earliest next attempt; zero = attempt now
	last  string    // the failure being waited out, for the group's note
}

// ready reports whether the endpoint may be asked again.
func (b *usageBackoff) ready(now time.Time) bool {
	return b.next.IsZero() || !now.Before(b.next)
}

// reset forgets the run. Called on success, and on a failure that never reached
// the network (no credential, an expired token): those cost nothing to retry,
// and stretching the poll for them would only leave the local estimate — the
// very thing they fall back to — going stale for no saving at all.
func (b *usageBackoff) reset() {
	b.fails, b.next, b.last = 0, time.Time{}, ""
}

// fail records one remote failure and sets the next attempt.
func (b *usageBackoff) fail(now time.Time, err error) {
	b.fails++
	b.last = err.Error()
	if b.fails < 2 {
		b.next = time.Time{} // one hiccup is forgiven; retry on the normal tick
		return
	}
	d := usageBackoffFirst
	// Doubling in a loop rather than by shifting: 1<<n overflows a Duration at
	// 54 doublings, and a daemon left running against a dead endpoint for a
	// week gets there.
	for i := 2; i < b.fails && d < usageBackoffMax; i++ {
		d *= 2
	}
	if d > usageBackoffMax {
		d = usageBackoffMax
	}
	b.next = now.Add(d)
}

// note is the caption a group shows while it is waiting one out. The endpoint's
// own complaint is kept in it — "HTTP 429" is the difference between "cats is
// broken" and "the account is rate-limited" — and the retry time is what says
// the section is waiting on purpose rather than stuck.
func (b *usageBackoff) note(now time.Time) string {
	if b.last == "" {
		return ""
	}
	if d := b.next.Sub(now); d > 0 {
		return b.last + " · retrying in " + fmtRetry(d)
	}
	return b.last
}

// fmtRetry prints a retry delay one unit deep, in the register the sidebar's
// other countdowns use.
func fmtRetry(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	}
	return strconv.Itoa(int(d.Round(time.Minute)/time.Minute)) + "m"
}

// RefreshUsage asks the poller for a reading now (the usage.refresh command,
// app.Backend). Loop-goroutine only, and deliberately non-blocking: the send is
// dropped when a nudge is already pending, because two asks arriving before the
// poller wakes want the same single fresh reading.
func (o *orch) RefreshUsage() {
	select {
	case o.usageNudge <- struct{}{}:
	default:
	}
}

// setUsage stores the latest reading and pushes it to every client. The store
// is what a browser connecting mid-interval is sent (serveInit), so a fresh
// page gets the current numbers rather than a blank section until the next
// tick.
//
// Every reading is pushed, including one whose numbers did not move, because
// the message carries the instant it was read (Usage.ReadAt) and the sidebar
// prints that as "n ago". Suppressing an unchanged reading would freeze that
// label, and a section reporting an hour-old age while the server polls every
// two minutes is a worse lie than the redundant push is a cost — the message is
// a few hundred bytes, twice an hour per client.
func (o *orch) setUsage(m browserproto.Usage) {
	o.usage = &m
	// Stamped from the loop's own clock rather than parsed back out of the
	// message: this is only ever compared against time.Now() to decide whether a
	// refocus has earned a fresh read (noteUsageAttention), and the message's
	// own ReadAt is a wire format that exists to be printed.
	o.lastUsageRead = time.Now()
	o.broadcast(m)
}

// readUsage produces one reading: a subsection per provider that reports
// anything, with the host's own memory last.
//
// The order is fixed rather than sorted, because it is a reading order: claude
// first because it is the one with real percentages, copilot after it, and the
// machine itself at the bottom because it answers a different question from the
// two above it. A provider that reports nothing on this host contributes no
// group at all — an empty heading would say "copilot spent nothing" where the
// truth is "there is no copilot here".
//
// The host group does not depend on any account: a machine with no claude login
// still has a memory ceiling worth watching, and the row should not disappear
// because a token expired.
func readUsage(claude *usageEstimator, copilot *copilotEstimator, cpu *cpuSampler, back *usageBackoff) browserproto.Usage {
	now := time.Now()
	groups := []browserproto.UsageGroup{claudeUsageGroup(claude, back, now)}
	if g, ok := copilot.group(now); ok {
		groups = append(groups, g)
	}
	if g, ok := hostUsageGroup(cpu); ok {
		groups = append(groups, g)
	}
	return browserproto.NewUsage(groups)
}

// claudeUsageGroup is the CLAUDE subsection: the account when its credential and
// endpoint both cooperate, the local estimate otherwise. A failed account read
// is not silent — its reason becomes the group's note, so the sidebar can say
// why it is showing an estimate.
//
// This group is always returned, even when both sources come up empty. It is
// the section's reason for existing, and an absent CLAUDE heading would read as
// a broken sidebar rather than as an unreadable account; the note carries the
// explanation instead.
//
// The 5-hour window is the group's headline — the row that stands in for the
// rest when the group is folded (UsageWindow.Headline). It is the one that
// actually stops work: a week runs out once and is planned around, while the
// 5-hour window is what a long afternoon walks into, and it is the number a
// folded reader is folding *toward* rather than away from.
// How close to a rollover each of Claude's windows starts to matter, sent to the
// browser as UsageWindow.SoonSecs and shown there as a warning-coloured
// countdown. Both are roughly "one working stretch left", which is the span in
// which the answer changes a decision — whatever is left in the window either
// gets spent now or waits for the reset, and a long task started inside it
// straddles the boundary.
//
// They are not the same fraction of their windows and should not be: a tenth of
// the five-hour window is half an hour, while a tenth of a week is most of a day
// and would leave the row shouting through every Friday. Two hours is where a
// week becomes something you can still act on and not yet something you can
// ignore.
const (
	usageSoonFiveHour = 30 * time.Minute
	usageSoonWeek     = 2 * time.Hour
)

func claudeUsageGroup(est *usageEstimator, back *usageBackoff, now time.Time) browserproto.UsageGroup {
	g := browserproto.UsageGroup{ID: "claude", Name: "Claude"}
	// Inside a backoff window the endpoint is not asked at all, and the note
	// carries why plus when it will be. Skipping the read is the entire saving:
	// everything below this point is local.
	if !back.ready(now) {
		return claudeEstimateGroup(g, est, now, back.note(now))
	}
	report, err := readAccountUsage()
	if err != nil {
		// Only a failure that actually reached out earns a pause. A missing or
		// expired credential fails before the request is even built
		// (claudeOAuthToken), so there is nothing there to be gentle with — and
		// that is exactly the case where the estimate is the permanent answer
		// rather than a stopgap, so it should keep refreshing on the normal tick.
		note := err.Error()
		var remote usageRemoteError
		if errors.As(err, &remote) {
			back.fail(now, err)
			note = back.note(now)
		} else {
			back.reset()
		}
		logUsageOnce(err.Error())
		return claudeEstimateGroup(g, est, now, note)
	}
	back.reset()
	report.fiveHour.Name, report.fiveHour.Headline = "5 hr", true
	report.fiveHour.SoonSecs = int(usageSoonFiveHour / time.Second)
	report.weekly.Name = "Week"
	report.weekly.SoonSecs = int(usageSoonWeek / time.Second)
	g.Windows = []browserproto.UsageWindow{report.fiveHour, report.weekly}
	// A model with its own weekly allowance on top of the all-models week —
	// Fable, say. Only the account read knows about it, and only on plans
	// that meter one, so the row appears when a name came back and not
	// otherwise: an empty "Week ·" row would read as a broken window rather
	// than an absent one. The name is the account's own label, printed as
	// given.
	if report.weeklyModelName != "" {
		report.weeklyModel.Name = "Week · " + report.weeklyModelName
		// A week is a week whichever allowance it meters, so it takes the same
		// horizon as the all-models row above it.
		report.weeklyModel.SoonSecs = int(usageSoonWeek / time.Second)
		g.Windows = append(g.Windows, report.weeklyModel)
	}
	return g
}

// claudeEstimateGroup fills the CLAUDE group from the local transcripts, with
// why the account was not used as its caption.
//
// Split out because there are now three ways to arrive at the estimate — a read
// that failed, a read that was skipped because a previous one failed, and a
// machine with no credential to read with — and they differ only in that
// caption. The rows they produce must not differ at all.
func claudeEstimateGroup(g browserproto.UsageGroup, est *usageEstimator, now time.Time, why string) browserproto.UsageGroup {
	fiveHour, weekly := est.windows(now)
	fiveHour.Name, weekly.Name = "5 hr", "Week"
	// The headline holds even for the estimate, where the row carries a token
	// count and no percentage: folded, the group then stands in for itself with
	// the figure rather than a bar, which is still the 5-hour answer and still
	// the one being asked for.
	fiveHour.Headline = true
	g.Windows = []browserproto.UsageWindow{fiveHour, weekly}
	// Only the fallback explains itself. An account reading is the expected case
	// and needs no caption; an estimate has to say that it is one, and why the
	// real numbers were not available.
	g.Note = "estimate · " + why
	return g
}

// hostUsageGroup is the HOST subsection: the machine's own memory, CPU and
// disk, the windows here with nothing to do with any account. They share the
// section because they answer the same question the others do ("is something
// about to stop?") on the same glance, and they share the poll because they are
// read on the same tick — a laptop runs out of RAM, or out of disk, long before
// an account runs out of week.
//
// The rows are gathered independently. Each reader reports nothing rather than a
// guess on a host it cannot ask (see hostMemory, cpuSampler.window, hostDisk),
// and a reader that came up empty drops its row instead of taking the section
// down with it: they have no host in common where exactly one is expected to
// fail, but a permission, a synthetic mount or a killed iostat can silence any
// of them on its own, and the surviving numbers are still worth showing. Only a
// group with no rows at all is withheld — an empty heading reads as a broken
// sidebar.
//
// Memory is the group's headline (UsageWindow.Headline): the row a folded HOST
// stands in for the rest with. It is the resource that ends a session soonest
// and least reversibly — a machine that starts swapping makes every pane treacle
// and nothing but a process exiting takes it back, where a pegged CPU is usually
// the work itself and a full disk has been full for a week.
func hostUsageGroup(cpu *cpuSampler) (browserproto.UsageGroup, bool) {
	g := browserproto.UsageGroup{ID: "host", Name: "Host"}
	if mem := hostMemory(); mem.Pct >= 0 {
		mem.Name, mem.Headline = "Memory", true
		g.Windows = append(g.Windows, mem)
	}
	// Reading order is by how fast each one moves against how badly it ends:
	// memory first (minutes, and it stops the session), CPU second (seconds, and
	// it is usually the work rather than a problem), disk last (weeks). CPU sits
	// in the middle rather than first for that reason — it is the row most often
	// high for a good reason, and leading the group with it would train the eye
	// to skip the group.
	if c := cpu.window(); c.Pct >= 0 {
		c.Name = "CPU"
		g.Windows = append(g.Windows, c)
	}
	if disk := hostDisk(); disk.Pct >= 0 {
		disk.Name = "Disk"
		g.Windows = append(g.Windows, disk)
	}
	if len(g.Windows) == 0 {
		return browserproto.UsageGroup{}, false
	}
	return g, true
}

// --- The account read ---------------------------------------------------------

type accountUsage struct {
	fiveHour browserproto.UsageWindow
	weekly   browserproto.UsageWindow
	// The weekly window of a separately metered model, and its display name.
	// Empty name = the account reports no such window (see usageAPILimit).
	weeklyModel     browserproto.UsageWindow
	weeklyModelName string
}

// usageRemoteError marks a failure that cost a request to the endpoint (or an
// attempt at one), as against a local one that never left this machine. Only
// the former is worth backing off — see claudeUsageGroup.
//
// It carries the wrapped message unchanged, because that message is what the
// sidebar prints and it was written to be read there.
type usageRemoteError struct{ err error }

func (e usageRemoteError) Error() string { return e.err.Error() }
func (e usageRemoteError) Unwrap() error { return e.err }

func readAccountUsage() (accountUsage, error) {
	token, err := claudeOAuthToken()
	if err != nil {
		return accountUsage{}, err // local: no request was made, none is owed a pause
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageTimeout)
	defer cancel()
	u, err := fetchAccountUsage(ctx, http.DefaultClient, usageEndpoint, token)
	if err != nil {
		return accountUsage{}, usageRemoteError{err}
	}
	return u, nil
}

// usageAPIWindow is the endpoint's per-window shape. Utilization is a
// percentage already (0–100), ResetsAt an RFC 3339 instant.
type usageAPIWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// usageAPIResponse is the slice of the endpoint's reply this cares about. It
// also reports extra-usage credits and a spend summary, which the sidebar has
// no room for.
type usageAPIResponse struct {
	FiveHour *usageAPIWindow `json:"five_hour"`
	SevenDay *usageAPIWindow `json:"seven_day"`
	Limits   []usageAPILimit `json:"limits"`
}

// usageAPILimit is one entry of the endpoint's limits array — the newer,
// self-describing view of the same windows the top two fields carry, plus the
// scoped ones they do not.
//
// The array is where a per-model week now lives. The reply still has
// seven_day_opus / seven_day_sonnet / seven_day_fable-shaped fields, but they
// come back null on accounts that plainly do have a per-model limit; the live
// number arrives as a limits entry of kind "weekly_scoped" carrying
// scope.model.display_name. Reading the array rather than the named fields
// means a model added or renamed on the plan shows up without a code change —
// the name is the account's to choose, not ours to enumerate. Note that
// scope.model.id can be null while display_name is set, so the name is the only
// dependable identifier here.
type usageAPILimit struct {
	Kind     string  `json:"kind"` // "session" | "weekly_all" | "weekly_scoped" | …
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
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
	name, modelWeek := weeklyModelLimit(out.Limits)
	return accountUsage{
		fiveHour:        apiWindow(out.FiveHour),
		weekly:          apiWindow(out.SevenDay),
		weeklyModel:     modelWeek,
		weeklyModelName: name,
	}, nil
}

// weeklyModelLimit picks the per-model weekly window to show, if there is one.
//
// An account can carry more than one scoped weekly limit, and the sidebar has
// room for a single extra row, so this takes the one nearest its ceiling — the
// one that will stop the work first, which is the only reason to look at the
// section at all. Entries without a model scope (a surface-scoped limit, say)
// are skipped: the row is labelled with a model name and cannot describe them.
func weeklyModelLimit(limits []usageAPILimit) (string, browserproto.UsageWindow) {
	var name string
	window := browserproto.UsageWindow{Pct: browserproto.UsagePctUnknown}
	for _, l := range limits {
		if l.Kind != "weekly_scoped" || l.Scope == nil || l.Scope.Model == nil {
			continue
		}
		if l.Scope.Model.DisplayName == "" {
			continue // nothing to label the row with
		}
		if name != "" && l.Percent <= window.Pct {
			continue
		}
		name = l.Scope.Model.DisplayName
		window = browserproto.UsageWindow{Pct: clampPct(l.Percent), ResetsAt: l.ResetsAt}
	}
	return name, window
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
