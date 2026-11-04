package data

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func newNoticeRepo(t *testing.T) *CloudNoticeRepo {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewCloudNoticeRepo(d.DB)
}

var noticeNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func nrow(id string, rev int, sev string) CloudNotice {
	return CloudNotice{
		ID: id, Rev: rev, SenderKind: "platform", SenderName: "Universal Till",
		Severity: sev, Lang: "en-gb", Title: "T " + id, Body: "B " + id,
		StartsAt: noticeNow.Add(-time.Hour), ExpiresAt: noticeNow.Add(48 * time.Hour),
		CreatedAt: noticeNow.Add(-time.Hour),
	}
}

func ids(ns []CloudNotice) string {
	var s []string
	for _, n := range ns {
		s = append(s, n.ID)
	}
	return strings.Join(s, ",")
}

func TestNoticeMirror_RemovesIdsTheCloudNoLongerLists(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	if err := r.Mirror(ctx, []CloudNotice{nrow("a", 1, "info"), nrow("b", 1, "info")}, noticeNow); err != nil {
		t.Fatal(err)
	}
	if err := r.Mirror(ctx, []CloudNotice{nrow("b", 1, "info")}, noticeNow); err != nil {
		t.Fatal(err)
	}
	got, _ := r.List(ctx, noticeNow, NoticeAll)
	if ids(got) != "b" {
		t.Fatalf("held = %q, want b", ids(got))
	}
}

func TestNoticeMirror_EmptySetClearsTheStore(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 1, "info")}, noticeNow)
	if err := r.Mirror(ctx, nil, noticeNow); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Pairs(ctx); len(p) != 0 {
		t.Fatalf("pairs = %v", p)
	}
}

func TestNoticeMirror_KeepsMarksOnSameRevResetsOnNewRev(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 1, "important")}, noticeNow)
	_ = r.MarkRead(ctx, "a", noticeNow)
	_ = r.MarkToasted(ctx, "a", noticeNow)
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 1, "important")}, noticeNow)
	n, _, _ := r.Get(ctx, "a")
	if n.ReadAt == nil || n.ToastedAt == nil {
		t.Fatal("same rev must keep read/toasted marks")
	}
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 2, "important")}, noticeNow)
	n, _, _ = r.Get(ctx, "a")
	if n.ReadAt != nil || n.ToastedAt != nil {
		t.Fatal("a new rev is new content: read/toasted must reset")
	}
}

func TestNoticeMirror_DropsExpiredAndNotYetStarted(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	exp := nrow("old", 1, "info")
	exp.ExpiresAt = noticeNow.Add(-time.Minute)
	if err := r.Mirror(ctx, []CloudNotice{exp, nrow("ok", 1, "info")}, noticeNow); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Pairs(ctx); len(p) != 1 || p[0].ID != "ok" {
		t.Fatalf("pairs = %v", p)
	}
	// Expiry on read: a held row that expires later is not listed.
	got, _ := r.List(ctx, noticeNow.Add(72*time.Hour), NoticeAll)
	if len(got) != 0 {
		t.Fatalf("expired row listed: %s", ids(got))
	}
	if n, err := r.PurgeExpired(ctx, noticeNow.Add(72*time.Hour)); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
}

func TestNoticeUnreadAndTabs(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 1, "info"), nrow("b", 1, "critical"), nrow("c", 1, "important")}, noticeNow)
	n, crit, err := r.UnreadCount(ctx, noticeNow)
	if err != nil || n != 3 || !crit {
		t.Fatalf("unread = %d crit=%v err=%v", n, crit, err)
	}
	_ = r.MarkRead(ctx, "b", noticeNow)
	_ = r.Dismiss(ctx, "c", noticeNow)
	n, crit, _ = r.UnreadCount(ctx, noticeNow)
	if n != 1 || crit {
		t.Fatalf("after read+dismiss unread = %d crit=%v", n, crit)
	}
	un, _ := r.List(ctx, noticeNow, NoticeUnread)
	if ids(un) != "a" {
		t.Fatalf("unread tab = %q", ids(un))
	}
	all, _ := r.List(ctx, noticeNow, NoticeAll)
	if len(all) != 3 {
		t.Fatalf("all tab = %q (dismissed stays in the inbox)", ids(all))
	}
}

func TestNoticeAckAndStrip(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	partnerCrit := nrow("p", 1, "critical")
	partnerCrit.SenderKind = "partner" // cannot exist (D4) but never strips
	_ = r.Mirror(ctx, []CloudNotice{nrow("c", 1, "critical"), nrow("i", 1, "info"), partnerCrit}, noticeNow)
	s, err := r.Strip(ctx, noticeNow)
	if err != nil || len(s) != 1 || s[0].ID != "c" {
		t.Fatalf("strip = %v err=%v", s, err)
	}
	if err := r.Acknowledge(ctx, "i", noticeNow); err == nil {
		t.Fatal("only a critical notice can be acknowledged")
	}
	if err := r.Acknowledge(ctx, "c", noticeNow); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.Strip(ctx, noticeNow); len(s) != 0 {
		t.Fatalf("acknowledged notice still strips: %v", s)
	}
	// Acknowledging does not mark it read: it stays unread until opened.
	un, _ := r.List(ctx, noticeNow, NoticeUnread)
	if !strings.Contains(ids(un), "c") {
		t.Fatalf("acked critical should stay unread, unread=%q", ids(un))
	}
}

func TestNoticeToastQueue(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	_ = r.Mirror(ctx, []CloudNotice{nrow("i", 1, "info"), nrow("m", 1, "important")}, noticeNow)
	n, ok, err := r.NextToast(ctx, noticeNow)
	if err != nil || !ok || n.ID != "m" {
		t.Fatalf("toast = %v %v %v (info never toasts)", n.ID, ok, err)
	}
	_ = r.MarkToasted(ctx, "m", noticeNow)
	if _, ok, _ := r.NextToast(ctx, noticeNow); ok {
		t.Fatal("never toast the same id+rev twice")
	}
}

func TestNoticeAcksBatchAndReported(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 1, "info"), nrow("b", 1, "info")}, noticeNow)
	acks, _ := r.PendingAcks(ctx, 50)
	if len(acks) != 2 || acks[0].DeliveredAt == "" {
		t.Fatalf("first report carries delivered_at: %+v", acks)
	}
	_ = r.MarkReported(ctx, []string{"a", "b"}, noticeNow)
	if acks, _ = r.PendingAcks(ctx, 50); len(acks) != 0 {
		t.Fatalf("reported acks must not repeat: %+v", acks)
	}
	_ = r.MarkRead(ctx, "a", noticeNow.Add(time.Minute))
	acks, _ = r.PendingAcks(ctx, 50)
	if len(acks) != 1 || acks[0].ID != "a" || acks[0].ReadAt == "" {
		t.Fatalf("a new mark is sent once: %+v", acks)
	}
	// limit
	many := []CloudNotice{}
	for i := 0; i < 60; i++ {
		many = append(many, nrow(strings.Repeat("x", 1)+string(rune('A'+i%26))+string(rune('a'+i/26)), 1, "info"))
	}
	_ = r.Mirror(ctx, many, noticeNow)
	if acks, _ = r.PendingAcks(ctx, 50); len(acks) > 50 {
		t.Fatalf("batch = %d, max 50", len(acks))
	}
}

func TestNoticeBounds_RowsAndBytes_EvictionOrder(t *testing.T) {
	r := newNoticeRepo(t)
	r.maxRows, r.maxBytes = 3, 1<<20
	ctx := context.Background()
	rows := []CloudNotice{}
	for i, id := range []string{"unreadcrit", "unreadinfo", "readinfo", "unreadimp"} {
		n := nrow(id, 1, "info")
		switch id {
		case "unreadcrit":
			n.Severity = "critical"
		case "unreadimp":
			n.Severity = "important"
		}
		n.CreatedAt = noticeNow.Add(-time.Duration(10-i) * time.Hour)
		rows = append(rows, n)
	}
	_ = r.Mirror(ctx, rows[:3], noticeNow)
	_ = r.MarkRead(ctx, "readinfo", noticeNow)
	if err := r.Mirror(ctx, rows, noticeNow); err != nil {
		t.Fatal(err)
	}
	// 4 rows into a cap of 3: the read one goes first.
	p, _ := r.Pairs(ctx)
	got := map[string]bool{}
	for _, x := range p {
		got[x.ID] = true
	}
	if len(p) != 3 || got["readinfo"] || !got["unreadcrit"] {
		t.Fatalf("held = %v, want the read row evicted and the unread critical kept", p)
	}
	// Byte cap: only the unread critical may survive being the last.
	r.maxRows, r.maxBytes = 100, 40
	if err := r.Mirror(ctx, rows, noticeNow); err != nil {
		t.Fatal(err)
	}
	p, _ = r.Pairs(ctx)
	if len(p) != 1 || p[0].ID != "unreadcrit" {
		t.Fatalf("under the byte cap an unread critical is evicted last, held=%v", p)
	}
}

func TestNoticeReset(t *testing.T) {
	r := newNoticeRepo(t)
	ctx := context.Background()
	_ = r.Mirror(ctx, []CloudNotice{nrow("a", 1, "info")}, noticeNow)
	if err := r.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Pairs(ctx); len(p) != 0 {
		t.Fatal("reset must empty the store")
	}
}
