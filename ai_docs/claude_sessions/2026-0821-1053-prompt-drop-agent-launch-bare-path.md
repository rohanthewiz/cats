# Session: a drop that opened a shell — launchd's bare PATH under the daemons

- Session id: `61704bb0-3519-487b-b24f-f22a8b3b3566`
- Date: 2026-08-21
- Branch: `main` (both repos)
- Commits: cats `3e05e54`, cats-todo `48f7fb8` — both pushed
- Predecessor: `2026-0819-1757-a-connection-is-a-view.md`

One report: **"prompt drops from cats-todo seem to not successfully launch the
claude agent."** It was not a cats-todo bug in the sense the symptom suggests —
the drop machinery was doing everything right. The agent binary was simply
unreachable from the process that had to exec it.

## The mechanism

A new-session drop is one round trip: `tab.create {cwd, title, command:
["claude"]}`. cathost turns that into `exec.Command("claude")` and starts a PTY
on it (`internal/orchestration/host.go:createPane`). The failure is entirely in
where that name gets resolved:

```
catapp (launched from the Dock → launchd)   PATH=/usr/bin:/bin:/usr/sbin:/sbin
  └─ cathost   ← resolves every pane's program with THIS PATH
       ├─ /bin/zsh   fine — an interactive shell rebuilds PATH from the rc files
       └─ claude     not found: it lives in ~/.local/bin
```

Go resolves a bare program name with the **spawning process's** PATH, never
with the `cmd.Env` prepared for the child — so no amount of environment handed
to the pane could have helped. `pty.StartWithSize` failed, `createPane` took
its "command %q: … — falling back to shell" branch, and the tab came up as a
plain zsh.

Everything downstream then behaves exactly as designed and makes it worse:
`waitForAgentReady` probes twelve seconds for a Claude banner that will never
draw, times out, pastes anyway (deliberately — the timeout case is where we
know least), and in run mode presses Enter. The prompt is typed at a shell
prompt and **executed by the shell**. From the outside that reads as "the agent
launched and ignored me".

### Evidence gathered, in order

- `ps ax` → catapp 15311, cathost 15526, catway 15527, all from
  `/Applications/Cats.app`.
- `ps eww -p 15526` → `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, alongside
  `__CFBundleIdentifier=dev.cats.app` and nothing else of the user's. The whole
  env is launchd's, inherited verbatim from catapp.
- `env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin which claude` → nothing.
  `which claude` in a pane → `~/.local/bin/claude`.
- `go version -m` on the installed catapp → `vcs.revision=2f7ce1f`, clean, i.e.
  the running app *does* contain `hydratePATH`.

That last point is the interesting one. `cmd/catapp/shellenv.go` exists to
prevent precisely this: on a GUI launch it asks the login shell for its PATH
and adopts it before spawning any daemon. It is called at the top of `runLocal`,
before `startBackend`, and the installed binary has it. Yet the daemons hold
the raw launchd env, which is what a **failed probe** looks like (the function
leaves PATH untouched on any failure).

Attempts to reproduce the probe failure all succeeded instead: `zsh -ilc` with
a bare env, with `env -i`, from cwd `/`, with the launchd variables — ~1s every
time, marker-fenced value intact. `go test ./cmd/catapp -run TestHydratePATH`
passes. So the startup hydration is right but not *reliable* — it failed once,
at 17:25 on Aug 20, for a reason not reproducible after the fact (a slow cold
rc chain against the 5s timeout is the best guess; the 9s gap between catapp's
and cathost's start times fits).

**That is the design conclusion this session turns on:** a single best-effort
probe at app startup is the wrong place for the *only* copy of this logic. When
it fails there is no second chance, and the failure is silent and total for
every command pane the session will ever open. The resolution belongs where the
exec happens.

## Fix, layer 1 — cats (`3e05e54`)

New `internal/shellenv`, cross-platform (no darwin tag — cathost also runs on
Linux hosts). The probe moves here out of catapp:

- `LoginPATH()` — `$SHELL -ilc printf '<marker>%s<marker>' "$PATH"`, 5s
  timeout, marker-fenced against rc-file banners, memoized with
  `sync.OnceValue` so a full interactive shell startup is paid once per process
  and a failed probe is not retried per pane.
- `Merge(shellPath, inherited)` — login entries first, inherited-only entries
  (a managed Mac can inject one at the launchd level) appended.
- `Lookup(name) (path, envPATH)` — the new part, and the two returns fix two
  different halves of the same failure:
  - `path` is what gets exec'd, always absolute when resolvable. This takes the
    daemon's PATH out of the question entirely.
  - `envPATH` is what the child then works with. An agent shells out constantly
    (git, gh, node, a toolchain); launching it with a PATH that could not find
    the agent itself only moves the failure one level down.
  - `lookIn` is `exec.LookPath` against an explicit list — os/exec has no such
    entry point, so PATH is swapped in under a mutex and restored
    unconditionally.

`createPane` uses it for command panes only (a shell pane sources the rc files
itself and needs none of this). The pane's env gets the hydrated PATH unless
the caller pinned `PATH` explicitly — `c.Env` is copied rather than mutated,
since it belongs to the caller's message. `os/exec` dedups env keeping the last
occurrence, so appending is enough to override.

`cmd/catapp/shellenv.go` keeps `hydratePATH` and its `__CFBundleIdentifier`
gate — a terminal launch must not have its PATH re-derived and reordered — and
delegates the probe. Startup hydration stays the first line of defence (get it
right for every child at once); cathost's per-command lookup is the backstop
for the launches it misses: a probe that failed or timed out, or a cathost
started by something that was never a shell.

Tests moved with the code (`internal/shellenv/shellenv_test.go`), plus the one
that states the daemon-side contract directly: a program in a temp directory
the rc files know nothing about, a bare process PATH that provably cannot see
it, and `lookIn` finding it anyway — and leaving PATH exactly as it was.

## Fix, layer 2 — cats-todo (`48f7fb8`)

`resolveAgentPath` in `drop.go`: `argv[0]` is resolved with `exec.LookPath`
before the `tab.create`. The manager runs *inside a pane*, so its PATH is the
user's — it is in a strictly better position to resolve the agent than the
daemon is.

This is not redundancy for its own sake. It is what makes drops work against
**the cats builds already installed**, with no app rebuild and no restart —
which mattered here, because rebuilding Cats.app would have killed the session
doing the work. Best effort in both directions: a name that is already a path,
or one this process cannot resolve either, goes over unchanged (the daemon may
have a PATH we do not, and its error message is the better one).

## Delivery

- Both commits pushed to `main`.
- `catctl plugin update rohanthewiz.cats-todo` run: the installed plugin is a
  GitHub clone at `~/.config/cats/plugins/rohanthewiz.cats-todo`, so the update
  re-fetched and rebuilt `bin/cats-todo` with the fix. An already-open manager
  pane still runs the old binary — relaunch to pick it up.
- The cats half needs `make macapp` and an app restart, left to the user for
  the reason above.

## Worth remembering

- **`exec.Command` resolves against the parent's PATH, not `cmd.Env`.** Any
  daemon that execs user-named programs has to resolve them itself; preparing a
  good environment for the child is necessary but not sufficient.
- **A GUI-launched macOS app has no user PATH**, and every descendant inherits
  that. Shell panes hide it, which is why the gap only ever shows up for panes
  exec'd straight into a program: agent launches, plugin actions, prompt drops.
- **The shell fallback in `createPane` is a good idea with a bad blast radius
  when the caller is automation.** A human gets a usable shell where they
  wanted an agent; a drop gets its prompt executed by that shell. The fallback
  stays (a dead pane is worse), but the resolution in front of it is now strong
  enough that it should not be reached for a PATH reason.
- When the visible symptom is "the agent ignored my prompt", check what the
  pane's process actually *is* before looking at anything on the prompt-delivery
  path.
