# Review — Android update: installer closed or never shown → tap to open it again (ut-docs#4495)

**Reviewer:** independent Fable subagent (the change was written on Opus 5.5).
**Verdict:** safe to merge.

## What shipped
- `web/public/android-update.js`: new `closed` state. Every update button shows
  `data-again` ("Didn't see Android's installer? Tap to open it again") and is
  re-enabled. The page synthesises `closed` when `installing` gets no further
  report within 12 s, and on `visibilitychange` back to visible. Android's
  installer is a see-through dialog activity, so `visibilitychange` alone never
  fired. That left the check-result button disabled on "Opening Android's
  installer…" for good, which is the reported bug.
- `MainActivity.kt`: `updateInstallerLaunched` is set once `startActivity` hands
  the APK over. The next `onResume` reports `closed`, because an install kills
  the process, so a resume means no install happened. A new attempt clears the flag.
- Chip (`base.html`): `closed` makes the next tap a direct retry, like `failed`.
  The chip's and the Settings form's own `visibilitychange` handlers are removed.
  The shared `closed` report covers both.
- `data-again` added on the chip, the Settings form and the check-result button
  (`update_api.go`, pinned by `android_update_placement_test.go`).
- New key `settings.update.android.install_again` in en/ar/fa/tr and in
  `en.meta.json`. Pack PRs: ut-plugin-language-de#456, es#455, pt#103→#105.
- Manual: `web/help/{en,ar,fa,tr,de}/updates.md`. Step 4 covers the new retry
  text and step 3 the Play Protect "hasn't seen this app" notice (the card's side note).
- Playwright `e2e/tests/android-update-installer-closed-4495.spec.ts`.

## Findings
1. Minor, **fixed**: a stale `updateInstallerLaunched`. If Android silently
   refuses to launch the installer (no pause), the flag survived into the next
   attempt. A resume during that attempt's download then reported `closed`
   for about 1 s. The flag is now cleared when `downloadAndInstallUpdate`
   starts a new attempt.
2. Minor, **fixed**: an 8 s grace could fire while the installer's staging or
   Play Protect scan is legitimately in front. Raised to 12 s (harmless either
   way, because the installer covers the page).
3. Minor, **fixed**: the German text was 71 characters against `max_length` 70.
   Shortened to "Android-Installer nicht gesehen? Zum erneuten Öffnen tippen" in
   the de help and the de pack PR.
4. Nit, accepted: after `closed`, the sale-screen chip retries on one tap
   without the two-tap confirm. This matches the existing `failed` retry, and
   the tap only reopens Android's own confirm dialog.

## Verified
- gofmt, vet, `go test ./internal/pages/ -run 'Update|Android'`, guard-i18n,
  i18n-meta, help-topics, help-drift, compliance, naming, RTL, no-showmodal.
- TDD: the spec fails 2/3 on `origin/main`'s android-update.js (the button stays
  disabled on the installing text) and passes 3/3 with the fix. The reviewer
  re-ran this in a separate worktree, and it was re-run after the review fixes.
- Packs: `validate.sh` passes, and `check-key-drift.sh` reports 0 drift against
  this branch's en.json. Their CI `key-drift` stays red until core lands
  (brand-new key).
- **Not verified here:** the Kotlin compile (no Android SDK in this lane;
  `android-ci.yml` builds it) and a real tablet run. The device check from the
  card (cancel the installer, then the button must be tappable and say "open it
  again") is still owed. It is a follow-up on the card.
