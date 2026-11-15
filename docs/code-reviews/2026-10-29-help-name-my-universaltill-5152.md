# Review — help: name my.universaltill.com instead of "the shop's cloud manage page" (ut-docs#5152)

Date: 2026-10-29 · Branch: `fix/5152-help-name-my-universaltill` · Lane: `lane:cloud-41`

## What shipped

Follow-up to ut-docs#4490 (which renamed "the shop's cloud manager" in
`sell.md`/`multitill.md`). The same unnamed wording remained as "the shop's
cloud manage page" in four topics. "Cloud manager" is now the Shop-tier
ADR-0162 feature, so every such sentence now names my.universaltill.com.
Prose only, 1:1 line swaps:

- `web/help/{en,de,tr,fa,ar}/claim.md` — the plan-gating sentence ("keeping the
  till in sync with your online account … is part of the paid plans") now says
  "changes made on my.universaltill.com". Still true for every paid plan: these
  are my. directive edits, not the ADR-0162 Cloud manager, so no tier is implied.
- `display.md` (shop rename to/from my.), `catalog.md` (item photo),
  `categories.md` (category order, category photo, hide + icon) — same rename.
- de/fa/ar `categories.md` lines 20/31 used a second variant ("Verwaltungsseite
  des Shops in der Cloud", "صفحهٔ مدیریت فروشگاه در (فضای) ابر",
  "صفحة إدارة المتجر في السحابة") — renamed too, for consistency.
- Grammar after the noun "page" became a domain: de relative clause
  ("…, die einer Kategorie …") rewritten as "; dort lässt sich …"; ar "والتي
  يمكنها" → "حيث يمكن"; tr vowel harmony "üzerinden de".

No sentence in these topics uses "Cloud manager"; it stays reserved for the
ADR-0162 feature.

## Review

Independent review by a different model (Sonnet; the change was authored by Opus).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `categories.md` line 31 still pointed back at "the cloud page" (en "from the cloud page", de "von der Cloud-Seite", fa "صفحهٔ ابری", ar "صفحة السحابة", tr "o sayfa … / buluttan") after the first mention became a domain | Fixed: en/fa/ar name my.universaltill.com; de "ein dort vergebenes Symbol"; tr "orada … verilebilir" / "orada verilen" |
| 2 | minor | "that page" back-references on `categories.md` line 20 (all locales) | Accepted: still reads correctly (a page of that site) |
| 3 | nit | fa line 31 "که" now has a domain as antecedent | Accepted: readable |
| 4 | nit | Bare Latin domain inside ar/fa RTL prose, no LRI/PDI isolates | Accepted: matches the #4490 precedent (1594ed9) |
| 5 | nit | tr sentence-initial lower-case domain | Accepted: matches the precedent |
| — | — | Diff is 1:1 line swaps; meaning kept; claim.md plan-gating sentence still true and implies no tier | — |

## Verified

- `guard-help-drift.sh`, `guard-help-topics.sh`, `guard-help-breadcrumbs.sh`,
  `guard-compliance-claims.sh`, `guard-competitor-naming.sh`,
  `guard-mail-addresses.sh`: pass.
- `go test` for `internal/manual`, `internal/httpx`, `internal/pages/common`,
  `internal/pages/catalog`: ok. `internal/plugins` hit the local 10-minute default
  timeout (no help dependency); left to CI, as for #4490.
- `make docs-shots`: no PNG changed; only `web/help/img/manifest.json` topic hashes for
  catalog, categories and display moved. The categories hashes were refreshed again
  (sha256 of each locale's markdown, as the guard defines them) after the review fix;
  `scripts/ci/guard-docs-shots.sh` passes locally.

Verdict: safe to merge.
