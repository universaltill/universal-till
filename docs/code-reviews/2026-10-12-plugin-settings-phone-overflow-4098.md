# Review — plugin settings: takeaway overrides no longer push the page sideways at phone width (ut-docs#4098)

**Date:** 2026-10-12 · **Lane:** lane:cloud-41 · **Built by:** Opus 5.5 (inline) · **Reviewed by:** Fable (fresh-context subagent) — card `complexity:easy`.

## What shipped

- **Cause:** a `<fieldset>` defaults to `min-inline-size: min-content`. The typed takeaway-overrides editor on Plugins → ⚙ Settings (`web/ui/pages/plugin_settings.html`) sized itself to its widest stacked row. The `overflow-x:auto` wrapper inside it never got a narrower box, so the page scrolled sideways: 27px at 360px, 67px at 320px.
- **Fix:** the fieldset gets class `ps-takeaway-fieldset` (`min-inline-size: 0`, `web/public/app.css`). It now shrinks to its `.card` like any block. Above phone width nothing changes: the fieldset still fills its 32rem card.
- **Test:** `e2e/tests/plugin-settings-phone-overflow-4098.spec.ts`. At 320 and 360px, in en, fa (RTL) and tr, it checks two things:
  - the page has no horizontal scroll;
  - the fieldset and every override input sit inside the card.
- **Test seed:** `e2e/seed_faq` stores one `takeaway_rate_overrides` row on the seeded FAQ plugin, string-wrapped like every real writer. The row holds a live code and an orphan (a deleted code's leftover), so the editor renders both row shapes. The phone sweep (`phone-layout-sweep-3297`) never visits a plugin's settings page, so it could not catch this.
- **Docs-shots:** the surface hash is refreshed and the commit carries `Docs-Shots-Unchanged: true`. The `plugins` topic's screenshotted route is `/plugins`, which this change doesn't repaint.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The docs-shots surface hash was not refreshed, so `run-docs-shots-guard.sh` exited 1 and the `build` job would have gone red. | Fixed: ran `update-docs-shots-surface-hash.sh`; commit carries the `Docs-Shots-Unchanged: true` trailer. |
| 2 | minor | The seed stored a raw JSON object. Real writers store it string-wrapped (`data.EncodeMapSettingValue`), and the raw form only works through a legacy fallback. | Fixed: the seed now stores the wrapped form. Spec re-run: 6 passed. |
| 3 | nit | The geometry assertion could also cover the sweep's 480px boundary. | Accepted: the AC names 320/360. |

The reviewer also checked:
- **Other users of the seeded row:** run-till.sh, worker tills, docs-shots and the locale audit. No spec visits the FAQ settings page or asserts "no settings", and `/plugins` doesn't depend on `plugin_settings` rows.
- **Repo rules:** logical CSS only, no new strings, spec imports from `./fixtures`, SQL only in e2e seed tooling.

## TDD re-verification

The reviewer ran the spec in an isolated worktree:
- with the fix: 6 passed;
- with only `app.css` reverted to `origin/main`: 6 failed, with "page is 67px wider" at 320px and "27px wider" at 360px.

Before the fix I had also seen the spec fail with the same numbers.

## Verified beyond automated tests

I took screenshots and looked at them:
- 360px en: before and after;
- 360px fa: RTL, cards stay inside the frame;
- 320px tr: longest labels wrap inside the card;
- 1024px en.

An earlier attempt added `overflow-wrap: anywhere`. At 1024px that broke the table headers mid-word ("RAT E"), so I dropped it before review.

**Not checked:**
- the de pack's wording: the pack isn't installed in core e2e, so tr stood in as the long locale;
- the dark theme;
- real touch hardware.

## Gate

- `go build ./...`, `go vet`, `gofmt` clean.
- `go test ./internal/pages/... ./e2e/...` passed.
- All 99 `scripts/ci/*.sh` guards in `ci.yml`'s `build` job ran on the final commit; 98 passed. `guard-shellcheck-version.sh` could not run because `shellcheck` is not installed in this container. No shell script changed; CI runs it.
- Playwright `default` project: faq, layout-plugin-menu, phone-layout-sweep, sale-only-cashier and the new spec, 96 passed.

**Verdict:** safe to merge.
