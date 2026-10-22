# Review — pair: own message when the till's cloud identity is env-pinned (ut-docs#4062)

**Change.** `POST /api/enrol/pair` maps `enroll.ErrPairIdentityPinned` to the new
`settings.enrol.pair_identity_pinned` message (en/tr/ar/fa + `en.meta.json`)
instead of the generic `pair_failed`, which sent the operator to retry a code that
never works. Test through the real mux: env-pinned store id → new text, no cloud call
(fails without the handler change).

**Author:** Sonnet 5.5. **Reviewer:** Opus 5.5 (fresh context), one round.

**Findings (all low, all fixed):** stale doc comment on the sentinel; meta text
claimed "new code will not help" while en text didn't say the code was unused
(meta reworded); ar/fa wording used a term inconsistent with the rest of the locale.

**Verified.** `go test ./internal/pages ./internal/enroll ./web`, gofmt, i18n,
i18n-meta, RTL, compliance guards green. Docs-shots surface hash refreshed (error-path
text only, no rendered pixel in the manual changes). No help-topic change: help does
not document the sibling pair refusals.

**Follow-up.** `ut-plugin-language-{de,es,pt}` PRs for the new key, same cycle.
