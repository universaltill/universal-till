# Code review: LAN till discovery over mDNS (ADR-0033 part 1/3)

**Date:** 2026-08-02
**Scope:** `internal/discovery/` (new package: `discovery.go`,
`advertiser.go`, `browse.go` + tests), `internal/pages/discovery_api.go`
(new), `internal/pages/init.go`, `internal/pages/pairing_api.go`,
`internal/app/app.go`, `web/ui/pages/tills.html`,
`web/locales/{en,ar,fa,tr}.json`, `go.mod`/`go.sum`, `README.md`.
**Trigger:** universaltill/ut-docs#264 — ADR-0033 part 1 was previously
closed "completed" (`#183`) with **zero code**; this is the actual
implementation. Part 2/3 (`#184`) is already merged; part 3 (`#185`,
click-to-select a discovered primary) is still open.
**Reviewer:** independent pass by a different model than the one that ran
BA/Architect/Dev/Tester on this card. Prior-phase self-reports were
treated as unverified claims and re-checked from scratch.

## What shipped

A primary now announces itself on the LAN and a prospective replica can
find it, per ADR-0033 §1 — no static IP, no typed or scanned code.

- **`discovery.TillID`** — settings-backed (`lan_discovery.till_id`),
  get-or-create, uuid v4. One source of truth for the stable per-install
  id, deliberately shared between the mDNS beacon and the pairing
  verification code.
- **`discovery.Advertiser`** — advertises `_unitill-sync._tcp` via
  `github.com/hashicorp/mdns` (pure Go, no CGO, one of the two libraries
  ADR-0033 explicitly sanctions). Advertises **only while primary**, with
  a live 30s role recheck so promote/demote needs no restart. Joins
  `app.go`'s background `WaitGroup`, so `drainBackgroundServices` covers
  its shutdown.
- **`discovery.RoleCheckFromSettings`** — the production role rule
  (empty `sync.primary_url` ⇒ primary/standalone), extracted so it is
  directly covered rather than only exercised through a synthetic
  injected bool.
- **`discovery.Browse`** — bounded, on-demand client lookup. Explicitly
  not an ambient background browser.
- **`GET /api/sync/discover-primaries`** — manager-gated, returns
  read-only candidates.
- **`pairing_api.go`** — `derivedVerificationCode`'s `primaryTillID` now
  comes from `discovery.TillID` instead of the old
  `marketplace.DeviceIDFromConfig` stand-in. This is the real security
  fix of the card: it closes ADR-0033 §8's impersonation loop, which
  `#184` could only stub because no channel carried the primary's id.
- **Tills page** — "Find a primary on this network", read-only results.
  Selecting one is `#185`'s job; its absence is by design, not a gap.

No ADR needed — this implements ADR-0033 as accepted. Nothing here
touches `internal/pos` (the checkout/sale path) or `sync_api.go`.

## Independent review findings

### 1. Goroutine leak in `Browse` on mid-scan cancellation — MEDIUM, fixed

`Browse` fans out two goroutines: the mDNS query and a collector ranging
over the `entries` channel. `entries` was closed **only** on the success
path. On the `ctx.Done()` path it returned without closing, so the
collector stayed blocked on `range entries` **forever** — one permanently
stuck goroutine per abandoned request, for the life of the process.

Triggered by something entirely ordinary: a manager clicks "Find a
primary" and closes or navigates away from the Tills tab mid-scan,
cancelling `r.Context()`. On a POS host that runs for weeks, these
accumulate without bound.

The in-code comment reasoned that the leak was "bounded" — but that
reasoning only covered the `mdns.Query` goroutine (which does self-expire
at its timeout), not the collector, which does not. The existing
`TestBrowse_RespectsAlreadyCancelledContext` never caught this because an
already-cancelled context returns at the guard clause at the top of
`Browse` and never reaches the `select`.

**Fix:** close `entries` in the query goroutine, unconditionally, once
`mdns.Query` returns. Verified safe at the library source level, not
assumed: `hashicorp/mdns@v1.0.5`'s sends to `params.Entries` all happen
inside `client.query`'s own receive loop, which has returned by the time
`Query` returns — so there is no send-on-closed-channel window. (That
was checked precisely because a naive "just close it earlier" fix *would*
have introduced a remotely-triggerable panic.)

**Regression test:** `TestBrowse_DoesNotLeakCollectorGoroutineWhenCancelledMidScan`,
covering the mid-scan cancel path the existing test missed. Confirmed
failing first — `2 goroutines before, 3 still running after the scan
finished` — then passing.

### 2. `Browse` result set unbounded against a flooding responder — LOW, fixed

Discovery is a LAN-open surface: any host can answer and no responder is
authenticated. The collector appended every valid entry with no ceiling,
so a rogue or malfunctioning peer could grow the slice — and the JSON
response built from it — for the whole scan window.

**Fix:** `maxCandidates = 64` (far above any real shop, still a hard
ceiling). The collector keeps *draining* past the cap so the query
goroutine is never blocked on a full channel, but stops accumulating.

**Regression test:** `TestBrowse_CapsCandidatesFromAFloodingResponder`.
Confirmed failing first (returned **640** candidates), then passing.

### 3. Data race in `TestAdvertiser_StartJoinsWaitGroupAndShutsDownOnCancel` — LOW, fixed

The test polled `a.server` **without** holding `a.mu`, while `Start`'s
goroutine writes it under that lock — a genuine data race, confirmed by
`go test -race`. Notably the production code is correct here (`tick`
holds the mutex properly); the defect is test-side only, and the test
even locks correctly two lines later, so this was an oversight rather
than a design problem. CI does not currently run `-race`, so it was not
failing the build — but it is a real race and a latent flake.

**Fix:** read `a.server` through a locked helper in the polling loop.
`go test -race -count=3 ./internal/discovery/` now clean.

### 4. Checks that came back clean

Each of these was probed adversarially and found genuinely sound, not
merely asserted:

- **TXT record carries no secret material.** Verified by reading the
  bytes `txtRecord` actually constructs — `v=1`, `name=<shop name>`,
  `id=<uuid>` — not just by trusting
  `TestAdvertiser_TXTRecordCarriesNoSecrets` (which is itself a real
  allowlist: it rejects any key outside `v`/`name`/`id` and any
  64-hex-char value shaped like the pairing commitment). Matches ADR-0033
  §1 exactly. The mDNS instance name is the till id, also non-secret.
- **Auth gate genuinely matches its neighbours** — not an approximation.
  `/api/sync/discover-primaries` uses `isManagerOrAuthOff` with the same
  403 body as `/api/sync/enroll-token` and `/api/sync/pair-requests`.
  Critically, it is **not** in `internal/auth/middleware.go`'s `exempt`
  list (which is exact-match, and covers only the machine-to-machine
  bearer/token-authed sync endpoints), so it goes through real session
  resolution first. Correct on both halves.
- **First-boot / fresh-DB gating.** With no `sync.primary_url` and no
  `store.name` row, `RoleCheckFromSettings` returns true (advertise) and
  the name falls back to "this shop". That is correct per ADR-0033:
  standalone *is* primary. Parity with `common.Deps.SyncPrimaryURL` was
  checked line-by-line — both do `strings.TrimSpace(settings.Get(...))`
  against the identical key, so the two can't drift in meaning.
- **Advertiser lifecycle across promote/demote churn.** Traced the real
  code: `tick` toggles under the mutex and guards on `server == nil` /
  `!= nil`, so repeated ticks in the same role neither restart nor
  double-shutdown; a failed start leaves `server` nil and is retried next
  tick; `Start` defers `ticker.Stop()` and calls `shutdownIfAdvertising`
  on `ctx.Done()`. No `mdns.Server`, ticker, or goroutine leak found.
- **The two recurring bug classes this pipeline keeps hitting elsewhere
  do not apply**: this feature writes **nothing** to disk (grepped for
  `os.Create`/`os.WriteFile`/`os.OpenFile`/`MkdirAll`/`filepath.Join`
  across the new code — zero hits), so there is no missing `os.MkdirAll`
  and no cwd-relative path that should have been `paths.Data(...)`.
- **Hostile-name rendering.** A hostile advertiser controls the `name=`
  field, and the Tills page renders it client-side. The template's
  `esc()` helper (`textContent` → `innerHTML`) correctly neutralises it,
  and the values land in text position, not attribute position, so the
  absence of quote-escaping is not exploitable. Malformed TXT records
  cannot crash `candidateFromEntry` either — it uses `strings.Cut` and
  skips non-`key=value` fields, and entries with no `id=` are dropped.
- **No real client/shop name as test data, no secret-shaped literals.**
  The only name-like fixture is "Task Runner", which is the product's own
  vendor (`auth.attribution`: "a product of Task Runner Technology LTD")
  — self-referential, not a third-party customer. Grep for
  password/secret/token/api-key/bearer literals: none.
- **Existing QR/manual-code pairing is behaviourally unchanged.**
  `internal/pages/sync_api.go` is not in the diff at all; the enrol-token
  flow, `enrolTokens` store, and `encodeEnrollCode`/`decodeEnrollCode`
  are untouched. ADR-0033 keeps that path as the documented fallback.

## Deferred (real, but out of scope for #264)

Filed rather than silently folded in:

- **universaltill/ut-docs#271** (p3) — `discovery.TillID`'s get-or-create
  is a non-atomic read-then-write; two concurrent callers on a fresh DB
  could persist different uuids, which would desynchronise the beacon id
  from the verification-code id. Very narrow window in practice (the
  advertiser's first tick runs before the HTTP listener accepts), so
  low priority; proper fix is an atomic `INSERT OR IGNORE` + `SELECT`
  repo method in `internal/data`.
- **universaltill/ut-docs#272** (p3) — `hashicorp/mdns` logs to the
  global stdlib logger, bypassing `internal/logging`. On IPv6-less hosts
  (containers, many Pi/kiosk images) every advertise/browse emits
  `[ERR] mdns: Failed to bind to udp6 port: ...`. Observed live during
  this review. Cosmetic — IPv4 discovery works fine — but `[ERR]`-tagged
  noise reads as a real fault in field logs.

## Verification performed (beyond reading the diff)

**Full gate, re-run personally after the fixes:**

- `go build ./...` — clean. `go vet ./...` — clean.
- `bash scripts/ci/guard-data-access.sh` — ✓ no inline SQL outside
  `internal/data`/`internal/db`.
- `bash scripts/ci/guard-i18n.sh` — ✓ 824 template keys resolve, all four
  locales match `en.json`.
- `go test ./...` — green except `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure`. Confirmed pre-existing and
  unrelated, **not** taken on faith: checked out unmodified `main` in a
  separate worktree and reproduced the identical failure there. It is
  the known root-sandbox environmental case (ut-docs#258) — we run as
  root, so a read-only directory doesn't block a write.
- `go test -race -count=3 ./internal/discovery/` — clean after fix #3.
- `gofmt -l internal/` flags 4 files, all pre-existing and untouched by
  this branch (verified identical on `main`); every file in this diff is
  gofmt-clean.

**TDD claims re-verified personally** (revert → confirm fail → restore →
confirm pass), rather than trusting "confirmed failing first" in the
prior phase's report:

1. **`pairing_api.go` → `discovery.TillID` wiring.** Reverted to
   `marketplace.DeviceIDFromConfig`.
   `TestListPairRequests_VerificationCodeUsesDiscoveryTillID` failed with
   exactly the claimed reasoning — `verification_code = "683635", want
   "718801"` — i.e. the two sides really do compute different codes off
   the old id. Restored → passes. The test is genuinely load-bearing.
2. **`RoleCheckFromSettings`.** Broke the rule to `return true`.
   `TestRoleCheckFromSettings_FalseWhenPrimaryURLSet` failed
   ("expected replica role (false) when sync.primary_url is set");
   the other two still passed, which is the right discrimination.
   Restored → passes. So this genuinely covers the production rule, not
   a synthetic bool — the prior phase's coverage-gap claim holds up.

**Live two-server run, reproduced independently.** Rather than trusting
the reported live run, drove the real `Advertiser` (real `mdns.NewServer`,
no fake seam) against the real `Browse` over actual UDP multicast on this
sandbox:

- primary advertised as `Reviewer Live Primary`, id
  `fc0abc31-eb44-4367-8c62-5fed1db2565f`;
- `Browse` returned exactly that candidate with name and id intact over
  the wire — so the TXT round-trip genuinely works end to end;
- after setting `sync.primary_url` and re-ticking, `Browse` no longer
  found it — **replica-doesn't-advertise confirmed live**, not just in
  unit tests.

The prior phases' live-run claims are therefore corroborated. The probe
was removed after the run and is not part of the diff.

## Verdict

**Safe to merge.** Three real defects were found and fixed here, each
with a regression test confirmed failing against the bug first: a genuine
unbounded goroutine leak (the one that actually mattered), an unbounded
result set on a LAN-open surface, and a test-side data race. Two further
real-but-out-of-scope items are filed as #271 and #272.

The core security intent of the card — making ADR-0033 §8's impersonation
mitigation real end-to-end by giving the beacon and the verification code
one shared id — is correctly implemented and independently verified.
Offline-first is unaffected: discovery is manager-initiated, bounded, and
entirely off the checkout path.
