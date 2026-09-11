# The workspace dot takes over, and says whether the repo is still current

Session: https://claude.ai/code/session_01DcxG4ANNKTGJiVUz8QVot5
Date: 2026-09-11
Repo: `~/projs/go/cats` (branch `main`)
Commit: `19da315`

## Request

> For the active workspaces take over the small circle next to the workspace
> name. Currently I think that dot just indicates focus.
> I want it to indicate whether the repo is in-sync with its remote on the
> master or main branch.
> - A red-ish magenta to indicate the remote has commits we don't
> - Blue (same blue we use to indicate an agent is done) when we have commit to push
> - Green when we are completely in-sync
> Pool every 2 mins

## What the dot was

One line in `renderWorkspaces` (`cmd/catway/web/js/07-workspaces.js`):

```js
name.textContent = (w.active ? "● " : "○ ") + w.name;
```

A text node. Filled meant "the keyboard is here", hollow meant it isn't, and
the whole thing was uncolourable because it was glued to the name.

## The design: two channels, one glyph

"Take over" did not have to mean "give up focus". The dot has two independent
channels and the request only claims one of them:

```
shape    ● / ○     which workspace the keyboard is in   (unchanged)
colour   class     is this checkout level with its remote?  (new)
```

Focus was always carried by the *shape*, and `#ws-list li.active { color:accent }`
says it a second time on the whole row. So nothing was lost by taking the colour.

The dot became a nested span inside the name span, with an ordinary text space
between them:

```js
name.appendChild(gitDot(w));
name.appendChild(document.createTextNode(" " + w.name));
```

Nested rather than a sibling because the row is `display:flex; gap:6px` — a
sibling dot would suddenly sit a gap away from the name it belongs to — and
because the name span stays the row's first child, which the drag reorder, the
rename and the hover card all already assume.

### CSS specificity, the part that would have silently broken

```css
#ws-list li .ws-dot.git-synced { color:var(--git-synced); }
#ws-list li .ws-dot.git-ahead  { color:var(--git-ahead); }
#ws-list li .ws-dot.git-behind { color:var(--git-behind); }
```

Written through `#ws-list` with two classes (1,2,1) so they out-rank
`#ws-list li.active` (1,1,1). A bare `.git-synced` (0,2,0) would lose on the
focused row alone — the one row the user is most likely to be looking at.

### The palette

```css
--git-synced:var(--ok); --git-ahead:var(--done); --git-behind:#e0559b;
```

Two are `var()` aliases onto tokens that already mean this, following the
`--flag-*` family's precedent (CSS-level aliases, not entries in
`internal/theme`'s vocabulary). `--done` is the cyan the sidebar already uses
for "an agent finished and you haven't looked yet", which is the same sentence
as "you have commits and haven't pushed them".

`--git-behind` had to be invented. It can't be `--err` — that red means "this
pane died" one span over, and a colleague pushing is not a fault — and `--warn`
is already occupied by `--todo`.

## internal/gitsync — how the answer is found

New package. Deliberately forks git, where `internal/gitbranch` reads
`.git/HEAD` directly: the whole question is about a sha on another machine, so
a network round trip is unavoidable, and once a process is being forked the
file-reading version of the rest (packed-refs, worktree `commondir`, per-branch
remote config, config includes) is a reimplementation of git's lookup rules for
no saving.

```
for-each-ref   local    main (else master) and its tip      ─┐
config         local    branch.<trunk>.remote, else origin   │ 3 forks in the
ls-remote      NETWORK  the remote's tip for that branch    ─┘ common case
  shas equal -> synced, stop here
cat-file -e    local    do we even hold their commit? no -> behind
rev-list       local    --left-right --count, both directions at once
```

### It does not fetch

`git fetch` would give exact counts in both directions. It also mutates a
repository the user is actively working in — new objects, moved remote-tracking
refs, a reflog entry — every two minutes, forever. **A status indicator must not
change the thing it reports on.** The cost: "behind" carries no number, because
those commits are not here to be counted. "Ahead" does.

### Diverged reads as behind

Both sides having commits collapses into `Behind`. The indicator's job is to say
what has to happen next, and in both states that's a pull. Someone told "behind"
who pulls finds out immediately; someone told "ahead" who pushes finds out via a
rejected push, having already decided they were fine.

### hardenedEnv

The difference between a background poll and a hung one. A private repo over
https with an expired credential helper, or over ssh with a passphrase-locked
key, would by default sit waiting for a human at a tty that does not exist —
reaped only by the timeout, every two minutes, forever.

```
GIT_TERMINAL_PROMPT=0        no tty prompt
GIT_ASKPASS= / SSH_ASKPASS=  no GUI dialog
SSH_ASKPASS_REQUIRE=never
GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10
```

The user's config is otherwise untouched, so non-interactive helpers (the macOS
keychain, a cached token) still work — which is what makes a private repo
resolvable at all.

Timeouts: 10s local, 30s network.

## The sweep — cmd/catway/gitsync.go

Three hops, because the two ends have opposite requirements: the session may
only be read on the loop goroutine, the resolution may only happen off it.

```
runWorkspaceGit    own goroutine, 10s start delay, 2-minute ticker
  -> o.post        onto the loop: gather (ws id, dir) for LOCAL workspaces
  -> go            off again: bounded 4-at-a-time fan-out, gitsync.Resolve
  -> o.post        back on: store, and broadcast only if something changed
```

The change check is what makes the steady state free: a session where nobody has
pushed resolves to the same states forever, and re-sending an identical list
every two minutes would re-render every workspace row for nothing.

`wsGitBusy` stops a slow sweep stacking on the next tick — with an unreachable
remote a sweep can take the full network timeout per workspace.

### Which workspaces are asked about

- **Directory** is `ws.IdentityCwd` — the same field the row's *name* is derived
  from, so the dot and the label always describe the same checkout. No fallback
  to the daemon's own cwd, which would paint every identity-less row with the
  state of wherever catway happened to be started.
- **Remote-host workspaces are skipped**, not guessed at. Their path names a
  directory on *that* machine; for a monorepo checked out in the same place on
  both boxes, resolving it here gives a plausible and completely wrong answer.
  (`o.workspaceHostOwns(ws, localHostID)` — the same check `paneCwd` makes.)
- **Asleep and locked are included.** Neither says anything about the
  repository, and a workspace put to bed is exactly the one most likely to have
  gone stale while nobody was looking.

A workspace that *stops* having an answer drops its colour rather than keeping
the last one: a stale "in sync" is the single reading that would actively
mislead, since it's the one that says "go ahead".

## The wire: ws_git

Its own message, not fields on `WorkspaceInfo`, for the reason `pane_branch` is
its own message rather than fields on `pane_cwd` — the two move on completely
different clocks. `layout` goes out on every split, tab switch and resize; this
moves on a two-minute network poll. Folding them together would mean either
re-sending the whole viewport when a colleague pushes, or re-resolving network
state on every keystroke that splits a pane.

```json
{"t":"ws_git","workspaces":[
  {"ws":"w1","sync":"synced","branch":"main","remote":"origin"},
  {"ws":"w2","sync":"ahead","branch":"main","remote":"origin","ahead":3},
  {"ws":"w3","sync":"behind","branch":"master","remote":"upstream"}]}
```

Sent whole. Only workspaces that *have* an answer are listed — absent is the
uncoloured state, which is also what an old client (or a client of an old
server) leaves every row in. Also sent on connect, gated on non-empty.

`branch` and `remote` ride along because neither is safe to assume: a tree still
on master, or one whose main pushes to a fork, would otherwise report a state
the user cannot account for. The tooltip says them out loud —
`main vs origin: 3 commits to push`.

## Client side

- `wsGit` Map in `01-bootstrap.js`, keyed by workspace id; a missing key is the
  uncoloured state.
- `applyWorkspaceGit` replaces the map outright — the rollup is whole, there's
  nothing to merge, and a merge would quietly keep a colour that should have
  gone.
- `gitSyncText` is the single wording, read twice: by the dot's `title` and by
  the hover card's new **Git** row. One wording, so the card can't drift from
  the tooltip it replaces while `muteTitles` has it suppressed.
- The Git row *rides along* in the card rather than qualifying it. A colour is a
  whole sentence already, and making every git checkout pop a card would be a
  card that never stops appearing.

## Tests

- `internal/gitsync/gitsync_test.go` — real repositories against a bare "remote"
  on disk, no network. synced / ahead+count / behind / diverged-reads-as-behind
  / behind-with-objects-present / master trunk / main-preferred-over-master /
  configured remote / linked worktree / packed refs / four unknown paths /
  unreachable remote / cancelled context.
- `cmd/catway/gitsync_test.go` — the sweep's own logic with answers injected, so
  no test forks git: targeting rules, cache pruning, forgetting a lost answer,
  busy-flag clearing, session-order rollup.
- `cmd/catway/web/jstest/wsdot.test.mjs` — both channels pinned independently,
  every focus × sync combination, including the focused-row-keeps-its-colour
  case the CSS specificity note exists for.

## Verification

`make fmt-check vet build test jstest` clean; `-race` clean on
`cmd/catway`, `internal/gitsync`, `wire`. Smoke-resolved this repo and
`cats-todo` live: both `synced`, ~600ms each; `/tmp` correctly `unknown` in
32ms.

Two `internal/inputenc` failures in `make test-ghostty` are pre-existing
(missing generated Dart golden, wants `FLUTTER_ROOT`) and were left alone.

## Open

- **Remote workspaces get no dot.** Doing it properly means asking their
  cathost, the way `pane_branch` does — a protocol addition, not something the
  local sweep can fake.
- The MacApp bundle needs reinstalling to pick this up; `make binaries` built
  into `bin/`, but the installed app is still the old build.
