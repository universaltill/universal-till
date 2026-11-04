package cloudsync

// ADR-0176 (ut-docs#5366): the till's half of notices pulled on the check-in.
// Everything here runs on Start's goroutine, never on the sale path; the
// cloud's answer is re-validated (cloudnotice.Validate) and mirrored into the
// bounded cloud_notices table. Flag off (notices.enabled, default off) = no
// notices object in the request, no extra GET parameter, no extra request.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/cloudnotice"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

const (
	// noticeMaxAcks per request (ADR-0176 §4).
	noticeMaxAcks = 50
	// noticeMemFloor: below this MemAvailable the pull is skipped and `have`
	// kept (ADR-0176 §12). Same figure as the sell-screen cache's floor.
	noticeMemFloor = 64 << 20
)

// noticeMemAvailable reads MemAvailable in bytes; tests replace it.
var noticeMemAvailable = func() (uint64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:"); ok {
			fs := strings.Fields(rest)
			if len(fs) >= 1 {
				if kb, err := strconv.ParseUint(fs[0], 10, 64); err == nil {
					return kb << 10, true
				}
			}
		}
	}
	return 0, false
}

// noticeNow is the client's clock; tests pin it.
var noticeNow = time.Now

type noticeClient struct {
	repo     *data.CloudNoticeRepo
	settings *data.SettingsRepo
	lang     func() string
}

func newNoticeClient(db *sql.DB, settings *data.SettingsRepo, hooks Hooks) *noticeClient {
	if db == nil || settings == nil {
		return nil
	}
	return &noticeClient{repo: data.NewCloudNoticeRepo(db), settings: settings, lang: hooks.NoticeLang}
}

func (n *noticeClient) get(ctx context.Context, k string) string {
	v, _, _ := n.settings.Get(ctx, k)
	return v
}

// enabled: the flag is on and memory is not below the floor.
func (n *noticeClient) enabled(ctx context.Context) bool {
	if n == nil || !cloudnotice.Truthy(n.get(ctx, cloudnotice.SettingEnabled)) {
		return false
	}
	if avail, ok := noticeMemAvailable(); ok && avail < noticeMemFloor {
		return false
	}
	return true
}

func (n *noticeClient) wireLang() string {
	l := ""
	if n.lang != nil {
		l = n.lang()
	}
	return cloudnotice.WireLang(l)
}

// rev is the opaque revision stored from the last pull ("" when the language
// changed since: ADR-0176 §4 resets `have` and so the revision).
func (n *noticeClient) rev(ctx context.Context) string {
	if n.get(ctx, cloudnotice.SettingLang) != n.wireLang() {
		return ""
	}
	return n.get(ctx, cloudnotice.SettingRev)
}

func (n *noticeClient) acksWaiting(ctx context.Context) bool {
	acks, err := n.repo.PendingAcks(ctx, 1)
	return err == nil && len(acks) > 0
}

// attach adds the notices object to the request when the feature is on, and
// registers the response handler.
func (n *noticeClient) attach(ctx context.Context, req *syncRequest) {
	if !n.enabled(ctx) {
		return
	}
	now := noticeNow()
	if _, err := n.repo.PurgeExpired(ctx, now); err != nil {
		logging.L().Warnf("cloudsync: notices purge: %v", err)
	}
	lang := n.wireLang()
	have, rev := "", ""
	if n.get(ctx, cloudnotice.SettingLang) == lang {
		pairs, err := n.repo.Pairs(ctx)
		if err != nil {
			logging.L().Warnf("cloudsync: notices have: %v", err)
			return
		}
		have, rev = cloudnotice.Have(pairs), n.get(ctx, cloudnotice.SettingRev)
	}
	acks, err := n.repo.PendingAcks(ctx, noticeMaxAcks)
	if err != nil {
		logging.L().Warnf("cloudsync: notices acks: %v", err)
		acks = nil
	}
	obj := map[string]any{"lang": lang, "have": have, "rev": rev}
	if len(acks) > 0 {
		obj["acks"] = acks
	}
	req.notices = obj
	ackIDs := make([]string, len(acks))
	for i, a := range acks {
		ackIDs[i] = a.ID
	}
	req.onNotices = func(raw json.RawMessage) { n.apply(ctx, raw, lang, ackIDs) }
}

// apply handles the response's notices block. Best-effort: it logs and never
// fails the tick. A block that does not parse changes nothing.
func (n *noticeClient) apply(ctx context.Context, raw json.RawMessage, lang string, ackIDs []string) {
	var blk struct {
		Unchanged bool               `json:"unchanged"`
		Rev       string             `json:"rev"`
		More      bool               `json:"more"`
		Items     []cloudnotice.Item `json:"items"`
	}
	// Bounded: ADR-0176 §4 says a response is at most 80 KiB.
	if len(raw) > 128<<10 || json.Unmarshal(raw, &blk) != nil {
		logging.L().Warnf("cloudsync: ignoring malformed notices block")
		return
	}
	now := noticeNow()
	// The cloud has the acks now (the block exists, so it knows notices).
	if len(ackIDs) > 0 {
		if err := n.repo.MarkReported(ctx, ackIDs, now); err != nil {
			logging.L().Warnf("cloudsync: notices acks not marked: %v", err)
		}
	}
	if blk.Unchanged {
		n.remember(ctx, blk.Rev, lang)
		return
	}
	if len(blk.Items) > cloudnotice.MaxItems {
		blk.Items = blk.Items[:cloudnotice.MaxItems]
	}
	var keep []data.CloudNotice
	for _, it := range blk.Items {
		if err := cloudnotice.Validate(it, now); err != nil {
			logging.L().Warnf("cloudsync: dropped notice %s: %v", cloudnotice.Describe(it), err)
			continue
		}
		picked, tx, ok := cloudnotice.PickText(it.Texts, lang)
		if !ok {
			continue
		}
		link := ""
		if it.Link != nil {
			if b, err := json.Marshal(it.Link); err == nil {
				link = string(b)
			}
		}
		keep = append(keep, data.CloudNotice{
			ID: it.ID, Rev: it.Rev, SenderKind: it.Sender.Kind, SenderName: it.Sender.Name,
			Severity: it.Severity, Lang: picked, Title: tx.Title, Body: tx.Body, LinkJSON: link,
			StartsAt: it.StartsAt, ExpiresAt: it.ExpiresAt, CreatedAt: it.CreatedAt,
		})
	}
	if err := n.repo.Mirror(ctx, keep, now); err != nil {
		logging.L().Warnf("cloudsync: notices not stored (will retry next pull): %v", err)
		return
	}
	n.remember(ctx, blk.Rev, lang)
}

func (n *noticeClient) remember(ctx context.Context, rev, lang string) {
	if err := n.settings.SetMany(ctx, map[string]string{cloudnotice.SettingRev: rev, cloudnotice.SettingLang: lang}); err != nil {
		logging.L().Warnf("cloudsync: notices cursor not saved: %v", err)
	}
}
