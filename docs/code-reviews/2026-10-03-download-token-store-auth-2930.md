# Code review — download tokens need the till's device credential for paid listings (ut-docs#2930)

**Date:** 2026-10-03 · **Lane:** lane:cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)
**Card:** universaltill/ut-docs#2930 (ADR-0120 Phase 2, finding F1, download half)
**PRs:** ut-cloud `fix/2930-download-token-store-auth`, universal-till `fix/2930-download-token-device-credential`, ut-docs `docs/2930-download-token-store-auth`

## What shipped
- **ut-cloud `downloadsvc`:** `IssueTokenRequest.StoreAuthenticated` (`json:"-"`), and new `ErrStoreAuthRequired`. If a listing is `paid_listing` and the caller is not store-authenticated, it is refused right after the listing loads. That happens before any merchant lookup, entitlement check or self-serve acquisition, so the answer says nothing about the claimed store. gRPC `IssueDownloadToken` never sets the flag and maps the error to `PermissionDenied`.
- **ut-cloud `handlers/downloads.go`:**
  - A hex bearer is checked by the shared `authorizeStore` for the body's `store_id`.
  - Device credentials are opaque hex. A bearer containing `.` is a JWT: a self-mintable (b) token or a staff token. It is never treated as store authentication.
  - On success: `speaksFor(device_id)` must hold, or the answer is 403 `device_mismatch`. The merchant is the authenticated store, and the body's `merchant_id` is ignored.
  - Anyone else gets free listings only. A paid listing answers 403 `store_auth_required`. It is a 403, not a 401, so it never counts toward ADR-0116 D6 "pair again".
  - A missing `store_id` answers 400 before the credential check.
  - The router wires `WithStoreAuth`. The route stays `SchemePublic` so older tills (no bearer) keep installing free plugins.
- **universal-till `marketplace.Client.IssueDownloadToken`:** sends `cfg.MerchantToken`, the live ADR-0116 credential (every caller builds its client from `enroll.Effective`), as the bearer, ahead of any OAuth token. It goes **only to `EndpointURL`**, never to a DevMode override, and is never logged.
- **Docs:** ut-cloud `docs/api-reference.md` and `docs/operations.md`; ut-docs `reference/plugin-lifecycle.md`.

## Findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A till whose credential is refused (revoked, rotated out, retired legacy token) now gets a 401 even for **free** installs. Before, free installs worked anonymously. | **Accepted + documented.** Such a till is already refused on every other till route and must be paired again. Falling back to the anonymous path would hide that signal. Added to api-reference and plugin-lifecycle. |
| 2 | minor | The till UI shows a raw error for `store_auth_required` and the till-auth 401 codes. | Follow-up **#3541** (needs new locale keys and language-pack PRs). |
| 3 | minor | Pre-existing: `authorizeStore` maps `ErrStoreDeleted` to 503 and logs an error on every till route. | Follow-up **#3542**. |
| 4 | nit | Stale `routes.go` comment ("real tills send no bearer here"). | Fixed. |
| 5 | nit | Empty `store_id` with a credential gave 401, not 400. | Fixed, with test `TestIssueTokenMissingStoreIDIs400`. |
| 6 | nit | Docs sentence for #1. | Fixed. |

The reviewer confirmed there is no remaining anonymous path to a paid listing. It checked:
- the alias route and grpc-gateway shadowing (the exact-path mux wins);
- the gRPC server;
- the `release_id`/`version` params;
- self-serve acquisition;
- register- and merchant-scope entitlements.

It also checked these were sound:
- the `.` heuristic (tokens are `hex(32 random bytes)`);
- the middleware not rejecting a hex bearer on this route;
- merchant == store org, which `stores/register` returns as `merchant_id`;
- legacy shared tokens still authenticating until retirement.

## Verification
- **TDD**, re-verified by the reviewer in separate worktrees. With the service gate removed, the handler block disabled, or the till sending no credential (or the dev-override guard dropped), each of the six new tests fails. Restored, they pass.
- **ut-cloud** `scripts/ci/verify.sh`: gofmt, vet, golangci-lint, `go test ./...`, timezone gate, contract and residency guards. EXIT 0, both before and after the review fixes.
- **universal-till:**
  - `gofmt`, `go build ./...`, `golangci-lint` (0 issues), `go test ./...` all pass.
  - Every guard in `ci.yml` passes except three that this container cannot run: `guard-shellcheck-version` (no shellcheck binary), `guard-deadcode-baseline` (it skips `cmd/unitill-desktop` without GTK headers, then flags `internal/logging/file.go`, which this diff doesn't touch), and `retry-with-backoff.sh` (a helper, not a guard).
- **Not run:** a live till ↔ cloud install. There are no paid listings live (exposure was nil; #2930 comment 2026-09-26). The handler tests use the real ent schema and the real `merchantauth` service (`ProvisionStore`, `Revoke`).

## Deferred
- #3540: retire or bind `/v1/auth/merchant-token` and scheme (b) (the second half of Phase 2 F1).
- #3541 and #3542, as above.

## Verdict
Safe to merge, ut-cloud and universal-till together. The cloud change on its own is backward-compatible: free installs keep working, and paid installs were never possible for real tills before.
