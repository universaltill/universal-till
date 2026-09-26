# Review — DE receipt QR from the signer (ut-docs#2880)

**Change (3 repos):** contract `fiscal-sign-ask.md` 1.10.0 — an `approved`
answer may carry a generic `receipt` object (`qr_payload` ≤ 1 KiB, `lines`
≤ 10 × 200). `ut-plugin-tax-de` 0.8.0 passes fiskaly's `qr_code_data` through
verbatim as `receipt.qr_payload`. `universal-till`: migration 048
`fiscal_receipt_evidence` (per-till, first write wins), bounds-checked parse
(invalid object dropped + logged, sale still approves), stored at sale /
refund / return, both receipt paths draw the QR from the stored value;
`buildTSEQRPayload` (self-invented provisional `UT-TSE-V0|…`) deleted.
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Delivery: a till on core-with-this-change but tax-de 0.7.0 prints no QR until 0.8.0 is installed (packs never auto-update) | merge order plugin 0.8.0 → core; core PR/release notes say "requires tax-de ≥ 0.8.0 for the receipt QR"; no fallback (the old payload was never the DSFinV-K format; no live German signed sale exists, #1511) |
| 2 | minor | Thermal Meta lines clipped at 42 columns — a signer's 200-char line lost its tail | fixed, and wider than reported: **the existing TSE signature (88 chars) and serial (64) were also clipped on paper**; Meta lines now hard-wrap at the printer width on ESC/POS and text preview; test `TestRenderWrapsLongMetaLines` (failed first on both renderings) |
| 3 | minor | Contract "Last updated" not bumped | fixed |
| 4 | minor | Contract said lines print "under the fiscal block"; on ESC/POS they sit in the meta block with the TSE lines | wording fixed |
| 5 | minor | `unicode.IsControl` lets U+2028/2029 and bidi Cf through | accepted (escaped on screen; bytes on paper) |
| 6 | minor | Unlabelled lines block for a non-DE signer | accepted (cosmetic, no such signer yet) |

**Checked, no issue (reviewer):** payload verbatim end to end (real sandbox
fixture pinned; compiled plugin's output compared); fiskaly `qr_code_data` is
the BSI TR-03153 / DSFinV-K receipt QR content; plugin omits `receipt` when
not approved / blank / > 1 KiB; core bounds drop the whole object and never
fail a sale; persist failure journalled with `evidence: "receipt"`; migration
048 free (main tops at 047, no open PR claims one), checksum pinned,
per-till in `sync_admin_repo` (never copied between tills); a return's
receipt carries its own Rückgabe QR, never the original's; html/template
escaping of lines, QR still `template.URL`; help text (en/de/tr/fa/ar)
consistent, no compliance-outcome wording (guard passed); contract version,
changelog and consumers consistent; plugin manifest bumped per its CLAUDE.md.

**Verification:** core `go build`, `go vet`, `go test -race` data/db/print +
pages (40-min race run 1009 s), guards (data-access, migration-collision,
kiosk-engine, i18n, compliance, competitor-naming, help-topics, help-drift,
page-http-error, price-history-sync, readme-links); `make docs-shots` for the
changed sell.md. Plugin `go test ./...`, wasip1 vet, build.sh, validate.sh
v0.8.0, i18n guard.

**Verdict:** safe to merge in order contract → plugin (release 0.8.0) → core.
