# Code review: manager-gated approve/deny pairing UI (ADR-0033 part 3/3)

**Date:** 2026-08-02
**Scope:** `internal/discovery/browse.go` (+test), `internal/pages/sync_api.go`,
`internal/pages/pairing_api.go`, `internal/pages/pairing_join.go` (new, +test),
`internal/pages/pending_pairings.go` (new, +test), `internal/pages/init.go`,
`internal/pages/discovery_api_test.go`, `web/ui/pages/tills.html`,
`web/ui/partials/pairing_wait.html` (new), `web/ui/partials/pending_pairings.html`
(new), `web/locales/{en,ar,fa,tr}.json`, `README.md`.
**Trigger:** universaltill/ut-docs#185 — ADR-0033 part 3/3 (parts 1/2 already
merged as #183/#184). Closes the loop the ADR describes: discover a primary
over mDNS, request to pair, manager approves from a verification-code
compare, replica completes enrolment — no typed/scanned code.

## What shipped

- **`discovery.Candidate` gained `BaseURL`** (derived from the mDNS entry's
  `AddrV4`/`AddrV6` + `Port`). #183's own code discarded the address
  entirely, so a replica had nothing to send a pair-request to — this was a
  real, in-scope gap in #183, not a new design decision.
- **`completeJoin`**, extracted from `joinPrimary` (`sync_api.go`): the
  snapshot-download/stage-restore/stage-identity tail, now callable with a
  bare `(primaryURL, token, name)` instead of requiring a decoded QR code.
  Pure extraction — the existing QR-flow tests pass unmodified.
- **Replica side** (`pairing_join.go`): `POST /api/sync/pair-start` generates
  a `request_secret` + its commitment server-side, POSTs to the discovered
  primary; `GET /api/sync/pair-status` polls the primary's possession-gated
  retrieval endpoint every 15s (browser-driven via htmx), completes the join
  via `completeJoin` once approved, or expires client-side after the same
  10-minute TTL the primary's row uses (`pairingJoinNow` time-seam for tests).
- **Primary side** (`pending_pairings.go`): `GET /ui/tills/pending-pairings`,
  a manager-gated HTML partial listing pending requests + verification code,
  each with inline approve/deny mini-forms. An additive `HX-Refresh: true`
  header was added to the *existing* approve/deny handlers' success path
  only (`pairing_api.go`) — #184's JSON contract and tests are untouched.
- `web/ui/pages/tills.html`: wires both surfaces in; the previously read-only
  "Find a primary" results list gained a "Request to pair" action per result.
- ~16 new `tills.pairing.*` i18n keys across all four locales.
- One README line, previously stale ("selecting a discovered result to join
  directly is not yet wired up"), corrected.

## Independent review

Different-model subagent (Opus), fully independent: read the diff cold, ran
the whole gate itself, and re-verified specific claims by mutation rather
than trusting prose. This was not a rubber stamp — it found a genuine,
severity-ordered set of real problems.

### 1. CRITICAL, fixed — the feature was inert in a real browser

The first draft's own comment claimed "htmx's own MutationObserver
auto-processes [dynamically inserted buttons], no manual `htmx.process()`
call needed." **False for the vendored htmx** — `web/public/vendor/htmx.min.js`
is version 1.9.12; MutationObserver auto-processing is a 2.x feature. Content
inserted via plain `fetch()`/`innerHTML` (as the "Request to pair" button
was) is never scanned for `hx-*` attributes by htmx 1.x. The button was
inert markup — clicking it did nothing, and no Go test could have caught
this (they all call `mux.ServeHTTP` directly, bypassing the browser
entirely). The codebase already has the correct precedent for exactly this
situation, unused here: `catalog.html`'s image-upload handler calls
`htmx.process(...)` after its own `innerHTML` swap.

**Fix:** added `htmx.process(out);` immediately after the results
`innerHTML` assignment in `tills.html`; corrected the false comment.
Verified live (see below) that the resulting request actually reaches the
server.

### 2. MEDIUM, fixed — htmx silently discards every error render

`pair-start`'s failure paths rendered a real HTML error fragment but with a
non-2xx status (400/502). htmx does not swap non-2xx responses by default
and there is no `htmx:responseError` listener wired on the Tills page (there
is on `refund.html`/`catalog.html`, not on `tills.html`) — so once finding
#1 was fixed, every error (unreachable primary, primary refused, missing
till name) would still render invisibly. `TestPairStart_SurfacesUnreachablePrimary`
had actually locked in the broken behaviour by asserting the non-200 status.

**Fix:** every branch of `pair-start` and `pair-status` now returns 200,
with the outcome (waiting/error/joined/expired) encoded in the body, same
pattern the terminal states already used. Updated
`TestPairStart_SurfacesUnreachablePrimary` accordingly and added
`TestPairStart_RejectsInvalidBaseURL` for the new validation (below).

### 3. MEDIUM, fixed — wrong manager PIN gave zero feedback

The approve/deny mini-forms used `hx-swap="none"` with no error target. A
wrong PIN (403, correctly no `HX-Refresh`) left the form sitting there with
no visible change — indistinguishable from nothing having happened.
`settings.html` already has the right pattern for this
(`hx-on::after-request="if(event.detail.successful){...}else{show error}"`).

**Fix:** mirrored that pattern — each mini-form now reveals a
`hidden`-by-default `<p id="pin-error-{{.ID}}">` on a failed request. New
test `TestPendingPairingsUI_RendersWrongPINFeedbackWiring` locks in the
markup (this can't be tested via the approve/deny endpoint's own response,
which is correctly unchanged — it's the form wiring that had to change).

### 4. Three false-pass tests, mutation-proven and fixed

Same failure class this pipeline's coverage push has repeatedly found: a
test that would still pass even if the thing it names were broken.

- `TestPairStatus_NoActiveAttemptRendersSafely` only checked `rec.Code ==
  200` — the reviewer changed the no-active-attempt branch to render a
  bogus perpetually-polling "waiting" state instead, and the test still
  passed. **Fixed:** now asserts the body carries no `hx-trigger`.
- `TestPendingPairingsUI_EmptyStateWhenNonePending` — same gap, plus the
  reviewer made the handler return an entirely blank body (no polling
  element at all, silently freezing the card forever) and it still passed.
  **Fixed:** asserts the empty branch never renders a `<table>` (checked
  structurally, not by locale text — see below) and that the container
  keeps polling.
- `TestPendingPairingsUI_ListsPendingWithMatchingVerificationCode` had a
  dead "cross-check": it fetched the JSON API's response into `jrec` and
  then never asserted on it, instead recomputing the expected code locally
  from `derivedVerificationCode` — which would pass even if the partial's
  handler called a different/broken derivation. **Fixed:** now asserts
  against the JSON endpoint's own reported `verification_code`.

One assertion-design bug surfaced while fixing these: the empty-state check
originally asserted the translated English string, which failed
intermittently depending on *test execution order within the package* —
`config.I18n` is a process-global singleton another test in the same binary
may or may not have already initialized. Rewrote it to check structure
(absence of `<table>`) instead of locale-dependent text, which is
order-independent. (The unreachable-primary/invalid-base_url tests check
`.errMsg` text directly, which is never translated — those were already
order-independent and needed no change.)

### 5. Already-fixed false-pass, independently re-reproduced

The prior session's own fix (`TestPairStatus_TreatsPrimary404/429AsStillWaiting`
strengthened to check for the `hx-trigger` marker instead of just the status
code) was re-verified from scratch by the reviewer: patched the 404/429
branch to `if false`, both tests failed with the actual wrong-state body
shown, reverted, both passed. Confirmed genuine, not re-asserted on trust.

### 6. Security claim — confirmed, mutation-proven

`derivedVerificationCode` is defined once (`pairing_api.go`); both the
primary's list handler and the replica's `pairing_join.go`/`pending_pairings.go`
call the same package-level symbol — not two implementations that happen to
agree. `primaryTillID` provenance traced end-to-end:
`discovery.TillID` (settings-backed) → mDNS TXT record → `Candidate.TillID`
→ `discover-primaries` JSON → `hx-vals` → `pair-start`'s `till_id` form
field — same value both sides. Mutation-proven: hardcoding a wrong id in
`pair-start` makes `TestPairStart_SendsRequestAndShowsIndependentlyDerivedCode`
fail. Also reconfirmed live (see "Live verification" below) across two real
OS processes, not just in-process Go tests.

*Noted for a future ADR revision, not a defect in this diff*: the code
binds `commitment ‖ primary_till_id`, and `primary_till_id` is itself public
over mDNS — a relaying MITM that repeats the real primary's id would produce
a matching code on both screens. ADR-0033 §4 specifies exactly this
derivation, so this diff correctly implements the accepted design; closing
that gap is a future ADR question (binding the code to something only the
real primary holds), not something to fix here.

### 7. Recommended fixes, applied in this same session

- **Duplicate DOM id**: the outer container and the swapped-in partial were
  both `id="pairing-status"` — worked by accident (`hx-target="this"`) but
  invalid HTML. Renamed the outer host to `#pairing-status-host`.
- **Wrong i18n key on the replica's own idle state**: it reused
  `tills.pairing.none` (the *primary-side* pending-list's empty copy).
  Renamed the replica's no-active-attempt status to `"idle"` and added a
  dedicated `tills.pairing.no_active_request` key (all 4 locales).
- **Double-`completeJoin` race**: `GET /api/sync/pair-status` previously
  read/decided/wrote the pairing state across separate locked `get()`/`set()`
  calls, so two browser tabs polling concurrently could both observe
  `status == "waiting"`, both retrieve the (non-one-time-readable) token,
  and both race `completeJoin` — the primary's one-time enrolment token
  would let only one succeed, but the other lands in a confusing "primary
  refused the enrolment" error state even though the join actually
  succeeded. **Fixed:** the whole read-decide-network-call-mutate sequence
  now runs under one `sync.Mutex` hold for the handler's entire body — a
  deliberate full serialization, documented on `replicaPairing`'s own
  comment, and cost-free since there is only ever one outbound attempt per
  till by design. Verified clean under `go test -race`.
- **Unvalidated external input**: `base_url` (from an untrusted LAN mDNS
  responder) was used to build an outbound request with no scheme/host
  check — CLAUDE.md: "validate all external input." **Fixed:**
  `validPrimaryBaseURL` rejects anything that isn't a parseable `http`/`https`
  URL with a host. New test `TestPairStart_RejectsInvalidBaseURL` covers
  `javascript:`, a bare string, and `ftp://`.
- Stale doc comment on `pairWaitView` (described a non-existent parameter) —
  corrected.

### 8. Confirmed clean, no changes needed

- **No new filesystem writes** — grepped the new files for
  `os.Create`/`WriteFile`/`OpenFile`/`MkdirAll`/`Remove`/`Rename`/`Mkdir`:
  zero hits. The only disk writes are inside the pre-existing `completeJoin`
  → `db.StageRestoreFromReader`/`StageReplicaIdentity`, both already
  writing into `filepath.Dir(d.Cfg.DBPath)` (not cwd-relative, directory
  necessarily exists since the DB is open). Neither of this pipeline's two
  recurring bug classes applies.
- **XSS / attribute-breakout**: `esc()` (`textContent`→`innerHTML`) then
  `escAttr` (`.replace(/"/g,'&quot;')`) — order is correct, no
  double-decode. Verified against a crafted `till_id` containing `"
  onfocus="alert(1)`: `JSON.stringify` escapes to `\"` first, then every
  `"` becomes `&quot;`, so the `hx-vals="…"` attribute never contains a raw
  `"`. Server-side, `{{ .DeviceName }}` is auto-escaped by `html/template`.
  No XSS found.
- **i18n**: all 4 locales independently diffed key-by-key (not just via the
  guard script) — 1084 keys, zero missing, zero extra. ar/fa/tr spot-checked
  for real translations (not copy-pasted English), consistent terminology
  with the surrounding files.
- **No sensitive demo data** — only generic fixture names (`Bar Till`,
  `Kitchen Till`, `Corner Shop`, `Task Runner`, etc.).
- **RTL** — no literal `left`/`right` CSS in any touched/new template.

## Live verification (beyond `go test`)

Both before and after the independent-review fixes, drove the actual
feature across two real, independent OS processes (built binary, two
listeners, real mDNS over this sandbox's network — following the precedent
already established live in the #183 review):

- `discovery.Browse` found both real instances with correct, dialable
  `base_url` values (the literal gap this card closes).
- `pair-start` on the replica against the real primary produced a 6-digit
  code; the primary's real `/ui/tills/pending-pairings` partial and its
  JSON API independently derived the identical code — confirmed across two
  separate OS processes, not assumed from unit tests.
- Real approve (confirmed `HX-Refresh: true` on the live response) →
  replica's real `pair-status` poll completed the join, rendered "✓ Joined:
  My Store — restart this till to finish", polling correctly stopped.
- Real files staged on disk (`restore-pending.db`, `replica-identity.json`
  with correct `primary_url`/`till_name`/`receipt_prefix`); **restarted the
  real replica process** and confirmed the log shows "staged backup restore
  applied" + "replica identity applied — this till is now part of the
  shop", and the primary's real `/tills` page then lists the new till as
  enrolled.
- Real deny path: replica correctly stays in "waiting" (ADR-0033 §4's own
  accepted denied-vs-pending ambiguity, not a bug) until the client TTL
  fires (verified separately via the time-seam unit test, not by waiting 10
  real minutes).
- After the fixes: re-ran the same live two-process flow end-to-end again —
  invalid `base_url` correctly rejected with a translated, visible error
  (confirms real i18n wiring, not just the raw key seen in unit-test
  fixtures); the full pair→approve→join flow still completes correctly
  through the widened lock in `pair-status`.
- All test processes killed and scratch directories removed afterward.

**Not verified live**: an actual browser click-through (no Playwright suite
covers the Tills page). The `htmx.process()` fix (finding #1) is exactly
the class of bug only a real browser DOM can catch — verified by code
inspection against the working `catalog.html` precedent and by confirming
the server-side request the button targets behaves correctly when driven
directly, not by an actual click.

## Full gate (re-run after every fix, not just once)

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` on every touched/new file — clean.
- `go test ./...` — green except `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure` (ut-docs#258, confirmed via
  `git stash` to fail identically on unmodified `main` — a root-sandbox
  environmental case, unrelated to this diff).
- `go test -race ./internal/pages/...` (pairing/pending-pairings tests) —
  clean, confirming the widened-lock race fix doesn't introduce a deadlock
  or a new race.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  both green (854 template keys resolve, all locales match `en.json`).

## Verdict

**Safe to merge.** The independent review found and this session fixed one
critical defect (the feature was completely inert in a real browser — no
Go test suite could have caught it), two medium UX defects (invisible error
states, invisible PIN-failure feedback), three mutation-proven false-pass
tests, and a real (if narrow) concurrency race — every fix backed by a
test, the false-pass fixes specifically re-verified fail-then-pass by the
independent reviewer itself, not just asserted. One design observation
(verification-code binding to a spoofable public id) is noted against
ADR-0033 for a possible future revision, not a defect in this diff, which
correctly implements the ADR as accepted.
