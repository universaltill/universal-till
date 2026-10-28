package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// A joined till re-joining a main till keeps its unsent journal (ut-docs#5058
// UT-5; ADR-0011 D3; design §5 "N3").
//
// A join replaces this till's whole database with the main till's copy
// (completeJoin → db.StageRestoreFromReader). Until now a joined till whose
// main till died or was replaced, and which then joined the new main till,
// lost from the shop everything its push loop had not yet delivered: queued
// sales, no-sale opens and cleared baskets (they stayed only in the
// pre-restore copy in its backups folder). Now, right after the new main
// till has enrolled this till and BEFORE its copy is downloaded, the till
// sends that journal to it over the new enrolment, through the same
// endpoints and the same idempotent apply (by sale id) the push loop uses.
// The copy this till then stages already holds those sales, under their
// own ids and receipt numbers, so they survive the wipe. Anything it cannot
// send fails the join: the enrolment is withdrawn and nothing on this till
// changes. A sale rung while the join runs is sent by a last pass after the
// copy is staged, and again by replayStagedJoin right before the staged copy
// replaces this till's database (the restart button, and the next start).
//
// Only a till that is joined to a main till (sync.primary_url set) does
// this: the journal past its push cursor is by definition not on any main
// till. A standalone till's sales are its own history; its first join keeps
// today's explicit "replace this till's sales" confirm.
//
// Day closes are not journaled to the main till: each till uploads its own
// Z chain to the cloud (retention mode cloud/both). A join while some are
// not acknowledged by the cloud yet is refused before anything is enrolled
// (rejoinUnsentDayCloses), so they are never wiped unsent.

// rejoinSaleBatch is how many sales one replay request carries: the push
// loop's batch, well inside /api/sync/sales's limit of 100.
const rejoinSaleBatch = 50

// rejoinReplay is what a replay sent.
type rejoinReplay struct {
	Sales, NoSales, ClearedBaskets int
}

// isJoinedTill reports whether this till is joined to a main till.
func isJoinedTill(ctx context.Context, d *common.Deps) bool {
	v, _, _ := d.Settings.Get(ctx, "sync.primary_url")
	return strings.TrimSpace(v) != ""
}

// rejoinUnsentDayCloses counts this till's day closes the cloud has not
// acknowledged, when its retention mode uploads them (else 0).
func rejoinUnsentDayCloses(ctx context.Context, d *common.Deps) (int, error) {
	mode, _, _ := d.Settings.Get(ctx, data.ReportRetentionModeKey)
	if !data.UploadsReportArchives(strings.TrimSpace(mode)) {
		return 0, nil
	}
	return data.NewPOSRepo(d.Db).CountUnackedReportArchives(ctx)
}

// replayUnsentJournal sends this till's journal past its push cursors to the
// main till at base, authenticated with the bearer that main till has just
// issued. sinceSale overrides the sales cursor ("" = the stored push
// cursor). It stops at the first refusal; what was sent before stays sent
// (the apply is idempotent, so a retried join sends it again harmlessly).
func replayUnsentJournal(ctx context.Context, d *common.Deps, client *http.Client, base, bearer, sinceSale string) (rejoinReplay, error) {
	var out rejoinReplay
	repo := data.NewPOSRepo(d.Db)
	cursor := sinceSale
	if cursor == "" {
		cursor, _, _ = d.Settings.Get(ctx, "sync.push_cursor")
	}
	afterAt, afterID := parseSalesPushCursor(cursor)
	// Every unsent receipt at once, then batches of the list: paging by
	// created_at alone would skip sales sharing an instant across a batch
	// edge.
	receipts, err := repo.LocalSalesSince(ctx, afterAt, afterID, 1<<30)
	if err != nil {
		return out, err
	}
	for start := 0; start < len(receipts); start += rejoinSaleBatch {
		end := min(start+rejoinSaleBatch, len(receipts))
		batch := make([]journalSale, 0, end-start)
		for _, rn := range receipts[start:end] {
			j, found, err := buildJournal(ctx, repo, rn)
			if err != nil {
				return out, fmt.Errorf("read sale %s: %w", rn, err)
			}
			if !found {
				continue
			}
			batch = append(batch, j)
		}
		if err := postJournal(ctx, client, base+"/api/sync/sales", bearer, batch); err != nil {
			return out, err
		}
		out.Sales += len(batch)
	}

	// No-sale opens and cleared baskets page by (created_at, id), exactly
	// like their push steps.
	afterAt, afterID = parseNoSalePushCursor(settingTrim(ctx, d, noSalePushCursorKey))
	for {
		events, err := repo.LocalNoSaleEventsSince(ctx, afterAt, afterID, noSalePushBatch)
		if err != nil {
			return out, err
		}
		if len(events) == 0 {
			break
		}
		batch := make([]journalNoSale, 0, len(events))
		for _, e := range events {
			batch = append(batch, journalNoSale{ID: e.ID, CreatedAt: e.CreatedAt, RegisterID: e.RegisterID,
				ActorID: e.ActorID, ApproverID: e.ApproverID, Reason: e.Reason})
		}
		if err := postJournal(ctx, client, base+"/api/sync/no-sales", bearer, batch); err != nil {
			return out, err
		}
		out.NoSales += len(batch)
		last := events[len(events)-1]
		afterAt, afterID = last.CreatedAt, last.ID
	}
	afterAt, afterID = parseNoSalePushCursor(settingTrim(ctx, d, clearedBasketPushCursorKey))
	for {
		clears, err := repo.LocalClearedBasketsSince(ctx, afterAt, afterID, clearedBasketPushBatch)
		if err != nil {
			return out, err
		}
		if len(clears) == 0 {
			break
		}
		batch := make([]journalClearedBasket, 0, len(clears))
		for _, e := range clears {
			batch = append(batch, journalClearedBasket{ID: e.ID, CreatedAt: e.CreatedAt, RegisterID: e.RegisterID,
				ActorID: e.ActorID, LineCount: e.LineCount, TotalMinor: e.Total.Minor()})
		}
		if err := postJournal(ctx, client, base+"/api/sync/cleared-baskets", bearer, batch); err != nil {
			return out, err
		}
		out.ClearedBaskets += len(batch)
		last := clears[len(clears)-1]
		afterAt, afterID = last.CreatedAt, last.ID
	}
	return out, nil
}

func settingTrim(ctx context.Context, d *common.Deps, key string) string {
	v, _, _ := d.Settings.Get(ctx, key)
	return strings.TrimSpace(v)
}

// postJournal POSTs one journal batch and requires a 200 that quarantined
// no sale: anything else — a 422 apply failure, a 409 from a replaced main
// till, a 404 from a main till too old for the route, an entry the main till
// could only hold for review (sync quarantine: an item, voucher, user or
// original sale it does not know) — fails the replay, so the join keeps this
// till's data instead of wiping a sale that exists nowhere as a sale.
func postJournal(ctx context.Context, client *http.Client, url, bearer string, batch any) error {
	strictSales := strings.HasSuffix(url, "/api/sync/sales")
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	var res struct {
		Data struct {
			Quarantined int `json:"quarantined"`
			Rejected    int `json:"rejected"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res); err != nil {
		return fmt.Errorf("%s: unreadable answer: %w", url, err)
	}
	if strictSales && res.Data.Quarantined > 0 {
		return fmt.Errorf("%s held %d sale(s) for review", url, res.Data.Quarantined)
	}
	if res.Data.Rejected > 0 {
		// An invalid no-sale open or cleared basket is refused the same way
		// by every main till, so it would block a re-join for good; it is
		// no sale, so it is logged and the join goes on.
		logging.L().Warnf("re-join: %s refused %d invalid entries", url, res.Data.Rejected)
	}
	return nil
}

// countUnsentSales is the number of this till's sales past its push cursor
// (the refusal message names it).
func countUnsentSales(ctx context.Context, d *common.Deps) int {
	n, err := countQueuedSales(ctx, data.NewPOSRepo(d.Db), settingTrim(ctx, d, "sync.push_cursor"))
	if err != nil {
		logging.L().Warnf("re-join: count unsent sales: %v", err)
	}
	return n
}

// replayStagedJoin is the last chance for a sale rung after a join was
// staged (ut-docs#5058 UT-5): the restart that applies the staged copy may
// come minutes or hours later (the manager's Restart now, or on Windows the
// next start). It sends this till's journal past its push cursor to the main
// till named in the staged identity, with the bearer that main till issued.
// Nothing staged → nil. The apply on the main till is idempotent, so what an
// earlier pass sent is skipped there.
func replayStagedJoin(ctx context.Context, d *common.Deps, dbPath string) (rejoinReplay, error) {
	if !db.PendingRestore(dbPath) {
		return rejoinReplay{}, nil
	}
	raw, err := os.ReadFile(db.ReplicaIdentityPath(dbPath))
	if err != nil {
		if os.IsNotExist(err) {
			return rejoinReplay{}, nil // a backup restore, not a join
		}
		return rejoinReplay{}, err
	}
	var id db.ReplicaIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return rejoinReplay{}, fmt.Errorf("read the staged join: %w", err)
	}
	base := strings.TrimSuffix(strings.TrimSpace(id.PrimaryURL), "/")
	if base == "" || id.Bearer == "" {
		return rejoinReplay{}, nil
	}
	client := netaccess.NewClient(60 * time.Second)
	if id.PrimaryCertPin != "" {
		tlsBase, ok := lantls.HTTPSBase(base)
		if !ok {
			return rejoinReplay{}, fmt.Errorf("pinned main till has no LAN address: %s", base)
		}
		base, client = tlsBase, lantls.NewPinnedClient(60*time.Second, id.PrimaryCertPin).Client
	}
	return replayUnsentJournal(ctx, d, client, base, id.Bearer, "")
}

// ReplayStagedJoinAtStart runs replayStagedJoin on the database about to be
// replaced, from app start before the staged copy is applied. Best effort and
// bounded: the start never waits long on the network. A failure is an Error
// (the Problems panel): those sales are then only in the pre-restore copy in
// the backups folder.
func ReplayStagedJoinAtStart(ctx context.Context, sqlDB *sql.DB, dbPath string) {
	d := &common.Deps{Db: sqlDB, Settings: settings.NewStore(sqlDB)}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n := countUnsentSales(ctx, d)
	sent, err := replayStagedJoin(ctx, d, dbPath)
	if err != nil {
		logging.L().Errorf("join: %d sale(s) of this till could not be sent to its new main till before the join was applied (%v); they are kept in the pre-restore copy in the backups folder (ut-docs#5058)", n, err)
		return
	}
	if sent.Sales+sent.NoSales+sent.ClearedBaskets > 0 {
		logging.L().Infof("join: sent %d sale(s), %d no-sale open(s), %d cleared basket(s) to the new main till before applying the join", sent.Sales, sent.NoSales, sent.ClearedBaskets)
	}
}
