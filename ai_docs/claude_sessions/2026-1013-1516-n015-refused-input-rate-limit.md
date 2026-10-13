# N-015: refused input reported once per episode; next-list review

Session: `9912fa41-2f6c-47a8-8567-394eeb33ccad` · 2026-10-13

Two parts. The session started with `/next-list`, a review of the living
list. Then the user pasted N-015 from the backlog, and it was fixed. Code
commit: `e3551fb`.

## /next-list review

- The bookkeeping checks out: N-001 to N-060 are all present, none
  duplicated, and Next ID N-061 is correct. Every Raised, Closed and Moved
  note in the last 15 session docs matches the file, and all of those docs
  use the `Closed: … Raised: …` form.
- The user's uncommitted move of N-055 into Roadmap went in with this
  session. The "None yet." line it left above the item was removed.
- Two premises were stale and were corrected in place:
  - **N-010:** the Homebrew half was already done. ced `84e60d3`
    (2026-09-14, minutes after the item was raised) dropped the Homebrew
    instructions, and it changed the `alt` comment to name the cats
    plugin's ced. Only the `install.sh` section and the `alt` target are
    left to decide.
  - **N-042:** cats-todo is no longer behind; it's up to date at v0.45.0.
    `catctl plugin check` shows ced as the one pending update
    (63c7a41 → 6cba63d), so ced is the plugin to test a single update with.
    **Update all** waits for a second pending plugin.
- The other premises still hold. N-009: `.zshrc` has only the `shellinit`
  eval, with no `cats.zsh` integration. N-016: only SGR reports are
  droppable (`isDroppable`). N-019: ced still releases by hand ("Release ced
  0.4.3"), while `release.yml` and the README describe the `release` branch.
  N-020: cats-mobile stores `Session.Plugins` but nothing draws it. N-054:
  the prompts chip is still the last field. N-001: spot-checked identifiers
  are all present.
- N-055 is not ready to promote. codex isn't installed, and `~/.codex`
  holds only data from July. opencode is installed and has
  `~/.local/share/opencode`, which would be the first reader to add.
- Left with the user: commit `401a375` (horizontal wheel, landed today with
  no session doc) is unchecked in the WKWebView and has no list entry.
  N-015, N-016 and N-020 were named as candidates for Roadmap or Non-goals.
  Then the user asked for N-015 to be done.

## N-015: what changed

catway turns every cathost error event into a `daemons.log` WARN and a toast
in every window. A pane whose lossless input backlog is full refused every
message with its own event, so typing into a wedged pane, or a script
sending to one in a loop, spammed both.

- `internal/orchestration/paneinput.go`: a new `fullReporter` with
  `fullReportEvery = 10s`.
  - `refused(now)` returns the bare `errPaneInputFull` for an episode's first
    refusal. Within the interval it returns nil and counts the refusal as
    folded. After the interval it returns `errPaneInputFull` wrapped with
    "; N more refused in the last 10s", or the bare error if nothing was
    folded.
  - `accepted()` ends the episode, so the next refusal is reported at once.
  - It has a mutex for safety, but in practice input comes only from the
    one attached session's read loop, so the lock is never contended.
- `internal/orchestration/host.go`: each `pane` carries a `fullReports`
  field. The `MsgInput` case calls `accepted()` on success, sends
  `errPaneInputFull` through the reporter, and emits other errors as before.
- Design choices, written up in the type's comment:
  - The limit lives in cathost, at the point where the event is emitted, so
    both the log and the toast are spared. catway is unchanged.
  - Only dispatch's refusals count. The emulator's query replies share the
    queue but drop their errors, so counting them could spend an episode's
    first report on an event nobody sees. That would then silence the
    user's own refused keys for an interval.
  - Refusals folded in an episode's final interval are not reported on
    their own. The pane has already been reported, and recovery shows in
    the pane.

## Tests

- `TestFullReporterRateLimits` (`paneinput_test.go`) uses a fake clock. It
  checks: the first refusal is bare; refusals inside the interval are
  folded; the re-report says "3 more refused in the last 10s"; one folded
  refusal still counts ("1 more refused"); an interval with none folded
  re-reports bare; and `accepted()` makes the next refusal immediate.
- `TestHostRefusedInputReportsOncePerEpisode` (`host_test.go`, ghostty tag)
  runs `Attach` over a `net.Pipe`. It adds a hand-built pane after the
  welcome, with an 8-byte budget and no `inputPump`, and removes it before
  teardown. An input to pane 99 marks the end of each burst. Twenty-one
  refused messages give one error. After the test drains the queue, an
  accepted message and then a refusal give one immediate error. With the
  old per-message emit put back, the test failed with twenty-one errors.
- Run with
  `PKG_CONFIG_PATH=$PWD/third_party/libghostty-vt/zig-out/share/pkgconfig`
  and `-tags ghostty -race`. `internal/orchestration` and `cmd/catway` pass.
  gofmt and vet are clean for both tagged and untagged builds, and the
  untagged `go build ./...` builds.
- Not seen live: a real pane only refuses input after 16MB of it goes
  unread (`2 * MaxFrameSize`).

## Next

Closed: N-015. Declined: None. Raised: None. Deferred: N-055. Promoted: None.
Moved: None. Updated: N-010, N-042. Full list: `ai_docs/todo/next-list.md`.
