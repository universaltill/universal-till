# Code review — bare first-boot setup names the shop (ut-docs#3116)

**Date:** 2026-09-29 · **Lane:** cloud-54 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

`POST /api/auth/setup`, the bare first-boot fallback kept next to the guided
`/setup` wizard, finished first boot without a shop name, so the till kept
migration 001's seeded `store.name = "My Store"` — which #3096 already refuses
in the wizard.

- `login.html`'s first-boot form gets a shop-name field (existing keys
  `setup.store.title` / `setup.store.placeholder`; no new locale keys, so no
  language-pack follow-ups). Autofocus moves to it.
- The handler refuses a blank, placeholder, or too-long name with
  `setup.error.store_name_required`, after the PIN checks and **before any
  write**, and persists the trimmed name after the admin PIN is set.
- A PIN error re-renders with the typed name; a refused name is not echoed.
- Shared helper `isRefusedStoreName` (setup_page.go) now serves the wizard and
  the fallback, and adds a server-side 60-rune cap matching the inputs'
  `maxlength="60"`.
- Existing tests that use the fallback as their first-boot helper now send
  `store_name`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No server-side length cap on `store_name` (client `maxlength` only), pre-existing in the wizard | Fixed: 60-rune cap in the shared helper, test case added |
| 2 | minor (pre-existing) | `d.Cfg.StoreName` isn't refreshed after first boot, so the self-order header shows the boot default until restart — same in the wizard | Already tracked as ut-docs#3020 |
| 3 | nit | `store.name` was written before the PIN, so a failing PIN write left a name behind | Fixed: written after `SetUserPIN` |
| 4 | nit | Nothing pinned that a refused name isn't echoed back | Fixed: test asserts `value=""` on every refused case |
| 5 | nit | Wizard behaviour after helper extraction | Verified identical (it already trimmed); wizard tests green |

Also added during build: the `settings-write:allow` annotation the
ut-docs#2979 settings-write guard requires, with the same first-boot reason
`setup_page.go` is allow-listed for.

## Verified

- TDD: `TestBareFirstBootSetupRequiresShopName` failed on the old handler
  (`code=303`, first boot completed with no name) and passes with the fix;
  it checks every refused case stays first boot with no PIN set.
- Reviewer probes: the echoed name is attribute-escaped; `GET /login` on a
  first-boot till still redirects to `/setup`; `POST /api/auth/login` on a
  first-boot till renders the form with an empty name. No e2e helper posts
  to the bare route (all use the wizard).
- Full gate: gofmt, build, vet, `go test ./...`, golangci-lint, CI guards.
- docs-shots: the first-boot login form is not screenshotted (`/login` is
  not any topic's `routes[0]`), so only `surface_sha256` was refreshed
  (`Docs-Shots-Unchanged: true`). The user manual already states the shop
  name rule (`web/help/en/users.md` step 4); this route is not reachable from
  normal UI, so no help change.

**Verdict:** safe to merge.
