# Session: files across the seam, and the loop that does the chunking

- Session id: `964b359c-6c3f-4b2c-a506-383dea7db6c9`
- Date: 2026-08-19
- Branch: `main`
- Plan/record: `ai_docs/plans/remote-catalog.md` (Phase 6c scoped, Phase 7 done)
- Predecessor: `2026-0819-1035-triggers-and-the-loops-that-stop-themselves.md`

The ask was two things: scope 6c into the plan, then build Phase 7. Both are
done; Phase 7 is verified live and recorded. The plan's one-line description of
Phase 7 said "chunked over the seam", and that turned out to be the wrong shape
for a reason that only shows up when you count the ceilings between the two ends.

## Part 1 — Phase 6c, scoped but not started

**The blocker is real and is the whole phase.** The ledger records *shell*
commands via OSC 133; no journal of §7 commands exists anywhere. 6a and 6b
already built everything downstream of "turn what I just did into a runbook" —
the document format, `params:`, step refs, `on:` — so all of 6c is upstream of it.

Four decisions written down, because each one has an obvious wrong answer:

- **The journal hooks at `app.Dispatcher.Dispatch` and nowhere else.** It is the
  choke point browser `cmd`, catctl, the control relay, plugin binaries, a
  runbook's own steps and a trigger's run all already pass through, so a recorder
  there records the *vocabulary* rather than one client's use of it. That it also
  catches runbook steps is a feature: recording while a runbook runs should
  produce a runbook that does the same thing.
- **Effects, not queries — declared, not inferred.** "Has an effect" is not
  derivable from the table (`pane.split` returns a result and is very much an
  effect), so it is a third dispatch property beside `ReplyRequired` and
  `ParamsRequired`. Declared for the reason the other two are: the next command
  added must have to answer the question.
- **Redaction is per FIELD and cannot live in the recorder.** `chat.send` carries
  what was typed to an agent, `config.set` carries whatever was set, and
  `pane.send_input` carries keystrokes — simultaneously the most private field in
  the vocabulary and the one a macro exists to replay. So it is a `cats:"secret"`
  struct tag walked by the same reflection `runbook.EventMap` uses. The default
  for an untagged field has to be *recorded* (a recorder that dropped unknown
  fields would silently emit runbooks that do not reproduce what was done — 6b's
  `omitempty` bug in a different hat), which means the safety has to come from a
  **table test that fails the build** when a params struct grows a field no
  classification covers.
- **Armed recording over an always-on journal.** `runbook.record start/stop`,
  held in memory, nothing written until a name is given. The always-on version is
  strictly more powerful and strictly worse to own: a durable store of every
  parameter of every command including every chat message, kept by default, on a
  machine somebody else may administer. It can be built later on the same
  recorder, inheriting a working redaction rather than inventing one.

**And the reason it is not an afternoon:** a recorded `pane.send_input {pane: 7}`
replays into whoever is pane 7 tomorrow. The emitter has to rewrite pane
references into step refs (`{{ steps.<id>.pane }}`) for panes the recording
itself created, and a reference it did not create has no honest rewrite — so the
refusal has to arrive with the fix in the same sentence.

## Part 2 — Phase 7, and why "chunked over the seam" was wrong

Count the hops between a caller and a remote disk:

```
catctl ──ctlproto──▶ catway ──seam frame──▶ cathost ──▶ disk
   (or a pane's catctl ──control relay──▶ catway, on a remote box)
```

Every one is a **whole-message** transport with its own ceiling.
`orchestration.MaxFrameSize` is 8 MiB; `controlRelayMaxLine` is 4 MiB; and JSON
renders `[]byte` as base64, so a payload costs 4/3 of its size on all of them. A
streaming transfer would have had to invent its own chunking to fit inside those
anyway — and would then own the state: half-open transfers, abandoned readers, a
cathost holding descriptors for a catway that went away.

So the primitive is **stateless and positional** and the chunking is the
**caller's loop**. One seam message pair (`request_file` / `file_result`, the
worktree pair's shape with `filexfer.OpRequest` inside it), one capability, one
request and one answer per chunk, nothing held open — and the one-shot case
("read this 40-line config") needs no loop at all. `catctl cp` is the loop.

Four decisions the writing produced:

**A whole-file read of a large file is REFUSED, not truncated.** `file.get` with
neither offset nor length means "the whole file" and errors with the size when it
will not fit in one chunk. Handing back the first megabyte with `eof:false` is
indistinguishable from the whole file to a caller that did not check the flag —
and a caller asking for a whole file without ranging it is exactly the caller who
would not check. Same family as 6b's `omitempty`: the failure is silent and looks
like success.

**`more` is inverted on purpose.** A write marks the chunks that are NOT last, so
the default — no flag — is "this put is the whole file" and the naive one-shot
caller gets an atomic write for free. The obvious spelling (`final` on the last
chunk) would make every flagless call write a part file that never lands.

**Writes go through a part file and rename.** `.name.cats-part`, renamed by the
chunk that is not `more`, so an interrupted transfer leaves a visible fragment
rather than a truncated file under the name a script is about to read. That
matters *here* specifically because the client doing the chunking is on the far
side of a network that can drop. The overwrite refusal runs on every chunk rather
than only the first, and a refusal removes its own now-dead fragment.

**`MaxChunk` is a constant, not a config key.** 1 MiB, in `internal/filexfer`,
because it is not a preference — it is what the transports allow. A knob would
let an operator choose a value that makes every transfer through a relayed pane
fail, with a symptom ("connection closed") a long way from the setting.

Two more shaped by precedent rather than by discovery:

- **No config gate at all.** A considered `files.enabled` was dropped: it would
  sit on catway while the disk being exposed is the HOST's, and it would gate
  something that grants no new privilege — a control-socket caller can already
  `pane.send_input` a `cat` and read the pane back, and a peer holding the seam
  can already spawn arbitrary processes there (`create_pane` takes a command and
  an argv). `path.list` and the worktree commands read and write another
  machine's filesystem with no switch either.
- **Local files take the in-process path**, like `path.list` and unlike the
  worktree commands. The "one implementation" argument that sends a worktree op
  to the daemon even locally does not apply, because a file operation *is*
  `internal/filexfer` — calling it directly is the same code. It also means
  transfer works against a local cathost too old to advertise the capability.

## Part 3 — the browser half

Dropping a file on a pane uploads it into that pane's cwd, on that pane's
machine, through the same `file.put` — no new browser-protocol message, because
`handleCmd` dispatches by name.

**The browser never learns the cwd.** It sends a bare filename and lets
`file.put` resolve it against the anchor pane's live cwd on the answering
machine. Reading the cwd client-side would use the value as of the last
`pane_cwd` event, so a file dropped just after somebody `cd`'d would land in the
previous directory with nothing to indicate it.

Smaller things the page needed: a depth counter around `dragenter`/`dragleave`
(they fire per child element, so a plain add/remove flickers the highlight off
the moment the cursor crosses from the canvas onto the chrome); a `Files`-type
check so in-app pane-swap drags do not light up an upload target; a basename
that keeps its dot (`.bashrc` is a name, `.` and `..` are not) since a directory
drop hands over a relative *path*; and a per-chunk timeout, because a dropped
WebSocket resolves no callback at all and the upload would otherwise hang with no
toast either way.

## What the live runs confirmed

Two cathosts and an isolated catway (own `XDG_CONFIG_HOME`, own state dir,
sockets under `/tmp/f7`; the MacApp session untouched, and only the three test
processes stopped, matched by socket path):

- a 3 MB binary copied host→local **byte-identical** over three chunks; also
  local→host, host→host, `.` and a trailing slash as destinations
- the overwrite refusal, then `-f`; a missing file, a missing destination
  directory and an unknown host each refused by name
- `~` resolved on the **answering** machine; a relative path resolved against the
  anchor pane's cwd — and that anchor correctly **dropped** when the addressed
  host was not the anchor's, so the relative path failed rather than silently
  resolving against a directory from another filesystem
- mode carried, so a copied script stayed executable
- a deliberately abandoned put leaving only `.half.bin.cats-part`, and the next
  chunk renaming it into place
- the whole-file refusal naming the size and the fix

The browser half was driven headlessly by a WebSocket client sending the page's
own `file.put` sequence (Node 22 has a global `WebSocket`): 2.5 MB into a pane on
the remote host in three chunks, checksum-identical, landing in that pane's cwd —
and a second drop of the same name refused. The page's script also passes
`node --check`.

One thing checked by reading rather than running: rweb's WS message cap is 10 MB,
so a 1 MiB chunk at 1.37 MiB of base64 has room; and `ctlproto`'s `ReadBytes`
framing has no line cap at all, so the control socket is not the binding
constraint — the relay's 4 MiB is.

## Shape of the work

- `internal/filexfer/` — the whole primitive: `OpRequest` / `OpResult`, `Do`,
  the length policy, the part-file discipline, `MaxChunk`
- `internal/orchestration/protocol.go` — `FeatureFileTransfer`, `RequestFile` /
  `FileResult`; `host.go` — the dispatch case, off the dispatch goroutine
- `internal/app` — `file.stat` / `file.get` / `file.put` in the command table,
  their params and results, three `Backend` methods, three dispatch cases (with
  the reply gate first, as `ledger.output` does)
- `cmd/catway/files.go` — `fileAnchor` (which machine, what a relative path
  anchors against), `runFileOp` (local in-process, remote over the seam),
  `fileResponder`; `catway.go` / `daemon.go` — `reqFile`, `fileKey`,
  `fileTimeout`, the `file_result` route
- `cmd/catctl/cp.go` — the transfer loop, the scp notation, destination
  resolution; wired into `main.go` before the flag re-parse, plus help and
  completion
- `cmd/catway/web/index.html` — `attachDrop`, `uploadFile`, `putChunk`, and a
  `.pane.filedrop` cue distinct from the in-app `.droptarget`
- Docs: `orchestration-seam.md`, `control-api.md`, `cli.md`
- Tests: `internal/filexfer/filexfer_test.go`, `cmd/catway/files_test.go`,
  `cmd/catctl/cp_test.go`, additions to `internal/app/commands_test.go`

## Still owed

- **cats-mobile regen** — catgen-dart goldens are regenerated and `[]byte` lands
  as `Uint8List`, so the phone gets typed `file.get` / `file.put`; the sibling
  repo is not done (push cats first; see memory). This is now owed from 6b as
  well as 7.
- **Phase 6c** — scoped above, not started.
- **Phase 8 — adjacent stars** is next: record & replay, port preview, agent
  migration, presence, the global palette.
- **`make fmt-check` is still red on `internal/push/push_test.go`** —
  pre-existing on main, a gofmt-version disagreement, untouched by this work.
  Every other gate (`test`, `test-ghostty`, `vet`, `vet-ghostty`) is green.
- Phase 5b (collapse) still deliberately deferred.
