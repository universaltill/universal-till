# UI / E2E suite (Playwright)

Real-browser tests against a REAL till: `playwright.config.ts` splits
specs across projects, each against its own server(s):
- **default** (`UT_AUTH=off`) — throwaway data dir, demo catalog, auth
  bypassed so specs drive the operator UI directly. Every spec except the
  ones the other projects claim. Since ut-docs#2345 this project runs its
  ~134 files in PARALLEL, and **every worker boots its OWN till** on port
  `9091 + parallelIndex` (`tests/worker-till.ts`, driven by the
  `workerServerURL` fixture in `tests/fixtures.ts`; the binary is built
  once per run by `global-setup.ts` into the gitignored `e2e/.bin/`).
  There is no `webServer` entry and no fixed port for it any more — a
  spec that needs its own URL takes the `baseURL` fixture, never a
  literal.
- **auth** (`run-till-auth.sh`, port 8092, auth ON) — a genuinely fresh
  install with no operator PINs set yet, so `login.spec.ts` and its 4
  sibling specs (nav-rail lock/icons, session-expiry x2) can drive the
  real first-boot wizard and PIN login/lockout instead of bypassing auth.
- **ai-identify** (8093), **layout** (8094), **diagnostics** (8095) — one
  static server each, 1 spec file each; see `playwright.config.ts`.

`run-till.sh` (port 8091) is what the docs-shots harness
(`playwright.docs.config.ts`) still boots; the per-worker till reproduces
the same steps in-process. Every one of these builds the binary and runs
it FROM INSIDE the fresh temp data dir, not the repo root — see the comment
in `run-till.sh` for why: `go run` ties the process's CWD to the module
root, and the app's one-time
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
    npx playwright test                    # headless, all 5 projects
    npx playwright test --headed           # watch it
    npx playwright test --project=default  # skip the auth/setup specs
    npx playwright test --project=auth     # just login.spec.ts and its siblings

Rules learned the hard way:
- **One till per worker, never one till shared by two workers** — a
  till's basket/settings state is SERVER-side, shared by every spec that
  drives it, so two workers on one server race each other (which is why
  this suite ran at `workers: 1` until ut-docs#2345). `workers` is now
  `4` on CI / `2` locally, and it is safe only because each
  `default`-project worker has its own server. The other four projects
  still share ONE static server each — found live on the first 4-worker
  run: `auth` has 5 spec files, and without a cap Playwright spread them
  across several workers against the same shared till, so a nav-rail spec
  could complete the first-boot wizard while `login.spec.ts` (elsewhere,
  file-sort order matters — see its own header comment) was still
  expecting a never-set-up install. Each static-server project therefore
  carries its own `workers: 1` (`STATIC_SERVER_WORKERS` in
  `playwright.config.ts`), which keeps its files sequential in one worker
  exactly as the whole suite used to be — this is NOT "too few files to
  ever be split," it's the cap actively preventing the split. If you add
  a project, give it either its own `e2eWorkerServer: true` (parallel,
  per-worker server) or keep it capped at `workers: 1` like the other
  four — never a shared static server with no cap.
- Locally, a till already listening on a worker's port (9091, 9092, …)
  is REUSED, same as `reuseExistingServer` for the static servers — handy
  for a single-spec loop, but kill it if a spec starts failing on state
  it didn't create. On CI a busy port is an error instead.
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
