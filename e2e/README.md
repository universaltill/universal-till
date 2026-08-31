# UI / E2E suite (Playwright)

Real-browser tests against a REAL till: `playwright.config.ts` boots TWO
servers and splits specs across two projects:
- **default** (`run-till.sh`, port 8091, `UT_AUTH=off`) — throwaway data
  dir, demo catalog, auth bypassed so specs drive the operator UI
  directly. Every spec except `login.spec.ts`.
- **auth** (`run-till-auth.sh`, port 8092, auth ON) — a genuinely fresh
  install with no operator PINs set yet, so `login.spec.ts` can drive the
  real first-boot wizard and PIN login/lockout instead of bypassing auth.

Both scripts build the binary and run it FROM INSIDE the fresh temp data
dir, not the repo root — see the comment in either script for why: `go
run` ties the process's CWD to the module root, and the app's one-time
legacy-data migration looks for `./data/unitill-pos.db` relative to CWD.
Running from the repo root silently copies a real local dev database
(with real operator PINs already set) into what's supposed to be a
throwaway till, defeating test isolation. Confirmed live 2026-07-29: a
from-scratch install test landed on a login screen with a real operator's
PIN already set, and separately, `sale.spec.ts`'s hardcoded price
assertion (`1.44`, tax-exclusive) turned out to only ever have passed
because it was riding on that leaked developer's tax settings — the
actual default config is tax-INCLUSIVE (`UT_TAX_INCLUSIVE=true`), so a
genuinely fresh till shows `1.20`, not `1.44`. Fixed both the isolation
bug and the now-exposed test.

    cd e2e && npm ci && npx playwright install chromium
    npx playwright test                    # headless, both projects
    npx playwright test --headed           # watch it
    npx playwright test --project=default  # skip the auth/setup specs
    npx playwright test --project=auth     # just login.spec.ts

Rules learned the hard way:
- **workers: 1** — each project's till has basket/settings state shared
  SERVER-side by every spec in that project; parallel workers race each
  other. Kept as a single global setting (not per-project) so the two
  projects' servers are never driven concurrently either.
- A spec that adds basket items must COMPLETE its sale (or explicitly
  remove the line, like `catalog-image-to-till.spec.ts` does); one that
  flips a server setting (OSK) must restore it.
- **Import `test`/`expect` from `./fixtures`, not `'@playwright/test'`
  directly** (ut-docs#1315; `scripts/ci/guard-e2e-fixtures-import.sh`
  enforces this). `fixtures.ts` wraps `test` with an auto fixture that
  resets the shared till's basket once per spec FILE, before that file's
  first test — the backstop for the rule above when a spec forgets it
  (ut-docs#1310: `settings-osk.spec.ts` cancelling its hold-sale dialog
  left a basket item that broke `split-tender-i18n-925.spec.ts`'s fa/RTL
  test, on a completely unrelated later run, purely from alphabetical
  file ordering). It only resets *between* files — a file's own tests
  still see each other's basket state exactly as before, e.g.
  `tender-panel-reachable.spec.ts` holding several sales in a row within
  one test. `login.spec.ts` is the one exception: it drives the separate
  `auth` project against a genuinely fresh, never-set-up till, where a
  basket reset is meaningless and the guard exempts it explicitly.
  **Ordering caveat:** the reset fires before a file's first *test body*
  but *after* a `test.beforeAll` in that same file, if it has one — don't
  seed basket state meant to survive the whole file in `beforeAll`, it
  will be silently wiped.
- `watchConsole(page)` fails a spec on any JS/console error — keep it in
  every spec; it's the layer Go tests can't see.
- Within `login.spec.ts`, the whole flow is ONE `test.describe.serial`
  block sharing a single `page` (via `beforeAll`/`afterAll`), not one
  `page` per `test()` — Playwright gives every `test()` a fresh, cookie-
  less browser context by default, which would make a later step look
  logged-out again even though the server-side session is still valid.
- `login.spec.ts`'s first test expects a genuinely fresh, never-set-up
  till (`/` → `/setup`). Locally, `reuseExistingServer: !CI` means if a
  `run-till-auth.sh` from an earlier session is still listening on 8092
  (already past setup), the test fails against that stale server instead
  of a fresh one — kill it first if this test starts failing for no
  obvious reason (`pkill -f run-till-auth`).
