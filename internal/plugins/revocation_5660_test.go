package plugins

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5660: processRevocation persists why a plugin was turned off.

func revAuditRows(t *testing.T, db *sql.DB, id string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT data_json FROM audit_log WHERE entity_type='plugin' AND entity_id=? AND action='disable_revoked' ORDER BY rowid`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestProcessRevocation_ActivePersistsVersionAndReason(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.rev", "Rev", "1.2.3", "none", true)
	rc := NewRevocationChecker(db, "http://unused", nil)

	ok, err := rc.processRevocation(ctx, RevocationEntry{PluginID: "com.test.rev", Action: "disable", Reason: "  bad\nthing  "})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	notes, err := data.NewPluginRepo(db).RevocationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := notes["com.test.rev"]
	if n.Version != "1.2.3" || n.Reason != "bad thing" || n.RevokedAt.IsZero() {
		t.Fatalf("notice = %+v", n)
	}
}

func TestProcessRevocation_EmptyVersionEntryStoresInstalledVersion(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.all", "All", "4.5.6", "none", true)
	rc := NewRevocationChecker(db, "http://unused", nil)
	if _, err := rc.processRevocation(ctx, RevocationEntry{PluginID: "com.test.all", Version: "", Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	n := mustNotice(t, db, "com.test.all")
	if n.Version != "4.5.6" {
		t.Fatalf("version = %q", n.Version)
	}
}

func mustNotice(t *testing.T, db *sql.DB, id string) data.RevocationNotice {
	t.Helper()
	notes, err := data.NewPluginRepo(db).RevocationNotices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n, ok := notes[id]
	if !ok {
		t.Fatalf("no notice for %s: %+v", id, notes)
	}
	return n
}

func TestProcessRevocation_ReasonIsCapped(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.test.cap", "Cap", "1.0.0", "none", true)
	rc := NewRevocationChecker(db, "http://unused", nil)
	if _, err := rc.processRevocation(context.Background(), RevocationEntry{PluginID: "com.test.cap", Reason: strings.Repeat("x", 900)}); err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(mustNotice(t, db, "com.test.cap").Reason)); got != 200 {
		t.Fatalf("stored reason runes = %d", got)
	}
	for _, raw := range revAuditRows(t, db, "com.test.cap") {
		if len(raw) > 400 {
			t.Fatalf("audit row stores uncapped reason (%d bytes)", len(raw))
		}
	}
}

func TestProcessRevocation_AlreadyDisabledRecordsWithoutStop(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.off", "Off", "1.0.0", "none", false)
	stopped := false
	sup := NewSupervisor(db)
	sup.processes["com.test.off"] = &PluginProcess{PluginID: "com.test.off", cancel: func() { stopped = true }}
	rc := NewRevocationChecker(db, "http://unused", sup)

	ok, err := rc.processRevocation(ctx, RevocationEntry{PluginID: "com.test.off", Reason: "withdrawn"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("an already-disabled plugin must not count as a genuine disable")
	}
	if stopped {
		t.Fatal("StopPlugin must not run for an already-disabled plugin")
	}
	if n := mustNotice(t, db, "com.test.off"); n.Reason != "withdrawn" || n.Version != "1.0.0" {
		t.Fatalf("notice = %+v", n)
	}
	rows := revAuditRows(t, db, "com.test.off")
	if len(rows) != 1 || !strings.Contains(rows[0], `"was_active":false`) {
		t.Fatalf("audit rows = %v", rows)
	}
}

func TestProcessRevocation_AlreadyRevokedInactiveIsNoop(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.twice", "Twice", "1.0.0", "none", false)
	rc := NewRevocationChecker(db, "http://unused", nil)
	for i := 0; i < 2; i++ {
		if ok, err := rc.processRevocation(ctx, RevocationEntry{PluginID: "com.test.twice", Reason: "r"}); err != nil || ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
	}
	if rows := revAuditRows(t, db, "com.test.twice"); len(rows) != 1 {
		t.Fatalf("want 1 audit row after two polls, got %d", len(rows))
	}
}

func TestProcessRevocation_ReenabledRevokedWritesNewRowLastWins(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.re", "Re", "1.0.0", "none", true)
	rc := NewRevocationChecker(db, "http://unused", nil)
	if ok, err := rc.processRevocation(ctx, RevocationEntry{PluginID: "com.test.re", Reason: "first"}); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if err := data.NewPluginRepo(db).SetPluginActive(ctx, nil, "com.test.re", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := rc.processRevocation(ctx, RevocationEntry{PluginID: "com.test.re", Reason: "second"}); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if n := mustNotice(t, db, "com.test.re"); n.Reason != "second" {
		t.Fatalf("reason = %q", n.Reason)
	}
}
