# Review — Designer category row links to the category image editor (ut-docs#2568)

PR universal-till#2487. Author model Sonnet 5.5; reviewer Fable 5.2 (independent, fresh context, batch review 2026-11-19); tester Sonnet 5.5.
Pre-flight: review record — this file · help — `web/help/en/till-designer.md` updated and screenshots regenerated on Linux (Docker, `make docs-shots`, 200 passed), `guard-docs-shots` green · i18n/i18n-meta/rtl guards pass · data-access guard pass · reference n/a · ADR n/a.

## What shipped
The Designer's category row gets a link to `/categories?edit=<id>` where the category image is edited; the categories page opens that editor automatically.

## Findings
- Code APPROVE. TDD re-verified by reviewer and tester: removing the `designer-cat-image-link` element from `web/ui/partials/buttons.html` turns `TestDesignerCategories_ImageLink_2568` red (row lacks href, id and testid); restored, green.
- B1 (blocker, fixed): `guard-docs-shots` failed because the till-designer screenshot hash was stale and `make docs-shots` needs the Linux font pin. Regenerated in the `ut-docshots` Linux image; designer and till-designer PNGs (4 locales) plus the manifest committed; after merging main the manifest hash was recomputed with `write-manifest.js` and the guard passes. Other PNGs the run re-rendered were byte-noise and deliberately not committed.
- Not run locally: Playwright e2e (`designer-category-image-link-2568.spec.ts`, the only coverage of the `?edit=` auto-open JS) and tablet/RTL/dark visual checks; left to CI `playwright` and `webkit-phone`.

## Verification
`go test ./internal/pages -run 'Categor|Designer|2568|2699'` pass; guards i18n, i18n-meta, rtl, help-topics, data-access, docs-shots pass; CI e2e on the head decides the rest.
Verdict: APPROVE (code); screenshot blocker fixed.
