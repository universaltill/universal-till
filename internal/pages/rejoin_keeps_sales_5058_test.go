package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5058 UT-5: an additional till whose main till died (or was
// replaced) re-joins the shop's NEW main till. The join replaces its whole
// database with the new main till's copy, so before that copy is taken the
// till sends the new main till its journal that never reached a main till
// (queued sales, no-sale opens, cleared baskets) over its new enrolment. The
// sales keep their own ids and receipt numbers; the new main till's copy
// then holds them, so they survive the wipe. If they cannot be sent, the
// join is refused and nothing on this till changes. Real migrated temp DBs
// on both sides, the real join route and the real main-till endpoints.

type rejoinShop struct {
	main     *common.Deps
	mainMux  *http.ServeMux
	srv      *httptest.Server
	till     *common.Deps
	tillPath string
	tillMux  *http.ServeMux
}

// newRejoinShop: a new main till serving enrolment, snapshot and the three
// journals, and an additional till still joined to its dead old main till
// (sync.primary_url points nowhere) with a push cursor.
func newRejoinShop(t *testing.T) *rejoinShop {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	s := &rejoinShop{}
	s.main, _ = newSyncDepsWithPath(t, "newmain.db")
	s.mainMux = http.NewServeMux()
	registerSyncAPI(s.mainMux, s.main)
	registerSyncSales(s.mainMux, s.main)
	registerSyncNoSales(s.mainMux, s.main)
	registerSyncClearedBaskets(s.mainMux, s.main)
	s.srv = httptest.NewServer(s.mainMux)
	t.Cleanup(s.srv.Close)

	s.till, s.tillPath = newSyncDepsWithPath(t, "till2.db")
	s.tillMux = http.NewServeMux()
	registerSyncAPI(s.tillMux, s.till)
	ctx := context.Background()
	for k, v := range map[string]string{
		"sync.primary_url":    "http://127.0.0.1:1", // the dead old main till
		"sync.bearer":         "old-bearer",
		"sync.receipt_prefix": "T2-",
		"sync.push_cursor":    "2026-10-26T08:00:00Z",
	} {
		if err := s.till.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *rejoinShop) join(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return postSyncJoin(t, s.tillMux, "/api/sync/join", url.Values{
		"code": {issueEnrolCode(t, s.mainMux, s.srv.URL)}, "name": {"Till 2"}, "replace_local_data": {"1"},
	})
}

func receiptsOn(t *testing.T, db *sql.DB, ids ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range ids {
		var rn string
		if err := db.QueryRow(`SELECT receipt_no FROM sales WHERE id = ?`, id).Scan(&rn); err == nil {
			out[id] = rn
		}
	}
	return out
}

func TestRejoinSendsUnsentSalesToTheNewMainTillBeforeTheWipe(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	// Pushed to the old main till before it died (before the cursor), and
	// two the old main till never received.
	seedLocalSale(t, s.till, "sale-sent", "T2-000000001", "2026-10-26T07:00:00Z")
	seedLocalSale(t, s.till, "sale-unsent-1", "T2-000000002", "2026-10-26T09:00:00Z")
	seedLocalSale(t, s.till, "sale-unsent-2", "T2-000000003", "2026-10-26T09:05:00Z")
	tillRepo := data.NewPOSRepo(s.till.Db)
	if _, err := tillRepo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{ID: "ns-unsent", CreatedAt: "2026-10-26T09:10:00Z", RegisterID: "reg-1", ActorID: "u1", Reason: "change"}); err != nil {
		t.Fatal(err)
	}

	rec := s.join(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-join: %d %s", rec.Code, rec.Body.String())
	}

	// The new main till has both unsent sales, under their own receipt
	// numbers; the one the old main till already had is not invented here.
	got := receiptsOn(t, s.main.Db, "sale-sent", "sale-unsent-1", "sale-unsent-2")
	if got["sale-unsent-1"] != "T2-000000002" || got["sale-unsent-2"] != "T2-000000003" {
		t.Fatalf("unsent sales on the new main till = %v, want both with their original receipt numbers", got)
	}
	if _, ok := got["sale-sent"]; ok {
		t.Fatal("a sale the old main till had already received was sent again")
	}
	var nsMain int
	if err := s.main.Db.QueryRow(`SELECT COUNT(*) FROM no_sale_events WHERE id = 'ns-unsent'`).Scan(&nsMain); err != nil || nsMain != 1 {
		t.Fatalf("unsent no-sale open on the new main till: n=%d err=%v", nsMain, err)
	}

	// The copy this till boots from after the restart already holds them:
	// they survive the wipe of its own database.
	if !appdb.PendingRestore(s.tillPath) {
		t.Fatal("the join staged no restore")
	}
	staged, err := sql.Open("sqlite", filepath.Join(filepath.Dir(s.tillPath), "restore-pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if g := receiptsOn(t, staged, "sale-unsent-1", "sale-unsent-2"); len(g) != 2 {
		t.Fatalf("the staged copy holds %v, want both unsent sales", g)
	}
	// Audited on the till.
	var audits int
	if err := s.till.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'rejoin_journal_sent'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("rejoin audit rows = %d (err %v), want 1", audits, err)
	}
}

// More than one batch, and two sales sharing a created_at across the batch
// edge: none is skipped (the replay pages by the list, not by created_at).
func TestRejoinSendsEveryUnsentSaleAcrossBatches(t *testing.T) {
	s := newRejoinShop(t)
	var ids []string
	for i := 0; i < rejoinSaleBatch+3; i++ {
		id := "bulk-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		ids = append(ids, id)
		seedLocalSale(t, s.till, id, "T2-B"+id, "2026-10-26T09:00:00Z") // same instant for all
	}
	if rec := s.join(t); rec.Code != http.StatusOK {
		t.Fatalf("re-join: %d %s", rec.Code, rec.Body.String())
	}
	if got := receiptsOn(t, s.main.Db, ids...); len(got) != len(ids) {
		t.Fatalf("new main till has %d of %d unsent sales", len(got), len(ids))
	}
}

// The new main till refuses them (here: a receipt number it already uses
// for another sale) → the join is refused, nothing is staged, the
// enrolment is withdrawn, and the till keeps its sales and queue.
func TestRejoinRefusedWhenUnsentSalesCannotBeSent(t *testing.T) {
	s := newRejoinShop(t)
	seedLocalSale(t, s.till, "sale-unsent-1", "T2-000000002", "2026-10-26T09:00:00Z")
	seedLocalSale(t, s.main, "other-sale", "T2-000000002", "2026-10-26T09:30:00Z")

	rec := s.join(t)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "could not be sent") {
		t.Fatalf("re-join with unsendable sales: %d %s, want the refusal", rec.Code, rec.Body.String())
	}
	if appdb.PendingRestore(s.tillPath) {
		t.Fatal("a refused re-join staged the wipe")
	}
	if n := countTills(t, s.main); n != 0 {
		t.Fatalf("the new main till kept the refused enrolment (%d tills)", n)
	}
	if got := receiptsOn(t, s.till.Db, "sale-unsent-1"); got["sale-unsent-1"] != "T2-000000002" {
		t.Fatal("the till lost its unsent sale")
	}
	if cur, _, _ := s.till.Settings.Get(context.Background(), "sync.push_cursor"); cur != "2026-10-26T08:00:00Z" {
		t.Fatalf("the push cursor moved to %q", cur)
	}
}

// Day closes the cloud has not acknowledged (retention mode cloud/both) live
// only on this till: the join waits for them instead of wiping them.
func TestRejoinRefusedWhileDayClosesAreNotInTheCloudYet(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	if err := s.till.Settings.Set(ctx, data.ReportRetentionModeKey, data.ReportRetentionModeCloud); err != nil {
		t.Fatal(err)
	}
	if _, err := s.till.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('z-1', 'eod', '2026-10-25', '{}', '2026-10-25T22:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	rec := s.join(t)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "Day closes") {
		t.Fatalf("re-join with an unsent day close: %d %s", rec.Code, rec.Body.String())
	}
	if n := countTills(t, s.main); n != 0 {
		t.Fatalf("enrolled %d till(s) although the join was refused before enrolling", n)
	}
	if appdb.PendingRestore(s.tillPath) {
		t.Fatal("a refused re-join staged the wipe")
	}
}

// A till that never joined a main till keeps today's behaviour: its sales
// are its own history, replaced only after the manager's explicit confirm.
func TestFirstJoinOfAStandaloneTillSendsNothing(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	for _, k := range []string{"sync.primary_url", "sync.bearer", "sync.push_cursor"} {
		if err := s.till.Settings.Set(ctx, k, ""); err != nil {
			t.Fatal(err)
		}
	}
	seedLocalSale(t, s.till, "own-history", "000000001", "2026-10-26T09:00:00Z")
	if rec := s.join(t); rec.Code != http.StatusOK {
		t.Fatalf("join: %d %s", rec.Code, rec.Body.String())
	}
	if got := receiptsOn(t, s.main.Db, "own-history"); len(got) != 0 {
		t.Fatal("a standalone till's own history was sent to the main till")
	}
}

// Re-joining in a different order must not hand a till another till's old
// receipt prefix: the joining till asks for its own (T2-), and the new main
// till keeps it when no other till holds it or has sales under it. Here the
// old T3 re-joins first and must stay T3, not become T2.
func TestRejoinKeepsTheTillsOwnReceiptPrefix(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	if err := s.till.Settings.Set(ctx, "sync.receipt_prefix", "T3-"); err != nil {
		t.Fatal(err)
	}
	seedLocalSale(t, s.till, "t3-unsent", "T3-000000007", "2026-10-26T09:00:00Z")
	if rec := s.join(t); rec.Code != http.StatusOK {
		t.Fatalf("re-join: %d %s", rec.Code, rec.Body.String())
	}
	if id := stagedIdentity(t, s.tillPath); id.ReceiptPrefix != "T3-" {
		t.Fatalf("staged receipt prefix = %q, want the till's own T3-", id.ReceiptPrefix)
	}
	// A fresh till joining next gets a number no till holds or used.
	fresh, freshPath := newSyncDepsWithPath(t, "fresh.db")
	fmux := http.NewServeMux()
	registerSyncAPI(fmux, fresh)
	rec := postSyncJoin(t, fmux, "/api/sync/join", url.Values{"code": {issueEnrolCode(t, s.mainMux, s.srv.URL)}, "name": {"Till 4"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh join: %d %s", rec.Code, rec.Body.String())
	}
	if p := stagedIdentity(t, freshPath).ReceiptPrefix; p == "T3-" {
		t.Fatalf("the fresh till got the re-joined till's prefix %q", p)
	}
}

// A prefix another till already uses on the new main till is not handed out
// twice: the joining till gets a new one, and its replayed sales still keep
// their own receipt numbers.
func TestRejoinPrefixTakenElsewhereGetsANewOne(t *testing.T) {
	s := newRejoinShop(t)
	seedLocalSale(t, s.main, "someone-else", "T2-000000100", "2026-10-26T08:30:00Z")
	seedLocalSale(t, s.till, "mine", "T2-000000007", "2026-10-26T09:00:00Z")
	if rec := s.join(t); rec.Code != http.StatusOK {
		t.Fatalf("re-join: %d %s", rec.Code, rec.Body.String())
	}
	if p := stagedIdentity(t, s.tillPath).ReceiptPrefix; p == "T2-" || p == "" {
		t.Fatalf("staged prefix = %q, want a new one (T2- is used by another till's sales)", p)
	}
	if got := receiptsOn(t, s.main.Db, "mine"); got["mine"] != "T2-000000007" {
		t.Fatalf("replayed sale = %v", got)
	}
}

// A sale the new main till can only quarantine (here: an item it does not
// know) is not delivered: the join is refused instead of wiping it.
func TestRejoinRefusedWhenTheNewMainTillQuarantinesASale(t *testing.T) {
	s := newRejoinShop(t)
	seedLocalSale(t, s.till, "orphan", "T2-000000009", "2026-10-26T09:00:00Z")
	if _, err := s.till.Db.Exec(`PRAGMA foreign_keys = OFF; UPDATE sale_lines SET item_id = 'item-unknown-on-main' WHERE sale_id = 'orphan'; PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	rec := s.join(t)
	if rec.Code == http.StatusOK || appdb.PendingRestore(s.tillPath) {
		t.Fatalf("re-join with a quarantined sale: %d %s (staged=%v)", rec.Code, rec.Body.String(), appdb.PendingRestore(s.tillPath))
	}
}

func stagedIdentity(t *testing.T, dbPath string) appdb.ReplicaIdentity {
	t.Helper()
	raw, err := os.ReadFile(appdb.ReplicaIdentityPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	var id appdb.ReplicaIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A sale rung after the join was staged and before the restart applies it
// goes to the new main till when the restart comes (the start-time pass,
// which also covers Windows, where the till cannot restart itself).
func TestSaleAfterTheJoinWasStagedReachesTheNewMainTillAtRestart(t *testing.T) {
	s := newRejoinShop(t)
	seedLocalSale(t, s.till, "before", "T2-000000002", "2026-10-26T09:00:00Z")
	if rec := s.join(t); rec.Code != http.StatusOK {
		t.Fatalf("re-join: %d %s", rec.Code, rec.Body.String())
	}
	seedLocalSale(t, s.till, "after-staging", "T2-000000003", "2026-10-26T09:30:00Z")
	ReplayStagedJoinAtStart(context.Background(), s.till.Db, s.tillPath)
	if got := receiptsOn(t, s.main.Db, "before", "after-staging"); len(got) != 2 {
		t.Fatalf("new main till has %v, want both sales", got)
	}
}

// Nothing staged, or a staged backup restore (no join identity): nothing is sent.
func TestReplayStagedJoinWithoutAJoinSendsNothing(t *testing.T) {
	s := newRejoinShop(t)
	seedLocalSale(t, s.till, "x", "T2-000000002", "2026-10-26T09:00:00Z")
	sent, err := replayStagedJoin(context.Background(), s.till, s.tillPath)
	if err != nil || sent.Sales != 0 {
		t.Fatalf("no staged join: sent=%+v err=%v", sent, err)
	}
}

// When the cloud refuses the uploads (subscription not active), waiting does
// not help: the message says what does.
func TestRejoinDayClosesRefusedByTheCloudSaysWhy(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	for k, v := range map[string]string{data.ReportRetentionModeKey: data.ReportRetentionModeBoth, data.ReportArchiveCloudRefusedAtKey: "2026-10-26T08:00:00Z"} {
		if err := s.till.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.till.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('z-1', 'eod', '2026-10-25', '{}', '2026-10-25T22:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if b := s.join(t).Body.String(); !strings.Contains(b, "subscription is not active") {
		t.Fatalf("refused day closes: %s", b)
	}
}

// ut-docs#5162: a restored copy of a joined till can never upload its day
// closes (its cloud identity is reset), so the day-close gate must not hold
// the join forever. They stay in this till's backup copy, and the join says
// so in the audit log.
func TestCompleteJoin_RestoredReplicaSkipsDayCloseGate(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		data.ReportRetentionModeKey: data.ReportRetentionModeCloud,
		"sync.bearer":               "", // the restore check dropped it
		"till_identity.restored_at": "2026-10-29T09:00:00Z",
	} {
		if err := s.till.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"z-1", "z-2"} {
		if _, err := s.till.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES (?, 'eod', ?, '{}', '2026-10-25T22:00:00Z')`, id, "2026-10-2"+id[2:]); err != nil {
			t.Fatal(err)
		}
	}
	rec := s.join(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("restored copy's re-join: %d %s", rec.Code, rec.Body.String())
	}
	if !appdb.PendingRestore(s.tillPath) {
		t.Fatal("the join staged no restore")
	}
	var payload string
	if err := s.till.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'rejoin_day_closes_kept_local'`).Scan(&payload); err != nil {
		t.Fatalf("no rejoin_day_closes_kept_local audit row: %v", err)
	}
	if !strings.Contains(payload, `"day_closes":2`) {
		t.Fatalf("audit payload = %s, want the count of 2", payload)
	}
}

// Without the restored flag the gate still refuses (the test above would
// otherwise pass for any till).
func TestCompleteJoin_NotRestoredStillRefusedOnDayCloses(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	if err := s.till.Settings.Set(ctx, data.ReportRetentionModeKey, data.ReportRetentionModeCloud); err != nil {
		t.Fatal(err)
	}
	if _, err := s.till.Db.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('z-1', 'eod', '2026-10-25', '{}', '2026-10-25T22:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if rec := s.join(t); rec.Code == http.StatusOK {
		t.Fatalf("a non-restored till's re-join with an unsent day close was accepted: %s", rec.Body.String())
	}
}

// ut-docs#5162: a restored copy (no bearer, provisional R<hex>- receipt
// prefix) re-joins keeping its sales: the unsent ones are applied on the main
// till under their own numbers, ones the main till already holds are skipped
// by id, and the main till grants a new T<n>- instead of the R prefix.
func TestRejoin_RestoredReplicaWithoutBearerReplaysAndGetsANewTillNo(t *testing.T) {
	s := newRejoinShop(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		"sync.bearer": "", "sync.till_id": "", "sync.receipt_prefix": "R1a2b-",
		"till_identity.restored_at": "2026-10-29T09:00:00Z",
	} {
		if err := s.till.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	// "dup" is in the copy past its cursor but the original already pushed it.
	seedLocalSale(t, s.till, "dup", "T2-000000004", "2026-10-26T09:00:00Z")
	seedLocalSale(t, s.main, "dup", "T2-000000004", "2026-10-26T09:00:00Z")
	seedLocalSale(t, s.till, "mine", "R1a2b-000000001", "2026-10-29T10:00:00Z")

	rec := s.join(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("restored copy's re-join: %d %s", rec.Code, rec.Body.String())
	}
	got := receiptsOn(t, s.main.Db, "dup", "mine")
	if got["mine"] != "R1a2b-000000001" || got["dup"] != "T2-000000004" {
		t.Fatalf("main till sales = %v, want mine under its R number and dup untouched", got)
	}
	var n int
	if err := s.main.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE id = 'dup'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dup sale rows on the main till = %d (err %v), want 1", n, err)
	}
	if p := stagedIdentity(t, s.tillPath).ReceiptPrefix; !regexp.MustCompile(`^T[0-9]+-$`).MatchString(p) {
		t.Fatalf("staged receipt prefix = %q, want a granted T<n>-, not the provisional R prefix", p)
	}
}
