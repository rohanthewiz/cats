//go:build ghostty

package orchestration

import (
	"sync"
	"time"

	"github.com/rohanthewiz/cats/internal/hostmeter"
)

// Host-stats subscriptions: the daemon measuring the machine it runs on, so a
// pane on another box can say something about that box.
//
// The shape is a subscription rather than a request/reply pair, and that follows
// from what a CPU reading is. Utilization is a rate, so it does not exist as a
// value to be read — only as a difference between two readings an interval
// apart. A daemon that started measuring when asked and answered immediately
// would have nothing to say. So a client subscribes, the daemon starts sampling,
// and readings arrive on the interval the client chose.
//
// The corollary is the part worth being careful about: a cathost is not a
// monitoring agent. Nothing is sampled until somebody subscribes, and the
// subscription ends — taking the sampler and, on darwin, its iostat with it — as
// soon as the client says so or the connection goes. A box in the roster that
// nobody has opened the sidebar for costs nothing.
//
// One subscription per daemon, not per client: a cathost serves one client at a
// time, so a second request re-paces the first rather than adding to it.

// hostStatsMinInterval floors what a client can ask for. The reading is a
// sidebar row, and a client asking for one every 100ms would spend more of the
// daemon on being measured than on the terminals — while gaining nothing, since
// the CPU figure underneath only advances once per hostmeter sample.
const hostStatsMinInterval = time.Second

// statsSub is the daemon's live subscription (nil when nobody has asked).
type statsSub struct {
	stop    chan struct{}
	sampler *hostmeter.Sampler
}

// requestHostStats installs, re-paces or cancels the subscription. An interval
// of zero or less cancels; anything shorter than hostStatsMinInterval is raised
// to it rather than refused, because the client asked for "as often as you can"
// and that is what it is being given.
func (h *Host) requestHostStats(c RequestHostStats) {
	interval := time.Duration(c.IntervalMs) * time.Millisecond
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	h.stopHostStatsLocked()
	if interval <= 0 {
		return
	}
	interval = max(interval, hostStatsMinInterval)

	sub := &statsSub{stop: make(chan struct{}), sampler: hostmeter.NewSampler()}
	h.stats = sub
	go sub.sampler.Run()
	go h.pumpHostStats(sub, interval)
}

// stopHostStatsLocked ends any live subscription. Callers hold statsMu.
func (h *Host) stopHostStatsLocked() {
	if h.stats == nil {
		return
	}
	close(h.stats.stop)
	h.stats.sampler.Stop()
	h.stats = nil
}

// stopHostStats is stopHostStatsLocked for the callers that do not already hold
// the lock — the end of a session and the daemon's teardown. A subscription
// belongs to the connection that asked for it: the next client will ask again,
// and it may want a different interval or nothing at all.
func (h *Host) stopHostStats() {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	h.stopHostStatsLocked()
}

// pumpHostStats emits one reading per interval until the subscription ends.
//
// The first reading goes out immediately. It carries memory and disk but no CPU
// — that one is an interval away by construction — and sending it anyway is
// deliberate: two of the three rows are available now, and a section that stays
// blank until the first tick reads as a machine that cannot be measured.
func (h *Host) pumpHostStats(sub *statsSub, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		h.emit(NewHostStats(hostmeter.Rows(sub.sampler)))
		select {
		case <-t.C:
		case <-sub.stop:
			return
		case <-h.closed:
			return
		}
	}
}

// statsFields is embedded in Host; kept here so the whole subscription lives in
// one file.
type statsFields struct {
	statsMu sync.Mutex
	stats   *statsSub
}
