# Code review — decide: self-order mode and sessions on other devices (ut-docs#1301)

- **Date:** 2026-08-30
- **Branch:** `fix/1301-self-order-other-sessions-decision`
- **Reviewer:** independent reviewer (Opus, different model from the Sonnet
  implementer — `complexity:medium` per the model-routing table), fresh
  context, no prior involvement in the change.
- **Verdict: SAFE TO MERGE.** One blocking finding — the decision reversed
  this ticket's own grooming recommendation without using the escape hatch
  that recommendation defined — fixed before merge by posting the required
  note on the issue and rewriting the rationale to rely on verifiable,
  in-repo facts instead of an unsupported vendor-precedent claim. A second,
  related blocking finding (same root cause) was fixed the same way. One nit
  (unnecessary PNG regeneration) also fixed.

## What shipped

Follow-up from the `ut-docs#1259` review
(`docs/code-reviews/2026-08-30-self-order-mode-revoke-session.md` finding
NB-2). `POST /api/settings/display-mode` already revokes the *acting*
browser's session when switching a till into `display.mode=self_order`
(ut-docs#1259) but leaves every other live session on that same till alone —
an undocumented, untested choice. ut-docs#1301 asked the pipeline to decide,
document, and pin the behavior with a regression test.

**Decision: keep current behavior (a) — revoke only the acting session.**
Documented as a comment on the `display-mode` handler
(`internal/pages/settings_page.go`) and on the new test, and as a comment on
the issue itself (see below — the *process* miss this review caught).
Pinned by a new test, `TestSelfOrderMode_DoesNotRevokeOtherSessionsOnTheTill`
(`internal/pages/self_order_mode_test.go`): logs in two independent
managers (distinct users, distinct PINs, distinct tokens) against the same
mux, switches the till into `self_order` using the first session, and
asserts the second session's token still resolves server-side and can still
reach `GET /settings` — unlike the acting cookie, covered by the neighboring
`TestSelfOrderMode_RevokesActingSessionOnEntry`.

The manual documentation of the device-profile selector as a whole
(`web/help/en/display.md` + `fa`/`ar`/`tr`) is `ut-docs#1302`'s scope — filed
alongside this card for the same #1259 review (finding NB-4) — not
duplicated here; a cross-reference comment was left on #1302 with the
decided wording.

## Independent review findings (Opus, general-purpose subagent, working
directly in the shared checkout — no worktree isolation was requested for
this review, noted as a process gap for next time)

### Blocking 1 — the decision reversed the ticket's own recommendation with no note on the issue

`ut-docs#1301`'s only comment (the grooming pass) explicitly recommended
**option (b)** — revoke every session on the till — under the standing
2026-08-08 "Security first" rule, with an explicit escape hatch: "Architect/
Dev should implement (b) unless design turns up a concrete technical reason
to keep (a), in which case note it on this issue." The Dev step implemented
(a) without reading that comment (it read the issue body via a `get` call
that does not include comments, and never called `get_comments`) and without
posting the required note.

**Fixed:** posted the note on `ut-docs#1301` (see the issue), naming the
concrete technical reason (below) and stating the decision explicitly
against the grooming recommendation, before merge.

### Blocking 2 — the rationale's load-bearing claim was an argument from silence, and contradicted the same ticket

The original code/test comments said "no kiosk-capable POS researched … reaches
into another device's session on a mode change either" as if that were a
research finding that no vendor *does* this. The ticket's own grooming
comment already said the honest version of that fact: "no major POS vendor
publishes fine-grained session-revocation semantics for a kiosk-mode toggle
… so there's no external precedent to defer to here." Converting "nobody
documents this" into "nobody does this" in a permanent code comment, a test
comment, and the commit message overstated what was actually known — and
the documented rationale is this ticket's entire deliverable, so an unsound
central claim doesn't satisfy "document the chosen behavior."

**Fixed:** dropped the vendor-precedent claim from both comments (handler
and test) and the issue note. Replaced with two facts the reviewer verified
directly in this repo:

1. `auth.Middleware`'s `/self-order`, `/api/self-order/*` exemption
   (`internal/auth/middleware.go`) is unconditional, not gated on
   `display.mode` — an anonymous LAN client could always reach that surface.
   The switch changes what a walk-up customer is routed to from `/`, not
   what's reachable; it grants nothing against a session on a different
   device that a direct request to `/self-order` didn't already grant.
2. Any forgotten session is already time-bounded by the idle-lock default
   (`common.DefaultIdleLockMinutes = 10`), enforced server-side on every
   `Resolve`, independent of this till's display mode.

Neither of these changes for a session on a different device before vs.
after the switch, so revoking it too would cost real UX (kicking a manager
off their own laptop for a switch made elsewhere) without closing a gap
this change actually introduced.

### Non-blocking, filed as a follow-up (already handled)

- **NB-2 (reviewer's numbering)** — the ticket's AC references "ut-docs#1300"
  for the manual doc, which is a closed, unrelated ADR PR (a copy-paste
  numbering slip in the original ticket text); the actual manual card is
  `ut-docs#1302`. Reviewer flagged that #1302 is already `status:in-progress`
  and the delegation wasn't recorded anywhere, risking a silent gap if #1302
  lands first without the #1301 outcome folded in. **Fixed:** cross-reference
  comment posted on #1302 with the decided wording, and #1301's close-out
  comment records the delegation explicitly.

### Nit, fixed

- **N-1** — `web/help/img/en/sell.png` was regenerated by the first
  `make docs-shots` pass even though the comment-only Go change doesn't
  change any rendered screenshot. Reviewer proved this by restoring `main`'s
  PNG bytes and confirming `guard-docs-shots.sh` still passes once
  `manifest.json`'s `surface_sha256` is bumped alone (same pattern as
  precedent commit `7fdf8f88`, ut-docs#1303, which touched the same Go file
  and updated only `manifest.json`). **Fixed:** restored `main`'s
  `sell.png` bytes; recomputed and applied just the surface hash via
  `GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash scripts/ci/guard-docs-shots.sh`
  rather than re-running the full screenshot suite for a second, larger
  comment edit.

## Verification performed

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | pass / pass / empty |
| `go test ./internal/pages/... -run SelfOrderMode -v` | 6/6 pass |
| `go test ./internal/auth/...` | pass |
| `go test ./internal/pages/...` (full package) | pass |
| `go test ./...` (whole repo) | pass |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-kiosk-engine.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass |
| `bash scripts/ci/guard-compliance-claims.sh` | pass |
| `bash scripts/ci/guard-docs-shots.sh` | pass |
| `bash scripts/ci/guard-help-topics.sh` | pass |
| `bash scripts/ci/guard-plugin-menu-read.sh`, `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`, `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`, `check-brand-assets.sh`, `guard-makefile-version.sh` | all pass (reviewer's independent run) |

### TDD re-verification (reviewer, independent, twice)

**Would the new test catch option (b)?** Temporarily added a loop to the
handler revoking every user's sessions (simulating (b)); the new test went
red for exactly the expected reason:

```
--- FAIL: TestSelfOrderMode_DoesNotRevokeOtherSessionsOnTheTill (0.44s)
    self_order_mode_test.go:725: a different device's session must survive
    another device switching this till into self_order mode
```

Reverted; green again. Separately confirmed each of the test's two
assertions is independently load-bearing (neither is decorative) by muting
one at a time and re-running the inverted probe.

**Are the two sessions genuinely independent?** Temporarily asserted the
tokens and resolved users differ — confirmed distinct users, distinct PINs,
distinct random tokens, not a same-session artifact.

**Is `settings_page.go`'s change genuinely comment-only?** Reviewer stripped
comments from both `main`'s and the branch's version with a string-aware
tokenizer and diffed: 1176 code lines each, 0 diff lines.

## Checked and found clean

- No raw SQL outside `internal/data`/tests (`guard-data-access`).
- No new user-facing string (`guard-i18n`) — comment/test-only change plus a
  manifest hash bump.
- No new route; no locale file touched (`guard-help-topics` structural
  check unaffected — the content-level manual gap is #1302's separate scope).
- `register`/`backoffice` modes unaffected — this change touches only the
  `self_order` branch's surrounding documentation, no logic.
- Audit behaviour from `ut-docs#1303` (the dedicated
  `self_order_session_revoked` audit entry) untouched.
- `manifest.json`'s hash bump is genuinely required: reverting it alone
  (with the comment changes still in place) makes `guard-docs-shots.sh` fail.

## Merge

`merge_method: "merge"` (never squash/rebase — ut-docs#250), after CI is
green on the PR.
