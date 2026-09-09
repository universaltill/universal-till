# Step-up manager-PIN gate for the four destructive Settings → Data actions (ut-docs#1841)

- **Date**: 2026-09-09
- **Card**: ut-docs#1841 (`p2`, `security`, `source:user`, `pilot:germany`, `complexity:medium`)
- **Lane**: `lane:local`
- **ADR**: ADR-0087 (new; builds on ADR-0052)
- **Repos touched**: `universal-till`, `ut-docs`, `ut-plugin-language-de`, `ut-plugin-language-es`
- **Model routing**: Dev = Sonnet subagent, independent review = Opus subagent (isolated worktree)

## What shipped

Catalog cleanup, reset transaction history, archive purge and archive restore each confirmed by typing a magic word (`CLEANUP`, `RESET`, `PURGE`, `RESTORE`). They now require a manager PIN re-entered at the moment of the action.

- `internal/pages/elevation.go` — new `checkStepUp`, plus `verifyElevationPIN` factored out of `checkOrElevate` so the two share one PIN path.
- `internal/pages/data_api.go` — the four handlers; new `elevationActors` helper resolving the audit actor/blocked-actor pair.
- `internal/data/{pos_repo,reset_archive_repo}.go` — `blockedActorID` threaded to the four mutations; new uncapped `CountObsoleteItems`.
- `web/ui/pages/settings.html` — the four typed-word inputs removed; the four raw `fetch()` calls routed through the existing `window.utPostWithElevation`.
- `web/locales/{en,ar,fa,tr}.json` + the `de`/`es` packs — four `elevation.summary.data_*` keys added, ten typed-word keys removed.
- `web/help/{en,de,ar,fa,tr}/display.md` — items 12–14 rewritten; screenshots regenerated.

## The design finding that changed the approach

The card asked to reuse ADR-0052's `checkOrElevate`. Doing so literally would have made these four actions **weaker than the typed word**, not stronger.

`checkOrElevate` is denial-recovery, not step-up: its first branch returns `allowed` with no PIN whenever `canPerform(action)` is already true. The Settings → Data block is `{{ if .isManager }}`-only *and* every handler re-checks `canPerform(..., "data_management")`, so every session that can reach these handlers is exactly one it waves through. Result: no typed word, no PIN, one click to a permanent catalogue deletion.

The card's cited precedent has the same property — `elevation.summary.remove_demo_data` prompts a *cashier* and never a signed-in manager — which is why the gap was easy to miss. Hence `checkStepUp`, which never short-circuits on `canPerform`.

Recorded in ADR-0087 so the next call site does not reach for the wrong one.

## Research (standing rule: check the market before choosing a behaviour)

Genuinely split, stated plainly: Square re-prompts for a passcode per sensitive task; ready2order and Zettle confirm destructive bulk deletes with a dialog only; SumUp, Toast, Lightspeed and Shopify POS could not be confirmed either way without a paid trial. The primary sources are not split — OWASP ASVS 2.26 and the OWASP Authentication Cheat Sheet both call for re-authentication before sensitive operations, and GitHub's sudo mode is the same pattern.

Shipped unconditionally rather than as a toggle: the security-first rule makes the stricter option the default, and a setting labelled "don't ask for a PIN before permanently deleting the catalogue" is not a merchant preference to offer unprompted. Full write-up on the card.

## Findings

### Found by the orchestrator before review

1. **Blocker — the confirmation understated the deletion.** The prompt's count came from `ListObsoleteItems(ctx, 200)`, which clamps to `LIMIT 200`, while `CleanupObsoleteItems` deletes every matching row. With 350 obsolete items the dialog promised "remove 200" and deleted 350 — understating a destructive action in the sentence that exists to prevent one, and that sentence is now the only safety information the approver sees. Fixed with `CountObsoleteItems`, sharing the same `obsoleteItemsWhere` predicate. Regression test `TestCountObsoleteItems_NotCappedLikeList`.
2. **Fail-closed on a count error.** The lookup was best-effort, defaulting to `0` — "remove 0 product(s)" before deleting many. Now returns 500; a count query that cannot run means the DB is unhealthy, and refusing a mass deletion then is the safe answer.
3. Four verbatim copies of the audit-attribution block collapsed into `elevationActors`.

### Found by the independent Opus review

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Major** | All four new call sites lost the `.catch()` the code they replaced had, and that both existing `utPostWithElevation` callers keep. `utPostWithElevation` guards only its retry leg, so a network drop — on an offline-first product — left a destructive button disabled and "clearing…" on screen until reload. | **Fixed**, all four sites |
| 2 | Minor | Deleting `settings.data.reset_confirm_dialog` silently dropped "the catalog, users, settings and archived reports are kept" — a sentence a previous review (2026-08-12, ADR-0040 §9) had deliberately added at the point of action. | **Fixed** — restored into `elevation.summary.data_reset_transactions` in all six locales, reusing each translator's own wording |
| 3 | Minor | Handlers branched on `== needsElevation` and fell through to the mutation, safe only while `checkStepUp` structurally cannot return `allowed`. A one-token edit reopened the bypass with no compile error. | **Fixed** — now `!= elevated`, so the mutation is unreachable by construction |
| 4 | Minor | i18n guards check key parity, never printf verbs; a pack dropping `%d` ships `%!d(MISSING)` in the blast-radius sentence. | **Deferred** → ut-docs#1865 |
| 5 | Minor | The dominant real case — one manager entering their *own* PIN — was untested; only the differing-approver branch was covered. | **Fixed** — `TestElevationActors_SelfApproval` |
| 6–8 | Nit | False "clearing…" behind the dialog (self-heals via `onCancel`); `PathValue` re-embedded without re-encoding (inert today); count read at prompt time vs delete at retry time. | Accepted, documented |

## Verified beyond automated tests

- **TDD claims re-verified personally, not taken on trust.** Reverting `CountObsoleteItems` to the capped list produced `CountObsoleteItems = 200, want 250`; restored, it passes. Removing the `ApproverID != ActorID` guard produced `blockedActorID = "mgr-1", want ""`; restored, it passes.
- **False-pass probe.** The reviewer swapped `checkStepUp` → `checkOrElevate` at two handlers; both really did mutate (`"archived 1 sales…"`, `"removed 1 obsolete products"`) and five test functions failed, including the audit test on the actual `actor_id`. These assert row counts and audit values, not just status codes.
- **Bypass hunt found none**: empty and whitespace PINs, a JSON body against `ParseForm`, multipart, the dialog's re-POST round trip (batch id rides the URL path; `hidden` is `nil` and no handler reads any other form field), and every non-test caller of the four repo methods.
- Manual prose re-read in English and skimmed in the other four locales; no "type RESET/PURGE/…" instruction survives anywhere.

## Not caused by this change, found while gating it

- **ut-docs#1864** — `TestWindowReports_ExcludeReturns_DeptTillPayments` fails for a few hours nightly: a UTC date key queried against a local-day grouping. Reproduced on `main` at four separate commits. Fixed separately in PR #948; a wider timezone sweep stays open on that card.
- **ut-docs#1863** — `main` is red on `lang-pack-drift`: five core keys from #1818 have no pack translation, and `catalog.stock_untracked` is #1850's pack half landed ahead of its core half. Deliberately not touched — #1850 is live on another lane and deleting the key would destroy its work.
- **ut-docs#1860** — `POST /api/backup/restore` still guards "replace all current data" behind a typed word, and GDPR customer erase has no confirmation at all.
- **ut-docs#1865** — the printf-verb guard gap above.

## Verdict

**Safe to merge** once the review's major finding was fixed — the reviewer's own verdict was "NOT SAFE as-is" on finding 1 alone, and that is fixed here along with 2, 3 and 5.

Merge order matters: the `de`/`es` pack PRs land **before** core, since `lang-pack-drift` is advisory on a PR and blocking on push to `main`.
