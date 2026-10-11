# Review: "Remove background" in the item, category and variant photo pickers (ut-docs#3127)

- **Branch:** `feat/3127-photo-cutout-picker` (+ `i18n/3127-photo-cutout-keys` in
  `ut-plugin-language-{de,es,pt}`)
- **Design:** ut-docs `architecture/ai-product-photo.md` §6/§8 (card C); the
  endpoint `POST /api/catalog/image/cutout` pre-existed (universal-till#1817).
- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Fable (independent
  subagent, own worktree). Orchestrator triaged and fixed.

## What shipped

- `aiCutoutOffered` (`internal/pages/ai_cutout.go`): `catalog_management` **and**
  a configured background-removal service — the same gate as the endpoint.
  Reached from `internal/pages/catalog` through `Deps.AICutoutOffered` (import
  cycle; same pattern as `MainTillUnreachable`). `AICutout` flag on the item
  page, the variants panel and the categories page; off → the pages render as
  before.
- One shared client module `web/public/photo-cutout.js` + partial
  `web/ui/partials/photo_cutout.html` (all strings from `{{ T }}` data
  attributes). The offer appears right after a photo is taken or chosen; nothing
  is sent until **Remove background**; the browser downscales to a 1024 px long
  edge; original vs result, background tile colour / white / none (re-request,
  cached per choice); **Use this** puts the PNG into the page's own file input so
  the existing save runs; **Retake**; **Use original photo**. Errors inline, the
  original stays selected; no dialog, no `showModal`.
- Variant rows: with the offer on, a pick no longer auto-uploads; the upload runs
  on Use this / Use original photo. Off → unchanged.
- 13 i18n keys in en/ar/fa/tr and the de/es/pt packs; manual: catalog +
  categories topics in en/de/ar/fa/tr incl. the self-hosted rembg setup;
  docs-shots regenerated for those two topics.
- e2e: `cutout` Playwright project (`run-till-cutout.sh`, port 8101) against
  `fake-rembg-server.mjs` (8102); `photo-cutout-off-3127.spec.ts` in `default`
  pins the AI-off behaviour. Stale `SquareTile`/`subjectBounds` deadcode
  baseline lines dropped (now reachable).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Tile-colour swatch painted once at pick time; the colour sent is re-read per request (operator changes colour on Details after picking → swatch white, result red) | **Fixed**: `paintSwatch` on every request |
| 2 | should-fix | Every panel button disabled while busy (100 s timeout) — no way out from a hung rembg | **Fixed**: Use original photo stays enabled and aborts the request; e2e `a request in flight…` (fails on the pre-fix module) |
| 3 | should-fix | Variant: a pick on a second row orphaned the first row's undecided pick (never uploads) | **Fixed**: previous row's input cleared; e2e `…second row drops…` (fails on the pre-fix module) |
| 4 | should-fix | Report-only CSP has no `img-src blob:`; the module decodes via `<img src=blob:>`, so an enforcing policy as-is would break Remove background | **Deferred** to ut-docs#3327 (CSP enforcement; finding added there). The policy is report-only today and the item preview already relies on `blob:` |
| 5 | nit | Superseded answer created an object URL never revoked | **Fixed** |
| 6 | nit | Per-render cost: `canPerform` + two plugin-settings reads on catalog/categories/variant renders | Accepted: three small SQLite reads on back-office renders, never the sale path |
| 7 | nit | `e2e/README.md` didn't list the `cutout` project; project count stale | **Fixed** (generic wording) |
| 8 | nit | en: key says "Tile color", help sentence mixes "color"/"colour" | Accepted: en.json is already mixed; editing the topic would force a full docs-shots regeneration for a spelling nit |
| 9 | nit | ar/fa: "no background" choice and "without background" caption read the same | Accepted: they label different things |
| 10 | nit | Help says nothing is kept on the shop's rembg server | Accepted: true of `rembg s`; no ADR-0040 claim (guard green) |

Reviewer's screenshot question (white swatch, red result in the German shot): not
a bug — the shot's item has no colour (white tile) and the fake's red box is the
*subject*; finding 1 was the only real way for the two to disagree.

## Verified

- Reviewer re-verified the TDD claim: reverting the template/flag changes makes 3
  of 4 `TestCutoutOffer*` fail; restored → 4/4 pass. The two new e2e specs were
  run against the pre-fix module (both fail: `isEnabled` false; first row still
  holds 1 file) and pass with the fix.
- `gofmt`, `go build`, `go vet`, `go test ./...`, `golangci-lint` (0 issues),
  every `build`-job guard (i18n, help topics/drift, no-showmodal, no-inline-
  handlers, compliance, competitor naming, core-neutral, data-access,
  docs-shots…). e2e `cutout` project 8 passed / 1 skipped (opt-in screenshots);
  existing image specs (catalog-image-to-till, category-image-2500, viewfinder
  1472, variant specs) pass.
- Looked at 30 screenshots: three surfaces × en/de at 1024×600 and 360 px, fa
  (RTL) at 1024×600 — no overlap/clipping; German wraps cleanly; fa mirrors.
- Local shellcheck was 0.11.0 (CI pins 0.9.0): 14 SC2329 infos in untouched
  files only; the new `e2e/run-till-cutout.sh` is clean.

## Not verified (hardware lane)

A real rembg model, a real touchscreen and a phone's OS camera (EXIF rotation)
— not available in a cloud container. Recorded on ut-docs#3127.

**Verdict:** safe to merge.
