# Session: the store seam, and the login bug it surfaced

- Session id: `da6184ae-d529-4dbb-8620-149027b94df5`
- Date: 2026-08-14
- Branch: `main` (cats); gonotes on `master`
- Subject repo: `~/projs/go/gonotes` — commit `3ebebe4`, **pushed**
- cats: `4ddd73e` (plan record), **pushed**
- Plan/record updated: `ai_docs/cats-gonotes-intg.md` (Phase 4 marked done)
- Predecessor: `2026-0814-1911-conformance-revamp-and-a-bubbles-bug.md`

## What this session was

Phase 4 of the gonotes plan: hybrid data access. Three new source files
(`tui/store.go`, `store_local.go`, `store_http.go`), two new test files, and
the eight existing `tui/` files rewired to reach data through an interface
instead of `models.*`.

103 + 103 + 736 lines of new source, 1,247 lines of tests, 321 insertions /
123 deletions across the existing files. `go build`, `go vet`,
`go test -race ./...` green; 130 test results in `tui/`.

Landed as specified. What follows is what the spec left open.

## The interface shape was chosen to keep the default path boring

18 methods, and every one mirrors a models function the TUI already called,
with the arguments it already had. That is the whole design rule:
`store_local.go` is a file of one-line pass-throughs with nothing to get
wrong. The seam only pays off if the *default* path stays trivially correct;
anything clever in that file would be a behavior change smuggled in under a
refactor.

Where a models function had a parameter the TUI always passed the same value
for — `ListNotes`' limit/offset — the interface drops it and the local wrapper
supplies it, rather than pushing `0, 0` onto every caller.

`userGUID` is dead weight in `store_http.go`: the server scopes every query
from the JWT, so eleven methods take the argument and ignore it (`_ string`).
It stays anyway. Two implementations with different signatures is not a seam.

### Two methods have no models analog

- `ResumeSession() (*models.User, error)` — the store's chance to get past the
  login screen without a password. Local always declines: with direct database
  access there is no session to resume, only a password to check.
- `ListUsernames` returning the new `ErrNoUserList` sentinel.

The sentinel matters more than it looks. The HTTP store returns it *always*,
because "list every account" is an endpoint a shared sync hub must not have.
But the login screen reads an **empty** list as "fresh install" and flips into
registration mode — so "unknown" and "none" must be distinguishable, or a
returning user gets an account-creation form against a server that already
holds their notes. `ErrNoUserList` is checked before the generic error branch
and falls back to prefilling from `GONOTES_USER`.

## The 401 policy, and why the password lives in memory

A JWT expires; a TUI can sit open for days. `request()` catches a 401, attempts
**one** silent re-login with the credentials from the last successful login
(falling back to the environment), and retries once. Never a loop — a wrong
password would otherwise be re-sent forever.

On re-login failure the **original 401** propagates, not the re-login error:
"unauthorized" is the accurate description of what happened to the user's
action, and reporting a login attempt they did not make is confusing.

Holding the password in memory is a real cost, accepted because the
alternatives are worse: a TUI that dies on token expiry mid-edit, or one that
prompts again for a password already sitting in a textinput belonging to the
same process. `TestExpiredTokenTriggersOneSilentRelogin` asserts the login
*count*, not just the recovery — the count is what catches a retry loop.

## The probe requires the envelope, not a 200

Port 8444 can be held by anything, and guessing wrong starts the TUI in HTTP
mode against a stranger, where every screen shows a decode error. `ProbeServer`
demands `success: true` **and** `data.status == "ok"`.

The test table lists five services that would pass a laxer check:

| response | accepted |
|---|---|
| the real gonotes health envelope | yes |
| something answering `OK` with 200 | no |
| a JSON API returning `{"status":"healthy"}` | no |
| the envelope, but `success: false` | no |
| the envelope with `status: "starting"` | no |

Also capped: the body read goes through an `io.LimitReader`, so a confused
endpoint cannot stall startup by streaming forever.

## A login bug the seam surfaced

`models.AuthenticateUser` reports bad credentials as `(nil, nil)` — the web
API handler checks `user == nil` separately, which is how it gets away with
it. The old `loginCmd` only checked `err`, so a password typo left `busy` set
and the screen sat on "Signing in..." ignoring every subsequent key.

Nothing about Phase 4 required finding this. It surfaced because writing
`fakeStore.AuthenticateUser` meant deciding what the real contract was, and
the contract turned out to be one the caller wasn't honoring. Fixed in
`loginCmd`, pinned by `TestBadPasswordClearsTheBusyFlag`.

## Tests: the fake is the point

`fake_store_test.go` is an in-memory `Store`. Before this, every teatest flow
needed `models.InitTestDB`: a temp dir, two bytdb files, a bcrypt user, a
`CloseDB` cleanup — per test, and serialized, since the models layer keeps its
databases in package globals.

`storeFixtures()` reruns the boot flows against **both** the local store and
the fake. The local run proves the pass-through wrappers really reach bytdb;
the fake run proves the screens depend on the interface and nothing else — a
screen that reaches around the Store back into `models.*` fails there, because
no database is open at all.

The fake also carries a `failWith` hook, which is the only sane way to produce
a storage failure on demand. `TestLoginPrefillsFromEnvWhenUsernamesAreUnavailable`
uses it to make the fake decline exactly as `httpStore` does.

`store_http_test.go` runs the real `httpStore` against a real
`httptest.Server` — backed by a `fakeStore`, so round trips are genuine: a
note created over HTTP is one the next GET returns. That is what lets
`syncNoteCategories` (the one piece of real logic in `commands.go`) be
exercised across the wire, over six endpoints. The fake API reproduces the
note-categories endpoint's *detail* shape rather than `CategoryOutput`, which
is the only way `categoryFromDetail` gets tested.

Two mapping assertions worth keeping: an absent body must become an
**invalid** `NullString`, not an empty-but-valid one (the detail screen keys
off `.Valid`), and the token file must be 0600 with its parent created —
`~/.gonotes` may not exist on a machine that has only ever used HTTP mode.

## Verification past the suite

Same pty harness as Phases 2–3 (`pty.fork` + `TIOCSWINSZ`, answering OSC 11).
A scratch server on port 18444 over a scratch data dir, `HOME` redirected so
the token cache could not touch the real one.

Three runs on the **same** data directory with the **same** command, differing
only in what was available:

| run | mode | login screen | API calls the server logged |
|---|---|---|---|
| server up, env credentials | HTTP | flashes at byte 423, gone by 1760 | `health`, `auth/login`, `notes` |
| server up, cached token only | HTTP | **never renders** (count 0) | `health`, `auth/me`, `notes` |
| server stopped | local | prefilled username, password typed | — (probe refused) |

The cached-token run is the cleanest result: `/auth/me` answers faster than the
first paint, so browse *is* the first frame. A login round trip does not, which
is why the env-credential run shows the login card briefly. Both are correct —
worth knowing before someone reports the flash as a bug.

The server-side trace is the real proof: in HTTP mode the TUI process made no
database call at all, while the server held the lock.

### The decryption risk item is resolved, and was framed on the old architecture

A private note seeded with the body `CLEARTEXT-MARKER-9931` renders that marker
in the HTTP-mode preview pane. Per-note AES died with the bytdb merge —
`Note.EncryptionIV` survives for transport compatibility but is no longer
persisted — and bytdb encrypts the *whole private database* at rest, so a
server that opened it with the key reads plaintext and every handler
serializes plaintext. No warning banner needed.

### A trap that cost a scratch-server restart

`gonotes serve` is **not** a subcommand. Serving is the default action, so
`gonotes serve -d <dir>` treats `serve` as a positional argument, silently
ignores `-d`, and opens the real `~/.gonotes`. It got as far as initializing
the schema and closing cleanly before failing on a too-short JWT secret; no
data was touched, but the correct invocation is `gonotes -d <dir> -p <port>`
and `GONOTES_JWT_SECRET` must be at least 32 characters. Recorded in the plan.

## Where things stand

Both repos pushed. Phase 5 is next: the `cats/` transport package hand-copied
from `../dbc/cats/` (`detect`, `client` + the `capture` verb, `hooks`,
`events`), `tui/cats_glue.go`, and the `Run` wiring — catsInit before
`NewProgram`, `Release()` after `p.Run()` returns even when the TUI fails to
start. ~1,900 lines, mostly copied tests.

Still deliberately not done, carried from Phase 2: `~/bin/gonotes` and the
MacApp bundle are on the pre-Phase-2 build. Swapping the binary is a live
service change (it carries the server as well as the TUI), and a stale bundle
silently serves the wrong thing. Phase 4 raises the stakes slightly in the
good direction — with a current binary installed, a cats-hosted TUI would now
coexist with the running server instead of failing on the lock.
