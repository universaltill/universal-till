package fiscal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/paths"
)

// withTestDataDir points the process-global data root at a temp dir so the
// default store never writes into the repo tree — same convention as
// internal/pages' initTestPaths.
func withTestDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init("") })
	return dir
}

func TestSigningDeviceCredentialStoreSaveLoadRoundtrip(t *testing.T) {
	store := NewSigningDeviceCredentialStoreAt(filepath.Join(t.TempDir(), "fiscal", "signing_device_credential.json"))

	if store.Exists() {
		t.Fatal("fresh store must not report an existing credential")
	}
	if _, ok, err := store.Load(); ok || err != nil {
		t.Fatalf("Load on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	cred := map[string]any{
		"api_key":    "test-key-123",
		"api_secret": "test-secret-456",
		"tss_id":     "tss-1",
		"nested":     map[string]any{"client_id": "client-9"},
	}
	if err := store.Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !store.Exists() {
		t.Fatal("Exists() false after a successful Save")
	}
	got, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after Save: ok=%v err=%v", ok, err)
	}
	if got["api_key"] != "test-key-123" || got["api_secret"] != "test-secret-456" {
		t.Fatalf("roundtrip lost fields: %+v", got)
	}
	nested, _ := got["nested"].(map[string]any)
	if nested["client_id"] != "client-9" {
		t.Fatalf("nested map lost: %+v", got)
	}
}

// The credential is a secret at rest: 0600 on the file, 0700 on its own
// directory — token_client.go's convention, per ADR-0053's universal-till
// card and ADR-0045 Decision 2.
func TestSigningDeviceCredentialStoreRestrictivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fiscal", "signing_device_credential.json")
	store := NewSigningDeviceCredentialStoreAt(path)
	if err := store.Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credential file perm = %o, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("credential dir perm = %o, want 0700", perm)
	}
}

// An empty credential map must be rejected — storing "nothing" as if it were
// a credential would let fiscal.signing_device_configured flip true on a hollow success.
func TestSigningDeviceCredentialStoreRejectsEmptyCredential(t *testing.T) {
	store := NewSigningDeviceCredentialStoreAt(filepath.Join(t.TempDir(), "cred.json"))
	if err := store.Save(nil); err == nil {
		t.Fatal("Save(nil) must error")
	}
	if err := store.Save(map[string]any{}); err == nil {
		t.Fatal("Save(empty) must error")
	}
	if store.Exists() {
		t.Fatal("a rejected save must not leave a file behind")
	}
}

// Regression test (Reviewer finding, ut-docs#802): os.WriteFile alone opens
// O_CREATE|O_TRUNC, so a write that fails partway through used to leave a
// truncated/zero-length file at the final path — which Exists() (and the
// caller's old stat-only idempotency check) would report as "a credential is
// stored," even though nothing readable was ever written. Save is now
// write-tmp-then-rename; this pins that a failed write leaves NEITHER the
// final path NOR a stray .tmp file behind. The failure is forced by making
// the final path's .tmp sibling a directory — os.WriteFile(tmp, ...) then
// fails with EISDIR regardless of the OS user's privileges (a permission-bit
// failure wouldn't reproduce under a root-run test).
func TestSigningDeviceCredentialStoreSaveFailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fiscal", "cred.json")
	store := NewSigningDeviceCredentialStoreAt(target)
	if err := os.MkdirAll(filepath.Join(dir, "fiscal", "cred.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := store.Save(map[string]any{"api_key": "x"}); err == nil {
		t.Fatal("Save must fail when its .tmp path is unwritable")
	}
	if store.Exists() {
		t.Fatal("a failed Save must not leave any file at the final path")
	}
}

// The default store lives under paths.Data("fiscal", ...) — the till's
// stable operational data root — and NOT under paths.Plugins (that tree is
// plugin auth cache, a different data class), never a cwd-relative path.
func TestSigningDeviceCredentialStoreDefaultPathUnderDataDir(t *testing.T) {
	dir := withTestDataDir(t)
	store := NewSigningDeviceCredentialStore()
	if !strings.HasPrefix(store.Path(), filepath.Join(dir, "fiscal")+string(filepath.Separator)) {
		t.Fatalf("default path %q not under <data>/fiscal/", store.Path())
	}
	if strings.HasPrefix(store.Path(), paths.Plugins()+string(filepath.Separator)) {
		t.Fatalf("default path %q must not live under the plugins tree", store.Path())
	}
	if err := store.Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatalf("Save at default path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fiscal", "signing_device_credential.json")); err != nil {
		t.Fatalf("credential not written under the data root: %v", err)
	}
}

// ADR-0045 Decision 2 / ADR-0034: the credential is excluded from
// diagnostics/support-bundle collection — asserted for real, not just in a
// comment. An issue-report bundle (this till's only support-bundle surface,
// ADR-0022) captured while the credential sits on disk must contain no trace
// of the secret material: the collector is allowlist-based (note + logs +
// operator-captured media), and this test pins that the credential never
// leaks into any file of a produced bundle, and that the bundle tree and the
// credential file live in disjoint directories.
func TestSigningDeviceCredentialExcludedFromSupportBundle(t *testing.T) {
	dataDir := withTestDataDir(t)

	// Point the pending-bundle queue where production points it (paths.Data)
	// so the layout under one shared data root is exactly production's.
	origPending := issuereport.PendingDir
	issuereport.PendingDir = filepath.Join(dataDir, "issue-reports", "pending")
	t.Cleanup(func() { issuereport.PendingDir = origPending })

	const secret = "super-secret-tse-credential-value-XYZZY"
	store := NewSigningDeviceCredentialStore()
	if err := store.Save(map[string]any{"api_key": secret}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	id, err := issuereport.Save("printer jams on long receipts", "en", nil, nil, nil)
	if err != nil {
		t.Fatalf("issuereport.Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 || bundles[0].Meta.ID != id {
		t.Fatalf("Pending: %v (%d bundles)", err, len(bundles))
	}

	// The credential file must not sit anywhere under the bundle tree.
	rel, err := filepath.Rel(bundles[0].Dir, store.Path())
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("credential path %q is INSIDE the support-bundle dir %q", store.Path(), bundles[0].Dir)
	}

	// And no file the bundle actually contains carries the secret.
	if err := filepath.WalkDir(bundles[0].Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), secret) {
			t.Fatalf("support bundle file %s contains the TSE credential secret", p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk bundle: %v", err)
	}

	// Belt and braces: the bundle's meta (the only structured content the
	// upload sends besides operator media) must not reference the credential
	// path either.
	mb, err := os.ReadFile(filepath.Join(bundles[0].Dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(mb, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if strings.Contains(string(mb), "signing_device_credential") {
		t.Fatal("bundle meta references the credential file")
	}
}

// ADR-0081 Decision 4: a till provisioned under ADR-0048's original naming
// holds its credential at fiscal/tse_operational_credential.json. The
// production constructor must find it there and move it (rename, not copy —
// a secret must not be left duplicated on disk) to the signing-device path
// before returning, so the credential is readable through the store's
// ordinary Load() with no operator action and no re-provisioning.
func TestSigningDeviceCredentialStoreMigratesLegacyTSEFilename(t *testing.T) {
	dir := withTestDataDir(t)
	legacy := filepath.Join(dir, "fiscal", "tse_operational_credential.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"api_key":"legacy-key-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewSigningDeviceCredentialStore()

	want := filepath.Join(dir, "fiscal", "signing_device_credential.json")
	if store.Path() != want {
		t.Fatalf("store path = %q, want %q", store.Path(), want)
	}
	cred, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after legacy migration: ok=%v err=%v", ok, err)
	}
	if cred["api_key"] != "legacy-key-1" {
		t.Fatalf("migrated credential lost its content: %+v", cred)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy file still present (stat err=%v) — must be moved, not copied", err)
	}
	fi, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat migrated file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("migrated credential perm = %o, want 0600 preserved by the rename", perm)
	}
}

// If both files exist, the signing-device path is authoritative and the
// legacy file is left exactly where it was: the constructor must never
// overwrite a credential a newer provisioning already wrote.
func TestSigningDeviceCredentialStoreKeepsNewFileWhenLegacyAlsoPresent(t *testing.T) {
	dir := withTestDataDir(t)
	fiscalDir := filepath.Join(dir, "fiscal")
	if err := os.MkdirAll(fiscalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(fiscalDir, "tse_operational_credential.json")
	current := filepath.Join(fiscalDir, "signing_device_credential.json")
	if err := os.WriteFile(legacy, []byte(`{"api_key":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(`{"api_key":"current"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cred, ok, err := NewSigningDeviceCredentialStore().Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if cred["api_key"] != "current" {
		t.Fatalf("new-path credential was clobbered by the legacy one: %+v", cred)
	}
	if b, err := os.ReadFile(legacy); err != nil || string(b) != `{"api_key":"stale"}` {
		t.Fatalf("legacy file must be left untouched when the new path already exists: %q, %v", b, err)
	}
}

// A fresh install (no fiscal/ dir at all) must construct cleanly with
// nothing stored — no directory created, no error, no stray file.
func TestSigningDeviceCredentialStoreFreshInstallHasNothingToMigrate(t *testing.T) {
	dir := withTestDataDir(t)
	store := NewSigningDeviceCredentialStore()
	if store.Exists() {
		t.Fatal("fresh install must not report a credential")
	}
	if _, ok, err := store.Load(); ok || err != nil {
		t.Fatalf("Load on fresh install: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fiscal")); !os.IsNotExist(err) {
		t.Fatalf("constructing the store must not create fiscal/ on a fresh install (stat err=%v)", err)
	}
}
