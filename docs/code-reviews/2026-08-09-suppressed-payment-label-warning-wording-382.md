# Code review: sibling payment-label warning no longer points at a nonexistent "rename the conflicting tender" UI

**Card:** universaltill/ut-docs#382
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), review via a fresh-context Sonnet
subagent. One review round; nothing found needing a second.

## What shipped

`warnPaymentMethodAnomalies` (`internal/plugins/plugins.go`) has two
warning loops. Ut-docs#170 already fixed the first (`FindOrphanedPaymentMethods`
→ `orphanPaymentMethodWarning`), which told the operator to
"reassign/rename the tender" — no such affordance exists anywhere in
`web/ui/`. That fix deliberately left the second loop
(`FindSuppressedPaymentNameEntries`) out of scope, since ut-docs#170 only
quoted the first message's text — but the second loop had the exact same
problem: "...pick a distinct label **or rename the conflicting tender**".

Fix:

- **`internal/plugins/plugins.go`**: extracted a `suppressedPaymentLabelWarning`
  helper (mirroring the existing `orphanPaymentMethodWarning` helper one
  function above it) and dropped "or rename the conflicting tender" from
  the message, leaving "pick a distinct label" as the one real remedy.
  The call site switched from `log.Printf(...)` to
  `log.Print(suppressedPaymentLabelWarning(s))`, same shape as the sibling
  loop.
- New test: `TestSuppressedPaymentLabelWarning_DoesNotClaimNonexistentRenameAffordance`
  (`internal/plugins/manager_test.go`) — same structure as
  `TestOrphanPaymentMethodWarning_DoesNotClaimNonexistentRenameAffordance`:
  asserts the message never contains "reassign"/"rename" (case-insensitive)
  and does contain the entry's `PluginID`/`Key`/`Label`/`BlockingID` plus
  "distinct label".

## TDD verification (personally re-verified, not just taken on trust)

Reverted the fix in place (put "or rename the conflicting tender" back
into the format string, function/test otherwise untouched) and re-ran the
new test: it failed exactly as expected —
`warning still points at the nonexistent rename/reassign UI surface
(contains "rename")`. Restored the fix; test green again, full
`internal/plugins` package green.

## Independent review

Fresh-context Sonnet subagent (no prior exposure to this diff's reasoning),
per this pipeline's easy-tier rule (mechanical change → different instance
of the same model is a genuinely independent read here, not the model
reviewing its own reasoning). Findings: **none blocking, none should-fix.**
It independently:
- Confirmed the `fmt.Sprintf` verb/arg pairing is correct and the
  `log.Printf`→`log.Print` call-site change is consistent with the sibling
  helper's pattern.
- Grepped the repo for any other surface (docs, help manual, other log
  lines) still claiming a "rename the conflicting tender" affordance for
  this warning — found none; the only other "pick a distinct label" hit is
  an unrelated install-time validation path (`internal/plugins/manifest.go`)
  that never claimed a rename affordance in the first place.
- Independently reran its own revert-then-restore TDD check and confirmed
  the test pins the regression down.
- Confirmed `go build ./... && go vet ./...` clean, and the targeted test
  green.
- Confirmed this is a `log.Print` startup diagnostic only — never wired
  into a template, HTMX partial, or UI problem chip — so the i18n `{{ T }}`
  rule (which governs `web/ui/**/*.html` and page-local JS shown to a shop
  owner) does not apply here, and verified that reasoning by grep rather
  than asserting it.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on the changed files — all
  clean.
- Full gate: `go test ./...` — all packages green (`internal/plugins`
  49.4s, no new failures).
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all pass (no SQL/kiosk/plugin-menu surface
  touched by this change).
- Confirmed no other repo surface (help manual, docs, other log call
  sites) references the removed wording — this is a server-log-only
  message with no user-facing UI/i18n surface, so no `web/help/` manual
  topic or locale key needed updating.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Safe-to-merge verdict

Yes. Small, well-scoped, TDD-verified both directions, independently
reviewed with no findings, full gate and guards green.

## Deferred / out of scope

None — the card's acceptance criteria are fully met by this change; no
follow-up filed.
