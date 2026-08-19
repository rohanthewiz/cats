//go:build darwin

// Command catapp is the native desktop launcher for cats: a thin Go
// supervisor around one or more WebKit windows (window_darwin.m). It has two
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
// Either mode shows the UI in NATIVE windows (window_darwin.{m,go}): one
// NSWindow + WKWebView each, all over one catway session. A window is a view on
// one workspace — the server has treated every connection that way since
// multi-window — so "a window per project on the second monitor" is a URL
// (`?ws=<id>`) plus a window to put it in. webview_go remains only for the
// startup error sheet, which is modal, single-window, and happens before any of
// this exists.
//
// The launcher itself is plain Go (no -tags ghostty) — it only supervises
// processes and shows windows; the terminal/VT work lives entirely in the
// bundled catway/cathost binaries. It is macOS-only (WebKit + a native menu
// via cgo), hence the darwin build constraint on every file in this package.
package main

import (
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
// The native shell has its own copy of the size (kDefaultW/kDefaultH in
// window_darwin.m, which is where a window is actually built); these stay as the
// Go-side statement of the same default and as the title every window opens
// with.
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
// loopback. The backend is reaped when the LAST window closes (the app
// delegate's applicationShouldTerminateAfterLastWindowClosed), on a Cmd-Q, or
// on a termination signal — all routed through runCleanup.
func runLocal(cfg appConfig) {
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

	// The window set from the last run, or one window on a first run. A saved
	// window whose workspace has since been closed still opens: the server
	// resolves an unknown ?ws= to the primary view rather than erroring, so a
	// window layout survives the projects it was made for.
	m := startWindowShell("http://"+b.addr, windowTitle)
	m.restore(cfg.Windows)
	m.run()
}

// runRemote is the thin-client path: no local daemons, just windows pointed at
// a catway.
//
// It keeps a list of them (appConfig.Presets) rather than one address, and a
// Connect menu to move between them. Switching catways is a navigation of the
// windows already open, not a relaunch, so the session cookie WebKit holds for
// each host survives being away from it — and the shared website data store
// means one login serves every window.
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

	m := startWindowShell(cfg.Remote.URL, windowTitle)
	if cfg.Remote.URL != "" {
		m.setTitle(remoteTitle(cfg.Remote.URL))
		m.restore(cfg.Windows)
		rt.refreshMenu()
	} else {
		// First run: no catway to restore windows onto, so the connect form is
		// the whole app until there is one.
		rt.showConnect()
	}
	m.run()
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

// forget drops a target from the list. The windows are left where they are —
// see appConfig.removePreset for why forgetting is not disconnecting.
func (r *remoteRuntime) forget(rawURL string) {
	r.cfg.removePreset(rawURL)
	r.save()
	r.showConnect()
}

// cancel goes back to the live session from the connect form, when there is one
// to go back to.
func (r *remoteRuntime) cancel() {
	if r.cfg.Remote.URL != "" {
		r.show(r.cfg.Remote.URL)
	}
}

// show points EVERY window at a catway and re-titles them. All of them, because
// a different catway is a different session: leaving one window on the old one
// would put two servers' workspaces on screen with nothing to tell them apart.
// Hopped onto the main thread, because the callers are a page message and a
// menu action.
func (r *remoteRuntime) show(rawURL string) {
	onMainThread(func() {
		r.refreshMenu()
		if windows != nil {
			windows.navigateAll(rawURL, remoteTitle(rawURL))
		}
	})
}

// showConnect renders the picker in the front window. Cancelling is offered
// only when there is a session to go back to, so a first run cannot end up on a
// page with a button that does nothing.
func (r *remoteRuntime) showConnect() {
	onMainThread(func() {
		r.refreshMenu()
		if windows != nil {
			windows.showHTML(connectPage(r.cfg.Presets, r.cfg.Remote.URL, r.cfg.Remote.URL != ""),
				windowTitle)
		}
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

// zoomFont steps the terminal font size (+1/-1, 0 = reset) in the FRONT
// window's page, by calling the hook the UI exposes for exactly this path — see
// catappZoom in menu_darwin.go for why the native menu owns ⌘+/⌘-/⌘0. The guard
// on the JS side keeps this a no-op on pages without the hook (connect form,
// login).
//
// "The front window", not "the window": with several windows open, a
// process-global reference would zoom whichever one happened to be stored,
// which is the window you are not looking at as often as not.
func zoomFont(delta int) {
	if windows == nil {
		return
	}
	onMainThread(func() { windows.zoomKeyWindow(delta) })
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
