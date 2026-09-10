//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>

// Defined in splash_darwin.m — the startup window.
void catsOpenSplash(const char *html, const char *title);
void catsSplashPush(const char *js);
void catsSplashRaise(void);
void catsCloseSplash(void);
*/
import "C"

import (
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"
)

// The Go half of the startup window, and the tail end of the startup sequence
// it reports on.
//
// Two things live here. The first is the splash itself: open it, push the boot
// log into it, close it. The second is the part of startup that does NOT happen
// in this process — the page loading in the web view and that page's WebSocket
// bringing a session back. Those are reported to us through cgo callbacks (a
// navigation delegate, and a small bridge the page calls), and they are the
// steps most likely to be where a launch stalls, because they are the ones that
// depend on the daemons actually working rather than merely having started.
//
//	this process            the window                    the page
//	  daemons up  ──►  document loaded (catappWindowDidLoad)
//	                                     ──►  ws-connecting / ws-open / ready
//	                                              │
//	                     splash closes ◄──────────┘ (or 3s after load, for a
//	                                                 page that does not report)

// splashTitle names the window in the Dock's window menu and ⌘` cycling.
const splashTitle = "Starting Cats"

// splashGraceAfterLoad is how long the splash waits, after a page finishes
// loading, for that page to report its own boot phases. A catway new enough to
// report cancels it on the first report; anything else — an older server, a
// login page, the thin client's connect form — never will, and the splash
// closes on this timer instead. Long enough that a report on a busy machine
// still beats it, short enough that nobody stares at a stale window.
const splashGraceAfterLoad = 3 * time.Second

// When the startup window is put back in front of the workspace window that
// just opened on top of it.
//
// Not always, which is the point: on a launch that took under a second the log
// has already said everything it has to say, and shoving it back over the UI
// the user is waiting for would make the diagnostic itself the annoyance. So it
// comes forward only for a launch that is ALREADY slow when the page loads, or
// one that goes quiet afterwards — a page that loaded but whose session never
// arrives, which is the failure the window would otherwise hide behind the
// window it is meant to be explaining.
const (
	splashRaiseIfSlower = 3 * time.Second
	splashRaiseIfStuck  = 4 * time.Second
)

// splash is the window's state on the Go side: whether it is showing, so
// pushes and the failure path know whether there is anything to talk to.
var splash struct {
	mu   sync.Mutex
	open bool
}

// showSplash opens the startup window and connects the boot log to it. Must be
// called on the main thread, before the run loop starts (runLocal/runRemote do,
// right after the shell is created) — everything recorded up to that point is
// pushed as soon as the sink is installed, so the entries from before the
// window existed are in it too.
func showSplash() {
	splash.mu.Lock()
	if splash.open {
		splash.mu.Unlock()
		return
	}
	splash.open = true
	splash.mu.Unlock()

	cHTML, cTitle := C.CString(splashPageHTML()), C.CString(splashTitle)
	defer C.free(unsafe.Pointer(cHTML))
	defer C.free(unsafe.Pointer(cTitle))
	C.catsOpenSplash(cHTML, cTitle)

	boot.setSink(splashPush)
}

// splashVisible reports whether there is still a startup window to show things
// in. The user can close it at any time, and the failure path needs to know
// that it has no surface left.
func splashVisible() bool {
	splash.mu.Lock()
	defer splash.mu.Unlock()
	return splash.open
}

// splashPush is the boot log's sink: one snapshot, rendered by the page. It is
// called from whichever goroutine touched the log — the supervisor, a daemon's
// output pump, a cgo callback — so it hops to the main thread for the AppKit
// call.
//
// The JSON is a complete JS expression (Go's encoder escapes <, > and the two
// line separators that would otherwise break out of a script), so the statement
// can be built by concatenation.
func splashPush(snapshot []byte) {
	if !splashVisible() {
		return
	}
	js := "window.catsBootPush && window.catsBootPush(" + string(snapshot) + ")"
	onMainThread(func() {
		cJS := C.CString(js)
		defer C.free(unsafe.Pointer(cJS))
		C.catsSplashPush(cJS)
	})
}

// raiseSplash brings the startup window back in front — after the workspace
// windows open on top of it while startup is still going, and when a failure
// needs to be read.
func raiseSplash() {
	if !splashVisible() {
		return
	}
	onMainThread(func() { C.catsSplashRaise() })
}

// closeSplash takes the startup window away and stops the log pushing to it.
// Idempotent; safe from any goroutine.
func closeSplash() {
	splash.mu.Lock()
	if !splash.open {
		splash.mu.Unlock()
		return
	}
	splash.open = false
	splash.mu.Unlock()

	boot.setSink(nil)
	onMainThread(func() { C.catsCloseSplash() })
}

// catappSplashClosed is called from splash_darwin.m when the user closes the
// startup window themselves. Nothing about the launch changes — the log simply
// loses its surface.
//
//export catappSplashClosed
func catappSplashClosed() {
	splash.mu.Lock()
	splash.open = false
	splash.mu.Unlock()
	boot.setSink(nil)
}

// --- the last leg: the window and its page --------------------------------------

// bootWatch tracks the steps that finish outside this process. It exists
// because the two ends of that stretch arrive as unrelated callbacks — a
// navigation delegate on one side, a page's own report on the other — and
// something has to hold "which step is that" between them.
type bootWatch struct {
	mu       sync.Mutex
	windowID int  // the step covering "open the window and load the page"
	sessID   int  // the step covering the page's WebSocket session
	loaded   bool // a page has finished loading at least once
	reported bool // the page speaks the boot bridge, so wait for its "ready"
	grace    *time.Timer
}

var bootView = &bootWatch{}

// startUIWatch opens the step that covers everything from "we have an address"
// to "there is a workspace on screen". Called just before the windows are
// opened.
func startUIWatch(windows int) { bootView.start(boot, windows) }

func (w *bootWatch) start(b *bootLog, windows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.windowID != 0 {
		return
	}
	what := "opening the window"
	if windows > 1 {
		what = fmt.Sprintf("opening %d windows", windows)
	}
	w.windowID = b.begin(what)
}

// catappWindowDidLoad is called from window_darwin.m when a window's page
// finishes loading. Only the first one during startup matters: it closes the
// window step and starts the grace timer that gives the page a moment to report
// its own progress.
//
//export catappWindowDidLoad
func catappWindowDidLoad() {
	if !bootView.pageLoaded(boot) {
		return
	}
	// The window that just loaded is now on top of the log. Bring the log back
	// only when it is worth reading: a launch that was already slow getting
	// here, or one that stops moving now that it is here.
	if boot.elapsedMs() > splashRaiseIfSlower.Milliseconds() {
		raiseSplash()
	}
	time.AfterFunc(splashRaiseIfStuck, func() {
		if !boot.isDone() {
			raiseSplash()
		}
	})
}

// pageLoaded settles the window step and arms the grace timer. It reports
// whether this was the first load of the launch — later ones (a reconnect, a
// new window, a navigation) are not startup and must not touch the log.
func (w *bootWatch) pageLoaded(b *bootLog) bool {
	if b.isDone() {
		return false
	}
	w.mu.Lock()
	if w.loaded {
		w.mu.Unlock()
		return false
	}
	w.loaded = true
	id := w.windowID
	w.grace = time.AfterFunc(splashGraceAfterLoad, graceExpired)
	w.mu.Unlock()

	if id != 0 {
		b.okDetail(id, "page loaded")
	} else {
		b.note("ui", "page loaded")
	}
	return true
}

// catappWindowLoadFailed is called when a window's page cannot be loaded at all
// — the catway died between accepting our readiness dial and serving the
// document, or a thin client's host is unreachable. The launch stops here and
// the log stays up saying so.
//
//export catappWindowLoadFailed
func catappWindowLoadFailed(cReason *C.char) {
	reason := C.GoString(cReason)
	if err := bootView.pageFailed(boot, reason); err != nil {
		bootFailed("Could not load the cats UI", err)
	}
}

// pageFailed records a page that would not load, and returns the error when
// that is a startup failure rather than an incident.
//
// Only a failure BEFORE anything has loaded ends the launch. Later ones are
// noted and no more: once the UI is up, a navigation that fails is that
// window's problem — and WKWebView reports a navigation superseded by another
// as a failure too, so treating every one as fatal would turn ordinary
// behaviour into a red startup window.
func (w *bootWatch) pageFailed(b *bootLog, reason string) error {
	if b.isDone() {
		return nil
	}
	w.mu.Lock()
	loaded, id := w.loaded, w.windowID
	w.mu.Unlock()

	err := fmt.Errorf("the page could not be loaded: %s", reason)
	if loaded {
		b.note("ui", err.Error())
		return nil
	}
	if id != 0 {
		b.fail(id, err)
	} else {
		b.note("ui", err.Error())
	}
	return err
}

// catappBootPhase is the page's own report, arriving through the catsBoot
// bridge (window_darwin.m). The phases are the front end's startup: its
// WebSocket opening, the server's welcome, and the first layout — the moment
// there is actually something on screen.
//
// This is the half of startup a launcher cannot see for itself, and it is
// where a broken session shows up: "connecting to the session" that never ends
// is a catway that answered HTTP and then could not talk to cathost.
//
//export catappBootPhase
func catappBootPhase(cPhase, cDetail *C.char) {
	if bootView.phase(boot, C.GoString(cPhase), C.GoString(cDetail)) {
		finishBoot()
	}
}

// phase files one report and says whether it was the last one — the page
// telling us there is a workspace on screen. Split from the cgo entry point
// above so the mapping can be tested: a test file cannot import "C".
func (w *bootWatch) phase(b *bootLog, phase, detail string) bool {
	if b.isDone() {
		return false
	}

	w.mu.Lock()
	// The first report proves the page speaks this bridge, so the grace timer
	// (which exists for pages that do not) is no longer the right way to end
	// startup — its "ready" is.
	w.reported = true
	if w.grace != nil {
		w.grace.Stop()
		w.grace = nil
	}
	sessID := w.sessID
	if phase == "ws-connecting" && sessID == 0 {
		sessID = b.begin("connecting to the session")
		w.sessID = sessID
	}
	w.mu.Unlock()

	switch phase {
	case "ws-connecting":
		// The step opened above is the report.
	case "ws-open":
		b.note("ui", "websocket open — waiting for the session")
	case "welcome":
		b.note("ui", "server welcomed the client"+formatDetail(detail))
	case "ws-closed":
		// The session step is left running on purpose: a connect loop that
		// keeps closing is a hang with extra steps, and the clock on that step
		// is what shows it for what it is.
		b.note("ui", "websocket closed — retrying"+formatDetail(detail))
	case "ready":
		if sessID != 0 {
			b.okDetail(sessID, detail)
		} else {
			b.note("ui", "session ready"+formatDetail(detail))
		}
		return true
	default:
		b.note("ui", phase+formatDetail(detail))
	}
	return false
}

// graceExpired ends startup for a page that never reported. Everything the
// launcher can check has already passed by this point — the document loaded —
// so there is nothing left to wait for and no reason to keep the window.
func graceExpired() {
	if bootView.graceExpired(boot) {
		finishBoot()
	}
}

func (w *bootWatch) graceExpired(b *bootLog) bool {
	w.mu.Lock()
	reported := w.reported
	w.grace = nil
	w.mu.Unlock()
	if reported {
		return false
	}
	b.note("ui", "the page does not report its own startup; assuming it is up")
	return true
}

// finishBoot closes the log and takes the window away. Idempotent — the grace
// timer and the page's "ready" can both reach it.
func finishBoot() {
	if boot.isDone() {
		return
	}
	boot.finish()
	closeSplash()
}

// bootFailed puts a startup failure in front of the user. The startup window is
// the right place for it — it already shows which step failed, how long it ran
// and what the daemons said on the way — so all this does is make sure it is
// on screen. If the user had closed it, it is reopened: the log is held in
// memory, so a reopened window shows the whole launch including the failure.
//
// (showError, the old single-window error sheet, is NOT the fallback here. It
// builds a webview_go window, and webview_go makes itself NSApp's delegate and
// calls [NSApp run] — re-entering a run loop that is already going, in an app
// whose delegate owns teardown. It stays for a failure with no AppKit at all
// behind it; every local-mode failure now happens after the shell is up.)
func bootFailed(title string, err error) {
	log.Printf("%s: %v", title, err)
	onMainThread(func() {
		showSplash() // no-op when it is already open
		C.catsSplashRaise()
	})
}
