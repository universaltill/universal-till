# Code review: internal/fiscal package doc says multi-market, not "German" (ut-docs#1568)

**Card:** universaltill/ut-docs#1568 — "internal/fiscal: package doc says
\"German\" but gates DE+TR, and German TSE vocabulary is baked into
country-neutral core". Scoped by BA/Architect to **Problem 1 only** (the
stale package doc comment); acceptance criteria 2-4 (renaming the
TSE-specific identifiers, which needs its own ADR superseding/amending
ADR-0048) were split off to **ut-docs#1587** and are explicitly out of
scope here.

**Dev model:** Sonnet (inline, comment-only change) · **Review model:**
Opus, fresh context, independent read of the real code rather than the
handoff summary.

## What shipped

One rewritten paragraph — the opening of `internal/fiscal/fiscal.go`'s
package doc. The old first line read:

> `// Package fiscal implements the German TSE hard-gate policy engine`

which is what made a reader (the product owner) conclude the package was
misplaced country code that belongs in `ut-plugin-tax-de`. The new opening
says it is the **multi-market fiscal-readiness hard-gate policy engine**,
names today's gated markets (Germany, ADR-0048; Turkey, ut-docs#1208),
points at the exported `RequiresHardGate` as the one-line extension point
for the next market from ADR-0047, and adds an explicit note that the
settings keys, API route and on-disk path still carry Germany-specific
"TSE" naming inherited from ADR-0048, tracked for a possible rename in
ut-docs#1587 and not yet decided.

Everything after that first paragraph (the enforcement call-site list, the
deliberately-not-gated cases, the override route) is unchanged.

Plus two stale comments in the same package's test file, found by this
review — see below.

## Independently verified against the real code

Not taken on trust from the handoff; each factual claim in the new comment
was checked against the source:

- **"Today's gated markets are Germany and Turkey."** Correct.
  `RequiresHardGate` (`internal/fiscal/fiscal.go:141`) is a bare
  `switch country { case "DE", "TR": return true; default: return false }`
  — exactly two markets, no other caller-visible way in. `EvaluateGate`
  short-circuits to `Allowed` for anything else without touching the
  settings store at all. `TestRequiresHardGate` pins DE/TR true and
  GB/AT/""/lowercase-`de`/lowercase-`tr` false.
- **"RequiresHardGate is the extension point."** Correct, and it is the
  only decision point: `EvaluateGate` is its sole in-package caller, and
  `internal/pages.enforceFiscalGate` reaches the policy only through
  `EvaluateGate`. Adding a market really is one `case` arm.
- **Settings keys are still German-named.** Correct:
  `fiscal.tse_configured`, `fiscal.tse_failing_since`,
  `fiscal.tse_override_until` / `_reason` / `_actor`
  (`fiscal.go:56-85`). Only `fiscal.system_of_record` is neutral.
- **API route is still German-named.** Correct:
  `mux.HandleFunc("POST /api/fiscal/tse-override", ...)`
  (`internal/pages/fiscal_api.go:44`), handler `createTSEOverride`.
- **On-disk path is still German-named.** Correct:
  `paths.Data("fiscal", "tse_operational_credential.json")` via
  `tseCredentialRelPath` (`internal/fiscal/tse_credential_store.go:34`),
  type `TSECredentialStore`.
- **Cited references exist.** ADR-0048 (`0048-german-tse-hard-gate-and-owner-override.md`),
  ADR-0047, ADR-0050 and `reference/turkey-compliance.md` are all present
  in ut-docs; ut-docs#1208, #1568 and #1587 are all real cards, and #1587
  is open and carries exactly criteria 2-4.
- **Rendered output checked, not just the source.** `go doc ./internal/fiscal`
  renders the new paragraph correctly (no broken list, no mangled
  wrapping).
- **Genuinely comment-only.** `git diff` on `fiscal.go` touches lines 1-12
  only, every line a `//` comment; no declaration, no logic, no behaviour,
  no signature, no test assertion changed. `go doc` confirms the exported
  surface is byte-identical apart from the doc text.

## Commands run (all passed)

```
go build ./...
go vet ./...
go test -count=1 ./internal/fiscal/... ./internal/pages/... ./internal/cloudsync/...
gofmt -l internal/fiscal/          # prints nothing
bash scripts/ci/guard-data-access.sh
bash scripts/ci/guard-i18n.sh
go doc ./internal/fiscal
```

`-count=1` was used deliberately so the fiscal/pages results are a real
run and not a cached one. Both CI guards pass and are unaffected by
design — the diff adds no SQL (data-access guard) and no user-facing
string (i18n guard: 1411 template keys still resolve, all locales still
match `en.json`).

## Findings

**Minor, fixed in this change — two stale Germany-only comments remained
in the package.** Acceptance criterion 1 of #1568 is not only the package
doc: it says "*Any other comment in the package that implies German-only
is corrected*". Two in `internal/fiscal/fiscal_test.go` were missed:

1. `TestEvaluateGate_NonGatedCountryNeverReadsSettings` opened with "A
   **non-German** shop must be completely unaffected". As a general rule
   that is now false — Turkey is non-German and *is* affected. Reworded to
   "A shop in a non-gated market (GB here — not DE/TR)…", which is what
   the test (and its name) actually asserts.
2. `TestEvaluateGate_SettingsErrorPropagates` opened with "A **German**
   shop with a broken settings store fails closed". True of the `DE` case
   it runs, but it reads as a Germany-only rule; the test's own failure
   message already says "for a gated country". Reworded to "A shop in a
   gated market (DE here; TR behaves identically)…".

Both are comment-only, in test files, zero behavioural surface; the fiscal
suite was re-run uncached and `gofmt -l internal/fiscal/` is still silent
after them.

**No blocking findings.** Also checked and clean: no real client or shop
name anywhere in the diff (the only proper nouns are country names,
statutes and ADR/ticket ids); no secret-shaped literal; the new wording is
accurate, and it deliberately does not pre-empt #1587's decision — it
records the German naming as *tracked and undecided* rather than
justifying or condemning it.

## Noted, deliberately not fixed here

- **The TSE-named identifiers themselves** (`KeyTSEConfigured`,
  `TSECredentialStore`, `/api/fiscal/tse-override`,
  `tse_operational_credential.json`) and their per-symbol doc comments
  — e.g. `KeyTSEConfigured`'s "this shop has a TSE set up", which for a
  Turkish shop is really a GİB-certified YN ÖKC. These are #1568's
  criteria 2-4, now **ut-docs#1587**, and are ADR-gated (ADR-0007: the key
  names were defined by ADR-0048, so they cannot simply be renamed).
  Touching those comments here would have quietly pre-decided that ADR.
  The new package doc discharges the immediate risk by explaining the
  naming in place.
- **`internal/pages` still carries Germany-framed comments about this
  gate** — `pos_api.go:95` ("the ADR-0048 German TSE hard gate"),
  `index_page.go:110`, `self_order_shop.go:384`, and several gate test
  files. Out of #1568's scope (its criterion 1 is scoped to *this*
  package), and out of #1587's scope too, which is about identifiers.
  Worth a small follow-up sweep card; nothing here is wrong at runtime.
- **`tse_credential_store.go`'s file header sits directly above its
  `package fiscal` clause**, so Go treats it as a *second* package comment
  and `go doc ./internal/fiscal` concatenates the credential-storage prose
  onto the end of the package doc. Pre-existing (not introduced or
  worsened by this diff) and harmless, but it means the package doc is
  longer than its author's intent; a blank line between that comment block
  and the `package` clause would detach it. Left alone to keep this
  comment-only change to exactly its ticket.

## Not applicable

- **No UI surface.** Pure Go doc comment — no template, no handler, no
  user-facing string, no locale bundle. The UX-guidelines checklist,
  visual-check attestation and help-topic/manual update all do not apply.
- **No TDD/revert-restore verification.** There is no bug fix and no
  regression test in this diff — nothing to revert to watch fail, so the
  worktree-isolated revert dance was correctly skipped rather than
  performed for show.

## Verdict

**Safe to merge.** The change is comment-only, factually accurate against
the code as it exists today, and fixes exactly the misleading first line
that prompted the card. Two additional stale Germany-only comments in the
package's test file were found by this review and corrected in the same
change, completing acceptance criterion 1. Criteria 2-4 remain open and
correctly deferred to ut-docs#1587.
