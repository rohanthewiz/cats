# cats "Remote dream" — the extras (deferred slices)

## Context

`ai_docs/plans/remote-dream.md` shipped slice 1 (one catway, N cathosts, every question
about another machine asked of that machine). `ai_docs/plans/remote-catalog.md` shipped
the catalog slices on top of it: `ui.notify`, reply-from-notification, `pane.open_file`,
shell integration + the command ledger, blocks, runbooks with `on:` triggers, file
transfer, and record-a-macro.

Two items from that plan were never started, for two different reasons — one is deferred
on a cost that has to be paid with eyes open, the other was always going to be scoped
after the ledger existed. They are moved here so remote-catalog can be read as a record of
what shipped, and so these two stop looking like unfinished business inside it.

Nothing here blocks anything there. Both are independent of each other.

## Deferred — block collapse

*(was remote-catalog Phase 5b)*

cats renders the grid server-side and ships frames, so "collapse this block" is not a
client-side fold: it would mean the daemon rendering a viewport with rows elided, inside
libghostty's screen model. Worth doing only with that cost understood rather than assumed
away — and the three verbs people actually reach for (jump, copy, send to chat) are shipped
without it.

What would have to be settled before this is worth starting:

- **Whose model holds the elision.** libghostty-vt's screen is a scrollback plus a
  viewport; a fold is neither. Either the daemon maintains a row-mapping between the real
  screen and the one it renders (and every scroll, resize and reflow has to survive it), or
  the collapse is a client-side overlay that can only hide rows the client happens to be
  holding — which is not the case for anything that has scrolled out.
- **What a collapsed block does when the pane resizes.** Block bounds are OSC 133 marks,
  which move with reflow; a fold drawn over rows is not the same object as a fold over
  marks.
- **Whether it survives the frame diff.** The frame stream is a diff over the drawn grid,
  so a fold changes what "the same screen" means for every client attached to that pane,
  including ones that never asked for it.

The honest alternative, if collapse turns out to cost more than it returns: the block
verbs already shipped (`catctl output`, `catctl jump`, send-to-chat) cover the reasons
people fold — get past it, keep it, act on it — without touching the rendering model.

## To scope — adjacent stars

*(was remote-catalog Phase 8)*

Record & replay, port preview, agent migration between hosts, presence, the global palette.
Scoped when the shape of the ledger and the §7 journal are known in use — which they now
are, so each of these can be scoped on demand rather than as one phase.

From Appendix A of remote-dream, with what slice 1 and the catalog left in place for each:

| Item | What it already has | What it still needs |
|---|---|---|
| **Record & replay** | The command ledger (`internal/ledger`, OSC 133 blocks) and the agent-state timeline from the hook API | A capture format that pairs asciicast frames with the agent-state track, and a player |
| **Port preview** | Nothing — this is the one with no foundation here | catway proxying a host's `localhost:PORT` through its own HTTP surface, behind `authGuard`, as a preview tab. Kills `ssh -L` for the demo case |
| **Agent migration between hosts** | Per-pane `HostID`, respawn-on-another-host (Phase 5's forced detach does exactly this), agent resume ids | Moving a *live* agent's resume state across machines, which is the agent's own notion of a session, not a pane's |
| **Presence** | Multi-client attach, pairing, per-client viewports | Per-user identity, coloured focus rings, read-only watch links |
| **Global palette** | The whole §7 vocabulary, the ledger, runbooks, host roster — every source it would search | One client-side surface that searches across hosts / panes / commands / history / runbooks / notes |

Per-host bookmarks/frecency and the agent activity heat strip from Appendix A belong here
too, and are small next to any of the above.

## Verification

Same as the other two plans: `make test` and `make test-ghostty`; regen
`cmd/catgen-dart/testdata/golden` whenever `internal/app`, `browserproto` or
`orchestration` wire structs change, then cats-mobile per memory;
`TestCommandSpecsRouted` for each new command.
