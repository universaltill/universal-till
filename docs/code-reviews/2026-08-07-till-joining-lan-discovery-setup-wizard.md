# Till-joining: LAN discovery + approve-to-pair on the setup wizard (ut-docs#289)

## What shipped

The first-boot setup wizard's "Join an existing shop" step (`/setup`, step
99) gains LAN discovery + approve-to-pair alongside its existing manual
paste-a-code form. Previously this mechanism (ADR-0033 parts 1–3) only
existed on the `/tills` page of an *already-configured, logged-in* till —
per a 2026-08-06 product-owner scope correction, that was backwards: the
device that actually needs discovery is the brand-new till, which has no
session yet.

- Three new routes, first-boot-gated instead of manager-gated:
  `GET /api/setup/discover-primaries`, `POST /api/setup/pair-start`,
  `GET /api/setup/pair-status` — mirroring the existing
  `/api/sync/discover-primaries` / `/api/sync/pair-start` /
  `/api/sync/pair-status` trio used by the Tills page.
- `internal/pages/api_gates.go` (new): an `apiGate` abstraction
  (`managerGate`, `firstBootGate`, `rateLimited`) so `discovery_api.go` and
  `pairing_join.go` share one handler body per route between the two
  authorization flavours — no duplicated business logic.
- `internal/auth/middleware.go`: the three new `/api/setup/*` paths added to
  the exact-path exemption list (they're unreachable otherwise — a
  first-boot till has no session).
- All four `pair-start`/`pair-status` routes (2 manager-gated + 2
  first-boot-gated) share one `*replicaPairing` instance — a till only
  ever has one outbound pairing attempt in flight, by design.
- `web/ui/partials/pairing_wait.html` / `pairWaitView`: the polling
  `hx-get` target is now parameterized per flavour (`statusURL`) — a
  first-boot till polling the manager-gated `/api/sync/pair-status` would
  401 forever and the wizard would hang on "waiting".
- `web/ui/pages/setup.html`: new discovery button/results list/status area
  in step 99, with its own inline `<script>` written from scratch against
  `/api/setup/*` (not copy-pasted from `tills.html`, which has a real,
  separately-filed syntax bug — ut-docs#373 — that this diff does not
  reproduce).
- i18n: one new key (`setup.join.discover_help`) across all four locales;
  everything else reuses the existing `tills.discovery.*` / `tills.pairing.*`
  namespace.
- `web/help/{en,ar,fa,tr}/multitill.md` updated to document both paths;
  `README.md` updated; `make docs-shots` regenerated (all 44
  topic×locale screenshots, `web/help/img/manifest.json` refreshed).

## Independent review

Fresh-context Opus subagent (complexity:hard card → Fable built, Opus
reviewed, per this pipeline's model routing — deliberately not Fable
reviewing its own work). It ran the full gate itself (not just read the
diff): build/vet/gofmt, `go test ./...` (incl. `-race`), all CI guard
scripts, `node --check` on both inline scripts (with a control run
reproducing ut-docs#373 on `tills.html` to prove the method actually
catches this bug class), the `git stash` re-verification of the
security-critical gate, and 5 independent code mutations (each killed its
intended test, each reverted). Verdict: **safe to merge on correctness and
security grounds**. Findings:

- **Blocker (fixed): `guard-docs-shots.sh` red.** The sandbox this cycle
  ran in has only `chromium-1194` pre-installed, not the
  `chromium_headless_shell` build this repo's pinned Playwright version
  wants by default — `make docs-shots` failed silently in Dev's own
  sandbox for the same reason. Fixed by temporarily pointing
  `e2e/playwright.docs.config.ts` at the pre-installed binary
  (`launchOptions.executablePath`), running the real screenshot suite
  (44/44 passed), writing the manifest, then reverting the config edit —
  the committed config is unchanged from `main`, only the screenshots and
  manifest are new.
- **Should-fix (fixed): no rate limiter on the new unauthenticated
  routes.** `discover-primaries` triggers a 4s mDNS multicast scan per
  request; `pair-start` is an outbound-HTTP primitive to any
  attacker-chosen host (`validPrimaryBaseURL` only checks scheme+host, and
  dial errors are reflected back verbatim) — both reachable by any LAN
  host during the first-boot window, unlike their manager-gated siblings.
  Fixed by reusing `pairing_api.go`'s existing `pairRateLimiter`/`sourceOf`
  (1/min per source, cap 5) via the new `rateLimited` gate wrapper, applied
  to both new routes. `pair-status` deliberately stays unlimited — it's the
  wizard's own legitimate 15s self-poll, not an attacker-chosen outbound
  call. New regression tests
  (`TestSetupDiscoverPrimariesRateLimited`,
  `TestSetupPairStartRateLimited`) — both independently mutation-tested
  (widened the cap to 5000, watched both fail, restored, watched both
  pass).
- **Should-fix (fixed): two identically-labelled "This till's name"
  inputs on the same screen with no disambiguating heading.** `tills.html`
  has the same duplication but disambiguates with a card `<h3>` per
  section; `setup.html` step 99 had only an `<hr>` between them. Added
  `<h3>{{ T "tills.discovery.title" }}</h3>` (an existing i18n key, no new
  string) above the discovery half. Re-verified visually (screenshot
  below).
- **Should-fix (filed as a follow-up, not fixed here — cross-repo, out of
  this diff's scope): `check-lang-pack-drift.sh` flips `de` clean→red.**
  The one new i18n key isn't in `ut-plugin-language-de`'s pack (a separate
  repo this session can't write to). Doesn't block this PR's own CI (that
  guard runs on `push:main` in its own workflow, not on pull requests) but
  leaves `main` red on that check until addressed. Filed:
  universaltill/ut-docs#375.
- **Nitpicks, accepted as-is (pre-existing pattern, not a regression this
  diff introduces):** the first-boot gate's refusal is a plain 303 rather
  than an `HX-Redirect` (mirrors `/api/setup/join`'s own existing gate,
  `sync_api.go:406`, which has the identical property); the `name` input's
  `placeholder="Till 2"` is hardcoded (mirrors `tills.html` and the
  untouched manual-code form's own `name` input on the same page);
  `pairing_wait.html`'s error strings are hardcoded English (unchanged
  behaviour from the existing manager-gated flow, now also reachable from
  a non-English wizard session — a real but pre-existing gap, not
  introduced here).

## Verified beyond automated tests

- **Real browser, twice** (the pre-installed `/opt/pw-browsers/chromium-1194`
  binary via `playwright-core`, since the project's own pinned Playwright
  browser version isn't available in this sandbox — the project's own
  `e2e`/`docs-shots` suites still run against the pinned version in real
  CI): confirmed step 99 renders both forms without breakage in English,
  confirmed the discovery button performs a real mDNS scan and returns a
  real, actionable "Request to pair" result, confirmed fa/RTL renders with
  correct mirroring, and re-confirmed after the F4 heading fix that the
  two "This till's name" fields are now disambiguated.
- **`make docs-shots` run for real** (not skipped) — see Blocker above.
- Full local gate re-run after every fix round: `go build ./...`,
  `go vet ./...`, `gofmt -l` (touched files clean; 4 unrelated files have
  pre-existing drift on `main`), `go test ./...` (green except
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, a known
  root-sandbox artifact reproducing identically on `main` before this
  diff — confirmed via `git stash`, and confirmed it shares no dependency
  on `internal/pages`/`internal/auth`), `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, `guard-htmx-loaded.sh`,
  `guard-docs-shots.sh` — all green.
- TDD claim on the security-critical gate independently re-verified twice
  (orchestrator, then again by the reviewer): `git stash push --
  internal/auth/middleware.go`, all four `TestSetup*FirstBootGate` /
  `TestSetupPairStartShowsCodeAndPollsSetupStatus` tests fail with 401,
  `git stash pop` restores green.

## Explicitly deferred

- The `ut-plugin-language-de` translation gap (universaltill/ut-docs#375).
- A real WebView2/desktop-shell walkthrough of the new pairing flow — this
  card is server-rendered HTMX, verified via a real (non-Windows) browser
  and the existing Go/HTTP test suite; no desktop-shell-specific behavior
  was introduced.
- `tills.html`'s own pre-existing, unrelated JS syntax bug
  (universaltill/ut-docs#373) — confirmed not reproduced by this diff's new
  script, not this diff's job to fix.

## Safe to merge

Yes.
