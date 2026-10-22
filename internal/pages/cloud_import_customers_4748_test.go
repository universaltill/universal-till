package pages

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/hpke"
)

// The sealed import_customers apply (ADR-0168 §5, ut-docs#4748). The
// customers below are invented.

func sealCustomers(t *testing.T, keys *directivekey.Store, aad string, plaintext any) string {
	t.Helper()
	pt, err := json.Marshal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	k, err := keys.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc, ct, err := hpke.SealDeterministic(k.PublicKey(), eph, []byte(directivekey.CustomersInfo), []byte(aad), pt)
	if err != nil {
		t.Fatal(err)
	}
	return "v1." + directivekey.KID(k.PublicKey().Bytes()) + "." + base64.RawURLEncoding.EncodeToString(enc) + "." + base64.RawURLEncoding.EncodeToString(ct)
}

func TestCloudImportCustomers_OpensAppliesAndReportsCountsOnly(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	keys := directivekey.New()
	store := enroll.Effective(dp.Cfg).Marketplace.StoreID
	aad := directivekey.CustomersAAD(store, "dir-1", "import_customers", "job-1", 2)
	sealed := sealCustomers(t, keys, aad, map[string]any{
		"source": "file:test-shop",
		"rows": []map[string]any{
			{"row": 2, "name": "Ada Quill", "email": "ada@example.test"},
			{"row": 3, "name": "Bo Marsh", "phone": "07700 900123"},
		},
	})
	out, err := cloudImportCustomers(t.Context(), dp, keys, cloudsync.ImportCustomersDirective{
		DirectiveID: "dir-1", JobID: "job-1", ChunkIndex: 2, Sealed: sealed,
	})
	if err != nil {
		t.Fatalf("cloudImportCustomers: %v", err)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM customers WHERE name IN ('Ada Quill','Bo Marsh')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("customers = %d %v", n, err)
	}
	if !strings.Contains(out.Message, "2 created") {
		t.Fatalf("message = %q", out.Message)
	}
	b, _ := json.Marshal(out.Data)
	for _, secret := range []string{"Ada Quill", "ada@example.test", "Bo Marsh", "07700 900123"} {
		if strings.Contains(out.Message+string(b), secret) {
			t.Fatalf("result carries customer data %q", secret)
		}
	}
	rows, _ := out.Data["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("result rows = %v", out.Data["rows"])
	}
}

func TestCloudImportCustomers_RefusesWhatDoesNotOpen(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	keys := directivekey.New()
	store := enroll.Effective(dp.Cfg).Marketplace.StoreID
	aad := directivekey.CustomersAAD(store, "dir-1", "import_customers", "job-1", 0)
	sealed := sealCustomers(t, keys, aad, map[string]any{"source": "s", "rows": []map[string]any{{"row": 1, "name": "Cy Alder"}}})
	for name, dir := range map[string]cloudsync.ImportCustomersDirective{
		"other chunk":     {DirectiveID: "dir-1", JobID: "job-1", ChunkIndex: 1, Sealed: sealed},
		"other directive": {DirectiveID: "dir-9", JobID: "job-1", ChunkIndex: 0, Sealed: sealed},
		"other job":       {DirectiveID: "dir-1", JobID: "job-2", ChunkIndex: 0, Sealed: sealed},
		"malformed":       {DirectiveID: "dir-1", JobID: "job-1", ChunkIndex: 0, Sealed: "nope"},
		"empty":           {DirectiveID: "dir-1", JobID: "job-1", ChunkIndex: 0},
	} {
		if _, err := cloudImportCustomers(t.Context(), dp, keys, dir); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM customers WHERE name = 'Cy Alder'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused chunk wrote %d customers (%v)", n, err)
	}
}

func TestCloudImportCustomers_SatelliteRefuses(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	setReplica(t, dp)
	keys := directivekey.New()
	store := enroll.Effective(dp.Cfg).Marketplace.StoreID
	aad := directivekey.CustomersAAD(store, "dir-1", "import_customers", "job-1", 0)
	sealed := sealCustomers(t, keys, aad, map[string]any{"source": "s", "rows": []map[string]any{{"row": 1, "name": "Di Fenn"}}})
	_, err := cloudImportCustomers(t.Context(), dp, keys, cloudsync.ImportCustomersDirective{DirectiveID: "dir-1", JobID: "job-1", Sealed: sealed})
	if err == nil || !strings.HasPrefix(err.Error(), "this data is primary-wins synced") {
		t.Fatalf("satellite: %v", err)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM customers WHERE name = 'Di Fenn'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a satellite wrote customers: %d %v", n, err)
	}
}

func TestCloudImportCustomers_TooLargeOrBadPayload(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	keys := directivekey.New()
	store := enroll.Effective(dp.Cfg).Marketplace.StoreID
	aad := directivekey.CustomersAAD(store, "dir-1", "import_customers", "job-1", 0)
	big := sealCustomers(t, keys, aad, map[string]any{"source": "s", "rows": []map[string]any{{"row": 1, "name": strings.Repeat("x", 70*1024)}}})
	if _, err := cloudImportCustomers(t.Context(), dp, keys, cloudsync.ImportCustomersDirective{DirectiveID: "dir-1", JobID: "job-1", Sealed: big}); err == nil {
		t.Fatal("a chunk over 64 KiB must be refused")
	}
	notJSON := sealCustomers(t, keys, aad, "not an object")
	if _, err := cloudImportCustomers(t.Context(), dp, keys, cloudsync.ImportCustomersDirective{DirectiveID: "dir-1", JobID: "job-1", Sealed: notJSON}); err == nil {
		t.Fatal("a payload that is not the request shape must be refused")
	}
}
