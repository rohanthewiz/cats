# Session: a pane can no longer clear the clipboard (OSC 52)

Session ID: 60d98b2f-5d90-4c67-92de-cdd6810ae121
Date: 2026-10-07
Driven from: cats

The request came from the user's cats-todo backlog, not the next-list. It was
pasted as a prompt.

## Background

A pane app that emits an empty OSC 52 write (`ESC ] 52;c; BEL`) used to clear
the user's macOS clipboard with nothing on screen to show it:

```
child ──ESC]52;c;BEL──▶ cathost osc52Scanner ──pane_clipboard{data:""}──▶ catway
      ──clipboard{data:""}──▶ page clipWrite("") ──▶ catapp pbcopy (no stdin)
      ──▶ pasteboard completely empty
```

`parseOSC52Clipboard` decodes an empty payload as a clear on purpose, mirroring
cats's (Rust) `parse_osc52_clipboard_write`. On 2026-10-05 the user copied
something, pressed ⌘V in the cats-todo prompt editor inside Cats.app, and got
"clipboard has no text". `osascript -e 'clipboard info'` returned nothing. The
cause was never identified, and a password manager's timed clear is just as
plausible, so this is hardening rather than a confirmed bug.

The two options were (1) ignore empty writes or (2) honour them and toast
"pane N cleared the clipboard". **Chose option 1.** A dropped copy costs the
program nothing, while a clear destroys whatever the user last copied
anywhere on the machine. A toast would explain the loss without preventing
it. A WARN log line keeps the traceability that option 2 was after.

## 1. The change

### Where the filter lives: catway, not the parser

`cmd/catway/daemon.go`, in the `MsgPaneClipboard` case of `dispatch`: an event
with `len(ev.Data) == 0` is dropped before `o.post` and logged as
`dlog.Warnf("catway: ignored an OSC 52 clipboard clear from pane %d on %s", …)`.

Why not `parseOSC52Clipboard`:

- **cathost is persistent across catway upgrades.** A parser change would not
  take effect until that daemon happened to be restarted, and restarting it
  ends the user's shells. The catway check takes effect with the build the
  user just installed.
- **The parser stays a faithful decoder** and stays in step with cats's
  `parse_osc52_clipboard_write`, as its doc comment says. The Rust
  implementation is retired (README) and no checkout exists on this machine,
  so the Go parser is the only copy. It is unchanged; its doc comment now says
  the policy lives in catway.

Why WARN: it is a refused request (dlog's definition), and catapp keeps only
WARN and above in `daemons.log`. That makes the log the place to check when a
clipboard vanishes.

### Page

`cmd/catway/web/js/19-messages.js`, `"clipboard"` case: `if (!msg.data) break;`.
This covers a page attached to an older catway, and follows the wire contract's
new "treat empty as a no-op" rule. The page is normally served by the same
catway, so this is defense in depth.

### Contracts and comments

- `internal/orchestration/osc52.go`: `parseOSC52Clipboard`'s doc says the clear
  is still decoded (mirroring Rust) and catway drops it.
- `internal/orchestration/protocol.go`: `PaneClipboard`'s doc says the Host
  reports clears and catway does not relay them.
- `wire/down.go`: `Clipboard`'s doc says Data is never empty, and a client that
  gets an empty one should treat it as a no-op. cats-mobile already ignores
  `MsgClipboard` entirely (`internal/catsclient/session.go`), so it needs no
  change and no re-pin.

### Docs

- `README.md`: the Copy mode bullet says a pane can set the clipboard but not
  read or clear it, and that the clear is dropped and logged.
- `docs/index.md`: the same, briefly.
- `docs/reference/troubleshooting.md`: new entry, "⌘V says 'clipboard has no
  text' right after a copy". It says the cause is not a pane, lists likely
  causes, and gives the `daemons.log` line to look for.
- `docs/protocols/browser-protocol.md`, `docs/protocols/orchestration-seam.md`:
  the "empty data is a clear" rows now say catway drops it.
- `docs/protocols/control-api.md`: the "why read-only" note adds that the
  write path is narrower than OSC 52 too (no clear).
- The in-app help overlay (`32-help.js`) was not changed. It is a key-binding
  reference and a policy note did not fit there.

## 2. Tests

- New `TestPaneClipboardClearIsNotRelayed` (`cmd/catway/clipboard_test.go`,
  ghostty tag). It dispatches `pane_clipboard` with `[]byte{}` (`""` on the
  wire), `nil` (`null`), and then `"hello"`. It runs the posted mailbox closures
  by hand, without starting `o.run` and its dialer, and asserts that the client
  received exactly one `Clipboard`, "hello". The "hello" is there so a handler
  that dropped every write would fail.
- Mutation check: with the guard disabled (`if false && …`) the test fails with
  `relayed = ["" "" "hello"]`. The guard was restored afterwards.
- `TestOSC52ClearClipboard` (the parser) is unchanged apart from a comment
  pointing at the catway test.
- Green: `go test ./...`, `go test -tags ghostty ./cmd/catway
  ./internal/orchestration ./wire`, and `make jstest`. The webview_go cgo
  warnings in the build output are pre-existing third-party noise.
- Not run: the hands-on check in Cats.app, which needs a rebuilt app (added to
  N-001). The test command must not be run in a pane on the currently
  installed build: that build still relays the clear and would empty the
  user's pasteboard.

## Next

Closed: None. Declined: None. Raised: None.
Deferred: None. Promoted: None.
Updated: N-001 (new sub-check: the dropped OSC 52 clear in a rebuilt Cats.app,
and what a recurrence without the log line would mean). Full list:
`ai_docs/todo/next-list.md`.
