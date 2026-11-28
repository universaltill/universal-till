package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// ADR-0215 Decision 9 / legal review 2026-11-28 R1..R9 (ut-docs#6988): the
// till erases a delivery order's customer note on its own clock.

func noteApply(t *testing.T, repo *ChannelOrdersRepo, ext, status string, rev int64) string {
	t.Helper()
	in := sampleDeliveryOrder()
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID = ext, "#"+ext, "dir-"+ext+"-"+status
	in.Status, in.Revision = status, rev
	res, err := repo.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("apply %s %s: %v", ext, status, err)
	}
	return res.OrderID
}

func noteState(t *testing.T, d *sql.DB, id string) (orderNote, lineNote string) {
	t.Helper()
	if err := d.QueryRow(`SELECT note FROM channel_orders WHERE id=?`, id).Scan(&orderNote); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COALESCE(MAX(note),'') FROM channel_order_lines WHERE order_id=?`, id).Scan(&lineNote); err != nil {
		t.Fatal(err)
	}
	return
}

func TestDeliveryNoteRetention_ClampsToTheRules(t *testing.T) {
	cases := map[string]time.Duration{
		"": 24 * time.Hour, "12": 12 * time.Hour, "1": time.Hour, "72": 72 * time.Hour,
		"73": 72 * time.Hour, "9999": 72 * time.Hour, "0": 24 * time.Hour, "-5": 24 * time.Hour, "abc": 24 * time.Hour,
	}
	for in, want := range cases {
		if got := DeliveryNoteRetention(in); got != want {
			t.Errorf("DeliveryNoteRetention(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEraseExpiredDeliveryNotes_BlanksAfterTerminalPlusRetention(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	id := noteApply(t, repo, "d-1", "new", 1)
	noteApply(t, repo, "d-1", "collected", 2)
	if o, l := noteState(t, d, id); o == "" || l == "" {
		t.Fatalf("notes must be kept while live: %q %q", o, l)
	}
	var term string
	if err := d.QueryRow(`SELECT terminal_at FROM channel_orders WHERE id=?`, id).Scan(&term); err != nil || term == "" {
		t.Fatalf("terminal_at = %q err=%v", term, err)
	}
	tAt, _ := time.Parse(time.RFC3339, term)

	// 23 h after terminal with a 24 h retention: still there.
	if n, err := repo.EraseExpiredDeliveryNotes(ctx, tAt.Add(23*time.Hour), 24*time.Hour); err != nil || n != 0 {
		t.Fatalf("early erase n=%d err=%v", n, err)
	}
	if o, _ := noteState(t, d, id); o == "" {
		t.Fatal("note erased before the retention elapsed")
	}
	// 25 h after: blanked on both the order and its line, audit kept without text.
	n, err := repo.EraseExpiredDeliveryNotes(ctx, tAt.Add(25*time.Hour), 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("erase n=%d err=%v", n, err)
	}
	if o, l := noteState(t, d, id); o != "" || l != "" {
		t.Fatalf("notes not blanked: %q %q", o, l)
	}
	var present, chars int
	var erasedAt, by string
	if err := d.QueryRow(`SELECT note_present, note_chars, note_erased_at, note_erased_by FROM channel_orders WHERE id=?`, id).Scan(&present, &chars, &erasedAt, &by); err != nil {
		t.Fatal(err)
	}
	if present != 1 || chars != len("No nuts please") || erasedAt == "" || by != NoteErasedByCap {
		t.Fatalf("audit = present %d chars %d at %q by %q", present, chars, erasedAt, by)
	}
	// Idempotent, and the order itself stays.
	if n, _ := repo.EraseExpiredDeliveryNotes(ctx, tAt.Add(48*time.Hour), 24*time.Hour); n != 0 {
		t.Fatalf("second run erased %d", n)
	}
	if o, ok, _ := repo.Get(ctx, id); !ok || o.Status != "collected" || len(o.Lines) != 1 {
		t.Fatalf("order must remain: %+v %v", o, ok)
	}
}

func TestEraseExpiredDeliveryNotes_NeverBlanksALiveOrderBeforeItsBackstop(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	id := noteApply(t, repo, "d-live", "accepted", 1)
	var created string
	if err := d.QueryRow(`SELECT created_at FROM channel_orders WHERE id=?`, id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	c, _ := time.Parse(time.RFC3339, created)
	if n, _ := repo.EraseExpiredDeliveryNotes(ctx, c.Add(13*24*time.Hour), 24*time.Hour); n != 0 {
		t.Fatal("a live order's note must survive until received_at + 14 d")
	}
	n, err := repo.EraseExpiredDeliveryNotes(ctx, c.Add(14*24*time.Hour+time.Minute), 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("backstop n=%d err=%v", n, err)
	}
	var by string
	_ = d.QueryRow(`SELECT note_erased_by FROM channel_orders WHERE id=?`, id).Scan(&by)
	if by != NoteErasedByBackstop {
		t.Fatalf("erased_by = %q", by)
	}
}

func TestEraseExpiredDeliveryNotes_ScheduledOrderUsesStartPlus72h(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second) // a month ahead: cooked later
	in := sampleDeliveryOrder()
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID = "d-sched", "#S", "dir-s"
	in.Delivery.ScheduledStartAt = start.Format(time.RFC3339)
	res, err := repo.Apply(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// 15 days after receipt the 14 d backstop would have fired for an
	// unscheduled order; a scheduled one keeps its note until start + 72 h.
	if n, _ := repo.EraseExpiredDeliveryNotes(ctx, time.Now().UTC().Add(15*24*time.Hour), 24*time.Hour); n != 0 {
		t.Fatal("scheduled order erased before its start")
	}
	if n, _ := repo.EraseExpiredDeliveryNotes(ctx, start.Add(71*time.Hour), 24*time.Hour); n != 0 {
		t.Fatal("scheduled order erased before start + 72 h")
	}
	if n, err := repo.EraseExpiredDeliveryNotes(ctx, start.Add(73*time.Hour), 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if o, _ := noteState(t, d, res.OrderID); o != "" {
		t.Fatal("note kept")
	}
}

func TestEraseExpiredDeliveryNotes_TerminalClockIsNotRestartedByARefund(t *testing.T) {
	d, repo := openChannelOrders(t)
	id := noteApply(t, repo, "d-ref", "collected", 1)
	var first string
	_ = d.QueryRow(`SELECT terminal_at FROM channel_orders WHERE id=?`, id).Scan(&first)
	if _, err := d.Exec(`UPDATE channel_orders SET terminal_at='2000-01-01T00:00:00Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	noteApply(t, repo, "d-ref", "refunded", 2)
	var after string
	_ = d.QueryRow(`SELECT terminal_at FROM channel_orders WHERE id=?`, id).Scan(&after)
	if after != "2000-01-01T00:00:00Z" {
		t.Fatalf("terminal_at moved to %q", after)
	}
}

func TestApply_AfterErasureAStaleUpdateNeverRestoresTheNote(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	id := noteApply(t, repo, "d-stale", "collected", 1)
	if _, err := repo.EraseExpiredDeliveryNotes(ctx, time.Now().UTC().Add(25*time.Hour), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	noteApply(t, repo, "d-stale", "refunded", 2) // payload still carries the note text
	if o, l := noteState(t, d, id); o != "" || l != "" {
		t.Fatalf("an update re-wrote an erased note: %q %q", o, l)
	}
}

// R8: the cloud erases at once and sends the order's next revision as a
// terminal status with no note. A live order is moved to terminal in the same
// update, so the note never disappears from an order being cooked unannounced.
func TestApply_CloudEndingUpdateBlanksNoteAndRecordsTheStatus(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	id := noteApply(t, repo, "d-red", "preparing", 1)
	if o, l := noteState(t, d, id); o == "" || l == "" {
		t.Fatal("setup: notes must exist while live")
	}
	in := sampleDeliveryOrder()
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID = "d-red", "#d-red", "dir-red-2"
	in.Status, in.Revision, in.Note = "cancelled", 2, ""
	in.Lines[0].Note = ""
	if _, err := repo.Apply(ctx, in); err != nil {
		t.Fatal(err)
	}
	if o, l := noteState(t, d, id); o != "" || l != "" {
		t.Fatalf("notes kept after redact: %q %q", o, l)
	}
	var st, term, by, reason string
	var present int
	if err := d.QueryRow(`SELECT status, terminal_at, note_erased_by, note_present, reject_reason FROM channel_orders WHERE id=?`, id).Scan(&st, &term, &by, &present, &reason); err != nil {
		t.Fatal(err)
	}
	if st != "cancelled" || term == "" || by != NoteErasedByCloudEnd || present != 1 || reason != "" {
		t.Fatalf("after redact: status %q terminal_at %q erased_by %q note_present %d reason %q", st, term, by, present, reason)
	}
}

func TestMarkDeliveryNoteViewed_FirstViewOnlyAndNoTextInAudit(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	id := noteApply(t, repo, "d-v", "new", 1)
	t1 := time.Now().UTC().Truncate(time.Second)
	if err := repo.MarkDeliveryNoteViewed(ctx, id, t1); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkDeliveryNoteViewed(ctx, id, t1.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var v string
	_ = d.QueryRow(`SELECT note_viewed_at FROM channel_orders WHERE id=?`, id).Scan(&v)
	if v != t1.Format(time.RFC3339) {
		t.Fatalf("note_viewed_at = %q", v)
	}
	// No audit column may hold the text.
	rows, err := d.Query(`SELECT terminal_at, note_viewed_at, note_erased_at, note_erased_by FROM channel_orders`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c, e string
		_ = rows.Scan(&a, &b, &c, &e)
		if strings.Contains(a+b+c+e, "nuts") {
			t.Fatal("audit columns hold note text")
		}
	}
}

// Review finding: the cloud queues every staff-action revision (Accept, Ready)
// with no note. A note-free revision of a LIVE order is not a redact: the
// kitchen still needs the note while it cooks.
func TestApply_NoteFreeRevisionOfALiveOrderKeepsTheNote(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	id := noteApply(t, repo, "d-keep", "new", 1)
	in := sampleDeliveryOrder()
	in.ExternalOrderID, in.OrderNumber, in.DirectiveID = "d-keep", "#d-keep", "dir-keep-2"
	in.Status, in.Revision, in.Note = "accepted", 2, ""
	in.Lines[0].Note = ""
	if _, err := repo.Apply(ctx, in); err != nil {
		t.Fatal(err)
	}
	if o, l := noteState(t, d, id); o != sampleDeliveryOrder().Note || l != sampleDeliveryOrder().Lines[0].Note || o == "" || l == "" {
		t.Fatalf("notes lost or changed on a staff-action revision of a live order: %q %q", o, l)
	}
	var by string
	_ = d.QueryRow(`SELECT note_erased_by FROM channel_orders WHERE id=?`, id).Scan(&by)
	if by != "" {
		t.Fatalf("marked erased: %q", by)
	}
}

// Line notes alone (no order note) are kept too, and a plain "collected" is not
// shown as withdrawn.
func TestApply_LineNoteOnlyKeptAndCollectedIsNotWithdrawn(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	first := sampleDeliveryOrder()
	first.ExternalOrderID, first.OrderNumber, first.DirectiveID = "d-ln", "#d-ln", "dir-ln-1"
	first.Status, first.Revision, first.Note = "ready", 1, ""
	first.Lines[0].Note = "no onions"
	res, err := repo.Apply(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	id := res.OrderID
	next := sampleDeliveryOrder()
	next.ExternalOrderID, next.OrderNumber, next.DirectiveID = "d-ln", "#d-ln", "dir-ln-2"
	next.Status, next.Revision, next.Note = "ready", 2, ""
	next.Lines[0].Note = ""
	if _, err := repo.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, l := noteState(t, d, id); l != "no onions" {
		t.Fatalf("line note lost: %q", l)
	}
	done := sampleDeliveryOrder()
	done.ExternalOrderID, done.OrderNumber, done.DirectiveID = "d-ln", "#d-ln", "dir-ln-3"
	done.Status, done.Revision, done.Note = "collected", 3, ""
	done.Lines[0].Note = ""
	if _, err := repo.Apply(ctx, done); err != nil {
		t.Fatal(err)
	}
	var reason string
	_ = d.QueryRow(`SELECT reject_reason FROM channel_orders WHERE id=?`, id).Scan(&reason)
	if reason != "" {
		t.Fatalf("a collected order shown as %q", reason)
	}
	if _, l := noteState(t, d, id); l != "" {
		t.Fatalf("line note kept after the order ended: %q", l)
	}
}

// The plugin numbers lines by position, so a platform update that drops line 1
// renumbers the rest: a stored line note must never be carried onto a line that
// now names a different item.
func TestApply_KeptLineNoteNeverMovesToADifferentItem(t *testing.T) {
	d, repo := openChannelOrders(t)
	ctx := context.Background()
	first := sampleDeliveryOrder()
	first.ExternalOrderID, first.OrderNumber, first.DirectiveID = "d-renum", "#d-renum", "dir-rn-1"
	first.Status, first.Revision, first.Note = "accepted", 1, ""
	first.Lines[0].LineID, first.Lines[0].Name, first.Lines[0].SKU, first.Lines[0].Note = "l1", "Burger", "BURGER", "nut allergy"
	res, err := repo.Apply(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	next := sampleDeliveryOrder()
	next.ExternalOrderID, next.OrderNumber, next.DirectiveID = "d-renum", "#d-renum", "dir-rn-2"
	next.Status, next.Revision, next.Note = "accepted", 2, ""
	next.Lines[0].LineID, next.Lines[0].Name, next.Lines[0].SKU, next.Lines[0].Note = "l1", "Fries", "FRIES", ""
	if _, err := repo.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, l := noteState(t, d, res.OrderID); l != "" {
		t.Fatalf("burger note moved onto the fries line: %q", l)
	}
}
