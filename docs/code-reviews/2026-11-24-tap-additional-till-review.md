# Code review: Tap to Pay on an additional till (universal-till#2701), combined Reviewer 2026-11-24

Scope: the till half of ADR-0198 Amendment 2026-11-23 (role gate, check-in `till_role`, vouch `role`, refund/reversal guard, settings panel, copy, help). Reviewed together with ut-cloud#1145 and ut-docs#6976.

Pre-flight: build, vet and affected package tests (pages, paybroker, enroll, cloudsync, pos) run on the branch; the new test fails without the fix and passes with it.

Findings
- F1 (medium, FIXED): a joined till in self-order display mode reports `kiosk` to the cloud (the cloud narrows the credential) but the local gate (`tapJoinedRole`) still read the roster/setting `additional` and offered a tap the cloud then refuses. `tapJoinedRole` now answers kiosk for `display.mode = self_order`. Test `TestTapOffer_AdditionalTillInSelfOrderModeIsAKioskNoTap` (fails before, passes after).
- F2 (low, ACCEPTED): `RoleUnrecordedRecently` is process-wide, so a role refusal followed by an unrelated tap start failure within 10 minutes (and no successful broker call) shows "set this till up again"; `device_role_unconfirmed` (self-heals at the next check-in) reads the same. Cosmetic, fail-closed, no money path.
- Checked, no finding: explicit-role gate with no fallback to additional; offline sale still refused; kiosk actor keeps its own source; refund page and reversal path never send `refund` on an additional till (nothing sent, failed-reversal journal row); phone reader ignored on additional; `till_role` sent only by a replica; vouch role only from the roster, never guessed; English-only `en.json` + `en.meta.json`; help topic updated.

Verdict: MERGE after CI (go-live order: ut-docs#6976 first, then this and ut-cloud#1145).
