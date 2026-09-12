# Code review: a `ut_lang` cookie set once pinned a till to the wrong language (ut-docs#2135)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2135
**Branch:** `fix/2135-ut-lang-cookie-pins-wrong-language`
**Complexity:** medium (Dev: Opus inline, Review: Fable subagent — see "Model routing" below)

## What shipped

A German pilot till rendered an **English sale screen for weeks** while
Settings insisted the language was German. It was first reported, and first
diagnosed, as a missing translation (#2115); the stale language pack was real
and separately fixed, and it was not the cause.

The cause: `httpx.RequestLocale` resolved `?lang=` → **`ut_lang` cookie** →
shop default → `"en"`, and `ResolveLocale` wrote that cookie with
`MaxAge: 31536000` — one year — on any request carrying `?lang=`. The only two
URLs that set it are `/menu?lang=` and `/setup?lang=`, so **clicking through
the setup wizard in English pinned the till to English for a year**, silently
outranking the shop's configured locale on every page. `Accept-Language` is
never consulted, which is why it could not be diagnosed from the client side.

The operator had no move. Settings → Language sets the shop *default*;
`settings.html` said so in its own comment ("Distinct from the `?lang=`/
`ut_lang` cookie"). The one control named "Language" could not change what the
till displayed. Two controls disagreeing, with the operator holding the one
that loses.

**Fix.** The cookie now records the two things an override is only valid
relative to: `"<locale>|<shop default at the time>|<locale generation at the
time>"`. `httpx.LocaleOverride` honours it only while **both** still hold.

- **Shop default moved** → the override is stale. Covers the setup case
  directly: finishing the wizard sets the shop's locale, retiring the
  wizard's own `?lang=` cookie.
- **Generation moved** → the shop has explicitly restated its language.
  `store.locale_generation` is a persisted counter bumped only where an
  operator genuinely chooses: Settings' Language card, a `store.locale` write
  from the all-settings table, and finishing setup. Never by a *derivation* —
  the same line `KeyLocaleConfirmed` already draws, for the same reason.
- **A cookie with fewer than three fields** is pre-#2135 and is ignored. This
  is what makes the pilot tablet heal itself on upgrade rather than waiting
  for an operator to find a control that, before this change, could not clear
  it anyway. The cost is real and deliberate: a pre-upgrade per-browser choice
  is forgotten once and has to be re-picked.

The generation is the half that matters most, and it replaced a weaker first
cut (see finding 3). A `Set-Cookie` clear only reaches the browser that made
the request; the generation retires overrides on **every** browser in the
shop, so a manager can fix a till from their own phone — and so that
re-applying the language the shop is *already* set to does something, which is
exactly what a confused operator does.

## Independent review (Fable subagent — deliberately not the author's model)

The routing table maps `complexity:medium` → review at Opus, on the assumption
Sonnet wrote it. **Opus wrote this one**, so reviewing at Opus would have been
the same model re-checking its own work — the thing the skill's own "why a
different model" section exists to prevent. Reviewed at Fable instead, and the
deviation is recorded here rather than left implicit.

The reviewer ran real code — `go build`, `go vet`, both packages, `-shuffle=on`
×3 — and independently mutated the implementation four ways to prove the tests
fail without it. It found **four real defects**, all fixed before this commit:

1. **The scope handed to the reviewer was incomplete — four existing tests
   broke.** `shifts_api_test.go` (×3) and `setup_language_catalog_test.go`
   carried bare-`"fa"`/`"de"` cookie fixtures, so three shift-summary
   translation tests rendered English and the wizard test saw the new format.
   Fixed; all fixtures now build their value through the single canonical
   `httpx.LocaleOverrideValue`, so the format has one definition.

2. **`/api/settings/upsert` retired overrides on a REJECTED locale**
   (`settings_page.go`). The value is only accepted if it is in
   `AvailableLocales()`, but the post-write `case common.KeyLocale:` acted
   unconditionally — so `key=store.locale&value=xx-typo` changed nothing and
   still wiped the language for every browser. Now guarded on the same
   `slices.Contains` check the write itself uses, matching the Language card's
   own `localeChosen` rule. Pinned by a new test on **both** endpoints.

3. **Cross-device: the first cut only worked for the browser that saved.** The
   original design cleared the cookie on the save response, which cannot reach
   a till the manager is not standing at, and does nothing when the shop
   default has not moved. The reviewer called AC1/AC3 only partly met. Rather
   than document the gap, the design was changed: the persisted generation
   replaced the cookie clear entirely, which fixes the cross-device case *and*
   removes finding 2's whole class of bug (there is no longer an unconditional
   side effect on the response).

4. **The setup wizard's comment now described the opposite of the behaviour**
   (`setup_page.go`): "a fresh wizard run's own step-1 language detection
   cookie, when present, still wins for this browser." It no longer does — by
   design, since that cookie is precisely what caused this bug. Comment
   rewritten to state the behaviour *and* name its cost: an operator who picks
   a language for themselves in step 1, different from the shop's, has to pick
   it again afterwards.

5. **(also fixed)** `setup_page.go`'s first-boot check tested for the mere
   *existence* of `ut_lang`, which after this change can be true for a cookie
   that is then ignored — a stale jar would suppress OS-language detection and
   leave the wizard in its fallback language with nothing having chosen it.
   Now asks `httpx.LocaleOverride` for validity, not `r.Cookie` for presence.
   This one had a consequence the reviewer did not reach, found by the full
   suite afterwards: `TestSetupWizardDetectionSkippedOnceAChoiceExists` asserts
   that a cookie from an earlier visit suppresses detection, and it was passing
   a bare `"en"`. Under the fix that cookie is ignored, so detection correctly
   fires again. The test's *intent* — "a choice already happened" — is right;
   its fixture was expressing that intent with a value that no longer
   constitutes a choice, so the fixture moved to `LocaleOverrideValue`. A new
   test, `TestSetupWizardDetectionStillRunsForAnIgnoredLanguageCookie`, pins
   the corrected meaning directly for both an ignored-legacy and an
   ignored-stale cookie.

Checked and found sound by the reviewer: cutting at the **first** separator
(so `?lang=en|de|7` cannot forge the trailing fields — now pinned by its own
test), `|` being a legal cookie byte, the atomic `DefaultLocale()` reads, no
new injection surface (the value reaches templates only as an
attribute-escaped `<html lang>`, and the issue-report path still clamps to
`isAvailableLocale`), and no repository/money/i18n rule violations.

## A sixth call site, found by CI after merging main

`items_page.go` synthesizes a `ut_lang` cookie for an in-process sub-request
rather than copying the caller's, so a first-ever `?lang=fa` visit renders the
embedded panel in Persian too (ut-docs#2114). It built that cookie as a bare
locale, which this change treats as a stale pre-#2135 value and ignores — so
the panel silently fell back to the shop default, undoing #2114.

It was invisible until `main` was merged in, because #2114 and its test landed
on `main` after this branch was cut: the branch was green, `main` was green,
and only the combination was broken. Fixed by building that cookie through
`httpx.LocaleOverrideValue` — which is exactly why that helper is exported and
documented as the single definition of the format. Caught by the full suite
locally and by CI's own `internal/pages` job, independently.

The general lesson, and the reason this is written down rather than just
fixed: changing a cookie's *format* is not confined to the code that reads it.
Every place that WRITES one has to move too, including the ones that
synthesize a request rather than serve one. The reviewer swept readers of
`ut_lang`/`RequestLocale`/`ResolveLocale` thoroughly and still missed this,
because it is a writer, and because its test did not exist on this branch yet.

## TDD verification, re-done personally on the shipping code

The reviewer's own four mutations were run against the *first* design. After
that design was replaced (finding 3), they no longer proved anything about
what ships, so the whole exercise was re-run against the final implementation.
Each mutation was applied in place, the affected packages re-run, then
restored:

| mutation | fails, and only this |
|---|---|
| drop the generation check | `TestLocaleOverrideIgnoredOnceTheShopRestatesItsLanguage`, `TestSaveSettings_LocaleRetiresAStaleOverrideOnEveryBrowser` |
| drop the shop-default check | `TestLocaleOverrideIgnoredOnceShopDefaultChanged` |
| honour legacy/malformed cookies | `TestLegacyLocaleOverrideIsIgnored` |
| make the upsert bump unconditional | `TestUpsertSettings_RejectedLocaleLeavesOverridesAlone` |

Restored afterwards and re-confirmed green. The tests are real, and each one
fails for its own reason rather than everything failing together.

Full suite: **`GO_TEST_RC=0`, 57 packages**, run with nothing else touching the
tree.

**Two process failures worth recording, because both produced a confident
wrong answer.** A full `go test ./...` was run *while* the review subagent was
mutating the same working tree, and its failures were briefly taken at face
value; the review agent should have been given `isolation: "worktree"`, which
its own skill prescribes for revert-then-restore verification. Same class as
the known shared-e2e-port collisions — a concurrent session produces failures
indistinguishable from real defects. Separately, an earlier "suite is green"
reading came from a pipeline whose exit code was `echo`'s, not `go test`'s,
with the real output truncated by `head`; every suite result quoted here is
`GO_TEST_RC` captured from the command itself.

## Verified against the real app, not only tests

Driven against a running till (each expectation stated before the run):

| scenario | expected | result |
|---|---|---|
| legacy `ut_lang=en`, shop `tr` — the pilot tablet's exact state | `tr` | 0× *Hold Sale*, 4× *Satışı beklet* |
| `?lang=en` writes the new format | `en\|tr\|1` | `en\|tr\|1` |
| till holds a live override | English | 4× *Hold Sale* |
| manager re-applies `tr` **from another device**, shop default unmoved | till flips | 0× *Hold Sale*, 4× *Satışı beklet* |
| rejected locale `xx-not-real` | override survives | 4× *Hold Sale* |
| restart, previously-valid cookie | still honoured | English — generation persisted |
| restart, no cookie | shop default | Turkish |

The cross-device row is the one the first design could not do.

**Not verified on the pilot tablet.** It runs v0.14.12; this needs a new
release to reach it, which is a separate decision and has been raised rather
than assumed. Acceptance criterion 6 is therefore **not yet met** — and given
this card exists *because* reading rendered HTML over the API hid the bug for a
whole cycle (the server said German while the device showed English), the
device screenshot is the evidence that actually counts here.

## Deliberate non-change

`Accept-Language` is still never consulted. The card names it as diagnostic
context, not a requirement, and making a WebView's OS language outrank a
shop's configured locale would re-create this bug from a different direction.
