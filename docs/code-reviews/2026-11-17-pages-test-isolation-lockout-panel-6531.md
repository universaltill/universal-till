# Code review: lock-out panel test isolation (ut-docs#6531, related ut-docs#5145)

- Repo: universal-till, branch `fix/pages-test-isolation-lockout-panel`, one commit (9c7b0817b), test-only: `internal/pages/reconnect_5145_test.go`.
- Reviewer: Sonnet 5.5, combined review with the tester notes. No subagent (a 7-line test-only diff). Deliberately not a different model from the author; the diff is small enough that this is accepted.
- Date: 2026-11-17

## What shipped
The negative checks in the lock-out panel tests matched the prose "Reconnect this till". Settings renders the v0.36.1 release notes under About, and those notes quote that phrase, so the checks false-positived. They now match `hx-post="/api/enrol/reconnect"`, plus a comment saying why.

## Findings
- Marker is unique to the real panel (verified, no finding). In the shipped tree the string `hx-post="/api/enrol/reconnect"` is emitted only by `web/ui/pages/settings.html:120`, inside `{{ if .reconnectPanel }}`. It is absent from `web/release-notes/` and `web/help/`: no `hx-post` appears in v0.36.1.md or claim.md. Release notes are Markdown rendered to HTML, so even a code span would be escaped (`&#34;`) and could not match the raw attribute string.
- Positive control (verified). Line 129 asserts the same marker is present when locked out, so a template rename would fail that test rather than silently pass the negatives. The tester's mutation (`tillLockedOutAnonymous` returns true) makes all three negative tests fail.
- Comment accurate (verified): the release notes quote the phrase and Settings renders them. The wording "assert on the panel's own markers" is slightly loose, since the negatives now use the htmx marker and the `data-testid`. Not worth a change.
- Other prose negative checks in `internal/pages` (low, accepted). I grepped negative `strings.Contains(..., "Prose")` on page bodies. Settings-body ones: `diagnostics_settings_test.go:347` ("Diagnostic mode"), `country_settings_page_test.go:114,167` ("FR", "Show only my country"). None of those phrases is in `web/release-notes/`; "Diagnostic mode" and "Show only my country" occur only in `web/help/`, which Settings does not render inline (help is a link). Latent risk: a future release note that says "Diagnostic mode" or contains "FR" as a substring would break them, the same class of bug. Not fixed here. A general fix (render Settings with the About card or release notes stubbed) is a separate change, and "FR" is the most fragile. Deferred as an epic checklist line, not a card (under a day).
- No real client or shop names, and no secrets, in the diff.

Pre-flight: help/docs-shots n/a (test-only), en.json/en.meta.json n/a, README n/a, review record updated.

## Verdict
Approve. The fix is correct and minimal, and the tester mutation shows the checks still bite. The full `./internal/pages` package was already green three times (run by the orchestrator, not re-run here).

## Deferred
- Harden the Settings-body negative prose checks (`FR`, `Diagnostic mode`) against release-notes content.
