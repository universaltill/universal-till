# Review — ship the actual supplied logo (ut-docs#290, reopened)

**Scope:** this repo's part of the cross-repo remediation of card #290,
2026-08-05. The full record, including why the card was reopened and the
evidence, is `ut-docs/code-reviews/2026-08-05-logo-real-asset.md`.

## Summary

The 2026-08-04 attempt shipped the **previous logo renamed** to
`unitill-logo.svg` — the artwork paths were byte-for-byte identical, only
three Inkscape export-metadata attributes differed. Every check that day
matched on filenames, so nothing caught it. The supplied mark
(`sha256 d4816d6d…`, 4,838 bytes, portrait aspect 0.73) is now in place, and
the guards assert **content**, not filenames.

## Changed here

- `web/public/assets/logo/unitill-logo.svg` — the real supplied mark;
  `ut-logo.ico` regenerated from it.
- `scripts/ci/check-brand-assets.sh` — asserts the canonical sha256, and that
  the dark-surface plate rule survives in `app.css`.
- `web/public/app.css` — the mark is a black silhouette, so the login and
  self-order surfaces get the same white plate the nav `.logo` already used;
  Midnight puts `--surface` at `#1e293b`, where black-on-dark is invisible.
  Height-only sizing throughout: the mark is portrait and pinning both
  dimensions distorts it.
- `packaging/macos/build-app.sh` — `sips -z s s` forced a square from a
  portrait raster, stretching the app icon. Now rasterises through a square
  wrapper viewBox that centres the mark.
- `android/generate-launcher-icons.sh` (new) — the mipmap PNGs were still the
  old mark; nothing in the 2026-08-04 change touched them. They are derived
  artifacts now, regenerated from the canonical SVG.
- `tests/e2e/tests/pos_ui_mvp.spec.ts` — the assertion no longer trusts the
  filename: the image must decode (`naturalWidth > 0`) and be portrait, so a
  landscape mark fails whatever it is called.

## Verification

- `go build ./...`, `go test ./...` — 50 packages, pass.
- `scripts/ci/check-brand-assets.sh`, `guard-i18n.sh`, `guard-data-access.sh`.
- Driven run against a freshly seeded DB (`scripts/e2e_seed`, `UT_AUTH=off`):
  Playwright **16/16**, and nav / login / self-order captured in both the
  default and Midnight themes with the mark measured at its true aspect.
- Red-then-green on both new guards: with the old logo restored the brand
  guard exits 1 and the e2e spec fails; restoring the real asset passes.
- Local server stopped and port 8080 confirmed free afterwards.
