# `export.requested.ask` wire-schema drift guard (ut-docs#204)

**Card:** universaltill/ut-docs#204
**Branches:** `feat/204-export-contract-guard` (universal-till),
`docs/204-export-contract-guard` (ut-docs)
**Complexity:** medium — Dev at Sonnet, Review at a fresh-context Opus
instance (different model from the implementer), per `scrum-master`'s
model-routing table.

## What shipped

`internal/pages/export_contract_test.go` (new, test-only — no production
code touched anywhere in the diff): reflection-based tests pinning the JSON
wire shape of every type `export.requested.ask` puts on the wire.

- `exportRequestPayload`, `exportResponse` (unexported, `internal/pages/data_api.go`)
- `data.ExportSaleRow`, `data.ExportSaleTaxLine`, `data.ExportSalePayment`,
  `data.ExportStockRow`, `data.ExportRow`, `data.TaxCodeView`,
  `data.EODCloseExport`

A helper `assertJSONFields` walks each struct's fields via `reflect` and
compares the `json` tag names, in declaration order, against a hardcoded
expected list. A rename/add/remove/reorder on the Go side now fails CI
instead of silently drifting from the contract documented in ut-docs'
`reference/plugin-manifest.md`.

Companion ut-docs commit adds a "Drift guard" paragraph to that section
pointing at the test.

Scope is deliberately the card's **"at minimum"** acceptance bar (a
schema both sides can check against), not the **"ideally"** cross-repo
contract test — `ut-plugin-tax-de` is outside this pipeline lane's repo
access. `data.EODReport` (`eod_closes[].report`'s own nested shape) is
also deliberately out of scope: it is the archived Z-report format, which
predates this event, and `plugin-manifest.md` already treats it in prose
rather than field-by-field.

## Independent review

**Verdict: PASS, after one substantive finding fixed during review.**

### Pinned field lists — verified field-for-field against the source

Every pinned list was checked against the actual struct definitions
(`internal/pages/data_api.go`, `internal/data/export_repo.go`,
`internal/data/catalog_repo.go`) rather than taken on trust. All 9 types,
all 48 fields: **complete, correct, and in declaration order** — no field
silently omitted from a pin (which would have defeated the purpose: a
silent addition would go uncaught), none pinned that doesn't exist.

### F1 (substantive, fixed during review) — `omitempty` was not pinned

The original test discarded the `omitempty` bit and pinned bare field
names only. That leaves a real hole in exactly the drift class this card
exists to catch, because in this contract `omitempty` is **load-bearing
and separately documented**, not formatting:

- `plugin-manifest.md` documents `variant_id`/`variant_name` as *"omitted
  entirely (`omitempty`) on an item-level row, so a plugin can branch on
  their presence"*.
- `exportRequestPayload.EODCloses` carries an explicit source comment
  *"Deliberately NO omitempty"* — because `[]` (supported host, no closes
  in range) must stay wire-distinguishable from `null` (entity not
  declared / not granted), and `plugin-manifest.md` tells plugin authors
  to route on exactly that difference.

So adding `,omitempty` to `EODCloses`, or dropping it from `variant_id`,
would break documented plugin-visible behaviour **without changing a
single field name** — invisible to the original pin. Confirmed
empirically, not just by reading: with the original test, mutating
`EODCloses` to `json:"eod_closes,omitempty"` still passed.

Fixed by having the pin carry the option: a `want` entry has a
`,omitempty` suffix exactly when the field's tag does. Re-mutated
afterwards and the same change now fails loudly:

```
exportRequestPayload: wire field set/order changed.
  got:  [from to entry_key sales stock items tax_codes eod_closes,omitempty]
  want: [from to entry_key sales stock items tax_codes eod_closes]
```

The ut-docs paragraph was updated in the same pass to say the pin covers
`omitempty` and why.

### F2 (minor, fixed) — helper reimplemented `strings.Cut`; two dead returns

`stripJSONTagOptions` hand-rolled a comma splitter over ~20 lines and
returned `(name, hasOmitempty, rest)` of which callers used only `name` —
`rest` in particular carried fiddly index arithmetic (`tag[len(name)+1:]`)
that nothing exercised. Its parsing was nonetheless **verified correct**
before replacing it: a standalone harness compared its output against what
`encoding/json` itself does for `ok`, `filename,omitempty`, `-`, `-,`, ``,
`,omitempty`, `a,omitempty,string`, `a,,omitempty`, `omitempty`. It agreed
on every realistic case. Two divergences, both on tags that do not occur
in these types and both *fail-closed* (loud `t.Fatalf`, never a silent
pass): a bare `json:""` or `json:",omitempty"` is rejected as "empty json
tag name" where `encoding/json` would fall back to the Go field name —
deliberate strictness, since this contract requires every field be
explicitly named.

Replaced with `splitJSONTag` using `strings.Cut`, which now also returns
the `omitempty` bit F1 needs, so the previously-dead return became the fix.
The one genuine bug in the original was that `json:"-,"` (a field named
literally `-`) was skipped as if it were `json:"-"`; now handled and
documented.

### F3 (minor, fixed) — unexported fields would have caused a false failure

`assertJSONFields` called `t.Fatalf` on any field lacking a `json` tag.
An unexported field never reaches the wire, so adding one (a cache field,
a mutex) would have failed this test with a misleading "every field on the
wire must be explicitly named" even though the wire shape was unchanged.
No such field exists in these types today; added an `f.PkgPath != ""` skip
so the guard can't cry wolf later.

### Accepted gaps (not defects)

- **Field types are not pinned.** Changing `Method string` to
  `Method int` under the same `json:"method"` tag would change the wire
  and go undetected. Accepted: the card's bar is a field-name schema, and
  a type pin would be noisy against benign changes that do *not* alter the
  wire (`money.Money` is `type Money int64` with no custom marshaler, so
  `int64` ↔ `money.Money` is wire-identical and would false-positive). A
  type change is also far more likely to be caught by the compiler and by
  the existing dispatcher tests than a tag rename is. Noted as a possible
  follow-up, not filed.
- **Reflection pin vs. byte-for-byte golden JSON.** The reflection
  approach is the right call here: a golden-JSON compare would need a
  fully-populated fixture of 9 nested types and would churn on unrelated
  value changes, while what a plugin author actually binds to is the field
  name set and its optionality — precisely what is pinned.
- **Same-repo pin, not a true cross-repo test.** The card's own "ideally"
  bar. `ut-plugin-tax-de` is not reachable from this lane; both the test
  and the doc paragraph say so explicitly rather than overclaiming.

### Doc claim independently verified (AC (a))

The card's AC (a) — payload/response documented field-for-field — was
re-read rather than taken on faith. `reference/plugin-manifest.md`
already covered it before this PR: the request envelope (`from`, `to`,
`entry_key`) and `sales`/`stock`/`items` in a worked JSON example, plus
`tax_codes` (`id`, `name`, `rate_bp`, `takeaway_rate_bp`, `is_active`)
and `eod_closes` (`z_number`, `report`) enumerated in prose, and the
response's five fields inline. **All 48 pinned fields are accounted for.**
One cosmetic observation, not a gap: `tax_codes`/`eod_closes` appear in
prose only, not in the JSON example block. Left alone — out of this
card's scope and the prose is unambiguous.

## Verified beyond automated tests

- `gofmt -l .` (clean), `go build ./...`, `go vet ./...` — all clean.
- `go test ./internal/pages/ -run 'TestExport.*Schema' -v` — all 7 pass.
- Full `go test ./internal/pages/... -count=1` (the slow, real-DB-backed
  suite) — green on the post-review-fix tree.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `bash scripts/ci/guard-data-access.sh` — passes (no SQL added; the diff
  is test-only).
- **Mutation-tested twice, personally, not trusted from the Dev's claim.**
  (1) `exportResponse`'s `json:"ok"` → `json:"okxxx"`: test fails with the
  expected got/want diff and the "update BOTH this test AND
  plugin-manifest.md" remediation message. (2) `EODCloses` →
  `json:"eod_closes,omitempty"`: passes against the original test, fails
  against the fixed one (F1 above). Both mutations were applied and
  reverted inside a single shell invocation on the shared checkout, with
  `git status` confirmed clean afterwards each time.
- No UI surface: the diff touches one `_test.go` file and nothing under
  `web/`, no `.html`, no locale file. The UX-guidelines, manual/help-topic
  and screenshot review steps correctly do not apply, and no README claim
  changed.
- No real client/shop name and no secret-shaped literal anywhere in either
  diff (grepped for credential keywords and long base64-ish runs; the only
  base64-adjacent token is the field name `content_b64` itself).
- Supersession check: `git log main -- internal/pages/export_contract_test.go`
  empty; ut-docs `reference/plugin-manifest.md` untouched on `main` since
  the branch point; ut-docs#204 still open and unclaimed by any other PR.

## Safe-to-merge verdict

**Yes.** Test-only, purely additive, zero production-code risk. One
substantive review finding (`omitempty` unpinned, F1) closed a real hole
in the guard's coverage and was verified by mutation both before and after
the fix; two minor helper-robustness findings fixed alongside it.

## Explicitly deferred

- A true cross-repo contract test against `ut-plugin-tax-de`, mirroring
  `internal/signing.TestCanonicalManifestMirrorsPOS` — the card's
  "ideally" bar, blocked on repo access from this lane. Left as a stated
  possible follow-up in `plugin-manifest.md`; not filed as a card.
- Pinning field *types* alongside names (accepted gap above).
- Pinning `data.EODReport`'s own nested fields — out of this event's
  contract scope by design, consistent with how the doc treats it.
