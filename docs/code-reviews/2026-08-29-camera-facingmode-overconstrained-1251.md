# Code review — camera button reports no camera on a device that has one (ut-docs#1251)

- **Date:** 2026-08-29
- **Branch:** `fix/1251-camera-facingmode-overconstrained`
- **Reviewer:** independent reviewer (Opus, this pipeline's `complexity:medium`
  review tier per the `reviewer` skill's model-routing table), isolated
  git worktree, revert/restore TDD re-verification.
- **Verdict: SAFE TO MERGE** (after the fix below — the review's first pass
  on the original diff was correctly BLOCKING; see "What the review found").

## What was reported

"the camera button is saying there is no camera" on a device that should
have one. No reproduction steps were available (no direct human access to
the reporting device); scoped from code reading only.

## First pass (rejected) — what the review found

The initial diagnosis, made from code reading alone, was that
`navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })`
treats a bare-string `facingMode` as a REQUIRED exact constraint, and that a
device whose only camera can't satisfy "environment" would reject with
`OverconstrainedError`. The fix changed both call sites in `web/public/app.js`
(the AI-identify photo-capture IIFE and the camera barcode/QR-scan IIFE,
ut-docs#548) to `facingMode: { ideal: 'environment' }`, plus a matching e2e
regression test.

**The independent Opus review disproved this premise directly against a real
browser**, rather than taking the comment's claim on faith: a bare-string
value in the *basic* constraint set is already treated as `ideal` per the
MediaStream Constraints spec — only a value nested in `advanced`, or an
explicit `{ exact: … }` (never used here), is mandatory. The review probed
Chromium with a fake capture device reporting no `facingMode` at all: both
the bare-string and the `{ ideal: … }` forms resolved identically; only
`{ exact: … }` rejected with `OverconstrainedError`. **The original fix was a
behavioural no-op**, and its accompanying test was circular — it stubbed
`getUserMedia` to reject *iff* `typeof facingMode === 'string'`, which is not
real browser behaviour, just a restatement of the (wrong) implementation. ut-docs#1251
would have stayed open with the diff reading as "fixed."

## What actually shipped, after the correction

The review's own code-reading turned up the real, verifiable defect: neither
`getUserMedia` call site ever checked that `navigator.mediaDevices` (and its
`.getUserMedia` method) exists before calling it. On a **non-secure-context
origin** — plain `http://` to a LAN IP rather than `localhost` — the platform
never defines `navigator.mediaDevices` at all, so `navigator.mediaDevices.getUserMedia(...)`
throws a **synchronous `TypeError`**, before the promise chain (and its
`.catch()`) even exists. Confirmed against a real Chromium instance during
review (`secureContext=false` → `mediaDevices=undefined`). The overlay opens,
but the camera never starts and — because the throw happens before the
`.catch()` that would normally set the "Camera unavailable" status — **no
error is shown at all**, not even the existing message. This till is a LAN
kiosk product; a secondary device (self-order tablet, another till) reaching
a page over plain `http://<lan-ip>` is an ordinary deployment shape, not an
edge case.

The fix: both `open()` functions (AI-identify and camera barcode/QR-scan)
now guard explicitly —

```js
if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
  setStatus(msgs.msgCameraError);
  return;
}
```

— before calling `getUserMedia`, so that case reports the same, already-
existing "Camera unavailable" message instead of hanging silently with no
feedback. The disproven `facingMode: 'environment'` → `{ ideal: … }` change
was reverted in both places (kept as the harmless-but-pointless no-op it is
would have left misleading comments in the codebase for no benefit).

The e2e regression test in `e2e/tests/sale-screen-camera-barcode-scan-548.spec.ts`
was rewritten to match: it now stubs `navigator.mediaDevices` as `undefined`
(simulating the non-secure-context case) and asserts the overlay shows
"Camera unavailable" with **no uncaught page error** (`watchConsole`'s
`pageerror` listener), rather than the disproven OverconstrainedError model.

**Known, accepted scope gap**: the report's literal symptom — an explicit
"no camera" message on a device that has one — is not fully reproduced by
this fix, because the actual reporting device was never available to test
against. What this fix closes is a real, independently-confirmed bug in the
same feature area (a silent hang with zero feedback, worse than a wrong
message) that is a plausible match for the report. The `.catch()` in both
IIFEs still collapses every *other* getUserMedia failure — `NotAllowedError`
(permission denied), `NotFoundError` (genuinely no camera), `NotReadableError`
(camera in use by another app) — into the same generic "Camera unavailable"
text, which could independently produce a "says no camera" complaint for a
recoverable condition. Distinguishing those requires new user-facing copy
(new locale keys across `en`/`fa`/`ar`/`tr`, `guard-i18n.sh`-enforced), and
this pipeline's translation path (self-hosted Ollama on the homelab NAS,
`ut-docs/reference/translation.md`) is unreachable from this cloud session
(confirmed: connection timeout to `192.168.1.231:11434`) — filed as a
Backlog follow-up (ut-docs#1292) rather than silently left uncovered or
force-shipped with English-only/untranslated strings.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `go test ./...` | pass, 0 failures, all packages |
| `guard-i18n.sh` / `guard-data-access.sh` / `guard-kiosk-engine.sh` | pass / pass / pass (no Go/SQL/i18n changed) |
| `guard-docs-shots.sh` | pass — `web/public/app.js` changed (whole-file hash), so `make docs-shots` was re-run; only `en/sell.png`, `ar/sell.png`, `en/invoices.png` (antialiasing-level pixel drift from the sandbox's non-pinned Chromium revision, read as images and confirmed clean — no corruption, layout intact, RTL correct) plus the manifest actually changed |
| `e2e/tests/sale-screen-camera-barcode-scan-548.spec.ts` (full file) | 8/8 pass |

**TDD re-verified independently** (isolated worktree, revert-then-restore):
with only the `mediaDevices` guard reverted (test kept), the new test failed
with a real, specific assertion (`status` stuck at "Point the camera at a
barcode or QR code" instead of "Camera unavailable" — confirming the
pre-fix code genuinely hangs rather than declaring an error); restoring the
guard returned it to green, with no other test in the file affected.

**Visual check**: `web/help/img/{en,ar}/sell.png` and the opened
barcode-scan overlay (ad hoc screenshot during Tester's pass, not committed)
read as clean — no overlapping/clipped text, RTL (Arabic) lays out
correctly, dark-theme and kiosk/touch sizing were **not** separately
re-checked (this change touches zero CSS/HTML, no visual risk beyond what
the regenerated docs-shots already exercise across all four locales).

## Deferred / follow-up

- **ut-docs#1292** (new Backlog card, `needs-info`-free but flagged
  `blocked:env` for the translation step specifically): branch the
  `.catch()` in both IIFEs on `err.name` (`NotAllowedError`,
  `NotFoundError`, `NotReadableError`, `OverconstrainedError`) to give
  distinct, honest messages instead of one generic "Camera unavailable" —
  needs new locale keys translated into `fa`/`ar`/`tr` via the homelab
  Ollama endpoint, unreachable from a cloud pipeline session.
- ut-docs#1251 stays open until reproduction against the actual reporting
  device confirms whether this was the root cause, or the `err.name`
  follow-up above is needed too. Recommended default if no further report
  comes in: close after this fix ships and ask the reporter to confirm.
