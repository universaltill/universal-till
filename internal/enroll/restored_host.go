package enroll

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
)

// Copied-host detection (ut-docs#6907). A whole data folder copied to other
// hardware (folder, SD card, file copy) carries secrets/install-id with it,
// so the install-id check of ut-docs#5157 sees nothing. The DB also holds the
// id of the machine it last booted on (as an HMAC tag, never the raw id); a
// different machine owes the #6294 number jump.
//
// Decision A1: jump only. No cloud-identity reset and no restored_at: a false
// positive (OS reinstall, Mac migration) costs a number gap and nothing more;
// a true copy keeps the original's device credential, as it always did.
// Decision A2: the series prefix is kept (ADR-0166 §A). An original that keeps
// selling in parallel reaches the copy's floor after about 10 000 receipts;
// closing that needs a new series and an ADR amendment (separate card).
// A replica gets the existing provisional-prefix treatment and no jump.
const keyHostTag = "till_identity.host_tag" // per till: TillCloudIdentityPrefixes covers "till_identity."

// restoreHostTagKey is this use's own HMAC key, not lantls'.
var restoreHostTagKey = []byte("universal-till restore host id v1")

// readHostID is the machine's id source; tests swap it to be another machine.
var readHostID = lantls.ReadHostID

// copiedHostJumpToken is "copied-host:<install id>:<16 hex of the new tag>".
// A retry of one detection computes the same token (same install id, same
// tag), and a later move (H1 to H2 to H1) a different one, since the tag
// differs. The install id is never re-marked on this path, so it stays put.
// installID may be "": the tag part alone still tells moves apart.
func copiedHostJumpToken(installID, tag string) string {
	_, h, _ := strings.Cut(tag, ":")
	if len(h) > 16 {
		h = h[:16]
	}
	return "copied-host:" + installID + ":" + h
}

// currentHostTag is this boot's tag; off when UT_HOST_ID=off. "" when the
// host id is unknown.
func currentHostTag(cfg *config.Config) (tag string, off bool) {
	if cfg != nil {
		pin := strings.TrimSpace(cfg.HostID)
		if strings.EqualFold(pin, "off") {
			return "", true
		}
		if pin != "" {
			return lantls.HostTag(restoreHostTagKey, "pinned", pin), false
		}
	}
	src, raw := readHostID()
	return lantls.HostTag(restoreHostTagKey, src, raw), false
}

func tagSource(tag string) string {
	src, _, _ := strings.Cut(tag, ":")
	return src
}

func checkHostMove(ctx context.Context, cfg *config.Config, kv Settings, newest NewestJournalEntry, owe OweNumberJump) {
	log := logging.L()
	cur, off := currentHostTag(cfg)
	if off || cur == "" {
		return
	}
	stored, _, err := kv.Get(ctx, keyHostTag)
	if err != nil {
		log.Warnf("enrolment: host check: read host tag: %v; leaving the numbers alone (ut-docs#6907)", err)
		return
	}
	stored = strings.TrimSpace(stored)
	record := func() bool {
		if err := kv.Set(ctx, keyHostTag, cur); err != nil {
			log.Warnf("enrolment: host check: record host tag: %v (ut-docs#6907)", err)
			return false
		}
		return true
	}
	if stored == "" {
		record()
		return
	}
	if stored == cur {
		return
	}
	if ss, cs := tagSource(stored), tagSource(cur); ss != cs && (ss == "pinned" || cs == "pinned") {
		// The operator set or removed the pin on purpose: never a gap.
		if record() {
			log.Infof("enrolment: host check: host id pin changed; recorded without a number jump (ut-docs#6907)")
		}
		return
	}

	// Moved. The install id only names the token; an unreadable one is a
	// failed read, not a verdict, so keep the old tag and ask again next boot.
	dbID, _, err := kv.Get(ctx, keyInstallID)
	if err != nil {
		log.Warnf("enrolment: host check: read install id: %v; leaving the numbers alone, will re-check (ut-docs#6907)", err)
		return
	}
	dbID = strings.TrimSpace(dbID)
	replica := isReplica(ctx, kv)
	if replica {
		// Only the prefix hex comes from newID; nothing is re-marked.
		clearClonedIdentity(ctx, kv, true, uuid.NewString(), newest)
	} else if owe != nil {
		if err := owe(copiedHostJumpToken(dbID, cur)); err != nil {
			log.Errorf("enrolment: host check: record the owed receipt/Z number jump: %v; re-checking on the next boot (ut-docs#6907)", err)
			return
		}
	}
	// The install id and secrets/install-id stay as they are: re-marking
	// would trip the #5157 restore detector on a half-done write (a second
	// jump and a wiped cloud identity).
	if !record() {
		return
	}
	if replica {
		log.Warnf("enrolment: this till's data folder now runs on different hardware (copied with secrets/); join the main till again (ut-docs#6907)")
		return
	}
	log.Warnf("enrolment: this till's data folder now runs on different hardware (copied with secrets/); receipt and Z numbers move ahead (ut-docs#6907)")
}
