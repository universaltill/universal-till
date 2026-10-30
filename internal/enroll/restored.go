package enroll

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// Restored-install detection (ut-docs#5157).
//
// marketplace.device_id and marketplace.token are settings rows inside the
// backed-up data folder, so an iCloud restore onto a second iPhone clones
// the till's cloud identity: two tills under one device id and token. iOS
// excludes till/secrets (and till/tls) from backup, so a marker file kept
// under secrets/ survives only on the install that wrote it. The DB holds
// the same id in till_identity.install_id; an enrolled DB whose marker is
// missing or different is therefore a restore, and the cloud identity is
// reset exactly the way a fresh pairing resets it (resetIdentityForPair,
// ADR-0116 D5): the till keeps selling and shows a "connect again" chip.
//
// Upgrade safety: a DB with no install_id row (an install from before this
// shipped, or a just-joined replica whose snapshot dropped till_identity.*)
// is never treated as restored; it adopts the marker instead. Whenever the
// check is unsure (unreadable marker, DB read error) it changes nothing.
const (
	keyInstallID  = "till_identity.install_id"
	keyRestoredAt = "till_identity.restored_at"
	// keyTillCloudID mirrors data.TillIdentityCloudIDSettingsKey (a test pins
	// the equality): the id the cloud knows this till's device row by.
	keyTillCloudID = "till_identity.cloud_id"

	keySyncReceiptPrefix = "sync.receipt_prefix"
	// keySyncRestoredSince is when a restored replica copy was reset
	// (ut-docs#5177). The "sync." prefix keeps it per-till: admin pulls never
	// overwrite it.
	keySyncRestoredSince = "sync.restored_since"
)

// installMarkerRel is the marker's place under the data root. It must start
// with "secrets": scripts/ci/ios-secrets-backup_test.sh pins that, because
// the iOS app excludes only that folder (and tls) from backup.
var installMarkerRel = []string{"secrets", "install-id"}

// provisionalPrefix is the receipt prefix clearClonedIdentity gives a restored
// replica (ut-docs#5162).
var provisionalPrefix = regexp.MustCompile(`^R[0-9a-f]{4}-$`)

// RestoredCopyMarks reports what marks this till as a restored replica copy
// (ut-docs#5177): its provisional receipt prefix (only a valid R<4 hex>- one,
// else "") and the instant it was restored (sync.restored_since, trimmed).
// Both empty means this is not a restored copy. A read error reads as empty.
func RestoredCopyMarks(ctx context.Context, kv Settings) (prefix, since string) {
	if kv == nil {
		return "", ""
	}
	if v, _, err := kv.Get(ctx, keySyncReceiptPrefix); err == nil && provisionalPrefix.MatchString(strings.TrimSpace(v)) {
		prefix = strings.TrimSpace(v)
	}
	if v, _, err := kv.Get(ctx, keySyncRestoredSince); err == nil {
		since = strings.TrimSpace(v)
	}
	return prefix, since
}

// RestoredState reports whether the till's cloud identity was reset by a
// restore and has not been re-established since. A read error reads as not
// restored.
func RestoredState(ctx context.Context, kv Settings) bool {
	if kv == nil {
		return false
	}
	v, _, err := kv.Get(ctx, keyRestoredAt)
	return err == nil && strings.TrimSpace(v) != ""
}

// CheckRestoredInstall compares the DB's install id with the marker file and
// resets the cloud identity when they show a restore. It reports whether it
// reset. Call once, synchronously, at boot: after the database and settings
// store are ready, before settings.LoadRuntimeConfig and Init, so both read
// the post-reset rows. cfg is still the environment-only config then, so
// its marketplace fields are exactly the operator's pins. Never blocks boot
// and never wipes when unsure.
func CheckRestoredInstall(ctx context.Context, cfg *config.Config, kv Settings, newest NewestJournalEntry) (reset bool) {
	log := logging.L()
	dbID, _, err := kv.Get(ctx, keyInstallID)
	if err != nil {
		log.Warnf("enrolment: restore check: read install id: %v; leaving the identity alone (ut-docs#5157)", err)
		return false
	}
	dbID = strings.TrimSpace(dbID)
	path := paths.Data(installMarkerRel...)
	fileID := ""
	if b, err := os.ReadFile(path); err == nil {
		fileID = strings.TrimSpace(string(b))
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Warnf("enrolment: restore check: read install marker: %v; leaving the identity alone (ut-docs#5157)", err)
		return false
	}

	if dbID == "" {
		// Fresh install, upgrade, or just joined: adopt, wipe nothing.
		if fileID == "" {
			fileID = uuid.NewString()
			if err := writeInstallMarker(path, fileID); err != nil {
				log.Warnf("enrolment: restore check: write install marker: %v (ut-docs#5157)", err)
				return false
			}
		}
		if err := kv.Set(ctx, keyInstallID, fileID); err != nil {
			log.Warnf("enrolment: restore check: persist install id: %v", err)
		}
		return false
	}
	if dbID == fileID {
		return false
	}

	// The DB came from somewhere else: this install has no marker, or a
	// different one.
	newID := uuid.NewString()
	if cfg != nil && (cfg.Marketplace.DeviceID != "" || cfg.Marketplace.ClientID != "" || cfg.Marketplace.MerchantToken != "") {
		// The operator pinned the identity in the environment, which wins
		// over the DB anyway; just re-mark this install.
		clearClonedIdentity(ctx, kv, isReplica(ctx, kv), newID, newest)
		return remark(ctx, kv, path, newID, false)
	}
	get := func(key string) string {
		v, _, _ := kv.Get(ctx, key)
		return strings.TrimSpace(v)
	}
	// A restored flag already set means an earlier boot reset and then died
	// before re-marking: the identity is empty now, but the flag must stay.
	// A replica that was never cloud-registered still held a LAN bearer: the
	// copy must say it needs joining again (ut-docs#5162).
	replica := isReplica(ctx, kv)
	held := get(keyToken) != "" || get(keyDeviceRegistered) != "" || get(keyPairedAt) != "" || RestoredState(ctx, kv) ||
		(replica && get(keySyncBearer) != "")
	// Needs sync.till_id (it marks the new device id as minted for it), so it
	// runs before the LAN identity is cleared.
	resetIdentityForPair(ctx, kv, replica, "till-"+uuid.NewString())
	if held {
		if err := kv.Set(ctx, keyRestoredAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Warnf("enrolment: restore check: record restored state: %v", err)
		}
	}
	// After the restored flag: a crash part-way repeats the whole check on the
	// next boot, which finds the flag and runs this again.
	clearClonedIdentity(ctx, kv, replica, newID, newest)
	log.Warnf("enrolment: this till's data was restored from a backup onto a new install; its cloud identity was reset and it must connect again (ut-docs#5157)")
	return remark(ctx, kv, path, newID, true)
}

// clearClonedIdentity drops the per-till identity a restored copy shares with
// the original (ut-docs#5162), the LAN counterpart of resetIdentityForPair.
// Always the cloud device row id (till_identity.cloud_id, carried over from a
// promotion); on a replica also the LAN bearer, till id and the pinned
// main-till certificates, so the copy cannot sync as the original. It keeps
// sync.primary_url (the till stays a joined till, so joining again keeps its
// sales), the till name/role and the push cursors. The receipt prefix becomes
// a provisional R<4 hex>- one: the original's T<n>- would mint the same
// receipt numbers, and sales.receipt_no is UNIQUE at the main till; a join
// grants a new T<n>- because that is not a valid T<n>- prefix.
func clearClonedIdentity(ctx context.Context, kv Settings, replica bool, installID string, newest NewestJournalEntry) {
	set := func(key, value string) {
		if err := kv.Set(ctx, key, value); err != nil {
			logging.L().Warnf("enrolment: restore check: clear %s: %v (ut-docs#5162)", key, err)
		}
	}
	set(keyTillCloudID, "")
	if !replica {
		return
	}
	// The bearer first: it is what authenticates the copy to the main till.
	set(keySyncBearer, "")
	set(keySyncTillID, "")
	set(discovery.PrimaryCertPinSettingKey, "")
	set(discovery.PrimaryCertNextPinSettingKey, "")
	// When the copy was restored (ut-docs#5177): its entries from now on carry
	// the ORIGINAL till's register id, which a re-join rewrites. Kept like the
	// prefix below, so a retried reset never moves it. Never earlier than just
	// after the newest pre-restore journal entry (ut-docs#5185).
	if cur, _, err := kv.Get(ctx, keySyncRestoredSince); err != nil || strings.TrimSpace(cur) == "" {
		set(keySyncRestoredSince, restoredSince(ctx, newest).Format(time.RFC3339))
	}
	// A provisional prefix already set (an earlier boot reset, then failed
	// to re-mark) is kept, so a retried reset never starts a second series.
	if cur, _, err := kv.Get(ctx, keySyncReceiptPrefix); err == nil && provisionalPrefix.MatchString(strings.TrimSpace(cur)) {
		return
	}
	hex := strings.ReplaceAll(installID, "-", "")
	if len(hex) > 4 {
		hex = hex[:4]
	}
	set(keySyncReceiptPrefix, "R"+hex+"-")
}

// NewestJournalEntry returns the greatest created_at (RFC 3339) among this
// till's own no-sale opens and cleared baskets, "" when there are none. The
// boot code passes it in so enroll keeps no dependency on internal/data
// (ut-docs#5185).
type NewestJournalEntry func(ctx context.Context) (string, error)

// restoredSince is the instant a restored replica copy's own entries start:
// now, or one second after the newest pre-restore journal entry when that is
// later. Rows made before the clone were stamped by the ORIGINAL till's
// clock, which may run ahead of this one; the +1s matters because the replay
// counts created_at == since as made by the copy. The trade-off: when the
// original's clock ran ahead by more than the gap between backup and
// restore, the copy's own no-sale opens and cleared baskets in that window
// keep the original's register at re-join — the original's entries are
// never moved. A lookup error or an unreadable value falls back to now and
// never blocks boot (ut-docs#5185).
func restoredSince(ctx context.Context, newest NewestJournalEntry) time.Time {
	now := time.Now().UTC()
	if newest == nil {
		return now
	}
	v, err := newest(ctx)
	if err != nil {
		logging.L().Warnf("enrolment: restore check: newest journal entry: %v; using the restore time (ut-docs#5185)", err)
		return now
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return now
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		logging.L().Warnf("enrolment: restore check: newest journal entry %q unreadable: %v; using the restore time (ut-docs#5185)", v, err)
		return now
	}
	if after := t.UTC().Truncate(time.Second).Add(time.Second); after.After(now) {
		if after.Sub(now) > 24*time.Hour {
			// A clock set far ahead once: every entry the copy makes until
			// then keeps the original's register. Said, not hidden.
			logging.L().Warnf("enrolment: restore check: newest journal entry %s is %s ahead of this clock; entries made before then keep the original till's register at re-join (ut-docs#5185)", v, after.Sub(now).Round(time.Minute))
		}
		return after
	}
	return now
}

// remark writes the new marker, then the DB row last, so a crash between
// the two repeats the whole check rather than hiding the restore.
func remark(ctx context.Context, kv Settings, path, id string, reset bool) bool {
	if err := writeInstallMarker(path, id); err != nil {
		logging.L().Warnf("enrolment: restore check: write install marker: %v (ut-docs#5157)", err)
		return reset
	}
	if err := kv.Set(ctx, keyInstallID, id); err != nil {
		logging.L().Warnf("enrolment: restore check: persist install id: %v", err)
	}
	return reset
}

// writeInstallMarker writes id atomically (temp file + rename) with 0600
// under a 0700 directory.
func writeInstallMarker(path, id string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".install-id-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(id + "\n")
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return errors.Join(werr, cerr)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
