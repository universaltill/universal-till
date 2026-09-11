# Code review: explicit error for shelf/price labels on a system printer (ut-docs#2062)

**Branch:** `pipeline/2062-label-print-system-mode-error`
**Complexity:** easy → reviewed by a fresh-context Sonnet subagent (Model routing by complexity)

## What shipped

`POST /api/print/labels` (`internal/pages/print_api.go`) previously let a
misconfigured "Regular printer (system)" connection fall through to
`print.NewTransport`, which has no `"system"` case and returned a generic
`unknown printer mode "system"` error — surfaced to the operator as a bare,
unexplained "Print failed" (502).

The fix checks `cfg.Mode == "system"` explicitly, ahead of `NewTransport`,
and returns a new translated 400 error (`catalog.labels.system_unsupported`)
naming the real constraint: a shelf/price label's entire content is a
scannable CODE128 barcode drawn via ESC/POS raster commands
(`print.RenderLabel`/`barcode()` in `internal/print/escpos.go`), and a
"system" (CUPS `lp`, plain-text-only) printer can never render that. This
mirrors a precedent already established in the same package: `RenderText`
deliberately omits `TSEQR` (a scannable code) on system-mode receipts rather
than faking a placeholder (ut-docs#1245) — labels have no such honest
degraded rendering either, so this is a permanent constraint, not a stopgap.

New locale key added to all four shipped locales (`en`/`ar`/`fa`/`tr`).
`web/help/en/printing.md` updated in two places to name the new message and
state plainly that this is permanent, not a gap pending a decision.

## Independent review — findings

Reviewed via a fresh-context Sonnet subagent (this card's `complexity:easy`
label routes review to a different Sonnet instance, per
`scrum-master`'s "Model routing by complexity"). Verdict: **safe to merge**,
one real prose nit found and fixed before merge:

- **Fixed:** `web/help/en/printing.md`'s "Nothing is printing" section
  (line 59, not originally touched by this diff) still claimed a label
  print "only ever show[s] a bare 'Print failed'" — directly contradicted
  by this same PR's own fix two lines below it. `guard-help-drift.sh` only
  checks heading/step/bullet *counts*, so it couldn't catch this
  prose self-contradiction. Reworded to carve out the label-print case and
  point at the "Printing shelf/price labels" section for its own specific
  message.
- Nitpick, not fixed (pre-existing, out of scope): `keyPrinterMode`'s
  comment in `print_api.go` (`// off | network | device`) has never listed
  `"system"` as a valid value — predates this diff, harmless.
- Confirmed `print.RenderLabel` has exactly one caller in the whole repo
  (the handler this diff touches) — no sibling code path (kitchen tickets,
  self-order, receipt-designer preview) needed the same fix.
- Confirmed all four locale files stay valid JSON with no duplicate keys
  and the same key count (2334) after the addition, and that translations
  reuse existing terminology from `settings.printer.mode_system`/
  `system_hint` rather than being machine-garbage.

## Verified beyond automated tests

- **TDD claim re-verified independently** (not just taken on trust): the
  reviewing subagent itself reverted the new `cfg.Mode == "system"` check,
  re-ran `TestPostPrintLabels_SystemModeRendersPosNoticeError`, confirmed it
  fails with the exact claimed pre-fix symptom (502, bare "Print failed" /
  "unknown printer mode"), then restored the check and confirmed the test
  passes again. `git diff`/`git status` confirmed the working tree was left
  exactly as committed afterward. (The implementing session had already
  done this same revert/restore check once before review; the reviewer
  repeated it independently rather than trusting that report.)
- Full CI-equivalent gate replicated locally, matching `.github/workflows/ci.yml`'s
  actual `Test` steps exactly (this repo's own `internal/plugins`/
  `internal/pages` packages are known, pre-existing, documented margin
  cases under a blanket `-race` run — ut-docs#643/#753/#776/#1992 — so CI
  itself never runs `-race` and splits these into their own
  `-timeout 20m` steps; replicated that exact split rather than a single
  `go test ./... -race`):
  - `go test` on the whole tree minus `internal/plugins`(+`oauth`/`marketplace`)/`internal/pages` — all pass.
  - `go test -v ./internal/plugins/oauth ./internal/plugins/marketplace` — pass.
  - `go test -timeout 20m -v ./internal/plugins` — pass (108.6s, isolated).
  - `go test -timeout 20m ./internal/pages` — pass (331.8s, isolated).
- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run ./...` — 0 issues.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-compliance-claims.sh`, `guard-page-http-error.sh` — all pass
  (`printing` topic's ar/de/fa/tr structural drift is pre-existing/
  baselined under ut-docs#332, unrelated to this diff's English-only prose
  edits within the same headings/list items).
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Safe-to-merge verdict

Yes. No blocking findings. One review finding (stale contradictory prose)
fixed before merge.

## Explicitly deferred / out of scope

- Making label printing actually work on a system printer (e.g. rendering
  the barcode as a rasterized image piped to `lp`) — not requested by this
  card, and no clear operator demand established; the explicit-error path
  is the card's own second suggested option and is what this fix
  implements.
- `keyPrinterMode`'s stale doc comment (pre-existing, harmless) — not
  worth its own follow-up card.
- Translation of the new locale key into the external `ut-plugin-language-{de,es}`
  packs — handled by the existing `lang-pack-drift` advisory-on-PR /
  blocking-on-`main` mechanism per this repo's own `CLAUDE.md`, not this
  PR's responsibility to pre-empt.
