# Review: setup wizard reads country_settings (ut-docs#660)

**Date**: 2026-08-13
**Card**: universaltill/ut-docs#660 — "Setup wizard: read country defaults from country_settings instead of the hardcoded slice"
**Complexity**: medium
**Reviewer model**: fresh-context Opus subagent, worktree-isolated (per this card's `complexity:medium` tier — see `scrum-master` skill's model routing)

## What shipped

- `internal/pages/setup_page.go`: removed the hardcoded `setupCountries` slice.
  Added `wizardCountries(ctx, db)` (reads `data.CountrySettingsRepo.List()`,
  rounds `TaxRateBP` to the nearest whole percent, keeps `OTHER` sorted
  last) and `builtinSetupCountries()` (the same shape from
  `data.BuiltinCountryDefaults()`, for the fallback below), both funnelled
  through a shared `countrySettingsToSetupCountries` so the two paths can't
  drift on rounding/ordering.
- `internal/pages/setup_detect.go`: `detectCountry(codes []string) string`
  — takes the caller's live country list instead of reading a package var,
  so an admin-added/deleted country changes what's detectable.
- `web/ui/pages/setup.html`: an operator-added country with no seeded
  `NameKey` falls back to its raw code as the option label.
- `internal/pages/setup_country_drift_test.go` deleted per its own header
  instruction ("when #660 removes setupCountries, delete this file with it").
- `internal/pages/setup_detect_test.go`: `TestDetectCountry` updated for
  the new signature.
- `internal/pages/setup_page_test.go` / `auth_page_test.go`: both test
  fixtures that register the setup wizard now seed `country_settings` by
  executing the real `internal/db/migrations/041_country_settings.sql`
  file (not hand-rolled DDL — the tester skill's standing rule against
  schema drift). Three new TDD tests: an admin-added country renders
  (code-as-label fallback), an admin-edited country's tax rate reaches the
  wizard's prefill, and (added during review triage, see below) a
  `country_settings` read failure degrades to builtin defaults instead of
  taking down first boot.
- `web/help/en/country-settings.md`: corrected the "not yet connected to
  your own shop" claim (now half-true post-#660) and, post-review, the
  "half-percent rates are fine" claim (true for storage, not for the
  wizard's prefill). tr/fa/ar translations of both passages need the NAS
  Ollama pipeline, unreachable from this cloud session — filed as
  **ut-docs#684** (`blocked:env`), not silently skipped.

TDD-first throughout, including the two fixes made during review triage
(see below) — each new/changed test confirmed failing against the
pre-fix code (a real behavioural failure, not just a compile error — see
the independent re-verification below), then passing after.

## Independent review (fresh-context Opus, worktree-isolated)

**Verdict: safe to merge, no blocking issues.** Full findings, all
addressed or accepted as noted:

**Independent TDD re-verification** went beyond the commit's own claim:
rather than just confirming a build break, the reviewer isolated each new
test individually (reverting only the template change, then temporarily
swapping `wizardCountries`' data source) and confirmed each fails with the
*specific* assertion message it claims to guard, not just a compile error.
Also independently ran the full `-race` suite for the affected packages
(`internal/pages`, `internal/data`, `internal/db`) against a clean tree.

**Rounding correctness** (`(bp + 50) / 100`, round-half-up): verified
correct arithmetic including exact-half and the 100%-boundary case, and
confirmed the domain is safely bounded by `CountrySettingsRepo`'s own
`0 ≤ bp ≤ 10000` validation. The reviewer's own judgment on the *tradeoff*
(not the arithmetic) is what produced finding **N1** below — #659
deliberately engineered `country_settings` to preserve fractional
percent (`step="0.01"` in the admin UI, a comment there about avoiding a
float-truncation bug), and #660 silently discards that precision on the
read side. Well-disclosed to developers in the code comment; **not**
disclosed to the shop owner in the manual until N1's fix.

**`detectCountry`/`OTHER`-exclusion/timezone-map**: confirmed both call
sites updated, the `OTHER`-exclusion contract upheld by two independent
paths, and — the sharper question — that `setupTimezoneCountry`'s static
map still resolves correctly now that `detectCountry` validates against a
*live* list rather than trusting the map unconditionally. Traced to why:
`CountrySettingsRepo.Delete` restores a builtin to its shipped defaults
instead of removing the row, so none of the 13 timezone-mapped builtins
can ever actually go missing at runtime. Flagged (informational) that this
safety currently rests entirely on that restore-not-delete semantic, with
no test pinning the coupling.

**Test-fixture blast radius**: independently grepped for every
`registerSetup(mux, ...)` fixture rather than trusting the diff — found
the same three this cycle identified (`newAuthTestMux`, `newFullAuthDeps`
updated; `demo_seed_opt_in_test.go`'s `newRealDBDeps` correctly left alone,
since it already uses `db.Open`'s real embedded migrations). Confirmed the
041 migration is FK-free and idempotent, safe to layer onto hand-rolled
schemas in either order.

**Manual accuracy**: verified each of the three post-#660 claims in
`country-settings.md` against the code directly (not the commit message)
— including grepping every real reader of `ArchiveMinDays` to confirm
retention enforcement genuinely isn't wired yet. All accurate. Confirmed
the tr/fa/ar files are unchanged (not silently missing), consistent with
the ut-docs#684 deferral.

**Visual verification**: engaged directly with the "no CSS/layout change,
verified by exact-HTML-fragment assertion" reasoning for skipping a
screenshot — agreed with the *outcome* but flagged that the stated
reasoning missed a real, user-visible change: country option order moved
from a hand-curated list to `country_settings`' alphabetical-by-code
order (verified empirically, e.g. GB moves from 1st to 5th). This is
disclosed in the code's own comment as a deliberate, minor change, and is
functionally harmless (placeholder stays the default), but the "no visual
change" framing understated it. Recorded as **N3**, not fixed — the
alternative (restoring curated order) needs a display-order concept
#659/#660 deliberately didn't add.

## Fixed during review triage

- **N1 (manual now contradicts code, in a file this diff already
  touches)**: `country-settings.md`'s "half-percent rates like 8.5 are
  fine" is only true for storage — the wizard prefill rounds to whole
  percent. Fixed: appended the clarification in the same bullet.
- **N2 (first boot could hard-fail on a transient DB read, unlike every
  other failure in this same handler)**: `renderWizard` now falls back to
  `builtinSetupCountries()` (the same values the pre-#660 hardcoded slice
  had) and logs, instead of `http.Error(500)`, matching this file's own
  established "never block first boot" posture (locale persist, restore
  prompt, plugin install, demo seed all already do this). New TDD test
  (`TestSetupWizardCountrySettingsReadFailureFallsBackToBuiltins`)
  confirmed failing (500) against the pre-fix code, passing after.

Both fixes re-ran the full local gate (see below) — no second review
round: neither is blocker-class (money/tax-incorrect, data loss,
security), both are cheap, in-file, and directly actionable from the
reviewer's own report, per the pipeline's "earn a second round" rule.

## Not fixed — accepted as noted

- **N3** — country dropdown order changed (hand-curated → alphabetical).
  Deliberate, disclosed in-code, functionally harmless. Restoring curated
  order is out of scope (no display-order field, and #660's own non-goals
  exclude new per-country fields).
- **N4** — the test-fixture helper's "can't drift" comment overstates
  itself slightly (true for this one migration, not proof against a
  *future* migration altering the table). Fails loudly if that ever
  happens, and `TestBuiltinDefaultsMatchMigrationSeed` already guards the
  adjacent Go-vs-migration seed. Low value for the churn of a fix.
- **N5** — `{{ if .NameKey }}...{{ else }}...{{ end }}` confirmed valid
  and unreachable-for-empty-Code by three independent validation layers.
  No action.

## Safe-to-merge

Yes. Full gate green: `go build ./...`, `go vet ./...`, `gofmt`,
`go test ./internal/pages/...`, `go test ./... -race` (whole repo),
`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
`guard-help-topics.sh`, `guard-plugin-menu-read.sh` — all clean, re-run
after the N1/N2 fixes.

## Explicitly deferred

- universaltill/ut-docs#684 — tr/fa/ar translations of the two English
  passages this card and its review edited in `country-settings.md`
  (needs NAS Ollama, unreachable from this cloud session).
