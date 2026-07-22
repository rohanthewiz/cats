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
	windowTitle  = "cats"
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

// runRemote is the thin-client path: no local daemons, just point the window at
// a remote catway. On first run (no saved URL) it shows a small connect form;
// the bound catsConnect callback persists the entered URL and navigates the
// same window to it, so subsequent launches connect straight away.
func runRemote(cfg appConfig) {
	installSignalHandler() // no daemons to reap, but honour a clean quit uniformly

	if cfg.Remote.URL != "" {
		w := newWindow(remoteTitle(cfg.Remote.URL))
		defer w.Destroy()
		w.Navigate(cfg.Remote.URL)
		w.Run()
		return
	}

	w := newWindow(windowTitle)
	defer w.Destroy()
	if err := w.Bind("catsConnect", func(rawURL string) {
		u := strings.TrimSpace(rawURL)
		if u == "" {
			return
		}
		cfg.Remote.URL = u
		if err := saveAppConfig(cfg); err != nil {
			log.Printf("could not save connection choice: %v", err)
		}
		// Navigate on the UI thread; the callback runs off it.
		w.Dispatch(func() {
			w.SetTitle(remoteTitle(u))
			w.Navigate(u)
		})
	}); err != nil {
		showError("Could not initialise the connect form", err.Error())
		return
	}
	w.SetHtml(connectPageHTML)
	w.Run()
}

// newWindow builds the shared webview window (title + size) and installs the
// native menu bar so Cmd-Q and the standard editing shortcuts work. debug is
// false: no devtools in the shipped app.
func newWindow(title string) webview.WebView {
	w := webview.New(false)
	installMenu() // NSApp now exists (created by webview.New); menu before Run()
	w.SetTitle(title)
	w.SetSize(windowWidth, windowHeight, webview.HintNone)
	return w
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
	w.SetTitle("cats — error")
	w.SetSize(560, 320, webview.HintFixed)
	w.SetHtml(errorPageHTML(title, detail))
	w.Run()
}
