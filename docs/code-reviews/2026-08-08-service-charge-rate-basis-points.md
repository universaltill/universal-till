# 2026-08-08 — Service charge rate basis-points fix (ut-docs#244)

## What shipped

`RuntimeState.ServiceChargeRatePct` (a whole-percent `int`) is retyped to
`ServiceChargeRateBasisPoints` (basis points, 1bp = 0.01%), so the standard
UK restaurant service-charge rate — 12.5% — can finally be configured and
computed exactly, instead of truncating to 12% or 13%.

- `internal/pages/common/deps.go`: field rename + doc comment.
- `internal/pages/common/state.go`: new exported helpers
  `ParseServiceChargeRateBasisPoints`/`FormatServiceChargeRatePercent`
  parse/format the *same* persisted settings key
  (`store.service_charge_rate_pct`) as a decimal-percent string
  (`strconv.ParseFloat`, half-up round to bp). Fully backward compatible —
  a pre-fix whole-percent value ("12") still parses to 1200bp, unchanged
  behaviour, no DB migration.
- `internal/pages/init.go`, `settings_page.go`, `setup_page.go`,
  `pos_api.go`: removed the `*100` whole-percent→basis-point conversion at
  every call site (6 total) now that the field carries basis points
  directly.
- `internal/pages/settings_page.go`'s `/api/settings/upsert` handler: the
  `KeyServiceChargeRate` case now validates *before* `d.Settings.Set` is
  called, returning `400` on an invalid/negative value instead of silently
  no-op'ing after already persisting the bad string to the settings store
  (the original bug report's second complaint — "no operator feedback").

**Explicitly out of scope**: `TaxRatePct` has the identical whole-percent
limitation but is a separate, existing backlog concern per the issue's own
scoping — not touched here. No dedicated `settings.html` UI field exists
for this setting (only the generic key/value "All settings" editor reaches
it) — confirmed by the reviewer that none existed before or needs adding
now; no manual topic references this key either, so nothing went stale.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → same-model, clean-context
review per the `scrum-master`/`reviewer` skills), `isolation: "worktree"`,
read-only instruction given explicitly, briefed with the exact diff scope
and this repo's `CLAUDE.md` rules.

Ran build/vet/gofmt/tests/guards for real (not just read the diff) — all
clean. Independently re-verified two tests via revert-then-restore
(reverted the parser to the old whole-percent path — confirmed
`TestLoadState_ServiceChargeRateFractionalPercent` fails with
`ServiceChargeRateBasisPoints = 0, want 1250`; reverted the `pos_api.go`
call site to re-introduce the old `*100` double-scaling — confirmed
`TestTenderHandler_QuickTenderCoversFractionalServiceCharge` fails with
`got 1250` instead of `13`; restored both, both pass again).

**Found one real-but-minor gap, fixed in this diff**:
`ParseServiceChargeRateBasisPoints`'s validity check (`err != nil || f < 0`)
didn't reject `NaN`/`+Inf`/`Infinity` — `strconv.ParseFloat` accepts these
as valid floats, and converting a non-finite `float64` to `int` is
undefined behaviour on this architecture (verified: evaluates to
`math.MinInt64`). Practical blast radius was limited —
`pos.ComputeTaxBasisPoints` treats any `rateBasisPoints <= 0` as
"disabled" and never crashed or corrupted a sale total — but it defeated
the exact contract this diff advertises: `/api/settings/upsert` would
have returned `204` and persisted the literal string `"NaN"`/`"Infinity"`
to the settings store while the live engine silently disabled the charge.
Fixed by also rejecting `math.IsNaN(f) || math.IsInf(f, 0)`, TDD'd the
same way (confirmed `NaN`/`Inf`/`Infinity` slip through and land as
`math.MinInt64` against the pre-fix check; `-Inf` was already caught by
the existing `f < 0` guard). Reviewer's other finding (no upper bound on
the rate, e.g. `"1e10"`) is a pre-existing, non-regression gap shared with
`TaxRatePct` — not fixed here, not blocking.

No other findings. Reviewer's verdict: safe to merge.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on every touched file — clean.
- Full `go test ./...` (whole module, not just the touched packages) — clean.
- `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`
  — all pass.
- `grep -rn "ServiceChargeRatePct"` across the repo — zero remaining
  references; fully migrated.
- No visible UI surface *changed* by this fix (no dedicated settings-page
  field exists for this setting) — confirmed by checking
  `web/ui/pages/settings.html` and `web/help/` directly rather than
  assuming. `scripts/ci/guard-docs-shots.sh` still failed CI, though: it
  hashes every non-test `internal/pages/**.go` file as part of the
  manual-screenshot freshness check regardless of whether a given change
  is visible, so this diff's `.go` edits alone were enough to go stale.
  Ran `make docs-shots` for real (Playwright, all 4 locales, 14 routed
  topics, 56 specs) and committed the regenerated screenshots + manifest.
  Only two of the 14 topics' PNGs actually differ byte-for-byte —
  `alerts` and `designer`, both unrelated to this change — because both
  screens bake the live current time into their content (a "recent
  problems" log timestamp and a mock receipt's printed date/time); looked
  at both (en) directly, laid out correctly, no regression. The other 12
  topics' PNGs are byte-identical to before, confirming nothing else
  moved. Did not check every locale/theme/viewport combination for these
  two, since neither is a surface this fix touches — the regeneration was
  needed purely to refresh the guard's hash, not because a screen
  actually changed.
- No real client/shop name used in any new test data.

## Safe-to-merge verdict

Yes. Independent review found one real-but-minor gap; fixed and re-verified
TDD-first in this same diff before merge, not deferred.
