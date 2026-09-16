# Code review — `set_till_setting` directive, till side (ut-docs#2289)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2289 (Phase A slice — till half; companion PR in
  `ut-cloud` reviewed separately)
- **Branch:** `feat/2289-till-settings-directive`
- **Reviewer:** independent pass, Opus subagent — different model from the
  Sonnet implementation, never saw the dev reasoning, re-ran every claim the
  Tester phase made rather than trusting the report.
- **Verdict: SAFE TO MERGE.** One must-fix (premature ADR citation) folded
  in, one stale-doc fix folded in, one defence-in-depth hardening folded in,
  one process fix folded in (missing `Docs-Shots-Unchanged` trailer). Two
  genuinely out-of-scope findings deferred to backlog cards rather than
  silently expanding this diff.

## What shipped

- `internal/cloudsync/cloudsync.go` — new `Hooks.SetTillSetting` field and a
  `case "set_till_setting"` in `apply`'s dispatch, shaped exactly like the
  existing `set_setting` case (nil hook → `"set_till_setting is not
  supported on this till"`, blank key → `"missing setting key"`, otherwise
  the hook's message/error becomes the directive's result). Deliberately a
  **separate** hook from `SetSetting`, which stays the generic unrestricted
  channel and is untouched.
- `internal/pages/cloudsync_wire.go` —
  - `allowedRemoteTillSettingKeys`: the till-side whitelist
    (`printer.receipt_policy`, `receipt.header1/2/3`, `receipt.footer`,
    `kiosk.idle_reset_seconds`), built from the **same constants the local
    settings pages write** (`keyPrinterReceiptPolicy`, `keyReceiptHeader*`,
    `keyReceiptFooter`, `common.KeyKioskIdleReset`), never re-typed strings.
  - `cloudSetTillSetting`: whitelist check → per-key value validation that
    mirrors the local form → `d.Settings.Set` → `rederive`. A refused key or
    value writes nothing and re-derives nothing.
  - `remoteTillSettingsReport`: the read side, surfaced in the heartbeat's
    `DeviceExtra` as `till_settings`.
  - `buildCloudHooks` split out of `StartCloudSync` so the wiring is
    testable without starting the sync goroutine.
- `internal/cloudsync/cloudsync_test.go`, `internal/pages/cloudsync_wire_test.go`
  — dispatch-shape tests plus whitelist / value-validation / Germany-lock /
  re-derive / report / wiring tests.
- `web/help/img/manifest.json` — `surface_sha256` bump only (no PNG, no
  topic hash). Confirmed a true no-op refresh; see finding 4.

## Findings

1. **MUST-FIX — fixed. Premature ADR citation.** Ten comments across the
   four Go files cited **"ADR-0095"** as settled, accepted doctrine
   (`ADR-0095 Decision 1`, `ADR-0095 Decision 2`, and a quoted line of its
   text). Verified directly against `ut-docs`' default branch: `adr/` stops
   at `0094-category-modifier-group-inheritance-with-item-opt-out.md`.
   ADR-0095 exists only in the still-open PR universaltill/ut-docs#2306, so
   every one of those citations pointed at a document that does not exist
   where a reader would look for it — and CLAUDE.md treats accepted ADRs as
   binding, which makes a citation of a non-accepted one actively
   misleading. All ten softened to name the issue instead
   (`ut-docs#2306`, "proposed ADR-0095, pending merge"), with the
   whole-slice explanation and a "swap them for ADR-0095 once it merges"
   note parked once, on `allowedRemoteTillSettingKeys` — the canonical
   definition site — rather than repeated ten times. The commit message
   body carried the same claim and was corrected in the same amend.
   Sites fixed:
   - `internal/cloudsync/cloudsync.go` — `Hooks.SetTillSetting` doc.
   - `internal/pages/cloudsync_wire.go` — `allowedRemoteTillSettingKeys`
     doc, `cloudSetTillSetting` doc, `remoteTillSettingsReport` doc, the
     `SetTillSetting` wiring comment in `buildCloudHooks`, and the
     `till_settings` comment in `DeviceExtra`.
   - `internal/cloudsync/cloudsync_test.go` — `TestApplySetTillSetting` doc.
   - `internal/pages/cloudsync_wire_test.go` — the section banner, the
     value-validation test doc, and the report test doc.
2. **Should-fix — fixed. Stale deletion checklist on a compliance
   carve-out.** `receiptPolicyLockedForCountry`'s doc comment
   (`internal/pages/receipt_policy_hook.go`) is the removal checklist for
   ADR-0089 Decision 3's interim Germany lock — it names "its **three** call
   sites … should be deleted, NOT extended to a second country". This diff
   adds a **fourth** (`cloudSetTillSetting`'s `printer.receipt_policy`
   branch) without updating it. That comment is the only place the
   ut-docs#1908 follow-up will look; leaving it at three is how a remote
   write keeps forcing `always` for DE long after the local UI stopped.
   Updated to four, naming the new site and what it does.
3. **Should-fix — fixed. The `default` branch failed open on whitelist
   growth.** `cloudSetTillSetting`'s value switch had `case
   keyPrinterReceiptPolicy`, `case common.KeyKioskIdleReset`, then a
   `default:` that silently meant "free text, trimmed" — correct for the
   four receipt lines it was written for, but it also means **any future key
   added to `allowedRemoteTillSettingKeys` inherits unvalidated free-text
   writes by default**, which is the opposite of what the file's own
   comments (and the whole "the portal may be stale or wrong" premise) argue
   for. Given the whitelist's doc comment explicitly advertises three
   expected future additions, this is a live risk, not a hypothetical.
   Changed to an explicit `case keyReceiptHeader1, keyReceiptHeader2,
   keyReceiptHeader3, keyReceiptFooter:` for the free-text group, with the
   `default:` now **failing closed** (`"%s has no remote validation rule on
   this till"`). Paired with a test-side twin: the accepted-value table in
   `TestCloudSetTillSetting_WhitelistedKeysWrite` now asserts it covers every
   key in `allowedRemoteTillSettingKeys`, so the whitelist and the switch
   cannot grow independently. Both halves verified to actually fire — see
   "re-verification" below.
4. **Nit — fixed. Missing `Docs-Shots-Unchanged: true` trailer.** The
   original commit bumped `surface_sha256` alone (no PNG, no topic hash) —
   the exact signature of `scripts/ci/update-docs-shots-surface-hash.sh`'s
   escape hatch — but carried none of the trailer that script's header asks
   for, which exists precisely so a reviewer scanning `git log` can tell a
   bare hash bump was a confirmed no-op and not an accident. Trailer added
   to the amended message. The hash itself was re-refreshed after this
   review's own edits and re-verified: `git diff -- web/help/img/manifest.json`
   is one field, and every source change in this diff (comments plus a
   cloud-directive hook with no route, no template and no rendered surface)
   is incapable of moving a pixel.
5. **Nit — not fixed, no action needed.** `apply`'s `str()` helper
   `TrimSpace`es the payload key, so `"printer.receipt_policy "` arriving
   over the wire is trimmed *before* the hook sees it and is then accepted.
   `cloudSetTillSetting` itself does no key trimming (exact match only), and
   `TestCloudSetTillSetting_RejectsNonWhitelistedKeysWithoutWriting`'s
   `" " + keyReceiptFooter` case asserts that. Both behaviours are correct
   and consistent with the pre-existing `set_setting` path; noting it only
   so the two layers aren't later mistaken for a contradiction.

## What the reviewer personally re-verified

**Adversarial whitelist probe, written from scratch by the reviewer.** Drove
`cloudSetTillSetting` *and* the wired `buildCloudHooks(...).SetTillSetting`
with 27 keys chosen to look legitimate or to be dangerous, reading the value
back out of the store each time rather than trusting the returned error:
`"printer.receipt_policy "` (trailing space), `" printer.receipt_policy"`,
`"PRINTER.RECEIPT_POLICY"`, `"Printer.Receipt_Policy"`, tab- and
newline-suffixed variants, `"printer.receipt_policyx"`,
`"printer.receipt_polic"`, `"receipt.footer2"`, `"receipt.header4"`,
`"kiosk.idle_reset_second"` / `"…secondss"`, `"fiscal.tse_puk"`,
`"fiscal.tse_admin_pin"`, `"auth.pin"`, `"auth.admin_pin"`,
`"printer.address"`, `"printer.device"`, `"store.country"`,
`"display.mode"`, `"sync.primary_url"`, `""`, `" "`, `"\t"`, a
NUL-suffixed key, `"../receipt.footer"`, and
`"receipt.footer;receipt.header1"`. **Every one refused, with no write —
neither to the probed key nor collaterally to any whitelisted key**, at both
the function and the wired-hook layer. The report was checked in the same
run: it carries exactly the whitelisted keys and never `store.country` or
`printer.address`.

**Germany receipt-policy lock, compared line by line against the local
handler** (`POST /api/settings/printer`, `internal/pages/print_api.go`)
rather than merely re-running the DE test:

| step | local form | remote hook |
|---|---|---|
| normalise | `ToLower(TrimSpace(form))` | `ToLower(TrimSpace(value))` — same |
| recognised value | unknown/empty **silently defaults to `always`** | unknown/empty **refused** |
| country plugin | `receiptPolicyPermitted(p, AskReceiptPolicy(ctx))` → reject | identical call, identical reject |
| DE lock | `receiptPolicyLockedForCountry(country) && p != always` → reject | identical |
| store | `Settings.Set(keyPrinterReceiptPolicy, p)` | same key, same value |

The remote path is a **strict subset** of what the local form accepts: the
one divergence is that the local form's "unknown → safe default `always`"
coercion becomes an outright refusal remotely, which is the safe direction
(and gives the cloud's result column a reason instead of a silent
substitution). **There is no value the remote path can store that the local
UI would refuse** — including for a DE shop, where only `always` survives.
Confirmed empirically too: the DE fixture rejects `ask`, leaves the store
untouched, and accepts `always`.

**Value-validation probe.** `printer.receipt_policy` accepts only the three
values after case-fold/trim (`"ALWAYS\n"` → stored `always`, matching the
local form exactly); `"always ask"`, `"alwaysx"`, `"0"` all refused with no
write. `kiosk.idle_reset_seconds` matches
`POST /api/settings/kiosk-idle-reset`'s own `Atoi` + `0..600` bound exactly,
including the shared quirks (`"+90"` → `90`, `"-0"` → `0`, which
`strconv.Atoi` accepts on **both** paths) and canonicalises via
`strconv.Itoa` before storing; `"9_0"`, `"0x10"`, `"1e3"`, `"-1"`, `"601"`,
`"99999999999999999999"`, `"ninety"` all refused with no write. Boundaries
`"0"` and `"600"` accepted.

**Re-derive actually happens.** Traced the claim rather than trusting the
counter test: `rederive` is `newRederiveSettings` (`internal/pages/init.go`),
which calls `common.LoadState(c, dp.Settings, dp.Cfg)` and pushes the result
through `dp.UpdateState`; `LoadState` reads `common.KeyKioskIdleReset`
(`internal/pages/common/state.go`), so `Settings.Set` + `rederive` lands the
new idle-reset window in the live derived `State` — equivalent to the local
handler's `SaveState` + `SetState`, and without it the value really would
wait for a restart. Confirmed a refused write does **not** re-derive.

**TDD claims re-verified for real — four independent weaken-then-confirm
passes, not a re-run of already-green tests:**
- Whitelist check neutered (`if key == "" && !allowed[key]`) → **4 tests
  failed**: `TestCloudSetTillSetting_RejectsNonWhitelistedKeysWithoutWriting`,
  `TestCloudSetTillSetting_RederivesState`,
  `TestBuildCloudHooks_WiresTillSettings`, plus the reviewer's own probe.
  Matches the Tester's reported three exactly.
- Germany lock neutered → `TestCloudSetTillSetting_ReceiptPolicyLockedForGermany`
  failed, alone.
- `kiosk.idle_reset_seconds` bound neutered (`err != nil` only) →
  `TestCloudSetTillSetting_ValidatesValuesLikeTheLocalForms` failed, alone.
- Finding 3's own hardening: temporarily added `"review.probe.key"` to the
  whitelist → the new coverage assertion failed with the intended message,
  **and** a direct call proved the `default` branch refuses it at runtime
  with nothing written. Both reverted.
- Source restored and confirmed byte-identical (`git diff` clean on the
  affected hunks) before the fixes were applied.

**Cross-repo contract, checked by hand** (nothing enforces it — see deferred
item 2): `ut-cloud`'s `claims.AllowedTillSettingKeys`
(`internal/claims/directives.go:84`) lists exactly the same six keys, and
`claims.parseDeviceFields` (`internal/claims/claims.go:395`) reads the
heartbeat under the same `"till_settings"` name with a tolerant
`map[string]any` decode, which the till's `map[string]string` marshals into
cleanly. The two sides agree today.

**Repo-rule checks:**
- **No raw SQL outside `internal/data`/`internal/db`** — read the diff
  myself as well as running the guard: every access is
  `d.Settings.Get`/`d.Settings.Set`. `guard-data-access.sh` green.
- **Backend-only, no i18n surface** — the diff touches no template, no
  `web/ui/**`, no `web/locales/*.json`. The hook's error strings go to the
  cloud's directive result column, never to a till screen, exactly like the
  pre-existing `rejectRemoteFiscalPostureWrite` messages next to them;
  `guard-i18n.sh` green (1681 keys, all locales matched). No new
  user-facing string anywhere, so no help-topic update and no
  `make docs-shots` regeneration is owed.
- **Recurring bug classes: both absent, confirmed by reading.** Zero file
  I/O in the diff — no `os.Create`/`WriteFile`, hence no missing
  `os.MkdirAll`; no path construction at all, hence no cwd-relative path
  where `paths.Data(...)` belongs. The only new import is `strconv`.
- **Offline-first / kiosk isolation** — untouched: nothing here runs in the
  checkout path, and `guard-kiosk-engine.sh` is green (no `/self-order`
  route involved).
- **Test data / secrets** — clean. `"Corner Shop"`, `"High Street 1"`,
  `"Thank you!"`, `"Bye!"`, `192.168.1.50:9100`: generic, no real client or
  shop name. `"fiscal.tse_puk"` / `"auth.admin_pin"` appear as refused key
  *names*, never as credential values.

## Full gate, re-run after every fix

`gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
`golangci-lint run ./...` (**0 issues**), `go test ./... -count=1` (**all 77
packages ok**, including `internal/pages` genuinely executing at ~322s — not
a cached or skipped result), and the guards a Go/docs change can move:
`guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh`,
`guard-page-http-error.sh`, `guard-kiosk-engine.sh`,
`guard-compliance-claims.sh`, `guard-help-topics.sh` — all green.
`guard-deadcode-baseline.sh` could not run in this worktree (`deadcode`
needs the GTK/WebKit pkg-config headers CI installs for that job; it fails
on `webview_go`, unrelated to this diff). `shellcheck` not installed
locally and not applicable — no `scripts/ci/*.sh` file changed.

## Deferred — backlog cards, deliberately not folded in

1. **A remotely-applied setting change leaves no local audit row.** The
   local printer form writes `settingsAudit(..., "printer_settings_changed")`
   and the kiosk-idle form writes `kiosk_idle_reset_changed`; neither
   `cloudSetTillSetting` nor the pre-existing `SetSetting` hook writes
   anything to the audit log — the only trace is the status/message returned
   to the cloud. This is a **pre-existing gap in the whole ADR-0018
   directive channel**, not something this diff introduces, but this diff is
   what first routes a fiscal-adjacent setting (`printer.receipt_policy`
   under ADR-0089's DE lock) through it, so it is now worth a card. Fixing
   it here would mean changing the shared directive path and inventing an
   actor identity for "the cloud", which is real design work, not a review
   edit.
2. **Nothing mechanically enforces "MUST match ut-cloud's
   `claims.AllowedTillSettingKeys` byte-for-byte".** That comment states a
   cross-repo invariant with no guard behind it — unlike the signing
   contract, which has `manifest-contract-guard` /
   `TestCanonicalManifestMirrorsPOS` for exactly this class of silent
   breakage. The two lists agree today (verified by hand above), but a
   one-sided edit would drift unnoticed until a merchant's directive was
   queued by the portal and refused by the till. A guard in the same shape
   as `manifest-contract-guard` is the obvious follow-up and belongs on its
   own card.

## Post-review update (merge conflict, same day)

`universal-till#1188` (ut-docs#2286) merged to `main` while this PR was in
flight, removing `receiptPolicyLockedForCountry` and its DE-only lock
core-wide per a direct product-owner decision — German shops now choose
freely among always/ask/never like every other country. The Germany-lock
verification above (line-by-line comparison, adversarial testing) was
accurate *at review time*; merging `main` into this branch required
removing `cloudSetTillSetting`'s now-dead call to the deleted function and
replacing `TestCloudSetTillSetting_ReceiptPolicyLockedForGermany` with
`TestCloudSetTillSetting_ReceiptPolicyFreeForGermany`, asserting the new
(simpler) behavior: a DE shop's remote write is validated only against the
installed country plugin's allow-list, same as every other country, with
no country-specific branch left to keep in sync. Re-verified after the
merge: full gate green, adversarial/whitelist tests unaffected (they don't
touch country logic).
