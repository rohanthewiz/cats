# Architecture overview

cats splits cleanly into a **front end**, an **orchestrator**, and a **terminal
backend**, joined by versioned protocols. Every run mode is the same three
pieces; only the machine boundaries move.

## Component map

```mermaid
flowchart TB
  subgraph front["Front end"]
    BROWSER["browser page<br/>embedded index.html"]
    WEBVIEW["catapp<br/>WebKit window + supervisor"]
  end

  subgraph gw["catway — the cats server"]
    HTTP["rweb HTTP server<br/>/ /login /ws"]
    AUTH["gwauth · gwtls<br/>password, cookie, TLS"]
    LOOP["orchestrator event loop<br/>sole state owner"]
    SESS["app.Session<br/>workspaces / tabs / panes"]
    DISP["app.Dispatcher<br/>command table"]
    ENC["inputenc<br/>keys/mouse -> VT bytes"]
    PERSIST["persist<br/>session.json · history.json"]
    CTLSRV["ctlproto server<br/>control socket"]
    HOOKSRV["hook server<br/>hook socket"]
    DPUMP["daemon client<br/>dial · reconcile · pump"]
  end

  subgraph th["cathost — terminal backend"]
    HOST["orchestration.Host"]
    PANE["per-pane: PTY + emulator<br/>+ readPump / detectPump"]
    VT["libghostty-vt<br/>VT emulation"]
    DET["detect<br/>agent identification"]
  end

  SHELL["shells and agents<br/>zsh · claude · codex · ..."]
  CATCTL["catctl / plugins"]
  HOOKS["installed agent hooks"]

  BROWSER <-->|"WS9 WebSocket"| HTTP
  WEBVIEW <-->|"WS9 WebSocket"| HTTP
  HTTP --> AUTH
  HTTP --> LOOP
  LOOP --> SESS
  LOOP --> DISP
  LOOP --> ENC
  LOOP --> PERSIST
  CTLSRV --> DISP
  HOOKSRV --> LOOP
  CATCTL --> CTLSRV
  HOOKS --> HOOKSRV
  LOOP <--> DPUMP
  DPUMP <-->|"orchestration seam"| HOST
  HOST --> PANE
  PANE --> VT
  PANE --> DET
  PANE <-->|"pty"| SHELL
```

## Responsibility split

| Concern | Lives in |
|---------|----------|
| What the session looks like; what a command changes | `catway` — `internal/app` |
| Pane geometry | `catway` — `internal/layout` |
| Rendering the page, drawing cell grids, copy mode UI | front end — `cmd/catway/web/index.html` |
| Encoding a keystroke into VT bytes | `catway` — `internal/inputenc` |
| PTY ownership, child processes, VT emulation, cell grids | `cathost` — `internal/orchestration` + `internal/terminal` |
| Which agent is in a pane, and what it is doing | `cathost` detection + `catway` hook arbitration |
| Durable state | `catway` — `internal/persist` |
| Access control | `catway` — `internal/gwauth`, `internal/gwtls` |

Two rules explain most of the design:

1. **The orchestrator never touches a PTY.** It sends commands over the seam
   and receives frames. That is what lets `cathost` outlive it.
2. **The terminal backend never knows about layout.** It knows pane ids and grid
   sizes. Tabs, workspaces, splits and zoom are invisible to it.

## The four seams

```mermaid
flowchart LR
  FE["front end"]
  GW["catway"]
  TH["cathost"]
  CLI["catctl · plugins · scripts"]
  AG["agent hooks"]

  FE <-->|"1 · browserproto v1<br/>WebSocket, JSON text frames"| GW
  GW <-->|"2 · orchestration v2<br/>u32-LE length + JSON, 8 MiB cap"| TH
  CLI -->|"3 · ctlproto v1<br/>newline JSON, unix socket 0600"| GW
  AG -->|"4 · hook API<br/>one JSON line per report"| GW
```

Each protocol has its **own** version constant, bumped independently. See
[Protocols](../protocols/index.md).

## Process lifecycles

`catway` and `cathost` are independent processes with independent lifetimes.
Neither spawns the other — `catway` only *dials*.

```mermaid
sequenceDiagram
  participant U as operator / catapp
  participant TH as cathost
  participant GW as catway
  participant FE as front end

  U->>TH: start (-persistent)
  U->>GW: start
  GW->>TH: dial unix socket (retry with backoff)
  GW->>TH: hello (protocol 2)
  TH-->>GW: welcome (+ surviving pane ids)
  GW->>GW: reconcile model against surviving panes
  FE->>GW: HTTP GET / then WebSocket /ws
  FE->>GW: init (grid size)
  GW-->>FE: welcome, layout, pane frames
```

The important consequences:

* **`catway` can restart** without losing a shell. On reconnect it reconciles
  the restored model against the panes `cathost` still has, re-adopting live
  PTYs and re-spawning only the dead ones.
* **`cathost` can die.** `catway` keeps serving, redials with backoff, and on
  reconnect replays the model — re-spawning panes and seeding them with the
  scrollback it captured into `history.json`.
* **The front end can come and go** freely. It is stateless; a fresh connection
  gets a full resync (layout, chrome, pane frames) from cached runtime state.
* `server.stop` / SIGINT / SIGTERM on `catway` saves state, runs a final
  scrollback capture, and exits — **terminals survive**.

## The three run modes at a glance

```mermaid
flowchart LR
  subgraph m1["Mode 1 · Standalone Mac"]
    A1["Cats.app: catapp"]
    A2["catway · loopback · auth none"]
    A3["cathost -persistent"]
    A1 -->|"127.0.0.1:ephemeral"| A2
    A2 -->|"TMPDIR unix socket"| A3
  end

  subgraph m2["Mode 2 · Mac client / Linux server"]
    B1["Cats Client.app: catapp"]
    B2["catway on Linux · TLS · password"]
    B3["cathost -persistent on Linux"]
    B1 -->|"WSS over LAN / VPN / relay"| B2
    B2 -->|"unix socket"| B3
  end

  subgraph m3["Mode 3 · Web client / Mac server"]
    C1["any browser"]
    C2["catway on the Mac"]
    C3["cathost -persistent on the Mac"]
    C1 -->|"HTTP or HTTPS + WS"| C2
    C2 -->|"unix socket"| C3
  end
```

Detailed pages:

* [Mode 1 — Standalone Mac](standalone-mac.md)
* [Mode 2 — Mac client / Linux server](mac-client-linux-server.md)
* [Mode 3 — Web client / Mac server](web-client-mac-server.md)
* [Choosing a topology](choosing-a-topology.md)

## Build-tag topology

The CGO terminal path is behind the `ghostty` build tag. This is what keeps most
of the tree testable without a Zig toolchain.

```mermaid
flowchart LR
  subgraph tagged["needs -tags ghostty + PKG_CONFIG_PATH"]
    GW["cmd/catway"]
    TH["cmd/cathost"]
    HOSTF["orchestration/host.go"]
    GHOST["terminal/ghostty.go"]
    ENCG["inputenc/encoder.go"]
  end

  subgraph plain["plain go build ./..."]
    CTL["cmd/catctl"]
    APP["cmd/catapp (cgo for WebKit, no tag)"]
    PROTO["orchestration/protocol.go"]
    TERMI["terminal/terminal.go (Emulator iface)"]
    REST["app · layout · workspace · config · persist<br/>ctlproto · browserproto · detect · integration<br/>plugin · gwauth · gwtls · worktree · startdir"]
  end

  VTLIB["third_party/libghostty-vt<br/>vendored Zig source"]
  VTLIB --> HOSTF
  VTLIB --> GHOST
  VTLIB --> ENCG
```

`catapp` needs cgo (WebKit) but **not** the `ghostty` tag — it only supervises
processes and shows a window. That is why the thin-client bundle can be built on
a Mac with no Zig toolchain at all.
