# Review — core-neutral guard cites ADR-0121 §11; ADR-0067/0068 notes (ut-docs#3193)

**Card:** ut-docs#3193 · **Author:** Sonnet 5.5 (lane:cloud-24, complexity:easy) ·
**Reviewer:** Opus 5.5 (independent subagent, fresh context)
**Branches:** universal-till `fix/3193-core-neutral-adr-cite`, ut-docs `docs/3193-adr-0067-0068-notes`

## What shipped

- universal-till: `scripts/ci/coreneutral/main.go:2`, `scripts/ci/guard-core-neutral.sh:3`
  and `CLAUDE.md`'s "Core neutrality" heading cited ADR-0119 (per-till visual
  effects) for the core-neutral rule. They now cite ADR-0121 §11 ("CI guard"),
  and the heading drops "proposed" (ADR-0121 is Accepted, 2026-09-28).
- ut-docs: ADR-0067 (retracted) and ADR-0068 (accepted) each gain a dated
  note. The `setupBasePlugins` country table they compare against was
  replaced by `basePluginsForCountry`, which derives the pack from
  `country_settings.DefaultLocale` (universal-till#1511, ut-docs#2964). The
  maps that remain (`countryTaxLocale`, `taxRateSwitchMandatedCountries`)
  are allow-listed under their move card #2879. No decision changes, so
  nothing is superseded.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | ADR-0067 note said the ADR described `setupBasePlugins` "as an explicit country list". It only cites the mechanism; the explicit list it describes is the mandated-tax `{"DE":"de"}` map (`countryTaxLocale`) | Fixed: note reworded and names `countryTaxLocale` |
| 2 | minor | "core never names a country" overstated; `countryTaxLocale` is still in core, allow-listed (#2879) | Fixed: "only through that shrink-only allow-list (ADR-0121 §11)" |
| 3 | nit | Heading levels differ (`##` in 0067 after the retracted text, `###` under Consequences in 0068); 0067's note sits below "not current guidance" | Note now opens "This ADR stays retracted"; heading levels kept (0067's note is not part of the retracted text) |
| 4 | nit | Review record missing | This file |

Reviewer also checked: no other core-neutral ADR-0119 mis-cite remains in
either repo (remaining ADR-0119 hits are visual effects/motion, plus the
historical 2026-09-28 review record that documents this mis-cite). The
second comment on the card (CLAUDE.md / `reference/translation.md` listing
packs as `{de,es}`) was already fixed: CLAUDE.md points at `PACKS`, and
translation.md says `{de,es,…}`.

## Verified

- `gofmt -l scripts/ci/coreneutral` (empty), `go vet` / `go build -o /dev/null ./scripts/ci/coreneutral/`,
  `bash scripts/ci/guard-core-neutral.sh` (569 files, 24 allow-list entries, no offender),
  `scripts/ci/guard-core-neutral_test.sh`.
- ut-docs `scripts/guard-adr-index.sh` → ok.
- Comment/doc-only: no runtime behaviour, UI, locale or help change.

**Verdict:** safe to merge.
