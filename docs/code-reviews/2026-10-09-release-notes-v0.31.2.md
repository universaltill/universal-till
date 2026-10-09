# Review: release notes v0.31.2

**Shipped.** Owner-facing "What's new" notes for v0.31.2 in en/de/ar/fa/tr
(`web/release-notes/*/v0.31.2.md`), covering the cards merged since v0.31.1:
incoming-version release notes before an update (ut-docs#3940), plugin
actions keeping typed input (#3879), plugin suggestion thumbnails (#3957),
Settings refusals no longer raising the page-wide banner (#3247), the
pending-tax wording (#3243, #3974), joined tills' no-sale drawer opens
reaching the main till (#3562), and plugin panel forms under slot-only
permission (#3963). Omitted as not shop-visible: #3052 (CI guard), #3698
(developer read view).

**Author:** Opus 5.5 subagent (drafted and translated from each card's
close-out). **Reviewer:** independent Sonnet subagent, which checked each
bullet against the close-outs and the merged code, and UI labels against
`web/locales/*.json` / `web/help/de`.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | #3963 bullet said "another main screen", which reads as covering the setup-wizard panel; that slot has no host gate and still fails closed | fixed in all five languages: names the gated screens (`pluginSlotGates`: Reports, End of day, Settings, Administration, item edit window) |
| 2 | low | #3974 bullet omits the ~15-minute staleness detail | accepted; owner-level wording is accurate |
| 3 | low | #3879 bullet doesn't mention the no-JS refill exception for secret/file fields | accepted; edge case |

**Verified:** `go test ./internal/releasenotes/` (incl.
`TestBuiltin_NotesAreOwnerLanguage`), `guard-release-notes.sh v0.31.2`, the
compliance-claims and competitor-naming guards; headings match each locale's
v0.31.1 file.

**Verdict:** safe to merge.
