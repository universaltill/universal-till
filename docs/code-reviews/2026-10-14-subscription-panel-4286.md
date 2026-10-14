# Review: Renew / Choose-a-plan panel on the till (ut-docs#4286)

**Owner decisions (2026-10-14):**
- Pressing Renew or Upgrade first refreshes from the cloud, so an admin-side change shows at once.
- Payment is handed off by a QR code inside the panel (the owner picked "QR + in-app sheet").
- The till never turns into a browser or navigates away.
- The in-app "Open here" sheet is a separate card, #4298. Checkout itself is #678.

**Shipped:**
- **"Renew or change plan"** (managers and admins) on the Subscription card (every state, so an active plan can upgrade), the lapsed banner and the status-bar chip. It opens a non-modal panel (`.show()` over `#ut-scrim`, no `showModal`). The status bar stays reachable, Esc or ✕ close it, and focus goes in and comes back.
- **Step 1, `POST /api/subscription/panel`:** `refreshEntitlementNow` first. If the plan is active, it shows "Paid until …", Change plan and Close, with no QR. Otherwise it offers Renew current plan (only when there is a paid plan) or Choose a plan. If the cloud can't be reached, it says so and still offers both.
- **Step 2, `POST /api/subscription/panel/handoff`:** a QR code and the URL text for `<webBase>/plans?intent=&lang=&store=`. ut-cloud#462's `/plans` redirect forwards this to my.'s plans page (ut-my-shop#70 reads `?intent=`).
- **`POST /api/subscription/panel/poll`:** runs every 20 s, at most 30 times, and flips the panel to active as soon as its own check-in lands.
- **`internal/enroll/plans_url.go`:** `WebBase` (the claim URL now uses it too, unchanged byte for byte), `CloudLocale` (a whitelist mapped to the cloud's locale form) and `PlansURL`.
- 15 new keys in en, tr, ar and fa. The `claim.md` help is updated in 5 languages.

**Tests:**
- 11+ handler tests: refresh first; already renewed shows no QR; lapsed choices; offline; no paid plan; the exact escaped URL; cashier refused; unregistered; the poll count and limit, including a forged `n`; the poll flips on its own check-in; an elevated handoff doesn't poll; check-in throttling; button gating; no `showModal`.
- `plans_url_test.go`.
- e2e `subscription-panel-4286.spec.ts`: 1024×600, 360 px, and the chip with Esc.

**Tester:** mutation-checked (one gap found, fixed).
- Fixed: the new poller is now classified as background, so it doesn't keep an idle session alive.
- Fixed: a renewal showed only on the 2nd poll; it now flips at 20 s, test first.
- Driven against a stub cloud: all three openers, the QR URL, already renewed with no QR, the status bar clickable, focus return, no console errors.
- Looked at 1024×600 and 360×740, light and dark, in en, fa (RTL), tr and ar.
- **Not looked at:** German on screen (the pack wasn't installed), real touch hardware, kiosk mode, a real phone scanning the QR.

**Independent review:** Fable (built by Opus 5.5). Approved with 4 low findings, all fixed with failing tests first:
1. **PIN-elevated cashier:** a cashier let in by a manager PIN no longer gets a 20 s poller that would stop in confusion. The poll-stopped guidance is shown straight away.
2. **Server-side throttle:** `refreshEntitlementNow` kicks at most one check-in per 5 s and answers from the cache otherwise. This also covers #4285's Check now.
3. **a11y:** no `aria-live` on the panel body (it re-announced every poll); each step's lead has `role="status"`.
4. **CSS:** a `vh` fallback before each `dvh` rule (older WebKitGTK on the Pi).
- Verified OK: the elevation gates, the background-poll path still needs a session, the claim URL is unchanged, URL encoding, no htmx inheritance trap, never a sale gate.
- **TDD re-verified:** with the source reverted, the tests fail to build; restored, they pass.

**Verdict:** safe to merge.
