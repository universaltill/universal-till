package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#5157: an iCloud restore onto a second iPhone clones the data
// folder, and with it the settings rows holding the till's cloud identity.
// The install marker under secrets/ is excluded from backup, so a missing
// or different marker next to an enrolled DB means "restored".

// restoreDir points the data root at a fresh directory for one test.
func restoreDir(t *testing.T) string {
	t.Helper()
	orig := paths.DataDir()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init(orig) })
	return dir
}

func markerPath() string { return paths.Data(installMarkerRel...) }

func readMarker(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(markerPath())
	if err != nil {
		t.Fatalf("read install marker: %v", err)
	}
	return strings.TrimSpace(string(b))
}

func writeMarker(t *testing.T, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(markerPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath(), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// enrolledKV is a till enrolled with the cloud, with sales-side and
// entitlement rows that a restore check must never touch.
func enrolledKV(t *testing.T) *orderedKV {
	t.Helper()
	kv := newOrderedKV()
	for k, v := range map[string]string{
		keyDeviceID:          "till-old-device",
		keyDeviceRegistered:  "till-old-device",
		keyToken:             "tok-old-secret-0001",
		keyStoreID:           "store-1",
		keyMerchantID:        "merchant-1",
		keyEnrolledAt:        "2026-01-01T00:00:00Z",
		keyPairedAt:          "2026-01-02T00:00:00Z",
		keyPublicKey:         "pinned-public-key",
		"entitlement.plan":   "shop",
		"store.name":         "Corner Shop",
		"checkout.last_sale": "42",
	} {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	return kv
}

func totalSets(kv *orderedKV) int {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return len(kv.writes)
}

func TestRestoreCheck_FirstBootWritesMarker(t *testing.T) {
	restoreDir(t)
	kv := newOrderedKV()
	if CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("a fresh install is not a restore")
	}
	id := readMarker(t)
	if id == "" || kv.get(keyInstallID) != id {
		t.Fatalf("marker %q / row %q, want the same non-empty id", id, kv.get(keyInstallID))
	}
	if RestoredState(context.Background(), kv) {
		t.Fatal("restored state set on a fresh install")
	}
}

func TestRestoreCheck_UpgradeEnrolledNoMarkerKeepsIdentity(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t) // enrolled before this feature shipped: no row, no marker
	if CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("an upgrade must not be treated as a restore")
	}
	for k, want := range map[string]string{keyToken: "tok-old-secret-0001", keyDeviceID: "till-old-device", keyStoreID: "store-1"} {
		if got := kv.get(k); got != want {
			t.Fatalf("%s = %q, want it kept (%q)", k, got, want)
		}
	}
	if id := readMarker(t); id == "" || kv.get(keyInstallID) != id {
		t.Fatalf("marker %q / row %q, want adopted and matching", id, kv.get(keyInstallID))
	}
	if RestoredState(context.Background(), kv) {
		t.Fatal("restored flag set on upgrade")
	}
}

func TestRestoreCheck_NormalRestartNoop(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	writeMarker(t, "install-A")
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	n := totalSets(kv)
	if CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("matching marker reported a restore")
	}
	if got := totalSets(kv); got != n {
		t.Fatalf("a normal restart made %d Set calls, want 0", got-n)
	}
	if kv.get(keyToken) == "" {
		t.Fatal("token cleared on a normal restart")
	}
}

func TestRestoreCheck_MarkerMissingClearsIdentity_Main(t *testing.T) {
	restoreDir(t)
	logs := captureLog(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A") // marker file absent: restored
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("DB enrolled and marker missing must report a restore")
	}
	for _, k := range []string{keyToken, keyEnrolledAt, keyPairedAt, keyDeviceRegistered, keyStoreID, keyMerchantID} {
		if v := kv.get(k); v != "" {
			t.Fatalf("%s = %q, want cleared", k, v)
		}
	}
	if id := kv.get(keyDeviceID); id == "" || id == "till-old-device" || !strings.HasPrefix(id, "till-") {
		t.Fatalf("device_id = %q, want a fresh till-<uuid>", id)
	}
	if kv.get(keyPublicKey) != "pinned-public-key" || kv.get("entitlement.plan") != "shop" {
		t.Fatal("public key / entitlement rows must be kept")
	}
	if _, err := time.Parse(time.RFC3339, kv.get(keyRestoredAt)); err != nil {
		t.Fatalf("restored_at = %q: %v", kv.get(keyRestoredAt), err)
	}
	if !RestoredState(context.Background(), kv) {
		t.Fatal("RestoredState false after a restore")
	}
	id := readMarker(t)
	if id == "" || id == "install-A" || kv.get(keyInstallID) != id {
		t.Fatalf("marker %q / row %q, want a new matching id", id, kv.get(keyInstallID))
	}
	// A boot after the reset sees an unregistered till.
	resetState()
	t.Cleanup(resetState)
	initForTest(t, &config.Config{}, kv)
	if CurrentStatus().Registered {
		t.Fatal("CurrentStatus().Registered after restore, want false")
	}
	if out := logs.String(); strings.Contains(out, "tok-old-secret-0001") {
		t.Fatalf("token reached the log:\n%s", out)
	}
	// And the next restart is quiet.
	n := totalSets(kv)
	if CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) || totalSets(kv) != n {
		t.Fatal("the boot after a reset must be a no-op")
	}
}

func TestRestoreCheck_Replica_KeepsStore(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keySyncPrimaryURL, "http://192.168.1.5:8080")
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	if kv.get(keyStoreID) != "store-1" || kv.get(keyMerchantID) != "merchant-1" {
		t.Fatalf("replica lost its synced store: %q / %q", kv.get(keyStoreID), kv.get(keyMerchantID))
	}
	if kv.get(keyToken) != "" || kv.get(keyDeviceID) == "till-old-device" {
		t.Fatal("replica credential / device id not reset")
	}
}

func TestRestoreCheck_MismatchedMarker(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	writeMarker(t, "install-B") // another install's marker
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("a different marker is a restore")
	}
	if kv.get(keyToken) != "" {
		t.Fatal("token kept")
	}
	if id := readMarker(t); id == "install-B" || kv.get(keyInstallID) != id {
		t.Fatalf("marker %q / row %q, want re-marked", id, kv.get(keyInstallID))
	}
}

func TestRestoreCheck_UnreadableMarkerNoop(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if err := os.MkdirAll(markerPath(), 0o700); err != nil { // a directory where the file belongs
		t.Fatal(err)
	}
	n := totalSets(kv)
	if CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("never wipe when unsure")
	}
	if totalSets(kv) != n || kv.get(keyToken) == "" {
		t.Fatal("an unreadable marker must change nothing")
	}
}

func TestRestoreCheck_EnvPinnedNoWipe(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"device id": {Marketplace: config.MarketplaceConfig{DeviceID: "pinned"}},
		"client id": {Marketplace: config.MarketplaceConfig{ClientID: "pinned"}},
		"token":     {Marketplace: config.MarketplaceConfig{MerchantToken: "pinned"}},
	} {
		t.Run(name, func(t *testing.T) {
			restoreDir(t)
			kv := enrolledKV(t)
			_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
			if CheckRestoredInstall(context.Background(), cfg, kv, nil) {
				t.Fatal("an operator-pinned identity must not be reset")
			}
			if kv.get(keyToken) != "tok-old-secret-0001" || kv.get(keyDeviceID) != "till-old-device" {
				t.Fatal("pinned till lost its identity")
			}
			if id := readMarker(t); kv.get(keyInstallID) != id || id == "install-A" {
				t.Fatalf("marker %q / row %q, want re-marked", id, kv.get(keyInstallID))
			}
			if RestoredState(context.Background(), kv) {
				t.Fatal("restored flag set for a pinned till")
			}
		})
	}
}

// "Sales untouched": the reset writes only the till's cloud identity rows.
func TestRestoreCheck_TouchesOnlyIdentityKeys(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for _, w := range kv.writes {
		ok := false
		for _, p := range db.TillCloudIdentityPrefixes {
			if strings.HasPrefix(w[0], p) {
				ok = true
			}
		}
		if w[0] == keyStoreID || w[0] == keyMerchantID {
			ok = true
		}
		if !ok {
			t.Fatalf("restore check wrote %q, which is not a cloud identity key", w[0])
		}
	}
	if kv.fakeKV.m["checkout.last_sale"] != "42" || kv.fakeKV.m["store.name"] != "Corner Shop" {
		t.Fatal("sales-side rows changed")
	}
}

func TestPairClearsRestored(t *testing.T) {
	cloud := newFakePairCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	_ = kv.fakeKV.Set(context.Background(), keyRestoredAt, "2026-02-02T00:00:00Z")
	if !RestoredState(context.Background(), kv) {
		t.Fatal("setup: restored flag not set")
	}
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if RestoredState(context.Background(), kv) {
		t.Fatal("a successful pairing must clear the restored flag")
	}
}

// A crash after the reset but before the new marker and install id are
// written repeats the check on the next boot. The identity is already
// empty by then, so the restored flag must survive on its own or the till
// ends up unregistered with no chip saying why (review of ut-docs#5157).
func TestRestoreCheck_CrashBeforeRemarkKeepsRestoredFlag(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("first boot must report a restore")
	}
	// Simulate the crash: neither the marker nor the row were written.
	if err := os.Remove(markerPath()); err != nil {
		t.Fatal(err)
	}
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("the repeated check must reset again")
	}
	if !RestoredState(context.Background(), kv) {
		t.Fatal("restored flag lost after a crash between reset and re-mark")
	}
}

// ut-docs#5162: a restored REPLICA also carries its LAN identity (the sync
// bearer, till id and main-till TLS pins), so two devices would sync to the
// main till as one till. The restore check must drop them too.

const (
	lanBearer  = "lan-bearer-secret-0001"
	lanTillID  = "till-row-7"
	lanPin     = "sha256:aaaa"
	lanNextPin = "sha256:bbbb"
	lanURL     = "http://192.168.1.5:8080"
)

// replicaKV is enrolledKV for a joined till: LAN identity plus the rows a
// re-join keeps.
func replicaKV(t *testing.T) *orderedKV {
	t.Helper()
	kv := enrolledKV(t)
	for k, v := range map[string]string{
		keySyncPrimaryURL:            lanURL,
		keySyncBearer:                lanBearer,
		keySyncTillID:                lanTillID,
		keySyncTillName:              "Back office",
		"sync.till_role":             "sale",
		"sync.primary_till_id":       "main-1",
		"sync.last_contact_at":       "2026-10-01T00:00:00Z",
		"sync.push_cursor":           "117",
		"sync.primary_cert_pin":      lanPin,
		"sync.primary_cert_next_pin": lanNextPin,
		"sync.receipt_prefix":        "T2-",
		keyTillCloudID:               "cloud-id-of-original",
		keyInstallID:                 "install-A",
	} {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	return kv
}

var lanKeptKeys = []string{keySyncPrimaryURL, keySyncTillName, "sync.till_role", "sync.primary_till_id", "sync.last_contact_at", "sync.push_cursor"}

func TestRestoreCheck_ReplicaClearsLANIdentity(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	for _, k := range []string{keySyncBearer, keySyncTillID, "sync.primary_cert_pin", "sync.primary_cert_next_pin", keyTillCloudID} {
		if v := kv.get(k); v != "" {
			t.Fatalf("%s = %q, want cleared", k, v)
		}
	}
	want := map[string]string{keySyncPrimaryURL: lanURL, keySyncTillName: "Back office", "sync.till_role": "sale",
		"sync.primary_till_id": "main-1", "sync.last_contact_at": "2026-10-01T00:00:00Z", "sync.push_cursor": "117"}
	for _, k := range lanKeptKeys {
		if got := kv.get(k); got != want[k] {
			t.Fatalf("%s = %q, want kept (%q)", k, got, want[k])
		}
	}
	if p := kv.get("sync.receipt_prefix"); !regexp.MustCompile(`^R[0-9a-f]{4}-$`).MatchString(p) {
		t.Fatalf("sync.receipt_prefix = %q, want a provisional R<4 hex>- prefix", p)
	}
	if _, err := time.Parse(time.RFC3339, kv.get(keyRestoredAt)); err != nil {
		t.Fatalf("restored_at = %q: %v", kv.get(keyRestoredAt), err)
	}
	if kv.get(keyToken) != "" || kv.get(keyDeviceID) == "till-old-device" {
		t.Fatal("cloud identity not reset")
	}
}

func TestRestoreCheck_ReplicaNormalRestartChangesNothing(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	writeMarker(t, "install-A")
	before := map[string]string{}
	for k, v := range kv.fakeKV.m {
		before[k] = v
	}
	n := totalSets(kv)
	if CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("matching marker reported a restore")
	}
	if totalSets(kv) != n {
		t.Fatal("a normal restart wrote settings")
	}
	for k, v := range kv.fakeKV.m {
		if before[k] != v {
			t.Fatalf("%s changed on a normal restart", k)
		}
	}
}

func TestRestoreCheck_MainTillClearsCloudIDNoSyncKeys(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t) // a promoted main till: no sync.primary_url
	_ = kv.fakeKV.Set(context.Background(), keyTillCloudID, "promoted-from")
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	if v := kv.get(keyTillCloudID); v != "" {
		t.Fatalf("cloud_id = %q, want cleared", v)
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for _, w := range kv.writes {
		if strings.HasPrefix(w[0], "sync.") {
			t.Fatalf("a main till's restore check wrote %q", w[0])
		}
	}
}

func TestRestoreCheck_UnregisteredReplicaSetsRestoredFlag(t *testing.T) {
	restoreDir(t)
	kv := newOrderedKV() // joined but never cloud-registered: no token, no pairing
	for k, v := range map[string]string{keySyncPrimaryURL: lanURL, keySyncBearer: lanBearer, keySyncTillID: lanTillID, keyInstallID: "install-A"} {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	if !RestoredState(context.Background(), kv) {
		t.Fatal("a replica holding a LAN bearer must get the restored flag")
	}
	if kv.get(keySyncBearer) != "" {
		t.Fatal("bearer kept")
	}
}

func TestRestoreCheck_EnvPinnedReplicaStillClearsLAN(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{MerchantToken: "pinned"}}
	if CheckRestoredInstall(context.Background(), cfg, kv, nil) {
		t.Fatal("a pinned cloud identity is not reset")
	}
	for _, k := range []string{keySyncBearer, keySyncTillID, "sync.primary_cert_pin", "sync.primary_cert_next_pin"} {
		if v := kv.get(k); v != "" {
			t.Fatalf("%s = %q, want cleared even when the cloud identity is pinned", k, v)
		}
	}
	if kv.get(keyToken) != "tok-old-secret-0001" || kv.get(keySyncPrimaryURL) != lanURL {
		t.Fatal("pinned cloud identity or primary_url touched")
	}
	if RestoredState(context.Background(), kv) {
		t.Fatal("restored flag set for a pinned till")
	}
}

func TestRestoreCheck_ReplicaThenRepairKeepsDeviceID(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	id := kv.get(keyDeviceID)
	get := func(k string) string { return strings.TrimSpace(kv.get(k)) }
	if repairCopiedIdentity(context.Background(), kv, get) {
		t.Fatal("repairCopiedIdentity must not replace the freshly minted device id")
	}
	if kv.get(keyDeviceID) != id {
		t.Fatal("device id changed")
	}
}

func TestCloudIDKeyMatchesData(t *testing.T) {
	if keyTillCloudID != data.TillIdentityCloudIDSettingsKey {
		t.Fatalf("keyTillCloudID %q != data key %q", keyTillCloudID, data.TillIdentityCloudIDSettingsKey)
	}
}

// A reset that repeats (an earlier boot reset, then failed to re-mark) keeps
// the provisional receipt prefix it already gave, so the copy never starts
// a second receipt series (ut-docs#5162 review).
func TestRestoreCheck_RepeatedResetKeepsProvisionalPrefix(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	if err := kv.Set(context.Background(), keySyncReceiptPrefix, "Rbeef-"); err != nil {
		t.Fatal(err)
	}
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	if p := kv.get(keySyncReceiptPrefix); p != "Rbeef-" {
		t.Fatalf("sync.receipt_prefix = %q, want the earlier provisional Rbeef- kept", p)
	}
}

// ut-docs#5177: the restore instant is recorded on a replica copy (so a
// re-join can tell the copy's own entries from the original's), kept on a
// retried reset, and never written for a main till.
func TestRestoreCheck_ReplicaRecordsRestoredSince(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	before := time.Now().UTC().Add(-time.Second)
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	since, err := time.Parse(time.RFC3339, kv.get(keySyncRestoredSince))
	if err != nil || since.Before(before) {
		t.Fatalf("sync.restored_since = %q (err %v), want the restore instant", kv.get(keySyncRestoredSince), err)
	}
	prefix, gotSince := RestoredCopyMarks(context.Background(), kv)
	if !provisionalPrefix.MatchString(prefix) || gotSince != kv.get(keySyncRestoredSince) {
		t.Fatalf("RestoredCopyMarks = %q, %q", prefix, gotSince)
	}
}

func TestRestoreCheck_RepeatedResetKeepsRestoredSince(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	if err := kv.Set(context.Background(), keySyncRestoredSince, "2026-10-01T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	if got := kv.get(keySyncRestoredSince); got != "2026-10-01T08:00:00Z" {
		t.Fatalf("sync.restored_since = %q, want the earlier instant kept", got)
	}
}

func TestRestoreCheck_MainTillDoesNotRecordRestoredSince(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil) {
		t.Fatal("want reset")
	}
	if got := kv.get(keySyncRestoredSince); got != "" {
		t.Fatalf("a main till's restore wrote sync.restored_since = %q", got)
	}
	if p, s := RestoredCopyMarks(context.Background(), kv); p != "" || s != "" {
		t.Fatalf("RestoredCopyMarks on a main till = %q, %q, want empty", p, s)
	}
}

func TestRestoredCopyMarks_IgnoresARealPrefix(t *testing.T) {
	kv := newOrderedKV()
	_ = kv.fakeKV.Set(context.Background(), keySyncReceiptPrefix, "T2-")
	if p, s := RestoredCopyMarks(context.Background(), kv); p != "" || s != "" {
		t.Fatalf("RestoredCopyMarks for T2- = %q, %q, want empty", p, s)
	}
}

// ut-docs#5185: sync.restored_since is never earlier than just after the
// newest pre-restore no-sale open / cleared basket, so an original till whose
// clock ran ahead of the copy's is not mistaken for the copy.
func newestEntry(v string, err error) (NewestJournalEntry, *int) {
	calls := new(int)
	return func(context.Context) (string, error) { *calls++; return v, err }, calls
}

func TestRestoreCheck_RestoredSinceAfterFutureNewestEntry(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	newest := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	fn, _ := newestEntry(newest.Format(time.RFC3339), nil)
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, fn) {
		t.Fatal("want reset")
	}
	want := newest.Add(time.Second).Format(time.RFC3339)
	if got := kv.get(keySyncRestoredSince); got != want {
		t.Fatalf("sync.restored_since = %q, want %q", got, want)
	}
}

func TestRestoreCheck_RestoredSinceNowWhenNewestEntryIsPast(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	before := time.Now().UTC().Add(-time.Second)
	fn, _ := newestEntry(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), nil)
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, fn)
	since, err := time.Parse(time.RFC3339, kv.get(keySyncRestoredSince))
	if err != nil || since.Before(before) || since.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("sync.restored_since = %q (err %v), want about now", kv.get(keySyncRestoredSince), err)
	}
}

func TestRestoreCheck_RestoredSinceNowWhenLookupFails(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	before := time.Now().UTC().Add(-time.Second)
	for _, fn := range []NewestJournalEntry{
		func(context.Context) (string, error) { return "", errors.New("db gone") },
		func(context.Context) (string, error) { return "garbage", nil },
	} {
		kv = replicaKV(t)
		if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, fn) {
			t.Fatal("want reset despite the failed lookup")
		}
		since, err := time.Parse(time.RFC3339, kv.get(keySyncRestoredSince))
		if err != nil || since.Before(before) {
			t.Fatalf("sync.restored_since = %q (err %v), want about now", kv.get(keySyncRestoredSince), err)
		}
		if kv.get(keySyncBearer) != "" {
			t.Fatal("identity not reset")
		}
		restoreDir(t)
	}
}

func TestRestoreCheck_ExistingRestoredSinceKeptDespiteNewestEntry(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	_ = kv.Set(context.Background(), keySyncRestoredSince, "2026-10-01T08:00:00Z")
	fn, calls := newestEntry(time.Now().UTC().Add(time.Hour).Format(time.RFC3339), nil)
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, fn)
	if got := kv.get(keySyncRestoredSince); got != "2026-10-01T08:00:00Z" {
		t.Fatalf("sync.restored_since = %q, want the earlier instant kept", got)
	}
	if *calls != 0 {
		t.Fatalf("journal lookup called %d times with restored_since already set, want 0", *calls)
	}
}

func TestRestoreCheck_MainTillNeverLooksUpNewestEntry(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	fn, calls := newestEntry(time.Now().UTC().Add(time.Hour).Format(time.RFC3339), nil)
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, fn) {
		t.Fatal("want reset")
	}
	if *calls != 0 || kv.get(keySyncRestoredSince) != "" {
		t.Fatalf("main till: lookup calls %d, restored_since %q", *calls, kv.get(keySyncRestoredSince))
	}
}
