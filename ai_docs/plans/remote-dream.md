# cats "Remote dream" — multi-host seam + host roster (slice 1 of the catalog)

## Context

cats is a browser-presented terminal workspace over its **own WS protocol (not SSH)**: catway
(WS edge + orchestrator) ↔ exactly one cathost (PTYs + VT), joined by a unix socket with no
auth. Remote use today = point a browser at a catway on LAN/Tailscale. There is no host
roster, no way to run panes on more than one machine, and the yamux relay is unbuilt. The
"Remote dream" (`.cats-todo/todos.json` item 0: hosts/sessions, remote cmd history,
scripts, ced) all becomes *cross-host* for free once catway can attach to N cathosts — so
this slice is the foundation. Appendix A keeps the full brainstorm catalog for the later slices.

Goal of this slice: **one catway attached to N cathosts ("hosts")**, a Hosts section in the
left aside, a host badge on each pane, a host picker when creating panes/tabs/workspaces,
restore-on-the-right-host, and (later phase) a native TLS+token transport so no ssh
port-forward is needed. Single-host users must see zero change with zero config edits.

## Verified facts that shape the design

- `cmd/catway/daemon.go`: `daemon{o, socket, mu, conn}` — **one global `orch.daemon`**;
  `run()` hardcodes `net.DialTimeout("unix", …)`; `session()` strict `ProtocolVersion` equality;
  `reconcile()` iterates *all* `o.session.AllPaneIDs()` and closes every alive pane not in the
  model (must become host-scoped); disconnect calls global `flushPending/flushWaiters` and
  broadcasts `NewError(0, "cathost connection lost")`.
- ~25 `o.daemon.send/connected` sites in `cmd/catway/catway.go` (536, 565, 596, 635, 698, 745,
  755, 885-903, 1185, 1232, 1259-1269, 1286 `DaemonConnected`, 1755-1767, 1828-1871),
  `persist.go:148,161`, `daemon.go:338`, `main.go:267 go o.daemon.run()`.
- `paneRuntime` (`catway.go:42`) has no host; `orch.panes map[uint32]*paneRuntime`.
- Pane ids are catway-allocated (`internal/layout.AllocPaneID`, global atomic) → already unique
  across hosts; cathost trusts them. Good.
- `internal/orchestration/host.go:436 handleHello` **ignores the Hello payload** (no version /
  token check host-side); unknown JSON fields are ignored, so `Hello{token}` is harmless to v2.
- `host.go:506 create_pane` with a non-existent `Cwd` → dead pane. Remote hosts will receive
  catway-machine paths (`o.cwd`, `IdentityCwd`, inherited neighbour cwd) → cathost-side cwd
  fallback is mandatory before any remote host ships.
- Local-FS assumptions in catway: `main.go:349-370 healStartDirs/healPaneCwds`,
  `internal/app/commands.go:971/983/1009 inheritedSplitCwd/inheritedTabCwd/workspaceStartDir`,
  `cmd/catway/gitbranch.go`, `worktrees.go`, `pathpick` (`path.list`), `agentmodel.go`,
  `resume.go`, host meters (`hostcpu/hostmem/hostdisk.go` → `usage.go:432 hostUsageGroup`).
- Model: `internal/workspace/spawner.go SpawnSpec{PaneID,Rows,Cols,Cwd,Argv,Command,ExtraEnv,
  PublicPaneID}`; `PaneState{AttachedTerminalID,Seen,CustomName}`; `persist.go restoreTab`
  respawns with `SpawnSpec{PaneID,Cwd:wsnap.IdentityCwd,PublicPaneID}`.
- `internal/persist`: `Version=1`, additive `omitempty` fields, no migrations (how `PaneCwds`
  and `PaneAgents` were added). Same style here.
- Wire-struct changes ⇒ regen `cmd/catgen-dart/testdata/golden` (`go run ./cmd/catgen-dart -out
  cmd/catgen-dart/testdata/golden`) then copy to `../cats-mobile/packages/catsproto/lib/src/generated`
  (see memory: cats-mobile regen flow). New §7 command = const + `commandSpecs` + `Dispatch`
  case (`TestCommandSpecsRouted`).
- Reusable: `internal/gwtls.EnsureSelfSigned/Fingerprint`, `internal/gwauth` bearer/pair helpers.
- Tests that hardcode one daemon: `cmd/catway/persist_test.go` (`pipeDaemon`, `o.daemon.setConn`),
  `resume_test.go:253 o.daemon.reconcile`, `multiclient/waiter/commands/pending_test.go`,
  `internal/app/{commands,session}_test.go` Backend fakes.

## Key decisions

| # | Decision | Choice |
|---|---|---|
| 1 | Host config | Top-level `hosts:` list in `config.yaml`: `Host{ID, Label, Addr, Token, TokenFile, Fingerprint string; Default bool}`, `addr` = `unix://path` \| `tcp://h:p` \| `tls://h:p`. `local` is **always synthesized** from `server.cathost_socket` (label = hostname) unless overridden; absent `hosts:` ⇒ `local` is the only + default host. Restart-only at first (like all `server.*`); hot attach/detach is Phase 5. |
| 2 | Transport/auth | Phase 1-3 need none: `unix://` over `ssh -L /tmp/devbox.sock:/tmp/cats-cathost.sock` already gives a real remote. Phase 4 adds native `tls://` (cathost self-signed via `gwtls`, catway pins by fingerprint via `VerifyPeerCertificate`) + token in `Hello.Token`; `tcp://` allowed only on loopback binds. `ProtocolVersion=3`, `MinProtocolVersion=2`; cathost `handleHello` now parses/rejects; catway accepts `Min ≤ peer ≤ Current` and records `peerVersion`. |
| 3 | Refactor shape | `orch.daemon` → `orch.hosts map[string]*daemon` + `orch.defaultHost`; `paneRuntime.host string`; `daemon{id,label,kind,dial func()(net.Conn,error),peerVersion}`; helpers `o.hostOf(rt)`, `o.hostForPane(pid)` returning a nil-safe `nopDaemon` (send drops, connected false). `reconcile` scoped to `rt.host == d.id`. `flushPendingFor/flushWaitersFor(hostID, reason)`. Keep `Backend.DaemonConnected()` (= default host) and add `PaneHostConnected(pane) bool` for the read/capture/wait gates. Unknown/vanished host on a restored pane ⇒ fall back to default host + error toast (never a permanent black pane). |
| 4 | Where HostID lives | All four, from Phase 1, `""` ⇒ default: `workspace.PaneState.HostID`, `PaneSnapshot.HostID json:"host,omitempty"`, `SpawnSpec.HostID`, `Workspace.HostID` (+`Snapshot.HostID`) as the default for new panes in that workspace. Per-pane is where restore truth must live; workspace-level is one extra field and gives the simple UI. |
| 5 | Browser/§7 surface | `PaneRectInfo.Host` + `WorkspaceInfo.Host` (omitempty); new down-msg `hosts {items:[HostItem{id,label,connected,addr_kind,default,panes,error?}]}`; `Host` param on `pane.split` / `tab.create` / `workspace.create`; new §7 `host.list` (and `host.attach/detach` in Phase 5, §7 like `config.set` — browser users are authenticated owners). Roster section and badges are hidden when only one host exists. |
| 6 | Local-FS helpers | Nothing moves in Phase 1. When remote becomes possible (Phase 2/3): guard catway-side (heal*, startdir, inherit-cwd only same-host, branch/agentmodel/resume skip non-local, worktrees + `path.list` local-only with a clear error/hidden picker). Phase 4 moves branch resolution and cwd fallback into cathost (`pane_branch` event). Meters/`path.list`-remote/hook relay = Phase 6. `clipboard.read` stays as-is (it's the UI machine's clipboard). |

## Phases (each independently shippable, tests green)

### Phase 1 — multi-daemon plumbing with exactly one host (zero behaviour change) — **DONE**

As built (deltas from the plan below): `localHostID = "local"` const; `newLocalDaemon(o, socket)`
builds the synthesized host; `o.hostByID/hostOf/hostForPane/paneHostID` are the only readers of
`orch.hosts` (package-level `nopDaemon`, no orch back-pointer, so it can never be dialed);
`syncDaemon` re-resolves `rt.host` every pass (a runtime built before its model state was restored
would otherwise keep the default forever); `captureHistory` skips disconnected hosts per pane and
leaves their `histDirty` set instead of returning early; `pane.send_input` moved onto
`PaneHostConnected` alongside read/capture/wait (same class of pane-addressed write);
`daemon.peerVersion` is recorded from the welcome but nothing branches on it yet;
`daemon.lostMessage()` keeps the historical toast verbatim while only one host exists.
Tests added: `cmd/catway/daemon_test.go` (two-host harness, host-scoped reconcile/close/flush,
per-pane connectivity, unknown-host fallback), `internal/workspace/host_test.go`,
`internal/app/session_test.go:TestPaneHostPlacement`,
`internal/persist:TestSessionFileHasNoHostKeyForSingleHost`.

- `cmd/catway/daemon.go`: add `id, label, kind, dial, peerVersion`; `unixDialer(path)`; `run()` uses `d.dial`; disconnect posts `flushPendingFor(d.id)/flushWaitersFor(d.id)` + `NewError(0, label+": cathost connection lost — reconnecting")`; `reconcile` filters model panes by `rt.host == d.id` and closes only this host's alive-but-unknown ids; `applyPaneModes` → `o.hostOf(rt).send`.
- `cmd/catway/catway.go`: `orch.hosts`, `orch.defaultHost`, `paneRuntime.host`; `hostOf/hostForPane/nopDaemon`; `PaneHostConnected`; `flush*For`; convert every `o.daemon.*` site; `syncDaemon` sets `rt.host = o.session.PaneHost(pid)` (fallback default); `newOrch(socket, cwd)` keeps its signature and builds `hosts["local"]`.
- `cmd/catway/persist.go` (148/161 gates per pane host), `cmd/catway/main.go:267` (range hosts → `go d.run()`).
- `internal/workspace/{spawner,tab,workspace,persist}.go`: `SpawnSpec.HostID`, `PaneState.HostID`, `Workspace.HostID`, `PaneSnapshot/Snapshot.HostID` omitempty; `splitFocusedWithSpawner` stores `spec.HostID` else `w.HostID`; `restoreTab` passes `ps.HostID`. Tab/Workspace must not overwrite a caller-set `HostID`.
- `internal/app/session.go`: `PaneHost(id) string`, `SplitPaneWith(target,dir,spec)`, `CreateTabInWith(ws,spec)`, `CreateWorkspaceAtOn(cwd,host)`; existing signatures become wrappers. `internal/app/commands.go`: `PaneHostConnected` on `Backend`, used by the read/capture/wait_for_output gates; fakes in `commands_test.go`/`session_test.go`.
- Tests: `persist_test.go` `newPipeDaemon` → `o.hosts[o.defaultHost].setConn`, add `newPipeDaemonFor(t,o,hostID)`; `resume_test.go` reconcile call; new `cmd/catway/daemon_test.go` (two pipe hosts: reconcile on A leaves B untouched; disconnect A flushes only A's pending/waiters); `internal/persist` round-trip proves no `host` key emitted; `internal/workspace` HostID default tests.
- Ship gate: `session.json`/`history.json` byte-identical before/after for a single-host session.

### Phase 2 — `hosts:` config, roster, badges, `host.list` — **DONE**

As built (deltas from the plan below): `config.LocalHostID` is now the one spelling of
`"local"` (catway's `localHostID` aliases it) and `Host.Transport()` is the single address
parser both `Validate` and `dialerFor` use — lenient about an empty unix target (a
socket-less test orch must fail at dial, not at build), strict in `Validate`; ids are
bounded by a regexp and a `local` entry must stay `unix://`. `EffectiveHosts` normalizes
the `Default` flag onto a copy, so the config's own slice is never rewritten. Construction
went through `newOrchHosts`/`newOrchHostsWith` with the old `newOrch`/`newOrchWith` kept as
single-host wrappers — the roster has to exist *before* the first `syncDaemon`, which is
where restored panes resolve their host. `orch.hostOrder` preserves configured order (map
iteration would reshuffle the sidebar every render); `daemon.lastErr` + `status()` carry
*why* a host is down, which the roster and `catctl hosts` both show. Host ids are filled
into `layout` unconditionally (always resolved, never the model's `""`) and the *client*
hides badges while the roster holds one host — one rule, one place. `HostItem.Default` is
`is_default` on the wire: `default` is reserved in Dart and catgen-dart refuses it.
`PaneMeta` (not `PaneInfo` directly) carries the resolved pane host, so `pane.list` and
`pane.get` get it from the one backend merge that already exists; `workspace.list` reports
the *stored* id instead, empty ⇒ default, because that field is a policy for new panes
rather than a location. Settings modal lists the roster only when >1 host.
Tests added: `internal/config` (synthesis, override, default selection, transport table,
validation rejects, YAML round trip), `cmd/catway/hosts_test.go` (single-host roster shape,
two-host counts + scoped disconnect, layout host fill, configured-roster construction,
tls:// refused), `internal/app` host.list routing (roster passthrough + single-host shape).
Verified live: two cathosts on two sockets → `catctl hosts` shows both; killing one leaves
the other connected and streaming with the dial error on the dead row; restarting it flips
back; `catctl probe` tallies the `hosts` push on connect; `catctl panes` reports `host`.

- `internal/config/config.go`: `Host`, `Config.Hosts`, `Validate()` (unique non-empty ids, ≤1 default, parse `Addr` scheme), `EffectiveHosts(cathostSocket) []Host` synthesizing `local`; example + tests.
- `cmd/catway/main.go`: build daemons from `EffectiveHosts` (`newOrchHosts`); `daemon.go` `dialerFor(addr)` (unix now; tcp/tls return "unsupported until Phase 4").
- `internal/browserproto/{proto,down}.go`: `MsgHosts`, `Hosts/HostItem`, `Host` on `PaneRectInfo`/`WorkspaceInfo`; `catway.go` `hostsMsg()` sent on client init and broadcast on connect/disconnect; layout fills `Host`.
- `internal/app/command_vocab.go`: `CmdHostList`, `HostInfo`, `HostListResult`, `PaneInfo.Host`, `WorkspaceInfo.Host`; `commands.go` Dispatch via `Backend.Hosts()`; `ConfigServerInfo` gains `hosts`.
- `cmd/catway/web/index.html`: `#sec-hosts` (`initSectionFold("sec-hosts","host-hctl","hosts")`), `renderHosts(msg)`, `onMessage case "hosts"`, `renderChrome` `seg("host", p.host)` after `pub` only when >1 host, workspace-row badge.
- `cmd/catctl/subcommands.go`: `hosts` verb (`host.list`). catgen-dart golden regen.
- Ship gate: one host ⇒ no section/badge; two local cathosts on two sockets show roster + badges; kill one → only its panes report loss.

### Phase 3 — host pickers, params, restore-on-host end to end — **DONE**

As built (deltas from the plan below): there is no `Backend.HostExists` — every host
question (exists? which is default? same machine as that pane?) is answered from the
`Backend.Hosts()` roster that already exists, through `Dispatcher.hostInfo/checkHost/sameHost`,
so the seam did not widen and "listed" and "exists" cannot disagree. `HostInfo`/`HostItem`
gained **`local`**, set by catway as `id == localHostID`: localness is the host id, never the
transport (a `unix://` address is exactly how the first real remote host is reached, over
`ssh -L`), and the browser needs the flag to decide whether to offer the start-path picker.
Session routing goes through new one-field wrappers `SplitPaneOn`/`CreateTabInOn` rather than
building a `workspace.SpawnSpec` inside the dispatcher. `workspaceStartDir` takes `local`: a
remote workspace's path is passed through verbatim and both defaulted states become `""`
(no directory named ⇒ cathost's own default), so `mkdir` never fires for a remote path.
`paneCwd` became host-scoped in *both* fallbacks — the workspace identity cwd is only sent
when the pane runs on the workspace's own host (a "split here on devbox" guest pane inside a
local workspace must not receive this machine's path), and the process cwd only for a local
pane; found live, where the guest pane was spawning in the catway machine's directory.
Beyond the planned skips, `worktree.open` now pins its new workspace to `local` explicitly
(the checkout was just stat'ed here) and `workspaceForPath` ignores remote workspaces (the
same path string on two machines is two directories). The context menu grew real submenus
(`{label, sub:[…]}`, hover/click, chain-aware close) instead of flat per-host rows, and
`dialogFields` grew a per-field `onChange` so the new-workspace dialog's host choice can
switch the path picker off (`picker.setEnabled`) and re-word its placeholder. catctl was left
alone — `--params` already carries `host`.
Verified live (two cathosts, one config, `catctl`): `pane.split --host` / `tab.create --host`
/ `workspace.create --host` land on the named host; an unknown host is refused before
anything is created; a cross-host pane spawns in *its* host's directory instead of inheriting
one from this machine; a remote workspace's `path` reaches cathost verbatim; worktree verbs
and `path.list` refuse a remote pane by name; catway restart re-adopts every pane on its own
host; killing one host leaves the other streaming and flips back on restart.
Known gap, as the plan predicted: a remote `path` that does not exist on that host still
produces a dead pane (`host.go createPane` has no cwd fallback yet) — Phase 4's first item.
Also unchanged from Phase 1 and worth a decision before Phase 5: an unqualified `pane.split`
takes the *workspace's* default host, not the split pane's, so splitting a guest pane yields
one on the workspace's host (its inherited cwd is correctly dropped in that case).
Tests added: `internal/app/commands_test.go` (split/tab/workspace host routing, unknown-host
refusals, cross-host cwd inheritance dropped both ways, remote path verbatim, workspace host
flowing to new panes), `cmd/catway/hosts_test.go` (restore-on-the-right-host with per-host
respawn, host-scoped `paneCwd`, `hostsMsg` local/default), `startdirs_test.go` (heal skips
remote workspaces and panes), `resume_test.go` (no resume argv for a remote pane),
`gitbranch_test.go`/`agentmodel_test.go` (no branch/model reads for a remote pane),
`worktrees_test.go`/`paths_test.go` (local-only refusals).

- `command_vocab.go`: `Host` on `SplitParams`/`TabCreateParams`/`WorkspaceCreateParams`, validated against `Backend.HostExists`; `commands.go` routes into the `*With/*On` session methods; `inheritedSplitCwd/inheritedTabCwd` inherit only when neighbour host == target host; `workspaceStartDir` passes path verbatim for non-local; `main.go` heal guards skip non-local; `refreshPaneBranch`, agentmodel, resume plans skip non-local panes; worktrees `Fail("worktrees are local-host only")` when host ≠ local; `path.list` picker hidden for non-local hosts.
- `index.html`: host `<select>` in `openNewWorkspaceDialog` (`dialogFields choices` precedent = plugin picker); "split here on…" submenu in `paneMenuItems`/tab menu; shown only when >1 host.
- Tests: `commands_test.go` param routing; `persist_test.go` snapshot with `host` restores `rt.host` and `CreatePane` reaches that pipe host.
- Ship gate: cross-host panes side by side; catway restart re-adopts survivors per host; real remote via `ssh -L` unix forward.

### Phase 4 — native remote transport + host-side moves — **DONE**

As built (deltas from the plan below): version negotiation is **two-sided** —
`NegotiateVersion(peer)` gates the range at both ends, and the daemon answers with
the *negotiated* version rather than its own (`NewWelcomeAt`), because every build
before v3 demands equality with what it sent; announcing v3 to them would have
broken every not-yet-upgraded catway on contact. A refused hello is a `welcome`
carrying `error` followed by an `endSession{}` **writer sentinel**, so the reason
reaches the wire before the connection goes (a bare close is indistinguishable from
a daemon that never started); `Host.dispatch` therefore returns an error, and
`Attach` keeps reading after a fatal one — leaving the loop early would close
`sessDone` and drop the queued explanation. Branch resolution needed more than "on
cwd change": the *local* cathost is v3 too, so catway skips its own resolver for
every pane, and a `git checkout` in a pane that never moves emits nothing at all —
hence a daemon-side `branchPump` (10s sweep + a non-blocking `wakeBranch()` nudge
from each cwd change) with the throttle keyed on the *directory*, so a pane that
moved is never throttled. `resyncPane` replays `pane_branch` even when empty; on
catway's side `applyPaneBranch` is the entry point and `refreshPaneBranch` returns
early for any pane whose host `resolvesBranch()` (connected ∧ peer ≥3) — a remote
pane whose host goes away still drops its label, since it describes a checkout
nobody can reach. `-listen` is one address string parsed by `config.Host.Transport`
(the same parser the catway config uses, so the two can never disagree), with
`-socket` kept as the default and shorthand; both network transports **refuse to
start without `-token-file`**, and `tcp://` is refused off the loopback at both the
bind and the dial. TLS pinning replaces chain verification rather than waiving it
(`InsecureSkipVerify` + `VerifyPeerCertificate`); a `tls://` host with no
fingerprint gets ordinary CA+hostname validation, and a fingerprint that is not a
hex SHA-256 fails at roster build. The token is read from its file *per handshake*,
so a rotation takes effect on the reconnect it causes. The cwd fallback reports
`$HOME` through a normal `error` event attributed to the pane, after the pane
exists, so the toast is actionable rather than a mystery about where the shell
landed. `gitBranch/findGitDir/headBranch` moved wholesale into `internal/gitbranch`
(catway keeps a one-line shim); their tests moved with them.
Tests added: `internal/gitbranch` (the moved resolver suite),
`internal/orchestration/protocol_test.go` (negotiation table, negotiated welcome,
omitempty token), `internal/orchestration/handshake_test.go` (v3 and v2 peers
served — the v2 one to completion — bad/missing token and out-of-range versions
refused *with the reason delivered before the close*, cwd fallback vs. an existing
cwd, `pane_branch` from a real daemon), `cmd/catway/transport_test.go` (fingerprint
pinned against a live TLS listener, fingerprint normalization, token read from
file, peer-version → `resolvesBranch`, a v1 daemon refused), `cmd/catway/gitbranch_test.go`
(catway defers to a v3 host, host answers applied, remote branch dropped when its
host is unreachable), `cmd/cathost/listen_test.go` (unix open/stale-socket/cleanup,
every unsafe-bind refusal, tls mints and serves a certificate, token file trimmed).
Verified live (two cathosts, one unix and one `tls://127.0.0.1:18422` with a token,
no ssh anywhere): `catctl hosts` shows both connected with `addr_kind: tls`; a
workspace created on the TLS host spawns there and its pane carries a real branch
(the client-init push is gated on a non-empty branch, so its arrival *is* the value
check); a workspace on a path that exists on neither machine lands in `$HOME` with
`daemon error (pane 3): /only/on/the/other/machine is not a directory on this host
— started in /Users/RAllison3 instead` instead of a dead pane; a wrong token puts
`daemon rejected hello: authentication failed` on the roster row and a wrong
fingerprint puts the two hashes side by side; a local pane in a repo still shows its
branch, now resolved by the local cathost rather than by catway.
Docs updated: `docs/protocols/orchestration-seam.md` (v3, transport table,
versioning, why the daemon owns cwd and branch), `docs/reference/cli.md` (the new
cathost flags + a serve-a-remote-catway recipe), `docs/reference/configuration.md`
and `config.example.yaml` (tls:// is real; pinning explained).
No catgen-dart golden churn: `Hello`/`PaneBranch` are seam types, not browser ones.
Known gap for Phase 5: hot attach/detach still needs a restart, and an unqualified
`pane.split` still takes the workspace's default host rather than the split pane's.

- `internal/orchestration/protocol.go`: `ProtocolVersion=3`, `MinProtocolVersion=2`, `Hello.Token`, `MsgPaneBranch{PaneID,Branch}`; `host.go`: `RequireToken`, `handleHello(payload)` (version range + token → `Welcome{Error}` + close), cwd fallback (`os.Stat` fail → `$HOME`, plus `Error` note), branch resolver on cwd change (extract `gitBranch/findGitDir/headBranch` into `internal/gitbranch`).
- `cmd/cathost/main.go`: `-listen unix://|tcp://|tls://`, `-token-file`, `-tls-dir` (`gwtls.EnsureSelfSigned`, prints fingerprint); `-socket` kept as alias. `cmd/catway/daemon.go`: `tcpDialer`, `tlsDialer(fingerprint)`, `peerVersion`, range check; `gitbranch.go` skips panes on peer ≥3 hosts.
- Tests: `protocol_test.go`, `host_test.go` (bad token/old version rejected; v2 hello on unix accepted), `daemon_test.go` (fingerprint mismatch fails).
- Ship gate: `hosts: [{id: devbox, addr: tls://devbox:8422, token_file: …, fingerprint: …}]` works with no ssh.

### Phase 5 — hot attach/detach — **DONE**

As built (deltas from the plan below): there is no `attachHost`/`detachHost` pair —
every roster change goes through one **`applyHostRoster(configured []config.Host)`**
that re-derives the effective roster from `o.cfg.Hosts` + `o.cathostSocket` and
diffs it against what is running, so attach, detach and `ReloadConfig` are the
same code path and the file and the session can never describe different
machines (`orch.effHosts` was written and then deleted for that reason: the
config *is* the base). Its order is load-bearing — build → retire → install →
re-home → start → announce: every new daemon is built before the first one is
retired, so an unusable address fails the whole edit (`TestApplyHostRosterIsAllOrNothing`),
and the departing host's panes are found while they still resolve to it. The diff
compares `sameDialTarget` (id/addr/token/token_file/fingerprint), not the whole
entry: a rename or a moved `default:` must not drop a live connection, while a
changed address rebuilds the daemon and **keeps** its panes (same host, new route
— its PTYs are not closed either, since the reconnect's reconcile may adopt them).
`daemon` grew `spec`, `quit` and `stopped`: `stop()` closes the live conn to
unblock the pump and the dial loop checks `stopping()` at both waiting points
(the backoff is a `select`, so detaching a host that is down does not leave a
goroutine and a roster row alive for another five seconds), and a stopped daemon
returns *without* the disconnect toast and pending-flush — a detach is not an
outage. Two things the plan did not anticipate: `Session.SetPaneHost` had to
exist (a re-home has to outlive the process, or the next restore puts the pane
back on a ghost) and it stores `""`, not the default host's id, so a displaced
pane tracks the default rather than being pinned; and `paneCwd`'s workspace check
became `workspaceHostOwns`, because `workspaceHostID` resolves an unknown host to
the *default*, which made a workspace pinned to the just-detached host match every
re-homed pane and hand it a path from a filesystem this catway can no longer reach
(found live). A forced detach sends `close_pane` to the departing host first
(best effort — it is usually unreachable, which is why it is being detached) so a
persistent cathost nobody is attached to does not keep the shells. `Workspace.HostID`
is deliberately left alone: a pane's host is where it *is*, a workspace's is a
policy for new panes, and a host that comes back should get its workspace again.
Dispatcher-side the checks are shape-only (`id`/`addr` present, `checkHost` for
detach) — the roster and the file are the backend's. catctl got `attach-host <id>
<addr> [label...]` / `detach-host <id> [force]` (force is a word, not a flag: it
is the argument that throws work away) and a new `argDetachHost` completion kind
that offers the roster *minus* local, since detaching local is always refused.
The browser got `＋` on the HOSTS heading, a right-click detach on each row (with
a confirm dialog when it holds panes) and **attach host… in the gear menu** — the
section is hidden while there is one host, which would otherwise make the first
attach unreachable from the UI.
Tests added: `cmd/catway/hostedit_test.go` (attach lands in roster + memory +
file and never writes the synthesized local host; `is_default` moves and releases;
five refusals each leaving no file behind; detach refuses local, refuses a host
with panes, plain-detaches an empty one; forced detach closes the remote PTY,
re-homes the pane, respawns it on the default host and clears the file; a re-homed
pane does not inherit the departed host's path; the dial loop stops; rename keeps
the connection; readdress redials and keeps its panes; two hosts dropped in one
edit are each told about their OWN panes — the orphan list accumulates, and the
first draft handed the running total to every departing host; an unusable entry
changes nothing), `internal/app/commands_test.go` (attach/detach routing, missing id/addr
refused before the backend, unknown detach id refused), `cmd/catctl` (the new
kind in `TestArgKindsMatchSynopsis`).
Verified live (two cathosts on two sockets, one catway, no restart anywhere):
`catctl attach-host` → the row appears and connects and the `hosts:` block is
written; a workspace created on it spawns there (`echo` into a file on B proves
it); plain detach is refused naming the pane count and the destination; forced
detach re-homes the pane, which then streams a live shell on A with its branch
badge (seen through `catctl probe`); `catctl reload` after a hand-edit attaches,
renames (without redialing — B logged no second connect) and detaches; every
refusal (cleartext off the loopback, duplicate id, `local`, unknown id) leaves
the roster untouched; `catctl __complete` offers `devbox` and not `local`.
Docs updated: `docs/reference/configuration.md` (a new "editing the roster
without a restart" section + the live-reload diagram), `docs/protocols/control-api.md`
(the two commands and what `force` costs), `docs/reference/cli.md` (the verbs and
the raw form for tokens/fingerprints), `config.example.yaml`.
catgen-dart goldens regenerated (two new commands); **cats-mobile has not been
regenerated** — that needs cats pushed first (see memory: cats-mobile regen flow).
Known gap, unchanged from Phase 3: an unqualified `pane.split` still takes the
*workspace's* default host rather than the split pane's.

- §7 `host.attach`/`host.detach` (`HostAttachParams` = config `Host` shape); `orch.attachHost/detachHost` (detach refuses while it owns panes unless `force` → respawn on default); persist to `hosts:` like `config.set`; `ReloadConfig` diffs the roster; roster buttons in the aside; `catctl host attach/detach`.

### Phase 6 — follow-ups
Per-host meters (`host_stats` event → `UsageGroup{ID:"host:"+id}`), remote `path.list` via a cathost `list_dir` request, hook relay for remote agents, latency in `HostItem`, catapp `Cats Client.app` host presets.

## Verification
- Every phase: `make test` and `make test-ghostty` (`-tags ghostty ./...`); regen catgen-dart goldens whenever `internal/app`, `browserproto`, or `orchestration` wire structs change and `go test ./cmd/catgen-dart`; `TestCommandSpecsRouted` for each new command; then cats-mobile regen per memory.
- Phase 1: byte-identical `session.json`/`history.json`; all existing catway tests unchanged in intent.
- Phase 2/3 (`/run`-driven): two `cathost -persistent -socket /tmp/a.sock|/tmp/b.sock`, config two hosts → roster shows both; split a pane onto B beside an A pane; kill B → only B's panes toast/lose stream, A keeps streaming; restart catway → per-host reconcile adopts survivors; `catctl hosts`; `ssh -L` unix forward to a real remote box.
- Phase 4: wrong fingerprint/token/old version rejected with a clear log line; v2 cathost on unix still works; a create with a catway-only cwd lands in the remote `$HOME` with an error toast, not a dead pane; branch badge appears for remote panes.

---

## Appendix A — full "Remote dream" catalog (later slices)

**0. Hosts & sessions:** roster/attach/kill (this slice) · federated cross-host workspaces (this slice) · per-host workspace recipes (runbook `on: host.attach`) · session hand-off to phone/other browser (pairing exists) · **agent migration between hosts** (resume ids + spawn elsewhere) · presence: per-user identity, coloured focus rings, read-only watch links · per-host health strip.

**1. Remote command history:** **OSC 133 shell integration → command ledger** `{host,pane,cwd,cmd,exit,duration,origin}` via `catctl integration install shell`; origin tagging human vs agent (hooks); cross-host fuzzy history palette (re-run here / on host X); **semantic scrollback blocks** (collapse, jump, copy output, send block to chat/agent/note); "explain this failed block" via `chat.send`; gonotes lab journal from ledger + captures. Storage: bytdb (global preference) or `~/.local/state/cats/ledger/*.jsonl`.

**2. Scripts/automations:** **runbooks** = YAML steps over the §7 vocabulary (`pane.split/send_input/wait_for_output/capture/chat.send/notify`) with params and `on:` triggers (manual, `host.attach`, agent state, `pane_exited`, cron via cats-todo scheduling); record-a-macro from live use; agent-authored runbooks via an ACP tool; guardrail automations (cwd leaves repo, dangerous command → pause + push); reply-from-notification (ntfy action buttons answer agent prompts).

**3. Remote editing with CEd:** `pane.open_file {path,line}` (ced upstream ask #6: click a path anywhere → focused ced pane on that host, spawn if absent); **file transfer through cathost** (drag-drop upload to pane cwd, `file.get`, `catctl cp host:path .`); remote diff of a file across hosts in a ced diff pane; pinned "editor slot" per host; `ui.notify {title,body,actions}` (ask #4).

**4. Adjacent stars:** agent-aware **session record & replay** (asciicast + agent-state timeline, "what did the agent do while I was away"); **port preview** (expose host `localhost:PORT` through catway as a preview tab — kills `ssh -L`); resilient input (queue during reconnect, latency indicator, optional local-echo prediction); cross-host drag of paths; global palette across hosts/panes/commands/history/runbooks/notes; per-host bookmarks/frecency; agent activity heat strip; the relay (already designed).

**Recommended order:** this slice → shell integration + ledger + blocks → runbooks → ced trio (`pane.open_file`, file transfer, `ui.notify`) → record/replay, port preview, agent migration, relay.
Quick wins needing no foundation: `pane.open_file`, `ui.notify`, reply-from-notification.
