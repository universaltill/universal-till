# Review: till credheal store_purged + undo date (ut-docs#2630)
Shipped: credheal `store_purged` (final, needs person); `restorable_until` carried in Detail; deleted-shop sentence with date; Download your data + Check again; en.json/en.meta.json only (legal draft).
Reviewer: Opus 5.5 (independent). Findings: fixed-date guard (fixed, dates moved to 2031); chip Href changed (fixed: ButtonHref added, chip keeps /tills?tab=this); CheckAgain ran in waiting/healing (fixed: only needs_person+store_deleted); offline Check again drops banner (accepted); stale date on repeat signal / enroll.Recover path (accepted, falls back to old sentence); no sameOrigin check on POST (accepted, manager gated); help topic not updated (deferred, with legal wording OK).
Tester (Sonnet, #2630): narrow tests pass. Pre-flight: narrow tests only, build/vet clean. Verdict: approved after fixes.

## Ship review (Fable, independent, 2026-11-28)
Verdict: changes requested; all fixed. Findings: (1) CheckAgain race in the loop: fixed, the loop re-checks needs_person+store_deleted; (2) CheckAgain untested: fixed, TestCheckAgainOnlyForDeletedShop; (3) stale help topic web/help/en/claim.md: fixed; (4) store_purged final reason in uploader untested: fixed, TestFinalRevokeReasonsIncludesStorePurged; (6) past restorable_until shown as live: fixed, with test; (7) date format: fixed, FormatDateTime. Accepted: (5) Detail plumbing covered only by the parse test; (8) status-bar pill links to /tills?tab=this by design; (9) banner render/handler not browser-tested; (10) wording has no Check again hint (legal draft, owner review); (11) as before.
TDD claims re-verified by mutation (reviewer: classify, parse, date branch; tester: new tests).
Pre-flight: narrow tests + full gate (results/ship-till.md).
