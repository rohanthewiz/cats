# N-060, N-040, N-033: catctl raw params, detach revokes, ui prefs pushed

Session: `c911b2be-e5ee-44d0-ad48-dfc9e1d9516f` · 2026-10-11

Third batch of the unattended `/work-next-list` run (after
`2026-1011-2316-…` and `2026-1011-2327-…`).

## N-060: `catctl capture --params …` works

- `catctl help capture` shows `catctl capture --params '{…"ansi":true}'`,
  but a verb whose name matched its method shadowed the raw path and refused
  any `--params`.
- `resolveVerb` (`cmd/catctl/subcommands.go`): `--params` with no operands
  takes the raw path when the verb is spelled like its method (capture, read,
  scroll). Operands plus `--params` are still refused, and `split
  --params` still is too (its raw spelling is `pane.split`).
- Live: an ANSI capture of a live pane returned VT text. The same capture of
  an exited pane returned N-039's refusal.

## N-040: detach-peer cleans up both ends

- `POST /peer/v1/unpair` (`handlePeerUnpair`, `peersync.PathUnpair`,
  `Client.Unpair`) revokes the grant the request is authenticated with. The
  guard already requires a credential on `/peer/v1/*`. The handler re-checks
  it to learn which grant it is, and answers 400 to anything that isn't a
  grant (the password, say).
- `PeerDetach` reads the credential before anything is removed, saves the
  config, deletes the managed token file, then unpairs on a goroutine and
  replies after the attempt. A failure (offline, an older peer's 404, an
  already revoked grant) keeps the old "stays live until revoked there" log
  line with the reason. A peer whose credential the operator manages is not
  contacted.
- Live, two scratch catways: B ran password auth on :18518, and A ran
  `--auth none` on :18517.
  - B minted a link. Its `u=` named B's LAN address while B listened only on
    loopback, so the test rewrote it to 127.0.0.1.
  - A attached, and B listed grant `f349c29a`.
  - `detach-peer` on A logged "revoked its grant f349c29a on the peer", B
    logged the revocation, and B's grant list was empty.
  - With B stopped, a detach completed and logged "connection refused; its
    grant on the peer stays live…".
- Docs: peer-sync.md and cli.md.

## N-033: ui prefs reach windows already open

- `wire.UIPrefs` / `ui_prefs`: a new down type, additive within protocol v1.
  The sizes are omitempty (0 means unset). `show_untitled_panes` is always
  sent, so false can turn it back off.
- catway sends it in the connect burst and broadcasts it after `config.set`
  or `server.reload_config` when `o.cfg.UI` differs from the section before.
- Page: `applyUIPrefsPush` applies only the fields that differ from
  `uiPrefsFile`. The saving window updated its copy before sending, so it
  ignores the echo, and a gutter being dragged again is never snapped back.
  `applyUIPrefs` merges before calling `setFontSize`, so `persistUIPref`
  writes nothing back.
- Tests: `TestConfigSetBroadcastsUIPrefs` (a ui change pushes, a panes-only
  save doesn't), `uiprefs.test.mjs`, and the wire round-trip and type count.
- Live: a Node WebSocket client saw the burst copy and the push. A Chrome
  tab open on the scratch catway went from a 278px to a 340px sidebar after
  `catctl config.set --params '{"options":{"ui":{"sidebar_width":340}}}'`,
  with no reload.

## Gotchas

- `catctl pair peer` takes its label positionally. `--label X` became the
  label "--label scratchA".
- A peer link's `u=` is the grantor's LAN address by design (never
  loopback), so a loopback-only scratch grantor needs the link rewritten.

## Next

Closed: N-060, N-040, N-033. Declined: None. Raised: None. Deferred: None.
Promoted: None. Moved: None. Updated: None. Full list:
`ai_docs/todo/next-list.md`.
