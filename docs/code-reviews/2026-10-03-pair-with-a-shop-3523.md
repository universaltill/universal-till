# Review: Settings → "Pair with a shop" (ut-docs#3523, ADR-0116 D5/D6)

Date: 2026-10-03 · Branch: `feat/3523-pair-with-a-shop`
Built and fixed by Opus-based agents (dev/tester); independent review by
Fable (different model), in its own worktree off the "WIP: pre-review
snapshot" commit.

## What shipped

- `enroll.Pair` (`internal/enroll/pair.go`): refuses an empty/over-long
  code, a missing endpoint, an env-pinned identity (token, client id, device
  id, and — on a main/standalone till only — store id) and a replica with no
  synced store, all **before** anything is cleared or the single-use code is
  spent. Then, serialized on `attemptSem` with registration: clears exactly
  this till's cloud identity (token, enrolled_at, device_registered,
  device_till_id; plus store_id/merchant_id off a replica), mints
  `till-<uuid>`, POSTs `{code, device_id, device_name, version}` to
  `/v1/stores/pair` with no Authorization header, accepts only a complete
  answer with a well-formed token, refuses another store on a replica, and
  adopts the credential through `adoptOwnCredential`. The pinned signing key
  and store-level preferences are untouched. `identityReplaced` makes
  `Effective`/`liveToken` report the live (even empty) identity instead of
  Init's startup copy.
- `POST /api/enrol/pair` (`settings_page.go`): same always-200 HTMX shape and
  `checkOrElevate("settings")` gate as `/api/enrol/now`; the elevation dialog
  carries the code as a hidden field; the audit row holds the store id only.
  Demo mode denies it. Role-matrix test row added.
- Settings card: an unconditional "Pair with a shop" form inside the
  Till-registration card; en/ar/fa/tr strings; `web/help/*/claim.md` gains a
  "Pair with a shop (and re-pairing)" section in all five languages; a
  Playwright spec checks fit at 1024×600 and 360×740 in en/fa, the
  `required` guard and the error state.

## Re-verified TDD claims (reviewer, own throwaway worktree)

- Env-pinned **store id** guard: with `(storeIDExplicit && !replica)` removed
  from `pinned`, `TestPairRefusedWhenEnvPinsIdentity/store_id` fails with
  `Pair ran although UT_MARKETPLACE_STORE_ID pins the identity` (the log
  shows the paired token adopted under the pinned store — the split identity
  Tester found). Restored → passes.
- Replica without a synced store: with the `if replica && own == ""` block
  removed, `TestPairReplicaWithoutSyncedStoreDoesNotBurnCode` fails with
  `Pair err = the pairing code belongs to a different shop…, want
  errPairReplicaNoStore` — i.e. the cloud was called and the code burnt.
  Restored → passes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | **Double-submit wipes a successful pairing.** htmx queues a second submit of the same form behind the in-flight one (hx-trigger's default `queue:last`), and `Pair` is not idempotent: it clears the identity before the single-use code is spent. A double-tap on the touchscreen Pair button (or on the elevation dialog's Approve, the only path for a cashier) = first call pairs, queued second call wipes it and is refused `code_invalid` → till unregistered, "Pairing failed". `/api/enrol/now` has the same shape but `RegisterNow` is idempotent, so it never mattered there. | **Fixed**: `hx-sync="this:drop"` + `hx-disabled-elt="find button[type=submit]"` + `hx-indicator="#pair-busy"` on the pair form (also the missing loading state), and the same `hx-sync`/`hx-disabled-elt` pair on the shared `elevation_prompt.html` form (harmless to every other site; it only drops duplicate submits of the same dialog). Pinned by `TestSettingsPage_ShowsPairWithAShop` and `TestEnrolPair_PromptFormDropsDuplicateSubmits`. New key `settings.enrol.pair_busy` (en/ar/fa/tr). |
| 2 | minor | The replica-without-store refusal (a real operator case, by design never reaches the cloud) surfaced as the generic "Pairing failed: <English error>" while the store-mismatch refusal got its own translated message. | **Fixed**: `ErrPairReplicaNoStore` exported; handler branch; key `settings.enrol.pair_replica_no_store` (en/ar/fa/tr); `TestEnrolPair_ReplicaWithoutStoreHasOwnMessage` (also asserts 0 cloud calls). |
| 3 | minor | Help search: `multitill.md` (LAN till-to-till pairing) carries `pairing` as a keyword, `claim.md` did not, so "pairing" after a "needs pairing again" chip ranked the wrong topic first (keywords score 50, body 10). | **Fixed**: pairing keywords added to `claim.md` front-matter in en/de/ar/fa/tr. |
| 4 | minor | After a *refused* pair on a main till, `Effective(cfg).Marketplace.StoreID` still reports Init's startup store id (the override only applies when `cur.StoreID != ""`), with an empty token. Every consumer checked (`cloudsync` skips on empty token; `currentStoreAuth`/`registerDeviceID` refuse without a token; status chip uses `displayStoreID`, which is cleared) is harmless, so no functional effect. | **Accepted**, noted for a follow-up: a one-liner (`(cur.StoreID != "" \|\| identityReplaced) && !storeIDExplicit`) plus an assertion in `TestPairRefusedLeavesIdentityCleared` would make `Effective` fully consistent. |
| 5 | minor | Remaining raw-English `err.Error()` reasons shown by the handler: "enter the pairing code…" (unreachable through the UI: `required`), the `UT_MARKETPLACE_*` env-pin message (operator/ops-facing, names env vars), and `cloud answered 403 code_invalid` (same as `/api/enrol/now`). | **Accepted** — matches the `/api/enrol/now` precedent; the two operator-actionable refusals now have translated messages (finding 2 and the existing mismatch one). |
| 6 | nit | On a replica with `UT_MARKETPLACE_STORE_ID` pinned and no synced store, a successful pair adopts the token but `CurrentStatus().Registered` stays false (adoptOwnCredential never fills `cur.StoreID` under an env pin) → the handler shows "Pairing failed: This till is not registered yet" despite success. Pre-existing quirk of env-pinned replicas (same at boot); exotic configuration. | **Accepted**, noted. |
| 7 | nit | The mismatch warning logs the cloud's `store_id` unbounded (redeem bounds LAN-sourced ids to 64 chars). The cloud is a TLS-authenticated peer, the read is capped at 16 KiB. | **Accepted**. |
| 8 | nit | `e2e` spec uses a fixed `waitForTimeout(300)` to assert that no POST happened on an empty code. | **Accepted** (negative assertion; no better primitive without a server-side marker). |
| 9 | note | After a successful pair on a main till that has joined replicas, the admin sync pushes the new store_id to them while they hold tokens for the old store → they 401 and need pairing too. That is ADR-0116's design (each device pairs itself); the help's step 4 covers the replica side. | Accepted. |

Invariants checked with file:line: no raw SQL and no file writes in the diff
(settings keys only; `paths.Data` N/A); the pairing code and the tokens never
appear in any log line, error string, audit payload or response (every branch
of `pair.go` and the handler; `assertPairSecretsNotLogged` captures
pre-redaction); no Authorization header on `/pair`; 16 KiB bounded read;
error code bounded by `redeemErrCodeRe`; `validRotatedToken` on the answer;
`attemptSem` serializes with `RegisterNow`/`run`; `-race -count=3` clean;
nothing on the checkout path (explicit Settings action, bounded by the
request context and the 15 s HTTP client); no real shop/person names in
tests (`store-paired`, `Front counter`, `Back office`). UX checklist:
existing tokens/classes only (`set-divider`, `set-row`, `btn btn-touch`,
`muted`, `htmx-indicator`), the `h3` inline margin matches the 15 sibling
`h3`s in the card, logical properties untouched, RTL and 360 px checked by
the e2e spec, `required` + error + (now) loading state, no modal, `osk.js`'s
guard sweep gives the text input `inputmode="none"`.

Help topic: `claim.md` is the right file (store registration / cloud
account); `multitill.md` is LAN till-to-till pairing, a different feature.
The added section is accurate against the ADR (8 characters, 15 minutes,
single use; "Add or re-pair a till"; manager/admin approval; replica must
match its main till's shop; refused code leaves the till unregistered) and
the real card label ("Till registration", not the ADR's "Cloud").

## R1 (always-visible control, no confirm, clears before the call)

Verdict: **should-fix, not a blocker** — a same-cycle follow-up card, not a
reason to hold this PR.

- The ADR mandates the clear → mint → call order once the operator commits;
  it says nothing against asking first, so a confirmation does not
  contradict it.
- Worst case is bounded: on a **claimed** store a mistyped code costs one
  more pairing code (minutes); on an **anonymous** store (no owner, no
  pairing code) the till can only "Register as a new store", which ADR-0116
  D6 itself rates as "only the fleet history is lost". Paid entitlements
  hang off a claimed store, so the irrecoverable case carries no licence.
- But the control sits on a healthy till too, a manager session gets no
  prompt at all (only a cashier sees the elevation dialog and its summary),
  and the realistic mistake — owner mints "Add a till", walks to the wrong
  till — is a one-field form with no second look.
- Recommended shape (no modal, kiosk-safe, same HTMX pattern): when
  `CurrentStatus().Registered || ViaMainTill`, the first POST renders an
  in-place confirmation in `#pair-msg` ("This till is currently connected to
  shop X. Pairing replaces that connection; a wrong code leaves it
  disconnected.") with a button that re-POSTs `code` + `confirm=1`; an
  unregistered/revoked till (the flow's real audience) keeps the one-step
  path. Needs UX sign-off and the driven pass, hence the follow-up.

## Verification

- `gofmt -l .` clean · `go build ./...` · `go vet ./...` · `go test
  ./internal/enroll/... ./internal/pages/...` green · `go test -race
  -count=3 ./internal/enroll/...` green · `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-no-inline-handlers.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` all pass — run once on the
  WIP snapshot and again after the review fixes.
- The three new page tests seen running and passing with `-v`.
- Not re-done: the full driven visual sweep (Tester's, pre-review); the
  busy indicator is `display:none` when idle, so the geometry the e2e spec
  pins is unchanged. The e2e spec was not run here (no Playwright install in
  the review container).

## Verdict

Safe to merge once the orchestrator brings the review fixes in (finding 1 is
the one that matters). `lang-pack-drift` goes red on `main` for the two
brand-new keys, as expected for a core-first merge; the de/es/pt pack PRs
are the owed same-cycle follow-up.

## Deferred

- R1: in-place confirmation when the till is currently registered (above).
- Finding 4: `Effective` store id after a refused pair (one-liner + test).
- ADR-0116 D6 status chip ("Removed from the shop's cloud account" / "This
  till needs pairing again") linking to this control — separate card.
