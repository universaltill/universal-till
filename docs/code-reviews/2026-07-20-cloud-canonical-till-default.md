# 2026-07-20 — Packaged till default: cloud.universaltill.com

## Context
Stage E of the ut-market-place→ut-cloud rename plan (domain consolidation),
explicitly gated pending Farshid's sign-off given the blast radius: every
till in the field ships with `UT_MARKETPLACE_ENDPOINT_URL` defaulting to
`marketplace.universaltill.com`, and a sync failure never blocks checkout
(offline-first) — so a bad default fails silently. Farshid signed off on
the full scope today, including this step: change the *packaged* default
for new installs to `cloud.universaltill.com`, the now-canonical host
(`cloud.universaltill.com` was also just promoted to the primary OIDC
redirect host in `homelab-k8s`, see that repo's
`feat/cloud-canonical-oidc-host` PR).

## Change
- `packaging/pos.env.example:29` — default flips to
  `https://cloud.universaltill.com/api`.
- `docs/marketplace-config.md` — three example URLs updated, plus a new
  line noting `marketplace.universaltill.com` still answers and is kept
  alive indefinitely for tills packaged before this change.
- New test: `packaging/pos_env_example_test.go` —
  `TestPosEnvExampleDefaultsToCanonicalCloudHost`, asserting the packaged
  default line verbatim. There was no prior coverage pinning this value at
  all, despite the silent-failure blast radius — added per an independent
  review's suggestion.

## Review (independent, sonnet)
- Confirmed `internal/config/config.go`'s actual runtime fallback is
  `http://127.0.0.1:8081` — the marketplace/cloud domain is **not** compiled
  into the binary; it only exists as a value in this packaged template.
- Confirmed `.goreleaser.yaml` and `packaging/macos/build-app.sh` both copy
  `pos.env.example` verbatim into release artifacts, and no other file in
  the repo hardcodes the old domain as a default — this fix is complete,
  not partial.
- Confirmed `cloud.universaltill.com` is live and routes to the identical
  backend Service as `marketplace.universaltill.com`
  (`homelab-k8s/kubernetes/apps/unitill-marketplace/deployment.yaml`).
- Confirmed this change has **zero effect on tills already in the field** —
  it only changes the default baked into *future* installer packages;
  existing tills have their own already-materialized `pos.env` on disk.
- Verdict: safe to commit and release.

## Verification
- `go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh` all
  green, including the new packaging test.

## Not touched (by design)
- `docs/code-reviews/2026-07-16-store-enrolment.md` and
  `2026-07-16-marketplace-endpoint-and-port-fallback.md` — historical dated
  records, correctly left describing what was true then.
