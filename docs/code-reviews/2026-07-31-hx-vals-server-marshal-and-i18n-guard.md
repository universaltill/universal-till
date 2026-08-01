# Code review — escape-safe hx-vals across templates + Go-side i18n guard gap (board #19)

- **Date**: 2026-07-31
- **Branch**: `fix/hx-vals-server-marshal-and-i18n-guard` → PR (this change)
- **Card**: ut-docs #19 — coverage batch 4 follow-ups. Third bullet
  (barcode-less items can't become shortcut tiles) split into its own card,
  `ut-docs#173` — it needs a real schema decision, not a bounded fix here.

## What shipped

1. **`hx-vals` server-side JSON marshaling.** `buttons_admin.html` was
   already fixed (an earlier PR) to stop interpolating raw fields into a
   hand-written `hx-vals='{"k":"{{ .V }}"}'` literal — invalid JSON, or a
   value breaking out of the HTML attribute, for anything containing a
   quote/backslash/apostrophe. The same latent bug existed in six more
   call sites across five templates, none sharing one rich view-model
   struct, so instead of bespoke `Vals()` methods this adds a generic,
   reusable template func — `jsonVals(pairs ...any) (string, error)` in
   `internal/httpx/httpx.go`, registered globally via `baseFuncs`. Returns
   a plain `string` (not `template.JS`, matching the existing `AddVals`
   pattern) so `html/template`'s normal attribute-context escaping still
   applies.
2. **Go-side i18n guard gap.** `scripts/ci/guard-i18n.sh` only ever
   audited `web/ui/*.html` — a Go handler writing raw English straight
   into an HTTP response was invisible to it (the card's own example:
   `buttons_api.go`'s `/api/buttons/search` "Type 3+ characters" hint).
   Added a heuristic scan of `internal/**/*.go` for prose-looking literals
   passed to `w.Write`/`fmt.Fprintf`/`fmt.Fprint`/`fmt.Fprintln(w, ...)`,
   fixed the 5 real violations it found (the search hint + 4
   `plugin_api.go` status strings), added new locale keys with real
   translations to all 4 locale files.

## Independent (opus) review — real findings, all addressed

Overall verdict: **safe to merge**, no blocking findings — but several
real, worth-fixing gaps:

- **MINOR (real, higher-impact than what shipped) — fixed.** The sweep
  missed `web/ui/partials/buttons.html:23` — the actual cashier
  **sale-screen tile** (`hx-post="/api/pos/scan"`), not just an admin
  panel. `shortcut_buttons.barcode` has no format restriction (proven by
  this PR's own catalog test), so a button seeded with a quote-containing
  code would silently do nothing when tapped at checkout. Fixed with the
  same `jsonVals` call; new TDD'd regression test
  `TestButtonsUIFragment_HxValsSurvivesQuotedCode` (confirmed red against
  the pre-fix template — invalid-JSON error — then green).
- **MINOR (consistency) — fixed.** `self_order_cart.html`'s 3 `.LineKey`
  sites carried the identical field `basket.html` was fixed for, left
  inconsistent. Fixed the same way; preserved the original `delta` JSON
  string type (`"-1"`/`"1"`, not bare numbers) to avoid any behavior
  change in what htmx actually posts.
- **MINOR (guard's own bar) — fixed.** Writing the new hx-vals regression
  guard (see below) itself surfaced two more sites the review flagged as
  lower-risk but not zero-risk: `index.html:50` (a **plugin-supplied**
  payment-method ID — not architecturally guaranteed quote-free) and
  `catalog_table.html:29` (item ID). Fixed both rather than immediately
  needing `// i18n:ignore` exceptions on a guard that had just landed.
- **MINOR (message overclaimed scope) — fixed.** The guard's success
  message said "no hardcoded Go-side response strings" as if the scan
  were exhaustive. The reviewer wrote a probe file with 7 realistic
  evasions (backtick raw strings, variable indirection, literal-as-argument
  rather than format-string, cross-line calls, `Fprintln`, single-word
  strings like "Saved") — all 7 slipped past. Added `Fprintln` to the
  regex (cheap, real coverage gain); reworded the doc comment and success
  message to state plainly this is a heuristic guarding the exact shape
  of bug this card found, not an exhaustive Go-string audit — a full
  audit would need real AST parsing, disproportionate to this card's
  scope. `http.Error` and JSON `"message"` fields remain deliberately
  out of scope (an existing, separate, larger inconsistency — e.g.
  ~60 hardcoded strings in `plugin_api.go` error paths alone — logged as
  a genuine future follow-up, not attempted here).
- **NIT (factually wrong comment) — fixed, independently re-verified
  myself.** The original comment claimed `template.JS` "tells
  html/template to skip escaping entirely... a literal apostrophe would
  break out of the single-quoted attribute unescaped." I wrote a small
  standalone `html/template` program and confirmed this is false for this
  specific context: a plain string and a `template.JS` value produce
  **byte-identical** escaped output when placed in an HTML attribute
  value (not a `<script>`/execution context) — `html/template`'s
  contextual escaper still applies attribute escaping to `template.JS`
  there. The choice to return a plain string is still correct — it
  doesn't depend on the escaper's context classification staying the
  same if a call site ever moved into a `<script>` block — but the
  comment now states that as the real reason instead of a wrong claim.
- **NIT (checked, not applicable) — no change.** Reviewer flagged the new
  `ar.json` string using Latin `3` instead of Arabic-Indic `٣`. Checked
  the actual existing convention across `ar.json`'s other digit-containing
  strings (`"15 دقيقة"`, `"28 يوماً"`, `"04 123 4567"`, a VAT number) —
  all consistently use Latin digits in prose; `LocalizeDigits`/`digitsAr`
  is a separate runtime-formatting helper for money/quantity display, not
  applied to static translator strings. Fixing it would have made this
  string the *inconsistent* one.
- **NIT (key coupling) — fixed.** `plugins.import.success` was reused for
  the two `plugin_api.go` marketplace-install responses, coupling an
  unrelated template heading (`plugin_manual_import.html`) to an API
  response body — editing one's wording would silently change the other,
  and it also quietly changed the API text's casing. Added a dedicated
  `plugins.marketplace.install_success` key (same translated text,
  decoupled) to all 4 locales instead.
- **NIT (no regression guard for the exact bug found) — fixed.** Added a
  fourth guard check: flags any `hx-vals='{"..."` literal that still
  contains a `{{ ... }}` template action (the unsafe shape), while leaving
  genuinely-constant `hx-vals` (e.g. `{"order_type":"takeaway"}`, no
  template action) untouched. This is what actually caught the two
  additional sites above.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l` on every touched file — clean.
- `go test ./internal/httpx/... ./internal/pages/... ./internal/pages/catalog/...`
  and the full `go test ./...` — all green except
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, confirmed
  pre-existing/environmental by both myself and the independent reviewer,
  independently (root-in-container `chmod` probe, unrelated package).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  both green; the i18n guard now also enforces its own two new checks.
- **TDD, every fix**: `TestJSONVals_*` (the core helper),
  `TestBarcodeAttach_PanelHxValsSurvivesQuotedBarcode`,
  `TestButtonsUIFragment_HxValsSurvivesQuotedCode` — all confirmed red
  (real assertion failures, not compile errors) against the pre-fix code
  via manual file backup/restore (never `git checkout --`, since other
  work shared the tree), then green after the fix, independently
  re-verified by the reviewer for two of them.
- **Mutation-tested both new guard checks** (Go-string scan and hx-vals
  literal scan): reintroduced a real violation, confirmed the guard fails
  and names the exact file/line, restored, confirmed green.
- No real client/shop name or secret-shaped literal in the diff.
- Checked the two recurring bug classes this pipeline's history flags: no
  file writes were added (template funcs + Go string routing only), so
  neither the `os.MkdirAll` nor the `paths.Data(...)` class applies here.

## Safe to merge

Yes — every real finding (not the two checked-and-confirmed-not-applicable
ones) was fixed and re-verified, not just noted. The guard additions are
themselves mutation-tested, so this isn't a one-time fix but a standing
regression gate for the exact bug classes this card found.
