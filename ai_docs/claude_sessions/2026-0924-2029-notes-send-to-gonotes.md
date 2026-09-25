# Session: info prompts sent to the notes plugin (N-021 / cats-todo N-030)

Session ID: 967eb4a7-afe7-4275-8d03-fd477d33a41d
Date: 2026-09-24

The same doc is saved in cats and in cats-todo under the same stem, because
both repos' next lists cite it. The work is in three repos: gonotes (the
receiver), cats-todo (the sender), and cats (a next-list closure only).

## 1. The ask

The user pasted next-list item **N-021** from `ai_docs/todo/next-list.md`:

> cats-todo: "Send info prompts to gonotes" (carried in cats-todo's own
> session docs) can now find the notes plugin by `plugin_type == "notes_mgr"`
> in `pane.list` instead of by id.

The feature it points at is cats-todo's **N-030**: "Add a drop target for
info-marked prompts that delivers them to a notes plugin… Work out gonotes'
intake contract first." Later: `/sw notes-send-to-gonotes`.

## 2. Working out the intake contract

There wasn't one. What exists:

- **gonotes** had no paste handler, no inbox, and no `add` command. Its CLI is
  `serve`, `tui`, `import-gob`, `export-md` and `import-md`. The manifest,
  `cats-plugin.toml`, already declares `type = "notes_mgr"`, with a comment
  saying that is how cats-todo can find it.
- **cats** has no plugin-to-plugin message. The wire vocabulary is `pane.*`,
  `tab.*`, `workspace.*` and so on. The only way into a running TUI is
  `pane.send_input`, and its doc says the text is paste-encoded against the
  pane's live modes (`internal/inputenc`, ghostty's `PasteEncode`). Bubble
  Tea v2 turns bracketed paste on, so the text arrives as one `tea.PasteMsg`.
- **plugin_type on a pane** is set in catway from the spawn env
  (`CATS_PLUGIN_ID` + `CATS_PLUGIN_TYPE`) when a plugin action launches it.
  Since N-035 it is also set by `tools.types` from the reported agent label,
  which maps `gonotes` to `notes_mgr` by default. So a gonotes typed into a
  shell is typed as well.

The user was offered three options (`AskUserQuestion`) and chose the
recommended one for each:

| Question | Chosen | Rejected |
|---|---|---|
| Transport | **Paste envelope** into the `notes_mgr` pane via `pane.send_input` (Submit off) | inbox dir `~/.gonotes/inbox/` (gonotes-specific path); `gonotes add` CLI (bytdb lock against a local-mode TUI, login against a server) |
| After a hand-off | **Mark the prompt done** (a done row is the undo) | leave it open |
| In gonotes | **Open an unsaved, pre-filled form** (Phase 7's capture rule) | save immediately |

The deciding argument for the paste: the process in the pane already owns the
store. It holds the bytdb lock in local mode, or the login token in HTTP mode,
so the sender needs neither.

**The envelope (v1):**

```
<!-- cats-note v1 -->
---
title: "…"
description: "from cats-todo · <project>"
tags: ["cats-todo"]
---

<prompt body, markdown>
```

- The sentinel must be the first line exactly, and it is versioned. A receiver
  refuses a newer version in words rather than guessing.
- It is an HTML comment, so an older receiver that pastes it raw leaves only
  an invisible marker in rendered markdown.
- Frontmatter keys are `title`, `description`, `tags` and `categories`.
  Unknown keys are ignored.
- cats-todo writes every value as a JSON string. That is valid YAML and needs
  no YAML dependency, and nothing in a title (`a: b`, `- x`, `#`, `---`) can
  be read as structure.

## 3. gonotes — the receiver (`08eb409`, master)

- New `tui/intake.go`:
  - `parseIntake` normalizes CR and CRLF, since a pasted newline can arrive as
    CR. It matches the sentinel and splits the frontmatter the way
    `parseNoteMd` does on import: an unterminated `---` is body. It returns
    one of three outcomes: not an envelope; an envelope it can't use (newer
    version or bad YAML), which is swallowed with a status line rather than
    dumped into the focused field; or a note.
  - `intakePaste` / `openIntake` push `newFormScreen` with `prefill` (the
    dirty baseline is not moved, so esc asks) and set the description. The
    sender's categories go in if it sent any; otherwise it uses
    `browseFilingSpec`, as capture does, via `presetCategories`.
- `tui/tui.go`:
  - A `case tea.PasteMsg` at the root, ahead of the active screen. A
    non-envelope falls through to the screen unchanged.
  - A new `appModel.heldIntake`: notes that arrive before login are held and
    opened by `loggedInMsg`.
- `tui/intake_test.go` has 9 tests covering parsing, delivery over a
  half-typed form (which stays untouched), an ordinary paste still reaching
  the focused field, held until login, and
  `TestParseIntakeReadsWhatCatsTodoWrites`. That last one uses cats-todo's
  exact output, so the two ends are pinned to each other.
- README: a new "Notes sent from other programs" section under Terminal UI.
- Trap hit: the login test hung. `loggedInMsg` schedules `syncTickCmd` (a
  `tea.Tick`), and the `drainCmd` test helper *runs* commands, so it slept
  through the poll interval. The fix is `m.sess.sync.polling = true` before
  login in the test.
- Verified: `go vet ./...` and `go test -race ./...` pass.

## 4. cats-todo — the sender (`52539fe`, main)

- New `notes.go`:
  - `notesEnvelope` builds the envelope. Attachments are listed as paths
    rather than as `composePrompt`'s "read these files" instruction to an
    agent.
  - `notesTitle` uses the prompt's title, else its first non-blank line
    (capped at 80), else "Note from cats-todo".
  - `jsonString` quotes values with HTML escaping off.
  - `pickNotesPane` keeps only `PluginType == wire.PluginTypeNotesMgr` and
    excludes our own pane. It ranks by own workspace, then Agent set (live),
    then visible, then lowest id.
  - `sendToNotes` runs `paneList` → pick → `sendInput(submit=false)` →
    best-effort `focusPane`.
  - `startNotesSend` sets `dropping` and returns a cmd that yields a
    `dropResultMsg{toNotes: true}`.
- `ui.go`:
  - The Info refusal in `startDrop` now routes to `startNotesSend`. Every Send
    path goes through `startDrop`: shift+enter, the menu, the form's ✉ Send
    and the bar chip. The dropping, socket and frozen guards still come first.
  - `dropResultMsg` gained `toNotes`. On failure the status line reads
    "send to notes failed: …". On success, `dropDoneStatus` reads
    "sent to notes → gonotes (w1:p5) · save it there", and the existing code
    appends "· marked done".
- `listmenu.go`: an info prompt's row reads **✉ Send to notes** and stays
  live. Whether a notes pane exists isn't known until the press, since a
  right-click must not dial the socket.
- `annotations.go`: `infoSendWhy` is now the no-notes-pane refusal: "no notes
  plugin open in cats — open GoNotes and send again, or clear ℹ Info to send
  it to an agent". Schedule's refusal (`infoScheduleWhy`) is unchanged.
- Tests: new `notes_test.go` (7 tests). `TestInfoPromptsRefuseToLeave` is
  narrowed to schedule, since the drop half is now `TestInfoSendGoesToNotes`.
- README Info section rewritten. `notes.go` added to the cats-todo-dev skill's
  file table.
- Trap: the Write tool turned the raw-string `` `<` `` in a test into a
  literal `<`, which made the test fail on itself. It is now written as
  `"\\u003c"`.

## 5. cats (`8f8fd2b`, main)

Next list only: N-021 is closed. No code change was needed.

## 6. Verification and a race that was already there

- gonotes: the full `-race` suite passes.
- cats-todo: the full suite passes without `-race`. Under `-race`,
  `TestProgramExitsOnHangup/sighup` fails: the helper exits 66, the race
  detector's exit code. That test only runs `idleModel`. A `git stash -u`
  comparison showed it **fails on a clean HEAD too** (an earlier 3-run pass on
  HEAD was luck). Filed as cats-todo **N-045**.
- Not exercised live in cats. The installed GoNotes plugin runs its own
  `bin/gonotes` copy, so `catctl plugin update` (or relink) is needed first,
  then a rebuilt cats-todo.

## 7. Repo state at wrap

- All three repos were pushed after the feature commits.
- While this doc was being written, another session was cutting cats-todo
  **v0.37.0**: the version bumps in `cats-plugin.toml` / `main.go`, and "Not
  yet released" → "Shipped in v0.37.0" on closed items, N-030 included. Those
  changes were left for that session to commit. Only this doc and this
  session's two next-list lines were committed there.

## Next

cats (`ai_docs/todo/next-list.md`):
Closed: N-021. Declined: None. Raised: None.
Deferred: None. Promoted: None. Updated: None.

cats-todo (`ai_docs/todo/next-list.md`):
Closed: N-030. Declined: None. Raised: N-045.
Deferred: None. Promoted: None. Updated: None.
