package data

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// ut-docs#5198: rotation of a replica's LAN sync bearer.

var rotationNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tillCols(t *testing.T, r *TillsRepo, id string) (cur, prev, issued, exp sql.NullString) {
	t.Helper()
	if err := r.db.QueryRow(`SELECT bearer_hash, bearer_prev_hash, bearer_issued_at, bearer_prev_expires_at FROM tills WHERE id = ?`, id).
		Scan(&cur, &prev, &issued, &exp); err != nil {
		t.Fatal(err)
	}
	return
}

func TestEnrolTill_SetsBearerIssuedAt(t *testing.T) {
	repo := newTillsTestDB(t)
	before := time.Now().UTC().Add(-time.Second)
	id, err := repo.InsertTill(context.Background(), "Till 2", "h1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, issued, _ := tillCols(t, repo, id)
	got, err := time.Parse(time.RFC3339, issued.String)
	if !issued.Valid || err != nil || got.Before(before) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("bearer_issued_at = %v (parse err %v), want ~now in RFC3339", issued, err)
	}
}

func TestTillByBearerHash_LegacyBearerIsRotateDue(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()
	id, _ := repo.InsertTill(ctx, "Till 2", "h1")

	got, ok, err := repo.TillByBearerHashAt(ctx, "h1", time.Now())
	if err != nil || !ok || got.RotateDue {
		t.Fatalf("fresh bearer: ok=%v due=%v err=%v, want ok and not due", ok, got.RotateDue, err)
	}
	// A bearer issued before rotation existed (migration 093 leaves NULL).
	if _, err := repo.db.Exec(`UPDATE tills SET bearer_issued_at = NULL WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ = repo.TillByBearerHashAt(ctx, "h1", time.Now()); !ok || !got.RotateDue {
		t.Fatalf("legacy bearer: ok=%v due=%v, want ok and due", ok, got.RotateDue)
	}
	// An old one is due too; one just inside the max age is not.
	repo.db.Exec(`UPDATE tills SET bearer_issued_at = ? WHERE id = ?`, stamp(rotationNow.Add(-SyncBearerMaxAge-time.Hour)), id)
	if got, _, _ = repo.TillByBearerHashAt(ctx, "h1", rotationNow); !got.RotateDue {
		t.Fatal("a bearer older than SyncBearerMaxAge must be rotate-due")
	}
	repo.db.Exec(`UPDATE tills SET bearer_issued_at = ? WHERE id = ?`, stamp(rotationNow.Add(-SyncBearerMaxAge+time.Hour)), id)
	if got, _, _ = repo.TillByBearerHashAt(ctx, "h1", rotationNow); got.RotateDue {
		t.Fatal("a bearer inside SyncBearerMaxAge must not be rotate-due")
	}
}

func TestRotateBearer_PrevPendingUntilNewBearerUsed(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()
	id, _ := repo.InsertTill(ctx, "Till 2", "old")

	ok, err := repo.RotateBearer(ctx, id, "old", "new", rotationNow)
	if err != nil || !ok {
		t.Fatalf("RotateBearer = %v, %v; want true", ok, err)
	}
	cur, prev, issued, exp := tillCols(t, repo, id)
	if cur.String != "new" || prev.String != "old" || issued.String != stamp(rotationNow) || exp.Valid {
		t.Fatalf("after rotate: cur=%v prev=%v issued=%v exp=%v; want new/old/now/NULL", cur, prev, issued, exp)
	}
	// Hours later the old bearer still works: the grace has not started.
	got, ok, _ := repo.TillByBearerHashAt(ctx, "old", rotationNow.Add(5*time.Hour))
	if !ok || !got.BearerPrev || !got.BearerPending || !got.RotateDue {
		t.Fatalf("pending prev: ok=%v %+v; want accepted, prev, pending, due", ok, got)
	}
	// First use of the new bearer starts the grace window.
	got, ok, _ = repo.TillByBearerHashAt(ctx, "new", rotationNow.Add(6*time.Hour))
	if !ok || got.BearerPrev || got.RotateDue {
		t.Fatalf("new bearer: ok=%v %+v", ok, got)
	}
	_, _, _, exp = tillCols(t, repo, id)
	if exp.String != stamp(rotationNow.Add(6*time.Hour+SyncBearerGrace)) {
		t.Fatalf("grace expiry = %q, want first use + SyncBearerGrace", exp.String)
	}
	// Inside the grace the old bearer is accepted but no longer pending.
	got, ok, _ = repo.TillByBearerHashAt(ctx, "old", rotationNow.Add(6*time.Hour+time.Minute))
	if !ok || !got.BearerPrev || got.BearerPending || got.RotateDue {
		t.Fatalf("prev in grace: ok=%v %+v", ok, got)
	}
	// A later use of the new bearer must not push the expiry out.
	repo.TillByBearerHashAt(ctx, "new", rotationNow.Add(6*time.Hour+5*time.Minute))
	if _, _, _, e2 := tillCols(t, repo, id); e2.String != exp.String {
		t.Fatalf("expiry moved from %q to %q", exp.String, e2.String)
	}
}

func TestRotateBearer_RetryWithPrevWhilePendingReplacesUnusedCurrent(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()
	id, _ := repo.InsertTill(ctx, "Till 2", "old")
	if ok, _ := repo.RotateBearer(ctx, id, "old", "lost", rotationNow); !ok {
		t.Fatal("first rotate refused")
	}
	// The response carrying "lost" never arrived; the replica retries with "old".
	ok, err := repo.RotateBearer(ctx, id, "old", "second", rotationNow.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("retry with the pending prev = %v, %v; want true", ok, err)
	}
	cur, prev, _, exp := tillCols(t, repo, id)
	if cur.String != "second" || prev.String != "old" || exp.Valid {
		t.Fatalf("after retry: cur=%v prev=%v exp=%v; want second/old/NULL", cur, prev, exp)
	}
	if _, ok, _ := repo.TillByBearerHashAt(ctx, "lost", rotationNow.Add(2*time.Minute)); ok {
		t.Fatal("the unused bearer from the lost response must be dead")
	}
	if _, ok, _ := repo.TillByBearerHashAt(ctx, "second", rotationNow.Add(2*time.Minute)); !ok {
		t.Fatal("the replacement bearer must work")
	}
	// An unrelated hash cannot rotate.
	if ok, _ := repo.RotateBearer(ctx, id, "stranger", "x", rotationNow); ok {
		t.Fatal("a hash that is neither current nor pending prev rotated the till")
	}
}

func TestRotateBearer_PrevCannotRotateOnceGraceStarted(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()
	id, _ := repo.InsertTill(ctx, "Till 2", "old")
	repo.RotateBearer(ctx, id, "old", "new", rotationNow)
	repo.TillByBearerHashAt(ctx, "new", rotationNow.Add(time.Minute)) // grace starts

	ok, err := repo.RotateBearer(ctx, id, "old", "evil", rotationNow.Add(2*time.Minute))
	if err != nil || ok {
		t.Fatalf("prev rotated after the new bearer was used: ok=%v err=%v", ok, err)
	}
	if cur, _, _, _ := tillCols(t, repo, id); cur.String != "new" {
		t.Fatalf("current bearer = %q, want new", cur.String)
	}
}

func TestTillByBearerHash_PrevRefusedAfterGrace(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()
	id, _ := repo.InsertTill(ctx, "Till 2", "old")
	repo.RotateBearer(ctx, id, "old", "new", rotationNow)
	repo.TillByBearerHashAt(ctx, "new", rotationNow)

	late := rotationNow.Add(SyncBearerGrace + time.Second)
	got, ok, err := repo.TillByBearerHashAt(ctx, "old", late)
	if err != nil || ok {
		t.Fatalf("expired prev accepted: ok=%v err=%v", ok, err)
	}
	if got.ID != id || !got.BearerPrev {
		t.Fatalf("a refused prev must still name the till for the caller's warning, got %+v", got)
	}
	if _, prev, _, exp := tillCols(t, repo, id); prev.Valid || exp.Valid {
		t.Fatalf("expired prev not cleared: prev=%v exp=%v", prev, exp)
	}
	if _, ok, _ := repo.TillByBearerHashAt(ctx, "new", late); !ok {
		t.Fatal("the current bearer must keep working")
	}
	// Still refused afterwards, and silently (nothing left to name).
	if got, ok, _ := repo.TillByBearerHashAt(ctx, "old", late); ok || got.ID != "" {
		t.Fatalf("second use of the dead bearer: ok=%v %+v", ok, got)
	}
}

func TestTillsRepo_ProofHashesByID(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()
	id, _ := repo.InsertTill(ctx, "Till 2", "old")
	cur, prev, ok, err := repo.ProofHashesByID(ctx, id)
	if err != nil || !ok || cur != "old" || prev != "" {
		t.Fatalf("fresh: %q %q %v %v", cur, prev, ok, err)
	}
	repo.RotateBearer(ctx, id, "old", "new", rotationNow)
	if cur, prev, _, _ = repo.ProofHashesByID(ctx, id); cur != "new" || prev != "old" {
		t.Fatalf("pending: %q %q, want new/old", cur, prev)
	}
	repo.TillByBearerHashAt(ctx, "new", rotationNow) // grace starts: prev is no longer offered
	if cur, prev, _, _ = repo.ProofHashesByID(ctx, id); cur != "new" || prev != "" {
		t.Fatalf("grace started: %q %q, want new/empty", cur, prev)
	}
	if _, _, ok, err := repo.ProofHashesByID(ctx, "nope"); ok || err != nil {
		t.Fatalf("unknown till: ok=%v err=%v", ok, err)
	}
}
