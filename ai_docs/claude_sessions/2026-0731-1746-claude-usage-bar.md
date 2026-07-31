# Session: Claude Usage Bars in the Sidebar

- **Session ID:** `3ea1a1de-7018-49c8-a7bc-4e8cc4775050`
- **Date:** 2026-07-31
- **Branch:** main
- **Repo:** `cats` (the session was *launched* from `cats-todo` — see "False start")

## Request

> add a claude usage bar under a new section in the aside called USAGE. Show 5
> hr usage and weekly usage. Only update every 2 minutes

Followed mid-turn by:

> Put the new section at the top of the left aside sections

## False start

The session opened in `cats-todo`, which has no `<aside>` — it is a Bubble Tea
TUI. Exploration went looking for one, started scanning sibling projects, and
was (correctly) interrupted: *"This is the wrong repo. Please abort."* Nothing
had been written. The target was `../cats`, where `cmd/catway/web/index.html`
holds the browser front end and its sidebar.

Worth remembering: `cats-todo`'s prose talks about "cats's sidebar" in a couple
of comments, which is what made the wrong repo look briefly plausible.

## The one scoping question

Where the numbers come from decided the whole implementation, and the codebase
could not answer it. There is **no `claude usage` CLI command** — the
percentages `/usage` shows come from an authenticated endpoint. Three options
were offered; the answer was:

> Do 1) with a fallback of 2), and in the future make the poll configurable

So: the account endpoint as the source of truth, the local transcript estimate
as the fallback, and the interval left as a named constant for now.

## What shipped

### 1. The wire message (`internal/browserproto/`)

New `MsgUsage` / `Usage` / `UsageWindow`, registered in `DecodeDown`.

```go
type Usage struct {
	T        Type        `json:"t"`
	Source   string      `json:"source"` // "account" | "local"
	FiveHour UsageWindow `json:"five_hour"`
	Weekly   UsageWindow `json:"weekly"`
	Err      string      `json:"err,omitempty"`
}

type UsageWindow struct {
	Pct      float64 `json:"pct"` // 0–100, or UsagePctUnknown (-1)
	ResetsAt string  `json:"resets_at,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}
```

`Pct == -1` is the load-bearing part: it means *no denominator exists*, which is
the fallback's actual epistemic state. The front end drops the meter entirely
rather than drawing it at 0%, because an empty bar reads as "0% used" — a
number we do not have.

### 2. The reader (`cmd/catway/usage.go`, new)

**Account path.** `GET https://api.anthropic.com/api/oauth/usage` with three
headers, all required:

| Header | Why |
|---|---|
| `Authorization: Bearer <token>` | the OAuth credential |
| `anthropic-beta: oauth-2025-04-20` | gates the OAuth-credentialed endpoints |
| `User-Agent: claude-code/<version>` | **not cosmetic** — without it the request is served from a far tighter rate-limit bucket and starts failing |

Response carries `five_hour`, `seven_day`, `seven_day_opus`, `seven_day_sonnet`,
`extra_usage`; each window is `{utilization, resets_at}`. We surface the two
that apply to every plan.

**Credential resolution — the subtle bug.** The first live attempt failed with
"no claude credential" despite the keychain item existing. Cause: **two macOS
keychain items share the service name `Claude Code-credentials`**.

| account | contents |
|---|---|
| `unknown` | `mcpOAuth` — tokens claude's MCP plugins collected |
| *(the macOS username)* | `claudeAiOauth` — the actual login credential |

`security find-generic-password -s …` returns whichever it likes, and it handed
back the plugin item. That item is perfectly valid JSON, so "did it parse?" is
not a sufficient test — **selection has to be by shape**. `firstClaudeToken`
now walks every store (credentials file → keychain by account → keychain by
service alone) and takes the first that actually yields a `claudeAiOauth`
token. An expired credential is remembered rather than skipped, so
"claude login expired" survives as the reported reason instead of collapsing
into "no credential".

**Credential handling.** Read into the process, placed in one header. Never
logged, never broadcast, never sent to the browser — the browser only ever
receives the two numbers. Error strings are deliberately scrubbed of both the
token and the endpoint's response body, since they are painted into the
sidebar. **Nothing refreshes the token**: the refresh token is claude's to
spend, and rotating it underneath a running claude to draw a progress bar is
not a trade worth making. An expired login degrades to the estimate.

**Fallback path — `usageEstimator`.** Sums token spend from the same
`~/.claude/projects/**/*.jsonl` files `agentmodel.go` already reads. The design
constraint was cost: a week of transcripts here is ~50 MB across ~105 files, and
re-reading that every 2 minutes for two numbers is absurd. So:

- per-file **offsets** — each sweep reads only appended bytes;
- per-minute **buckets** — both windows are a suffix sum, and the structure
  stays sized to one week of activity, not to the archive;
- **dedup by assistant message id** — a resumed/forked session starts a new
  transcript carrying the earlier turns, and counting those again would inflate
  both windows by however many times the user has resumed;
- a 16 MiB cap on the *first* read of a large transcript, flagged in the UI as
  `+` so a knowingly-short total never reads as authoritative.

It is only invoked when the account read fails, so the normal path pays none of
this.

### 3. Wiring

- `orch.usage` caches the last reading, so a browser connecting mid-interval
  gets current numbers instead of a blank section for up to two minutes.
  `setUsage` dedupes, broadcasting only on an actual move.
- `go o.runUsage()` in `main.go`, alongside `runAgentModels`. All I/O happens on
  that goroutine; only the finished message is `post`ed back onto the loop.
- First poll is immediate — a section silent for two minutes after launch reads
  as broken, not as pending.

### 4. Front end (`cmd/catway/web/index.html`)

`<section><h2>Usage</h2>…</section>` placed **first** in the aside, above
Workspaces. Each row is a label line (name · "resets in 3h" · value) over a
4px meter. Colour ladder: accent → `--warn` at 75% → `--err` at 90%. A 30s
tick rewrites just the countdown labels, mirroring how the agent-age labels
already work (absolute instant on the element, label re-derived locally).

## Bug found and fixed during the work

`foldFile` discarded the first line after seeking — correct for the capped
first read, which lands mid-record, but **wrong for a resumed read**, where the
recorded offset is always a record boundary. It silently dropped the first
appended record of every subsequent sweep. Caught by
`TestUsageEstimatorReadsOnlyAppendedBytes`, which is exactly why that test
appends and re-sweeps rather than just checking a single pass.

## Verification

- Live end-to-end against the real endpoint on this machine: **5h = 11%,
  week = 19%**, with reset timestamps. (Done via a temporary test that printed
  only the resulting numbers — never the credential — then deleted.)
- `resets_at` comes back as `…T23:10:00.655263+00:00`; confirmed `Date.parse`
  handles that offset form.
- New tests: header correctness, error paths asserting **no credential leak**,
  zeroed-windows-are-real, credential store selection incl. the MCP-item trap,
  estimator windows / dedup / incremental read / pruning / noise records,
  `setUsage` dedup, and two `browserproto` round-trips (account + local shapes).
- `gofmt`, `go vet`, full suite, and `-race` all clean. JS syntax checked with
  `node --check` on the extracted script block.

## Deferred (deliberately)

`usageInterval` stays a named constant. The ask was "in the future make the
poll configurable" — promoting it to a `config.Usage` knob is a one-line change
when wanted.

## Notes for next time

- The endpoint is undocumented. It degrades into the fallback rather than
  breaking the sidebar if its shape changes, which is why the reply is parsed
  leniently and a 200 with *no* windows is treated as a shape change (while a
  200 with *zeroed* windows is treated as a real, spent-nothing account).
- Don't reach for the keychain by service name alone anywhere else in this
  codebase — see the two-item trap above.
