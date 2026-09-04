# Code review — pairing wizard: required till name, frozen error panel, no approve/deny feedback (ut-docs#1540)

- **Date:** 2026-09-04
- **Branch:** `fix/1540-pairing-wizard-required-name` (pre-review snapshot
  `5350adb`, on top of `main` @ `aa1f9b2`)
- **Reviewer:** independent read via an Opus subagent, no shared context with
  the implementation, run in an isolated worktree.
- **Verdict: MERGE WITH FIXES.** Defects #1 (client-side `required`) and #3
  (approve/deny in-flight feedback) are correct and well-built. Defect #2
  (polling the "error" fragment) is **not safe as written**: it is justified
  by a rationale that does not hold against the real handler code, and it
  measurably *destroys* the very error message defect #1 was added to
  produce. Three must-fix items and two should-fix items below.
  **All five are applied and re-verified in the review worktree** (not
  committed) — `gofmt`/`build`/`vet` clean, `go test ./...` exit 0, every CI
  guard green including `guard-docs-shots` after a `make docs-shots` re-run.
  Nothing else blocks.

## What shipped

Three UI defects in the LAN till-pairing/discovery flow (ADR-0033 parts
1+3, ut-docs#185/#289):

1. **Till name not enforced client-side.** `required` added to
   `#setup-pairing-till-name` (`web/ui/pages/setup.html`) and
   `#pairing-till-name` (`web/ui/pages/tills.html`), plus an inline
   `hx-on::before-request` guard on the dynamically-injected
   "Request to pair" button, which lives outside any `<form>` and so is
   never natively validated. Server side, `pairStartHandler`
   (`internal/pages/pairing_join.go`) replaces the hardcoded English
   `"base_url, till_id and name are all required"` with translated
   `tills.pairing.error.name_required` / `.missing_request`.
2. **Error fragment never polled.** `pairWaitView`'s `polling` flag becomes
   `status == "waiting" || status == "error"`, so the error render carries
   `hx-trigger="every 15s"`. Deliberately flips
   `TestPairStart_SurfacesUnreachablePrimary`'s pre-existing assertion.
3. **No in-flight feedback on approve/deny.**
   `web/ui/partials/pending_pairings.html` gains
   `hx-indicator="#pair-busy-{{ .ID }}"` +
   `hx-disabled-elt="find button[type=submit]"` on both forms, a per-row
   `htmx-indicator` span (`tills.pairing.processing`), and device-name
   prefixed `placeholder`/`aria-label` on each PIN input.

Plus: 3 new locale keys × 4 locales, `web/help/{en,ar,fa,tr}/multitill.md`
step 3 rewritten, `web/help/img/manifest.json` regenerated, 3 new/changed
tests.

## Review findings

### MUST FIX — F1: the new required-name error message self-destructs after 15 s (regression, introduced by defect #2's fix)

`pairStartHandler` only calls `rp.set(...)` **after** the pair-request
succeeds. Every one of its six error branches — including the new
missing-name branch — returns with `rp.active` still `nil`. The error
fragment now polls `pair-status`, which for `state == nil` renders the
`idle` view. Reproduced directly (temporary probe test, since removed):

```
PAIR-START (missing name) BODY:
  <div id="pairing-status" hx-get="/api/sync/pair-status" hx-trigger="every 15s" ...>
    <p class="muted">Pairing failed: This till&#39;s name is required before you can request pairing.</p>

NEXT POLL BODY:
  <div id="pairing-status" >
    <p class="muted">No pairing attempt in progress.</p>
```

Same for the unreachable-primary case (`"Pairing failed: cannot reach that
primary: … connection refused"` → `"No pairing attempt in progress."`).

So the operator has 15 seconds to read the diagnosis; after that the panel
says something that reads as "nothing happened", with no clue which field
was wrong. That is the same class of defect the card set out to fix, and it
lands specifically on the message this card added. Before this diff the
message at least stayed on screen.

### MUST FIX — F2: a failed retry can resurrect the *previous* attempt's live verification code

Second probe, same run: with attempt 1 in flight (`rp.active.status ==
"waiting"`), a second `pair-start` that fails renders the error fragment;
its 15 s poll then swaps in attempt 1's screen:

```
ATTEMPT-2 ERROR BODY:
  <p class="muted">Pairing failed: This till&#39;s name is required …</p>
ATTEMPT-2 ERROR'S NEXT POLL BODY:
  <p>Waiting for approval…</p>
  <p class="pairing-code" …>622016</p>
  <p class="muted">Compare this code with the one shown on the primary's screen …</p>
```

The failure notice is replaced, unprompted, by an apparently-healthy
"waiting" screen carrying a 6-digit code the operator did not just request.
That code is what the manager is asked to *compare and approve* — silently
showing a stale one is worse than showing a stale error.

### MUST FIX — F3: the stated justification does not hold against the handler code

The code and test comments claim the poll is "how a later, separate
attempt's waiting/joined state ever reaches this stale div", and that
`"waiting"` is "the only status this poll can ever transition OUT of
'error' state's dead end". Both are wrong:

- The "Request to pair" button posts with
  `hx-target="#pairing-status-host"` / `#setup-pairing-status-host` and
  `hx-swap="innerHTML"`. A retry from the same page **replaces the error
  fragment itself** with its own response. The poll contributes nothing to
  the single-tab retry case the comment describes.
- The poll demonstrably transitions to `idle` (F1) and can equally reach
  `joined`/`expired`, not only `waiting`.

The only scenario the change actually serves is a *second browser tab or
device* driving the same singleton `replicaPairing` — real (the
`replicaPairing` doc comment contemplates two tabs) but narrow, and not
what the comments say.

Secondary, accepted rather than blocking: for errors that *do* have active
state (`pairStatusHandler`'s "unexpected response from the primary (…)",
`friendlyJoinError` failures) the fragment now polls **every 15 s forever**
with no cap or backoff. That is not a hammering risk — `pairStatusHandler`
returns on the `state.status != "waiting"` branch *before* the outbound
`client.Do`, so the dead primary is never contacted — and the message is
preserved on each re-render, so this half of the change is defensible. It
still never converges in a single tab.

**Fix applied (all three):** `polling` becomes an explicit decision of the
caller (`pairWaitViewPolling`) instead of a function of `status`. Only
`pairStatusHandler`'s error renders — which carry `state.errMsg`, so nothing
is lost on a re-poll, and which are the only errors that can legitimately
change — keep polling. Every `pairStartHandler` error stays terminal, so the
field-specific message stays on screen until the operator acts; the retry
re-renders the div directly anyway. The two flipped assertions are restored
to "must not poll" with the reasoning inline, and a new
`TestPairStatus_ErrorStateKeepsPollingAndKeepsItsMessage` covers the half of
defect #2 that survives (poll present, and the stored message re-rendered
unchanged on the next tick). `web/help/{en,ar,fa,tr}/multitill.md` reworded
from "the reason appears in place and updates on its own once the situation
changes" to "the reason stays on screen until you try again", and
`make docs-shots` re-run (new surface `8531b212f584…`).

### SHOULD FIX — F4: whitespace-only till name = silent no-op (the exact reported symptom, narrowed)

Verified character-by-character against the *rendered* page (dumped from a
real `GET /setup?lang=en`, then evaluated with node 22 exactly as htmx
1.9.12's `Ft()` does via `new Function('event', code)`):

```
HTML EMITTED : hx-on::before-request="var n=document.getElementById('setup-pairing-till-name');if(!n.value.trim()){event.preventDefault();n.reportValidity();}"
empty ""         -> { request_cancelled: true,  native_bubble_shown: true  }
whitespace "   " -> { request_cancelled: true,  native_bubble_shown: false }
valid "Till 2"   -> { request_cancelled: false, native_bubble_shown: false }
```

For a whitespace-only value the guard cancels the request but
`reportValidity()` returns `true` (HTML `required` is satisfied by a space),
so **no message is shown at all** — the button appears to do nothing, which
is precisely the "pairing is broken" symptom the card exists to remove. On
a touchscreen till with the on-screen keyboard this is not exotic.

Fix applied (no new locale key needed) — trim in place, then let `required` +
`reportValidity()` do the work:

```
var n=document.getElementById('…-till-name');n.value=n.value.trim();if(!n.reportValidity()){event.preventDefault();}
```

Verified the same way, over the same four inputs, and through the same
single-quote-escaped JS string literal round trip:

```
empty ""          -> value "",       cancelled: true,  bubble: true
whitespace "   "  -> value "",       cancelled: true,  bubble: true
padded " Till 2 " -> value "Till 2", cancelled: false, bubble: false
valid "Till 2"    -> value "Till 2", cancelled: false, bubble: false
```

(Trimming in place is also a small correctness gain in its own right: the
server already `TrimSpace`s `name`, so what is sent now matches what is
stored.) Applied identically in `setup.html` and `tills.html`.

### SHOULD FIX — F5: the PIN placeholder truncates away the field's purpose, and disagrees with its own `aria-label`

`aria-label="{{ T "…manager_pin" }} — {{ .DeviceName }}"` but
`placeholder="{{ .DeviceName }} — {{ T "…manager_pin" }}"` — opposite
order, so a screen-reader user and a sighted user get the label backwards
from each other. More importantly the *variable-length, untrusted* half is
first: `device_name` is accepted from any LAN device with only a
`TrimSpace` + non-empty check (`pairing_api.go:147-150`, no length cap), and
a placeholder clips at the input's width, so a long device name pushes
"Manager PIN" / "Yönetici PIN'i" entirely out of view — in RTL too, where
clipping happens at the logical end and takes the Arabic/Persian label
instead. `ux-guidelines.md` is explicit: "Design for the longest locale…
Labels/buttons must not truncate or overflow."

Fix applied: the fixed, meaningful half leads in both attributes
(`placeholder="{{ T "…manager_pin" }} — {{ .DeviceName }}"`), so the two
labels agree and it is the *device name* that clips, not the field's
purpose. The new test's assertion was tightened at the same time — it now
matches the whole `placeholder="Manager PIN — Kitchen Till"` and
`aria-label="…"` attributes rather than the substring `"Kitchen Till — "`,
which the `aria-label` alone could satisfy without the placeholder ever
naming the device to a sighted operator.

Not fixed here (out of scope, worth a card): `device_name` still has no
length cap on the primary — `pairing_api.go:147-150` only trims and checks
non-empty, so an unbounded LAN-supplied string reaches the `<td>` and both
new attributes.

### Accepted / deferred, no change requested

- **Five sibling English error strings remain untranslated on this exact
  handler** — `"not a valid primary address"`, `"cannot reach that primary:
  …"`, `"the primary refused the pair request"`, `"unexpected response from
  the primary"` (×2) — all rendered to the operator through the *same*
  `errMsg` slot this card just translated. `guard-i18n.sh`'s Go-side check
  only covers `common.LocalizedError` / `common.LogAndLocalizedError` /
  `httpx.T` call sites and toast literals, so `pairWaitView(…, "english")`
  slips past CI. Pre-existing; the diff translates 1 of 6. Worth a
  follow-up card rather than scope creep here — but note the flipped test
  now *asserts* the English `"cannot reach that primary"` string, so that
  follow-up has to touch this test too.
- **The native validation bubble is in the browser's UI language, not the
  till's locale.** Relying on `reportValidity()` means an Arabic-locale till
  on an English-locale browser gets "Please fill out this field." The
  translated server-side message remains the authoritative one, so this is a
  cosmetic gap, not a correctness one.
- **`.ID` is a UUID**, so `#pair-busy-<uuid>` is a valid CSS id selector
  (leading letter from the literal prefix). Confirmed in a rendered body.
- **Arabic terminology drift (nit):** the new keys say «طلب الإقران» while
  the existing `tills.pairing.request_btn` says «طلب الاقتران». Both are
  valid; mixing them in one screen is inconsistent.
- **`role="status"` on the busy span**: `app.css:1909` hides
  `.htmx-indicator` with `display:none`, so the live region is not in the
  a11y tree until it becomes visible, and its text never *changes* — some
  screen readers will not announce it. Harmless; the visible-state change is
  the primary signal and matches the `import.html` precedent (which uses no
  `role` at all).
- **The regenerated PNGs** (`sell`, `invoices`, `till-designer` across
  locales, in the pre-review snapshot and again after the review re-run)
  are the usual pixel-level Chromium rendering noise `docs-shots` warns
  about; none of them is a screen this diff touched. No `tills`/`setup`
  screenshot exists to go stale — `multitill`'s topic claims `/tills`, and
  its shot is unaffected by an attribute-only change.
- **Three new `en.json` keys need adding to the external
  `ut-plugin-language-{de,es}` packs** (`tills.pairing.error.name_required`,
  `.missing_request`, `tills.pairing.processing`). The `lang-pack-drift`
  check is advisory-only on the PR and blocking on `push` to `main`, so this
  is a real follow-up, not a nit.

### Confirmed fine — things I tried to break and could not

- **JS string escaping in the injected attribute (setup.html and
  tills.html).** Rendered the real page and dumped the exact bytes.
  `html/template` **elides the nine new `//` comment lines** (replacing each
  with its leading whitespace) — the concatenation chain is unaffected
  because no comment line carries the `+` operator, and `node --check` on
  the full rendered script set reports valid syntax. The `\'` escapes
  survive verbatim; the emitted HTML is a double-quoted attribute whose
  value contains bare `'`, which the HTML parser accepts, and which
  `new Function('event', …)` compiles. No interaction with `escAttr(vals)`
  (`hx-vals` is a separate, `&quot;`-escaped attribute) or `hx-include`;
  spacing between the concatenated attribute chunks is correct.
- **htmx 1.9.12 actually honours the cancel.** In the vendored
  `web/public/vendor/htmx.min.js`: `if(!ce(n,"htmx:beforeRequest",I)){ie(o);w();return l}`
  — and `ce` (triggerEvent) dispatches the camelCase event *and* its kebab
  form (`$t`), AND-ing both results, so `hx-on::before-request` (which
  `jt()` binds as `htmx:before-request`) can cancel. Crucially the indicator
  and disabled-elt classes (`or`/`sr`) are applied **after** that check, so
  a cancelled click leaves no stuck `htmx-request` state.
- **`htmx.process(out)` wires the new attribute.** `zt()` ends with
  `oe(kt(e),jt)`, and `kt` is the XPath sweep for `hx-on:`-prefixed
  attributes over `.//*` — the injected button is a descendant of the
  processed node, so it is found.
- **`hx-disabled-elt="find button[type=submit]"` is supported here.**
  `Z()` handles `find ` (`t.indexOf("find ")===0`), and `me()` resolves
  `hx-disabled-elt`/`hx-indicator` through it. Identical to the cited
  `import.html` ut-docs#1510 precedent (`import.html:60,67`), and scoping to
  `find` really does keep each form from disabling its sibling's button.
- **Locale parity is exact**: en/ar/fa/tr all 1874 keys, zero missing, zero
  extra. All three new values are genuine, distinct, native-script
  translations (spot-checked: fa "پیش از ارسال درخواست جفت‌سازی، نامی برای
  این صندوق وارد کنید" and tr "Eşleştirme isteği göndermeden önce bu kasaya
  bir ad girin" both mean what the English says); none is empty, English, or
  copy-pasted. The `tills.pairing.error` / `tills.pairing.error.name_required`
  prefix pair is safe — the store is a flat map, proven by the message
  resolving correctly at runtime.
- **Manual accuracy**: "give this till a name (required — the request is
  refused without one)" and "each pending request's PIN box shows which
  device it belongs to" are both accurate, and the ar/fa/tr edits carry the
  same two claims. The third claim — "the reason appears in place and
  updates on its own once the situation changes" — documented the F1/F2
  behaviour and was reworded in all four locales as part of the F1 fix
  (see above), with `make docs-shots` re-run so
  `web/help/img/manifest.json`'s `topics.multitill.*` and `surface_sha256`
  match again.
- Neither recurring bug class applies: this diff writes no files, so there
  is no missing `os.MkdirAll` and no cwd-relative path where
  `paths.Data(...)` belongs.
- No real client/shop names ("Kitchen Till", "Bar Till", "Till 2",
  `10.0.0.4x` are all generic), no secret-shaped literals
  (`commitOf("bind-secret-1")` is a test fixture).
- UI guidelines: no new hardcoded colors or spacing (the busy span reuses
  the existing `muted` + `htmx-indicator` classes, no inline style); no new
  modal blocker; no hover-only affordance; checkout is untouched, so
  offline-first is unaffected; existing patterns reused rather than invented.
- `README.md` claims nothing this diff changes.

## Verification beyond automated tests

- `gofmt -l .` (no output), `go build ./...`, `go vet ./...` — all clean.
- `go test ./internal/pages/... -run 'Pair|Pending' -v` — green;
  the 20 pairing/pending tests specifically all pass.
- `go test ./...` — **exit 0, 47 packages ok**, no failures.
- Every guard in `.github/workflows/ci.yml`'s `build` job run individually
  and green: `guard-data-access`, `guard-migration-version-collision`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-page-http-error`,
  `guard-i18n` (1357 template keys, all locales match en.json),
  `guard-compliance-claims` (236 files), `guard-docs-shots` (24 topics × 4
  locales fresh, surface `4a8df81488c0…`), `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-osk-loaded`,
  `guard-e2e-fixtures-import`, `check-brand-assets`,
  `guard-makefile-version`.
- **TDD claims independently re-verified by the reviewer, one production
  hunk at a time, in an isolated worktree — not taken on trust:**
  - Reverted `polling` to `status == "waiting"` →
    `TestPairStart_SurfacesUnreachablePrimary` FAILS with
    `pairing_join_test.go:139: error state must keep polling so the panel
    can recover once the situation changes`, and
    `TestPairStart_MissingNameRendersTranslatedFieldSpecificMessage` FAILS
    with `pairing_join_test.go:197: missing-name error state must keep
    polling too`. Restored → both pass.
  - Reverted the missing-name branch to the old hardcoded string →
    `TestPairStart_MissingNameRendersTranslatedFieldSpecificMessage` FAILS
    with `pairing_join_test.go:186: expected the message to name the visible
    field, not a JSON key, got: … Pairing failed: base_url, till_id and name
    are all required`. Restored → passes.
  - Reverted only the `placeholder`/`aria-label` hunk in
    `pending_pairings.html` →
    `TestPendingPairingsUI_PINFieldsBoundToTheirOwnDeviceAndShowBusyFeedback`
    FAILS with `expected the PIN field for "Kitchen Till" to be visibly
    labelled with its own device name`. Restored the placeholder, reverted
    only the `hx-indicator`/`hx-disabled-elt`/busy-span hunk → the same test
    FAILS with `expected approve/deny to show in-flight busy feedback
    (hx-indicator + hx-disabled-elt)`. Restored → passes. (Note
    `html/template` strips the HTML comment that mentions those attribute
    names, so the assertion really does test the attributes, not the
    comment.)
  - Working tree returned to `5350adb` exactly after every revert
    (`git status` clean); no probe file left behind.
- Behaviour proven with throwaway probes rather than argued from the source:
  the F1/F2 fragment sequences above came from driving the real
  `registerPairingJoinAPI` mux; the F4 escaping/behaviour table came from
  the real rendered `/setup` page evaluated in node 22. All probe files
  removed afterwards.
- **Re-verified after the review fixes were applied:** `gofmt -l .` empty,
  `go build ./...`, `go vet ./...` clean, `go test ./...` exit 0 (47
  packages), all 21 pairing/pending tests green including the new
  `TestPairStatus_ErrorStateKeepsPollingAndKeepsItsMessage`, and
  `guard-i18n`, `guard-help-topics`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `guard-osk-loaded`, `guard-page-http-error`, `guard-data-access` all
  green. `make docs-shots` re-run in full (96 screenshots, 2.1 min).

Refs: ADR-0033, ADR-0008, ut-docs#185, ut-docs#289, ut-docs#1510,
ut-docs#1540, `ut-docs/reference/ux-guidelines.md`.
