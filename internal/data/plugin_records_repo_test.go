package data

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ut-docs#3697: the plugin-records outbox, inbox and snapshot store.

func recordsRepo(t *testing.T) (*PluginRecordsRepo, *SyncAdminRepo) {
	t.Helper()
	d := b8OpenDB(t, "plugin-records.db")
	return NewPluginRecordsRepo(d.DB), NewSyncAdminRepo(d.DB)
}

func outboxCount(t *testing.T, r *PluginRecordsRepo, where string, args ...any) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM plugin_record_outbox WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordsEnqueue_Idempotent_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if _, err := r.Enqueue(ctx, "p.a", "entries", "id-1", []byte(`{"a":1}`), now); err != nil {
			t.Fatal(err)
		}
	}
	if n := outboxCount(t, r, "1=1"); n != 1 {
		t.Fatalf("same (plugin, collection, id) twice = %d rows, want 1", n)
	}
}

func TestRecordsEnqueue_PerPluginRowCapRejectsOldest_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r.limits.maxRows = 3
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if _, err := r.Enqueue(ctx, "p.a", "entries", id, []byte(`{}`), now); err != nil {
			t.Fatal(err)
		}
	}
	if n := outboxCount(t, r, "status='pending'"); n != 3 {
		t.Fatalf("pending = %d, want cap 3", n)
	}
	if n := outboxCount(t, r, "status='rejected' AND reason='overflow'"); n != 2 {
		t.Fatalf("overflow-rejected = %d, want 2 (the oldest)", n)
	}
	if n := outboxCount(t, r, "status='rejected' AND record_id IN ('a','b')"); n != 2 {
		t.Fatal("the OLDEST rows must be the rejected ones")
	}
}

func TestRecordsEnqueue_PerPluginByteCapRejectsOldest_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r.limits.maxBytes = 100
	big := []byte(`{"x":"` + strings.Repeat("y", 50) + `"}`) // 58 bytes
	for _, id := range []string{"a", "b", "c"} {
		if _, err := r.Enqueue(ctx, "p.a", "entries", id, big, now); err != nil {
			t.Fatal(err)
		}
	}
	var sum int
	if err := r.db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM plugin_record_outbox WHERE status='pending'`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum > 100 || sum == 0 {
		t.Fatalf("pending bytes = %d, want 1..100", sum)
	}
}

func TestRecordsEnqueue_GlobalCap_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r.limits.maxRowsAll = 2
	for i, p := range []string{"p.a", "p.b", "p.c"} {
		if _, err := r.Enqueue(ctx, p, "entries", "id", []byte(`{}`), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if n := outboxCount(t, r, "status='pending'"); n != 2 {
		t.Fatalf("pending across plugins = %d, want global cap 2", n)
	}
}

func TestRecordsEnqueue_OverflowIsAudited_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	r.limits.maxRows = 1
	now := time.Now().UTC()
	_, _ = r.Enqueue(context.Background(), "p.a", "entries", "a", []byte(`{}`), now)
	over, err := r.Enqueue(context.Background(), "p.a", "entries", "b", []byte(`{}`), now)
	if err != nil || over != 1 {
		t.Fatalf("overflowed = %d err = %v, want 1", over, err)
	}
}

func TestRecordsBackoffSchedule_3697(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{{0, 30 * time.Second}, {1, time.Minute}, {2, 2 * time.Minute}, {5, 16 * time.Minute}, {7, time.Hour}, {30, time.Hour}}
	for _, c := range cases {
		if got := RecordBackoff(c.attempts, 0); got != c.want {
			t.Errorf("RecordBackoff(%d, 0) = %v, want %v", c.attempts, got, c.want)
		}
	}
	if got := RecordBackoff(1, 1); got != 72*time.Second {
		t.Errorf("+20%% jitter = %v, want 72s", got)
	}
	if got := RecordBackoff(1, -1); got != 48*time.Second {
		t.Errorf("-20%% jitter = %v, want 48s", got)
	}
}

func TestRecordsDue_OrderAndBatchBounds_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		_, _ = r.Enqueue(ctx, "p.a", "entries", id, []byte(`{}`), now.Add(-time.Minute))
	}
	_, _ = r.Enqueue(ctx, "p.a", "entries", "future", []byte(`{}`), now.Add(time.Hour))
	due, err := r.Due(ctx, now, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].RecordID != "a" || due[1].RecordID != "b" {
		t.Fatalf("due = %+v, want a,b by seq, limit 2", due)
	}
	// byte bound: a 1-byte budget still yields the first row, never more
	due, _ = r.Due(ctx, now, 20, 1)
	if len(due) != 1 {
		t.Fatalf("byte-bounded batch = %d rows, want 1", len(due))
	}
}

func TestRecordsRetryAndReject_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, _ = r.Enqueue(ctx, "p.a", "entries", "a", []byte(`{}`), now)
	due, _ := r.Due(ctx, now, 10, 1<<20)
	if err := r.Retry(ctx, due[0].Seq, now, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Due(ctx, now, 10, 1<<20); len(got) != 0 {
		t.Fatal("a retried row must wait for its backoff")
	}
	if got, _ := r.Due(ctx, now.Add(time.Minute), 10, 1<<20); len(got) != 1 || got[0].Attempts != 1 {
		t.Fatalf("after backoff: %+v", got)
	}
	if err := r.MarkRejected(ctx, due[0].Seq, "invalid", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Due(ctx, now.Add(time.Hour), 10, 1<<20); len(got) != 0 {
		t.Fatal("a rejected row is never due")
	}
	st, _ := r.Status(ctx, "p.a", "entries")
	if len(st) != 1 || st[0].Status != "rejected" || st[0].Reason != "invalid" {
		t.Fatalf("status = %+v", st)
	}
}

func TestRecordsStatus_OwnPluginOnlyOldestFirstCapped_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, _ = r.Enqueue(ctx, "p.b", "entries", "other", []byte(`{}`), now)
	for i := 0; i < RecordStatusMaxRows+5; i++ {
		_, _ = r.Enqueue(ctx, "p.a", "entries", fmt.Sprintf("id-%d", i), []byte(`{}`), now)
	}
	st, err := r.Status(ctx, "p.a", "entries")
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != RecordStatusMaxRows {
		t.Fatalf("status rows = %d, want cap %d", len(st), RecordStatusMaxRows)
	}
	for _, s := range st {
		if s.ID == "other" {
			t.Fatal("another plugin's row leaked")
		}
	}
}

func TestRecordsSweep_ExpiresAndTrimsRejected_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, _ = r.Enqueue(ctx, "p.a", "entries", "old", []byte(`{}`), now.Add(-31*24*time.Hour))
	_, _ = r.Enqueue(ctx, "p.a", "entries", "new", []byte(`{}`), now)
	// a rejected row older than 30 days is deleted
	_, _ = r.Enqueue(ctx, "p.a", "entries", "oldrej", []byte(`{}`), now.Add(-40*24*time.Hour))
	if _, err := r.db.Exec(`UPDATE plugin_record_outbox SET status='rejected', rejected_at=? WHERE record_id='oldrej'`,
		now.Add(-31*24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	res, err := r.Sweep(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Expired != 1 || res.Purged != 1 {
		t.Fatalf("sweep = %+v, want expired 1 purged 1", res)
	}
	if n := outboxCount(t, r, "record_id='old' AND status='rejected' AND reason='expired'"); n != 1 {
		t.Fatal("expired row must be rejected reason expired")
	}
	if n := outboxCount(t, r, "record_id='new' AND status='pending'"); n != 1 {
		t.Fatal("fresh row must stay pending")
	}
}

func TestRecordsSweep_RejectedPerPluginCap_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r.limits.keepRejected = 2
	for _, id := range []string{"a", "b", "c", "d"} {
		_, _ = r.Enqueue(ctx, "p.a", "entries", id, []byte(`{}`), now)
	}
	if _, err := r.db.Exec(`UPDATE plugin_record_outbox SET status='rejected', rejected_at=?`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, r, "status='rejected'"); n != 2 {
		t.Fatalf("rejected kept = %d, want 2", n)
	}
}

func TestRecordsRejectAllPending_Promote_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, _ = r.Enqueue(ctx, "p.a", "entries", "a", []byte(`{}`), now)
	tx, _ := r.db.BeginTx(ctx, nil)
	if err := RejectAllPendingRecords(ctx, tx, RecordReasonTillPromoted, now); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if n := outboxCount(t, r, "status='rejected' AND reason='till_promoted'"); n != 1 {
		t.Fatal("promote must mark pending rows till_promoted")
	}
}

func TestRecordsInbox_DedupeAndTrim_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	has, _ := r.InboxHas(ctx, "p.a", "entries", "x")
	if has {
		t.Fatal("empty inbox has nothing")
	}
	if err := r.InboxAdd(ctx, "p.a", "entries", "x", "till-1", now.Add(-91*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.InboxAdd(ctx, "p.a", "entries", "x", "till-1", now); err != nil {
		t.Fatalf("re-add must be a no-op, got %v", err)
	}
	if has, _ := r.InboxHas(ctx, "p.a", "entries", "x"); !has {
		t.Fatal("inbox row missing")
	}
	r.limits.inboxMax = 2
	_ = r.InboxAdd(ctx, "p.a", "entries", "y", "t", now.Add(-2*time.Second))
	_ = r.InboxAdd(ctx, "p.a", "entries", "z", "t", now.Add(-time.Second))
	_ = r.InboxAdd(ctx, "p.a", "entries", "w", "t", now)
	if err := r.InboxTrim(ctx, now); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM plugin_record_inbox`).Scan(&n)
	if n != 2 {
		t.Fatalf("inbox rows after trim = %d, want cap 2 (and the 91-day row gone)", n)
	}
}

func TestSnapshot_SetGetBumpOnlyOnChange_3697(t *testing.T) {
	r, adm := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, found, _ := r.SnapshotGet(ctx, "p.a", "staff"); found {
		t.Fatal("unset snapshot found")
	}
	changed, err := r.SnapshotSet(ctx, "p.a", "staff", []byte(`[{"id":"1"}]`), now)
	if err != nil || !changed {
		t.Fatalf("first set: changed=%v err=%v", changed, err)
	}
	gen1, _ := adm.AdminGeneration(ctx)
	changed, _ = r.SnapshotSet(ctx, "p.a", "staff", []byte(`[{"id":"1"}]`), now)
	if changed {
		t.Fatal("identical bytes must not count as a change")
	}
	if gen2, _ := adm.AdminGeneration(ctx); gen2 != gen1 {
		t.Fatalf("unchanged set bumped generation %d -> %d", gen1, gen2)
	}
	changed, _ = r.SnapshotSet(ctx, "p.a", "staff", []byte(`[{"id":"2"}]`), now)
	if !changed {
		t.Fatal("changed bytes must store")
	}
	got, found, _ := r.SnapshotGet(ctx, "p.a", "staff")
	if !found || string(got) != `[{"id":"2"}]` {
		t.Fatalf("get = %s %v", got, found)
	}
	var ver int
	_ = r.db.QueryRow(`SELECT version FROM plugin_snapshot WHERE plugin_id='p.a'`).Scan(&ver)
	if ver != 2 {
		t.Fatalf("version = %d, want 2", ver)
	}
	if gen3, _ := adm.AdminGeneration(ctx); gen3 == gen1 {
		t.Fatal("a changed snapshot must bump the admin generation")
	}
}

func TestSnapshot_Bounds_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	big := []byte(`{"x":"` + strings.Repeat("y", SnapshotMaxBytes) + `"}`)
	if _, err := r.SnapshotSet(ctx, "p.a", "c", big, now); err != ErrRecordTooLarge {
		t.Fatalf("oversized snapshot err = %v, want ErrRecordTooLarge", err)
	}
	r.limits.snapPerPlugin = 2
	r.limits.snapPerTill = 3
	_, _ = r.SnapshotSet(ctx, "p.a", "c1", []byte(`{}`), now)
	_, _ = r.SnapshotSet(ctx, "p.a", "c2", []byte(`{}`), now)
	if _, err := r.SnapshotSet(ctx, "p.a", "c3", []byte(`{}`), now); err != ErrRecordQuota {
		t.Fatalf("3rd collection per plugin err = %v, want ErrRecordQuota", err)
	}
	_, _ = r.SnapshotSet(ctx, "p.b", "c1", []byte(`{}`), now)
	if _, err := r.SnapshotSet(ctx, "p.c", "c1", []byte(`{}`), now); err != ErrRecordQuota {
		t.Fatalf("collection over the till cap err = %v, want ErrRecordQuota", err)
	}
	// updating an existing collection never counts against the cap
	if _, err := r.SnapshotSet(ctx, "p.a", "c1", []byte(`{"a":1}`), now); err != nil {
		t.Fatalf("update of an existing collection: %v", err)
	}
}

func TestAdminBundle_PluginSnapshotsRoundTripAndFingerprint_3697(t *testing.T) {
	r, adm := recordsRepo(t)
	ctx := context.Background()
	empty, err := adm.DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.PluginSnapshots) != 0 {
		t.Fatal("no snapshots => empty section")
	}
	base := AdminBundle{Tables: empty.Tables}.Fingerprint()
	if empty.Fingerprint() != base {
		t.Fatal("a till without snapshots must keep the pre-snapshot fingerprint")
	}
	_, _ = r.SnapshotSet(ctx, "p.a", "staff", []byte(`[{"id":"1"}]`), time.Now().UTC())
	b, err := adm.DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.PluginSnapshots) != 1 || b.PluginSnapshots[0].PluginID != "p.a" || string(b.PluginSnapshots[0].Payload) != `[{"id":"1"}]` {
		t.Fatalf("bundle snapshots = %+v", b.PluginSnapshots)
	}
	if b.Fingerprint() == base {
		t.Fatal("snapshots must change the fingerprint")
	}
}

func TestAdminBundle_ApplyReplacesSnapshotsOnReplica_3697(t *testing.T) {
	d := b8OpenDB(t, "plugin-records-replica.db")
	ctx := context.Background()
	if _, err := d.DB.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('sync.primary_url','http://main',?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	r := NewPluginRecordsRepo(d.DB)
	_, _ = r.SnapshotSet(ctx, "p.stale", "x", []byte(`{}`), time.Now().UTC())
	adm := NewSyncAdminRepo(d.DB)
	bundle := AdminBundle{Tables: map[string][]map[string]any{}, PluginSnapshots: []PluginSnapshotRow{
		{PluginID: "p.a", Collection: "staff", Version: 3, Payload: []byte(`[{"id":"9"}]`)},
	}}
	if _, err := adm.ApplyAdminWithResult(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	got, found, _ := r.SnapshotGet(ctx, "p.a", "staff")
	if !found || string(got) != `[{"id":"9"}]` {
		t.Fatalf("replica snapshot = %s %v", got, found)
	}
	if _, found, _ := r.SnapshotGet(ctx, "p.stale", "x"); found {
		t.Fatal("the primary wins: snapshots it no longer has are deleted")
	}
}

func TestAdminBundle_ApplyWithoutSectionClearsSnapshots_3697(t *testing.T) {
	// The primary wins and a replica never writes snapshots itself, so a
	// bundle with no section means the primary holds none (omitempty): a
	// snapshot the primary deleted must propagate.
	d := b8OpenDB(t, "plugin-records-no-section.db")
	ctx := context.Background()
	_, _ = d.DB.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('sync.primary_url','http://main',?)`, time.Now().UTC())
	r := NewPluginRecordsRepo(d.DB)
	_, _ = r.SnapshotSet(ctx, "p.a", "x", []byte(`{}`), time.Now().UTC())
	if _, err := NewSyncAdminRepo(d.DB).ApplyAdminWithResult(ctx, AdminBundle{Tables: map[string][]map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := r.SnapshotGet(ctx, "p.a", "x"); found {
		t.Fatal("a bundle without the section means the primary holds no snapshots")
	}
}

func TestPromoteReplica_RejectsQueuedPluginRecords_3697(t *testing.T) {
	d := b8OpenDB(t, "plugin-records-promote.db")
	ctx := context.Background()
	r := NewPluginRecordsRepo(d.DB)
	_, _ = r.Enqueue(ctx, "p.a", "entries", "a", []byte(`{}`), time.Now().UTC())
	if err := NewSettingsRepo(d.DB).PromoteReplica(ctx); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, r, "status='rejected' AND reason='till_promoted'"); n != 1 {
		t.Fatalf("promote must reject queued records, got %d", n)
	}
	if n := outboxCount(t, r, "status='pending'"); n != 0 {
		t.Fatal("no pending row may survive a promote")
	}
}

// Tester gap (mutation OR REPLACE): a re-enqueue with different bytes must
// keep the first row (payload, seq, status), including after it was rejected.
func TestRecordsEnqueue_DuplicateKeepsFirstRow_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := r.Enqueue(ctx, "p.a", "entries", "id-1", []byte(`{"v":1}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE plugin_record_outbox SET status = 'rejected', reason = 'x'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Enqueue(ctx, "p.a", "entries", "id-1", []byte(`{"v":2}`), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var payload, status string
	if err := r.db.QueryRow(`SELECT payload, status FROM plugin_record_outbox`).Scan(&payload, &status); err != nil {
		t.Fatal(err)
	}
	if payload != `{"v":1}` || status != "rejected" {
		t.Fatalf("duplicate must not replace/resurrect the row: %s %s", payload, status)
	}
}

// Review M1: an uninstall drops the plugin's outbox, inbox and snapshot rows
// (and only that plugin's).
func TestDeletePluginRecords_OnlyThatPlugin_3697(t *testing.T) {
	r, _ := recordsRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, p := range []string{"p.a", "p.b"} {
		if _, err := r.Enqueue(ctx, p, "entries", "x", []byte(`{}`), now); err != nil {
			t.Fatal(err)
		}
		if err := r.InboxAdd(ctx, p, "entries", "y", "till-1", now); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SnapshotSet(ctx, p, "staff", []byte(`[]`), now); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := DeletePluginRecords(ctx, tx, "p.a")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("deleted = %d, want 3 (outbox, inbox, snapshot)", n)
	}
	for _, tbl := range []string{"plugin_record_outbox", "plugin_record_inbox", "plugin_snapshot"} {
		var a, b int
		_ = r.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE plugin_id = 'p.a'`).Scan(&a)
		_ = r.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE plugin_id = 'p.b'`).Scan(&b)
		if a != 0 || b != 1 {
			t.Fatalf("%s: p.a=%d (want 0) p.b=%d (want 1)", tbl, a, b)
		}
	}
}
