package offsitebackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// TestCloudRoundTripBridge is the till half of the real cross-repo round trip
// (ut-docs#5066). It runs only with UT_5066_BRIDGE=<dir> and needs the cloud
// half running: in ut-cloud,
//
//	UT_5066_BRIDGE=<dir> go test -tags bridge5066 ./internal/httpapi/handlers -run TestTillBackup_RoundTripBridge
//
// That serves the real handlers and writes <dir>/conn.json. Here a real till
// DB is snapshotted, identity-stripped, encrypted and uploaded over HTTP; a
// second, empty till requests a restore, the "owner" approves through the
// cloud's real approve service, and the till downloads, decrypts, runs
// ImportBackupFile and stages the restore. The restarted DB must equal the
// original, and done.json hands the cloud half the key and the plaintext
// markers to prove they never reached its DB, blobs or logs.
func TestCloudRoundTripBridge(t *testing.T) {
	dir := os.Getenv("UT_5066_BRIDGE")
	if dir == "" {
		t.Skip("UT_5066_BRIDGE not set")
	}
	var cn struct{ Base, Root, StoreID, Token string }
	var raw []byte
	for i := 0; i < 150; i++ {
		var err error
		if raw, err = os.ReadFile(filepath.Join(dir, "conn.json")); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("no conn.json: %v", err)
	}
	cn.Base, cn.Root, cn.StoreID, cn.Token = m["base"], m["root"], m["store_id"], m["token"]
	ctx := context.Background()
	cloud := &HTTPCloud{Resolve: func() Conn { return Conn{BaseURL: cn.Base, StoreID: cn.StoreID, Token: cn.Token} }, HTTP: &http.Client{Timeout: time.Minute}}

	// The original till, with data worth getting back.
	e := newTill(t)
	for k, v := range map[string]string{"marketplace.store_id": cn.StoreID, "store.name": "ZZ-PLAINTEXT-BAKERY-5066"} {
		if err := e.set.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	big := make([]byte, 3<<20)
	_, _ = rand.Read(big)
	if err := e.set.Set(ctx, "test.blob", hex.EncodeToString(big)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s1', 'ZZ-RECEIPT-5066-1', 'completed', 250, 250, '2026-10-20 17:45:00')`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s2', 'ZZ-RECEIPT-5066-2', 'completed', 1999, 1999, '2026-10-21 09:00:00')`,
	} {
		if _, err := e.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.up.Cloud = logCloud{cloud, t}
	e.up.StoreID = func() string { return cn.StoreID }
	e.snapshot()
	if err := e.up.Round(ctx, true); err != nil {
		t.Fatalf("upload round: %v", err)
	}
	if e.get(data.OffsiteLastSuccessAtKey) == "" {
		t.Fatalf("upload did not record success: error %q refused %q", e.get(data.OffsiteLastErrorKey), e.get(data.OffsiteRefusedReasonKey))
	}
	dek, _, err := e.up.Keys.Load(cn.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	e.noTempFiles()

	list, err := cloud.List(ctx)
	if err != nil || len(list.Backups) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	b := list.Backups[0]

	// The replacement till: empty, its own identity, no key.
	r := newTill(t)
	for k, v := range map[string]string{"marketplace.store_id": cn.StoreID, "marketplace.token": "NEW-TOKEN", "marketplace.device_id": "dev-2", "store.name": "Fresh Till"} {
		if err := r.set.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	rs := &Restorer{DBPath: r.dbPath, Cloud: cloud, PollEvery: 50 * time.Millisecond,
		LiveStoreID: func() string { return cn.StoreID }, StoreID: func() string { return cn.StoreID },
		Keys: KeyStore{Dir: filepath.Join(t.TempDir(), "keys2")}}
	if err := rs.Start(b.BackupID, b.TakenAt, func(string, map[string]any) {}); err != nil {
		t.Fatal(err)
	}
	// The owner approves the code shown on the till, through the cloud's owner path.
	approved := false
	for i := 0; i < 200 && !approved; i++ {
		if code := rs.State().Code; code != "" {
			resp, err := http.Get(cn.Root + "/_test/approve?code=" + url.QueryEscape(code))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("approve = %d", resp.StatusCode)
			}
			approved = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !approved {
		t.Fatal("the till never showed a code")
	}
	rs.Wait()
	if st := rs.State(); st.Phase != PhaseReady || st.Staged == "" {
		t.Fatalf("restore state = %+v", st)
	}
	r.conn.Close()
	if ok, err := db.ApplyPendingRestore(r.dbPath); err != nil || !ok {
		t.Fatalf("apply: %v %v", ok, err)
	}
	d, err := db.Open(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	set := data.NewSettingsRepo(d.DB)
	get := func(k string) string { v, _, _ := set.Get(ctx, k); return v }
	if get("store.name") != "ZZ-PLAINTEXT-BAKERY-5066" {
		t.Errorf("store.name = %q", get("store.name"))
	}
	if get("test.blob") != hex.EncodeToString(big) {
		t.Error("3 MiB setting did not come back byte for byte")
	}
	if get("marketplace.token") != "NEW-TOKEN" || get("marketplace.device_id") != "dev-2" {
		t.Error("the replacement lost its own identity")
	}
	rows, err := d.Query(`SELECT id, receipt_no, status, subtotal, total, created_at FROM sales ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var a, b2, c, f string
		var s1, s2 int
		if err := rows.Scan(&a, &b2, &c, &s1, &s2, &f); err != nil {
			t.Fatal(err)
		}
		got = append(got, a+"|"+b2+"|"+c+"|"+f)
	}
	rows.Close()
	want := []string{"s1|ZZ-RECEIPT-5066-1|completed|2026-10-20 17:45:00", "s2|ZZ-RECEIPT-5066-2|completed|2026-10-21 09:00:00"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("sales = %v, want %v", got, want)
	}
	if hex.EncodeToString(dek) == "" {
		t.Fatal("no key")
	}
	out, _ := json.Marshal(map[string]any{"dek_hex": hex.EncodeToString(dek), "plaintext_markers": []string{"ZZ-PLAINTEXT-BAKERY-5066", "ZZ-RECEIPT-5066"}})
	if err := os.WriteFile(filepath.Join(dir, "done.json"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // let the cloud half scan before this test's temp files go
}

// logCloud reports the real cloud's answer to each upload call, so a failed
// round says why (the uploader keeps only a reason class).
type logCloud struct {
	Cloud
	t *testing.T
}

func (l logCloud) Begin(ctx context.Context, r BeginRequest) (BeginResponse, error) {
	o, err := l.Cloud.Begin(ctx, r)
	if err != nil {
		l.t.Logf("begin: %v", err)
	}
	return o, err
}

func (l logCloud) PutPart(ctx context.Context, id string, n int, b []byte, sha string) error {
	err := l.Cloud.PutPart(ctx, id, n, b, sha)
	if err != nil {
		l.t.Logf("part %d: %v", n, err)
	}
	return err
}

func (l logCloud) Complete(ctx context.Context, id string, r CompleteRequest) error {
	err := l.Cloud.Complete(ctx, id, r)
	if err != nil {
		l.t.Logf("complete: %v", err)
	}
	return err
}

func (l logCloud) GetKey(ctx context.Context) (KeyState, error) {
	o, err := l.Cloud.GetKey(ctx)
	if err != nil {
		l.t.Logf("getkey: %v", err)
	}
	return o, err
}

func (l logCloud) PutKey(ctx context.Context, k KeyPut) error {
	err := l.Cloud.PutKey(ctx, k)
	if err != nil {
		l.t.Logf("putkey: %v", err)
	}
	return err
}
