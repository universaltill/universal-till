package offsitebackup

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// ProblemOffsiteFailed tags the Problems entry opened after 48 h of failing uploads, or
// at once when the disk is too full to prepare one (ut-docs#5066).
const ProblemOffsiteFailed = "backup.offsite_failed"

const (
	// lastSuccessFloor: an upload that succeeded this recently is enough.
	lastSuccessFloor = 20 * time.Hour
	// problemAfter: how long uploads may keep failing before a Problem opens.
	problemAfter = 48 * time.Hour
	// refusedWait is how long a 402/409 stops the rounds.
	refusedWait = 24 * time.Hour
	// pendingMaxAge: the cloud drops a pending backup after 24 h.
	pendingMaxAge = 24 * time.Hour
	formatV1      = 1
	partAttempts  = 3
)

// Backoff after consecutive failures (AC1.7), capped at the last entry.
var backoff = []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour}

// Reasons recorded in last_error / refused_reason; the UI maps each to a
// translated line (backup.cloud.err.*).
const (
	ReasonDisk         = "disk"
	ReasonNetwork      = "network"
	ReasonServer       = "server"
	ReasonRateLimited  = "rate_limited"
	ReasonNotInPlan    = "cloud_backup_not_in_plan"
	ReasonSubscription = "subscription_inactive"
	ReasonNotMainTill  = "not_main_till"
	ReasonTooLarge     = "too_large"
	ReasonNotEnabled   = "not_enabled"
	// ReasonNeedsLink: the cloud holds a shop key this till does not (or a
	// different one). The owner gives the till the key from Settings (K5).
	ReasonNeedsLink = "needs_link"
	ReasonOther     = "other"

	// Credential refusals (ut-docs#7058): the cloud does not accept this
	// till's device credential or shop. A fixed enum, never cloud text.
	ReasonPairAgain          = "pair_again"
	ReasonTillReplaced       = "till_replaced"
	ReasonTillMoved          = "till_moved"
	ReasonTillRemoved        = "till_removed"
	ReasonServiceUnavailable = "service_unavailable"
	// ReasonOwnerApproval: a new key generation waits for the owner on my.
	// (a failure with backoff, not a credential refusal).
	ReasonOwnerApproval = "owner_approval"
)

// credentialReasons are the reasons a dead credential records; they stop the
// rounds until a later successful check-in (or 24 h).
var credentialReasons = map[string]bool{
	ReasonPairAgain: true, ReasonTillReplaced: true, ReasonTillMoved: true,
	ReasonTillRemoved: true, ReasonServiceUnavailable: true,
}

// credentialRefusal maps a cloud answer that says this till's credential (or
// shop) is not accepted to its reason (ADR-0116 D6 codes, ut-docs#7058).
func credentialRefusal(ae *APIError) (string, bool) {
	switch {
	case ae.Status == 401 && ae.Code == "device_revoked":
		switch ae.Reason {
		case "replaced":
			return ReasonTillReplaced, true
		case "moved":
			return ReasonTillMoved, true
		}
		return ReasonTillRemoved, true
	case ae.Status == 401:
		return ReasonPairAgain, true
	case ae.Status == 403 && ae.Code == "device_mismatch":
		return ReasonPairAgain, true
	case ae.Status == 403 && ae.Code == "service_unavailable":
		return ReasonServiceUnavailable, true
	}
	return "", false
}

// Uploader runs one upload round at a time. All fields but Mu are set by the
// caller; zero Now/Sleep/FreeDisk/Strip fall back to the real ones.
type Uploader struct {
	DBPath        string
	Settings      Store
	Cloud         Cloud
	Gate          func(ctx context.Context, now time.Time) GateState
	AppVersion    string
	SchemaVersion func() int
	Now           func() time.Time
	Sleep         func(context.Context, time.Duration)
	FreeDisk      func(dir string) (uint64, error)
	Strip         func(src, dst string) error
	// Keys is the till's shop-key file; StoreID is the live cloud store id
	// (the key is bound to it and its id goes into the key derivation).
	Keys    KeyStore
	StoreID func() string

	mu sync.Mutex
}

func (u *Uploader) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Uploader) sleep(ctx context.Context, d time.Duration) {
	if u.Sleep != nil {
		u.Sleep(ctx, d)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

type pendingMeta struct {
	Snapshot        string `json:"snapshot"`
	TakenAt         string `json:"taken_at"`
	PartMax         int64  `json:"part_max"`
	Parts           int    `json:"parts"`
	CiphertextBytes int64  `json:"ciphertext_bytes"`
	PlaintextBytes  int64  `json:"plaintext_bytes"`
	StartedAt       string `json:"started_at"`
}

// Round does one pass: gates, backoff, then resume or start an upload. force
// (the "Upload now" button) skips the backoff and the 20 h rule but never
// the gates, and never re-sends a snapshot that is already in the cloud. It returns nil for "nothing to do" and for failures it has
// already recorded; an error only for a cancelled context.
func (u *Uploader) Round(ctx context.Context, force bool) error {
	if !u.mu.TryLock() {
		return nil // single flight
	}
	defer u.mu.Unlock()
	now := u.now()
	if u.Gate(ctx, now) != GateOpen {
		return nil
	}
	st := u.Settings
	get := func(k string) string { v, _, _ := st.Get(ctx, k); return strings.TrimSpace(v) }
	// A dead credential waits for a check-in the cloud answered after the
	// refusal (registration.last_checkin_at is never written by a 401).
	if credentialReasons[get(data.OffsiteRefusedReasonKey)] {
		refused, _ := time.Parse(time.RFC3339, get(data.OffsiteLastErrorAtKey))
		if c, err := time.Parse(time.RFC3339, get(enroll.KeyLastCheckinAt)); err == nil && c.After(refused) {
			_ = st.Delete(ctx, data.OffsiteRefusedReasonKey)
			_ = st.Delete(ctx, data.OffsiteNextAttemptAtKey)
		}
	}
	if !force {
		if t, err := time.Parse(time.RFC3339, get(data.OffsiteNextAttemptAtKey)); err == nil && now.Before(t) {
			return nil
		}
	}
	dir, err := db.BackupDir(u.DBPath)
	if err != nil {
		u.fail(ctx, now, err)
		return ctx.Err()
	}
	pendingID, pendingFile := get(data.OffsitePendingIDKey), get(data.OffsitePendingFileKey)
	u.sweepTemp(dir, pendingFile)

	var err2 error
	if pendingID != "" && pendingFile != "" {
		var meta pendingMeta
		_ = json.Unmarshal([]byte(get(data.OffsitePendingMetaKey)), &meta)
		started, _ := time.Parse(time.RFC3339, meta.StartedAt)
		if _, serr := os.Stat(filepath.Join(dir, pendingFile)); serr != nil || meta.Parts == 0 || (!started.IsZero() && now.Sub(started) >= pendingMaxAge) {
			u.dropPending(ctx, dir)
			pendingID = ""
		} else {
			err2 = u.finishUpload(ctx, now, dir, pendingID, pendingFile, meta, true)
			if isGone(err2) {
				u.dropPending(ctx, dir)
				pendingID, err2 = "", nil
			} else {
				return u.record(ctx, now, err2)
			}
		}
	}
	if !force {
		if t, err := time.Parse(time.RFC3339, get(data.OffsiteLastSuccessAtKey)); err == nil && now.Sub(t) < lastSuccessFloor {
			return nil
		}
	}
	return u.record(ctx, now, u.startUpload(ctx, now, dir, get(data.OffsiteLastSnapshotKey)))
}

func isGone(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 410)
}

type diskError struct{ need, free uint64 }

func (e *diskError) Error() string {
	return fmt.Sprintf("offsitebackup: not enough free disk (need %d, free %d)", e.need, e.free)
}

var errNothingToDo = errors.New("nothing to upload")

func (u *Uploader) startUpload(ctx context.Context, now time.Time, dir, lastSnapshot string) error {
	list, err := db.ListBackupsAt(u.DBPath, now)
	if err != nil {
		return err
	}
	var snap *db.BackupInfo
	for i := range list {
		if list[i].AgeKnown && !db.IsUploadedBackupName(list[i].Name) {
			snap = &list[i]
			break
		}
	}
	if snap == nil || snap.Name == lastSnapshot {
		return errNothingToDo
	}
	dek, gen, err := u.ensureKey(ctx)
	if err != nil {
		return err
	}
	defer zero(dek)
	// Free space: the stripped copy plus the sealed file (AC1.5).
	need := uint64(snap.Size) * 3 / 2
	if free, ferr := u.freeDisk(dir); ferr == nil && free < need {
		return &diskError{need: need, free: free}
	}
	copyPath := filepath.Join(dir, "offsite-"+strconv.FormatInt(now.UnixNano(), 36)+".part")
	defer func() {
		_ = os.Remove(copyPath)
		_ = os.Remove(copyPath + "-journal")
	}()
	strip := u.Strip
	if strip == nil {
		strip = db.StripCloudIdentityCopy
	}
	if err := strip(filepath.Join(dir, snap.Name), copyPath); err != nil {
		return fmt.Errorf("prepare the copy: %w", err)
	}
	cst, err := os.Stat(copyPath)
	if err != nil {
		return err
	}
	takenAt := snap.EffectiveTime.UTC().Format(time.RFC3339)
	begin, err := u.Cloud.Begin(ctx, BeginRequest{
		SnapshotBytes: cst.Size(), TakenAt: takenAt, AppVersion: u.AppVersion,
		SchemaVersion: u.schemaVersion(), Format: formatV1,
		DEKGeneration: gen, KeyFingerprint: Fingerprint(dek),
	})
	if err != nil {
		return err
	}
	id, err := parseUUID(begin.BackupID)
	if err != nil {
		return err
	}
	key, err := DeriveKey(dek, id, u.storeID())
	if err != nil {
		return err
	}
	defer zero(key)
	sealedName := "offsite-" + begin.BackupID + ".utbk"
	sealedPath := filepath.Join(dir, sealedName)
	n, err := sealFile(copyPath, sealedPath, key, id)
	if err != nil {
		_ = os.Remove(sealedPath)
		return fmt.Errorf("encrypt: %w", err)
	}
	partMax := begin.PartMaxBytes
	if partMax <= 0 {
		partMax = 8 << 20
	}
	meta := pendingMeta{
		Snapshot: snap.Name, TakenAt: takenAt, PartMax: partMax,
		Parts: int((n + partMax - 1) / partMax), CiphertextBytes: n, PlaintextBytes: cst.Size(),
		StartedAt: now.UTC().Format(time.RFC3339),
	}
	if begin.MaxBytes > 0 && n > begin.MaxBytes {
		_ = os.Remove(sealedPath)
		return &APIError{Status: 413, Code: "too_large"}
	}
	mb, _ := json.Marshal(meta)
	if err := u.Settings.SetMany(ctx, map[string]string{
		data.OffsitePendingIDKey: begin.BackupID, data.OffsitePendingFileKey: sealedName, data.OffsitePendingMetaKey: string(mb),
	}); err != nil {
		_ = os.Remove(sealedPath)
		return err
	}
	_ = os.Remove(copyPath)
	return u.finishUpload(ctx, now, dir, begin.BackupID, sealedName, meta, false)
}

func (u *Uploader) storeID() string {
	if u.StoreID != nil {
		return strings.TrimSpace(u.StoreID())
	}
	return ""
}

// StoreIDValue is the live cloud store id this till's key is bound to.
func (u *Uploader) StoreIDValue() string { return u.storeID() }

// errNeedsLink: the till must be linked to the shop's key first.
var errNeedsLink = &APIError{Status: 409, Code: ReasonNeedsLink}

// ensureKey returns this till's shop key (K1, E1). With none, and none known
// to the cloud, it generates one. The key's fingerprint is registered with the
// cloud before any begin (the cloud checks begin against it); a wrapped copy
// goes too only if the owner chose that (syncEscrow). A cloud that knows a key
// this till lacks (or a different one) means the till must be linked.
func (u *Uploader) ensureKey(ctx context.Context) ([]byte, int, error) {
	sid := u.storeID()
	if sid == "" {
		return nil, 0, errNotConnected
	}
	dek, gen, lerr := u.Keys.Load(sid)
	have := lerr == nil
	state, err := u.Cloud.GetKey(ctx)
	if err != nil {
		zero(dek)
		return nil, 0, err
	}
	u.noteBlocked(ctx, state.EscrowBlocked)
	cloudHas := state.Generation != nil && *state.Generation > 0
	switch {
	case !have && cloudHas:
		u.noteLinkCopy(ctx, state.InCloud)
		return nil, 0, errNeedsLink
	case !have:
		if dek, gen, err = u.Keys.Generate(sid); err != nil {
			return nil, 0, err
		}
		if err := u.register(ctx, dek, gen); err != nil {
			zero(dek)
			return nil, 0, err
		}
		return dek, gen, nil
	case cloudHas && gen > *state.Generation:
		// A newer key this till started (StartNewKey) that the cloud has not
		// registered yet: send it.
		if err := u.register(ctx, dek, gen); err != nil {
			zero(dek)
			return nil, 0, err
		}
		return dek, gen, nil
	case cloudHas:
		if *state.Generation != gen || state.Fingerprint == nil || *state.Fingerprint != Fingerprint(dek) {
			zero(dek)
			u.noteLinkCopy(ctx, state.InCloud)
			return nil, 0, errNeedsLink
		}
		u.syncEscrow(ctx, dek, gen, state)
		return dek, gen, nil
	}
	// A key the cloud has not seen yet (a failed register last time).
	if err := u.register(ctx, dek, gen); err != nil {
		zero(dek)
		return nil, 0, err
	}
	return dek, gen, nil
}

func (u *Uploader) choice(ctx context.Context) string {
	v, _, _ := u.Settings.Get(ctx, data.OffsiteKeyInCloudKey)
	return strings.TrimSpace(v)
}

// register tells the cloud about a new key: the fingerprint always, the key
// itself (to be wrapped) only when the owner said yes and has not been
// blocked on my. No begin happens until this worked. A 409 key_exists means
// another till registered a key first: ours is not the shop's.
func (u *Uploader) register(ctx context.Context, dek []byte, gen int) error {
	put := KeyPut{Generation: gen, Fingerprint: Fingerprint(dek)}
	blocked := false
	if v, _, _ := u.Settings.Get(ctx, data.OffsiteEscrowBlockedKey); strings.TrimSpace(v) == "1" {
		blocked = true
	}
	if u.choice(ctx) == data.OffsiteKeyInCloudOn && !blocked {
		put.DEKB64 = base64.StdEncoding.EncodeToString(dek)
	}
	err := u.Cloud.PutKey(ctx, put)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 409 {
		switch ae.Code {
		case "key_exists":
			// Keep every key generation we hold (H2): a 409 only means the
			// till must be linked; deleting would lose older backups' keys.
			u.noteLinkCopy(ctx, ae.KeyInCloud != nil && *ae.KeyInCloud)
			return errNeedsLink
		case "escrow_blocked":
			u.noteBlocked(ctx, true)
			put.DEKB64 = ""
			return u.Cloud.PutKey(ctx, put)
		}
	}
	return err
}

func (u *Uploader) noteLinkCopy(ctx context.Context, inCloud bool) {
	v := "no"
	if inCloud {
		v = "yes"
	}
	_ = u.Settings.Set(ctx, data.OffsiteLinkCopyKey, v)
}

// Errors the key actions return for the page to word.
var (
	ErrNotStuck = errors.New("offsitebackup: this till does not need a new backup key")
	// ErrNeedsOwnerApproval: the cloud registers a new key generation only
	// after the owner approved it on my. (ADR-0199 D12); a device token alone
	// never does (ut-docs#6607).
	ErrNeedsOwnerApproval = errors.New("offsitebackup: the owner must approve a new backup key on my.universaltill.com")
)

// resume ends the "needs linking" wait: the next round runs at once.
func (u *Uploader) resume(ctx context.Context) {
	for _, k := range []string{data.OffsiteRefusedReasonKey, data.OffsiteNextAttemptAtKey, data.OffsiteLinkCopyKey} {
		_ = u.Settings.Delete(ctx, k)
	}
}

// StartNewKey is the way out for a shop whose key is held by neither this
// till nor the cloud: it makes a new key, generation current+1, registers it
// (with a wrapped copy only if the owner's choice is on), and saves it. Older
// backups can then only be opened with the key the owner saved. It refuses
// unless this till has no key and the cloud shows a key without a copy.
// The key is saved first, so a failed registration is retried by the next
// round rather than losing the key.
func (u *Uploader) StartNewKey(ctx context.Context) error {
	sid := u.storeID()
	if sid == "" {
		return errNotConnected
	}
	if _, _, err := u.Keys.Load(sid); err == nil {
		return ErrNotStuck
	}
	st, err := u.Cloud.GetKey(ctx)
	if err != nil {
		return err
	}
	if st.Generation == nil || *st.Generation < 1 || st.InCloud {
		return ErrNotStuck
	}
	dek := make([]byte, KeySize)
	if _, err := rand.Read(dek); err != nil {
		return err
	}
	defer zero(dek)
	gen := *st.Generation + 1
	if err := u.Keys.Save(sid, gen, dek); err != nil {
		return err
	}
	if err := u.register(ctx, dek, gen); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == CodeOwnerApprovalRequired {
			// This till held no key before (checked above): drop the one just
			// made rather than keep a generation the cloud never accepted.
			u.Keys.Delete()
			return ErrNeedsOwnerApproval
		}
		return err // keys stay: a 409 never deletes a generation (H2)
	}
	u.resume(ctx)
	return nil
}

// CodeOwnerApprovalRequired is the cloud's refusal of a new key generation
// that the owner has not approved on my.
const CodeOwnerApprovalRequired = "new_key_not_approved"

// EnterSavedKey links this till with the owner's saved key (UTK1-...) after
// checking its checksum and that it is this shop's key.
func (u *Uploader) EnterSavedKey(ctx context.Context, text string) error {
	gen, dek, err := DecodeRecovery(text)
	if err != nil {
		return err
	}
	defer zero(dek)
	st, err := u.Cloud.GetKey(ctx)
	if err != nil {
		return err
	}
	if st.Fingerprint == nil || Fingerprint(dek) != *st.Fingerprint {
		return ErrWrongKey
	}
	if err := u.Keys.Save(u.storeID(), gen, dek); err != nil {
		return err
	}
	u.resume(ctx)
	return nil
}

func (u *Uploader) noteBlocked(ctx context.Context, blocked bool) {
	if blocked {
		_ = u.Settings.Set(ctx, data.OffsiteEscrowBlockedKey, "1")
	} else {
		_ = u.Settings.Delete(ctx, data.OffsiteEscrowBlockedKey)
	}
}

// syncEscrow makes the cloud copy match the owner's choice: on and missing,
// send it; off changes nothing in the cloud (see below). A failure is logged and retried next
// round; it never stops an upload.
func (u *Uploader) syncEscrow(ctx context.Context, dek []byte, gen int, st KeyState) {
	switch u.choice(ctx) {
	case data.OffsiteKeyInCloudOn:
		if st.InCloud || st.EscrowBlocked {
			return
		}
		err := u.Cloud.PutKey(ctx, KeyPut{Generation: gen, DEKB64: base64.StdEncoding.EncodeToString(dek), Fingerprint: Fingerprint(dek)})
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 409 && ae.Code == "escrow_blocked" {
			u.noteBlocked(ctx, true)
		} else if err != nil {
			logging.L().Warnf("offsitebackup: could not keep a copy of the key in the cloud: %s", classify(err))
		}
	}
	// A choice of "off" never deletes a copy the cloud already holds: a till
	// (device token) has no right to drop it. The owner removes it on my.
	// with a fresh sign-in (ut-docs#5066 M3).
}

// ApplyKeyChoice applies the owner's choice now (the Settings button), not at
// the next upload. With no key on this till yet there is nothing to do: the
// first upload applies it. The error is for the page to show.
func (u *Uploader) ApplyKeyChoice(ctx context.Context) error {
	dek, gen, err := u.Keys.Load(u.storeID())
	if err != nil {
		return nil
	}
	defer zero(dek)
	st, err := u.Cloud.GetKey(ctx)
	if err != nil {
		return err
	}
	u.noteBlocked(ctx, st.EscrowBlocked)
	if st.Generation == nil || *st.Generation != gen || st.Fingerprint == nil || *st.Fingerprint != Fingerprint(dek) {
		return nil // not registered yet (or needs linking): the round deals with it
	}
	switch u.choice(ctx) {
	case data.OffsiteKeyInCloudOn:
		if st.InCloud || st.EscrowBlocked {
			return nil
		}
		err := u.Cloud.PutKey(ctx, KeyPut{Generation: gen, DEKB64: base64.StdEncoding.EncodeToString(dek), Fingerprint: Fingerprint(dek)})
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 409 && ae.Code == "escrow_blocked" {
			u.noteBlocked(ctx, true) // the card says: turned off by the owner on my.
			return nil
		}
		return err
	}
	// "off" with a copy already in the cloud: the owner deletes it on my. (M3).
	return nil
}

func (u *Uploader) schemaVersion() int {
	if u.SchemaVersion != nil {
		return u.SchemaVersion()
	}
	return db.LatestMigrationVersion()
}

func (u *Uploader) freeDisk(dir string) (uint64, error) {
	if u.FreeDisk != nil {
		return u.FreeDisk(dir)
	}
	return probeFree(dir)
}

// sealFile gzips (level 1) and seals src into dst, streaming through a pipe:
// at most two cipher chunks, the gzip window and the copy buffer are in memory.
func sealFile(src, dst string, key []byte, id [16]byte) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	pr, pw := io.Pipe()
	go func() {
		defer logging.RecoverAndLog("offsitebackup.upload.gzip")
		zw, _ := gzip.NewWriterLevel(pw, gzip.BestSpeed)
		_, err := io.Copy(zw, bufio.NewReaderSize(in, 256<<10))
		if cerr := zw.Close(); err == nil {
			err = cerr
		}
		pw.CloseWithError(err)
	}()
	serr := Seal(out, key, id, pr)
	_ = pr.CloseWithError(serr) // unblocks the gzip goroutine on a Seal failure
	if cerr := out.Close(); serr == nil {
		serr = cerr
	}
	if serr != nil {
		return 0, serr
	}
	st, err := os.Stat(dst)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// finishUpload sends the parts the cloud does not have and completes.
func (u *Uploader) finishUpload(ctx context.Context, now time.Time, dir, backupID, file string, meta pendingMeta, resume bool) error {
	f, err := os.Open(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	defer f.Close()
	have := map[int]PartInfo{}
	if resume {
		st, err := u.Cloud.Status(ctx, backupID)
		if err != nil {
			return err
		}
		if st.Status == "deleted" {
			return &APIError{Status: 410, Code: "expired"}
		}
		for _, p := range st.Parts {
			have[p.N] = p
		}
	}
	buf := make([]byte, meta.PartMax)
	for n := 1; n <= meta.Parts; n++ {
		off := int64(n-1) * meta.PartMax
		size := min(meta.PartMax, meta.CiphertextBytes-off)
		chunk := buf[:size]
		if _, err := f.ReadAt(chunk, off); err != nil && !(errors.Is(err, io.EOF) && int64(len(chunk)) == size) {
			return err
		}
		sum := sha256.Sum256(chunk)
		sha := hex.EncodeToString(sum[:])
		if p, ok := have[n]; ok && p.SHA256 == sha && p.Bytes == size {
			continue
		}
		if err := u.putPart(ctx, backupID, n, chunk, sha); err != nil {
			return err
		}
	}
	if err := u.Cloud.Complete(ctx, backupID, CompleteRequest{
		Parts: meta.Parts, CiphertextBytes: meta.CiphertextBytes, PlaintextBytes: meta.PlaintextBytes,
	}); err != nil {
		return err
	}
	u.succeed(ctx, now, dir, meta)
	return nil
}

func (u *Uploader) putPart(ctx context.Context, backupID string, n int, chunk []byte, sha string) error {
	var err error
	for attempt := 0; attempt < partAttempts; attempt++ {
		if attempt > 0 {
			u.sleep(ctx, time.Duration(attempt)*2*time.Second)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = u.Cloud.PutPart(ctx, backupID, n, chunk, sha); err == nil {
			return nil
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != 408 && ae.Status != 429 {
			return err // not worth repeating
		}
	}
	return err
}

func (u *Uploader) succeed(ctx context.Context, now time.Time, dir string, meta pendingMeta) {
	st := u.Settings
	stamp := now.UTC().Format(time.RFC3339)
	_ = st.SetMany(ctx, map[string]string{
		data.OffsiteLastSuccessAtKey: stamp, data.OffsiteLastSnapshotKey: meta.Snapshot,
		// The #5071 "copy a backup off this till" reminder is met too.
		data.BackupCopiedAtSettingsKey: stamp,
	})
	for _, k := range []string{data.OffsiteLastErrorKey, data.OffsiteLastErrorAtKey, data.OffsiteFailingSinceKey,
		data.OffsiteNextAttemptAtKey, data.OffsiteAttemptsKey, data.OffsiteRefusedReasonKey} {
		_ = st.Delete(ctx, k)
	}
	u.dropPending(ctx, dir)
	logging.ResolveProblems(ProblemOffsiteFailed)
}

// dropPending forgets a pending upload and deletes its sealed file.
func (u *Uploader) dropPending(ctx context.Context, dir string) {
	if f, ok, _ := u.Settings.Get(ctx, data.OffsitePendingFileKey); ok && strings.TrimSpace(f) != "" {
		_ = os.Remove(filepath.Join(dir, filepath.Base(f)))
	}
	for _, k := range []string{data.OffsitePendingIDKey, data.OffsitePendingFileKey, data.OffsitePendingMetaKey} {
		_ = u.Settings.Delete(ctx, k)
	}
}

// sweepTemp removes offsite-* files nothing refers to (a crash between steps).
func (u *Uploader) sweepTemp(dir, keep string) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		n := e.Name()
		if !strings.HasPrefix(n, "offsite-") || n == keep {
			continue
		}
		if strings.HasSuffix(n, ".part") || strings.HasSuffix(n, ".part-journal") || strings.HasSuffix(n, ".utbk") {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// record turns a round's error into stored state: nil clears nothing (success
// already did), 402/409 are a refusal, anything else a failure with backoff.
func (u *Uploader) record(ctx context.Context, now time.Time, err error) error {
	switch {
	case err == nil, errors.Is(err, errNothingToDo):
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == "device_revoked" && finalRevokeReasons[ae.Reason] {
		// ADR-0199 D2: this till left the shop for good; its key file goes.
		// Only a reason that says so (ADR-0217 T3): a device_revoked with no
		// reason, or a healable one (store_deleted, pair_abandoned), may still
		// come back, and a till that heals needs its shop key.
		u.Keys.DeleteForStore(u.storeID())
	}
	if errors.As(err, &ae) {
		// ADR-0217: a 401 with an envelope code is evidence for the heal
		// engine (it probes before it acts).
		if ae.Status == 401 {
			credheal.Report(credheal.Signal{Source: "backup", Code: ae.Code, Reason: ae.Reason})
		}
		if reason, ok := credentialRefusal(ae); ok {
			stamp := now.UTC().Format(time.RFC3339)
			since, _, _ := u.Settings.Get(ctx, data.OffsiteFailingSinceKey)
			if _, perr := time.Parse(time.RFC3339, strings.TrimSpace(since)); perr != nil {
				since = stamp
			}
			_ = u.Settings.SetMany(ctx, map[string]string{
				data.OffsiteRefusedReasonKey: reason,
				data.OffsiteNextAttemptAtKey: now.Add(refusedWait).UTC().Format(time.RFC3339),
				data.OffsiteLastErrorAtKey:   stamp,
				data.OffsiteFailingSinceKey:  strings.TrimSpace(since),
			})
			logging.L().Warnf("offsitebackup: the cloud does not accept this till's connection (%s, HTTP %d); uploads wait until the till checks in again, at most 24 h", reason, ae.Status)
			u.maybeRaise(ctx, reason, now, strings.TrimSpace(since))
			return nil
		}
	}
	if errors.As(err, &ae) && (ae.Status == 402 || ae.Status == 409) {
		reason := ae.Code
		switch reason {
		case "":
			reason = ReasonOther
		case "key_mismatch", "key_required", "key_exists":
			reason = ReasonNeedsLink
		}
		_ = u.Settings.SetMany(ctx, map[string]string{
			data.OffsiteRefusedReasonKey: reason,
			data.OffsiteNextAttemptAtKey: now.Add(refusedWait).UTC().Format(time.RFC3339),
		})
		logging.L().Infof("offsitebackup: the cloud refused the backup (%s); next try in 24 h", reason)
		return nil
	}
	u.fail(ctx, now, err)
	return nil
}

// ClearCredentialRefusal ends a wait that exists only because the cloud refused
// this till's credential (a heal gave it a new one). It leaves any other wait
// (a key to link, the owner's approval) alone and reports whether it cleared.
func (u *Uploader) ClearCredentialRefusal(ctx context.Context) bool {
	v, _, _ := u.Settings.Get(ctx, data.OffsiteRefusedReasonKey)
	if !credentialReasons[strings.TrimSpace(v)] {
		return false
	}
	_ = u.Settings.Delete(ctx, data.OffsiteRefusedReasonKey)
	_ = u.Settings.Delete(ctx, data.OffsiteNextAttemptAtKey)
	return true
}

// finalRevokeReasons are the device_revoked reasons that cannot be undone:
// the till was replaced, moved to another shop, or removed.
var finalRevokeReasons = map[string]bool{"replaced": true, "moved": true, "removed": true}

func (u *Uploader) fail(ctx context.Context, now time.Time, err error) {
	st := u.Settings
	reason := classify(err)
	attempts := 0
	if v, _, _ := st.Get(ctx, data.OffsiteAttemptsKey); v != "" {
		attempts, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	wait := backoff[min(attempts, len(backoff)-1)]
	var ae *APIError
	if errors.As(err, &ae) && ae.RetryAfter > wait {
		wait = ae.RetryAfter
	}
	since, _, _ := st.Get(ctx, data.OffsiteFailingSinceKey)
	if _, perr := time.Parse(time.RFC3339, strings.TrimSpace(since)); perr != nil {
		since = now.UTC().Format(time.RFC3339)
	}
	_ = st.SetMany(ctx, map[string]string{
		data.OffsiteLastErrorKey: reason, data.OffsiteLastErrorAtKey: now.UTC().Format(time.RFC3339),
		data.OffsiteFailingSinceKey: since, data.OffsiteAttemptsKey: strconv.Itoa(attempts + 1),
		data.OffsiteNextAttemptAtKey: now.Add(wait).UTC().Format(time.RFC3339),
	})
	// The error text can carry paths; the log keeps the reason (and the
	// HTTP status of a cloud answer) only.
	if ae != nil {
		logging.L().Warnf("offsitebackup: upload failed (%s, HTTP %d); next try in %s", reason, ae.Status, wait)
	} else {
		logging.L().Warnf("offsitebackup: upload failed (%s); next try in %s", reason, wait)
	}
	u.maybeRaise(ctx, reason, now, since)
}

// maybeRaise opens the Problem after 48 h of failing, or at once for a full disk.
func (u *Uploader) maybeRaise(_ context.Context, reason string, now time.Time, since string) {
	t, _ := time.Parse(time.RFC3339, since)
	if reason == ReasonDisk || now.Sub(t) >= problemAfter {
		raise(reason, now.Sub(t))
	}
}

// failingFor prints an age as whole days and hours ("6d 19h").
func failingFor(d time.Duration) string {
	h := int(d / time.Hour)
	if h < 0 {
		h = 0
	}
	return fmt.Sprintf("%dd %dh", h/24, h%24)
}

func raise(reason string, failing time.Duration) {
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		if p.Key == ProblemOffsiteFailed {
			return
		}
	}
	logging.L().WarnProblemf(ProblemOffsiteFailed, "[Backup] off-site backup is failing (%s) for %s", reason, failingFor(failing))
}

func classify(err error) string {
	var de *diskError
	var ae *APIError
	var ne net.Error
	switch {
	case errors.As(err, &de):
		return ReasonDisk
	case errors.As(err, &ae):
		switch {
		case ae.Status == 429:
			return ReasonRateLimited
		case ae.Status == 413:
			return ReasonTooLarge
		case ae.Status == 503 && ae.Code == "not_enabled":
			return ReasonNotEnabled
		case ae.Status == 403 && ae.Code == CodeOwnerApprovalRequired:
			return ReasonOwnerApproval
		case ae.Status >= 500:
			return ReasonServer
		}
		return ReasonOther
	case errors.As(err, &ne), errors.Is(err, io.ErrUnexpectedEOF):
		return ReasonNetwork
	}
	return ReasonOther
}

func parseUUID(s string) ([16]byte, error) {
	var id [16]byte
	h := strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		return id, errors.New("the cloud sent an unusable backup id")
	}
	copy(id[:], b)
	return id, nil
}
