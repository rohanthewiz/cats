//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>

// Defined in window_darwin.m — the native multi-window shell.
void  catsAppStart(const char *appName);
void  catsOpenWindow(const char *url, double x, double y, double w, double h);
void  catsOpenHTMLWindow(const char *html, const char *title);
void  catsShowHTMLInKeyWindow(const char *html, const char *title);
void  catsNavigateAll(const char *url, const char *title);
void  catsZoomKeyWindow(int delta);
void  catsSetWindowTitle(const char *title);
char *catsWindowsJSON(void);
int   catsWindowCount(void);
void  catsRunApp(void);
void  catsDispatchMain(void);
*/
import "C"

import (
	"encoding/json"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// The Mac app's half of multi-window.
//
// The server already treats every connection as a view on one workspace
// (browser-protocol §Multiple connections), so "a window per project" reduces
// to opening N WKWebViews on `<ui base>?ws=<id>`. What lives here is everything
// AppKit will not do for us: owning the windows, turning the page's
// window.open into a native one, keeping ⌘+/⌘-/⌘0 pointed at the front window,
// and remembering the set across launches.
//
// Windows are CLIENT state, like the thin client's presets: catway persists
// nothing about them, because a window is a lens on the session and not part of
// it. Closing one closes nothing.

// savedWindow is one entry of the restore list: the workspace a window was
// showing and where it sat on screen. The workspace is read off the window's
// live URL — the page keeps `?ws=` in step with the view it is showing — so a
// workspace switch inside a window is remembered without any extra protocol.
type savedWindow struct {
	Workspace string  `json:"workspace,omitempty"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	W         float64 `json:"w"`
	H         float64 `json:"h"`
}

// winManager is the Go side of the shell: where the UI lives, and the config
// the window set is saved into. One per process, set up by runLocal/runRemote
// before any window opens.
type winManager struct {
	mu sync.Mutex
	// base is the UI's root URL ("http://127.0.0.1:8421" in local mode, the
	// remote catway's URL in remote mode). Windows are opened at base?ws=<id>.
	base string
	// title is the window title (remote mode appends the host).
	title string
	// saveTimer debounces the restore-list write. Window moves and resizes
	// arrive continuously while a window is being dragged; writing app.json on
	// each would be hundreds of file writes for one gesture.
	saveTimer *time.Timer
}

// windows is the process's window manager, nil until a native-window mode
// starts. Its nil-ness is also what the menu and the zoom path read as "this
// process is still on the single webview_go window" (the error sheet).
var windows *winManager

// windowSaveDelay is the debounce for the restore list — long enough that a
// drag writes once, short enough that a crash right after moving a window still
// remembers roughly where it was.
const windowSaveDelay = 700 * time.Millisecond

// startWindowShell creates the NSApplication and our app delegate. Everything
// after it — menus, windows, the run loop — needs NSApp to exist, and nothing
// before it may touch AppKit.
func startWindowShell(base, title string) *winManager {
	cName := C.CString(appMenuName())
	defer C.free(unsafe.Pointer(cName))
	C.catsAppStart(cName)
	windows = &winManager{base: base, title: title}
	windows.setTitle(title)
	return windows
}

// setTitle names the windows — the ones open and the ones opened next. The thin
// client uses it to say which catway it is on.
func (m *winManager) setTitle(title string) {
	m.mu.Lock()
	m.title = title
	m.mu.Unlock()
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cTitle))
	C.catsSetWindowTitle(cTitle)
}

// windowURL is the address of a window showing a workspace. An empty id opens
// on the primary view — whatever window the user touched last, which is what a
// plain ⌘N means and what the server resolves an omitted Init.Workspace to.
func (m *winManager) windowURL(wsID string) string {
	m.mu.Lock()
	base := strings.TrimRight(m.base, "/")
	m.mu.Unlock()
	if wsID == "" {
		return base + "/"
	}
	return base + "/?ws=" + url.QueryEscape(wsID)
}

// open opens a window on a workspace at a saved frame; a zero frame lets AppKit
// place it (centred, then cascaded off whatever is already open).
func (m *winManager) open(wsID string, f savedWindow) {
	m.openURL(m.windowURL(wsID), f)
}

func (m *winManager) openURL(u string, f savedWindow) {
	cURL := C.CString(u)
	defer C.free(unsafe.Pointer(cURL))
	C.catsOpenWindow(cURL, C.double(f.X), C.double(f.Y), C.double(f.W), C.double(f.H))
}

// restore opens the window set from the last run, or one window when there is
// nothing to restore. A saved window whose workspace no longer exists is NOT an
// error and NOT skipped: the server falls back to the primary view, so the
// window opens showing something. A user's window layout should survive them
// tidying up projects.
func (m *winManager) restore(saved []savedWindow) {
	for _, p := range m.restorePlan(saved) {
		m.openURL(p.URL, p.Frame)
	}
}

// windowPlan is one window a restore will open: where to point it and where to
// put it.
type windowPlan struct {
	URL   string
	Frame savedWindow
}

// restorePlan is restore's decision, without the AppKit — which is the half
// worth testing: that an empty list still yields exactly one window, that each
// saved entry keeps its own frame, and that a saved workspace reaches the URL
// unchanged (a vanished one is the server's problem, and the server falls back
// rather than failing).
func (m *winManager) restorePlan(saved []savedWindow) []windowPlan {
	if len(saved) == 0 {
		return []windowPlan{{URL: m.windowURL(""), Frame: savedWindow{}}}
	}
	out := make([]windowPlan, 0, len(saved))
	for _, w := range saved {
		out = append(out, windowPlan{URL: m.windowURL(w.Workspace), Frame: w})
	}
	return out
}

// snapshot reads the live window set back out of AppKit.
func (m *winManager) snapshot() []savedWindow {
	cs := C.catsWindowsJSON()
	if cs == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(cs))
	var out []savedWindow
	if err := json.Unmarshal([]byte(C.GoString(cs)), &out); err != nil {
		log.Printf("window snapshot: %v", err)
		return nil
	}
	return out
}

// saveSoon arms the debounced write of the restore list.
func (m *winManager) saveSoon() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveTimer != nil {
		m.saveTimer.Stop()
	}
	m.saveTimer = time.AfterFunc(windowSaveDelay, m.saveNow)
}

// saveNow writes the window set into app.json beside the mode and the presets.
// It re-reads the config first: the thin client's connect flow writes the same
// file from another path, and clobbering a just-saved preset with a stale copy
// held since launch is exactly the bug a window-move should not be able to
// cause.
//
// It must run on the main thread for the AppKit read, which is why the timer
// hands straight over to the main queue.
func (m *winManager) saveNow() {
	onMainThread(func() {
		wins := m.snapshot()
		cfg := loadAppConfig()
		cfg.Windows = wins
		if err := saveAppConfig(cfg); err != nil {
			log.Printf("could not save the window layout: %v", err)
		}
	})
}

// --- the main thread ------------------------------------------------------------
//
// Everything AppKit is main-thread-only, and the callers that need it are not:
// a debounce timer fires on some goroutine, and so does anything a menu action
// hands off. mainQueue + catsDispatchMain is the hop, and it is deliberately a
// buffered channel plus a drain rather than one dispatch_async per closure —
// cgo callbacks are not free and a drag emits a great many of them.

var mainQueue = make(chan func(), 64)

// onMainThread runs fn on the main thread, soon. It never blocks: a full queue
// means the main thread is already behind on the same kind of work, and the
// closure that would have waited is one whose newer twin is already queued
// (a later window snapshot supersedes an earlier one).
func onMainThread(fn func()) {
	select {
	case mainQueue <- fn:
		C.catsDispatchMain()
	default:
		log.Printf("main-thread queue full; dropping a UI update")
	}
}

// drainMainQueue runs everything queued. Called on the main thread by
// catappMainTick.
func drainMainQueue() {
	for {
		select {
		case fn := <-mainQueue:
			fn()
		default:
			return
		}
	}
}

// runNativeApp installs the menu bar and enters the run loop. Returns when the
// app terminates — the last window closing, Cmd-Q, or a signal handler's exit.
func (m *winManager) run() {
	installMenu() // NSApp exists by now; the Window menu appears because windows != nil
	C.catsRunApp()
}

// zoomKeyWindow steps the terminal font size in the FRONT window's page — the
// per-window half of the ⌘+/⌘-/⌘0 menu items, which Cocoa resolves before any
// WKWebView sees a keydown.
func (m *winManager) zoomKeyWindow(delta int) { C.catsZoomKeyWindow(C.int(delta)) }

// showHTML replaces the front window's content with a literal page (the connect
// form), opening a window if none is open.
func (m *winManager) showHTML(html, title string) {
	cHTML, cTitle := C.CString(html), C.CString(title)
	defer C.free(unsafe.Pointer(cHTML))
	defer C.free(unsafe.Pointer(cTitle))
	C.catsShowHTMLInKeyWindow(cHTML, cTitle)
}

// navigateAll points every window at a URL. Connecting a thin client to a
// different catway is a different SESSION, so every window has to move —
// leaving one behind would show two servers' workspaces with no way to tell
// which was which.
func (m *winManager) navigateAll(u, title string) {
	m.mu.Lock()
	m.base, m.title = strings.TrimRight(u, "/"), title
	m.mu.Unlock()
	cURL, cTitle := C.CString(u), C.CString(title)
	defer C.free(unsafe.Pointer(cURL))
	defer C.free(unsafe.Pointer(cTitle))
	C.catsNavigateAll(cURL, cTitle)
}

// --- cgo exports (called from window_darwin.m) ---------------------------------

// catappNewWindow is the Window menu's New Window (⌘N) and the Dock-icon
// reopen. It opens on the primary view — the server resolves an omitted
// workspace to whichever window the user touched last, which is what "another
// window on what I am doing" means.
//
//export catappNewWindow
func catappNewWindow() {
	if windows == nil {
		return
	}
	windows.open("", savedWindow{})
}

// catappOpenWindowURL is the page's window.open, intercepted in WKUIDelegate:
// the sidebar's "open in new window" and the ⌘K palette action both go through
// it. The URL is the page's own (`?ws=<id>` off the current path), so the shell
// needs to know nothing about workspaces to honour it.
//
//export catappOpenWindowURL
func catappOpenWindowURL(cURL *C.char) {
	if windows == nil {
		return
	}
	windows.openURL(C.GoString(cURL), savedWindow{})
}

// catappWindowsChanged is called whenever a window opens, closes, moves or
// resizes. Debounced: a drag emits these continuously.
//
//export catappWindowsChanged
func catappWindowsChanged() {
	if windows == nil {
		return
	}
	windows.saveSoon()
}

// catappClipWrite / catappClipRead are the native pasteboard bridge behind
// window.catsClipWrite / window.catsClipRead. WKWebView cripples
// navigator.clipboard — reads resolve empty and writes demand a user activation
// that a WebSocket-driven copy (OSC 52 from a pane) never has.
//
//export catappClipWrite
func catappClipWrite(text *C.char) {
	if err := clipboardWrite(C.GoString(text)); err != nil {
		log.Printf("clipboard write: %v", err)
	}
}

// catappClipRead returns a C string the caller must free.
//
//export catappClipRead
func catappClipRead() *C.char {
	s, err := clipboardRead()
	if err != nil {
		log.Printf("clipboard read: %v", err)
		return C.CString("")
	}
	return C.CString(s)
}

// catappConnectForm receives the connect page's three callbacks (remote mode).
// They are fire-and-forget: the form moves because of what this does, not
// because of a return value.
//
//export catappConnectForm
func catappConnectForm(cOp, cURL, cLabel *C.char) {
	r := activeRemote
	if r == nil {
		return
	}
	switch C.GoString(cOp) {
	case "connect":
		r.connect(C.GoString(cURL), C.GoString(cLabel))
	case "forget":
		r.forget(C.GoString(cURL))
	case "cancel":
		r.cancel()
	}
}
