# Session: Appendix A — the three quick wins, then the command ledger and its blocks

- Session id: `26c8b19c-f436-4ee3-a9b3-746ebe5ddc1f`
- Date: 2026-08-18
- Branch: `feat/working-with-remotes` (cats)
- Plan/record: **new** — `ai_docs/plans/remote-catalog.md`
- Predecessor: `ai_docs/plans/remote-dream.md`, whose eight phases were all done;
  this session started from its Appendix A

The ask was "continue with Appendix A — quick wins first, then the plan's
recommended order". Six commits: `3e2ef32` `668a992` `476ce71` `7385294`
`53aa304` `ed3efff`.

## What shipped

| Phase | What |
|---|---|
| 1 | `ui.notify` / `ui.action` — notifications anything can raise, with buttons |
| 2 | Reply-from-notification — ntfy action buttons that answer an agent's prompt |
| 3 | `pane.open_file` — click a path, it opens in the editor |
| 4 | Shell integration (OSC 133) + the command ledger |
| 5a | Blocks — jump to a recorded command, copy its output |

5b (collapse) is recorded as deliberately deferred; see below.

## The ideas worth keeping

**An action is a declared effect, not a callback.** The caller `ui.notify` exists
for is an agent hook script that reported its agent blocked and *exited*,
milliseconds before anybody saw the notification. Anything meaning "call me back"
would be dead on arrival in the exact case the feature is for. So an action says
what to do — `{label, send, submit}` — and catway does it.

**"Answered once" is a property of a registry, not of a route.** The entry is
dropped *before* the input is sent, which makes an action whose pane exited still
spent: a phone retrying over a flaky link must not land a second "yes" because
the first attempt reported an error. Both entry points (the `ui.action` command
and the HTTP token endpoint) go through one `takeNotifyAction`, so the two can
never disagree.

**The parser that refuses more than it accepts.** `internal/promptopts` reads an
agent's menu off the pane's screen. A menu is the last contiguous run of numbered
lines with *nothing printed after it*, from 1, ascending, at least two entries.
Anchoring at the bottom is not an optimisation — numbered lines are everywhere in
ordinary output, and "nothing came after it" is the only thing separating a live
prompt from a list that finished. Nothing parsed ⇒ no buttons, never a guess,
because a wrong button types a real keystroke into a real terminal.

**A path is only half an identity, again.** `pane.open_file` scopes the editor to
the file's machine, the same rule the worktree slice learned. And cats gained no
editor integration at all: it works out *which pane* should hear the request and
emits an event that pane's editor is already subscribed to. Cross-host is free
through the phase 7 control relay; no editor CLI is known here.

**A command with no command line is not recorded.** One rule, three jobs: it
drops the empty Enter every shell emits a full A-B-C-D cycle for, guarantees a
ledger row has the field the ledger exists for, and makes the precondition one
legible sentence rather than a history quietly full of blank rows.

**`exit` is a pointer end to end.** "Finished, status unknown" is true;
"succeeded" is not. It is the field somebody filters *what failed* on, so the
`failed` filter deliberately excludes an unknown status.

**A block is two MARKS, not two row numbers.** The single best decision of the
session, and it came from reading libghostty's bindings rather than the plan.
`TrackedGridRef` is a pin: it follows its cell and reports when the row has been
discarded. Recording screen-buffer rows — which the scoping note proposed — is
wrong in the way that matters least visibly: those rows count from the top of the
scrollback, so every evicted line shifts them by one and a stored row quietly
addresses somebody else's output. Nothing about the result would look wrong.

## What the live runs found that the tests could not

Nine of these, across five phases. Every one was a wrong answer being produced
confidently.

**Phase 4 — bash is the awkward shell.**

1. `__cats_precmd` was the first row in the ledger: bash's DEBUG trap fires
   before every simple command *including each one inside `PROMPT_COMMAND`*, so
   the prompt hook traced itself. Fixed by arming the trap only from `precmd`,
   putting `precmd` last in `PROMPT_COMMAND`, and ignoring `__cats_*` by name.
2. `cd /tmp; ls x` was recorded twice, at two different directories. The first
   draft disarmed the trap from inside its own handler, which does not hold — it
   is armed again by the time the second command in a list runs. A flag cleared
   at the next prompt does.
3. `$BASH_COMMAND` is the first *simple* command, so `make && ./run` was recorded
   as `make`. The line is now read back out of `history 1` with parameter
   expansion, no fork per prompt.

**Phase 5a — blocks.**

4. Every block's text was empty: `readPump` fed the whole 32 KiB chunk to the
   emulator and *then* scanned, so every mark in a chunk pinned the same final
   cursor position. The scan now owns the feed and interleaves it.
5. Every block's text ended in a stray `b`. `OSC 133;D` comes from the shell's
   *prompt* hook, by which time the cursor has moved past the output — so the
   block ended at column 0 of the next line and picked up the first character of
   `bash-5.2$`.
6. The jump landed the block at the *bottom* of the viewport: it scrolled to a
   command whose output was entirely off-screen. `block_result` now carries the
   viewport's *current* top row rather than a buffer total, so the scroll is one
   signed subtraction that works up, down, and from an already-scrolled pane.

**And one thing the live run appeared to find and did not.** Every ledger record
showed `cwd=/tmp`, which looked exactly like the cwd being captured at the end
rather than the start. It was a *persistent* cathost — the same PTY, already
`cd`'d, surviving the catway restart. Re-run cleanly, `cd /var; echo` records
`/tmp` and the next command records `/var`, which is right. Worth the habit: before
believing a live run has found a bug, check what state survived the restart.

## Decisions taken with the user

**Storage: `btypedb`, not `bytdb`.** The module cache holds two different
products from the same author — a typed KV store and a relational engine — and
the standing preference names the latter. Asked, because the ledger's shape (an
ordered range scan over a time-ordered key) is the KV store's and the choice was
not mine to assume. Answered: `btypedb`.

**5a only.** Scoping phase 5 against the code turned up that "collapse a block"
does not fit the rendering model: cats renders the grid server-side and ships
frames, so eliding rows means the daemon rendering a viewport with holes in it,
inside libghostty's screen model. The other three verbs (jump, copy output, send
to chat) need only a scroll and a read that already exist. Raised rather than
assumed away; the user chose 5a.

## Shape of the work

- New packages: `internal/promptopts`, `internal/ledger`, `internal/shellint`
- New seam capability `command_ledger` — `request_command_marks`,
  `command_start` / `command_end`, `request_block` / `block_result`.
  `ProtocolVersion` stays 3; capabilities carry it, as since phase 6.
- New §7: `ui.notify`, `ui.action`, `pane.open_file`, `ledger.list`,
  `ledger.output`, `ledger.jump`
- New events: `ui_action`, `pane_open_file` (the first event that is a *request*
  rather than a fact)
- New down-message: `history` (pushed, not polled)
- New config sections: `editor`, `ledger`; `push.actions` / `push.action_url`
- catctl: `notify`, `open`, `history`, `output`, `jump`,
  `integration install shell [bash|zsh|fish]`
- The one new inbound HTTP surface in the whole server:
  `POST /api/notify-action/<token>`, public in the middleware and authenticated
  by a single-use token — because a phone holds no session cookie, and the ntfy
  server relaying the request sees whatever credential rides it

## Still owed

- **cats-mobile has not been regenerated**, and is now five phases behind: still
  owed from remote-dream phases 6 and 8, plus catgen-dart churn from every phase
  here. It needs cats pushed first.
- **Phase 6 — runbooks** is next in the recommended order.
- Phase 5b (collapse) if it is ever judged worth the rendering work.
