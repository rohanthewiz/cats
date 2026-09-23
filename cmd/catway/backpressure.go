//go:build ghostty

package main

import (
	"log"
	"slices"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/dlog"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Backpressure for a browser that cannot keep up.
//
// Frames are the only traffic that can outrun a connection: everything else is
// small and event-driven, while a busy pane produces a frame every flush tick
// whether or not the last one has left the building. Before this, a slow link
// (a phone on cellular, a tab the OS throttled) queued frames until its
// 512-message channel filled — up to ~60 MB of screens that were stale before
// they were sent — and was then dropped outright.
//
// But a queued frame is only ever worth the NEWEST one: the screen it
// describes has been overwritten by the time it would arrive. So above a byte
// watermark the connection simply stops being handed frames, and once it has
// drained it is given one full frame per visible pane, straight from the
// pane's grid, which is the screen as it is now:
//
//	queued bytes
//	  ▲
//	  │        ┌── frames skipped: translator Reset, pane marked stale
//	hi├ ─ ─ ─ ─┼──────────────┐
//	  │       ╱│              │╲
//	  │      ╱ │              │ ╲  writer drains
//	lo├ ─ ─ ╱─ ┼ ─ ─ ─ ─ ─ ─ ─│─ ╲──── catchUp: one full frame per stale pane
//	  │    ╱                  │
//	  └─────────────────────────────▶ time
//
// Two watermarks, not one, so a connection hovering at the line does not
// alternate between a skipped frame and a full catch-up frame every tick —
// which would send MORE than streaming diffs did.
//
// Only frames are held back. Chrome, command results and everything else keep
// flowing (they are what the user is waiting on, and they are small), so the
// 512-message cap stays as the last resort for a peer that reads nothing at all.

// A var rather than a const only so a test can shrink them; nothing else
// writes them.
var (
	// congestHigh: stop translating frames for a connection with this much
	// still unwritten. ~35 full frames of a 200×50 pane — about half a second
	// of a streaming pane at the flush rate — so a healthy link never gets here.
	congestHigh int64 = 4 << 20
	// congestLow: resume once the backlog is down to this.
	congestLow int64 = 1 << 20
)

// frameCongested reports whether c is too far behind to be handed another
// frame.
func (c *client) frameCongested() bool { return c.queued.Load() > congestHigh }

// skipFrame records that a frame for pid went unsent to c: the translator is
// reset (the next thing c gets for the pane must be the whole screen, not a
// diff off a base it never received), and the pane is remembered for the
// catch-up. Loop-goroutine only.
func (c *client) skipFrame(pid uint32) {
	if c.stale == nil {
		c.stale = map[uint32]bool{}
	}
	if len(c.stale) == 0 {
		dlog.Warnf("catway: browser connection is %d KB behind; holding its frames back", c.queued.Load()>>10)
	}
	c.stale[pid] = true
	if t := c.trans[pid]; t != nil {
		t.Reset()
	}
	c.markBehind()
}

// markBehind arms the catch-up: the writer's next write under the low
// watermark posts it. Loop-goroutine only.
//
// The writer may already have drained the whole backlog between the check
// that brought the caller here and the Store below, in which case none of its
// writes saw behind set and none will come to post the catch-up. Checking
// again after the Store closes that gap: whichever side sees both "behind"
// and "drained" first posts it, and the CompareAndSwap lets only one of them.
func (c *client) markBehind() {
	c.behind.Store(true)
	if c.queued.Load() <= congestLow && c.behind.CompareAndSwap(true, false) {
		c.o.post(func() { c.o.catchUp(c) })
	}
}

// wrote is the writer's report that n bytes left for the socket. Crossing back
// under the low watermark with frames held back asks the loop for the
// catch-up — once: the CompareAndSwap makes only the first write under the
// line post it. Writer goroutine.
func (c *client) wrote(n int) {
	if c.queued.Add(-int64(n)) <= congestLow && c.behind.CompareAndSwap(true, false) {
		c.o.post(func() { c.o.catchUp(c) })
	}
}

// catchUp hands a drained connection the current screen of every pane it
// missed frames for and still shows. A pane that already got a frame since the
// connection drained (its reset translator made that frame a full one) is no
// longer stale and is skipped. Loop-goroutine only.
func (o *orch) catchUp(c *client) {
	if _, ok := o.conns[c]; !ok || len(c.stale) == 0 {
		return
	}
	if c.frameCongested() {
		// Refilled before the loop got here (chrome, or a burst of frames for
		// panes that were not stale). The next drain will post again.
		c.markBehind()
		return
	}
	pids := make([]uint32, 0, len(c.stale))
	for pid := range c.stale {
		pids = append(pids, pid)
	}
	slices.Sort(pids) // deterministic order for the tests
	clear(c.stale)
	for _, pid := range pids {
		rt := o.panes[pid]
		if rt == nil || !c.view.visible[pid] {
			continue
		}
		view, ok := rt.grid.FullView()
		if !ok {
			// The grid missed a frame too (the pane was off every screen), so
			// only the daemon has the screen. Its full frame reaches c through
			// the reset translator like any other.
			o.hostOf(rt).send(orchestration.NewRequestResync(pid))
			continue
		}
		msg := c.translator(pid).TranslateView(&view)
		if b, err := browserproto.Marshal(msg); err == nil {
			o.enqueue(c, b)
		}
	}
	log.Printf("catway: browser connection caught up (%d panes redrawn)", len(pids))
}
