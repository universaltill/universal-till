package offsitebackup

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// replacement builds a second, fresh till of the same shop with its own
// identity, restoring from e's cloud.
func replacement(t *testing.T, e *tillEnv) (*tillEnv, *Restorer) {
	t.Helper()
	r := newTill(t)
	r.cloud = e.cloud
	for k, v := range map[string]string{"marketplace.token": "NEW-TOKEN", "marketplace.device_id": "dev-2", "store.name": "Fresh Till"} {
		if err := r.set.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	rs := &Restorer{
		DBPath: r.dbPath, Cloud: e.cloud.cloud(), PollEvery: time.Millisecond,
		Sleep:       func(context.Context, time.Duration) {},
		LiveStoreID: func() string { return "st_1" },
		Keys:        r.up.Keys, StoreID: func() string { return "st_1" },
	}
	return r, rs
}

func uploadedTill(t *testing.T) *tillEnv {
	t.Helper()
	e := newTill(t)
	if _, err := e.conn.Exec(`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s1', 'R-1', 'completed', 250, 250, '2026-10-20 17:45:00')`); err != nil {
		t.Fatal(err)
	}
	e.snapshot()
	e.round(false)
	if !e.onlyBackup().complete {
		t.Fatal("setup: no backup uploaded")
	}
	return e
}

func TestRestoreEndToEndKeepsTheNewTillsIdentity(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)

	var audits []string
	var sawBefore bool
	rs.BeforeStage = func(context.Context) error { sawBefore = true; return nil }
	if err := rs.Start(b.id, b.begin.TakenAt, func(a string, _ map[string]any) { audits = append(audits, a) }); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	st := rs.State()
	if st.Phase != PhaseReady || st.Staged == "" {
		t.Fatalf("state = %+v", st)
	}
	if st.Code != "K7QX29PM" || st.ApproveURL == "" {
		t.Errorf("code/url not surfaced: %+v", st)
	}
	if !sawBefore {
		t.Error("the safety backup hook did not run before staging")
	}
	if len(audits) != 2 || audits[0] != "cloud_restore_requested" || audits[1] != "cloud_restore_staged" {
		t.Errorf("audits = %v", audits)
	}
	if got := e.cloud.requests[firstKey(e.cloud.requests)].status; got != "result:staged:" {
		t.Errorf("cloud was told %q", got)
	}
	if !db.PendingRestore(r.dbPath) {
		t.Fatal("restore not staged")
	}
	if _, err := os.Stat(notePath(r.dbPath)); err != nil {
		t.Errorf("restore note missing: %v", err)
	}
	// No temp ciphertext is left; the key was never stored anywhere.
	es, _ := os.ReadDir(r.dir)
	for _, f := range es {
		if strings.HasSuffix(f.Name(), ".part") || strings.HasSuffix(f.Name(), ".utbk") {
			t.Errorf("temp file left: %s", f.Name())
		}
	}
	// Restart: apply, then look.
	r.conn.Close()
	applied, err := db.ApplyPendingRestore(r.dbPath)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	d, err := db.Open(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	set := data.NewSettingsRepo(d.DB)
	get := func(k string) string { v, _, _ := set.Get(context.Background(), k); return v }
	if get("store.name") != "Test Bakery" {
		t.Errorf("data not restored: %q", get("store.name"))
	}
	if get("marketplace.token") != "NEW-TOKEN" || get("marketplace.device_id") != "dev-2" {
		t.Errorf("the replacement lost its own identity: %q %q", get("marketplace.token"), get("marketplace.device_id"))
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil || n != 1 {
		t.Errorf("sales = %d %v", n, err)
	}
	if get(data.OffsiteLastSuccessAtKey) != "" {
		t.Error("the off-site state came back with the restore")
	}
}

func firstKey[V any](m map[string]V) string {
	for k := range m {
		return k
	}
	return ""
}

func TestRestoreRefusesOtherShop(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	rs.LiveStoreID = func() string { return "st_other" }
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	rs.Wait()
	if st := rs.State(); st.Phase != PhaseFailed || st.ErrCode != ErrCodeOtherShop {
		t.Fatalf("state = %+v", st)
	}
	if db.PendingRestore(r.dbPath) {
		t.Fatal("staged a restore that was refused")
	}
	assertNoImportedCopy(t, r)
	if got := e.cloud.requests[firstKey(e.cloud.requests)].status; got != "result:failed:other_shop" {
		t.Errorf("result = %q", got)
	}
}

func assertNoImportedCopy(t *testing.T, r *tillEnv) {
	t.Helper()
	es, _ := os.ReadDir(r.dir)
	for _, f := range es {
		if strings.Contains(f.Name(), "-file") || strings.HasPrefix(f.Name(), "restore-") {
			t.Errorf("left behind: %s", f.Name())
		}
	}
}

func TestRestoreCorruptPartIsRetriedThenFails(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	// Flip a byte in the stored part: the SHA-256 the cloud lists is of the
	// tampered bytes, so the part passes its hash but fails to decrypt.
	for n, p := range b.parts {
		b.parts[n] = append([]byte(nil), p...)
		b.parts[n][len(p)-5] ^= 1
	}
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	rs.Wait()
	if st := rs.State(); st.Phase != PhaseFailed || st.ErrCode != ErrCodeCorrupt {
		t.Fatalf("state = %+v", st)
	}
	if db.PendingRestore(r.dbPath) {
		t.Fatal("staged a corrupt restore")
	}
	assertNoImportedCopy(t, r)
}

// A part whose bytes do not match its listed SHA-256 is retried 3 times.
func TestRestoreHashMismatchRetriesThreeTimes(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	_, rs := replacement(t, e)
	rs.Cloud = &hashLiar{Cloud: rs.Cloud}
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	rs.Wait()
	if st := rs.State(); st.Phase != PhaseFailed || st.ErrCode != ErrCodeDownload {
		t.Fatalf("state = %+v", st)
	}
	if e.cloud.partDownloads != 3 {
		t.Fatalf("part fetched %d times, want 3", e.cloud.partDownloads)
	}
}

type hashLiar struct{ Cloud }

func (c *hashLiar) PollRestore(ctx context.Context, id string) (RestorePoll, error) {
	p, err := c.Cloud.PollRestore(ctx, id)
	for i := range p.Parts {
		p.Parts[i].SHA256 = strings.Repeat("0", 64)
	}
	return p, err
}

func TestRestoreDeniedAndExpired(t *testing.T) {
	for _, status := range []string{"denied", "expired"} {
		e := uploadedTill(t)
		b := e.onlyBackup()
		r, rs := replacement(t, e)
		rs.Cloud = &statusCloud{Cloud: rs.Cloud, status: status}
		_ = rs.Start(b.id, b.begin.TakenAt, nil)
		rs.Wait()
		if st := rs.State(); st.Phase != status {
			t.Errorf("%s: state = %+v", status, st)
		}
		if db.PendingRestore(r.dbPath) {
			t.Errorf("%s: staged", status)
		}
	}
}

type statusCloud struct {
	Cloud
	status string
}

func (c *statusCloud) PollRestore(context.Context, string) (RestorePoll, error) {
	return RestorePoll{Status: c.status}, nil
}

func TestRestoreCancelWhileWaiting(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	rs.Cloud = &statusCloud{Cloud: rs.Cloud, status: "pending"}
	rs.Sleep = nil
	rs.PollEvery = 5 * time.Millisecond
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	for i := 0; i < 200 && rs.State().Phase != PhaseWaiting; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !rs.Active() {
		t.Fatalf("not waiting: %+v", rs.State())
	}
	if err := rs.Start("x", "", nil); err == nil {
		t.Error("a second restore started while one waits")
	}
	rs.Cancel()
	rs.Wait()
	if rs.State().Phase != PhaseCancelled || db.PendingRestore(r.dbPath) {
		t.Fatalf("state = %+v", rs.State())
	}
}

func TestRestoreSafetyBackupFailureStagesNothing(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	rs.BeforeStage = func(context.Context) error { return errors.New("disk full") }
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	rs.Wait()
	if rs.State().Phase != PhaseFailed || db.PendingRestore(r.dbPath) {
		t.Fatalf("state = %+v", rs.State())
	}
	assertNoImportedCopy(t, r)
	_ = filepath.Join
}

// pollCloud lets a test rewrite the cloud's restore answer and counts part
// downloads.
type pollCloud struct {
	Cloud
	tweak func(*RestorePoll)
	err   error
	parts int
}

func (p *pollCloud) PollRestore(ctx context.Context, id string) (RestorePoll, error) {
	if p.err != nil {
		return RestorePoll{}, p.err
	}
	poll, err := p.Cloud.PollRestore(ctx, id)
	if err == nil && p.tweak != nil {
		p.tweak(&poll)
	}
	return poll, err
}

func (p *pollCloud) GetPart(ctx context.Context, b string, n int, r string) (io.ReadCloser, error) {
	p.parts++
	return p.Cloud.GetPart(ctx, b, n, r)
}

func runRestoreWith(t *testing.T, pc *pollCloud, rs *Restorer, b *fakeBackup) RestoreState {
	t.Helper()
	pc.Cloud = rs.Cloud
	rs.Cloud = pc
	if err := rs.Start(b.id, b.begin.TakenAt, nil); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	return rs.State()
}

func TestRestoreRefusesAnotherBackupThanTheApprovedOne(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	_, rs := replacement(t, e)
	pc := &pollCloud{tweak: func(p *RestorePoll) { p.BackupID = "11111111-2222-4333-8444-555555555555" }}
	st := runRestoreWith(t, pc, rs, b)
	if st.Phase != PhaseFailed || st.ErrCode != ErrCodeCorrupt || pc.parts != 0 {
		t.Fatalf("phase=%s err=%s parts downloaded=%d, want failed/corrupt/0", st.Phase, st.ErrCode, pc.parts)
	}
}

func TestRestoreCapsPartCountAndTotalBytesBeforeDownloading(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	// Total over MaxBytes+8 MiB: 3 parts of 8 MiB against a 4 MiB cap.
	_, rs := replacement(t, e)
	rs.MaxBytes = 4 << 20
	pc := &pollCloud{tweak: func(p *RestorePoll) {
		p.Parts = []PartInfo{{N: 1, Bytes: 8 << 20}, {N: 2, Bytes: 8 << 20}, {N: 3, Bytes: 8 << 20}}
	}}
	if st := runRestoreWith(t, pc, rs, b); st.ErrCode != ErrCodeTooLarge || pc.parts != 0 {
		t.Fatalf("total cap: phase=%s err=%s parts=%d, want too_large/0", st.Phase, st.ErrCode, pc.parts)
	}
	// A hostile 10 000-part list is refused on count alone.
	_, rs2 := replacement(t, e)
	pc2 := &pollCloud{tweak: func(p *RestorePoll) { p.Parts = make([]PartInfo, 10000) }}
	if st := runRestoreWith(t, pc2, rs2, b); st.ErrCode != ErrCodeTooLarge || pc2.parts != 0 {
		t.Fatalf("count cap: err=%s parts=%d, want too_large/0", st.ErrCode, pc2.parts)
	}
	// A part larger than 8 MiB is corrupt.
	_, rs3 := replacement(t, e)
	pc3 := &pollCloud{tweak: func(p *RestorePoll) { p.Parts = []PartInfo{{N: 1, Bytes: 9 << 20}} }}
	if st := runRestoreWith(t, pc3, rs3, b); st.ErrCode != ErrCodeCorrupt || pc3.parts != 0 {
		t.Fatalf("part size cap: err=%s parts=%d, want corrupt/0", st.ErrCode, pc3.parts)
	}
}

func TestRestoreChecksFreeDiskBeforeDownloading(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	_, rs := replacement(t, e)
	rs.FreeDisk = func(string) (uint64, error) { return 10, nil }
	pc := &pollCloud{}
	if st := runRestoreWith(t, pc, rs, b); st.ErrCode != ErrCodeDisk || pc.parts != 0 {
		t.Fatalf("err=%s parts=%d, want disk/0", st.ErrCode, pc.parts)
	}
}

func TestLinkRefusesAReleasedKeyWithTheWrongFingerprint(t *testing.T) {
	e := newTill(t)
	shop := bytes.Repeat([]byte{7}, KeySize)
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 1, Fingerprint(shop), shop
	forged := bytes.Repeat([]byte{8}, KeySize)
	pc := &pollCloud{Cloud: e.cloud.cloud(), tweak: func(p *RestorePoll) {
		if p.DEKB64 != "" {
			p.DEKB64 = base64.StdEncoding.EncodeToString(forged)
		}
	}}
	rs := &Restorer{DBPath: e.dbPath, Cloud: pc, PollEvery: time.Millisecond,
		Sleep: func(context.Context, time.Duration) {}, Keys: e.up.Keys, StoreID: func() string { return "st_1" }}
	_ = rs.StartLink(nil)
	rs.Wait()
	if st := rs.State(); st.Phase != PhaseFailed || st.ErrCode != ErrCodeCorrupt {
		t.Fatalf("phase=%s err=%s, want failed/corrupt", st.Phase, st.ErrCode)
	}
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("a forged released key was saved")
	}
}

func TestRestoreStopsWhenTheCloudStaysUnreachable(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	_, rs := replacement(t, e)
	rs.GiveUpAfter = 30 * time.Millisecond
	pc := &pollCloud{err: errors.New("network down")}
	if st := runRestoreWith(t, pc, rs, b); st.Phase != PhaseFailed || st.ErrCode != ErrCodeOther {
		t.Fatalf("phase=%s err=%s, want failed/other", st.Phase, st.ErrCode)
	}
}

func TestWaitTimeoutReturnsFalseWhileTheRestoreRuns(t *testing.T) {
	rs := &Restorer{done: make(chan struct{})}
	if rs.WaitTimeout(20 * time.Millisecond) {
		t.Fatal("WaitTimeout reported an unfinished restore as ended")
	}
	close(rs.done)
	if !rs.WaitTimeout(time.Second) {
		t.Fatal("WaitTimeout missed a finished restore")
	}
}
