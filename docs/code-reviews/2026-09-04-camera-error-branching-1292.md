# Code review: camera-error branching (ut-docs#1292)

**PR:** universaltill/universal-till#772 (`fix/1292-camera-error-branching`)
**Reviewed by:** independent Opus subagent (Dev/original implementer used a
different session/model; this pipeline cycle — lane:cloud-54 — additionally
fixed a CI-red `guard-docs-shots` failure on the same branch before review).

## What shipped

The camera overlay's `getUserMedia(...).catch()` collapsed every rejection
into one generic `scan.camera.camera_error` ("Camera unavailable"). Both
camera IIFEs (`ai.identify` overlay and `scan.camera` barcode-scan overlay)
in `web/public/app.js` now branch on `err.name`:

| `err.name` | message |
|---|---|
| `NotFoundError` / `OverconstrainedError` | camera_not_found |
| `NotAllowedError` / `SecurityError` | camera_permission_denied |
| `NotReadableError` | camera_busy |
| anything else / no `err.name` | camera_error (existing fallback) |

6 new i18n keys per locale (`en/fa/ar/tr`, both `scan.camera.*` and
`ai.identify.*` namespaces), new `data-msg-*` attributes wired in
`web/ui/pages/index.html`, and an e2e regression test
(`e2e/tests/camera-error-branching-1292.spec.ts`) covering the
`scan.camera` overlay's four branches.

**This cycle's own contribution:** `guard-docs-shots.sh` was failing CI —
`web/ui/pages/index.html` is part of the "sell" topic's whole-file hash, and
the new `data-msg-camera-*` attributes changed it without a screenshot
regen. Ran `make docs-shots` (96/96 pass) and committed the refreshed
`manifest.json` + drifted PNGs (sell, invoices, translations, till-designer
— the latter three drift for reasons unrelated to this diff, e.g. rendered
timestamps).

## Independent review — verdict: SAFE TO MERGE

Full pass (diff read, `gofmt`/`go build`/guards run, e2e TDD
revert-restore) — see the review agent's own report for the complete
transcript. Summary:

- **Correctness**: the two `.catch` bodies are byte-for-byte symmetric; no
  copy-paste asymmetry between the two overlays. Fallback branch confirmed
  to still work for an unrecognized/missing `err.name`.
- **TDD re-verification**: reverted both `.catch` bodies to the old
  generic-message behaviour, rebuilt, re-ran the e2e test — 3 of 4 cases
  failed with the expected real assertion mismatch (not a crash), the
  fallback case correctly still passed. Restored, re-ran: 4/4 pass. This is
  a genuine regression test, not a false-pass.
- **i18n**: `guard-i18n.sh` passes; all 6 keys present with real
  translations (not `en` placeholders) in all 4 locale files.
- **Gates**: `gofmt -l .` clean, `go build ./...` clean,
  `guard-docs-shots.sh`/`guard-help-topics.sh`/`guard-compliance-claims.sh`/
  `guard-e2e-fixtures-import.sh`/`guard-htmx-loaded.sh` all pass.
- **docs-shots commit**: spot-checked as legitimate — PNGs are valid,
  non-zeroed images; the `translations.png` growth (+12KB) was rendered
  before/after and visibly shows the two new key rows; other deltas are
  normal PNG-compression jitter on an unrelated screenshot re-render.
- **Manual**: no help-topic update needed — `sell.md`/`quickstart.md`
  document the camera *buttons/flow*, not the error-message text itself
  (including the pre-existing generic message), so there's no stale prose
  to correct.
- No real client/shop name, no secret-shaped literals.

### Findings — none blocking, none fixed in this PR

1. **(Medium) The `ai.identify` overlay's own branching is untested** — no
   e2e spec covers it at all (`ai-identify` doesn't appear under
   `e2e/tests/`). A future edit to that overlay's duplicated `.catch` body,
   or a typo'd `data-msg-*` attribute on `#ai-identify-overlay`, would ship
   a silent blank error message with a fully green suite.
   **Deferred to ut-docs#1559** (new Backlog card, not itself
   blocker-class — this PR's own scope was `scan.camera`, matching the
   original ticket's driven repro).
2. (Low) `OverconstrainedError` → "no camera found" is a slightly lossy
   mapping given the `facingMode: 'ideal'` (non-`exact`) constraint shape;
   harmless today, worth a comment if constraints ever gain `exact`.
3. (Low) `AbortError` is unmapped, falls to the generic fallback — correct
   safe behaviour, just noted.
4. (Low, actioned) 6 new `en.json` keys mean `lang-pack-drift` goes red on
   `main` post-merge unless `ut-plugin-language-{de,es}` carry them too —
   ported in the same cycle, see below.
5. (Nit) Redundant `if (err && err.name)` wrapper — the `default:` case
   already covers it; harmless, not fixed.

## Verified beyond automated tests

The e2e TDD revert-restore above (real induced failure, not just "tests
pass"). No manual/hardware verification needed — this is a browser-API
error-message mapping, not hardware-dependent.

## Lang-pack-drift follow-up (owned by this merge, per lane-ownership rules)

This merge adds 6 keys to `web/locales/en.json`
(`scan.camera.camera_not_found`, `.camera_permission_denied`,
`.camera_busy`, `ai.identify.camera_not_found`,
`.camera_permission_denied`, `.camera_busy`). Ported the same keys into
`ut-plugin-language-de` and `ut-plugin-language-es` in this same cycle —
see those repos' own PRs, linked from ut-docs#1292's close-out comment.

## Safe-to-merge verdict

**Yes.** `merge_method: "merge"` per this repo's standing convention
(ut-docs#250 — preserves original commit authorship).
