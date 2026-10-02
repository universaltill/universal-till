# Code review — WASM runtime `net:validation:<host>` plain-HTTP validation-data fetch (ut-docs#3226)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3226 (ADR-0132 card 4/5; see `ut-docs` for the ADR
  amendment and the full cross-repo review record, which this one
  summarises for `universal-till`'s own diff).
- **Branch:** `feat/3226-wasm-validation-http`
- **Author:** Opus 5.5 (subagent).
- **Reviewer:** two independent Fable rounds (never the author's model) —
  round 2 scoped to re-verifying round 1's blocker-class fix.
- **Verdict: SAFE TO MERGE.**

## What shipped

`internal/plugins/`:
- `permission_setting.go` — new `net:validation:<host>` permission form
  (`ParseValidationPermission`, `validateValidationPermissions`,
  `validationGrantMatch`): exact host only, no wildcard, no `@setting`
  form, balanced-bracket IPv6 literals only.
- `manifest.go` / `manifest_verifier.go` — both the `ParseManifest` and
  `VerifyManifest` (marketplace-install/import) paths refuse a malformed
  grant.
- `wasm_egress.go` / `wasm_hostfns.go` — `hostHTTPRequest` allows plain
  `http://` and an 8 MiB response cap (`validationResponseCap`, vs. the
  ordinary 256 KiB `httpResponseCap`) for a host named by
  `net:validation:<host>`; that host is **never** an exact grant at dial
  time (public addresses only), even alongside an ordinary `net:<host>`
  grant for the same host. Redirect hops (`checkPluginRedirect`) are
  re-checked against each hop's own validation grant. Cap selection
  extracted into `readHTTPResponseBody(resp, grants)` for direct unit
  testing.
- `testdata/hostfn_guest/main.go` — new `http_len` guest mode for a test
  helper.
- New `wasm_validation_grant_test.go`: parser, manifest-refusal,
  grant-matching, a large egress table (scheme/host-exactness/
  metadata-LAN-loopback-till-port refusal/redirect-per-hop), response-cap
  table; new `TestReadHTTPResponseBodyCap` unit test.

## The bug round 1 found, and the fix

A plugin holding BOTH an ordinary `net:<host>` grant and the new
`net:validation:<host>` grant for the SAME host could reach a LAN/non-public
address over plain HTTP for that host (`httpNetPermission` was separately
consulting `netGrantMatch`, which returned `exact=true` when the ordinary
grant existed) — exactly the capability ADR-0121 reserves for its own
still-unbuilt `http:lan` (ut-docs#3156). Confirmed by an actual probe (a
fake LAN server took a hit it should never have received). Fixed:
`httpNetPermission` now returns `exact=false, nil` unconditionally on the
validation path — no DB lookup at all, so `exact=true` is unreachable
regardless of any other grant. A `net:validation:<host>` grant is public-only
for every request to that host, https included. Round 2 independently
reproduced the original bug via mutation (reverting the fix) and confirmed
the fix denies it. New test: `TestHTTPValidationGrant`'s "validation and
ordinary exact grant on the same LAN host/literal" cases.

Two more confirmed gaps, also fixed (full detail in the ut-docs record):
marketplace/import install (`VerifyManifest`) wasn't running the new
validation (now is); unbalanced IPv6 brackets parsed as a dead-but-accepted
grant in both this repo and `ut-cloud` (now refused in both, matching).

## Verified beyond automated checks

- `go build ./...`, `go vet ./...`, `gofmt -l` clean.
- `go test -timeout 20m ./internal/plugins` — this repo's actual CI gate
  (`ci.yml:402`, no `-race`) — green, re-run independently three times
  across the review cycle.
- `go test ./internal/plugins/... -race` on every new/touched test: green,
  no data races, across two independent review rounds plus the
  orchestrator's own runs.
- Two mutation-testing passes (pre-review and round-2 review) proving the
  scheme-check, response-cap and `httpNetPermission` fix are real
  behaviour, not tautological tests: breaking the logic fails the specific
  test for the right reason.
- A genuine, reproducible `-race` failure in `internal/netreach`
  (`logging.L()` lazy-init data race) was confirmed pre-existing on a clean
  `origin/main` checkout (`git stash` repro) — unrelated to this change,
  filed as its own Backlog card.
- No real client/shop name, no secrets, in any test data.
- No i18n key needed — verified `internal/pages/plugin_permission_view.go`
  renders every `net:*`-family permission as a raw string; no new
  locale-specific copy.

## Safe to merge

Yes, after the F1 (security) fix and two more confirmed gaps (F2, F3),
each independently re-verified by a second review round. See the full
cross-repo record and finding table in `ut-docs`:
`code-reviews/2026-10-02-wasm-validation-data-fetch-3226.md`.

## Deferred (Backlog)

- The pre-existing `internal/netreach` data race (unrelated to this
  change).
- A short install-time hint in `plugin_permission_view.go` that
  `net:validation:` means unencrypted HTTP (card excluded UI).
- The pre-existing `validateSettingBoundPermissions`-missing-from-`VerifyManifest`
  gap (same shape as this card's F2, different permission form) — out of
  scope here.
