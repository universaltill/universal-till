package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func openDirectiveResultsRepo(t *testing.T) (*db.DB, *DirectiveResultsRepo) {
	t.Helper()
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "directive_results.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewDirectiveResultsRepo(dbo.DB)
}

func TestDirectiveResults_SaveGetFirstWins(t *testing.T) {
	_, repo := openDirectiveResultsRepo(t)
	ctx := context.Background()
	if _, ok, err := repo.Get(ctx, "d1"); err != nil || ok {
		t.Fatalf("empty Get = ok %v err %v", ok, err)
	}
	if err := repo.Save(ctx, DirectiveResult{DirectiveID: "d1", DirectiveType: "adjust_stock", Status: "applied", Message: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, DirectiveResult{DirectiveID: "d1", DirectiveType: "adjust_stock", Status: "failed", Message: "second"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, "d1")
	if err != nil || !ok {
		t.Fatalf("Get ok %v err %v", ok, err)
	}
	if got.Status != "applied" || got.Message != "first" || got.Posted || got.DirectiveType != "adjust_stock" {
		t.Fatalf("got %+v, want the first result, unposted", got)
	}
	if err := repo.Save(ctx, DirectiveResult{}); err == nil {
		t.Fatal("empty directive id must be refused")
	}
}

func TestDirectiveResults_MarkPosted(t *testing.T) {
	_, repo := openDirectiveResultsRepo(t)
	ctx := context.Background()
	_ = repo.Save(ctx, DirectiveResult{DirectiveID: "d1", DirectiveType: "set_price", Status: "applied"})
	if err := repo.MarkPosted(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := repo.Get(ctx, "d1"); !got.Posted {
		t.Fatalf("not marked posted: %+v", got)
	}
}

func TestDirectiveResults_PruneBounds(t *testing.T) {
	dbo, repo := openDirectiveResultsRepo(t)
	ctx := context.Background()
	old := func(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339) }
	for _, r := range []struct {
		id     string
		posted int
		age    time.Duration
	}{
		{"posted-old", 1, 8 * 24 * time.Hour},
		{"posted-new", 1, 6 * 24 * time.Hour},
		{"unposted-old", 0, 31 * 24 * time.Hour},
		{"unposted-new", 0, 29 * 24 * time.Hour},
	} {
		if _, err := dbo.DB.Exec(`INSERT INTO directive_results (directive_id, directive_type, status, message, posted, created_at) VALUES (?, 't', 'applied', '', ?, ?)`,
			r.id, r.posted, old(r.age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"posted-old": false, "posted-new": true, "unposted-old": false, "unposted-new": true} {
		if _, ok, _ := repo.Get(ctx, id); ok != want {
			t.Errorf("%s kept = %v, want %v", id, ok, want)
		}
	}
}
