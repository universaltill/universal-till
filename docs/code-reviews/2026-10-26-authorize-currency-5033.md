# Review: authorize payload carries the sale currency (ut-docs#5033, part 1)

- Change: `completeTender` adds `currency` (ISO 4217, validated) to the
  `payment.<key>.authorize` payload; contract doc updated; the base-contract
  test now requires it.
- Independent review (Opus): 8 findings.
  - HIGH: enforcing `min_pos_version` against the real build version would
    refuse installs of first-party plugins that declare 1.0.0 (till is
    v0.34.x; ut-docs#3288 sequencing). **Part 2 dropped from this change**;
    follow-up card filed.
  - MEDIUM: "dev" skip misses `0.1.0-dev` and nightly stamps — moot, reverted.
  - LOW: client-supplied leg currency unvalidated — fixed: sale currency
    wins, leg currency only as fallback, malformed value omitted.
  - INFO: idempotency-key digest change is safe (attempt id is in-memory).
- Verified: `go build ./...`, gofmt, `TestTenderHandler_*`, `./internal/pages`.
  `./internal/plugins` times out in `TestBlobQuota`/`TestHTTPEgress`
  (network egress in this sandbox; the package is untouched by this change).
