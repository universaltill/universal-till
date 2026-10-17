# Review — LAN TLS enforcement 1/2: the main till refuses plain-HTTP sync from a pinned till (ut-docs#2737)

ADR-0114 §7 "Enforcement", after serving (#2736), pinning at pairing (#4091)
and pinned dialling (#4092). Built on Opus 5.5. The independent review ran
on **Sonnet** in a fresh worktree, because the Fable review was refused
(out of usage credits). This follows `MODEL-ROUTING.md`'s fallback.

## What shipped

- Migration `079_till_tls_pinned.sql`: a main-only table
  `till_tls_pinned(till_id → tills ON DELETE CASCADE, pinned_at)`. It is not
  a column on `tills`, because `tills` is admin-synced whole-row. It is
  classified not-synced in `sync_admin_repo.go`.
- `TillsRepo.TillByBearerHash` LEFT JOINs it into `TillRow.TLSPinned`.
  `MarkTLSPinned` is idempotent, and an unknown till fails on the FK.
- `pages.syncTill` is the single bearer check of every `/api/sync/*` bearer
  route:
  - A till's first bearer request over TLS records it.
  - A plain-HTTP request from a recorded till is refused with the same 401
    as an unknown bearer, warned once per till.
  - A till that has never used TLS keeps plain HTTP (ADR-0114 §11).
- The `web/help/img/manifest.json` surface hash is refreshed only. This is a
  backend change in `internal/pages` that renders nothing.
- ut-docs: ADR-0114 gets an amendment (observed TLS instead of the
  `report` flag), and `architecture/lan-sync.md` is updated. The rest of
  #2737 is split into #4409 (rotation), #4410 (re-proof) and #4411 (full
  close and TLS-only satellites).

## Tests (TDD: written first, seen failing)

- `internal/pages/sync_tls_enforce_test.go` runs on a real same-port TLS
  listener wired like `internal/server`, with a real `lantls` pinned client.
  - Before TLS, plain HTTP is accepted.
  - The first pinned TLS request is accepted and recorded.
  - After that, plain HTTP returns 401 and TLS returns 200.
  - An unpinned till keeps plain HTTP.
  - The record is persistent.
  - With the refusal disabled it fails:
    `plain ping after the till pinned = 200, want 401`.
- `internal/data/tills_tls_pinned_test.go` checks that the record is
  per-till, idempotent, cascades on revoke, and that an unknown id fails.
  With `MarkTLSPinned` a no-op it fails:
  `till a: want pinned after MarkTLSPinned`.
- The reviewer re-ran both mutations in its own worktree and saw both
  failures.

## Review findings

1. **Blocker, fixed.** `small_repos_test.go`'s hand-built `tills` schema
   lacked the new table, so the LEFT JOIN failed
   `TestTillsRepo_EnrollListLookupDelete`. The table was added there.
   Production always runs migration 079.
2. **Should-fix, fixed in ut-docs.** The ADR said the record comes "from its
   report". The code records it from an observed TLS login. The reviewer
   judged that sound (evidence rather than a claim, and every route covered).
   ADR-0114 has an amendment saying so (ADR-0007).
3. **Should-fix, accepted and documented.**
   - A pinned till whose replica later sends plain HTTP is locked out until
     it is revoked and re-paired. Examples: a rollback to a pre-#4092 build,
     or a replica DB restored from before the pin.
   - No released build sends a bearer over TLS and then over plain HTTP:
     v0.31.7 had #4091 only, and its bearer requests stayed plain.
   - Recovery is the existing revoke and re-pair. This is written into the
     ADR amendment and `lan-sync.md`.
   - Automatic recovery belongs with re-proof (#4410).
4. **Low, accepted and documented.** A stolen bearer used over TLS first can
   pin a till that is still on plain HTTP. That is a denial of service only,
   and the thief already has that till's access. Written into the ADR
   amendment.
5. **Nit, accepted.** A refused plain request still touches `last_seen_at`,
   because the touch happens inside `TillByBearerHash`. Only a client that
   already holds the bearer can cause it.
6. **Nit, accepted.**
   - The replica's ping probe treats a 401 as "no longer accepts this
     till's pairing". The real replica never sends plain once pinned, so it
     never sees this 401.
   - The exception is a one-off race: a request already in flight as the
     replica first stores its pin. That only produces a transient warning.
7. **Nit, fixed.** A failing `MarkTLSPinned` now warns once per till, not
   once per TLS request.
8. **Nit, no change.** A joining replica's raw snapshot carries the table's
   rows, which are FK-consistent and harmless. A promoted replica's
   leftover rows matter only for tills that already cannot talk to a new
   main till (different certificate).

Checked and fine:
- Every replica → main till bearer client uses `lantls.NewPeerClient` or the
  join's pinned client.
- `primary-proof` does not go through `syncTill`.
- Demo tills, and tills behind a TLS-terminating proxy, never see `r.TLS`.

## Gate

- `go build ./...`, `go vet ./...` and `golangci-lint` (changed packages,
  0 issues) are clean.
- `go test ./...` is green except `internal/plugins`, which hit the 10-minute
  package timeout in a WASM compile on a loaded container. That package is
  untouched; it was re-run with a longer timeout.
- The `ci.yml` build-job guards pass. `guard-deadcode-baseline`
  (deadcode tool built with go1.26) and `guard-shellcheck-version` (no
  shellcheck) could not run here, for environment reasons only.
- Not done: a driven two-till run on real devices. The socket-level test
  above is the realistic layer.

**Verdict: safe to merge.**
