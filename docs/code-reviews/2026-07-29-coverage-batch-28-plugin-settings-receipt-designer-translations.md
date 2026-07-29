# Test coverage batch 28: plugin_settings_page.go / receipt_designer.go / translations_page.go — one real bug found and fixed

2026-07-29

Three small, previously-untested admin pages, all at 0% coverage:
`internal/pages/plugin_settings_page.go` (generic plugin settings editor —
secret masking, per-till vs shop-wide scope), `internal/pages/receipt_designer.go`
(receipt header/footer/logo designer, live preview, test print),
`internal/pages/translations_page.go` (manager translation override editor).

## Bug: receipt logo upload never creates its own parent directory

`receiptLogoPath()` resolves to `paths.Data("public", "assets", "logo",
"receipt-logo.png")` — the stable per-user data directory (already fixed
for the cwd-relative-path bug class in an earlier session, see the
comment already in the file). But the upload handler
(`POST /api/receipt-designer/logo`) went straight to
`os.WriteFile(receiptLogoPath(), raw, 0o644)` with no `os.MkdirAll` first.
Every other file-write path in `internal/pages` creates its target
directory first — `ai_api.go` (item reference images), `backup_api.go`,
`import_page.go`, `plugin_api.go` (twice), `sync_assets.go` — this was
the one exception.

**Reachability check (not just "the dir happens to not exist in a
test")**: `paths.Data("public","assets","logo")` is only ever created by
`internal/paths/paths.go`'s `migrateLegacyUploadedAssets`, and only when
a legacy `web/public/assets/logo/receipt-logo.png` file already exists to
migrate — a one-time compatibility path for tills that hit the earlier
cwd-relative-path bug. `internal/app/app.go`'s `bootstrapPluginDirectories`
only creates the plugin cache/tmp dirs. So on a **genuinely fresh
install** (no legacy tree to migrate from — the common case for any new
shop from now on), nothing ever creates `public/assets/logo/` before the
first logo upload attempt. **The very first receipt logo upload on any
new till would 500 with "no such file or directory."**

Caught by `TestReceiptDesignerLogo_UploadThenRemove` failing against the
unmodified code with exactly that error before any fix was applied — not
a forced/contrived test, the natural first assertion in the natural test
for this handler.

**Fix**: `os.MkdirAll(filepath.Dir(receiptLogoPath()), 0o755)` before the
write, matching every sibling upload handler's pattern.

## Coverage added

- **`plugin_settings_page.go`** (0% → 81.5%/100%): `isSecretSettingKey`
  (table-driven: api_key/token/password/passwd/auth_value/private_key/
  `*_key`/bare `key` all classified secret); the settings editor page
  (manager-only, a non-secret value renders, a secret's actual value is
  never sent to the page); the save API (only plugin-declared keys are
  writable, a blank secret submission keeps the current value rather than
  clearing it, an update stays in its original scope so a register-scoped
  per-till setting doesn't leak shop-wide), plus the audit trail.
- **`receipt_designer.go`** (0% → 73-100%): `designFromForm` (blank
  headers skipped, checkboxes default false when absent from the form);
  the designer page (manager-only, renders the saved design); live
  preview (reflects unsaved form values, confirmed it does NOT persist
  them); save (persists all fields, writes the audit row); logo upload
  (rejects a non-image, the MkdirAll fix above, upload-then-remove
  round-trip with audit rows for both); test print (502 when no printer
  is configured — the safe, no-hardware-needed branch).
- **`translations_page.go`** (0% → 88.1%): manager-gated GET
  `/translations` and `/ui/translations-table` (locale list, query
  filtering); `POST /api/translations/set` (required-field validation,
  persists the override, **reloads the live in-memory translator
  immediately** — verified via `i18n.Entries()` showing `Source: "shop"`
  post-save, not just a DB row) and `/clear` (removes the override,
  falls back, audit rows for both). Auth here goes through
  `auth.FromContext`/`auth.WithUser` directly (not the `UT_AUTH=off`
  escape hatch the other two files use) since this handler checks the
  session user directly rather than via `isManagerOrAuthOff`.

## Independent review

Self-reviewed within this batch (solo, not delegated) — batches 25/27
this session used a different-model subagent for the implementation with
this session reviewing; this one was implemented directly. Flagging for
completeness: no separate cross-model pass was done on this specific
batch. Re-ran the regression test against the pre-fix code manually
(temporarily removed the `MkdirAll` line) to confirm it fails with the
exact "no such file or directory" error before the fix, consistent with
this session's TDD-verification standard for every other batch.

## Verification

`go build ./...`, `go clean -testcache && go test ./...` (whole repo),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.
`internal/pages` coverage: 58.2% → 63.2%.
