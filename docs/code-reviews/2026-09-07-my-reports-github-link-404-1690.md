# Review: /my-reports no longer links operators into the private tracker

**Card:** universaltill/ut-docs#1690
**Branch/PR:** `fix/1690-my-reports-github-link-404`
**Complexity:** easy — Dev at Sonnet (inline), Review at a fresh-context Sonnet subagent (isolated worktree)

## What shipped

`/my-reports` (manager-gated) used to render a **View on GitHub** link next
to any report that had been filed as a ticket. The target,
`universaltill/bug-reports`, is private by design (it holds user
transcripts/recordings/store detail). A shop operator following the link —
signed out of GitHub, or signed in as themselves — got a bare 404, and the
URL itself leaked which org/repo we file their reports into.

Per the ticket's own recommendation (option 1): stop rendering the link to
operators entirely. The status chip already carries the real answer
(*Open — being worked on* / *Fixed* / *Closed — not planned* / *Filed on
GitHub* / delivery status before that) and is unaffected.

- `internal/pages/my_reports_page.go`: the page-level `myReportRow` view
  model no longer carries `GithubIssueURL` at all — it can't leak back into
  the template by accident. `internal/data/issuereports_repo.go` and
  `internal/cloudsync` keep storing/syncing it untouched (still useful for
  a possible future read-only hosted status page, ut-docs#1418 — explicitly
  out of scope here).
- `web/ui/pages/my_reports.html`: removed the conditional anchor block.
- `internal/pages/my_reports_page_test.go`: the two tests that used to
  assert the link rendered now assert it does **not**, even when the
  underlying DB record carries a `github_issue_url` (seed data
  deliberately keeps one, to prove suppression rather than mere absence).
- `web/help/{en,fa,ar,tr}/my-reports.md`: dropped the now-false "a View on
  GitHub link appears" / "follow the link to see it" lines in all four
  shipped locales; `make docs-shots` regenerated to refresh the topic's
  content hash.
- `web/locales/{en,ar,fa,tr}.json`: removed
  `issuereport.my_reports.view_on_github`, orphaned by the template change
  (flagged by the independent review, not part of the original diff).

## Independent review

A fresh-context Sonnet subagent (isolated worktree — never touched this
checkout) reviewed cold, with no prior reasoning carried over. It:

- Built (`go build ./...`, `go vet ./...`), formatted (`gofmt -l`), and ran
  `golangci-lint run ./internal/pages/...` — all clean.
- Ran the full `TestMyReportsPage*` suite — all pass.
- Ran `guard-i18n.sh`, `guard-help-topics.sh`, `guard-data-access.sh`,
  `guard-page-http-error.sh`, `guard-compliance-claims.sh`, and (with the
  environment's pre-installed Chromium) `guard-docs-shots.sh` — all green.
- **Independently TDD-verified** the regression-test claim rather than
  trusting it: reverted `my_reports_page.go` + `my_reports.html` to `main`
  (keeping the new test file), re-ran the two rewritten tests, and got a
  genuine failure — `must never render a link to the private bug-reports
  tracker`, all three ticket-state subtests failing too — then restored
  the fix and confirmed a clean pass again.
- Grepped the whole repo for `GithubIssueURL`/`view_on_github` to confirm
  nothing else still depends on the removed field, and confirmed all four
  manual locales were edited in structurally parallel fashion (not just
  `en.md`).
- Checked for the two recurring bug classes (missing `os.MkdirAll`, a
  cwd-relative path instead of `paths.Data(...)`) — not applicable, no
  file writes in this diff.
- Checked for real client/shop names or secret-shaped literals — none.
- Flagged one non-blocking finding: the `issuereport.my_reports.
  view_on_github` locale key was now unreferenced in all four locale
  files. **Fixed** in a follow-up commit on this branch (removed from
  en/ar/fa/tr together; `guard-i18n.sh` re-run clean).

**Verdict: SAFE TO MERGE** (no blockers found).

## What was verified beyond automated tests

- The status chip is unaffected — no new empty state, no layout change,
  nothing added (a pure removal), so no RTL/logical-CSS concern.
- The two unrelated-looking screenshot PNG diffs (`ar/sell.png`,
  `ar/till-designer.png`) are pre-existing docs-shots run-to-run
  PNG-encoding noise (see `2026-08-24-docs-shots-determinism-930.md`), not
  a real visual regression — `guard-docs-shots.sh`'s content-hash check
  passed either way, and `/my-reports`' own screenshots are unchanged
  (the fixture data never populated a filed report with a link visible in
  frame to begin with).
- Data layer/cloudsync scope boundary respected: `GithubIssueURL` still
  flows from the cloud into SQLite exactly as before, ready for a future
  option-3 hosted status page — this card only stops it reaching the
  operator-facing template.

## Deferred / explicitly out of scope

- Option 3 from the ticket (a real, sanitized, cloud-hosted status page
  instead of a dead link) — noted in the ticket itself as future work
  overlapping ut-docs#1418, not blocked on this fix.
