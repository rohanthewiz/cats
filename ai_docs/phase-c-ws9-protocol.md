# WS9 — herdr browser protocol, v1 (spec)

**Date:** 2026-07-03. Deliverable of `phase-c-ws9-tasks.md` Stage 1. Implements decisions
**D1** (sparse-index diffs), **D2** (packed-u32 colors), **D3** (computed rects, no BSP tree),
**D4** (structured input, server-side VT encoding) — all provisional-locked 2026-07-03.

Source protocols consolidated here (anchors mapped 2026-07-03):
- **α** Phase-A browser JSON — `cmd/gateway/main.go:133-140` (up), `:264-335` (frames),
  `web/index.html` JS. Dies when the new protocol lands (old gateway mode kept during
  transition for Rust-herdr attach).
- **β** orchestration seam — `internal/orchestration/protocol.go`. **Unchanged by WS9**;
  the server consumes it (daemon ↔ server) and translates.
- **γ** bincode wire — `internal/wire` + `internal/herdrconn`. Not consulted; deleted in WS11.
- Command vocabulary cross-checked against the Rust control API (`src/api/schema.rs:22`
  `Method` enum) and keybinding actions (`src/app/input/navigate.rs:480` `NavigateAction`) —
  the WS2 orchestrator implements the same commands behind both this protocol and the CLI API.

---

## 0. Scope & principles

1. **One protocol.** Everything the browser sends or receives is a message defined here.
2. **The server owns all state** — focus, layout, modes, scrollback, selection semantics.
   The browser is a renderer + event source. It never computes layout, never encodes VT
   bytes (D4), never decides key-vs-keybinding.
3. **Chrome is data.** Sidebar/tabs/status arrive as structured state (§3), never as cells.
4. **Panes are addressed by `layout.PaneID`** (uint32, field `pane`). Public handles
   (`"w1:p3"`, `internal/workspace/ids.go`) appear in chrome payloads for display only.
5. **Interactive dialogs are chrome-local.** Rename/confirm/menus/pickers/help/settings are
   browser HTML; only their *effect* hits the wire, as a §7 command (per the Rust TUI-mode
   analysis: `Mode::{Rename*,Confirm*,ContextMenu,GlobalMenu,KeybindHelp,Navigator,Settings,
   Resize}` do not cross the protocol).

## 1. Transport & envelope

- WebSocket, **text frames, JSON**, one message per WS frame. Binary WS frames are reserved
  for a future packed cell encoding (version bump; see §10).
- Every message: `{"t": "<type>", ...}`. Unknown `t` MUST be ignored (forward compat).
- `PV = 1` (`internal/browserproto.ProtocolVersion`, independent of β's version).
- Per-pane messages carry `pane` (uint32). Commands may carry a client-chosen `id` (a
  string) echoed in the reply (§7).

## 2. Session lifecycle

**Up `init`** — first message on the socket, required:
`{t:"init", v:1, cols, rows, dpr, cell_w_px, cell_h_px}`
Grid size of the browser's pane-rendering area in cells (browser measures its font), device
pixel ratio, and cell pixel metrics (forwarded to β `create_pane`/`resize` for pixel-aware
apps).

**Down `welcome`** — server reply:
`{t:"welcome", v:1, error?}`
Version mismatch or rejection ⇒ `error` set, socket closed. Otherwise the server immediately
pushes initial full state: `layout` (§3), then for each **visible** pane (§8) a full
`pane_frame` + current `pane_title`/`pane_cwd`/`pane_modes`, plus the `agents` rollup and
app `title`.

**Reconnect** is just a fresh session: state is server-side (and pane content is
daemon-side, surviving even server restarts — β `welcome.panes` + `request_resync`).
**Multiple browsers** may connect concurrently; each gets its own viewport (active
workspace/tab) and its own frame streams; state-changing commands broadcast resulting
`layout`/chrome updates to all.

## 3. Down — layout & chrome state

**`layout`** — full replacement, sent on connect and on ANY structural/focus change
(splits, closes, swaps, zoom, tab/workspace ops, focus moves, window resize). Small, so
always full — no layout diffs in v1.

```jsonc
{t:"layout",
 workspaces:[{id:"w1", name, active:bool, agent_summary?}],   // sidebar order
 tabs:[{num, name, active:bool, zoomed:bool}],                 // active workspace's tabs
 panes:[{pane, pub:"w1:p3", rect:[x,y,w,h], inner:[x,y,w,h],  // active tab only —
         scrollbar?:[x,y,w,h], focused:bool}],                 // layout.PaneInfo (layout.go:56)
 borders:[{id, pos, dir, ratio, area:[x,y,w,h]}]}              // layout.SplitBorder (:70);
                                                               // id = server handle for resize_split
```

Rects are in the cell grid established by `init`/`resize` (D3: browser positions per-pane
canvases from these; the BSP tree never crosses the wire). `borders[].id` is an opaque
server-side handle for the border's `Path` (`layout.go:79`) so the browser never sees tree
paths (v1 encoding: stateless `internal/browserproto.BorderID`; the browser must treat it
as opaque).

**`agents`** — full rollup for the sidebar, all workspaces (frames stream only for visible
panes, but agent chrome is global):
`{t:"agents", items:[{pane, pub, workspace, agent, state, seen}]}`
`state` ∈ idle|working|blocked|unknown (β `PaneAgent`, `protocol.go:249`); `seen` from
`workspace.PaneState.Seen` (tab.go:12) — false renders as "Done".

**Per-pane chrome events** (incremental; source = β events of the same name):
- `{t:"pane_title", pane, title}` (β `:289`; "" clears)
- `{t:"pane_cwd", pane, cwd}` (β `:235`)
- `{t:"pane_agent", pane, agent, state, seen}` (β `:249` + server's Seen tracking; also
  patches the `agents` rollup client-side)
- `{t:"pane_modes", pane, mouse:bool, alt_screen:bool}` — the **display-relevant subset**
  of β `PaneModes` (`:332`): `mouse` gates pointer capture vs native text selection;
  `alt_screen` gates the scrollbar. The full mode state stays server-side where the input
  encoder (D4) consumes it. Extend only when WS8 demonstrates a need.
- `{t:"pane_exited", pane, code}` (β `:364`)

## 4. Down — pane content

**`pane_frame`** — full grid for one pane:
```jsonc
{t:"pane_frame", pane, w, h,
 cur:{x, y, vis, shape},              // shape = DECSCUSR param (β Cursor, protocol.go:399)
 def_fg, def_bg,                      // packed u32 defaults for this frame
 links?:["https://…"],                // OSC 8 URI table (frames with links are always full — β rule)
 cells:[{s, f?, b?, m?, h?}, …],      // row-major, len == w*h
 scroll?:{off, max, rows}}            // β ScrollInfo (:422), only when scrollback exists
```
Cell fields: `s` symbol (space for blank), `f`/`b` packed u32 `0x02_RR_GG_BB` (D2; **omitted
when equal to `def_fg`/`def_bg`** — the dominant case, big JSON savings), `m` ratatui
modifier bits (β `:429-436`), `h` hyperlink index+1 into `links` (0/omitted = none).
A tiny JS shim resolves packed u32 → CSS (`(v>>16)&255,(v>>8)&255,v&255`); `wire.ColorToCSS`
is NOT ported (γ dies whole).

**`pane_diff`** — sparse-index patch (D1):
`{t:"pane_diff", pane, cur?, cells:[{i, s, f?, b?, m?, h?}], scroll?}`
`i` = row-major index. Translated by the server from β's skip-flag diffs
(`FrameFromSnapshot`, `protocol.go:503-560`): a β diff's non-skip cells become `{i,…}`
entries. Full frames are sent when: β sends full (geometry change / links present / resync),
the pane becomes visible (§8), or the diff would exceed ~60% of cells (server's choice —
translation makes this free, α already did the equivalent).

Frame cadence, coalescing, and synchronized-output handling are server concerns; the browser
just applies what arrives in order.

## 5. Down — app-level

- `{t:"clipboard", data}` — base64; OSC 52 write from any pane (β `pane_clipboard` `:275`) →
  browser writes system clipboard. Empty = clear.
- `{t:"notify", kind, message, body?}` — toast + (permission-gated) system notification
  (α's shape kept).
- `{t:"title", title}` — browser-tab title (app-level; replaces α's titleHub POST path —
  the hub survives as a server feature, same message).
- `{t:"error", msg, pane?}` — non-fatal, render as toast.
- `{t:"shutdown"}` — server going away cleanly; browser shows "disconnected" chrome.
- `{t:"update_ready", version, command}` — optional, from the Rust `AppEvent::UpdateReady`
  equivalent when WS2 ports it. Chrome shows a banner.

## 6. Up — input events (D4: structured, encoded server-side)

The server routes keys to the **focused** pane (focus is server state), encodes VT bytes via
the pane's live β `pane_modes` mirror (kitty flags **incl. bits 2/8**, modifyOtherKeys,
DECCKM, bracketed paste, mouse encodings), and *also* runs keybinding interception
server-side (the port of `prepare_terminal_key_forward`, Rust `input/terminal.rs:30` —
direct chords → commands, prefix key → prefix state, PageUp/PageDown scrollback intercept).
The browser never pre-encodes (α's JS `SPECIAL` table dies).

- `{t:"key", code, key, mods, kind}` — W3C `KeyboardEvent.code` + `.key`, `mods` bitmask
  (1 shift, 2 alt, 4 ctrl, 8 meta), `kind` ∈ `d`(down)|`r`(repeat)|`u`(up). Up events are
  sent only while the focused pane's kitty flags request release reporting (server tells
  the browser via a `pane_modes` extension if ever needed client-side; v1: browser always
  sends d/r, sends u too — cheap — and the server drops what the encoding doesn't want).
- `{t:"mouse", pane, x, y, btn, kind, mods, dx?, dy?}` — cell coords within the pane
  (browser converts px→cell using its metrics), `btn` 0-left/1-mid/2-right/3-none,
  `kind` ∈ `d`|`u`|`m`(move)|`w`(wheel; `dx`/`dy` in lines). Clicking an unfocused pane:
  the browser sends `cmd pane.focus` first, then (if the pane captures mouse) the event.
  When the pane doesn't capture (`pane_modes.mouse=false`), wheel = `cmd scroll`, drag =
  browser-local selection (§7 `read`).
- `{t:"paste", data}` — plain text; server applies bracketed-paste wrapping per pane mode.
- `{t:"image", data, ext}` — base64 clipboard image (α behavior kept).
- `{t:"resize", cols, rows}` — browser window grid changed; server relayouts (→ new
  `layout`) and resizes panes over β.
- `{t:"raw", data}` — **deprecated escape hatch** (α's `input`): pre-encoded bytes to the
  focused pane. Exists only for the transition; removed before WS11.

## 7. Up — commands

Envelope: `{t:"cmd", id?:string, name, params?}`. Reply (always sent when `id` present):
`{t:"cmd_result", id, ok, error?, data?}`. Names reuse the **control-API vocabulary**
(`src/api/schema.rs:22`) — WS2 implements one command table serving both this protocol and
the CLI/API. v1 command set (browser-needed subset):

| name | params | replaces (Rust action) |
|---|---|---|
| `pane.split` | `{pane?, direction}` | SplitVertical/SplitHorizontal |
| `pane.close` | `{pane?}` | ClosePane (browser confirms first — ConfirmClose is chrome-local) |
| `pane.focus` | `{pane}` | click-to-focus |
| `pane.focus_direction` | `{dir}` | FocusPaneLeft/Down/Up/Right |
| `pane.cycle` | `{next:bool}` / `pane.last` | CyclePaneNext/Previous, LastPane |
| `pane.swap` | `{dir}` | SwapPane* |
| `pane.zoom` | `{pane?}` | Zoom |
| `pane.rename` | `{pane, name}` | RenamePane (dialog chrome-local) |
| `pane.resize_border` | `{border, ratio}` | Mode::Resize / drag (border id from `layout`) |
| `scroll` | `{pane, delta}` | β ScrollViewport passthrough (PageUp intercept, wheel, scrollbar) |
| `read` | `{pane, anchor:[row,col], cursor:[row,col], rect?}` → `cmd_result.data.text` | copy-mode yank / mouse selection (β RequestSelection `:150`; abs screen-buffer coords from frame `scroll`) |
| `tab.create` / `tab.close` / `tab.focus` | `{}` / `{num?}` / `{num}` | NewTab, CloseTab, SwitchTab/Next/Prev |
| `tab.rename` | `{num, name}` | RenameTab |
| `workspace.create/close/focus/rename` | `{id?…}` | NewWorkspace etc., WorkspacePicker effect |
| `agent.focus` | `{pane}` | FocusAgent/Next/PreviousAgent effect |
| `server.reload_config` | `{}` | ReloadConfig |
| `server.stop` | `{}` | quit |

Not commands (browser-local): sidebar toggle, help/menus/pickers, copy-mode motions
(browser cursor over its own grid + `scroll`/`read`), rename dialogs. Not v1 (WS2+ can add
under the same envelope): worktree ops, `agent.send`/`pane.send_text` (API-only), custom
command keybinds, detach (meaningless in-browser; the tab just closes).

Since added under the same envelope: worktree ops (`worktree.*`), and the text-injection
command — `pane.send_input` `{pane, text?, submit?}` — text paste-encoded against the
pane's live modes plus an optional real Enter (`submit`), the API half that lets an
automation client (`catctl send`) drive a pane it isn't rendering.

## 8. Visibility & frame streaming policy

The server streams `pane_frame`/`pane_diff` **only for panes in the connection's active
workspace+tab** (its viewport). On `tab.focus`/`workspace.focus` (or server-side focus
change broadcast), the server sends the new viewport's `layout` then a **full** frame per
newly-visible pane. Chrome events (§3) are **not** visibility-filtered — `agents`, titles,
cwd flow for all panes (sidebar needs them). The daemon always streams everything over β
(detection needs it); filtering is at the server→browser hop.

## 9. Disposition of the old protocols & seam open questions

| Old | Disposition |
|---|---|
| α `init/input/paste/image/resize` up | `init`/`raw`(deprecated)/`paste`/`image`/`resize` — `input` becomes structured `key`/`mouse` (D4) |
| α `frame/fdiff` (CSS colors, single grid) | `pane_frame`/`pane_diff` per pane, packed u32 (D1/D2) |
| α `title/mouse/clipboard/notify/error/shutdown` | `title`/`pane_modes.mouse`/`clipboard`/`notify`/`error`/`shutdown` |
| α titleHub `POST /title` | kept server-side, emits `title` |
| α JS key SPECIAL table + SGR mouse encoding (`index.html:252-290`) | **deleted** — server encodes (D4) |
| β (all messages) | unchanged; server-internal (daemon seam). This spec's `pane_*` chrome + `scroll` + `read` are translations of β events/commands |
| γ `internal/wire`/`herdrconn` | unreferenced by the new path; deleted in WS11 |

Answers to β's open questions (`phase-b-orchestration-seam.md:137-151`): input-encoding
ownership → **Go server** (D4, §6); OSC passthrough → implemented as β `pane_*`, now
forwarded (§3/§5); kitty graphics → still deferred (§10); scrollback/selection → `scroll` +
`read` over β's ScrollViewport/RequestSelection (§7); binary cells → deferred behind a
version bump (§10).

## 10. Deferred (explicit non-goals for v1)

Binary cell encoding (WS binary frames, same message semantics); kitty graphics; layout
diffs; per-connection independent viewports beyond active-tab (e.g. two browsers on
different tabs is ALLOWED — viewport is per-connection — but pinning/multi-view UI is not
spec'd); auth/TLS/origin (WS10); `Node`-tree serialization (WS3); worktree/settings/help
commands (add under §7 envelope as WS2 ports them).
