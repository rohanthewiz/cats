# Session: roman — bytdb, a TUI, and a cats `http_client` plugin

Session ID: b279ef3e-4cd1-4679-9cfd-b9ccff810d8a
Date: 2026-09-22
Driven from: cats (work mostly in `~/projs/go/roman`)

## 1. The ask

"Add roman (`~/projs/go/roman`) as an `http_client` plugin to cats." First do
a once-over of roman, and while at it:
- move its DB to `github.com/rohanthewiz/bytdb`;
- add any needed tests;
- `/next-list seed` on that repo;
- update to the latest deps, especially the rohanthewiz ones.

Mid-session: "When running roman within cats I would like a TUI", using "the
same as cats-todo" (bubbletea v2 / bubbles v2 / lipgloss v2).

## 2. What roman was

It was a small rweb + element web app (about 1.2k lines of Go plus embedded
JS/CSS) for batch API calls, with `{{field}}` placeholders fanned out "in
tandem" over comma lists. Storage was DuckDB (cgo), one `data/<name>.db` per
project, in a directory relative to the cwd.

**Once-over findings (all fixed):**
- The global `config` was mutated by `/config` while `/batch` goroutines read
  it, and `database.DB` was swapped on project open with no synchronisation.
  Both were data races.
- `writeJSONError` never set the HTTP status, so every error returned 200.
- `CreateTab` re-selected `ORDER BY created_at DESC LIMIT 1`, which could
  return another request's tab.
- `/project/open` opened any path it was given.
- The data dir was cwd-relative. A cats plugin pane starts in the user's
  project, so every pane would have shown a different, empty project set.
- Each call built a fresh `http.Client`, so no connections were reused, and
  the whole response body was read before truncating it.

The user's uncommitted `Textarea → TextArea` edit (the element rename) was
kept and shipped, because element v0.7.0 needs it.

## 3. Design

```
 browser ──HTTP──▶ web ──────────┐
                                 ├─▶ resources.Manager ──▶ database.Store (bytdb)
 TUI (owner) ─── Backend ────────┘            ▲
 TUI (guest) ─── client.Remote ──HTTP─────────┘
          both ──▶ batch.Runner ──▶ target API
```

- **bytdb via `database/sql`** (`stdlib` driver, Postgres dialect). Each
  project is `~/.roman/<name>.bytdb` (`-d`, `$ROMAN_DIR`). The new extension
  means old DuckDB files are never handed to bytdb. `Store` is a value, not a
  global. Save As is a row copy (`create`), which is also what `Import` uses.
- **`resources.Manager`** holds the open project behind an RWMutex. Tab ops
  take the read lock and switching takes the write lock. A switch opens the
  new file before closing the old one, so a failed open leaves the current
  project usable. Only bare `*.bytdb` names in the data dir are accepted
  (`BadProjectError` → 400).
- **`batch`** is the engine, taken out of `web`:
  - an explicit `Request` instead of global state;
  - a bounded worker pool and one shared `http.Client`;
  - context cancel and an `onResult` progress hook;
  - reading a limited preview of each body, then draining the rest.
- **One owner per data dir (`instance`).** bytdb's btypedb holds a file lock,
  so two processes can't share a project. The first roman takes an flock on
  `~/.roman/roman.lock`, serves HTTP, and writes its URL into the file. Later
  romans are guests over the existing HTTP API (`client.Remote`). flock is
  released by the kernel on any exit, so no stale PID-file problem.
  - `roman serve` on a held dir prints the owner's URL, opens it on `-open`,
    and exits 0.
  - `roman tui` as owner also serves HTTP. It falls back to port 0 if 8099 is
    taken, is non-verbose, and logs to `~/.roman/roman.log`.
- **TUI (`tui`, bubbletea v2):**
  - Layout: tab list, request form, results viewport.
  - Fields are edited as `name = v1, v2` lines, converted losslessly to and
    from the stored JSON.
  - Storage calls run synchronously in Update, which keeps save-then-switch
    ordering deterministic. Batches run async, with a 100ms tick and `ctrl+x`
    cancel.
  - Autosave happens on tab switch, run and quit. A run "touches" the tab
    (moves it to the front); autosave doesn't.
  - Pane title `roman: <project>` (+ `(done/total)`) is the cats PLUGINS row's
    channel. The palette is cats-todo's.
- **Wrong-project guard.** Tab ids are per file, and anyone can switch the
  owner's project. An autosave would then overwrite the same-id tab in the
  other project. `Model.checkProject` refuses the write and reloads. The test
  was confirmed to fail with the guard disabled. The web page has the same
  hazard (roman N-002).
- **Unreadable stored fields** are shown raw, and saving is refused until
  they're rewritten (`badFields`). Without that, `textToFields` would have
  "parsed" the JSON into a single bogus field and saved it.

## 4. What changed, by repo

**roman** (`master`, pushed):
- `1c968c6` roman: bytdb storage, a TUI, and a cats http_client plugin.
  - New packages `batch`, `client`, `instance`, `tui`, and `cmd/duckdb2bytdb`
    (its own module, `replace roman => ../..`, so roman itself has no cgo).
  - `database` and `resources` rewritten. `web/server.go` is a `Server` with
    `Start(addr, verbose)` → (listen addr, done, err) via rweb's `ReadyChan`.
  - `main.go`: `roman [serve] | tui | version`, with flags `-d`, `-port`,
    `-open`.
  - Deps: element v0.7.0, rweb v0.1.30, serr v1.4.0, logger v1.3.0,
    bytdb v0.15.0, charm v2 (as cats-todo); Go 1.26.1. DuckDB is gone from
    `go.mod`/`go.sum`.
  - `cats-plugin.toml`: `type = "http_client"`, `bin = ["./bin/roman"]`, and
    two actions:
    - `tui` (default, no `-d` on purpose);
    - `web` (`serve -open`).
    Its build stamps `-X main.version=0.1.0`.
  - README rewritten (TUI keys, running in cats, data/owner model,
    migration, architecture). `static/script.js` manual-open hint now says
    `.bytdb`. `bin/` is ignored.
  - `ai_docs/todo/next-list.md` seeded (see §6).
- **Tag `v0.1.0`** (annotated, on `1c968c6`), pushed.
- A follow-up commit closes roman N-004 (publish).

**cats** — `06a67c3` plugins: http_client type (roman), pushed.
- `wire.PluginTypeHTTPClient` and the type table.
- The sidebar's `pluginTypeLabel` drops `_client` as well as `_mgr`, so the
  row reads `http` (jstest +2 assertions, agentlist 25 → 27).
- `docs/subsystems/plugins.md` gets the type in its tables, template and
  label note.
- The valid-type test lists in `wire` and `internal/plugin` include the new
  type.

**Local state, not in git:**
- `catctl plugin link ~/projs/go/roman` makes roman a linked plugin:
  `~/.cats/bin/roman` points through `~/.config/cats/plugins/rohanthewiz.roman`
  to the checkout.
- The user's three DuckDB projects were converted into `~/.roman`: `dynamic`
  (2 tabs), `hardwire` (2), `new_project` (1).
  - Verified by metadata only (names, methods, URL lengths, field counts,
    auth flags, dates matched), without printing any values.
  - The originals' shasums are unchanged.

## 5. Things learned

- **bytdb ≠ btypedb.** bytdb is the Postgres-dialect SQL engine; btypedb is
  the typed KV store it sits on. The user's rule names bytdb, and the loaded
  btypedb skill didn't apply.
- **Reusing an engine per path.** bytdb's stdlib driver shares one engine per
  absolute path inside a process, but btypedb locks the file across
  processes. That lock is why roman needs a single owner.
- **rweb v0.1.30:**
  - `Run` blocks until SIGINT/SIGTERM and installs its own `signal.Notify`.
    There's no `Shutdown`.
  - `ReadyChan` + `GetListenAddr` give the bound address (port 0 works).
  - `SetStatus` sets the response code.
- **serr v1.4.0** has no key-lookup helper, so a typed error plus
  `errors.AsType` was cleaner for 400 vs 500.
- **logger is logrus underneath.** `logrus.SetOutput(file)` keeps handler
  logs off a TUI's screen.
- **`script -q` gives a 0×0 pty**, so bubbletea renders nothing. Use
  `sh -c "stty rows 30 cols 110; exec …"` to see the screen.
- **DuckDB-era files differ by age.** The user's files predate the
  `request_body`/`bearer_token` columns, so the converter probes
  `information_schema.columns` first.
- **Sandboxed plugin link.** `CATS_PLUGINS_DIR` + `CATS_BIN_DIR` validate a
  manifest with `catctl plugin link` without touching the live host.

## 6. Verification

- roman:
  - `go vet ./...` and `go test -race ./...` pass across `batch`, `client`
    (a real server end to end), `database`, `instance`, `resources` and `tui`
    (keys driven through `Update`).
  - Real-process runs: owner `serve` + a second `serve` (it prints the
    owner's URL); a path-traversal open returns 400; SIGINT clears the lock
    URL.
  - TUI as owner and as guest in a sized pty: the header shows `guest →
    127.0.0.1:…`, and the title is set and cleared.
- cats: `make vet`, `fmt-check`, `test` and `jstest` pass.
- Sandbox and then live `catctl plugin link`/`list` show
  `rohanthewiz.roman v0.1.0 [http_client]` with actions `tui` and `web`.
- **Not seen running:** the PLUGINS row in a live Cats.app (TCC; needs a
  rebuild). This is folded into N-001.
- **Not run:** `catctl plugin install rohanthewiz/roman --ref v0.1.0` (roman
  is linked locally instead).

## Next

Closed: None. Declined: None. Raised: None.
Deferred: None. Promoted: None.
Updated: N-001 (roman's PLUGINS row added to the Cats.app hands-on pass).
Full list: `ai_docs/todo/next-list.md`.

roman's own follow-ups are in `~/projs/go/roman/ai_docs/todo/next-list.md`,
seeded this session (N-001…N-011; N-004 closed). The medium items are:
- N-001: the page ignores non-2xx responses;
- N-002: page writes can land in another project after a switch elsewhere;
- N-003: the live Cats.app row.
