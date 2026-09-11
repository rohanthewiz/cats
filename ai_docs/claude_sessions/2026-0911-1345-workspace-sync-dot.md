# The workspace dot takes over, and says whether the repo is still current

*Two passes: the dot itself over local checkouts, then the same answer for
workspaces whose checkout lives on another machine.*

Session: https://claude.ai/code/session_01DcxG4ANNKTGJiVUz8QVot5
Date: 2026-09-11
Repo: `~/projs/go/cats` (branch `main`)
Commits: `19da315` (the dot, local workspaces), `cbece0c` (remote workspaces
via cathost)

## Request

> For the active workspaces take over the small circle next to the workspace
> name. Currently I think that dot just indicates focus.
> I want it to indicate whether the repo is in-sync with its remote on the
> master or main branch.
> - A red-ish magenta to indicate the remote has commits we don't
> - Blue (same blue we use to indicate an agent is done) when we have commit to push
> - Green when we are completely in-sync
> Pool every 2 mins

and, once the first half landed:

> Now do the same for remote workspaces via cathost

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

## Where the question is asked

A directory is only meaningful on the machine that holds it, so the question
goes to that machine — the same argument that moved branch resolution onto the
daemons in protocol v3. A workspace pinned to devbox names a path in devbox's
filesystem, and answering it in catway reports on whatever local directory
happens to share the name. For a monorepo checked out at the same place on both
boxes that is a *plausible and completely wrong* answer, which is worse than
none.

The first commit skipped remote workspaces for exactly that reason and left it
open. The second closed it.

### The seam: request_git_sync

```
request_git_sync  {id, dir}     ->  git_sync_result  {id, status}
```

Behind a `git_sync` capability, so it is **additive**: the feature list exists
precisely because `NegotiateVersion` refuses a peer newer than the local build,
so bumping the protocol to announce one new request would be rejected by every
already-deployed daemon one version behind. A daemon that does not advertise it
is simply never asked.

Same shape as `request_worktree`, with each of its properties needing a stronger
justification:

| | worktree | git sync |
|---|---|---|
| off the dispatch goroutine | `git worktree add` takes seconds to minutes | `ls-remote` waits on someone else's forge, to a 30s timeout |
| id-correlated, not ordered | git finishes in its own order | one remote is reachable, the next is not |
| failure is a result, not an event | the dialog shows git's stderr | *no* error field at all — see below |

**No error field.** Not a repository, no remote, unreachable, git missing — all
of them are the zero `Status`, which the sidebar draws as an uncoloured dot. An
`Error` event would put a toast in somebody's browser every two minutes for a
machine that is merely asleep.

**No subscription.** The daemon holds no state. catway asks again on its own
schedule, and an abandoned sweep costs the daemon nothing beyond the answers
already in flight.

### The local host is asked too

Once a daemon can answer, it answers for *every* host including the local one —
one code path, one environment doing the resolving. This follows
`runWorktreeOp`, which already prefers the daemon for the local host, and
`gitbranch.go`, where a v3 local cathost resolves its own panes' branches.

The in-process `gitsync.Resolve` survives as the fallback for the local machine
when its cathost cannot answer (an older build, or not connected yet). For any
other host there is nothing that could stand in, so the workspace is left out.

## The sweep — cmd/catway/gitsync.go

```
runWorkspaceGit    own goroutine, 10s start delay, 2-minute ticker
  -> o.post        onto the loop: the session is read here and nowhere else
     gitSyncPlan   split into "ask the host" and "do it here"
  |-> d.send       one request_git_sync per remote target, id-correlated
  |                  ... each answers on its own schedule via pendingReqs
  \-> go           the local fallback batch, bounded 4-at-a-time, off the loop
  -> o.post        every answer lands back on the loop as a collect()
     apply         when the LAST one is in: store, broadcast if it moved
```

The hops are forced: the session may only be read on the loop goroutine, and
neither git nor a daemon round trip may happen on it.

### Converging two transports

A sweep's answers now arrive from two places on two schedules, which is what
`gitSweep` is for:

```go
type gitSweep struct {
    gen         uint64          // drops answers from an abandoned pass
    outstanding int             // decided up front, only ever decremented
    results     []gitSyncResult
}
```

Waiting for all of them rather than applying each arrival keeps one sweep to one
broadcast. Applying incrementally would be *correct* — the change check would
still suppress no-op rows — but at startup, when every workspace changes at
once, it would send one full rollup per workspace to every window.

`gen` is the guard against a late reply from a pass that was abandoned: the two
describe different sets of workspaces, and folding a stale answer in would
decrement the wrong counter and end the new sweep early.

### The failure modes came free

Remote requests ride the existing `pendingReqs` machinery — the same one read,
capture and the worktree commands use — so:

- a host that **drops** mid-sweep is failed through `flushPendingFor`
- a daemon that **never answers** is failed by the registered timer
  (`gitSyncTimeout` 60s, chosen to clear gitsync's own 30s network budget so
  catway never fails a request the daemon is still going to answer, and to stay
  comfortably under the 2-minute interval so a lost reply cannot stall the next
  pass)

Either way the sweep gets its answer — an Unknown one — and cannot be left
waiting on a reply that is not coming. `gitSyncResponder` folds both into the
zero `Status` rather than surfacing them: there is no user waiting on a
background poll.

A sweep already in flight is left to finish rather than stacked on. With an
unreachable remote one pass can outlast the interval, and starting a second
would double the load on exactly the host that is already not answering.

### Which workspaces are asked about

- **Directory** is `ws.IdentityCwd` — the same field the row's *name* is derived
  from, so the dot and the label always describe the same checkout. No fallback
  to the daemon's own cwd, which would paint every identity-less row with the
  state of wherever catway happened to be started.
- **Asleep and locked are included.** Neither says anything about the
  repository, and a workspace put to bed is exactly the one most likely to have
  gone stale while nobody was looking.
- **Nobody able to answer means left out**: no start directory, a host that is
  down, or one too old for the capability. Left out means the dot goes back to
  uncoloured, which is the honest rendering of "we cannot currently find out" —
  the alternative is a colour that stops tracking reality the moment a link
  drops.

A workspace that *stops* having an answer drops its colour rather than keeping
the last one: a stale "in sync" is the single reading that would actively
mislead, since it's the one that says "go ahead".

### A host coming back asks at once

Every sweep a host was down for skipped its workspaces. Without a nudge on the
completed handshake, a machine reconnecting would leave its rows blank for up to
two minutes for no visible reason — so `startWorkspaceGitSweep` is called from
the same post that turns the roster's dot green. Free when there is nothing to
do: a sweep in flight is left alone, and a session with no workspace on that
host plans nothing.

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

The browser cannot tell a local row from a remote one, and should not: `w2` on
devbox carries a state exactly like `w1` here. The only difference is which
machine produced it.

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
  no test forks git. Planning: no directory, local fallback, remote routed to
  its host with the path untouched, a host that cannot answer skipped without
  taking the local workspaces with it, the local host preferred once it can
  answer. The round trip against a pipe daemon, including a host that drops
  mid-sweep (the sweep must clear, and the stale colour must go). Collection:
  waiting for every answer, ignoring a stale generation, not stacking sweeps.
  Caching, pruning, session-order rollup, and the reconnect nudge.
- `internal/orchestration/handshake_test.go` — a real handshake and round trip:
  the capability is advertised, the request dispatches, the id echoes, the
  status decodes. It asserts the *Unknown* answer deliberately — a case needing
  a reachable forge would be a test that fails on an aeroplane, and the state
  machine is already pinned against real remotes in `internal/gitsync`.
- `cmd/catway/web/jstest/wsdot.test.mjs` — both channels pinned independently,
  every focus × sync combination, including the focused-row-keeps-its-colour
  case the CSS specificity note exists for.

## Verification

`make fmt-check vet build test jstest` clean; `-race` clean on `cmd/catway`,
`internal/orchestration`, `internal/gitsync`, `wire`. Smoke-resolved this repo
and `cats-todo` live: both `synced`, ~600ms each; `/tmp` correctly `unknown` in
32ms.

Two `internal/inputenc` failures in `make test-ghostty` are pre-existing
(missing generated Dart golden, wants `FLUTTER_ROOT`) and were left alone.

## Open

- **Both machines need the new build.** The capability is advertised by the
  cathost, so a remote workspace stays uncoloured until *that* box is running a
  build with `git_sync` in its feature list. Nothing breaks in the meantime —
  the workspace is simply not asked about — but it is the thing to check first
  if a devbox row stays blank.
- The MacApp bundle needs reinstalling to pick this up; `make binaries` built
  into `bin/`, but the installed app is still the old build.
- A workspace's dot still reports the *trunk*, never the branch the workspace is
  actually on. That is deliberate — the pane header already says which branch a
  directory is on, and the dot is about the shared mainline — but it means a
  long-lived feature branch shows its project's staleness, not its own.
