# Peer sync

Two cats instances — a laptop and a mini-PC at home, say — each keep a backend
of their own: workspaces in `session.json`, plugins under the plugins root, and
cats-todo backlogs beside each project. Peer sync reconciles those backends
without a shared server: one catway dials the other, the two exchange what
they have, and each keeps what it lacked. The screen is not involved; what
syncs is the **state of record**.

A peer is not a [host](../reference/configuration.md#hosts). A host is another
machine's terminal daemon attached to this catway, so its panes appear here. A
peer is a second catway that is a source of truth of its own.

## What travels, and on what terms

| Store | Identity across machines | What a sync does | What it never does |
|---|---|---|---|
| **Workspaces** | the folder a workspace identifies with (`IdentityCwd`), with the home directory translated — `/Users/me/projs/x` on a Mac is `/home/me/projs/x` on Linux | creates the workspace on the other side **if that folder exists there**; new ones arrive asleep (in the list, no terminal) and the active workspace stays put | move tabs or panes (they are terminals on one machine); create a folder; close anything |
| **Todos** | the backlog file: `<project>/.cats-todo/todos.json` for each workspace's folder, plus the global backlog (`~/.config/cats-todo/todos.json`) | **merges** rows: adds what the other side lacks, propagates completion (a row done or frozen there and open here takes the finished record), copies attachments; creates a project backlog where the folder exists but has none | overwrite a row that differs (kept as it was, reported as a conflict); resurrect a row already done here; carry a schedule (it names a pane on the other machine) |
| **Plugins** | the install source recorded at install time (`.cats-plugin-source.json`: `owner/repo` or URL, plus ref) | installs, from that source, every plugin the other side has and this side lacks — a git clone and the manifest's build steps, run here | update a plugin present on both sides (versions may differ; `plugin update` is its own act); install a linked plugin (it is a checkout on the other machine); touch a broken entry |

Every category is opt-in per sync. Nothing is ever deleted or replaced in any
direction, which is what makes it safe to run either way, repeatedly, and read
the report afterwards rather than before.

## The report

A sync answers once, with the whole story: for each side, how many items were
synced, unchanged, skipped and failed; then every item that synced, was
skipped or failed, with its reason. *Skipped* is the list the report exists
for — the folder that does not exist on this machine, the plugin linked to a
checkout over there, the backlog row that differs on both sides and was kept.
The same report is what `catctl sync` prints and what the dialog draws.

```
sync with home (me@mini) — both, workspaces,todos
here: 2 synced, 5 unchanged, 1 skipped, 0 failed
  synced    workspace cats-todo (/Users/me/projs/go/cats-todo) — added, asleep
  synced    todos     global — 3 added, 1 completed, 12 unchanged
  skipped   workspace scratch (/Users/me/tmp/scratch) — no such folder on this machine
on home: 1 synced, 6 unchanged, 1 skipped, 0 failed
  synced    todos     cats (/home/me/projs/go/cats) — 1 added, 0 completed, 9 unchanged
  skipped   todos     cats (/home/me/projs/go/cats) — conflict: fix the flaky reconnect (kept this side's copy)
```

## How a sync runs

```
initiator                                        peer
   │  GET  /peer/v1/bundle?want=workspaces,todos   │
   │ ─────────────────────────────────────────────▶│   Collect: read session,
   │ ◀────────────────────────── Bundle (theirs) ──│   backlogs, plugin records
   │  Apply(theirs) here                "pull"     │
   │  POST /peer/v1/apply  Bundle (mine)           │
   │ ─────────────────────────────────────────────▶│   Apply(mine) there  "push"
   │ ◀──────────────────────────── ApplyReport ────│
   │  SyncReport = pulled + pushed
```

Both sides speak the same envelope, so the operation is symmetric: a *pull*
applies the peer's bundle here, a *push* sends ours for the peer to apply, and
*both* (the default) does the two in that order — so what was just pulled is
part of what is pushed, and one round leaves the two in step.

The bundle carries todo rows as the generic JSON objects they were read as,
never as a struct: a field cats-todo adds later round-trips through an older
cats intact instead of being dropped. Attachments ride inside it (capped per
file and per backlog); a row whose attachment could not come across still
lands, without it, and is counted.

## Trust and transport

The `/peer/v1/*` routes sit behind the same [auth guard](auth-and-tls.md) as
everything catway serves. A peer presents one of two bearer credentials:

- a **peer grant**, obtained by [pairing](#pairing) — the usual way; or
- that catway's own shared secret (`CATS_PASSWORD`) — the same thing a headless
  `catctl` presents. There is deliberately no narrower permission for a secret
  holder: it already has `/ws`, and with it `tab.create` and `pane.send_input`
  on every pane, so a plugin install through `/peer/v1/apply` grants nothing
  that could not be typed into a shell.

A grant is strictly less than the secret: the guard accepts it on `/peer/v1/*`
and nowhere else. It does not make sync harmless — a plugin install is still
code run on the other side — but it keeps a sync credential from being a
terminal credential, and it can be revoked without changing the password.

### Pairing

```
B: catctl pair peer ──▶ pairing token (5 min, single use, kind=peer)
                              │  pasted by a human as a cats://peer link
A: catctl attach-peer b 'cats://peer?u=…&t=…&f=…'
      └─ A's catway ── POST /peer/v1/pair {token, name} ──▶ B
                                                           │ spend the token
                                                           │ mint a grant; store its SHA-256
         <state>/peer-tokens/b.token (0600) ◀─ credential ─┘
```

`/peer/v1/pair` is the one public peer route; the pairing token in the body is
its credential. A peer pairing token is not a device pairing token — neither is
accepted at the other's door (`/login` vs `/peer/v1/pair`), and trying the wrong
one does not burn it. Grants live in B's `<state_dir>/peer-grants.db`
(btypedb, hashes only), survive restarts of either side, and are managed on B
over the control socket: `catctl peer-grants`, `catctl revoke-peer-grant <id>`.
A's `detach-peer` deletes the token file it wrote but cannot revoke the grant on
B; re-running `attach-peer` for an attached id with a fresh link re-pairs it.
Minting, listing and revoking are control-socket methods, not §7 commands, so
no browser session can administer peer access.

Over `https://`, a self-signed peer is **pinned** by its certificate's SHA-256
(the value catway logs at startup under `--tls`), the same rule that makes a
`tls://` cathost safe; without a pin the standard chain and hostname checks
apply. An `http://` URL is accepted only for this machine — loopback, or the
local end of an `ssh -L` tunnel — because the token rides every request.

Under `auth: none` the routes are as open as the rest of that server.

## Configuration and commands

The roster is the config's [`peers:` block](../reference/configuration.md#peers),
edited live by `peer.attach` / `peer.detach` (`catctl attach-peer` /
`detach-peer`, or the peers dialog's *add…* / *forget*). A sync is
`peer.sync` — `catctl sync <peer> [workspaces|todos|plugins|all] [pull|push|both]`,
or gear menu › *peers / sync…*, or the palette. See the
[CLI reference](../reference/cli.md#verbs) and the
[control API](../protocols/control-api.md#peers).
