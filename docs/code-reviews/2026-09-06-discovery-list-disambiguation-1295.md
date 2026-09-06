# Discovery-list disambiguation for colliding shop names (ut-docs#1295)

## What shipped

Two different, unrelated shops on the same LAN segment that are both still
on their unconfigured default name used to show up as visually identical
entries in the pre-pairing discovery list (`web/ui/pages/tills.html` and
`setup.html`) — the raw `till_id` UUID was the only thing distinguishing
them, meaningless to an operator without SSH/adb access to compare it by
eye (confirmed live: two entries both named "this shop" in one scan).

- `internal/discovery`: a new `advertisedName(ctx, settings, tillID)`
  helper is what the mDNS `Advertiser` broadcasts. Once an operator has
  actually named their shop (`store.name` set), that name is broadcast
  verbatim; the still-unset fallback now folds in a short suffix from the
  till's own stable id — `"this shop (7890)"` — so two unconfigured tills
  on the same LAN broadcast distinguishable names by construction. This
  is scoped to the advertiser only; `internal/pages`'s own, separate
  `storeNameOrDefault` (receipts, reports, the AI assistant) is untouched.
- `web/ui/pages/tills.html` and `setup.html` (identical rendering JS in
  both — the authenticated and first-boot discovery flows): each
  discovery-list entry now also shows its LAN address, derived
  client-side from the `base_url` the API already returns, next to the
  existing Till ID. New locale key `tills.discovery.result_address` in
  en/fa/tr/ar, plus the `ut-plugin-language-{de,es}` packs (separate PRs
  #157/#156).

## Independent review

Spawned as an `Agent` (Opus — this card is `complexity:medium`, Sonnet
wrote it) with `isolation: "worktree"`. First attempt was voided: the
worktree was created against whatever repo the orchestrating shell had
last `cd`'d into (`ut-plugin-language-es`, an unrelated repo touched
earlier in the same cycle for the lang-pack follow-up) rather than
`universal-till` — caught before the review did any real work, the agent
was stopped, and re-launched after confirming the primary working
directory was `universal-till`. Worth recording as a process note: launch
an `isolation: "worktree"` review from inside the target repo, don't
assume the tool infers it from the task description.

The corrected review ran the full gate itself (`go build/vet/test -race`,
full `go test ./...`, `golangci-lint run ./...`, all the CI guards, the
e2e spec) and **independently re-verified both TDD claims** by reverting
the fix and confirming the specific tests fail with the expected error
before restoring and confirming green again — for both the Go-level
`TestAdvertisedName_*` tests and the e2e spec. It also probed
`hostOf()` for XSS-shaped input and confirmed `esc()` still neutralizes
it, traced that `advertisedName`'s suffix reaches only the mDNS TXT
`name=` field (verified the pairing verification code is
name-independent, so this can't affect pairing security), and checked all
four locale translations are distinct, non-empty, and not copy-pasted
English.

### Findings, triaged

1. **Fixed — `hostOf()`'s regex fallback truncated a bracketed IPv6
   address at its first internal colon** (`tills.html`/`setup.html`,
   both files, identical bug). `internal/discovery/browse.go`'s
   `entryAddr` genuinely falls back to `AddrV6` (ut-docs#538), so an
   IPv6 `base_url` is a real production shape, not hypothetical — though
   display-only, since pairing itself uses the untouched `base_url`.
   Fixed with two separate capture groups (bracketed vs. bare host)
   instead of one character class that stopped at the first colon
   regardless of brackets. New e2e test case added
   (`discovery list address handles an IPv6 candidate without
   truncating it`) — the browser's real `new URL()` already handles this
   correctly, so the test mainly guards the fallback path against a
   future regression, not today's primary code path (every browser in
   this project's fleet has `URL`).
2. **Fixed — `internal/discovery.storeNameOrDefault` was dead code, and
   its neighbouring comment was actively misleading.** The diff's first
   draft kept the pre-existing in-package `storeNameOrDefault` (it used
   to have a real caller — the old `advertiser.go` line this PR
   replaced) alongside the new `advertisedName`, and `advertisedName`'s
   own doc comment said it was "separate from storeNameOrDefault's plain
   fallback (used by receipts, reports and the AI assistant)" — but
   those callers actually use a *different*, same-named function in
   `internal/pages` (`ask_api.go`, `print_api.go`, `reports_page.go`,
   `eod_api.go`, `sync_api.go`, `receipt_designer.go`, `pos_api.go`) that
   takes `*common.Deps`, not `*data.SettingsRepo` — the two have never
   been the same function. Confirmed via repo-wide grep that nothing
   outside its own test called the `internal/discovery` copy after this
   diff. Removed it, removed its dedicated test
   (`TestStoreNameOrDefault_FallsBackWhenUnset`), and reworded
   `advertisedName`'s comment to name `internal/pages`'s function
   correctly and explain why this package still can't call it directly
   (the reverse-import-cycle reason the original, now-removed function's
   comment recorded).
3. **Accepted, no action — `sell.png`'s prior diff description
   ("1-byte re-encode") slightly overstated pixel identity.** The file
   shrank by 1 byte but 7 of 614,400 pixels differ by ≤1/255 in a small
   cluster — sub-perceptual anti-aliasing noise from re-rendering, not a
   visual regression (confirmed by decoding and diffing both revisions
   pixel-by-pixel). Two more incidental screenshot re-encodes of the
   same shape (`ar/sell.png`, `en/till-designer.png`, `fa/multitill.png`)
   appeared on the second `make docs-shots` run after the fixes above —
   same class, all single-digit-byte deltas on files the diff's actual
   code changes don't touch the rendering of.
4. **Accepted, documented rather than fixed — `setup.html`'s copy of
   this change has no dedicated e2e coverage.** The JS is byte-identical
   between `tills.html` and `setup.html` (diffed to confirm), so the risk
   is low, but the first-boot flow is the one a brand-new operator
   actually exercises. Exercising it for real would mean adding a case
   inside `login.spec.ts`'s `test.describe.serial` fresh-install flow
   (the only spec that reaches `/setup` — `POST /api/setup/discover-
   primaries` is `firstBootGate`-gated, unreachable on every other
   spec's already-configured shared till) rather than a standalone test,
   which is more invasive than this fix warrants on its own. Recorded as
   a real, accepted gap rather than silently left uncovered.
5. **No action — the hardcoded English `"this shop"`/`"this shop
   (7890)"` literal is broadcast over mDNS whatever the receiving till's
   locale, same as the pre-existing behaviour** (`browse.go`'s own
   fallback for a malformed/older advertiser carries the identical
   literal). Go-side discovery names can't route through
   `web/locales/*.json` — there's no requesting-side locale to translate
   into at broadcast time. `guard-i18n.sh` passes; this is a pre-existing
   constraint, not a regression.

## Verified beyond automated tests

- Visually checked the rendered discovery list (mocked two-candidate
  collision) in English and Farsi/Arabic RTL at a real viewport size:
  labels sit correctly, addresses are legible, RTL bidi renders the LAN
  address/UUID correctly, no overlap with the fixed bottom status bar at
  a real viewport (a `fullPage` screenshot showed an apparent overlap
  that turned out to be a Playwright fullPage-stitching artifact of the
  `position: fixed` status bar, not a real defect — confirmed by
  re-checking at an actual viewport size).
- TDD: both the Go tests and the e2e spec were confirmed failing against
  the pre-fix code (exact old-rendering error message) before the fix,
  and passing after — independently re-confirmed by the review subagent
  a second time.

## Safe to merge

Yes. No blocking findings after the two fixes above; the two accepted
items (3, 4) are documented, not silently dropped.

## Deferred / follow-up

- `ut-plugin-language-de`#157 and `ut-plugin-language-es`#156 carry the
  matching translated key — their own `key-drift` CI shows a failure
  right now, but it's an ordering artifact (that check fetches core's
  *current* `main`, which doesn't have the new key until this PR merges)
  rather than a translation problem; commented on both PRs to that
  effect. Merge order: this PR first (unaffected — `lang-pack-drift` is
  advisory-only on a PR touching `en.json`), then re-run/merge the two
  language-pack PRs once `main` carries the key.
- `setup.html`'s dedicated e2e coverage gap (finding 4 above) — a
  reasonable follow-up if `login.spec.ts`'s serial fresh-install flow
  gets extended for other reasons; not scoped as its own Backlog card
  given how low-risk a byte-identical-JS gap is.
