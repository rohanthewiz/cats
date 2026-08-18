//go:build darwin

// Command catapp is the native desktop launcher for cats: a thin Go
// supervisor around a WebKit window (github.com/webview/webview_go). It has two
// runtime modes, chosen by the build-time defaultMode and overridable per user
// in app.json:
//
//   - local  — supervise the in-bundle daemons (cathost -persistent + catway
//     --auth none on loopback) and show their UI in the window. Fully offline;
//     this is the "self-contained" Cats.app (make macapp).
//   - remote — a thin client: start no daemons, point the window at a remote
//     catway URL (a relay host or a direct LAN/VPN address). The catway's own
//     login page collects the password and the webview persists the session
//     cookie across launches. This is Cats Client.app (make macapp-client).
//
// The launcher itself is plain Go (no -tags ghostty) — it only supervises
// processes and shows a window; the terminal/VT work lives entirely in the
// bundled catway/cathost binaries. It is macOS-only (WebKit + a native menu
// via cgo), hence the darwin build constraint on every file in this package.
package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"

	webview "github.com/webview/webview_go"
)

// defaultMode is the build-time default mode: "local" for the self-contained
// Cats.app, "remote" for the thin client. app.json overrides it at runtime.
// Injected via -ldflags "-X main.defaultMode=local|remote"; the var default
// keeps a plain `go build`/`go run` (development) in local mode.
var defaultMode = "local"

// Window geometry. Roomy default that still fits a laptop; the user can resize.
const (
	windowTitle  = "Cats Mux"
	windowWidth  = 1280
	windowHeight = 820
)

// cleanup runs the registered teardown exactly once, no matter which path
// reaches it — window close (deferred), Cmd-Q (the native menu, via the
// cgo-exported catappCleanup), or a SIGINT/SIGTERM. sync.Once is the single
// guard so daemons are reaped once and only once.
var (
	cleanupOnce sync.Once
	cleanupFn   func()
)

func registerCleanup(fn func()) { cleanupFn = fn }

func runCleanup() {
	cleanupOnce.Do(func() {
		if cleanupFn != nil {
			cleanupFn()
		}
	})
}

func main() {
	// Cocoa/WebKit require every UI call on the process's main thread; pin the
	// main goroutine to it before touching the webview. Run() then blocks here
	// until the window closes.
	runtime.LockOSThread()
	log.SetFlags(0)
	log.SetPrefix("catapp: ")

	cfg := loadAppConfig()
	switch cfg.Mode {
	case "remote":
		runRemote(cfg)
	default: // "local" and any unrecognised value
		runLocal(cfg)
	}
}

// runLocal supervises the in-bundle daemons and shows the UI they serve on
// loopback. The backend is reaped when the window closes (Run returns), on a
// Cmd-Q, or on a termination signal — all routed through runCleanup.
func runLocal(_ appConfig) {
	// Before any child exists: a GUI launch hands us launchd's bare PATH, and
	// everything downstream (daemons → panes → plugin build steps) inherits it.
	hydratePATH()

	b, err := startBackend()
	if err != nil {
		showError("Could not start cats", err.Error())
		return
	}
	registerCleanup(b.stop)
	defer runCleanup()
	installSignalHandler()

	w := newWindow(windowTitle)
	defer w.Destroy()
	w.Navigate("http://" + b.addr)
	w.Run()
}

// runRemote is the thin-client path: no local daemons, just a window pointed at
// a catway.
//
// It keeps a list of them (appConfig.Presets) rather than one address, and a
// Connect menu to move between them. The window is the same window throughout:
// switching catways is a navigation, not a relaunch, so the session cookie the
// webview holds for each host survives being away from it.
func runRemote(cfg appConfig) {
	installSignalHandler() // no daemons to reap, but honour a clean quit uniformly

	// Whatever the app opens on becomes a preset. Someone who connected once,
	// before presets existed, should find that catway in the menu rather than
	// have to type it again to "add" it.
	if cfg.Remote.URL != "" {
		cfg.upsertPreset(cfg.Remote)
	}
	rt := &remoteRuntime{cfg: cfg}
	activeRemote = rt

	w := newWindow(windowTitle)
	defer w.Destroy()
	rt.w = w
	if err := rt.bind(); err != nil {
		showError("Could not initialise the connect form", err.Error())
		return
	}
	if cfg.Remote.URL != "" {
		rt.show(cfg.Remote.URL)
	} else {
		rt.showConnect()
	}
	w.Run()
}

// remoteRuntime is the thin client's state while the window is open: which
// catways it knows, which one it is on, and the window to drive.
//
// It exists because three things now change together — the persisted config,
// the window, and the native Connect menu — and any two of them out of step is
// a visible bug (a checkmark on the wrong host, a menu missing what you just
// added). One place that changes all three is the only way they cannot drift.
type remoteRuntime struct {
	cfg appConfig
	w   webview.WebView
}

// connect saves a target, makes it current, and navigates to it.
func (r *remoteRuntime) connect(rawURL, label string) {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return
	}
	r.cfg.Remote = remoteTarget{URL: u, Label: strings.TrimSpace(label)}
	r.cfg.upsertPreset(r.cfg.Remote)
	r.save()
	r.show(u)
}

// forget drops a target from the list. The window is left where it is — see
// appConfig.removePreset for why forgetting is not disconnecting.
func (r *remoteRuntime) forget(rawURL string) {
	r.cfg.removePreset(rawURL)
	r.save()
	r.w.Dispatch(func() {
		r.refreshMenu()
		r.w.SetHtml(connectPage(r.cfg.Presets, r.cfg.Remote.URL, r.cfg.Remote.URL != ""))
	})
}

// show points the window at a catway and re-titles it. Dispatched, because the
// callers are a JS binding and a menu action, neither on the UI thread.
func (r *remoteRuntime) show(rawURL string) {
	r.w.Dispatch(func() {
		r.refreshMenu()
		r.w.SetTitle(remoteTitle(rawURL))
		r.w.Navigate(rawURL)
	})
}

// showConnect renders the picker. Cancelling is offered only when there is a
// session to go back to, so a first run cannot end up on a page with a button
// that does nothing.
func (r *remoteRuntime) showConnect() {
	r.w.Dispatch(func() {
		r.refreshMenu()
		r.w.SetTitle(windowTitle)
		r.w.SetHtml(connectPage(r.cfg.Presets, r.cfg.Remote.URL, r.cfg.Remote.URL != ""))
	})
}

// save persists the config, logging rather than failing: losing a preset is
// annoying, and refusing to connect over it would be worse.
func (r *remoteRuntime) save() {
	if err := saveAppConfig(r.cfg); err != nil {
		log.Printf("could not save connection settings: %v", err)
	}
}

// refreshMenu rebuilds the native menu so the Connect list and its checkmark
// match the config. Must be on the UI thread; every caller is already
// dispatched there.
func (r *remoteRuntime) refreshMenu() {
	installConnectMenu(presetNames(r.cfg.Presets), r.cfg.currentPreset())
}

// bind wires the connect page's three callbacks. They arrive off the UI thread,
// which is why everything they reach dispatches.
func (r *remoteRuntime) bind() error {
	if err := r.w.Bind("catsConnect", r.connect); err != nil {
		return err
	}
	if err := r.w.Bind("catsForget", r.forget); err != nil {
		return err
	}
	return r.w.Bind("catsCancel", func() {
		if r.cfg.Remote.URL != "" {
			r.show(r.cfg.Remote.URL)
		}
	})
}

func presetNames(presets []remoteTarget) []string {
	out := make([]string, 0, len(presets))
	for _, p := range presets {
		out = append(out, p.name())
	}
	return out
}

// activeRemote is the runtime the Connect menu's actions reach (menu_darwin.go's
// cgo export cannot carry a receiver). Nil in local mode, where no Connect menu
// is installed at all.
var activeRemote *remoteRuntime

// selectPreset is what a Connect menu item does: index into the preset list, or
// -1 for "Connect to Another…".
func selectPreset(index int) {
	r := activeRemote
	if r == nil {
		return
	}
	if index < 0 || index >= len(r.cfg.Presets) {
		r.showConnect()
		return
	}
	p := r.cfg.Presets[index]
	r.connect(p.URL, p.Label)
}

// newWindow builds the shared webview window (title + size) and installs the
// native menu bar so Cmd-Q and the standard editing shortcuts work. debug is
// false: no devtools in the shipped app.
func newWindow(title string) webview.WebView {
	w := webview.New(false)
	uiWindow = w
	installMenu() // NSApp now exists (created by webview.New); menu before Run()
	w.SetTitle(title)
	w.SetSize(windowWidth, windowHeight, webview.HintNone)
	// Native clipboard bridge (clipboard.go): injected into every page the
	// window loads; the catway UI prefers these over navigator.clipboard,
	// which WKWebView blocks (empty reads, activation-gated writes). The
	// window only ever loads the configured catway UI, so exposing the
	// pasteboard to the page does not leak it to arbitrary content.
	if err := w.Bind("catsClipWrite", clipboardWrite); err != nil {
		log.Printf("clipboard write bridge unavailable: %v", err)
	}
	if err := w.Bind("catsClipRead", clipboardRead); err != nil {
		log.Printf("clipboard read bridge unavailable: %v", err)
	}
	return w
}

// uiWindow is the single UI window, kept for the View menu's zoom actions.
// Menu actions fire only while Run() is blocking, so the reference is valid
// whenever zoomFont is reached.
var uiWindow webview.WebView

// zoomFont steps the terminal font size in the page (+1/-1, 0 = reset) by
// calling the hook the UI exposes for exactly this path — see catappZoom in
// menu_darwin.go for why the native menu owns ⌘+/⌘-/⌘0. The guard on the JS
// side keeps this a no-op on pages without the hook (connect form, login).
func zoomFont(delta int) {
	w := uiWindow
	if w == nil {
		return
	}
	w.Dispatch(func() {
		w.Eval(fmt.Sprintf("window.catsAdjustFont && window.catsAdjustFont(%d)", delta))
	})
}

// remoteTitle labels the window with the connected host so a thin client that
// can point anywhere shows where it is pointing. Falls back to the bare title
// if the URL won't parse.
func remoteTitle(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return windowTitle + " — " + u.Host
	}
	return windowTitle
}

// installSignalHandler reaps the backend and exits on SIGINT/SIGTERM (e.g. a
// logout or a `kill`), so a signalled quit leaves no orphaned daemons — the
// deferred cleanup in run* only fires on a normal window-close return.
func installSignalHandler() {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		runCleanup()
		os.Exit(0)
	}()
}

// showError opens a small, self-contained window describing a startup failure.
// A double-clicked .app has no console, so surfacing the reason in a window is
// the only way the user sees why nothing opened. Also logged for a dev terminal.
func showError(title, detail string) {
	log.Printf("%s: %s", title, detail)
	w := webview.New(false)
	installMenu()
	defer w.Destroy()
	w.SetTitle("Cats Mux — error")
	w.SetSize(560, 320, webview.HintFixed)
	w.SetHtml(errorPageHTML(title, detail))
	w.Run()
}
