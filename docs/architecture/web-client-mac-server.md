# Mode 3 — Web client / Mac server

The original shape, and still the most flexible: `catway` and `cathost` run on
your Mac, and **any browser** — the same Mac, an iPad on the couch, a second
laptop — is the front end. No bundle, no launcher.

```bash
bin/cathost -socket /tmp/cats-cathost.sock -persistent &
CATS_PASSWORD=changeme bin/catway --addr :8421 --tls
# open https://<mac>:8421
```

## Topology

```mermaid
flowchart LR
  subgraph clients["Front ends — any number, any device"]
    B1["Safari / Chrome on the Mac"]
    B2["Browser on an iPad or phone"]
    B3["Browser on another laptop"]
    PROBE["catctl probe<br/>headless WS client"]
  end

  subgraph mac["macOS host"]
    GW["catway<br/>:8421 · rweb · TLS optional"]
    TH["cathost -persistent"]
    SH["shells and agents"]
    CTL["control socket 0600"]
    HOOK["hook socket"]
    ST["~/.local/state/cats"]
    CFG["~/.config/cats/config.yaml"]
  end

  CLI["catctl · plugins · cats-todo"]
  HOOKS["installed agent hooks"]

  B1 <-->|"HTTPS + WSS"| GW
  B2 <-->|"HTTPS + WSS"| GW
  B3 <-->|"HTTPS + WSS"| GW
  PROBE <-->|"WS + Bearer token"| GW
  GW <-->|"unix socket"| TH
  TH <-->|"pty"| SH
  CLI --> CTL
  CTL --> GW
  HOOKS --> HOOK
  HOOK --> GW
  GW <--> ST
  GW <--> CFG
```

## What is actually served

`catway` runs an [rweb](https://github.com/rohanthewiz/rweb) server with a
deliberately tiny surface:

| Route | Purpose |
|-------|---------|
| `GET /` | the single-page UI, assembled by `cmd/catway/web`.`Page()` |
| `GET /login` | the login form (only when `auth: password`) |
| `POST /login` | check the secret, issue the `hsess` cookie |
| `GET /ws` | the one WebSocket endpoint — the whole browser protocol |

That is all. No REST API, no asset pipeline, no CDN. The page is
**self-contained** and embedded into the binary with `//go:embed`.

> **Note — rebuild after editing the UI**
>
> Because the page's sources are compiled in, editing them and reloading the
> browser keeps serving the old page. Rebuild and restart `catway`.

Theme colours and keybindings are injected into the served page at render time,
which is why `catctl reload` can apply a config edit with no restart: the
orchestrator re-renders the page and tells connected clients to reload it.

## Session lifecycle for a browser

```mermaid
sequenceDiagram
  participant B as browser
  participant GW as catway
  participant TH as cathost

  B->>GW: GET /
  alt no valid cookie
    GW-->>B: 302 /login
    B->>GW: POST /login (password)
    GW-->>B: Set-Cookie hsess, 303 /
    B->>GW: GET /
  end
  GW-->>B: the embedded page
  B->>GW: WebSocket upgrade /ws
  Note over GW: middleware checks Origin,<br/>then cookie or Bearer — pre-upgrade
  GW-->>B: welcome (protocol 1, features)
  B->>GW: init (cols, rows, cell metrics)
  GW->>GW: recompute layout for the reported grid
  GW->>TH: resize for every visible pane
  GW-->>B: layout, then pane_frame per visible pane
  loop steady state
    B->>GW: key / mouse / paste / resize / cmd
    GW->>TH: input / resize / create_pane / ...
    TH-->>GW: pane_frame, pane_cwd, pane_agent, pane_title
    GW-->>B: pane_diff / pane_frame, chrome updates
  end
```

Auth is checked **once, pre-upgrade**, by the global middleware — there is no
mid-session expiry. A long-lived WebSocket outlives its cookie's TTL.

## Multiple simultaneous clients

Several browsers can be connected at once. They share one `app.Session` —
one set of workspaces, tabs and panes, all live — but each connection is a
**view**: it shows one workspace, with that workspace's own active tab, focused
pane, zoom and grid. That is what makes "a window per project on the second
monitor" work on every topology, over the one running session.

Open a second window on a chosen workspace with `?ws=<id>` (the page forwards it
as `init.workspace`), or from the sidebar's **Open in new window**.

Consequences worth knowing:

* **Different workspaces are independent.** `workspace.focus`, `tab.focus`,
  splits, zoom and focus in one window do not touch another window on another
  workspace. Each window resizes only the workspace it is showing.
* **The same workspace mirrors.** Two windows on one workspace see the same tab
  and the same focused pane — one active tab per workspace is the model — and
  the **last reporter wins** on that workspace's pane sizes.
* **A viewer follows the primary view.** A phone (`init.viewer`) declares no
  geometry and no workspace; it shows whichever desktop window the user touched
  last, and never resizes anything.
* **`catctl` acts on the primary view** — the most recently focused desktop
  window. It has no window of its own, and neither do hook actions or runbook
  steps.
* **Closing a window closes nothing.** The workspace it showed keeps running,
  exactly as a workspace you switched away from, and keeps its pane sizes for
  the next window that opens on it.
* Broadcast messages (`notify`, `title`, `shutdown`, `update_ready`) go to all
  connected clients; per-pane chrome and frames go only to the windows showing
  that pane.
* A late joiner gets a full resync from cached runtime state — the layout of
  *its* workspace, per-pane chrome, and a full `pane_frame` per pane it is
  showing — so it never sees a partial screen.
* Two people driving the *same* workspace still fight over focus. Give them a
  workspace each and they do not.

## Serving on the LAN

`--addr :8421` binds all interfaces by default. To make that safe:

* `--tls` mints a self-signed certificate cached in `~/.config/cats`, with SANs
  covering the hostname and every non-loopback interface IP — deliberately, so a
  LAN IP validates. It auto-renews within 30 days of expiry (825-day validity).
* Keep `--auth password` and set the secret via `CATS_PASSWORD` or `--password`.
  If neither is given, `catway` **generates** one and logs it — fine for a quick
  local run, useless for a service.
* Bind narrowly if you only want local use: `--addr 127.0.0.1:8421`.
* Behind a reverse proxy, add the proxy's host to `server.allowed_origins` so
  the WebSocket origin check passes.

> **Warning — no `X-Forwarded-*` trust**
>
> `catway` does not interpret forwarded headers. Behind a proxy it sees the
> proxy's address, and the same-origin check compares the `Origin` header to
> the `Host` header it received. Subdomain-style routing keeps those equal;
> path-prefix routing does not.

## Running it as a background service on macOS

`launchd` is the macOS equivalent of the systemd setup in
[Mode 2](mac-client-linux-server.md#running-the-backend-as-a-service). Two
caveats specific to launchd, both already solved for `Cats.app` and worth
copying by hand here:

1. **PATH.** A launchd job gets the bare system PATH, not your shell's. Panes and
   plugin build steps inherit it, which is how you get
   `sh: go: command not found` from a job that works fine in a terminal. Set
   `EnvironmentVariables.PATH` explicitly in the plist.
2. **Working directory.** A launchd job's cwd is `/`. Set
   `WorkingDirectory` to your home directory, or every new pane opens at the
   filesystem root. (`internal/startdir` already falls back to `$HOME`, but
   being explicit costs nothing.)

## Headless clients

Two non-browser front ends speak the same edges:

* **`catctl`** over the control socket — the same command table as the browser.
  Owner-only `0600`, local, no auth of its own (filesystem permissions *are* the
  auth). See [Control API](../protocols/control-api.md).
* **`catctl probe`** over `/ws` — a stdlib-only WebSocket client for exercising
  the browser protocol headlessly, authenticating with
  `Authorization: Bearer <secret>` instead of a cookie. This is how the browser
  path gets tested without a browser.

## Trade-offs

| Upside | Downside |
|--------|----------|
| No bundle, no launcher — just two binaries | You manage the daemons yourself |
| Any device with a browser is a client, including tablets and phones | Browser clipboard restrictions: no native pasteboard bridge, so OSC 52 copies depend on `navigator.clipboard` and its activation rules |
| Multiple simultaneous views | Those views share one session — last resize wins, focus is global |
| Trivial to script and probe headlessly | ⌘+/⌘- font zoom is the browser's, not the app's |
| The Mac's own agents, keys and toolchains are right there | Serving on a LAN means doing the TLS and password work |
