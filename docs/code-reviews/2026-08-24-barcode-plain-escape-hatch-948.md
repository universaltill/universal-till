# Code review: catalog barcode plain-code escape hatch + BarcodeExists/DeleteBarcode canonical-key agreement (ut-docs#948)

**Date:** 2026-08-24
**Author:** autonomous pipeline (Dev+Tester: Sonnet, inline; Review: Opus, worktree-isolated subagent — `complexity:medium` per the scrum-master skill's model routing)
**Issue:** universaltill/ut-docs#948
**Design of record:** `ut-docs/adr/0059-barcode-symbology-registry.md` (Decision §2, §3); filed as a required follow-up by the ut-docs#934 review (`docs/code-reviews/2026-08-24-barcode-scan-registry-integration-934.md`, findings F1/F6).

## What shipped

Two gaps the #934 review left as follow-ups, both only reachable once a
shop enables an embedded symbology (`EAN13_WEIGHT_PREFIX2X` /
`EAN13_PRICE_PREFIX02`, ADR-0059) — which no production caller can do yet
(#935's settings checklist UI hasn't shipped), so this ships ahead of the
risk rather than after it.

- **F1 — plain-code escape hatch.** A `forcePlainBarcode` checkbox on the
  two catalog barcode-attach entry points (`/api/catalog/barcode`'s
  chip-add forms, and `/api/catalog/item`'s auto-fill flow). When ticked,
  the handler passes an explicit `BarcodeType: "EAN13"` to `AddBarcode`,
  taking the existing explicit-type path (check-digit validation only, no
  registry inference) — so a genuine plain retail EAN-13 whose prefix
  falls in an enabled embedded range (`20`-`29`/`02`) is stored as typed
  instead of being reinterpreted as a zeroed embedded-data key.

- **F6 — pre-check/store agreement.** `BarcodeExists`/`DeleteBarcode` now
  resolve **exact-first, canonical-fallback**: they try the code exactly
  as given, and only on a miss compute the canonical (possibly zeroed)
  key via a shared `canonicalBarcodeKey`. Signatures unchanged, so
  `cloudsync_wire.go` / `import_page.go` / `pos.RemoveBarcode` pick up the
  fix with no call-site changes.

New i18n keys `catalog.barcode.force_plain` / `_hint` in all four locales;
a visible manual note under `web/help/*/catalog.md` step 2; regenerated
`make docs-shots` screenshots.

## Independent review (Opus, worktree-isolated subagent)

The review ran the full gate itself (build/vet/`go test ./...`/all guards)
and mutation-tested every new/modified test in throwaway `git worktree`s —
breaking the fix and confirming the test caught it — rather than trusting
the Tester's self-report. It confirmed as correct: the inverted pre-existing
assertion in `catalog_repo_barcode_registry_test.go` (it had been pinning
the F6 bug); the exact-first ordering (worked through partial-match,
cross-table, empty-string, and item-vs-variant collision cases); that the
escape hatch remains scannable via #934's F2 raw-code fallback; that request
parsing, scope, and security are clean.

It raised 14 findings. Disposition:

- **F-1 (BLOCKER) — `guard-docs-shots` red.** Confirmed introduced by this
  diff (green at the base commit). **Fixed:** ran `make docs-shots`,
  committed the regenerated `web/help/img/**` + `manifest.json`. (A 6-byte
  `invoices.png` re-render rode along — pre-existing rendering drift the
  full re-render picked up, harmless, kept so the global surface hash is
  fresh.)

- **F-2 (MAJOR, correctness bug) — escape hatch hard-rejected non-EAN-13
  codes.** Forcing `BarcodeType: "EAN13"` makes `AddBarcode` assert a valid
  EAN-13 check digit, so ticking the box on a valid EAN-8 / UPC-A /
  GTIN-14 / CODE128 / internal-PLU code returned a wrong 400 (and on the
  item-create path, created the item but silently dropped the barcode).
  **Fixed:** the handler now forces `EAN13` only when the code actually is
  a valid EAN-13 (`plainBarcodeTypeFor` guards on
  `barcode.ValidEAN13Checksum`). Gratuitous otherwise: only the two
  embedded symbologies (which both require an EAN-13 check digit) can
  mis-infer, so a non-EAN-13 code never needs escaping. Added
  `TestCatalogBarcodeForcePlain_NonEAN13CodesStillAccepted` (EAN-8 / UPC-A
  / PLU), mutation-verified it fails against the old blind-force behaviour.

- **F-3 (MAJOR) — `CanonicalBarcodeKey`'s "single place" comment was false;
  `AddBarcode` still inlined its own copy.** **Fixed:** extracted a shared
  unexported `matchBarcode(ctx, code)` that both `AddBarcode`'s inference
  path and `canonicalBarcodeKey` now call — genuinely one place computing
  the match. (`CanonicalBarcodeKey` also unexported per F-12 — no
  out-of-package caller.)

- **F-4 (MAJOR) — manual not updated.** **Fixed:** added a "Plain code
  (ignore weight/price)" note under `web/help/{en,fa,ar,tr}/catalog.md`
  step 2, translated in all four (ar/fa/tr hand-authored, NAS pipeline
  unreachable — folded into #957).

- **F-5 (MINOR) — stale `AddBarcode` comment** claiming the escape hatch
  was still an open #935 gap. **Fixed:** rewritten to point at this card's
  shipped `forcePlainBarcode` path.

- **F-6 (MINOR) — hint reachable only via `title=` tooltip** (useless on a
  touchscreen till). **Fixed:** the item-form now renders the hint as a
  visible `.muted` line under the checkbox (verified in the regenerated
  en + fa/RTL screenshots). The compact chip-add variant forms keep the
  `title=` tooltip, per the reviewer's own "if space is tight" allowance.

- **F-9 (MINOR) — scan/delete resolution asymmetry** for the collision
  where a plain escape-hatch code and a different item's genuine scale
  label share a zeroed template key. Each ordering is locally correct but
  the pair isn't coherent end-to-end. **Documented** inline in
  `DeleteBarcode` and **filed as ut-docs#958** (ADR-level question, not
  reachable in production until #935 ships).

- **F-10, F-11 (MINOR)** — fa/tr hint register; `lang-pack-drift` advisory
  on the external de/es packs. **Noted**, folded into #957 (register
  correction) — `lang-pack-drift` is advisory on a PR touching `en.json`,
  blocking only on push to `main`, and the de/es packs aren't in this
  session.

- **F-7 (MINOR, checkbox shown even though no shop can enable an embedded
  symbology yet), F-8 (per-row settings read in the import dedupe loop),
  F-12/F-13/F-14 (NITs — unexport `CanonicalBarcodeKey` [done],
  non-transactional twin DELETEs [pre-existing], long label in the compact
  form).** **Accepted/deferred.** F-7 is by design harmless (the box is a
  no-op until #935, and gating it on enabled symbologies is arguably
  #935's job); F-8's extra read is real but bounded and `catimport`
  already canonicalises so the fallback rarely fires; the nits don't
  warrant churn. None block.

## TDD / verification evidence

- New tests written test-first, confirmed failing against pre-fix code
  with the actual error message, then passing (F6 case (b)/(c), the F1
  handler wiring, and the F-2 non-EAN-13 regression all mutation-verified).
- `TestDeleteBarcode_ExactMatchWinsOverCoincidentalCanonicalCollision`
  pins that exact-first ordering is load-bearing for correctness (not just
  efficiency): canonical-first deletes the wrong item's row — proven by
  temporarily inverting the ordering and watching the test fail.
- A real driven run (throwaway auth-off till, demo catalog) screenshotted
  the catalog page + variants panel in **English (light + dark) and
  Persian (RTL)**; the checkbox, label, and (post-F-6) visible hint line
  all lay out correctly with no overlap/clipping in every combination.
  The regenerated `make docs-shots` en + fa/RTL screenshots were read and
  confirm the same.
- Full gate re-run after every fix batch: `gofmt -l .` clean,
  `go build ./...` / `go vet ./...` clean, `go test ./...` green (41
  packages), and every CI-blocking guard passes — `guard-docs-shots.sh`
  now green (was the F-1 blocker).

## Verdict

MERGE. The reviewer's NEEDS-REWORK verdict was on the pre-fix state; all
four must-fix findings (F-1 CI-red, F-2 correctness, F-3 dedup, F-4
manual) plus the cheap should-fix ones (F-5, F-6) are addressed, and the
deferred items are recorded on the board (#957, #958) rather than dropped.

## Follow-ups filed

- **ut-docs#957** (`blocked:env`) — re-verify the ar/fa/tr locale keys AND
  the catalog help-topic note against the real NAS Ollama pipeline (+ the
  F-10 register correction).
- **ut-docs#958** — ADR-0059-level decision on coherent scan/delete
  resolution for the plain-vs-embedded zeroed-template collision (F-9).
