package data

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0082 (ut-docs#1739): plugin settings whose manifest declares
// `type: "secret"` OR whose key name matches the credential heuristic are
// sealed (AES-256-GCM, key outside the database) before they hit
// plugin_settings.value_json, and opened transparently on every read path
// the repository exposes. These tests query the raw column directly —
// bypassing GetPluginSetting — to prove what is actually at rest.

func newSecretSettingsTestDB(t *testing.T) (*db.DB, *PluginRepo) {
	t.Helper()
	d, repo := newPluginLifecycleTestDB(t)
	seedCatalogEntry(t, d, "com.example.pay", "1.0.0")
	if err := repo.InstallPlugin(context.Background(), nil, "com.example.pay"); err != nil {
		t.Fatal(err)
	}
	return d, repo
}

func rawValueJSON(t *testing.T, d *db.DB, pluginID, key string) string {
	t.Helper()
	var v string
	if err := d.QueryRow(`SELECT value_json FROM plugin_settings WHERE plugin_id = ? AND key = ?`, pluginID, key).Scan(&v); err != nil {
		t.Fatalf("raw value_json for %s/%s: %v", pluginID, key, err)
	}
	return v
}

func TestPluginSetting_HeuristicSecretKeyIsSealedAtRestAndReadsBack(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	const plain = `"sk_live_4242"`

	// UpsertPluginSetting (global convenience) — declares nothing; the key
	// name alone must trigger sealing.
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "stripe_secret_key", plain); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	raw := rawValueJSON(t, d, "com.example.pay", "stripe_secret_key")
	if !secrets.IsSealed(raw) {
		t.Fatalf("value_json at rest = %q, want a %q-prefixed sealed value", raw, secrets.Prefix)
	}
	if strings.Contains(raw, "sk_live") {
		t.Fatalf("value_json at rest leaks the credential: %q", raw)
	}

	got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "stripe_secret_key")
	if err != nil || !found || got != plain {
		t.Fatalf("GetPluginSetting = %q found=%v err=%v, want the opened plaintext %q", got, found, err, plain)
	}
	rows, err := repo.ListPluginSettings(ctx, "com.example.pay")
	if err != nil {
		t.Fatal(err)
	}
	var listed string
	for _, r := range rows {
		if r.Key == "stripe_secret_key" {
			listed = r.ValueJSON
		}
	}
	if listed != plain {
		t.Fatalf("ListPluginSettings value = %q, want the opened plaintext %q", listed, plain)
	}

	// A second write re-seals (fresh nonce) and still reads back.
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "stripe_secret_key", `"sk_live_9999"`); err != nil {
		t.Fatal(err)
	}
	raw2 := rawValueJSON(t, d, "com.example.pay", "stripe_secret_key")
	if raw2 == raw || !secrets.IsSealed(raw2) || strings.Contains(raw2, "9999") {
		t.Fatalf("second write at rest = %q", raw2)
	}
	if got, _, _ := repo.GetPluginSetting(ctx, "com.example.pay", "stripe_secret_key"); got != `"sk_live_9999"` {
		t.Fatalf("after second write: %q", got)
	}
}

func TestPluginSettingScoped_DeclaredSecretIsSealedAndNonSecretIsNot(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()

	// "merchant_code" matches no heuristic — only the manifest declaration
	// (declaredSecret=true) makes it a secret.
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "merchant_code", `"M-1"`, "global", true); err != nil {
		t.Fatal(err)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "merchant_code"); !secrets.IsSealed(raw) || strings.Contains(raw, "M-1") {
		t.Fatalf("declared-secret value at rest = %q, want sealed", raw)
	}
	if got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "merchant_code"); err != nil || !found || got != `"M-1"` {
		t.Fatalf("GetPluginSetting = %q %v %v", got, found, err)
	}

	// A plain setting stays plain JSON at rest — sealing everything would
	// make the LAN-sync bundle and every hand inspection opaque for no
	// security gain.
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "endpoint", `"https://api.example"`, "global", false); err != nil {
		t.Fatal(err)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "endpoint"); raw != `"https://api.example"` {
		t.Fatalf("non-secret value at rest = %q, want plain JSON", raw)
	}

	// Register scope seals too (a per-till reader secret).
	if err := repo.UpsertPluginSettingScoped(ctx, "com.example.pay", "reader_api_key", `"rk_1"`, "register", false); err != nil {
		t.Fatal(err)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "reader_api_key"); !secrets.IsSealed(raw) {
		t.Fatalf("register-scoped secret at rest = %q, want sealed", raw)
	}
}

// A row written before ADR-0082 holds raw JSON. It must read back unchanged
// (no prefix ⇒ legacy plaintext, not an error), and its NEXT write seals it.
func TestPluginSetting_LegacyPlaintextReadsUnchangedAndSealsOnNextWrite(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('legacy', 'com.example.pay', 'sumup_api_key', '"sup_legacy"', 'global')`)

	got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "sumup_api_key")
	if err != nil || !found || got != `"sup_legacy"` {
		t.Fatalf("legacy read = %q found=%v err=%v, want the plaintext unchanged", got, found, err)
	}
	rows, err := repo.ListPluginSettings(ctx, "com.example.pay")
	if err != nil || len(rows) != 1 || rows[0].ValueJSON != `"sup_legacy"` {
		t.Fatalf("legacy list = %+v %v", rows, err)
	}

	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "sumup_api_key", `"sup_new"`); err != nil {
		t.Fatal(err)
	}
	raw := rawValueJSON(t, d, "com.example.pay", "sumup_api_key")
	if !secrets.IsSealed(raw) || strings.Contains(raw, "sup_") {
		t.Fatalf("after rewrite at rest = %q, want sealed", raw)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.example.pay' AND key = 'sumup_api_key'`).Scan(&n)
	if n != 1 {
		t.Fatalf("rewrite must update the legacy row in place, got %d rows", n)
	}
	if got, _, _ := repo.GetPluginSetting(ctx, "com.example.pay", "sumup_api_key"); got != `"sup_new"` {
		t.Fatalf("after rewrite = %q", got)
	}
}

// ADR-0082 failure policy: a sealed value that cannot be opened (wrong key,
// corruption) reads as NOT CONFIGURED — the exact signal an absent row
// gives — and the ciphertext never reaches a caller.
func TestPluginSetting_CorruptSealedValueReadsAsNotSet(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	// Well-formed prefix + base64, but not sealed under this till's key.
	other := make([]byte, secrets.KeySize)
	for i := range other {
		other[i] = byte(i)
	}
	foreign, err := secrets.Seal(other, []byte(`"sk_from_another_shop"`))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('c1', 'com.example.pay', 'stripe_secret_key', ?, 'global')`, foreign)
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('c2', 'com.example.pay', 'webhook_secret', 'sealed:v1:***garbage***', 'global')`)
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('ok', 'com.example.pay', 'endpoint', '"https://x"', 'global')`)

	for _, key := range []string{"stripe_secret_key", "webhook_secret"} {
		got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", key)
		if err != nil || found || got != "" {
			t.Fatalf("GetPluginSetting(%s) = %q found=%v err=%v, want \"\", false, nil", key, got, found, err)
		}
	}
	rows, err := repo.ListPluginSettings(ctx, "com.example.pay")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, r := range rows {
		seen[r.Key] = r.ValueJSON
	}
	if len(seen) != 3 {
		t.Fatalf("ListPluginSettings must still list every declared key (so the operator can re-enter it), got %v", seen)
	}
	if seen["stripe_secret_key"] != "" || seen["webhook_secret"] != "" {
		t.Fatalf("unopenable rows must list as empty, never as ciphertext: %v", seen)
	}
	if seen["endpoint"] != `"https://x"` {
		t.Fatalf("sibling plain row must be unaffected: %v", seen)
	}
	for _, v := range seen {
		if strings.Contains(v, "sealed:") || strings.Contains(v, "another_shop") {
			t.Fatalf("ciphertext or foreign plaintext leaked: %v", seen)
		}
	}
	// A rewrite heals it: the operator re-enters the value.
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "stripe_secret_key", `"sk_fresh"`); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := repo.GetPluginSetting(ctx, "com.example.pay", "stripe_secret_key"); !found || got != `"sk_fresh"` {
		t.Fatalf("after re-entry = %q found=%v", got, found)
	}
}

// With no key available (replica that has not reached its primary yet, or
// a wiring bug) a secret write must FAIL, never fall back to plaintext —
// and a non-secret write must still succeed (checkout config unaffected).
func TestPluginSetting_SecretWriteWithoutKeyStoreFailsClosed(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	prev := secrets.Default()
	secrets.SetDefault(nil)
	t.Cleanup(func() { secrets.SetDefault(prev) })

	err := repo.UpsertPluginSetting(ctx, "com.example.pay", "stripe_secret_key", `"sk_live"`)
	if err == nil {
		t.Fatal("secret write with no key store must fail")
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE key = 'stripe_secret_key'`).Scan(&n)
	if n != 0 {
		t.Fatalf("failed secret write must not leave a row (found %d)", n)
	}
	if err := repo.UpsertPluginSetting(ctx, "com.example.pay", "endpoint", `"https://x"`); err != nil {
		t.Fatalf("non-secret write must not need the key store: %v", err)
	}
}

// ut-docs#1746: install-time default-value seeding (ReconcilePluginSettings)
// is a write path too, and must go through the same seal seam as every
// other one — a manifest default_value for a secret-typed or
// heuristic-matching key must never land in plugin_settings.value_json in
// cleartext just because it arrived via install rather than an operator
// edit.
func TestReconcilePluginSettings_DefaultValueSealedForSecretKeys(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()

	declared := []PluginSettingRow{
		// Heuristic match on the key name alone (no manifest declaration).
		{PluginID: "com.example.pay", Key: "stripe_secret_key", ValueJSON: `"sk_default_from_manifest"`, Scope: "global", UpdatedAt: time.Now()},
		// Only the manifest's `type: "secret"` declaration makes this one
		// secret — the key name matches no heuristic.
		{PluginID: "com.example.pay", Key: "merchant_code", ValueJSON: `"M-DEFAULT"`, Scope: "global", UpdatedAt: time.Now(), DeclaredSecret: true},
		// An ordinary setting must stay plain JSON at rest, unaffected.
		{PluginID: "com.example.pay", Key: "endpoint", ValueJSON: `"https://api.example"`, Scope: "global", UpdatedAt: time.Now()},
	}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.example.pay", declared); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	for _, tc := range []struct{ key, plaintextNeedle string }{
		{"stripe_secret_key", "sk_default_from_manifest"},
		{"merchant_code", "M-DEFAULT"},
	} {
		raw := rawValueJSON(t, d, "com.example.pay", tc.key)
		if !secrets.IsSealed(raw) {
			t.Fatalf("%s default at rest = %q, want a %q-prefixed sealed value (was written in cleartext by default-value seeding)", tc.key, raw, secrets.Prefix)
		}
		if strings.Contains(raw, tc.plaintextNeedle) {
			t.Fatalf("%s default at rest leaks the plaintext default: %q", tc.key, raw)
		}
	}
	if got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "stripe_secret_key"); err != nil || !found || got != `"sk_default_from_manifest"` {
		t.Fatalf("GetPluginSetting(stripe_secret_key) = %q found=%v err=%v", got, found, err)
	}
	if got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "merchant_code"); err != nil || !found || got != `"M-DEFAULT"` {
		t.Fatalf("GetPluginSetting(merchant_code) = %q found=%v err=%v", got, found, err)
	}
	if raw := rawValueJSON(t, d, "com.example.pay", "endpoint"); raw != `"https://api.example"` {
		t.Fatalf("non-secret default at rest = %q, want unchanged plain JSON", raw)
	}
}

// ut-docs#1752 (follow-up from #1746): #1746 only stops a NEW default row
// from landing in cleartext — it never rewrites value_json for a row that
// already exists. A plugin installed before #1746 shipped, whose
// secret-typed/heuristic-matching setting was never touched by an operator,
// stayed on its cleartext manifest default forever, healing only if someone
// happened to overwrite that exact key by hand. Reconcile must give it a
// one-time reseal instead.
func TestReconcilePluginSettings_PreExistingUnsealedRowIsResealedOnReconcile(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()

	// Seed the rows directly, bypassing every seal seam — exactly what the
	// pre-#1746 bug did: a heuristic-matching key and a declared-secret key,
	// both still sitting on their cleartext manifest default, plus an
	// ordinary sibling that must stay untouched.
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, updated_at) VALUES ('legacy-heuristic', 'com.example.pay', 'stripe_secret_key', '"sk_default_from_manifest"', 'global', '2026-01-01T00:00:00Z')`)
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id, updated_at) VALUES ('legacy-declared', 'com.example.pay', 'merchant_code', '"M-DEFAULT"', 'register', 'till-1', '2026-01-01T00:00:00Z')`)
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, updated_at) VALUES ('legacy-plain', 'com.example.pay', 'endpoint', '"https://api.example"', 'global', '2026-01-01T00:00:00Z')`)

	declared := []PluginSettingRow{
		{PluginID: "com.example.pay", Key: "stripe_secret_key", ValueJSON: `"sk_default_from_manifest"`, Scope: "global", UpdatedAt: time.Now()},
		{PluginID: "com.example.pay", Key: "merchant_code", ValueJSON: `"M-DEFAULT"`, Scope: "register", UpdatedAt: time.Now(), DeclaredSecret: true},
		{PluginID: "com.example.pay", Key: "endpoint", ValueJSON: `"https://api.example"`, Scope: "global", UpdatedAt: time.Now()},
	}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.example.pay", declared); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	for _, tc := range []struct{ key, plaintextNeedle string }{
		{"stripe_secret_key", "sk_default_from_manifest"},
		{"merchant_code", "M-DEFAULT"},
	} {
		raw := rawValueJSON(t, d, "com.example.pay", tc.key)
		if !secrets.IsSealed(raw) {
			t.Fatalf("%s pre-existing default at rest = %q, want sealed after a reconcile", tc.key, raw)
		}
		if strings.Contains(raw, tc.plaintextNeedle) {
			t.Fatalf("%s resealed row still leaks the plaintext: %q", tc.key, raw)
		}
		// Reseal in place, not a duplicate row.
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.example.pay' AND key = ?`, tc.key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s reseal must update the row in place, got %d rows", tc.key, n)
		}
	}
	// Opens back to the exact same plaintext — resealing must not change the value.
	if got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "stripe_secret_key"); err != nil || !found || got != `"sk_default_from_manifest"` {
		t.Fatalf("GetPluginSetting(stripe_secret_key) after reseal = %q found=%v err=%v", got, found, err)
	}
	if got, found, err := repo.GetPluginSetting(ctx, "com.example.pay", "merchant_code"); err != nil || !found || got != `"M-DEFAULT"` {
		t.Fatalf("GetPluginSetting(merchant_code) after reseal = %q found=%v err=%v", got, found, err)
	}
	// The register-scoped row's scope_id must survive the reseal — reseal
	// touches value_json only, never scope_id (that's the scope-move branch's
	// job, and scope didn't change here).
	var scopeID string
	if err := d.QueryRow(`SELECT scope_id FROM plugin_settings WHERE plugin_id = 'com.example.pay' AND key = 'merchant_code'`).Scan(&scopeID); err != nil {
		t.Fatal(err)
	}
	if scopeID != "till-1" {
		t.Fatalf("merchant_code scope_id = %q after reseal, want unchanged \"till-1\"", scopeID)
	}
	// The ordinary sibling is untouched — no needless rewrite.
	if raw := rawValueJSON(t, d, "com.example.pay", "endpoint"); raw != `"https://api.example"` {
		t.Fatalf("non-secret pre-existing row at rest = %q, want unchanged plain JSON", raw)
	}
}

// Same fail-closed guarantee as every other write in this seam: a manifest
// default for a secret key must not be persisted in cleartext just because
// no key store is available to seal it — the whole reconcile must error out
// rather than silently degrade.
func TestReconcilePluginSettings_DefaultValueFailsClosedWithoutKeyStore(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	prev := secrets.Default()
	secrets.SetDefault(nil)
	t.Cleanup(func() { secrets.SetDefault(prev) })

	declared := []PluginSettingRow{
		{PluginID: "com.example.pay", Key: "stripe_secret_key", ValueJSON: `"sk_default"`, Scope: "global", UpdatedAt: time.Now()},
	}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.example.pay", declared); err == nil {
		t.Fatal("reconcile seeding a secret default with no key store must fail")
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.example.pay' AND key = 'stripe_secret_key'`).Scan(&n)
	if n != 0 {
		t.Fatalf("failed default-value seal must not leave a cleartext row (found %d)", n)
	}
}

// Same fail-closed guarantee, for the reseal-existing-row path this PR adds
// (ut-docs#1752): a pre-existing cleartext row that can't be sealed right
// now (no key store available) must abort the whole reconcile, not silently
// leave the row in cleartext or partially succeed.
func TestReconcilePluginSettings_ResealExistingRowFailsClosedWithoutKeyStore(t *testing.T) {
	d, repo := newSecretSettingsTestDB(t)
	ctx := context.Background()
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, updated_at) VALUES ('legacy', 'com.example.pay', 'stripe_secret_key', '"sk_default_from_manifest"', 'global', '2026-01-01T00:00:00Z')`)

	prev := secrets.Default()
	secrets.SetDefault(nil)
	t.Cleanup(func() { secrets.SetDefault(prev) })

	declared := []PluginSettingRow{
		{PluginID: "com.example.pay", Key: "stripe_secret_key", ValueJSON: `"sk_default_from_manifest"`, Scope: "global", UpdatedAt: time.Now()},
	}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.example.pay", declared); err == nil {
		t.Fatal("reconcile reseal with no key store must fail")
	}
	raw := rawValueJSON(t, d, "com.example.pay", "stripe_secret_key")
	if raw != `"sk_default_from_manifest"` {
		t.Fatalf("failed reseal must leave the row exactly as it was, got %q", raw)
	}
}
