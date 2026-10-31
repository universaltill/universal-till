# README + CONTRIBUTING: invite people to translate in Weblate (part of ut-docs#4367)

Author model: Sonnet 5.5. Docs-only change, no code, no runtime effect.

## What changed
- README: a visible "Help translate" section before "What is Universal Till?"
  linking the public Weblate engage page (the badge alone did not say what to do).
  The old "see CONTRIBUTING.md" line now points to Weblate first.
- CONTRIBUTING.md Translations: replaced the stale "open a PR for a
  ut-plugin-language-<code> pack" with the Weblate route; notes legal wording
  is person-reviewed and old packs are being replaced.

## Verified
`translate.universaltill.com/engage/universal-till/` returns 200 from the public internet.

## Exposes
Nothing new: the page is already public. Anyone can register and suggest;
nothing reaches tills without approval and the signed bundle build.

## Findings
Reviewed by the author only (docs-only, a reviewer agent would cost more than the change). No findings.

## Addendum: remove the old language-plugin links
Owner pointed out README still linked the three `ut-plugin-language-*` repos
(marketplace table, Internationalization section, roadmap). Those are being
retired (ut-docs#5189): languages now ship as Weblate-translated signed bundles
(ADR-0160). Rows and links removed; the section now says German is live and
others follow once approved. Still true in code: the bundle installs under the
same plugin id, so nothing in the product flow changed.
