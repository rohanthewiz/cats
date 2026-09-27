//go:build ghostty

package main

import (
	"bytes"
	"context"
	"github.com/rohanthewiz/cats/wire"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/dlog"
	"github.com/rohanthewiz/cats/internal/plugin"
)

// The plugin commands (app.Backend seam, plugins dialog). Same shape as the
// worktree commands: each Start* runs on the loop goroutine, then does its
// filesystem work on its own goroutine and posts the Responder resolution back
// onto the loop, so a slow disk (or a large plugin dir removal) can never stall
// the orchestrator.
//
// list, uninstall and check_updates live here. install/update are deliberately NOT server
// commands: they shell out to git and a build whose live output the user wants
// to watch, so the dialog spawns them as `catctl plugin …` in a fresh tab via
// tab.create — a pane is the streaming surface the app already has, and the
// server stays out of the subprocess business.

// StartPluginList scans the plugins root and answers with wire-ready entries:
// action argv already anchored to each plugin's dir, the identity env a launch
// must carry, and the resolved catctl path for the dialog's install/update tabs.
func (o *orch) StartPluginList(r app.Responder) {
	go func() {
		plugins, err := plugin.List()
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		res := app.PluginListResult{Catctl: catctlPath(), Plugins: make([]app.PluginInfo, 0, len(plugins))}
		for _, p := range plugins {
			info := app.PluginInfo{
				ID:     p.ID,
				Dir:    p.Dir,
				Linked: p.Linked,
				Source: p.Source,
				Ref:    p.Ref,
			}
			if p.Err != nil {
				// A broken entry still lists (so it can be uninstalled) but
				// carries no launchable surface.
				info.Broken = p.Err.Error()
				res.Plugins = append(res.Plugins, info)
				continue
			}
			info.Name = p.Name
			info.Version = p.Version
			info.Description = p.Description
			info.Type = p.Type
			info.Env = plugin.LaunchEnv(p)
			for _, a := range p.Actions {
				info.Actions = append(info.Actions, app.PluginActionInfo{
					ID:    a.ID,
					Title: a.Title,
					Argv:  plugin.ActionArgv(p, a),
				})
			}
			res.Plugins = append(res.Plugins, info)
		}
		// Failed defaults ride along on every list so the dialog can say why
		// a default plugin is missing. The read is best effort: a corrupt
		// state file is already reported by the seed at startup, and it must
		// not stop the dialog from listing the plugins that do work.
		failed, err := plugin.FailedDefaults()
		if err != nil {
			dlog.Warnf("catway: plugin.list: default plugins: %v", err)
		}
		for _, f := range failed {
			res.FailedDefaults = append(res.FailedDefaults, wire.PluginFailedDefault{
				ID:       f.ID,
				Source:   f.Source,
				Attempts: f.Attempts,
				GaveUp:   f.GaveUp,
				Error:    f.Err,
				Output:   f.Output,
			})
		}
		o.post(func() { r.OK(res) })
	}()
}

// StartPluginDismissDefault answers plugin.dismiss_default. It runs off the
// loop because DismissDefault waits for a seed pass that is still running
// (see plugin.defaultsMu), and on a fresh install's first start that pass can
// last as long as a clone plus a build.
func (o *orch) StartPluginDismissDefault(r app.Responder, p wire.PluginDismissDefaultParams) {
	go func() {
		if err := plugin.DismissDefault(p.ID); err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		o.post(func() { r.OK(nil) })
	}()
}

// StartPluginUninstall removes an installed plugin's directory (or unlinks a
// linked one) and answers with the same human outcome line the CLI prints.
func (o *orch) StartPluginUninstall(r app.Responder, p app.PluginUninstallParams) {
	go func() {
		msg, err := plugin.Uninstall(p.ID)
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		o.post(func() { r.OK(app.PluginUninstallResult{Message: msg}) })
	}()
}

// Update checks. Each one is a git round trip per plugin, and the front-end
// asks on every connect, on an hourly timer, and whenever the dialog opens —
// from every window. The cache below is what makes that polling free: an ask
// inside the TTL is answered from memory, so N windows cost one round of
// remote traffic per TTL, not N.
//
// Entries are keyed by plugin id but only honored while the installed commit
// and pinned ref still match what was checked. That is what makes the cache
// safe to keep across an update: `catctl plugin update` moves HEAD, the next
// ask sees a different commit and re-checks, and the "update available" badge
// clears without anyone having to invalidate anything.
const (
	// pluginCheckTTL: how long a verdict stands. Plugin releases are a
	// days-scale event; half an hour keeps the badge timely without polling
	// GitHub from every server all day.
	pluginCheckTTL = 30 * time.Minute
	// pluginCheckErrTTL: failures age out faster, so a laptop that was offline
	// when it woke up does not show "check failed" for the next half hour.
	pluginCheckErrTTL = 2 * time.Minute
	// pluginCheckTimeout bounds one plugin's whole check (ls-remote plus the
	// optional version fetch). A hung remote must still produce an answer.
	pluginCheckTimeout = 20 * time.Second
	// pluginCheckWorkers caps concurrent git processes: enough to make a
	// dozen plugins finish in a couple of round trips, not so many that a big
	// plugin set fans out into a burst of simultaneous fetches.
	pluginCheckWorkers = 4
)

type pluginCheckEntry struct {
	commit string // installed HEAD when checked
	ref    string // pinned ref when checked
	at     time.Time
	info   app.PluginUpdateInfo
}

var pluginChecks = struct {
	sync.Mutex
	m map[string]pluginCheckEntry
}{m: map[string]pluginCheckEntry{}}

// StartPluginCheckUpdates answers plugin.check_updates: every installed plugin
// (or just p.IDs) gets a verdict, from the cache when a fresh one exists and
// from its git remote otherwise (always, with p.Force). Linked and broken
// entries are answered as skipped without touching git or the cache.
func (o *orch) StartPluginCheckUpdates(r app.Responder, p app.PluginCheckUpdatesParams) {
	go func() {
		plugins, err := plugin.List()
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		want := map[string]bool{}
		for _, id := range p.IDs {
			want[id] = true
		}
		var targets []plugin.Installed
		for _, inst := range plugins {
			if len(want) == 0 || want[inst.ID] {
				targets = append(targets, inst)
			}
		}

		// Results land by index so the reply keeps List's sorted order no
		// matter which worker finishes first.
		infos := make([]app.PluginUpdateInfo, len(targets))
		sem := make(chan struct{}, pluginCheckWorkers)
		var wg sync.WaitGroup
		for i, inst := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				infos[i] = checkPluginCached(inst, p.Force)
			}()
		}
		wg.Wait()

		res := app.PluginCheckUpdatesResult{Plugins: infos}
		for _, info := range infos {
			if info.Status == plugin.UpdateAvailable {
				res.Available++
			}
		}
		o.post(func() { r.OK(res) })
	}()
}

// checkPluginCached is one plugin's verdict, reusing a cached one when it is
// still fresh and still about the same commit + ref (see the cache note).
func checkPluginCached(inst plugin.Installed, force bool) app.PluginUpdateInfo {
	// Nothing to compare against → no git, no cache entry; CheckUpdate
	// classifies these without I/O beyond a stat.
	if inst.Err != nil || inst.Linked {
		return pluginUpdateInfo(plugin.CheckUpdate(context.Background(), inst), time.Now())
	}
	commit, _ := plugin.HeadCommit(inst) // "" on failure simply never matches a cached entry
	if !force && commit != "" {
		pluginChecks.Lock()
		e, ok := pluginChecks.m[inst.ID]
		pluginChecks.Unlock()
		ttl := pluginCheckTTL
		if e.info.Status == plugin.UpdateCheckFailed {
			ttl = pluginCheckErrTTL
		}
		if ok && e.commit == commit && e.ref == inst.Ref && time.Since(e.at) < ttl {
			return e.info
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), pluginCheckTimeout)
	defer cancel()
	now := time.Now()
	info := pluginUpdateInfo(plugin.CheckUpdate(ctx, inst), now)
	if commit != "" {
		pluginChecks.Lock()
		pluginChecks.m[inst.ID] = pluginCheckEntry{commit: commit, ref: inst.Ref, at: now, info: info}
		pluginChecks.Unlock()
	}
	return info
}

// pluginUpdateInfo shapes a host verdict for the wire: SHAs shortened to the
// 7 characters git itself shows, the check time in Unix ms.
func pluginUpdateInfo(c plugin.UpdateCheck, at time.Time) app.PluginUpdateInfo {
	short := func(sha string) string {
		if len(sha) > 7 {
			return sha[:7]
		}
		return sha
	}
	return app.PluginUpdateInfo{
		ID:             c.ID,
		Status:         c.Status,
		Reason:         c.Reason,
		CurrentVersion: c.LocalVersion,
		LatestVersion:  c.RemoteVersion,
		CurrentCommit:  short(c.LocalSHA),
		LatestCommit:   short(c.RemoteSHA),
		LatestSubject:  c.RemoteSubject,
		CheckedAt:      at.UnixMilli(),
	}
}

// catctlPath resolves the catctl binary the plugins dialog spawns for
// install/update tabs. Order: an explicit CATS_CATCTL override, the canonical
// name on PATH, then a sibling of the running server binary (make binaries and
// make local both drop catway and catctl into the same directory). The
// bare-name fallback makes a failed spawn at least say what was missing.
func catctlPath() string {
	if p := os.Getenv("CATS_CATCTL"); p != "" {
		return p
	}
	if p, err := exec.LookPath("catctl"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "catctl")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return "catctl"
}

// seedDefaultPlugins runs the first-run plugin seed (plugin.SeedDefaults) and
// logs what it did. It runs once per catway start, on its own goroutine:
// seeding is a git clone plus a `go build`, seconds to a minute of work that
// must never hold up the server coming up. Nothing waits on it. The browser
// reads plugin.list on demand (dialog open, picker open), so a plugin that
// lands mid-session simply shows up the next time either is opened.
//
// It lives in catway, not in catapp or an installer script, because catway is
// the one process every kind of cats install runs: the Mac app, `make local`,
// a dist tarball on a Linux server. Plugins also live on catway's side of the
// wire, so a thin client pointed at a remote server seeds that server, which
// is where the plugin would run.
func seedDefaultPlugins() {
	var out bytes.Buffer
	outcomes, err := plugin.SeedDefaults(plugin.Defaults, &out)
	if err != nil {
		dlog.Warnf("catway: default plugins: %v", err)
		return
	}
	for _, o := range outcomes {
		switch {
		case o.Installed:
			log.Printf("catway: installed default plugin %s v%s", o.Source, o.Version)
		case o.Present:
			// Installed by other means since the seed was planned. Not news.
		case o.GaveUp:
			dlog.Warnf("catway: default plugin %s not installed after %d attempts, giving up (install it later with `catctl plugin install %s`): %v%s",
				o.Source, o.Attempt, o.Source, o.Err, outputTail(out.String()))
		default:
			dlog.Warnf("catway: default plugin %s not installed (attempt %d, will retry on next start): %v%s",
				o.Source, o.Attempt, o.Err, outputTail(out.String()))
		}
	}
}

// outputTail keeps the last few lines of clone/build output for a log line:
// enough to show why a build failed (e.g. `go: command not found`) without
// pasting the whole build into daemons.log.
func outputTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return " | " + strings.Join(lines, " | ")
}
