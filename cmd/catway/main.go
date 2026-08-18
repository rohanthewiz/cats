//go:build ghostty

// Command catway serves the browser front-end: it speaks the browser protocol
// (internal/browserproto, spec ai_docs/phase-c-ws9-protocol.md) over WebSocket
// and sources pane content from the cathost daemon over the β orchestration
// seam. State is owned by the WS2 orchestrator (see catway.go) — a single-owner
// event loop over an app.Session that starts with one workspace, one tab, and one
// pane; splits, tabs, and workspaces are created at runtime via the §7 command
// table (WS8). Structured key/mouse/paste input is encoded server-side via
// internal/inputenc (D4).
//
// Access control (WS10): a shared password gates the UI. Browsers sign in at
// /login and receive an HMAC-signed session cookie; headless clients present
// the password as an Authorization: Bearer token. --tls serves HTTPS with an
// auto-generated self-signed certificate (override with --tls-cert/--tls-key,
// or extend its subject alternative names with --tls-san).
//
// Build (same prerequisite as cmd/cathost — the vendored libghostty-vt,
// built once via `make vt`):
//
//	PKG_CONFIG_PATH=$PWD/third_party/libghostty-vt/zig-out/share/pkgconfig \
//	  go build -tags ghostty ./cmd/catway
//
// Run a persistent daemon first:
//
//	cathost -socket /tmp/cats-cathost.sock -persistent
//
// A local control API (WS4) exposes the same §7 command table over a unix socket
// for CLI/automation clients (see cmd/catctl, internal/ctlproto). It reuses the
// browser's app.Dispatcher unchanged; the socket is owner-only (0600).
//
// Usage:
//
//	catway [--addr :8421] [--socket /tmp/cats-cathost.sock] \
//	         [--control-socket /tmp/cats-control.sock] \
//	         [--hook-socket /tmp/cats-hooks.sock] \
//	         [--auth password|none] [--password SECRET] [--session-ttl 24h] \
//	         [--tls] [--tls-cert cert.pem] [--tls-key key.pem] [--tls-san NAME,…] \
//	         [--persist=false] [--state-dir DIR] [--push-url URL]
//
// Push notifications (--push-url, or the config's push section) POST every
// agent notification to an ntfy-shaped webhook, so a phone reachable nowhere
// near this machine still learns that an agent is blocked. It is outbound-only
// and independent of every client-facing path — it keeps working when no
// browser is connected at all.
//
// Session persistence (WS3) is on by default: the workspace/tab/pane model is
// saved to $XDG_STATE_HOME/cats (default ~/.local/state/cats) on every
// mutation and restored at startup — surviving PTYs are re-adopted from the
// persistent daemon, dead ones re-spawned with their captured scrollback
// replayed.
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/gwauth"
	"github.com/rohanthewiz/cats/internal/gwtls"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/persist"
	"github.com/rohanthewiz/cats/internal/push"
	"github.com/rohanthewiz/cats/internal/startdir"
	"github.com/rohanthewiz/cats/internal/worktree"
)

//go:embed web/index.html
var webFS embed.FS

func main() {
	configPath := flag.String("config", "",
		"config file path (env "+config.EnvVar+"; default ~/.config/cats/config.yaml)")
	addr := flag.String("addr", ":8421", "listen address")
	socket := flag.String("socket", "/tmp/cats-cathost.sock", "cathost daemon socket path")
	controlSocket := flag.String("control-socket", "",
		"local control-API socket path (env "+ctlproto.SocketEnvVar+"; default "+ctlproto.DefaultSocket+")")
	hookSocket := flag.String("hook-socket", "",
		"agent hook-report API socket path (default "+defaultHookSocket+`; "none" disables)`)
	authMode := flag.String("auth", "password", `auth mode: "password" (login + session cookie) or "none"`)
	allowedOrigins := flag.String("allowed-origins", "",
		"comma-separated extra WebSocket origins to accept beyond same-origin (host[:port] or full origin)")
	password := flag.String("password", "", "shared access password/token (env CATS_PASSWORD; generated if unset)")
	sessionTTL := flag.Duration("session-ttl", 24*time.Hour, "session cookie lifetime")
	useTLS := flag.Bool("tls", false, "serve HTTPS (auto self-signed cert unless --tls-cert/--tls-key given)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate PEM (implies --tls)")
	tlsKey := flag.String("tls-key", "", "TLS private key PEM (implies --tls)")
	tlsSANs := flag.String("tls-san", "",
		"comma-separated extra names/IPs for the auto-generated certificate, e.g. cats.lan (implies --tls)")
	persistOn := flag.Bool("persist", true, "persist and restore session state (WS3)")
	stateDir := flag.String("state-dir", "", "session state directory (default $XDG_STATE_HOME/cats)")
	pushURL := flag.String("push-url", "",
		"push-notification webhook (ntfy topic URL); enables the push bridge (token: env CATS_PUSH_TOKEN)")
	flag.Parse()

	// Config precedence for server settings is flag > config file > default.
	// Start from the config (which starts from the defaults), then let any flag
	// the operator actually passed win. flag.Visit reports only explicitly-set
	// flags, so an unset flag never masks a config value with its default.
	cfg, cfgPath, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("catway: %v", err)
	}
	effTTL, err := cfg.Server.TTL()
	if err != nil {
		log.Fatalf("catway: %v", err) // Load already validated, but be explicit
	}
	eff := cfg.Server
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["addr"] {
		eff.Addr = *addr
	}
	if set["socket"] {
		eff.CathostSocket = *socket
	}
	if set["control-socket"] {
		eff.ControlSocket = *controlSocket
	}
	if set["hook-socket"] {
		eff.HookSocket = *hookSocket
	}
	if eff.HookSocket == "none" {
		eff.HookSocket = ""
	}
	if set["auth"] {
		eff.Auth = *authMode
	}
	if set["allowed-origins"] {
		eff.AllowedOrigins = splitCSV(*allowedOrigins)
	}
	if set["session-ttl"] {
		effTTL = *sessionTTL
	}
	if set["tls"] {
		eff.TLS.Enabled = *useTLS
	}
	if set["tls-cert"] {
		eff.TLS.Cert = *tlsCert
	}
	if set["tls-key"] {
		eff.TLS.Key = *tlsKey
	}
	if set["tls-san"] {
		eff.TLS.SANs = splitCSV(*tlsSANs)
	}
	effPersist := cfg.Persistence
	if set["persist"] {
		effPersist.Enabled = *persistOn
	}
	if set["state-dir"] {
		effPersist.StateDir = *stateDir
	}
	// Passing --push-url is itself the opt-in, so the operator does not have to
	// set both a URL and an enable flag; an empty value explicitly turns the
	// bridge off, overriding a config that enabled it.
	effPush := cfg.Push
	if set["push-url"] {
		effPush.URL, effPush.Enabled = *pushURL, *pushURL != ""
	}
	if err := effPush.Validate(); err != nil {
		log.Fatalf("catway: push.%v", err)
	}

	indexHTML, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("catway: read embedded page: %v", err)
	}

	// The host roster: the configured hosts with "local" synthesized from
	// server.cathost_socket (which the --socket flag may have overridden), so a
	// config with no hosts: block yields exactly the single host catway has
	// always dialed. Unlike the other server.* settings this one is not
	// restart-only: host.attach/host.detach and server.reload_config re-shape
	// the roster live (cmd/catway/hosts.go).
	hosts := cfg.EffectiveHosts(eff.CathostSocket)
	o, err := buildOrch(hosts, spawnRoot(), effPersist)
	if err != nil {
		log.Fatalf("catway: %v", err)
	}
	// Wire the config-driven served page: baseHTML + cfgPath let server.reload_config
	// re-render it; the initial render is stored for the "/" handler to serve.
	// o.cfg feeds config.get/config.set; the worktree root anchors worktree.create.
	o.baseHTML = indexHTML
	o.cfgPath = cfgPath
	o.cfg = cfg
	o.worktreeDir = worktree.ExpandTilde(cfg.Worktrees.Directory)
	// Outbound push bridge: an agent that blocks while nobody is watching still
	// reaches a phone. Deliberately independent of every client-facing path — it
	// is an ordinary outbound POST, so it keeps working when no client is
	// connected at all. push.New returns nil when unconfigured, and Send is
	// nil-safe, so this assignment is unconditional.
	if effPush.Enabled {
		o.push = push.New(push.Config{
			URL:         effPush.URL,
			Token:       resolvePushToken(),
			Kinds:       effPush.KindSet(),
			Priority:    effPush.Priority,
			ClickURL:    effPush.ClickURL,
			MinInterval: mustPushInterval(effPush),
		})
		log.Printf("catway: push notifications enabled (%s)", pushHostOf(effPush.URL))
	}
	initialPage := renderPage(indexHTML, cfg)
	o.page.Store(&initialPage)
	if cfgPath != "" {
		log.Printf("catway: config %s", cfgPath)
	}

	// Local control API (WS4): a CLI/automation client drives the same §7 command
	// table as the browser over a unix socket. Listen failure is non-fatal — the
	// browser front-end works without it. cleanup unlinks the socket on stop.
	// o.controlSocket must be set before the loop starts — createPane exports
	// it to every pane as CATS_CONTROL_SOCKET for in-pane automation clients.
	o.controlSocket = ctlproto.ResolveSocket(eff.ControlSocket)
	controlCleanup, err := serveControl(o, o.controlSocket)
	if err != nil {
		log.Printf("catway: control API disabled: %v", err)
		o.controlSocket = "" // don't point panes at a socket nobody serves
		controlCleanup = func() {}
	}

	// Hook-report API: installed agent integrations (catctl integration
	// install) report state/session transitions here. o.hookSocket must be set
	// before the loop starts — createPane injects it into every pane's env.
	o.hookSocket = eff.HookSocket
	hooksCleanup, err := serveHooks(o, eff.HookSocket)
	if err != nil {
		log.Printf("catway: hook-report API disabled: %v", err)
		o.hookSocket = "" // don't point panes at a socket nobody serves
		hooksCleanup = func() {}
	}

	// Process-exit hook, fired by orch.Shutdown (server.stop command or a
	// SIGINT/SIGTERM) after the state save + final capture: rweb has no graceful
	// shutdown, so exit after a short grace period that lets the final
	// cmd_result + shutdown broadcast flush to browsers. The persistent cathost
	// daemon is separate and survives.
	o.stop = func() {
		log.Printf("catway: shutting down — session state saved; cathost daemon survives")
		controlCleanup()
		hooksCleanup()
		time.AfterFunc(250*time.Millisecond, func() { os.Exit(0) })
	}

	// A clean quit (Ctrl-C / SIGTERM) routes through the same graceful path as
	// server.stop: save the model, run the bounded final scrollback capture,
	// then exit. A second signal force-quits.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigc
		o.post(func() { o.Shutdown() })
		<-sigc
		os.Exit(1)
	}()

	go o.run() // the orchestrator event loop (sole state owner)
	// One dial loop per host: each reconnects on its own schedule, so a host
	// that is down never delays or interrupts the others.
	for _, d := range o.hosts {
		go d.run()
	}
	if o.historyPath != "" {
		go o.runHistoryCapture() // periodic scrollback sweep for cold-restore seeds
	}
	go o.runAgentModels()  // periodic re-read of each agent pane's current model
	go o.runPaneBranches() // periodic re-read of each pane's checked-out git branch
	go o.runUsage()        // periodic re-read of the account's rate-limit windows

	// TLS: operator PEMs, or an auto-generated self-signed pair. Naming a cert,
	// a key, or a SAN is itself the opt-in — each is meaningless without HTTPS,
	// so requiring --tls alongside would only be a way to get it wrong.
	tlsOn := eff.TLS.Enabled || eff.TLS.Cert != "" || eff.TLS.Key != "" || len(eff.TLS.SANs) > 0
	var tlsCfg rweb.TLSCfg
	// certPath outlives the block: the pairing payload carries its fingerprint so
	// a phone can pin the self-signed certificate on first use.
	var certPath string
	if tlsOn {
		cert, keyPath, err := resolveTLS(eff.TLS.Cert, eff.TLS.Key, eff.TLS.SANs)
		if err != nil {
			log.Fatalf("catway: tls: %v", err)
		}
		certPath = cert
		tlsCfg = rweb.TLSCfg{UseTLS: true, TLSAddr: eff.Addr, CertFile: certPath, KeyFile: keyPath}
	}

	// Auth: build the guard unless explicitly disabled.
	guard, err := buildGuard(eff.Auth, *password, effTTL, tlsOn, eff.AllowedOrigins)
	if err != nil {
		log.Fatalf("catway: auth: %v", err)
	}
	// Device pairing (catctl pair) needs the guard's authenticator plus the URL
	// and certificate pin a phone will dial, so it can only be assembled once
	// both are resolved — after the control socket is already listening, which is
	// why orch holds it atomically. nil under --auth none: there is nothing to
	// pair with, and catctl pair says so.
	o.pairing.Store(buildPairing(guard, eff.Addr, certPath))

	s := rweb.NewServer(rweb.ServerOptions{Address: eff.Addr, TLS: tlsCfg, Verbose: true})
	if guard != nil {
		s.Use(guard.middleware)
		s.Get("/login", guard.handleLoginGet)
		s.Post("/login", guard.handleLoginPost)
	}
	s.Get("/", func(ctx rweb.Context) error {
		return ctx.WriteHTML(string(*o.page.Load()))
	})
	s.WebSocket("/ws", func(ws *rweb.WSConn) error {
		return o.serve(ws)
	})

	scheme := "http"
	if tlsOn {
		scheme = "https"
	}
	log.Printf("catway: serving at %s://localhost%s (cathost socket %s)", scheme, eff.Addr, eff.CathostSocket)
	if len(hosts) > 1 {
		// Only worth a line when there is something to say: with one host the
		// socket is already in the line above.
		for _, h := range hosts {
			def := ""
			if h.Default {
				def = " (default)"
			}
			log.Printf("catway: host %s [%s] %s%s", h.ID, h.DisplayLabel(), h.Addr, def)
		}
	}
	if err := s.Run(); err != nil {
		log.Fatalf("catway: %v", err)
	}
	// rweb installs its own SIGINT/SIGTERM handler and returns nil from Run on a
	// signal. The signal goroutine above got the same signal and is driving the
	// graceful shutdown (save + final capture → os.Exit); block until it does.
	select {}
}

// spawnRoot is the directory new panes spawn in when nothing more specific
// applies (no workspace identity cwd, no restored/override cwd): the process
// cwd, except that a GUI launch (Finder, `open`, launchd) hands us "/" — a
// terminal that opens at the filesystem root is useless, so fall back to the
// user's home there. A catway started from a shell keeps that shell's
// directory, which is the whole point of `cd project && catway`.
func spawnRoot() string {
	cwd, _ := os.Getwd()
	return startdir.Usable(cwd)
}

// healStartDirs repairs a restored session's spawn directories against root.
// The snapshot outlives the launch that produced it: a session first saved from
// a GUI launch (before spawnRoot existed, or from a build that predates it)
// carries "/" as its session cwd and in every workspace identity, and a
// directory can simply be deleted between runs. Restore is a pure model
// conversion, so the repair belongs here — without it a single bad launch
// pins every future workspace to the filesystem root forever.
//
// hosts scopes the workspace half to this machine: a workspace pinned to a
// remote host holds a path in *that* machine's filesystem, where every test
// startdir.Usable makes — does it exist, is it a directory — is being asked of
// the wrong disk. Rewriting it to this session's cwd would silently move a
// remote workspace's panes to a directory that only exists here, so those are
// left exactly as saved (a bad remote path is cathost's fallback to handle).
func healStartDirs(sess *app.Session, root string, hosts []config.Host) {
	if cwd := startdir.Usable(sess.Cwd(), root); cwd != sess.Cwd() {
		log.Printf("catway: restored session cwd %q unusable, using %q", sess.Cwd(), cwd)
		sess.SetCwd(cwd)
	}
	for _, ws := range sess.Workspaces() {
		if !hostIsLocal(hosts, ws.HostID) {
			continue
		}
		ws.IdentityCwd = startdir.Usable(ws.IdentityCwd, sess.Cwd())
	}
}

// hostIsLocal answers "is this stored host id this machine's own cathost", with
// "" meaning the roster's default host. It exists alongside orch.paneIsLocal
// rather than reusing it because the restore-time repairs below run *before*
// the orchestrator is constructed — the session model and the configured roster
// are all that exist at that point.
func hostIsLocal(hosts []config.Host, id string) bool {
	if id == "" {
		for _, h := range hosts {
			if h.Default {
				id = h.ID
				break
			}
		}
		if id == "" && len(hosts) > 0 {
			id = hosts[0].ID // EffectiveHosts always marks one; never depend on it
		}
	}
	return id == config.LocalHostID
}

// healPaneCwds drops saved per-pane directories that are no longer worth
// respawning in (the same "/" and deleted-directory cases healStartDirs covers).
// A dropped entry is not an error: createPane simply falls back to the pane's
// workspace identity cwd, which healStartDirs has already made usable.
//
// A remote pane's saved cwd is kept untouched, for the reason healStartDirs
// gives: it was reported by a shell on another machine, so the local stat that
// decides "worth respawning in" is being asked of the wrong filesystem — and
// its answer is nearly always "no", which would drop every remote pane's
// directory on every restart.
func healPaneCwds(cwds map[uint32]string, sess *app.Session, hosts []config.Host) map[uint32]string {
	for pid, cwd := range cwds {
		if !hostIsLocal(hosts, sess.PaneHost(layout.PaneID(pid))) {
			continue
		}
		if startdir.Usable(cwd) != cwd {
			delete(cwds, pid)
		}
	}
	return cwds
}

// buildOrch constructs the orchestrator, restoring the persisted session when
// persistence is on and a usable snapshot exists (WS3). Any load/restore
// problem beyond "no file yet" is logged and falls back to a fresh session —
// never a dead catway. Scrollback seeds and saved cwds are installed only
// when the model itself restored: against a fresh session their pane ids would
// collide with newly allocated ones.
func buildOrch(hosts []config.Host, cwd string, pc config.Persistence) (*orch, error) {
	if !pc.Enabled {
		return newOrchHosts(hosts, cwd)
	}
	dir := pc.StateDir
	if dir == "" {
		dir = persist.DefaultDir()
	}
	if dir == "" {
		log.Printf("catway: persistence disabled — no resolvable state dir")
		return newOrchHosts(hosts, cwd)
	}
	sessionPath, historyPath := persist.SessionPath(dir), persist.HistoryPath(dir)

	var sess *app.Session
	var savedCwds map[uint32]string
	var savedAgents map[uint32]persist.AgentSession
	snap, cwds, paneAgents, err := persist.LoadSession(sessionPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// first run — start fresh, silently
	case err != nil:
		log.Printf("catway: session state unusable, starting fresh: %v", err)
	default:
		if sess, err = app.RestoreSession(modelSpawner{}, snap); err != nil {
			log.Printf("catway: session restore failed, starting fresh: %v", err)
			sess = nil
		} else {
			healStartDirs(sess, cwd, hosts)
			savedCwds = healPaneCwds(cwds, sess, hosts)
			savedAgents = paneAgents
			total := len(snap.Workspaces)
			log.Printf("catway: restored session from %s (%d workspaces, %d panes)",
				sessionPath, total, len(sess.AllPaneIDs()))
		}
	}
	if sess == nil {
		o, err := newOrchHosts(hosts, cwd)
		if err != nil {
			return nil, err
		}
		o.sessionPath, o.historyPath = sessionPath, historyPath
		o.histLines = uint32(pc.HistoryLines)
		return o, nil
	}

	o, err := newOrchHostsWith(hosts, cwd, sess)
	if err != nil {
		return nil, err
	}
	o.sessionPath, o.historyPath = sessionPath, historyPath
	o.histLines = uint32(pc.HistoryLines)
	o.restoredCwds = savedCwds
	if seeds, err := persist.LoadHistory(historyPath); err == nil {
		o.seeds = seeds
		o.capturedHist = maps.Clone(seeds) // partial sweeps must not wipe other panes' seeds
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("catway: history state unusable, skipping scrollback seeds: %v", err)
	}
	// Agent resume (resume.go): validate the saved session refs, plan each
	// cold-start pane's resume argv, and drop the saved scrollback of every
	// resuming pane — the relaunched agent owns that screen, and replaying a
	// stale transcript under it would masquerade as live output.
	kept, plans, suppress := planResume(savedAgents, pc.ResumeAgents, o.paneIsLocal)
	o.restoredAgents, o.resumePlans = kept, plans
	for pid := range suppress {
		delete(o.seeds, pid)
	}
	if n := len(plans); n > 0 {
		log.Printf("catway: %d agent session(s) eligible for resume on cold start", n)
	}
	return o, nil
}

// buildGuard constructs the auth guard for the chosen mode. "none" returns a
// nil guard (no middleware). "password" resolves the shared secret (flag → env
// → generated) and logs a generated one so the operator can find it.
// allowedOrigins is the extra WebSocket Origin allow-list (see gwauth.OriginOK).
func buildGuard(mode, password string, ttl time.Duration, tlsOn bool, allowedOrigins []string) (*authGuard, error) {
	switch mode {
	case "none":
		log.Printf("catway: WARNING auth disabled (--auth none) — anyone who can reach the listen address can drive your terminals")
		return nil, nil
	case "password":
		secret, generated, err := resolveSecret(password)
		if err != nil {
			return nil, err
		}
		a, err := gwauth.New(secret, ttl)
		if err != nil {
			return nil, err
		}
		if generated {
			log.Printf("catway: no --password/CATS_PASSWORD set; generated access password: %s", secret)
		}
		return &authGuard{a: a, secure: tlsOn, allowedOrigins: allowedOrigins}, nil
	default:
		return nil, fmt.Errorf("unknown --auth %q (want password|none)", mode)
	}
}

// splitCSV parses a comma-separated flag value into a trimmed, non-empty slice.
// Returns nil for an empty/whitespace-only value so it matches an unset config.
// mustPushInterval is the parsed debounce window. Both config.Load and the
// explicit Validate above have already rejected an unparseable value, so a
// failure here is impossible; falling back to no debounce (rather than
// panicking) keeps a future refactor that skips a validation step from taking
// the server down over a notification setting.
func mustPushInterval(p config.Push) time.Duration {
	d, err := p.Interval()
	if err != nil {
		return 0
	}
	return d
}

// pushHostOf is the webhook's host for the startup log. Deliberately not the
// full URL: an ntfy topic path is a capability (anyone who reads it can publish
// to and subscribe to your notifications), and catway's log is routinely pasted
// into issues.
func pushHostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "configured"
	}
	return u.Host
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveTLS returns the cert/key PEM paths to serve: the operator's files if
// both are given, otherwise an auto-generated self-signed pair cached under the
// user config dir (~/.config/cats or the platform equivalent). sans are extra
// names the generated certificate must cover; adding one re-mints it.
func resolveTLS(certFlag, keyFlag string, sans []string) (certPath, keyPath string, err error) {
	if certFlag != "" && keyFlag != "" {
		// Say so rather than ignoring them. The operator asked for a name to be
		// covered; silence here reads as "done" and the trust warning that
		// follows would look like a bug in the SAN handling.
		if len(sans) > 0 {
			log.Printf("catway: ignoring tls.sans %v — they only apply to the auto-generated certificate, "+
				"and --tls-cert/--tls-key were supplied", sans)
		}
		return certFlag, keyFlag, nil
	}
	if certFlag != "" || keyFlag != "" {
		return "", "", fmt.Errorf("--tls-cert and --tls-key must be given together")
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("locate config dir: %w", err)
	}
	dir := filepath.Join(cfgDir, "cats")
	certPath, keyPath, err = gwtls.EnsureSelfSigned(dir, sans)
	if err != nil {
		return "", "", err
	}
	if len(sans) > 0 {
		log.Printf("catway: using self-signed TLS certificate in %s covering %v (browsers warn on first connect)",
			dir, sans)
	} else {
		log.Printf("catway: using self-signed TLS certificate in %s (browsers warn on first connect)", dir)
	}
	return certPath, keyPath, nil
}
