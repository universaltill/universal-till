# Review record: till reports install result key (ut-docs#6975, root cause 4)

Scope: internal/cloudsync (ReasonError, result data.message_key, migration 116), internal/pages hook wrapper.

Reviewer: independent Fable subagent (different model from the author), read-only, one round over all three repos; findings triaged below. Tester evidence: unit and handler tests listed in the PR; no device run (see PR body).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | high | A re-posted stored install result dropped the message key (first post failed, directive re-delivered) | fixed: till migration 116 stores message_key with the result; test TestTick_InstallPluginKeySurvivesRepost |
| 2 | high | "Per till" was whichever till answered first; older failures from another till stayed listed beside a newer success | fixed: only the till that answered the newest resolved directive is listed; per-till targeted directives deferred (ut-docs#2682) |
| 3 | medium | Failure reason hidden when the failing reporter is a legacy caller or a removed till | fixed in the card: the listing reason shows whenever state is failed and no listed till failed; test added |
| 4 | medium | Older tills still send raw error text, stored and served as the result | fixed: a failed install without a valid key stores a fixed line instead; test added |
| 5 | medium | Tests missed the wiring, a path-bearing error, installTills | fixed: wiring test, fixed-message test, installTills test, handler assertions |
| 6 | low | Older per-till rows shown while a retry is queued | fixed: rows hidden while queued |
| 7 | low | Till key set and my. map can drift | accepted: unknown keys read as a generic line; backlog note in PR |
| 8 | nit | unused variable in a test | fixed |

Pre-flight: walked reviewer/PREFLIGHT.md; TDD re-verified for the hook wiring (reverting it to cloudInstallPlugin fails TestBuildCloudHooks_InstallPluginReportsReason). Verdict: fixes applied, deferred items in the PR body.
