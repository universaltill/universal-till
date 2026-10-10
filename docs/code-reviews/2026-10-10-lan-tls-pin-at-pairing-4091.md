# Code review — pinned LAN TLS at pairing (ut-docs#4091)

- **Card:** universaltill/ut-docs#4091 (ADR-0114 §7, slice 2/3; split from #2736)
- **Branch:** `feat/4091-lan-tls-pin`
- **Author:** hand-started cloud session (`lane:cloudsession`)
- **Reviewer:** independent subagent on a different model from the author, on a detached worktree

## What shipped

- `internal/lantls`:
  - `Cert.Pin`, `PinOf`.
  - `ConnContext` / `ServedPin`: the pin served on *this* TLS connection is stamped into the request context. Plain HTTP carries none.
  - `PinnedClient`: TLS 1.3. Trusts the server by pin only (`VerifyConnection`, no CA, names or dates). Learns the pin, or enforces an expected one. No proxy, no keep-alives, no redirects. `Exchange` reports whether a plain-HTTP retry is safe: connected, no request written, not a pin mismatch.
  - `HTTPSBase`.
- `internal/server`: `withLANTLS` sets `srv.ConnContext` when TLS is on.
- **New pairs (ADR-0033 code over the pin):**
  - `pair-request` stores the pin served on its connection (migration 070, `pending_pairings.served_pin`). The manager's code is `first6digits(SHA-256(commitment ‖ primary_till_id ‖ pin))`. With no pin, the code is the old formula byte for byte.
  - The joining till sends `pair-request` over TLS on the discovered port first and binds the pin it saw. The `request_secret` status poll is pinned to that pin.
  - The pin is staged in `ReplicaIdentity.PrimaryCertPin` and becomes `sync.primary_cert_pin`. It is always written, so a re-join without a pin clears it.
- **Existing replicas:**
  - `PrimaryProof(…, pin)` covers the pin of the connection; with no pin it is the legacy HMAC.
  - The main till's proof handler passes `ServedPin`.
  - `PrimaryWatch.challenge` tries TLS first. A valid proof stores the pin on re-discovery and in the once-per-process `ContactOK` backfill. A plain proof never clears a pin. A proof answered over TLS that fails is final.
- Docs: `ut-docs/architecture/lan-sync.md` (pinned LAN TLS section).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A main till on slice A alone (serves TLS, binds no pin) against a till built from this slice: the TLS attempt succeeds, so there is no fallback. The codes differ and the proof fails. | **Accepted.** Slice A (universal-till#1846) merged after v0.31.6, so no released till runs it without this slice. ADR-0114 §11 already orders "main till first". Documented in lan-sync.md. Accepting a legacy HMAC over TLS would add a downgrade for no released peer. |
| 2 | minor | A fresh pinned transport on every 15 s status poll left idle TLS connections behind for 90 s. | **Fixed:** `DisableKeepAlives` on `PinnedClient`. |
| 3 | minor | A dead host cost a TLS timeout plus a plain timeout. | **Fixed:** fall back only when the TCP connection opened and no request was written (`Exchange`). |
| 4 | minor | A lost answer to a TLS pair request fell back to plain and created a second pending row. | **Fixed** by the same rule: a written request never falls back. Tested. |
| 5 | minor | The pairing pinned clients followed redirects (the URL carries `request_secret`). | **Fixed:** `NewPinnedClient` refuses redirects. Tested. |
| 6 | nit | The `ContactOK` backfill runs once per process, so a main till that is briefly unreachable leaves the replica unpinned until restart. | **Accepted.** It matches the existing id backfill. Slice 3/3 needs the pin and can re-arm it there. |

The reviewer found no security regression. They confirmed: a MITM gets different codes in every mixed case; `VerifyConnection` runs on every handshake, and there is no session cache anyway; `request_secret` only travels after the pinned handshake; and the legacy bytes are identical. Test gaps they named are now covered: the poll refuses another certificate; an answered TLS proof doesn't fall back.

## Verified

- TDD: lantls and discovery tests were written first and failed to build or run. Mutations: dropping the served pin on the main till fails `TestPairing_TLSMainTillBindsThePinAndStagesIt`, `TestPairing_MITMCertificateChangesTheCode` and `TestPrimaryProof_TLSReplicaLearnsThePin`. An unpinned status poll fails `TestPairStatus_PollRefusesAnotherCertificate`.
- **Real driven run, two till binaries** (`127.0.0.1:9181` main, `:9182` joining, separate data dirs):
  - The main till logs `[LAN TLS] serving TLS on the same port`.
  - `openssl s_client` shows the served SPKI pin `a2671cb4…bd640`.
  - `pair-start` on the joining till shows **815042**, and the main till's card shows **815042**.
  - `pending_pairings.served_pin` equals the served pin.
  - Approve, then poll: "Joined". `replica-identity.json` has `primary_cert_pin` equal to that pin and `primary_url` still `http://…`.
  - Both processes were stopped afterwards.
- `go build ./...`, `go vet`, `gofmt`, `golangci-lint` (0 issues), `go test -race` on lantls, discovery and the new pages tests, full `go test ./...`.
- Guards: data-access, netaccess, migration-version-collision, card-data-schema, core-neutral, i18n.
- deadcode: no new entries. The `logging.Stderr`/`timestampWriter.Write` hits are reached only from `cmd/unitill-desktop`, which this container can't build (no GTK headers); CI analyses it.
- No UI or locale change, so there was no UX pass and no help topic.

## Deferred / not in scope

- Dialling `https`/`wss` for sync, and moving enrolment and the snapshot to TLS: slice 3/3.
- Refusing plain HTTP once pinned, rotation and re-proof: ut-docs#2737. Until then, stripping TLS on both legs gives today's unpinned behaviour.

## Verdict

Safe to merge.
