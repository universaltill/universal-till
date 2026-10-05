# Review: retired-name clash refusal names the retired location, till side (ut-docs#3652)

- Date: 2026-10-05
- Branch: `fix/3652-stock-location-retired-clash`
- Author: Opus 5.5 (lane). Reviewer: Fable 5.1 (independent, different model).
- **Authoritative record:** ut-my-shop
  `docs/code-reviews/2026-10-05-stock-location-retired-clash-3652.md` (same branch name) — the
  user-facing change lands there; this file only covers the till-side part.

## What shipped here
- `internal/pages/cloudsync_wire.go`: `stockLocationRefusalf` (fmt-formatted refusal in the
  /locations page's own words); `cloudCreateStockLocation` / `cloudRenameStockLocation` refuse a
  clash with a **retired** location via `locations.error.create_retired` / `rename_retired`, naming
  the stored location (`l.Name`); an **active** clash is unchanged (create: idempotent no-op success;
  rename: generic `locations.error.rename`).
- `internal/pages/cloudsync_stock_locations_3383_test.go`: expects the parameterised text for the
  retired case and the generic text for the active case, in both cases with zero writes.
- `web/locales/{ar,en,fa,tr}.json`: the two keys. Brand-new keys → pack PRs in
  ut-plugin-language-{de,es,pt} land in the same cycle (`lang-pack-drift` red on `main` in between).

## Reviewer verification
- `go build ./... && go vet ./... && go test ./internal/pages/... ./internal/cloudsync/...`,
  `scripts/ci/guard-i18n.sh` (verb parity across locales included), compliance-claims and
  competitor-naming guards: green.
- TDD re-verified in an isolated worktree: en.json keys reverted → both tests fail for the right
  reason (raw key echoed / "has no English text"); restored → pass; diff byte-identical.
- No file write, no cwd-relative path, no SQL outside `internal/data`, no money.
- Finding (fixed): ar/fa/tr said "retired" in the pensioner sense; reworded to the inactive-state
  wording their neighbouring `locations.*` keys use.
- Deferred (Backlog card): the till's own `/locations` POST handlers still map a retired clash to
  the generic `locations.error.create` / `rename`.
- Help manual: unchanged on purpose — the `/locations` page behaviour and `web/help/en/inventory.md`
  are untouched; the new text surfaces only as a directive refusal shown in my.

## Verdict
Safe to merge. Part of ut-docs#3652.
