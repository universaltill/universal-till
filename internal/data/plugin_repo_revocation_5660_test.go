package data

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ut-docs#5660: RevocationNotices recovers why a plugin was turned off from
// the latest matching disable_revoked audit row.

func seedRevokedPlugin(t *testing.T, id, version string) (*PluginRepo, context.Context) {
	t.Helper()
	d, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	seedCatalogEntry(t, d, id, version)
	if err := repo.InstallPlugin(ctx, nil, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPluginState(ctx, id, version, PluginStateRevoked, false); err != nil {
		t.Fatal(err)
	}
	return repo, ctx
}

func auditRevoked(t *testing.T, repo *PluginRepo, ctx context.Context, id, version, reason string, ts time.Time) {
	t.Helper()
	if err := repo.InsertAuditRaw(ctx, nil, "disable_revoked", "plugin", id, map[string]any{"version": version, "reason": reason}, ts); err != nil {
		t.Fatal(err)
	}
}

func TestRevocationNotices_LatestMatchingVersionWins(t *testing.T) {
	repo, ctx := seedRevokedPlugin(t, "com.example.rev", "1.0.0")
	now := time.Now().UTC().Truncate(time.Second)
	auditRevoked(t, repo, ctx, "com.example.rev", "0.9.0", "old version reason", now.Add(-3*time.Hour))
	auditRevoked(t, repo, ctx, "com.example.rev", "1.0.0", "first reason", now.Add(-2*time.Hour))
	auditRevoked(t, repo, ctx, "com.example.rev", "1.0.0", "second reason", now.Add(-1*time.Hour))

	got, err := repo.RevocationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := got["com.example.rev"]
	if !ok || len(got) != 1 {
		t.Fatalf("want one notice, got %+v", got)
	}
	if n.Version != "1.0.0" || n.Reason != "second reason" {
		t.Fatalf("notice = %+v", n)
	}
	if !n.RevokedAt.Equal(now.Add(-1 * time.Hour)) {
		t.Fatalf("RevokedAt = %v", n.RevokedAt)
	}
}

func TestRevocationNotices_LegacyRevokedRowWithoutAudit(t *testing.T) {
	repo, ctx := seedRevokedPlugin(t, "com.example.legacy", "2.0.0")
	got, err := repo.RevocationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := got["com.example.legacy"]
	if !ok {
		t.Fatalf("legacy revoked row must still get a notice: %+v", got)
	}
	if n.Version != "2.0.0" || n.Reason != "" || !n.RevokedAt.IsZero() {
		t.Fatalf("notice = %+v", n)
	}
}

func TestRevocationNotices_StateNotRevokedOmitted(t *testing.T) {
	repo, ctx := seedRevokedPlugin(t, "com.example.upd", "1.0.0")
	auditRevoked(t, repo, ctx, "com.example.upd", "1.0.0", "bad", time.Now())
	if err := repo.UpdatePluginInstallState(ctx, "com.example.upd", PluginStateInstalled); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RevocationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want no notices after update, got %+v", got)
	}
}

func TestCapRevocationReason(t *testing.T) {
	if got := CapRevocationReason("  line one\n\tline  two\r\n "); got != "line one line two" {
		t.Fatalf("collapse = %q", got)
	}
	long := strings.Repeat("é", 500)
	got := CapRevocationReason(long)
	if utf8.RuneCountInString(got) != 200 || !strings.HasSuffix(got, "…") {
		t.Fatalf("cap: %d runes, %q", utf8.RuneCountInString(got), got[len(got)-4:])
	}
	if CapRevocationReason(strings.Repeat("a", 200)) != strings.Repeat("a", 200) {
		t.Fatal("exactly 200 runes must not be truncated")
	}
}

// Tester (ut-docs#5660): the version filter must hold even when a NEWER
// audit row belongs to another version (e.g. 2.0.0 was withdrawn later and
// the till rolled back to the earlier-withdrawn 1.0.0). Without this case the
// test above passes with the version filter removed.
func TestRevocationNotices_NewerOtherVersionRowIgnored(t *testing.T) {
	repo, ctx := seedRevokedPlugin(t, "com.example.rb", "1.0.0")
	now := time.Now().UTC().Truncate(time.Second)
	auditRevoked(t, repo, ctx, "com.example.rb", "1.0.0", "reason for 1.0.0", now.Add(-2*time.Hour))
	auditRevoked(t, repo, ctx, "com.example.rb", "2.0.0", "reason for 2.0.0", now.Add(-1*time.Hour))

	got, err := repo.RevocationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := got["com.example.rb"]; n.Version != "1.0.0" || n.Reason != "reason for 1.0.0" || !n.RevokedAt.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("notice = %+v, want the 1.0.0 row", n)
	}
}
