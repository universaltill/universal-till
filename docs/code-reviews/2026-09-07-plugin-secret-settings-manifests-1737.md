# ut-docs#1737 — declare `type: "secret"` in the stripe/sumup plugin manifests

Date: 2026-09-07
Reviewer: independent Reviewer (Opus), fresh context — `complexity:medium`
per the scrum-master skill's model routing (Sonnet built, Opus reviews).

Branches under review (one logical change across three repos):

| Repo | Branch | Commit |
| --- | --- | --- |
| `universal-till` | `feat/1737-plugin-secret-settings-manifests` | `72ab468` |
| `ut-plugin-payment-stripe` | `feat/1737-declare-secret-setting` | `66cd699` |
| `ut-plugin-payment-sumup` | `feat/1737-declare-secret-settings` | `f0f0022` |

## Verdict

**NOT MERGED — blocked.** The three diffs are individually correct, clean and
gate-green. They are blocked on a **cross-repo signing-contract defect in a
fourth repo (`ut-cloud`)** that makes this card's entire deliverable a no-op
for every marketplace install. Detail in "Blocking finding" below.

Nothing here is *wrong*; the problem is that it does not reach the shop.
Merging as-is would close ut-docs#1737 against an outcome that does not
actually happen in production, and tagging `v1.2.1` / `v1.1.1` (the card's
step 12) would publish two releases whose sole content is silently discarded
in transit.

## What shipped in each repo

**`ut-plugin-payment-stripe`** — `manifest.json` only: version `1.2.0` →
`1.2.1`, `"type": "secret"` added to the `stripe_secret_key` setting.

**`ut-plugin-payment-sumup`** — `manifest.json` only: version `1.1.0` →
`1.1.1`, `"type": "secret"` added to **both** `sumup_api_key` and
`sumup_affiliate_key`.

**`universal-till`** — test-only, no production code:
`internal/plugins/manifest_secret_setting_test.go` gains
`TestParseManifest_RealPaymentPluginSecretSettings` (+55 lines), asserting
both manifests' settings arrays parse and that `SettingDeclaredSecret` is
true for exactly the credential keys and false for
`currency` / `*_reader_id` / `sumup_merchant_code`.

## The "no behaviour change" claim — independently verified, TRUE

The claim was that all three keys *already* matched
`internal/secrets.IsSecretSettingKey`, so sealing-at-rest and settings-page
masking were live before this change. I read the implementation
(`internal/secrets/seal.go`, `IsSecretSettingKey`) rather than trusting the
summary, and probed it directly against the literal strings:

```
IsSecretSettingKey("stripe_secret_key"   ) = true      // contains "secret"
IsSecretSettingKey("sumup_api_key"       ) = true      // contains "api_key"
IsSecretSettingKey("sumup_affiliate_key" ) = true      // HasSuffix "_key"
IsSecretSettingKey("sumup_merchant_code" ) = false
IsSecretSettingKey("currency"            ) = false
IsSecretSettingKey("stripe_reader_id"    ) = false
IsSecretSettingKey("sumup_reader_id"     ) = false
```

Confirmed: this is defense-in-depth / self-documentation, **not** a
behaviour change for these three keys. The negative cases are also right —
`sumup_merchant_code` is a merchant identifier, not a credential, and is
correctly left undeclared.

## Blocking finding — `ut-cloud`'s signing mirror drops `type`

**`ut-cloud/internal/signing/manifest.go:57-61`** — `CanonicalSetting` has
**no `Type` field**:

```go
// CanonicalSetting mirrors plugins.ManifestSetting.
type CanonicalSetting struct {
	Key          string      `json:"key"`
	DefaultValue interface{} `json:"default_value,omitempty"`
	Scope        string      `json:"scope,omitempty"`
}
```

The POS side (`universal-till/internal/plugins/manifest.go:107-115`) *does*
have `Type string \`json:"type,omitempty"\``, added by ut-docs#1739. The
briefing I was given asserted ut-cloud "already has a `Type` field ... landed
with #1739". **That is false** — #1739 changed the POS side only. The mirror
drift has existed since #1739 but was *inert*, because no real manifest
populated `type`. This card is precisely what activates it.

That matters because `ut-cloud/CLAUDE.md`'s own "Signing contract (breakage
is silent — be careful)" section requires `CanonicalManifest` to mirror
`plugins.Manifest` field-for-field, and `signer.go`'s package doc states the
signer **round-trips the raw uploaded manifest through this struct**, so
unknown fields are dropped from the signed payload.

### Failure scenario (reproduced, not theorised)

I ran the real post-change sumup `manifest.json` through ut-cloud's actual
`Signer.SignManifest`:

```
INPUT  manifest contains 2 occurrence(s) of `"type": "secret"`
OUTPUT manifest contains 0 occurrence(s) of `"type":"secret"`
SHIPPED settings array:
"settings":[{"key":"sumup_api_key","default_value":"","scope":"global"},
 {"key":"sumup_merchant_code","default_value":"","scope":"global"},
 {"key":"sumup_affiliate_key","default_value":"","scope":"global"},
 {"key":"currency","default_value":"eur","scope":"global"},
 {"key":"sumup_reader_id","default_value":"","scope":"register"}]
```

The full chain, each link verified in source:

1. `release.yml` (both plugin repos) publishes to the marketplace and
   auto-approves — and the approve step *signs* it.
2. `ut-cloud/internal/reviews/approver.go:82` → `Signer.SignBundle`.
3. `signer.go:123-126` — for the bundle's root `manifest.json`, `data =
   signed`, i.e. the tarball's manifest is **replaced** by the round-tripped
   `CanonicalManifest`, with `type` gone.
4. The POS installs that bundle to `paths.Plugins(id, version,
   "manifest.json")`.
5. `internal/plugins/secret_settings.go`, `InstalledManifest` reads exactly
   that on-disk file, so `SettingDeclaredSecret("stripe_secret_key")` returns
   **false** on every marketplace install.

Net effect: the manifests still say `type: "secret"` in their git repos and
in dev/sideload installs, but every plugin that reaches a real till via the
marketplace has the declaration stripped. Masking and sealing keep working —
but only via `IsSecretSettingKey`, the exact fallback this card exists to stop
relying on.

Signature verification does **not** break (POS `Type` carries `omitempty`, so
the stripped manifest re-marshals to bytes matching the cloud's canonical
form). The failure is entirely silent — which is the specific hazard
`ut-cloud/CLAUDE.md` calls out.

### Required fix (in `ut-cloud`, before this card can ship)

Add `Type string \`json:"type,omitempty"\`` to `CanonicalSetting`, in the same
field order as the POS struct. It must be `omitempty` so already-signed
manifests without `type` keep verifying.

### Secondary finding — the documented mirror guard does not cover this

`ut-cloud/CLAUDE.md` says "A cross-repo test guards this". It does not, for
settings. `scripts/ci/contract_guard.sh` is unrelated (it governs
`specs/001-plugin-marketplace/contracts/` OpenAPI churn). The only real guard
is `internal/signing/signer_test.go`'s `verifyLikePOS`, whose fixture manifest
has **no `settings` array at all** — which is why #1739's drift went
unnoticed for a whole card. Worth a follow-up: give the signer fixture a
settings array including a `type: "secret"` entry, so a future field addition
fails loudly.

## Independent gate re-run (`universal-till`, feature branch)

| Check | Result |
| --- | --- |
| `gofmt -l .` | clean (no output) |
| `go build ./...` | rc=0 |
| `go vet ./...` | rc=0 |
| `go test ./internal/plugins/... ./internal/data/... ./internal/pages/...` | all `ok` (data 33.8s, pages 134.0s) |
| `golangci-lint run ./...` | `0 issues.` |
| every `scripts/ci/*.sh` in `ci.yml`'s `build` job | **all PASS** (36 scripts) |

Two scripts failed — `guard-deadcode-baseline.sh` and its `_test.sh` — and
both are **environmental, not caused by this diff**:

```
Package 'gtk+-3.0', required by 'virtual:world', not found
Package 'webkit2gtk-4.1', required by 'virtual:world', not found
deadcode: packages contain errors
```

They live in the separate `desktop-shell` job (which installs the GTK/WebKit
dev headers first), not in `build`, and I confirmed they fail identically on
a pristine `main` worktree. Consistent with `CLAUDE.md`'s standing note that
`cmd/unitill-desktop` awaits a `-tags=desktop` pass with real headers
(ut-docs#1581).

### TDD re-verification (isolated worktree, ut-docs#386)

Done in a detached worktree at `72ab468` — `git worktree add --detach` was
required because the branch is already checked out in the main tree; no
revert-then-restore on the shared checkout. Stripping `,"type":"secret"` from
the new test's embedded fixtures:

```
=== RUN   TestParseManifest_RealPaymentPluginSecretSettings
    manifest_secret_setting_test.go:103: stripe_secret_key should be declared secret
    manifest_secret_setting_test.go:124: sumup_api_key should be declared secret
    manifest_secret_setting_test.go:124: sumup_affiliate_key should be declared secret
--- FAIL
```

Exactly three failures, one per key. `git checkout --` restored it and it
passes again. The test is genuinely falsifiable, not a false-pass. Both
worktrees removed afterwards.

### Plugin repos

Both manifests are valid JSON, `type` appears only on credential-shaped keys,
and each repo's own scripts pass on the bumped version:

```
stripe: built bin/plugin.wasm (3174868 bytes) → ok com.universaltill.payment-stripe v1.2.1
sumup:  built bin/plugin.wasm (3193530 bytes) → ok com.universaltill.payment-sumup  v1.1.1
sumup:  go test ./... → ok .../ut-plugin-payment-sumup/src
```

Version bumps check out against each repo's real history: stripe's `v1.2.0`
tag matches its main manifest (`1.2.0` → `1.2.1` is a clean patch bump), while
sumup's last tag is `v1.0.0` against a main manifest of `1.1.0` — the
**untagged 1.1.0 drift is real**, not a briefing error. That 1.1.0 content is
the 2026-08-01 reader tip auto-sync (ut-docs#43), never released; tracked
separately as ut-docs#1756.

Note `scripts/validate.sh` in both repos does not validate setting `type`
values at all, so a typo like `"type": "secrets"` would pass validation and be
rejected only later by `ParseManifest`'s `isValidSettingType`. Not hit here
(both values are correct); worth folding into the same follow-up.

## Judgment calls

**`sumup_affiliate_key` scope-widening — justified, correct call.** The card
names only `sumup_api_key`, but declaring the affiliate key too was right, on
three independent grounds: it is genuinely a credential (the manifest's own
description has the operator "set your SumUp API key, merchant code, and (for
reader payments) an affiliate key"); it already matched
`IsSecretSettingKey` via the `_key` suffix, so declaring it changes no
behaviour and carries no regression risk; and leaving it out would have
produced an actively misleading manifest where one of two adjacent
credentials is marked and the other is not — a reader would reasonably infer
the affiliate key is *not* sensitive. The card's intent is "these plugins
declare their secrets", and a partial declaration would not satisfy it. This
is scope *completion*, not scope creep, and it does not touch anything the
card excluded. It did not need to be raised as a question first.
`sumup_merchant_code` was correctly left alone, which is the tell that the
line was drawn deliberately rather than by pattern-matching every setting.

**AC #3 ("verify the upgrade path works... with a test confirming the
transition") — already satisfied before this card; the new test is
appropriate but is not what closes that AC.** `internal/data/plugin_repo_secret_settings_test.go`
already covers the real upgrade path, using these very keys as literals:
`TestPluginSetting_LegacyPlaintextReadsUnchangedAndSealsOnNextWrite`
(`sumup_api_key`: pre-ADR-0082 plaintext row reads back unchanged, then seals
on next write) and `TestPluginSetting_HeuristicSecretKeyIsSealedAtRestAndReadsBack`
(`stripe_secret_key`), plus
`TestReconcilePluginSettings_PreExistingUnsealedRowIsResealedOnReconcile` for
the install-time reconcile. The transition is genuinely covered.

The new parse-level test adds something different and worth having — that
these two manifests' *actual shapes* parse and declare what the plugin repos
claim — so it is the right level for what it does. But one honest caveat: its
comment says it "parses the real `settings` arrays shipped in each plugin's
manifest.json", and it does not. It parses a **copy pasted into the test**.
Deleting `type: "secret"` from the real `ut-plugin-payment-sumup/manifest.json`
tomorrow would leave this test green. Given the POS repo cannot read a sibling
repo's files at test time, a copy is arguably the only option — but the
comment overstates the guarantee and should say "mirrors" rather than
"parses the real". Non-blocking; flagged for the follow-up.

Given the blocking finding above, the AC that is genuinely *not* met is the
implicit one: that a real install ends up with the declaration.

## Recurring bug classes checked (both N/A, confirmed)

- **File-write handler missing `os.MkdirAll`** — not applicable. The entire
  change is two `manifest.json` edits and one `_test.go` addition; no file
  writes are introduced anywhere in the diff.
- **cwd-relative path instead of `paths.Data(...)`** — not applicable. No
  paths are introduced. The adjacent code this touches already resolves
  correctly via `paths.Plugins()` (`secret_settings.go`, `InstalledManifest`),
  including a traversal guard.

## Secrets / client-name scan

`default_value` is `""` for `stripe_secret_key`, `sumup_api_key` and
`sumup_affiliate_key` — no literal credential anywhere. Grepped all three
diffs for `sk_live`/`sk_test`/`pk_live`/bearer tokens/long opaque strings and
for real shop or client names: nothing. The only long-string hits were the new
test's own Go identifiers.

Git identity is correct in all three repos —
`Farshid Mirza <4035824+farshidmirza@users.noreply.github.com>` on every
commit under review, no AI-tool default.

## Deferred / follow-ups

1. **Blocker, must land first (ut-cloud):** add `Type` to `CanonicalSetting`.
2. Give `signer_test.go`'s fixture a `settings` array with a `type: "secret"`
   entry so mirror drift fails loudly; the claim in `ut-cloud/CLAUDE.md` that
   a cross-repo test guards this is currently not true for settings.
3. Teach both plugin repos' `scripts/validate.sh` to enum-check setting
   `type`.
4. Soften the new test's comment from "parses the real ... manifest.json" to
   "mirrors", and consider a drift check.
5. sumup's untagged 1.1.0 content (ut-docs#1756) — unchanged by this review;
   whoever eventually tags `v1.1.1` should expect release notes spanning
   ut-docs#43 too.

No push, no PR, no merge, no tag was performed. All three branches remain
local and unpushed, pending the orchestrator's decision on the ut-cloud fix.
