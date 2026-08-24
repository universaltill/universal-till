# Code review — ut-docs#930: `make docs-shots` nondeterminism

**Date:** 2026-08-24
**Card:** [ut-docs#930](https://github.com/universaltill/ut-docs/issues/930)
**Complexity:** medium
**Build model:** Sonnet (inline) — **Review model:** Opus, fresh-context subagent (per the medium-tier routing)

## The problem

`make docs-shots` — the Playwright harness that regenerates the manual's
screenshots into `web/help/img/<locale>/<topic>.png` — was not byte-stable.
Run twice from an identical clean tree, with no source change, it
regenerated different bytes for a consistent set of screenshots every run
(`{en,fa,ar,tr}/alerts.png`, `{en,fa,ar,tr}/designer.png`), plus
intermittently `invoices`. Because most UI PRs are forced to re-run this
target (guard-docs-shots.sh), that churn dragged 8+ unrelated binary PNG
diffs into every such PR — review noise, repo weight, and a false-negative
risk (a genuinely stale screenshot hiding in the churn).

## Root causes (each confirmed with a live driven till, not just by reading)

- **`designer`** (`/receipt-designer`): `sampleReceiptDoc`
  (`internal/pages/receipt_designer.go`) baked `time.Now().Format("2006-01-02
  15:04")` into the sample receipt's Meta line. Confirmed: a booted till
  rendered the Meta line as the live wall-clock minute.
- **`alerts`** (`/backoffice`): the "recent problems" panel renders
  `logging.Recent()` entries, each shown with `{{ .At.Format "15:04" }}`.
  `Problem.At` is stamped at log time with `time.Now().UTC()`. On the
  auth-off docs till the panel deterministically shows exactly one WARN line
  ("UT_AUTH=off — operator login disabled") — but its HH:MM timestamp drifted
  every run. Confirmed live: the panel rendered `05:02`/`15:04` etc. tracking
  the wall clock.
- **`invoices` / `translations`** (intermittent): a capture-time paint race.
  `/translations`' key table is an `hx-trigger="load"` htmx fragment swapped
  in after first paint; the capture could race the swap/settle. A pixel-diff
  of two `ar/translations` captures isolated the residual to ~10 pixels in a
  fixed 2px×80px band — a single Arabic glyph's sub-pixel anti-aliasing
  toggling between two rasterizations.

## What shipped

1. **`internal/clock`** (new leaf package, stdlib-only): `clock.Now()` returns
   `time.Now()`, or a fixed instant parsed once (via `sync.Once`) from the env
   var `UT_DOCS_SHOTS_NOW` (RFC3339) when set. Unit-tested (`clock_test.go`):
   valid/invalid/empty parsing and the env-pinned `Now()` path.
2. **`internal/logging/logging.go`**: `remember()` stamps `Problem.At` with
   `clock.Now().UTC()`. No import cycle (`clock` is a stdlib-only leaf).
3. **`internal/pages/receipt_designer.go`**: `sampleReceiptDoc`'s Meta uses
   `clock.Now()`.
4. **`e2e/playwright.docs.config.ts`**: both docs webServers get
   `env: { UT_DOCS_SHOTS_NOW: '2026-01-02T15:04:05Z' }`. Deliberately NOT set
   in the shared `run-till.sh` (which the real e2e suite also boots) so e2e
   keeps the live clock — verified `run-till.sh`/`run-till-auth.sh` are shared
   by both `playwright.config.ts` and `playwright.docs.config.ts`.
5. **`e2e/tests-docs/docs-shots.spec.ts`** `capture()`: an htmx-idle wait
   (poll until no `.htmx-request/.htmx-swapping/.htmx-settling`), a
   double-`requestAnimationFrame` settle, and `animations: 'disabled'` on the
   screenshot — capture-robustness for the lazy-load/paint class. Rewrote the
   stale "KNOWN, ACCEPTED NON-DETERMINISM" comment (designer is now fixed) and
   documented the manifest-vs-PNG contract.
6. Regenerated the 8 `alerts`/`designer` PNGs + `manifest.json` with the pin.

## Production safety

The pin is completely inert outside the docs harness: with `UT_DOCS_SHOTS_NOW`
unset — production, the e2e suite, real installs — `clock.Now()` is exactly
`time.Now()`. `sampleReceiptDoc` is also used by the live `POST
/api/receipt-designer/preview`; there the sample receipt keeps showing the
real current minute, unchanged. The env is read once per process
(`sync.Once`), so `remember()` stays cheap on the logging hot path.

## The manifest-vs-PNG contract (AC #4)

`guard-docs-shots.sh` checks freshness from **source-surface hashes** recorded
in `manifest.json`, and never hashes the PNG bytes. The clock pin removes the
large, every-run, content-level churn (timestamps) at the root. A residual
sub-pixel anti-aliasing flake remains on the heaviest text screens
(`ar/translations`, occasionally `invoices`): a handful of pixels can still
toggle run-to-run — the same browser text-rasterization nondeterminism that
makes Playwright's own `toHaveScreenshot` compare with a pixel *tolerance*
rather than byte-equality. It survives every DOM-side settle. Because the
guard hashes source not PNG bytes, this AA noise cannot fail CI. The intended
contract, now documented in the spec: a PR touching a screened surface must
regenerate and commit `manifest.json`, but need only commit the PNGs whose
**content** actually changed — regenerated PNGs differing only by AA noise
should be reverted, not committed. The ut-docs#925 manifest-only commit was a
legitimate, correct outcome; it was just undocumented. With the clock pin this
manual triage is now rare and tiny, not the 8-file every-run event #930
reported.

## What was verified

- **Determinism of the shipped change**: `make docs-shots` run twice produced
  byte-identical `alerts`/`designer` PNGs (the clock-pinned content), proven by
  md5-comparing both runs.
- The committed diff is minimal and honest: exactly the 8 `alerts`/`designer`
  PNGs (real content change) + `manifest.json`. `translations`/`invoices`
  reproduced their committed bytes, so no incidental churn was committed.
- Live-till confirmation that with the pin set, `designer` renders
  `2026-01-02 15:04` and the `alerts` problems panel renders `15:04`; with the
  pin unset, both track the live clock.
- `gofmt -l` clean; `go build ./...` clean; `go vet` clean on touched packages;
  `go test ./...` green (incl. `-race` on `internal/clock` and
  `internal/logging`); `guard-docs-shots.sh`, `guard-i18n.sh`,
  `guard-data-access.sh` all exit 0.

## What the independent review found

_(Opus fresh-context subagent — findings triaged below.)_
