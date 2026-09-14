# Fable was stuck, not working, and the running cathost can't be recovered without ending its panes

*A continuation of the write-deadlock session. An "is fable still running?"
script found fable parked on a full pty. Checking the recovery plan against
the build actually running then showed that restarting only catway cannot work:
the installed cathost has the reader hang that fix 1 only just fixed.*

Session: https://claude.ai/code/session_012kHDH7fWkxr85jvxARw49y
Date: 2026-09-13
Repo: `~/projs/go/cats` (branch `main`)
Previous doc (same session): `2026-0913-2313-catway-cathost-write-deadlock.md`
Commits: none (this doc only). The fixes it builds on are `0236300` and `67c784c`.

## Request

> Give me a cmd or script to check if fable is still running

then:

> yes, do option 2

## Fable: alive, but stuck

The first version of the script reported "WORKING — mid-task", which was
wrong. Its last assistant entry was `tool_use`, but the transcript had not
changed in 28 minutes, CPU was 0% and the only child was `gopls`.

The transcript tail explained it. The last entries are the `tool_result` of a
successful `git push` (`73a8f70..fb75a4b` to grmob `master`, 22:51:40),
followed by nothing. Fable had finished its work and was printing its final
reply.

Two independent checks showed it blocked:

| check | result |
| --- | --- |
| `sample 95000`, thread by thread | main thread (`DispatchQueue_1: com.apple.main-thread`) in `__write_nocancel` for every sample, twice, minutes apart |
| `TIOCOUTQ` on its tty `/dev/ttys027` | **1024 unread bytes**, the macOS pty output cap |

Cats' cathost stopped reading that pane's pty when its `readPump` parked in
`emit`, the side effect the previous doc predicted. Fable fills the pty and
blocks in `write()`. It cannot progress until something drains the pty. Its
work is safe: `fb75a4b` is on `origin/master` and the tree is clean.

The opus agent (pid 76685, ced) is idle: `end_turn`, 0 bytes queued.

Mistakes made while writing the script:
- `awk '/main-thread/{f=1} f&&/^ *$/{exit}'` does not isolate a thread in `sample` output. There are no blank lines between threads, so it scanned all of them. Match on the nearest preceding `Thread_` header instead.
- `xargs basename` with two names treats the second as a suffix to strip. Use `sed 's#.*/##'`.

## The status script

This is a copy of the scratchpad script (the scratchpad gets cleared).
`fable-status [pid]` defaults to `claude --model fable`, and it works for any
Claude Code agent pid.

```zsh
#!/bin/zsh
# fable-status: is the fable agent alive, and is it working or waiting for input?
# Usage: fable-status [pid]   (default: find `claude --model fable` by command line)

pid=${1:-$(pgrep -f 'claude --model fable' | head -1)}
if [[ -z $pid ]] || ! kill -0 $pid 2>/dev/null; then
  echo "fable: not running"
  exit 1
fi

# The agent's project transcript dir is derived from its cwd (/ → -).
cwd=$(lsof -a -p $pid -d cwd -Fn 2>/dev/null | sed -n 's/^n//p')
dir=~/.claude/projects/${cwd//\//-}
transcript=$(ls -t $dir/*.jsonl 2>/dev/null | head -1)

ps -o pid=,etime=,time=,%cpu= -p $pid | read p etime cputime cpu
model=$(ps -ww -o command= -p $pid | sed -n 's/.*--model \([^ ]*\).*/\1/p')
echo "${model:-claude}: running  pid=$pid  up=$etime  cpu=$cputime (${cpu}% now)  cwd=$cwd"
echo "children: $(pgrep -P $pid | xargs -I{} ps -o comm= -p {} 2>/dev/null | sed 's#.*/##' | tr '\n' ' ')"

if [[ -n $transcript ]]; then
  age=$(( $(date +%s) - $(stat -f %m "$transcript") ))
  last=$(jq -r 'select(.type=="assistant") | .message.stop_reason // "-"' "$transcript" 2>/dev/null | tail -1)
  # end_turn = finished its reply and is waiting on you; tool_use = mid-task.
  case $last in
    end_turn) state="IDLE — finished its turn, waiting for input" ;;
    tool_use) state="mid-task (tool call or reply in progress)" ;;
    *)        state="unknown ($last)" ;;
  esac
  echo "transcript: last write ${age}s ago  → $state"

fi

# A quiet transcript is ambiguous: a long tool run, a permission prompt, or a
# process that cannot print. The last one is checkable directly. A pty's output
# queue holds what the process wrote and the terminal (in Cats: the pane's pty
# reader in cathost) has not yet read; macOS caps it at 1024 bytes, and a
# process that fills it blocks in write() until someone drains it.
tty=$(lsof -a -p $pid -d 1 -Fn 2>/dev/null | sed -n 's/^n//p')
if [[ $tty == /dev/tty* ]]; then
  outq=$(python3 -c '
import os, fcntl, struct, sys
fd = os.open(sys.argv[1], os.O_RDONLY | os.O_NOCTTY | os.O_NONBLOCK)
print(struct.unpack("i", fcntl.ioctl(fd, 0x40047473, b"\0\0\0\0"))[0])  # TIOCOUTQ
' $tty 2>/dev/null)
  echo "terminal: $tty  unread output: ${outq:-?} bytes"
  if (( ${outq:-0} >= 1000 )); then
    echo "⚠  STUCK — its terminal is not being read, so it is blocked printing and cannot progress"
  fi
fi
```

Output on the two live agents:

```
fable: running  pid=95000  up=01-05:30:27  cpu=37:13.94 (0.0% now)  cwd=~/projs/go/grmob
transcript: last write 1790s ago  → mid-task (tool call or reply in progress)
terminal: /dev/ttys027  unread output: 1024 bytes
⚠  STUCK — its terminal is not being read, so it is blocked printing and cannot progress

opus: running  pid=76685  up=01:43:40  cpu=1:06.01 (0.8% now)  cwd=~/projs/go/ced
transcript: last write 2617s ago  → IDLE — finished its turn, waiting for input
terminal: /dev/ttys025  unread output: 0 bytes
```

`TIOCOUTQ` (`0x40047473` on darwin) is the reliable signal. Opening the slave
tty `O_RDONLY|O_NOCTTY|O_NONBLOCK` just to query it neither steals input nor
makes it a controlling terminal.

## Option 2 was approved, then not done

The plan was to restart only catway and keep the panes. It was re-checked
against the running build before anything was signalled, and it would not have
worked.

- `go version -m /Applications/Cats.app/Contents/MacOS/{cathost,catway}` shows `vcs.revision=117ce96…` with `vcs.modified=false`. Build revisions can be read from the binaries without running them.
- No commit between `117ce96` and fix 1 touches `internal/orchestration/host.go`, `cmd/cathost/main.go`, `cmd/catway/daemon.go` or `cmd/catway/main.go`. The running code is the code that was read.

Why it fails on that build:

```
kill catway ──▶ cathost writer: EPIPE → cancel() → conn closed
                cathost reader: parked in dispatch (emit on full out, or similar),
                                not in a read → the close never reaches it
                emit selects only on out / sessDone / h.closed
                sessDone closes only after the reader leaves its loop  → never
                ──▶ Attach never returns
                ──▶ serial accept loop never accepts the new catway
                    (dial succeeds via backlog; handshake times out; retries forever)
                ──▶ pane pumps stay parked; fable stays stuck; UI stays blank
```

Whichever call the reader is actually parked in (emit on a full `out`,
`writePTY` on a full pty input, or `h.mu`), none of them is released by the
connection closing. That makes the conclusion hold without a goroutine dump.
A dump was not an option anyway: SIGQUIT would exit cathost and end every pane.

Also:
- **SIGTERM would not stop the frozen catway.** Its handler (`cmd/catway/main.go`) does `<-sigc; o.post(Shutdown); <-sigc; os.Exit(1)`, and `o.post` blocks on the full mailbox, so the second `<-sigc` is never reached. Only SIGKILL would work.
- **The earlier recovery write-up was wrong** on both counts: "SIGTERM twice" and "cathost detaches with its panes". Fix 1's `sessEnd` is exactly what makes a catway-only restart work, in builds that have it.

Nothing was killed.

## Recovery, as it now stands

The only way out ends cathost's panes. What that costs:

| pane | loss |
| --- | --- |
| fable (grmob) | its unprinted closing reply only; work pushed as `fb75a4b`; `claude --resume` in `~/projs/go/grmob` |
| opus (ced) | nothing; finished and pushed `3986bae` |
| shells, cats-todo panes | running processes; layout and scrollback restore from the 22:08:33 save |

Proposed order:
1. Build and install Cats.app from `main` (`scripts/build-macapp.sh`) so the relaunch has all three fixes. Replacing the bundle doesn't affect the running process, which keeps its binaries mapped.
2. The user quits and reopens Cats.app themselves.
3. Resume agents with `claude --resume` where wanted.

Step 1 was offered but not yet approved.

## Next

- **Recover Cats.** Build and install Cats.app from `main` (offered, awaiting a yes); the user quits and relaunches; resume fable and opus with `claude --resume` if wanted. A catway-only restart is **not possible** on the running `117ce96` build (see above).
- **Once relaunched, check that a catway-only restart now keeps panes.** With fix 1's `sessEnd`, a SIGKILLed catway should let cathost's `Attach` return and a new catway reconcile. This is the recovery path that was missing tonight, and it has only unit-test coverage (`TestAttachDropsAClientThatStopsReading`).
- **Fix catway's SIGTERM path.** The handler posts `Shutdown` into the mailbox before it can see a second signal, so a jammed loop makes SIGTERM useless. Read the second signal first (or `select` on post vs signal), or run the exit on a timer independent of the loop.
- **Fix 4: keep daemon logs.** catapp sends catway/cathost stdio to `/dev/null` after boot; write it to a rotating file beside `boot.log`. catapp also doesn't notice a catway exit.
- **Consider moving the status script into the repo.** It currently lives only in this doc; `catctl` may be a better home, e.g. a pane-health command that uses `TIOCOUTQ` on each pane's pty.
- **Pre-existing test failure in `internal/inputenc`**: `TestGeneratedKeyCodesParse` and `TestCommonKeysSurviveTheChain` read `cmd/catgen-dart/testdata/golden/keys.g.dart`, deleted in `5add396`.
- **Consider bounding cathost's `out` like catway's outbox.** Emitters, the pty pumps among them, still block for up to the 20s stall bound, which is exactly how fable got stuck.
- **Run the new tests under `-race`** once `libghostty-vt-static` is available.
- **Confirm the freeze trigger with logs** (depends on fix 4). The autoclose reflow after a clean agent exit is inferred, not seen.
- **Carried from 2026-09-11:** the other four flag kinds (`?` `★` `⚠` `✓`) are still tinted text. Deliberately left half drawn; open only if wanted.
