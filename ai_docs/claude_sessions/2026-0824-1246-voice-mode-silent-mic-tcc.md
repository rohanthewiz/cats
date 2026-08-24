# Voice mode in a cats pane: recorded fine, transcribed to nothing

Session: `894bc639-27ba-4f36-af38-99a08a5e778d` (https://claude.ai/code/session_01GBDjp19nFsFFsooS9m2Bap)
Date: 2026-08-24 · Branch: `main` · Commit produced: `fb67f3a`

## Symptom

Claude Code's `/voice` hold-to-talk did not work inside a Cats terminal pane
(the "ghostty terminal within cathost"). Voice mode enabled fine; holding
space produced no transcript. Same feature works in standalone Ghostty.

## How it was diagnosed (layer by layer, each one cleared)

1. **Keyboard.** A terminal has no key-release events, so Claude Code infers
   "space is held" from OS auto-repeat: every space char feeds an accumulator,
   a 120 ms gap resets it, recording arms at 5 (constants `Jwn=120`, `Kcy=5`
   in 2.1.241). Wrote `keyprobe.py` (raw-mode tty, timestamps each read,
   replays that exact rule) and ran it in a split pane via `catctl split` /
   `catctl run`. Verdict: repeats arrive every ~84 ms, well under 120 —
   the WKWebView → catway → `internal/inputenc` (libghostty
   `KeyActionRepeat`) path is fine.

2. **Recording pipeline.** Ran `claude --debug` in the pane and read
   `~/.claude/debug/<session>.txt`. The `[voice]` trace showed the full happy
   path: hold detected → native recorder (audio-capture-napi) started →
   ~2–4 s of 16 kHz PCM streamed to
   `wss://api.anthropic.com/api/ws/speech_to_text/voice_stream` → clean
   finalize → **`Final transcript assembled (0 chars)`**, zero interim
   transcripts, no errors. Twice. The service heard silence.

3. **Microphone.** SoX `rec` from the same pane tree captured real audio —
   which misled at first. The discriminator turned out to be the capture API,
   not process ancestry: a Swift `AVAudioEngine` input tap run from the same
   position (even at the hardware's native 48 kHz) returned **pure zeros**,
   while SoX's raw-CoreAudio path got signal.
   `AVCaptureDevice.authorizationStatus(for: .audio)` → **`notDetermined`**.

## Root cause

macOS TCC attributes a pane process's microphone request to its *responsible
process* — the app bundle at the top of the tree, `Cats.app` — and renders the
permission prompt with the usage string from **that bundle's** Info.plist.
`Cats.app` shipped no `NSMicrophoneUsageDescription`, so no prompt could ever
render; TCC silently denies by handing AVFoundation all-zero frames.
Claude Code's native recorder dutifully streamed that silence, and the STT
service correctly transcribed nothing. No error surfaces anywhere.

Ghostty.app ships the exact key ("A program running within Ghostty would like
to use your microphone") plus a dozen siblings — which is why voice works
there and not in cats.

Two aggravators in the old bundle: it was only linker-signed, so the bundle's
code-signing identifier was **`a.out`** (any TCC grant would land on a
meaningless name), and Info.plist was unsealed.

## Fix (`scripts/build-macapp.sh`, commit `fb67f3a`)

- Add `NSMicrophoneUsageDescription` to the generated Info.plist ("A program
  running in a Cats terminal pane would like to use the microphone.").
- `codesign --force --sign - "$APP"` after assembly: identifier becomes
  `dev.cats.app` (from CFBundleIdentifier), Info.plist gets sealed.
- Comments in the script now carry the TCC-attribution rationale.

Caveat: adhoc signature has no cert chain, so TCC pins the grant to the
build's cdhash — every `make macapp` rebuild costs one re-prompt.

## Verification steps (post-session)

Quit Cats (⌘Q), reopen, hold space in a `claude` pane → one-time macOS mic
prompt attributed to Cats → approve → transcription works. If no prompt:
`/voice off` then `/voice hold` re-runs the permission request.

## Lessons / reusable notes

- **TCC denial is silent silence.** Denied capture doesn't error — it yields
  zeroed frames. "Records but transcribes to nothing" is the signature.
- **Attribution walks to the responsible app.** Anything privacy-guarded that
  a pane workload does (mic, camera, contacts…) needs a usage-description key
  in *catapp's* Info.plist, not the workload's. Ghostty's plist is the
  reference list.
- **SoX hearing audio proves less than it seems** — the raw-CoreAudio path can
  pass where AVFoundation is denied; test with an `AVAudioEngine` tap
  (`mictap.swift` pattern: install tap, report RMS; 0.000000 = denial).
- **Detached processes lose mic too**: the same `rec` that worked as a
  synchronous child recorded pure zeros when run via a detached background
  task or `nohup … & disown` — attribution breaks when orphaned. Ground-truth
  recordings must be spawned attached.
- `catctl split` / `run` / `capture` + `claude --debug` in a scratch pane is a
  solid harness for interactive-input debugging; `keyprobe.py` and
  `mictap.swift` live in this doc's history if needed again.
- Claude Code voice internals (2.1.241): space-hold detection is 5 chars
  under 120 ms gaps; release inferred when repeats stop; SoX `rec` is the
  fallback recorder only when the native napi module fails to load.
