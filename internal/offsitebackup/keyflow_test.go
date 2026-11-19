package offsitebackup

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/paths"
)

func TestFirstUploadGeneratesAndEscrowsTheKeyBeforeBegin(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("setup: a key already exists")
	}
	e.round(false)
	dek, gen, err := e.up.Keys.Load("st_1")
	if err != nil || gen != 1 {
		t.Fatalf("no local key after the first upload: %v", err)
	}
	if e.cloud.keyGen != 1 || e.cloud.keyFP != Fingerprint(dek) || !bytes.Equal(e.cloud.keyDEK, dek) {
		t.Fatal("the cloud does not hold the till's key")
	}
	b := e.onlyBackup()
	if !b.complete || b.begin.DEKGeneration != 1 || b.begin.KeyFingerprint != Fingerprint(dek) {
		t.Fatalf("begin body: %+v", b.begin)
	}
	// The key is not in the live database or any snapshot or backup file.
	enc := base64.StdEncoding.EncodeToString(dek)
	all, _ := e.set.All(context.Background())
	for k, v := range all {
		if strings.Contains(v, enc) {
			t.Errorf("key in settings %s", k)
		}
	}
	es, _ := os.ReadDir(e.dir)
	for _, f := range es {
		raw, _ := os.ReadFile(filepath.Join(e.dir, f.Name()))
		if bytes.Contains(raw, dek) || bytes.Contains(raw, []byte(enc)) {
			t.Errorf("key material inside %s", f.Name())
		}
	}
	// A second backup reuses the key and escrows nothing more.
	puts := e.cloud.putKeys
	e.now = e.now.Add(25 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(false)
	if e.cloud.begins != 2 || e.cloud.putKeys != puts {
		t.Fatalf("begins=%d putKeys=%d (was %d)", e.cloud.begins, e.cloud.putKeys, puts)
	}
}

func TestKeyNotEscrowedMeansNoBeginThenRetries(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	// A till that already holds a key the cloud has not seen (a failed PUT).
	if _, _, err := e.up.Keys.Generate("st_1"); err != nil {
		t.Fatal(err)
	}
	e.cloud.keyGen = 0
	real := e.up.Cloud
	e.up.Cloud = &putFailCloud{Cloud: real, fails: 1}
	e.round(false)
	if e.cloud.begins != 0 {
		t.Fatal("begin was called before the key was escrowed")
	}
	if e.get(data.OffsiteFailingSinceKey) == "" {
		t.Fatal("failure not recorded")
	}
	e.now = e.now.Add(time.Hour)
	e.round(false)
	if e.cloud.begins != 1 || e.cloud.keyGen != 1 {
		t.Fatalf("begins=%d keyGen=%d", e.cloud.begins, e.cloud.keyGen)
	}
	dek, _, _ := e.up.Keys.Load("st_1")
	if !bytes.Equal(dek, e.cloud.keyDEK) {
		t.Error("the escrowed key is not the till's key")
	}
}

type putFailCloud struct {
	Cloud
	fails int
}

func (p *putFailCloud) PutKey(ctx context.Context, k KeyPut) error {
	if p.fails > 0 {
		p.fails--
		return &APIError{Status: 503, Code: "unavailable"}
	}
	return p.Cloud.PutKey(ctx, k)
}

func TestCloudHasKeyTillDoesNotNeedsLinking(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	other := make([]byte, KeySize)
	other[0] = 9
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 1, Fingerprint(other), other
	e.round(false)
	if e.cloud.begins != 0 || e.cloud.putKeys != 0 {
		t.Fatalf("begins=%d putKeys=%d: a till without the key must not begin or overwrite", e.cloud.begins, e.cloud.putKeys)
	}
	if e.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink {
		t.Fatalf("refused reason = %q", e.get(data.OffsiteRefusedReasonKey))
	}
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("a key was generated although the cloud has one")
	}
}

func TestKeyMismatchNeedsLinking(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	if _, _, err := e.up.Keys.Generate("st_1"); err != nil {
		t.Fatal(err)
	}
	other := make([]byte, KeySize)
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 1, Fingerprint(other), other
	e.round(false)
	if e.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink || e.cloud.begins != 0 {
		t.Fatalf("reason=%q begins=%d", e.get(data.OffsiteRefusedReasonKey), e.cloud.begins)
	}
	// And a begin the cloud itself answers 409 key_mismatch maps the same.
	e2 := newTill(t)
	e2.snapshot()
	e2.cloud.beginStatus, e2.cloud.beginCode = 409, "key_mismatch"
	e2.round(false)
	if e2.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink {
		t.Fatalf("begin 409 key_mismatch -> %q", e2.get(data.OffsiteRefusedReasonKey))
	}
}

func TestLinkFlowSavesTheKeyAndBackupsResume(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	shop := make([]byte, KeySize)
	shop[5] = 7
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 1, Fingerprint(shop), shop
	e.round(false)
	if e.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink {
		t.Fatal("setup: not in needs-link")
	}
	linked := false
	rs := &Restorer{DBPath: e.dbPath, Cloud: e.cloud.cloud(), PollEvery: time.Millisecond,
		Sleep: func(context.Context, time.Duration) {}, Keys: e.up.Keys, StoreID: func() string { return "st_1" },
		OnLinked: func() {
			linked = true
			_ = e.set.Delete(context.Background(), data.OffsiteRefusedReasonKey)
			_ = e.set.Delete(context.Background(), data.OffsiteNextAttemptAtKey)
		}}
	var audits []string
	if err := rs.StartLink(func(a string, _ map[string]any) { audits = append(audits, a) }); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	st := rs.State()
	if st.Phase != PhaseLinked || st.Kind != KindLink || st.Code != "LINK2X9P" {
		t.Fatalf("state = %+v", st)
	}
	if !linked || len(audits) != 2 || audits[1] != "cloud_key_linked" {
		t.Fatalf("linked=%v audits=%v", linked, audits)
	}
	dek, gen, err := e.up.Keys.Load("st_1")
	if err != nil || gen != 1 || !bytes.Equal(dek, shop) {
		t.Fatalf("key not saved: %v", err)
	}
	e.round(false)
	if !e.onlyBackup().complete {
		t.Fatal("backups did not resume after linking")
	}
	if _, err := e.cloud.decrypt(e.onlyBackup().id); err != nil {
		t.Fatalf("the cloud's key cannot open the backup: %v", err)
	}
}

func TestLinkDeniedSavesNothing(t *testing.T) {
	e := newTill(t)
	shop := make([]byte, KeySize)
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 1, Fingerprint(shop), shop
	rs := &Restorer{DBPath: e.dbPath, Cloud: &denyCloud{Cloud: e.cloud.cloud()}, PollEvery: time.Millisecond,
		Sleep: func(context.Context, time.Duration) {}, Keys: e.up.Keys, StoreID: func() string { return "st_1" }}
	_ = rs.StartLink(nil)
	rs.Wait()
	if rs.State().Phase != PhaseDenied {
		t.Fatalf("phase %s", rs.State().Phase)
	}
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("a key was saved after a denial")
	}
}

type denyCloud struct{ Cloud }

func (d *denyCloud) PollRestore(context.Context, string) (RestorePoll, error) {
	return RestorePoll{Status: "denied"}, nil
}

func TestRestoreOnTillWithKeySendsHaveKeyAndGetsNoKey(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	dek, gen, _ := e.up.Keys.Load("st_1")
	if err := r.up.Keys.Save("st_1", gen, dek); err != nil {
		t.Fatal(err)
	}
	if err := rs.Start(b.id, b.begin.TakenAt, nil); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	if rs.State().Phase != PhaseReady {
		t.Fatalf("state %+v", rs.State())
	}
	if e.cloud.lastHave == nil || e.cloud.lastHave.Fingerprint != Fingerprint(dek) || e.cloud.lastHave.Generation != gen {
		t.Fatalf("have_key = %+v", e.cloud.lastHave)
	}
	if e.cloud.keyReleases != 0 {
		t.Fatal("the cloud released a key the till already had")
	}
	if !db.PendingRestore(r.dbPath) {
		t.Fatal("not staged")
	}
}

func TestRestoreOnTillWithoutKeyReleasesAndSavesIt(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	if err := rs.Start(b.id, b.begin.TakenAt, nil); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	if rs.State().Phase != PhaseReady {
		t.Fatalf("state %+v", rs.State())
	}
	if e.cloud.lastHave != nil || e.cloud.keyReleases != 1 {
		t.Fatalf("have=%+v releases=%d", e.cloud.lastHave, e.cloud.keyReleases)
	}
	dek, gen, err := r.up.Keys.Load("st_1")
	if err != nil || gen != 1 || !bytes.Equal(dek, e.cloud.keyDEK) {
		t.Fatalf("the restored till did not keep the released key: %v", err)
	}
}

func TestFailedRestoreDoesNotSaveTheReleasedKey(t *testing.T) {
	e := uploadedTill(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	rs.LiveStoreID = func() string { return "st_other" } // import refuses another shop
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	rs.Wait()
	if rs.State().Phase != PhaseFailed {
		t.Fatalf("state %+v", rs.State())
	}
	if _, _, err := r.up.Keys.Load("st_1"); err == nil {
		t.Fatal("key saved after a failed restore")
	}
}

func TestLeavingTheShopDeletesTheKeyFile(t *testing.T) {
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	if _, _, err := (KeyStore{}).Generate("st_1"); err != nil {
		t.Fatal(err)
	}
	if want := paths.Data("keys", "offsite-backup.key"); (KeyStore{}).path() != want {
		t.Fatalf("path %s, want %s", (KeyStore{}).path(), want)
	}
	if enroll.OnStoreLeft == nil {
		t.Fatal("the pairing reset has no hook")
	}
	enroll.OnStoreLeft("st_other") // a hook for another shop keeps this shop's key
	if _, _, err := (KeyStore{}).Load("st_1"); err != nil {
		t.Fatalf("a delete for another shop removed the key: %v", err)
	}
	enroll.OnStoreLeft("st_1")
	if _, err := os.Stat((KeyStore{}).path()); !os.IsNotExist(err) {
		t.Fatal("key survived leaving the shop")
	}
}

func newTillChoice(t *testing.T, choice string) *tillEnv {
	e := newTill(t)
	if choice == "" {
		_ = e.set.Delete(context.Background(), data.OffsiteKeyInCloudKey)
	} else {
		_ = e.set.Set(context.Background(), data.OffsiteKeyInCloudKey, choice)
	}
	return e
}

func TestUnaskedChoiceRegistersFingerprintOnlyAndUploadsAnyway(t *testing.T) {
	e := newTillChoice(t, "")
	e.snapshot()
	e.round(false)
	b := e.onlyBackup()
	if !b.complete {
		t.Fatal("upload did not run with the choice unasked")
	}
	dek, _, _ := e.up.Keys.Load("st_1")
	if e.cloud.keyGen != 1 || e.cloud.keyFP != Fingerprint(dek) || e.cloud.keyDEK != nil {
		t.Fatalf("cloud: gen=%d dek-held=%v (must hold the fingerprint only)", e.cloud.keyGen, e.cloud.keyDEK != nil)
	}
	if _, err := e.cloud.decryptWith(b.id, dek); err != nil {
		t.Fatalf("the till's own key cannot open its backup: %v", err)
	}
}

func TestKeyChoiceOnThenOffNeverDeletesTheCloudCopy(t *testing.T) {
	e := newTillChoice(t, data.OffsiteKeyInCloudOff)
	e.snapshot()
	e.round(false)
	if e.cloud.keyDEK != nil {
		t.Fatal("off: the cloud got the key")
	}
	ctx := context.Background()
	_ = e.set.Set(ctx, data.OffsiteKeyInCloudKey, data.OffsiteKeyInCloudOn)
	if err := e.up.ApplyKeyChoice(ctx); err != nil {
		t.Fatal(err)
	}
	dek, _, _ := e.up.Keys.Load("st_1")
	if !bytes.Equal(e.cloud.keyDEK, dek) {
		t.Fatal("on: the cloud does not hold the key")
	}
	_ = e.set.Set(ctx, data.OffsiteKeyInCloudKey, data.OffsiteKeyInCloudOff)
	if err := e.up.ApplyKeyChoice(ctx); err != nil {
		t.Fatal(err)
	}
	// M3: a till token must never drop the cloud copy; only the owner on my. can.
	if !bytes.Equal(e.cloud.keyDEK, dek) || e.cloud.deletes != 0 {
		t.Fatalf("off: the till touched the cloud copy (deletes=%d, copy kept=%v)", e.cloud.deletes, e.cloud.keyDEK != nil)
	}
	// A later round (new snapshot) changes nothing and the backups still work.
	e.now = e.now.Add(25 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(false)
	if e.cloud.begins != 2 || e.cloud.deletes != 0 {
		t.Fatalf("begins=%d deletes=%d", e.cloud.begins, e.cloud.deletes)
	}
}

func TestEscrowBlockedByOwnerIsNotSentAndIsRemembered(t *testing.T) {
	e := newTillChoice(t, data.OffsiteKeyInCloudOn)
	e.snapshot()
	e.cloud.escrowBlocked = true
	e.round(false)
	if !e.onlyBackup().complete || e.cloud.keyDEK != nil || e.cloud.keyGen != 1 {
		t.Fatal("a blocked shop must still upload, with the fingerprint registered and no key sent")
	}
	if e.get(data.OffsiteEscrowBlockedKey) != "1" {
		t.Fatal("blocked state not remembered for the card")
	}
	if err := e.up.ApplyKeyChoice(context.Background()); err != nil || e.cloud.keyDEK != nil {
		t.Fatalf("apply while blocked: %v", err)
	}
	// The owner allows it again: the next round sends the copy.
	e.cloud.escrowBlocked = false
	e.now = e.now.Add(25 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(false)
	if e.cloud.keyDEK == nil || e.get(data.OffsiteEscrowBlockedKey) != "" {
		t.Fatal("copy not sent after the owner allowed it")
	}
}

func offKeyShop(t *testing.T) (*tillEnv, []byte) {
	e := newTillChoice(t, "")
	if _, err := e.conn.Exec(`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s1', 'R-1', 'completed', 250, 250, '2026-10-20 17:45:00')`); err != nil {
		t.Fatal(err)
	}
	e.snapshot()
	e.round(false)
	if !e.onlyBackup().complete || e.cloud.keyDEK != nil {
		t.Fatal("setup")
	}
	dek, _, _ := e.up.Keys.Load("st_1")
	return e, dek
}

func waitPhase(t *testing.T, rs *Restorer, phase string) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if rs.State().Phase == phase {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never reached %s, at %s", phase, rs.State().Phase)
}

func TestRestoreWithNoCloudCopyAsksForTheSavedKey(t *testing.T) {
	e, dek := offKeyShop(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	if err := rs.Start(b.id, b.begin.TakenAt, nil); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, rs, PhaseAskSaved)
	if !rs.Active() {
		t.Error("a restore waiting for the key must count as active")
	}
	// Damaged text: the checksum catches it, nothing proceeds.
	saved := EncodeRecovery(1, dek)
	damaged := []byte(saved)
	damaged[len(damaged)-2] ^= 1
	if err := rs.SubmitKey(string(damaged)); !errors.Is(err, ErrBadRecoveryKey) {
		t.Fatalf("damaged key: %v", err)
	}
	// A valid key of another shop: wrong fingerprint.
	other := bytes.Repeat([]byte{3}, KeySize)
	if err := rs.SubmitKey(EncodeRecovery(1, other)); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("wrong-shop key: %v", err)
	}
	if rs.State().Phase != PhaseAskSaved {
		t.Fatal("a bad key moved the restore on")
	}
	// The right key, typed in lower case with spaces.
	if err := rs.SubmitKey(strings.ToLower(strings.ReplaceAll(saved, "-", " "))); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	if rs.State().Phase != PhaseReady {
		t.Fatalf("state %+v", rs.State())
	}
	got, gen, err := r.up.Keys.Load("st_1")
	if err != nil || gen != 1 || !bytes.Equal(got, dek) {
		t.Fatalf("typed key not kept by the restored till: %v", err)
	}
	if !db.PendingRestore(r.dbPath) {
		t.Fatal("not staged")
	}
}

func TestSubmitKeyWhenNothingAsksIsRefused(t *testing.T) {
	rs := &Restorer{}
	if err := rs.SubmitKey(EncodeRecovery(1, make([]byte, KeySize))); !errors.Is(err, ErrNoKeyWanted) {
		t.Fatalf("%v", err)
	}
}

func TestRestoreNeedKeyCancelStopsCleanly(t *testing.T) {
	e, _ := offKeyShop(t)
	b := e.onlyBackup()
	r, rs := replacement(t, e)
	_ = rs.Start(b.id, b.begin.TakenAt, nil)
	waitPhase(t, rs, PhaseAskSaved)
	rs.Cancel()
	rs.Wait()
	if rs.State().Phase != PhaseCancelled {
		t.Fatalf("phase %s", rs.State().Phase)
	}
	if _, _, err := r.up.Keys.Load("st_1"); err == nil {
		t.Fatal("a key was saved")
	}
}

func TestLinkWithNoCloudCopyUsesTheSavedKey(t *testing.T) {
	e, dek := offKeyShop(t)
	e.up.Keys.Delete() // a new main till with no key
	linked := false
	rs := &Restorer{DBPath: e.dbPath, Cloud: e.cloud.cloud(), PollEvery: time.Millisecond,
		Sleep: func(context.Context, time.Duration) {}, Keys: e.up.Keys, StoreID: func() string { return "st_1" },
		OnLinked: func() { linked = true }}
	if err := rs.StartLink(nil); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, rs, PhaseAskSaved)
	if err := rs.SubmitKey(EncodeRecovery(1, bytes.Repeat([]byte{4}, KeySize))); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if err := rs.SubmitKey(EncodeRecovery(1, dek)); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	got, _, err := e.up.Keys.Load("st_1")
	if rs.State().Phase != PhaseLinked || !linked || err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("phase %s linked=%v err=%v", rs.State().Phase, linked, err)
	}
}

func TestKeyStoreKeepsOlderGenerations(t *testing.T) {
	ks := KeyStore{Dir: t.TempDir()}
	k1 := bytes.Repeat([]byte{1}, KeySize)
	k2 := bytes.Repeat([]byte{2}, KeySize)
	_ = ks.Save("st_1", 1, k1)
	_ = ks.Save("st_1", 2, k2)
	cur, gen, err := ks.Load("st_1")
	if err != nil || gen != 2 || !bytes.Equal(cur, k2) {
		t.Fatalf("current: %d %v", gen, err)
	}
	old := ks.Older("st_1")
	if len(old) != 1 || old[0].Generation != 1 || !bytes.Equal(old[0].DEK, k1) {
		t.Fatalf("older: %+v", old)
	}
	// Saving a lower generation later never demotes the current key.
	_ = ks.Save("st_1", 1, k1)
	if _, g, _ := ks.Load("st_1"); g != 2 {
		t.Fatalf("current demoted to %d", g)
	}
}

// stuckShop: the cloud holds generation 1 with no wrapped copy and this till
// has no key (a new main till; the owner never saved the key).
func stuckShop(t *testing.T) (*tillEnv, []byte) {
	e, dek := offKeyShop(t)
	e.up.Keys.Delete()
	return e, dek
}

func TestStuckShopNeedsLinkWithNoCloudCopyThenStartsANewKey(t *testing.T) {
	e, oldDEK := stuckShop(t)
	e.cloud.ownerApproved = true // the owner approved a new key on my.
	ctx := context.Background()
	e.now = e.now.Add(25 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(false)
	if e.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink || e.get(data.OffsiteLinkCopyKey) != "no" {
		t.Fatalf("reason=%q copy=%q", e.get(data.OffsiteRefusedReasonKey), e.get(data.OffsiteLinkCopyKey))
	}
	if e.cloud.begins != 1 {
		t.Fatalf("begin ran while stuck: %d", e.cloud.begins)
	}
	_ = e.set.Set(ctx, data.OffsiteKeyInCloudKey, data.OffsiteKeyInCloudOn)
	if err := e.up.StartNewKey(ctx); err != nil {
		t.Fatal(err)
	}
	dek, gen, err := e.up.Keys.Load("st_1")
	if err != nil || gen != 2 || bytes.Equal(dek, oldDEK) {
		t.Fatalf("new key: gen=%d err=%v", gen, err)
	}
	if e.cloud.keyGen != 2 || e.cloud.keyFP != Fingerprint(dek) || e.cloud.keyDEK == nil {
		t.Fatalf("cloud: gen=%d (choice is on, so a copy is expected)", e.cloud.keyGen)
	}
	if e.get(data.OffsiteRefusedReasonKey) != "" || e.get(data.OffsiteLinkCopyKey) != "" {
		t.Fatal("the needs-link state was not cleared")
	}
	e.round(true)
	if e.cloud.begins != 2 {
		t.Fatalf("backups did not resume: begins=%d", e.cloud.begins)
	}
}

func TestStartNewKeyChoiceOffSendsNoCopyAndRefusesWhenNotStuck(t *testing.T) {
	e, _ := stuckShop(t) // choice "" (not on)
	e.cloud.ownerApproved = true
	if err := e.up.StartNewKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.cloud.keyGen != 2 || e.cloud.keyDEK != nil {
		t.Fatalf("gen=%d copy=%v", e.cloud.keyGen, e.cloud.keyDEK != nil)
	}
	// Now this till has a key: no second new key.
	if err := e.up.StartNewKey(context.Background()); !errors.Is(err, ErrNotStuck) {
		t.Fatalf("second start: %v", err)
	}
	// Nor when the cloud holds a copy (link via my. instead).
	e2 := newTill(t)
	e2.snapshot()
	e2.round(false) // choice on: the cloud holds a copy
	e2.up.Keys.Delete()
	if err := e2.up.StartNewKey(context.Background()); !errors.Is(err, ErrNotStuck) {
		t.Fatalf("with a cloud copy: %v", err)
	}
}

func TestEnterSavedKeyLinksAndRejectsAWrongOne(t *testing.T) {
	e, dek := stuckShop(t)
	ctx := context.Background()
	if err := e.up.EnterSavedKey(ctx, EncodeRecovery(1, bytes.Repeat([]byte{8}, KeySize))); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if err := e.up.EnterSavedKey(ctx, "UTK1-nonsense"); !errors.Is(err, ErrBadRecoveryKey) {
		t.Fatalf("bad text: %v", err)
	}
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("a rejected key was saved")
	}
	if err := e.up.EnterSavedKey(ctx, EncodeRecovery(1, dek)); err != nil {
		t.Fatal(err)
	}
	if got, _, err := e.up.Keys.Load("st_1"); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("not saved: %v", err)
	}
}

func TestRestoreOfAnOlderGenerationBackupWorksLocally(t *testing.T) {
	// The backup was made under generation 1; this till later started
	// generation 2 and still knows generation 1 as an older key.
	e, dek1 := offKeyShop(t)
	b := e.onlyBackup()
	k2 := bytes.Repeat([]byte{6}, KeySize)
	_ = e.up.Keys.Save("st_1", 2, k2)
	e.cloud.keyGen, e.cloud.keyFP = 2, Fingerprint(k2)
	if older := e.up.Keys.Older("st_1"); len(older) != 1 || !bytes.Equal(older[0].DEK, dek1) {
		t.Fatal("setup: generation 1 not kept as older")
	}
	r, rs := replacement(t, e)
	_ = r.up // the replacement is a fresh database of the same shop
	rs.Keys = e.up.Keys
	if err := rs.Start(b.id, b.begin.TakenAt, nil); err != nil {
		t.Fatal(err)
	}
	rs.Wait()
	if rs.State().Phase != PhaseReady {
		t.Fatalf("older-generation restore: %+v", rs.State())
	}
}

func TestKeyExistsDetailsAreReadFromData(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	// A key registered by another till first: register hits 409 key_exists.
	other := bytes.Repeat([]byte{5}, KeySize)
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 1, Fingerprint(other), nil
	real := e.up.Cloud
	e.up.Cloud = &hideKeyCloud{Cloud: real}
	e.round(false)
	if e.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink || e.get(data.OffsiteLinkCopyKey) != "no" {
		t.Fatalf("reason=%q copy=%q", e.get(data.OffsiteRefusedReasonKey), e.get(data.OffsiteLinkCopyKey))
	}
}

// hideKeyCloud answers GET key as "none yet" so the till tries to register and
// meets the 409 key_exists body the cloud sends.
type hideKeyCloud struct{ Cloud }

func (h *hideKeyCloud) GetKey(context.Context) (KeyState, error) { return KeyState{}, nil }

func TestKeyExists409KeepsEveryKeyGeneration(t *testing.T) {
	e := newTill(t)
	ctx := context.Background()
	old, _, _ := e.up.Keys.Generate("st_1") // generation 1
	cur := bytes.Repeat([]byte{4}, KeySize)
	if err := e.up.Keys.Save("st_1", 2, cur); err != nil {
		t.Fatal(err)
	}
	other := bytes.Repeat([]byte{9}, KeySize)
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 5, Fingerprint(other), nil
	if err := e.up.register(ctx, cur, 2); !errors.Is(err, errNeedsLink) {
		t.Fatalf("register err = %v, want needs-link", err)
	}
	got, gen, err := e.up.Keys.Load("st_1")
	if err != nil || gen != 2 || !bytes.Equal(got, cur) {
		t.Fatalf("current key lost after a 409: gen=%d err=%v", gen, err)
	}
	if o := e.up.Keys.Older("st_1"); len(o) != 1 || o[0].Generation != 1 || !bytes.Equal(o[0].DEK, old) {
		t.Fatalf("older generations after a 409 = %+v, want generation 1", o)
	}
}

// ---- fix-bak-till: ut-docs#6607, #6608, #6612 ----

// #6607: a new key generation is the owner's action on my. (ADR-0199 D12): a
// device token alone never gets one registered, and the till says whom to ask.
func TestStartNewKeyWithoutOwnerApprovalIsRefusedAndLeavesNoKey(t *testing.T) {
	e, _ := stuckShop(t)
	e.cloud.ownerApproved = false
	err := e.up.StartNewKey(context.Background())
	if !errors.Is(err, ErrNeedsOwnerApproval) {
		t.Fatalf("err = %v, want ErrNeedsOwnerApproval", err)
	}
	if e.cloud.keyGen != 1 {
		t.Fatalf("the cloud moved to generation %d on a device token alone", e.cloud.keyGen)
	}
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("the till kept a key the cloud never accepted")
	}
}

// #6608: a till that holds a key never adopts a released key of another fingerprint.
func TestLinkRefusesAReleasedKeyOfAnotherFingerprintWhenTheTillHoldsAKey(t *testing.T) {
	e := newTillChoice(t, data.OffsiteKeyInCloudOn)
	e.snapshot()
	e.round(false)
	own, _, err := e.up.Keys.Load("st_1")
	if err != nil {
		t.Fatal(err)
	}
	x := bytes.Repeat([]byte{0xEE}, KeySize)
	e.cloud.mu.Lock()
	e.cloud.keyGen, e.cloud.keyFP, e.cloud.keyDEK = 2, Fingerprint(x), x
	e.cloud.mu.Unlock()
	rs := e.linkRestorer()
	_ = rs.StartLink(nil)
	rs.Wait()
	if rs.State().Phase == PhaseLinked {
		t.Fatal("the till adopted a different key")
	}
	cur, gen, err := e.up.Keys.Load("st_1")
	if err != nil || gen != 1 || !bytes.Equal(cur, own) {
		t.Fatalf("the till's key changed: gen=%d err=%v", gen, err)
	}
}

// #6608: with the choice Off a cloud-released key is never saved, even by a
// till that holds no key.
func TestChoiceOffNeverSavesACloudReleasedKey(t *testing.T) {
	e := newTillChoice(t, data.OffsiteKeyInCloudOff)
	e.snapshot()
	e.round(false)
	own, _, _ := e.up.Keys.Load("st_1")
	e.up.Keys.Delete() // lost its file; the cloud still names generation 1 with a copy
	e.cloud.mu.Lock()
	e.cloud.keyDEK = own
	e.cloud.mu.Unlock()
	rs := e.linkRestorer()
	_ = rs.StartLink(nil)
	deadline := time.Now().Add(3 * time.Second)
	for rs.State().Phase != PhaseAskSaved && rs.State().Phase != PhaseLinked && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rs.State().Phase != PhaseAskSaved {
		t.Fatalf("phase %s: want the till to ask for the saved key", rs.State().Phase)
	}
	rs.Cancel()
	rs.Wait()
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("the key file exists")
	}
}

// #6612: a final device_revoked answer deletes the key file.
func TestFinalDeviceRevokedReasonDeletesTheKeyFile(t *testing.T) {
	for _, reason := range []string{"replaced", "moved", "removed"} {
		t.Run(reason, func(t *testing.T) {
			e := newTill(t)
			e.snapshot()
			e.round(false)
			if _, _, err := e.up.Keys.Load("st_1"); err != nil {
				t.Fatal(err)
			}
			e.cloud.revokedReason = reason
			e.now = e.now.Add(25 * time.Hour)
			e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
			e.round(true)
			if _, _, err := e.up.Keys.Load("st_1"); err == nil {
				t.Fatal("the key file survived a final device_revoked")
			}
		})
	}
}

// The real cloud answers a plain removal with no reason (ut-docs#6612).
func TestReasonlessDeviceRevokedDeletesTheKeyFile(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.round(false)
	e.cloud.revokedNoReason = true
	e.now = e.now.Add(25 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(true)
	if _, _, err := e.up.Keys.Load("st_1"); err == nil {
		t.Fatal("the key file survived a reason-less device_revoked")
	}
}

func TestNonFinalOrOtherAuthErrorsKeepTheKeyFile(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.round(false)
	e.cloud.revokedReason = "something_temporary"
	e.now = e.now.Add(25 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(true)
	if _, _, err := e.up.Keys.Load("st_1"); err != nil {
		t.Fatalf("the key was deleted on a non-final reason: %v", err)
	}
}

func (e *tillEnv) linkRestorer() *Restorer {
	return &Restorer{DBPath: e.dbPath, Cloud: e.cloud.cloud(), PollEvery: time.Millisecond,
		Sleep: func(context.Context, time.Duration) {}, Keys: e.up.Keys, StoreID: func() string { return "st_1" },
		Choice:   func() string { return e.get(data.OffsiteKeyInCloudKey) },
		OnLinked: func() { e.up.resume(context.Background()) }}
}
