# Real browser E2E: login, inventory-to-till, catalog-image-to-till

2026-07-29

Farshid asked for real UI-level tests exercising full user flows: "login
to the till, go to the inventory, add some inventory and go to the till
page and sell them" and "go to the catalog upload some images and go to
the till and see if they are there." Everything covered in this session
up to this point was Go-level unit/integration tests (real SQLite, real
HTTP handlers via `httptest`) — none of it opens a browser or executes
client-side JS. There's a pre-existing Playwright suite at `e2e/`
(self-managing server, `workers:1`, `watchConsole` console-error gate);
extended it rather than building anything new, per its own README's
established conventions.

## What's new

- **`e2e/tests/login.spec.ts`** — drives the REAL first-boot setup wizard
  (a client-side Alpine.js `x-show` multi-step form: language → country →
  shop name → admin PIN → finish), then PIN login, session-gated page
  access, logout, and wrong-PIN rejection. Runs against a second, auth-ON
  server (`e2e/run-till-auth.sh`, new) via a new Playwright project
  (`playwright.config.ts` now boots two `webServer`s and splits specs
  across `default`/`auth` projects), since the existing suite's server
  runs with `UT_AUTH=off` specifically so other specs can skip login
  entirely.
- **`e2e/tests/inventory-to-till.spec.ts`** — receives stock via the real
  Inventory page form, sells that exact item at the till, confirms the
  stock level dropped by exactly 1 afterward. Uses Pepsi (itm002),
  deliberately not the Coca-Cola item other specs already use.
- **`e2e/tests/catalog-image-to-till.spec.ts`** — uploads a real PNG via
  the Catalog page's image panel, confirms the catalog table's thumbnail
  updates, then scans that item at the till and confirms the SAME
  uploaded image (not a stale/seeded one) renders on the basket line —
  this is the class of bug (wrong write path, stale cache-busting) this
  session already found and fixed twice in the Go layer, but could never
  actually PROVE fixed without a real browser loading the `<img>`.

## Two real bugs found while building this

**1. E2E test-isolation bug (local-dev-only, doesn't affect CI).**
`run-till.sh`/`run-till-auth.sh` ran `go run .` from the repo root. The
app's one-time legacy-data migration (`internal/paths/paths.go`,
`migrateLegacyDB`) looks for `./data/unitill-pos.db` relative to the
process's CWD and copies it into the target data dir if the target
doesn't exist yet — regardless of whether that target was deliberately a
fresh throwaway location. Since `go run` ties the resulting process's CWD
to the module directory (even with `-C`), and the repo root has a real
(gitignored) local dev database with real operator PINs already set,
every "fresh till" the e2e suite booted was silently seeded from that
real database. Confirmed live: a from-scratch install test landed
straight on a PIN-required login screen instead of the setup wizard.

**Fix**: build the binary once, then run the BUILT BINARY (not `go run`)
from inside the fresh temp data dir itself, which has no `data/`
subfolder — the legacy-copy check then correctly finds nothing. Applied
to both `run-till.sh` and `run-till-auth.sh`.

**2. Pre-existing spec bug, unmasked by fixing #1.** Once the till was
genuinely fresh, `sale.spec.ts`'s hardcoded `1.44` total (for a £1.20
item, assuming tax-exclusive pricing) started failing — the actual
default config is tax-**inclusive** (`UT_TAX_INCLUSIVE` defaults to
`"true"` in `internal/config/config.go`), so £1.20 already includes VAT
and is the correct total. The old assertion had only ever passed because
it was riding on the leaked developer database's tax settings. Fixed to
expect `1.20`; checked every other spec for the same dependency —
`rtl.spec.ts` asserts no specific total, and nothing else does either.

## Independent review (opus) — caught a real false-pass in my own new test

Verified both diagnoses above independently (re-read the relevant Go
source, re-derived the tax math, empirically tested the legacy-DB-copy
behavior against a live server). Also caught something I'd missed:
**`catalog-image-to-till.spec.ts`'s original assertions (`naturalWidth !=
0`) were a false pass** — itm003 (Sparkling Water) ships with a real
seeded 289×375 thumbnail baked into the embedded filesystem, so "some
non-zero-width image loaded" would pass even with a completely broken or
no-op upload; the spec would never have caught the exact bug it exists to
catch. The reviewer verified live that the served dimension genuinely
flips 289→2 (my fixture's exact size) only on a real successful upload.
**Fixed**: assert the exact fixture dimension (`naturalWidth: 2`) instead
of "non-zero," both on the catalog thumbnail and the basket line image.
Reran the full suite after the fix — still 18/18 green, now for the right
reason.

Also confirmed: all new selectors match real template markup (not
guesses that happened to pass), the PIN-pad digit-clicking loop correctly
disambiguates 0-9 from "Clear"/submit even though 1-9 render via an
Alpine `x-for`/`x-text` template, and `catalog-image-to-till.spec.ts`
leaves the shared server in a clean state for whatever spec runs next.

## Verification

`cd e2e && npx playwright test` (both projects, 18 specs) — run clean 4+
times across this session (3 by me, 2 more by the reviewer in a separate
environment), zero flakiness, ~10s total. `go build ./...`, `go test
./...`, both CI guards — unaffected (no Go source changed), all still
pass.

## Scope not covered here

This is a first pass at real browser E2E, not full coverage of it. Not
covered: shift open/close, refunds, sync, receipt printing, and most of
`internal/pages`'s ~30 handler files have no browser-level coverage at
all (only unit/integration coverage from earlier in this session, on the
3 highest-risk files). The existing `tests/e2e/` directory (a separate,
older Playwright suite requiring manual server setup, ATDD-style,
covering marketplace/plugin/docs flows) was surveyed but not touched or
consolidated with this one — worth a follow-up conversation with Farshid
about whether to merge or retire it.
