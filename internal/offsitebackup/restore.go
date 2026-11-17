package offsitebackup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
)

// Restore phases, as the page shows them.
const (
	PhaseIdle        = "idle"
	PhaseRequesting  = "requesting"
	PhaseWaiting     = "waiting" // the owner has not approved yet
	PhaseDownloading = "downloading"
	PhaseChecking    = "checking" // decrypting and checking the file
	PhaseReady       = "ready"    // staged: restart to finish
	PhaseDenied      = "denied"
	PhaseExpired     = "expired"
	PhaseFailed      = "failed"
	PhaseCancelled   = "cancelled"
	PhaseLinked      = "linked" // the shop key was saved: backups resume
	// PhaseAskSaved: the cloud holds no copy of the shop key, so the owner's
	// saved key (UTK1-...) is needed before the backup can be opened (E5).
	PhaseAskSaved = "needkey"
)

// Kinds of approval the owner gives on my.: a backup download, or just the
// shop's backup key for this till (K5).
const (
	KindRestore = "restore"
	KindLink    = "key"
)

// Error codes the page maps to translated lines (backup.cloud.err.*).
const (
	ErrCodeCorrupt      = "corrupt"
	ErrCodeOtherShop    = "other_shop"
	ErrCodeNewer        = "newer"
	ErrCodeTooOld       = "too_old"
	ErrCodeTooLarge     = "too_large"
	ErrCodeDisk         = "disk"
	ErrCodeDownload     = "download"
	ErrCodeApprovalUsed = "consumed"
	ErrCodeOther        = "other"
)

// RestoreState is what the status partial renders. It lives in memory only.
type RestoreState struct {
	Kind       string // KindRestore (default) or KindLink
	Phase      string
	BackupID   string
	TakenAt    string
	Code       string
	ApproveURL string
	ExpiresAt  string
	Done       int
	Total      int
	ErrCode    string
	Staged     string // the backup name staged, once ready
}

// Restorer brings one cloud backup back onto this till: request, wait for
// the owner's approval on my., download, decrypt, check, stage. It reuses
// db.ImportBackupFile and db.StageRestore unchanged, so every check a restore
// from a file gets (same shop, schema not newer, opened through the real boot
// path, identity stripped) applies here too, and the till keeps its own
// identity through db.ApplyPendingRestore.
type Restorer struct {
	DBPath      string
	Cloud       Cloud
	LiveStoreID func() string
	// BeforeStage runs right before staging: the safety snapshot of the
	// current data (as POST /api/backup/restore does). An error stops it.
	BeforeStage func(ctx context.Context) error
	// MaxBytes caps the decrypted file (default 4 GiB).
	MaxBytes int64
	// Keys is the till's shop-key file and StoreID the live store id; OnLinked
	// runs after a key link saved the key (backups resume).
	Keys     KeyStore
	StoreID  func() string
	OnLinked func()
	// Choice returns the owner's key-in-cloud choice (data.OffsiteKeyInCloud*);
	// with Off, a cloud-released key is never saved (ut-docs#6608).
	Choice    func() string
	PollEvery time.Duration // default 3 s
	// GiveUpAfter stops a restore whose polls keep failing (default 10 min).
	GiveUpAfter time.Duration
	// FreeDisk reports free bytes on dir (default: the real probe).
	FreeDisk func(dir string) (uint64, error)
	Sleep    func(ctx context.Context, d time.Duration)

	mu     sync.Mutex
	keyCh  chan keyEntry
	wantFP string // fingerprint a typed key must have, while PhaseAskSaved
	auditF func(action string, payload map[string]any)
	st     RestoreState
	cancel context.CancelFunc
	done   chan struct{}
}

type keyEntry struct {
	gen int
	dek []byte
}

// Errors SubmitKey returns for the page to word.
var (
	ErrNoKeyWanted = errors.New("offsitebackup: no saved key is being asked for")
	ErrWrongKey    = errors.New("offsitebackup: that key does not belong to this shop's backups")
	ErrKeyIgnored  = errors.New("offsitebackup: a key was already entered")
)

// SubmitKey hands the owner's saved key (the UTK1-... text) to a restore or a
// key link that is waiting for it. The checksum and the key's fingerprint are
// checked here, before anything is decrypted or saved.
func (r *Restorer) SubmitKey(text string) error {
	gen, dek, err := DecodeRecovery(text)
	if err != nil {
		return err
	}
	r.mu.Lock()
	ch, want, phase := r.keyCh, r.wantFP, r.st.Phase
	r.mu.Unlock()
	if phase != PhaseAskSaved || ch == nil {
		zero(dek)
		return ErrNoKeyWanted
	}
	if want == "" || Fingerprint(dek) != want {
		zero(dek)
		return ErrWrongKey
	}
	select {
	case ch <- keyEntry{gen, dek}:
		return nil
	default:
		zero(dek)
		return ErrKeyIgnored
	}
}

// askForKey waits in PhaseAskSaved for SubmitKey. ok is false when cancelled.
func (r *Restorer) askForKey(ctx context.Context, fingerprint string) (keyEntry, bool) {
	r.mu.Lock()
	r.wantFP = fingerprint
	r.mu.Unlock()
	r.update(func(s *RestoreState) { s.Phase = PhaseAskSaved })
	select {
	case e := <-r.keyCh:
		return e, true
	case <-ctx.Done():
		r.cancelled()
		return keyEntry{}, false
	}
}

// State returns a copy of the current state.
func (r *Restorer) State() RestoreState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.st.Phase == "" {
		return RestoreState{Phase: PhaseIdle}
	}
	return r.st
}

func (r *Restorer) update(f func(s *RestoreState)) {
	r.mu.Lock()
	f(&r.st)
	r.mu.Unlock()
}

// Active reports whether a restore is in progress (not finished or idle).
func (r *Restorer) Active() bool {
	switch r.State().Phase {
	case PhaseRequesting, PhaseWaiting, PhaseDownloading, PhaseChecking, PhaseAskSaved:
		return true
	}
	return false
}

// Start begins a restore of backupID in the background. audit (optional)
// records what happens, attributed by the caller to the manager who asked.
// It returns an error only when one is already running.
func (r *Restorer) Start(backupID, takenAt string, audit func(action string, payload map[string]any)) error {
	return r.begin(KindRestore, backupID, takenAt, audit)
}

// StartLink begins linking this till to the shop's backup key: the same
// code + QR approval on my., then the key is saved (K5). It shares the state,
// the status page and the poll with a restore.
func (r *Restorer) StartLink(audit func(action string, payload map[string]any)) error {
	return r.begin(KindLink, "", "", audit)
}

func (r *Restorer) begin(kind, backupID, takenAt string, audit func(action string, payload map[string]any)) error {
	r.mu.Lock()
	switch r.st.Phase {
	case PhaseRequesting, PhaseWaiting, PhaseDownloading, PhaseChecking, PhaseAskSaved:
		r.mu.Unlock()
		return errors.New("a cloud restore is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.auditF = audit
	r.keyCh = make(chan keyEntry, 1)
	r.wantFP = ""
	r.st = RestoreState{Kind: kind, Phase: PhaseRequesting, BackupID: backupID, TakenAt: takenAt}
	done := make(chan struct{})
	r.done = done
	r.mu.Unlock()
	go func() {
		defer logging.RecoverAndLog("offsitebackup.restore")
		defer close(done)
		if kind == KindLink {
			r.runLink(ctx)
			return
		}
		r.run(ctx, backupID, takenAt)
	}()
	return nil
}

// Cancel stops a restore that has not staged yet.
func (r *Restorer) Cancel() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait blocks until the running restore ends (tests).
func (r *Restorer) Wait() {
	r.mu.Lock()
	d := r.done
	r.mu.Unlock()
	if d != nil {
		<-d
	}
}

// WaitTimeout is Wait with a deadline: it reports whether the restore ended
// within d. An HTTP handler uses it so a Cancel during a long import cannot
// hold the request for minutes.
func (r *Restorer) WaitTimeout(d time.Duration) bool {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func (r *Restorer) pollEvery() time.Duration {
	if r.PollEvery > 0 {
		return r.PollEvery
	}
	return 3 * time.Second
}

// giveUpAfter is how long polls may keep failing before the restore stops.
func (r *Restorer) giveUpAfter() time.Duration {
	if r.GiveUpAfter > 0 {
		return r.GiveUpAfter
	}
	return 10 * time.Minute
}

func (r *Restorer) sleep(ctx context.Context, d time.Duration) {
	if r.Sleep != nil {
		r.Sleep(ctx, d)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (r *Restorer) audit(action string, p map[string]any) {
	r.mu.Lock()
	f := r.auditF
	r.mu.Unlock()
	if f != nil {
		f(action, p)
	}
}

func (r *Restorer) fail(code string) {
	r.update(func(s *RestoreState) { s.Phase, s.ErrCode = PhaseFailed, code })
}

func (r *Restorer) storeID() string {
	if r.StoreID != nil {
		return strings.TrimSpace(r.StoreID())
	}
	return ""
}

// wait shows the code and polls until the owner approves and the answer that
// consumes the approval arrives. ok is false when the state is final.
func (r *Restorer) wait(ctx context.Context, req RestoreRequestResponse) (RestorePoll, bool) {
	r.update(func(s *RestoreState) {
		s.Phase, s.Code, s.ApproveURL, s.ExpiresAt = PhaseWaiting, req.Code, req.ApproveURL, req.ExpiresAt
	})
	var firstFail time.Time
	for {
		poll, err := r.Cloud.PollRestore(ctx, req.RequestID)
		if err != nil {
			if ctx.Err() == nil && isTransient(err) {
				// Keep waiting for an unreachable cloud, but not forever.
				if firstFail.IsZero() {
					firstFail = time.Now()
				} else if time.Since(firstFail) > r.giveUpAfter() {
					r.fail(ErrCodeOther)
					return poll, false
				}
				r.sleep(ctx, r.pollEvery())
				if ctx.Err() != nil {
					r.cancelled()
					return poll, false
				}
				continue
			}
			r.finishErr(ctx, err, req.RequestID)
			return poll, false
		}
		switch poll.Status {
		case "denied":
			r.update(func(s *RestoreState) { s.Phase = PhaseDenied })
			return poll, false
		case "expired":
			r.update(func(s *RestoreState) { s.Phase = PhaseExpired })
			return poll, false
		}
		firstFail = time.Time{}
		if poll.DEKB64 != "" || (poll.KeyReleased != nil && !*poll.KeyReleased) {
			return poll, true
		}
		if poll.Status == "consumed" {
			// Released to an earlier call that never finished: ask again.
			r.fail(ErrCodeApprovalUsed)
			return poll, false
		}
		r.sleep(ctx, r.pollEvery())
		if ctx.Err() != nil {
			r.cancelled()
			return poll, false
		}
	}
}

// choiceOff reports the owner's "no copy in the cloud" choice.
func (r *Restorer) choiceOff() bool {
	return r.Choice != nil && strings.TrimSpace(r.Choice()) == data.OffsiteKeyInCloudOff
}

// mayKeepReleasedKey is the guard before a cloud-released key becomes this
// till's key (ut-docs#6608): never with the choice Off, and never when the
// till already holds a key of another fingerprint. The refusal is audited.
func (r *Restorer) mayKeepReleasedKey(sid string, dek []byte, gen int) bool {
	if r.choiceOff() {
		r.audit("cloud_key_link_refused", map[string]any{"why": "choice_off", "generation": gen})
		return false
	}
	if held, _, err := r.Keys.Load(sid); err == nil {
		same := Fingerprint(held) == Fingerprint(dek)
		zero(held)
		if !same {
			r.audit("cloud_key_link_refused", map[string]any{"why": "different_key_held", "generation": gen})
			return false
		}
	}
	return true
}

// runLink is the key-link flow (K5): approval, then save the released key.
func (r *Restorer) runLink(ctx context.Context) {
	if r.choiceOff() {
		// "No copy in the cloud": only the owner's saved key can link.
		r.linkFromSavedKey(ctx)
		return
	}
	req, err := r.Cloud.RequestKeyLink(ctx)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 409 && ae.Code == "no_key_in_cloud" {
		r.linkFromSavedKey(ctx)
		return
	}
	if err != nil {
		r.finishErr(ctx, err, "")
		return
	}
	r.audit("cloud_key_link_requested", map[string]any{})
	poll, ok := r.wait(ctx, req)
	if !ok {
		return
	}
	dek, derr := base64.StdEncoding.DecodeString(poll.DEKB64)
	defer zero(dek)
	sid := r.storeID()
	if derr != nil || len(dek) != KeySize || poll.DEKGeneration < 1 || sid == "" {
		r.fail(ErrCodeCorrupt)
		return
	}
	if !r.releasedKeyIsShops(ctx, dek, poll) || !r.mayKeepReleasedKey(sid, dek, poll.DEKGeneration) {
		r.fail(ErrCodeCorrupt)
		return
	}
	if err := r.Keys.Save(sid, poll.DEKGeneration, dek); err != nil {
		logging.L().Warnf("offsitebackup: could not save the backup key: %v", err)
		r.fail(ErrCodeDisk)
		return
	}
	r.update(func(s *RestoreState) { s.Phase = PhaseLinked })
	r.audit("cloud_key_linked", map[string]any{"generation": poll.DEKGeneration})
	if r.OnLinked != nil {
		r.OnLinked()
	}
}

// linkFromSavedKey: the cloud has no copy of the key, so the owner's saved key
// is the only way to link this till (E5). Nothing to approve on my.
func (r *Restorer) linkFromSavedKey(ctx context.Context) {
	st, err := r.Cloud.GetKey(ctx)
	if err != nil || st.Fingerprint == nil {
		r.finishErr(ctx, err, "")
		return
	}
	e, ok := r.askForKey(ctx, *st.Fingerprint)
	if !ok {
		return
	}
	defer zero(e.dek)
	sid := r.storeID()
	if err := r.Keys.Save(sid, e.gen, e.dek); err != nil || sid == "" {
		r.fail(ErrCodeDisk)
		return
	}
	r.update(func(s *RestoreState) { s.Phase = PhaseLinked })
	r.audit("cloud_key_linked", map[string]any{"generation": e.gen, "from": "saved_key"})
	if r.OnLinked != nil {
		r.OnLinked()
	}
}

func (r *Restorer) run(ctx context.Context, backupID, takenAt string) {
	sid := r.storeID()
	local, localGen, lerr := r.Keys.Load(sid)
	defer zero(local)
	var have *HaveKey
	if lerr == nil {
		have = &HaveKey{Generation: localGen, Fingerprint: Fingerprint(local)}
	}
	req, err := r.Cloud.RequestRestore(ctx, backupID, have)
	if err != nil {
		r.finishErr(ctx, err, "")
		return
	}
	r.audit("cloud_restore_requested", map[string]any{"backup_id": backupID, "taken_at": takenAt})
	// The owner's approval on my. is needed for every restore. The shop key
	// arrives only if this till lacks it, in the answer that consumes the
	// approval, and is kept only in memory until it is saved after staging.
	poll, ok := r.wait(ctx, req)
	if !ok {
		return
	}
	released := poll.DEKB64 != ""
	var key []byte
	var kerr error
	typed := keyEntry{}
	switch {
	case released:
		key, kerr = base64.StdEncoding.DecodeString(poll.DEKB64)
	case have != nil:
		key = append([]byte(nil), local...)
	case poll.KeyInCloud != nil && !*poll.KeyInCloud && poll.DEKFingerprint != "":
		// No copy in the cloud: the owner's saved key opens it (E5).
		var ok bool
		if typed, ok = r.askForKey(ctx, poll.DEKFingerprint); !ok {
			return
		}
		key = append([]byte(nil), typed.dek...)
		defer zero(typed.dek)
	default:
		kerr = errors.New("no key")
	}
	defer zero(key)
	if kerr == nil && len(key) == KeySize {
		id0, e := parseUUID(poll.BackupID)
		if e == nil {
			var dk []byte
			if dk, e = DeriveKey(key, id0, sid); e == nil {
				zero(key)
				key = dk
			}
		}
		kerr = e
	}
	id, ierr := parseUUID(poll.BackupID)
	// The owner approved this backup only: a different id from the cloud is a
	// substitution (an older backup, a rollback), never restored.
	if !strings.EqualFold(strings.TrimSpace(poll.BackupID), strings.TrimSpace(backupID)) {
		ierr = errors.New("the cloud answered with another backup than the approved one")
	}
	if kerr != nil || len(key) != KeySize || ierr != nil || len(poll.Parts) == 0 {
		r.fail(ErrCodeCorrupt)
		r.result(req.RequestID, "failed", ErrCodeCorrupt)
		return
	}
	dir, derr := db.BackupDir(r.DBPath)
	if derr != nil {
		r.fail(ErrCodeOther)
		r.result(req.RequestID, "failed", ErrCodeOther)
		return
	}
	// Bound what a hostile or buggy answer can make this till download.
	total, code := r.checkParts(poll.Parts)
	if code == "" {
		code = r.checkDisk(dir, total)
	}
	if code != "" {
		r.fail(code)
		r.result(req.RequestID, "failed", code)
		return
	}
	cipherPath := filepath.Join(dir, "restore-"+poll.BackupID+".utbk.part")
	defer os.Remove(cipherPath)
	r.update(func(s *RestoreState) { s.Phase, s.Total, s.Done = PhaseDownloading, len(poll.Parts), 0 })
	if code := r.download(ctx, poll, req.RequestID, cipherPath); code != "" {
		if ctx.Err() != nil {
			r.cancelled()
			return
		}
		r.fail(code)
		r.result(req.RequestID, "failed", code)
		return
	}
	r.update(func(s *RestoreState) { s.Phase = PhaseChecking })
	name, code := r.importAndStage(ctx, cipherPath, key, id)
	if code == ErrCodeCorrupt && have != nil && !released {
		// The backup may have been made under an older key generation this
		// till still knows (a new key was started later).
		for _, o := range r.Keys.Older(sid) {
			dk, derr := DeriveKey(o.DEK, id, sid)
			zero(o.DEK)
			if derr != nil {
				continue
			}
			name, code = r.importAndStage(ctx, cipherPath, dk, id)
			zero(dk)
			if code != ErrCodeCorrupt {
				break
			}
		}
	}
	if ctx.Err() != nil && code != "" {
		r.cancelled()
		return
	}
	if code != "" {
		r.fail(code)
		r.result(req.RequestID, "failed", code)
		return
	}
	if released && have == nil {
		r.keepReleasedKey(ctx, sid, poll)
	} else if typed.dek != nil && have == nil {
		// The owner's saved key checked out and opened the backup: this till
		// is now the key holder.
		_ = r.Keys.Save(sid, typed.gen, typed.dek)
	}
	r.update(func(s *RestoreState) { s.Phase, s.Staged = PhaseReady, name })
	writeNote(r.DBPath, restoreNote{BackupID: poll.BackupID, TakenAt: takenAt, StagedAt: time.Now().UTC().Format(time.RFC3339)})
	r.audit("cloud_restore_staged", map[string]any{"backup_id": poll.BackupID, "taken_at": takenAt, "file": name})
	r.result(req.RequestID, "staged", "")
}

// maxPartBytes is the cloud's part size (the uploader's 8 MiB).
const maxPartBytes = 8 << 20

func (r *Restorer) maxBytes() int64 {
	if r.MaxBytes > 0 {
		return r.MaxBytes
	}
	return 4 << 30
}

// checkParts bounds the part list: each part 1 byte..8 MiB, and the count and
// the total capped to the largest backup the till would import plus one part
// of overhead. It returns the total cipher bytes.
func (r *Restorer) checkParts(parts []PartInfo) (int64, string) {
	limit := r.maxBytes() + maxPartBytes
	if int64(len(parts)) > limit/maxPartBytes+1 {
		return 0, ErrCodeTooLarge
	}
	var total int64
	for _, p := range parts {
		if p.Bytes < 1 || p.Bytes > maxPartBytes {
			return 0, ErrCodeCorrupt
		}
		total += p.Bytes
		if total > limit {
			return 0, ErrCodeTooLarge
		}
	}
	return total, ""
}

// checkDisk needs room for the downloaded file and the imported copy (taken
// as the same size again). A platform that cannot tell skips the check.
func (r *Restorer) checkDisk(dir string, total int64) string {
	probe := r.FreeDisk
	if probe == nil {
		probe = probeFree
	}
	free, err := probe(dir)
	if err != nil {
		return ""
	}
	if uint64(total)*2 > free {
		return ErrCodeDisk
	}
	return ""
}

// keepReleasedKey saves the shop key the cloud released with this restore when
// it is the shop's current one and the till holds none: the till is now the
// key holder (K4).
func (r *Restorer) keepReleasedKey(ctx context.Context, sid string, poll RestorePoll) {
	dek, err := base64.StdEncoding.DecodeString(poll.DEKB64)
	defer zero(dek)
	if err != nil || len(dek) != KeySize || poll.DEKGeneration < 1 {
		return
	}
	if !r.releasedKeyIsShops(ctx, dek, poll) || !r.mayKeepReleasedKey(sid, dek, poll.DEKGeneration) {
		return
	}
	if err := r.Keys.Save(sid, poll.DEKGeneration, dek); err != nil {
		logging.L().Warnf("offsitebackup: could not save the backup key: %v", err)
	}
}

// releasedKeyIsShops checks a key the cloud released before it is saved: its
// fingerprint must equal the one the cloud registered for the shop's current
// generation. A corrupted or forged release must never become this till's key
// (it would then upload backups under it).
func (r *Restorer) releasedKeyIsShops(ctx context.Context, dek []byte, poll RestorePoll) bool {
	st, err := r.Cloud.GetKey(ctx)
	if err != nil || st.Generation == nil || *st.Generation != poll.DEKGeneration || st.Fingerprint == nil {
		return false
	}
	fp := Fingerprint(dek)
	if fp != *st.Fingerprint {
		return false
	}
	return poll.DEKFingerprint == "" || poll.DEKFingerprint == fp
}

func (r *Restorer) cancelled() {
	r.update(func(s *RestoreState) { s.Phase = PhaseCancelled })
}

func (r *Restorer) result(requestID, outcome, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.Cloud.Result(ctx, requestID, outcome, code); err != nil {
		logging.L().Warnf("offsitebackup: could not report the restore result: %v", err)
	}
}

func isTransient(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == 429
	}
	return true // a network error: keep waiting for the cloud
}

// finishErr maps a cloud refusal before staging to a failed state.
func (r *Restorer) finishErr(ctx context.Context, err error, requestID string) {
	if ctx.Err() != nil {
		r.cancelled()
		return
	}
	code := ErrCodeOther
	var ae *APIError
	switch {
	case errors.As(err, &ae) && ae.Status == 410:
		r.update(func(s *RestoreState) { s.Phase = PhaseExpired })
		return
	case errors.As(err, &ae) && ae.Code != "":
		code = ae.Code
	}
	r.fail(code)
	logging.L().Warnf("offsitebackup: restore stopped (%s)", code)
}

// download fetches every part to path, checking each part's SHA-256 (3
// tries per part). It returns an error code, "" on success.
func (r *Restorer) download(ctx context.Context, poll RestorePoll, requestID, path string) string {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrCodeDisk
	}
	defer out.Close()
	for i, p := range poll.Parts {
		var lastErr error
		ok := false
		for try := 0; try < 3 && !ok; try++ {
			if try > 0 {
				r.sleep(ctx, time.Duration(try)*2*time.Second)
			}
			if ctx.Err() != nil {
				return ErrCodeDownload
			}
			off, _ := out.Seek(0, io.SeekEnd)
			lastErr = r.fetchPart(ctx, poll.BackupID, p, requestID, out)
			if lastErr == nil {
				ok = true
				break
			}
			// Throw the bad part away and retry it.
			_ = out.Truncate(off)
			_, _ = out.Seek(off, io.SeekStart)
			var ae *APIError
			if errors.As(lastErr, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != 408 && ae.Status != 429 {
				break
			}
		}
		if !ok {
			logging.L().Warnf("offsitebackup: part %d could not be downloaded: %v", p.N, lastErr)
			if isNoSpace(lastErr) {
				return ErrCodeDisk
			}
			return ErrCodeDownload
		}
		r.update(func(s *RestoreState) { s.Done = i + 1 })
	}
	if err := out.Sync(); err != nil {
		return ErrCodeDisk
	}
	return ""
}

func (r *Restorer) fetchPart(ctx context.Context, backupID string, p PartInfo, requestID string, out *os.File) error {
	pctx, cancel := context.WithTimeout(ctx, PartTimeout)
	defer cancel()
	body, err := r.Cloud.GetPart(pctx, backupID, p.N, requestID)
	if err != nil {
		return err
	}
	defer body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(body, p.Bytes+1))
	if err != nil {
		return err
	}
	if n != p.Bytes || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return fmt.Errorf("part %d failed its SHA-256 check", p.N)
	}
	return nil
}

// importAndStage decrypts the downloaded file through a pipe into the
// existing import path, then stages the result. It returns the staged backup
// name, or an error code.
func (r *Restorer) importAndStage(ctx context.Context, cipherPath string, key []byte, id [16]byte) (string, string) {
	f, err := os.Open(cipherPath)
	if err != nil {
		return "", ErrCodeOther
	}
	defer f.Close()
	pr, pw := io.Pipe()
	openErr := make(chan error, 1)
	go func() {
		defer logging.RecoverAndLog("offsitebackup.restore.open")
		err := Open(pw, key, id, f)
		pw.CloseWithError(err)
		openErr <- err
	}()
	zr, zerr := gzip.NewReader(pr)
	var (
		info db.ImportedBackup
		ierr error
	)
	if zerr != nil {
		ierr = zerr
	} else {
		max := r.MaxBytes
		if max <= 0 {
			max = 4 << 30
		}
		live := ""
		if r.LiveStoreID != nil {
			live = r.LiveStoreID()
		}
		info, ierr = db.ImportBackupFile(r.DBPath, zr, max, live)
	}
	_ = pr.CloseWithError(io.ErrClosedPipe)
	oerr := <-openErr
	// A decrypt failure is the root cause even when the importer only saw a
	// broken stream.
	if oerr != nil && !errors.Is(oerr, io.ErrClosedPipe) {
		if info.Name != "" {
			discard(r.DBPath, info.Name)
		}
		if errors.Is(oerr, ErrCorrupt) {
			return "", ErrCodeCorrupt
		}
		return "", ErrCodeOther
	}
	if ierr != nil {
		switch {
		case errors.Is(ierr, db.ErrBackupOtherShop):
			return "", ErrCodeOtherShop
		case errors.Is(ierr, db.ErrBackupNewer):
			return "", ErrCodeNewer
		case errors.Is(ierr, db.ErrBackupTooOld):
			return "", ErrCodeTooOld
		case errors.Is(ierr, db.ErrBackupTooLarge):
			return "", ErrCodeTooLarge
		case errors.Is(ierr, gzip.ErrHeader), errors.Is(ierr, gzip.ErrChecksum), errors.Is(ierr, io.ErrUnexpectedEOF):
			return "", ErrCodeCorrupt
		case errors.Is(ierr, db.ErrNotTillBackup):
			return "", ErrCodeCorrupt
		}
		logging.L().Warnf("offsitebackup: import failed: %v", ierr)
		return "", ErrCodeOther
	}
	if ctx.Err() != nil {
		discard(r.DBPath, info.Name)
		return "", ErrCodeOther
	}
	if r.BeforeStage != nil {
		if err := r.BeforeStage(ctx); err != nil {
			discard(r.DBPath, info.Name)
			logging.L().Warnf("offsitebackup: the safety backup failed: %v", err)
			return "", ErrCodeOther
		}
	}
	if err := db.StageRestore(r.DBPath, info.Name); err != nil {
		logging.L().Warnf("offsitebackup: staging failed: %v", err)
		return "", ErrCodeOther
	}
	return info.Name, ""
}

// discard removes an imported copy that will not be restored.
func discard(dbPath, name string) {
	if !db.ValidBackupName(name) {
		return
	}
	if dir, err := db.BackupDir(dbPath); err == nil {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
