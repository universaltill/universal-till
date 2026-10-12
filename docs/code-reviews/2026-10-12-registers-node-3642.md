# Review — Registers node in my., till side (ut-docs#3642)

**Date:** 2026-10-12 · **Built by:** Opus 5.5 (`lane:cloud-41`) · **Reviewed by:** Fable 5.1
(independent, different model) · **Branch:** `feat/3642-registers-node`

Companions: ut-cloud#453 and the ut-my-shop branch of the same name. Mirrors ut-docs#3383
(`2026-10-04-stock-locations-node-3383.md`).

## What shipped here
- `internal/cloudsync/cloudsync.go`: the catalog snapshot carries `registers: [{id, name,
  is_active, location_id}]` (every register, `location_id` `""` when unassigned), inside the hashed
  payload so a register-only change re-pushes; a read error omits the field (the cloud keeps its
  copy). New `Hooks.SetRegisterLocation`; `apply()` handles `set_register_location` — blank
  `register_id` → failed; `location_id` must be present (`""` = unassign; non-string → failed).
- `internal/cloudsync/catalog_directives.go`: the type is main-till only and a catalog type.
- `internal/pages/cloudsync_wire.go`: `cloudSetRegisterLocation` — the /registers page's rule
  through the same `POSRepo.SetRegisterLocation`: unknown register or location refused; already
  there → no-op success, no write, no audit (at-least-once safe); a target location that is
  inactive and not the current one is refused; `requirePrimaryDirective` before the write;
  audited `register`/`register_update` as `system`. English result messages, no new locale keys.
- `web/help/img/manifest.json`: surface hash refreshed (no rendered change).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No Tick-level test that a satellite leaves the directive pending. | Accepted — the Tick skip is generic and tested for other types; map membership is pinned. |
| 2 | minor | Active check and write aren't one transaction (a location retired in between). | Accepted — benign: `ResolveStockLocationID` joins on active locations and falls back; the page has no check at all. |
| 3 | nit | Repo errors reach the cloud verbatim. | Accepted — same as every precedent hook; no secrets in SQLite errors. |
| 4 | nit | Whitespace-only `location_id` unassigns. | Accepted — matches the page; ut-cloud refuses it before queuing. |
| 5 | nit | Audit payload nil. | Accepted — same as the page; both would change together. |

## Verified beyond the unit tests
- Reviewer TDD re-verification (5 reverts): inactive refusal, snapshot field, `apply()` case,
  main-till map entries and the primary gate each fail their test with the intended assertion;
  all pass restored.
- No stale cache: all five `ResolveStockLocationID` callers read the DB per sale.
- Gate: `gofmt -l .` clean, `go build ./...`, `golangci-lint` 0 issues, full `go test ./...`
  (91 packages ok), 42/43 `ci.yml` build-job guards pass; `guard-shellcheck-version.sh` and
  `shellcheck` not run — no binary in this container, no shell script changed.

## Verdict
Safe to merge after ut-cloud#453. No UI change on the till; no help change.

## Deferred
None here. my.-side follow-ups: ut-docs#4173.
