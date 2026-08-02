package data

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newPairingTestRepo(t *testing.T) *PairingRepo {
	t.Helper()
	d := openMigratedDB(t, "pairing.db")
	return NewPairingRepo(d.DB)
}

func TestPairingRepo_CreateAndListPending(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	id, err := repo.CreatePendingRequest(ctx, "Kitchen Till", "deadbeef", 10*time.Minute)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty pending-request id")
	}

	list, err := repo.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].DeviceName != "Kitchen Till" || list[0].Commitment != "deadbeef" {
		t.Fatalf("unexpected pending list: %+v", list)
	}
	if list[0].Status != "pending" {
		t.Fatalf("expected status=pending, got %q", list[0].Status)
	}
}

func TestPairingRepo_ListPendingExcludesExpired(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	// A request that's already expired must never show up for the manager.
	id, err := repo.CreatePendingRequest(ctx, "Stale Till", "aaaa", -1*time.Minute)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}

	list, err := repo.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected expired request excluded from ListPending, got %+v", list)
	}

	// GetByID must also refuse to hand back an expired row.
	_, ok, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if ok {
		t.Fatal("expected GetByID to report not-found for an expired pending row")
	}
}

func TestPairingRepo_ApproveSetsTokenAndStatus(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	id, err := repo.CreatePendingRequest(ctx, "Bar Till", "commit123", 10*time.Minute)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}

	if err := repo.Approve(ctx, id, "tok-abc", 10*time.Minute); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	row, ok, err := repo.GetByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetByID after approve: ok=%v err=%v", ok, err)
	}
	if row.Status != "approved" || row.Token != "tok-abc" {
		t.Fatalf("expected approved status + token set, got %+v", row)
	}

	// Approved rows must no longer surface in the manager's pending list.
	list, err := repo.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected approved request to drop out of ListPending, got %+v", list)
	}
}

func TestPairingRepo_ApproveExtendsExpiry(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	// Approve a request that still has a very short time left on its
	// ORIGINAL ttl, then wait past what that original deadline would have
	// been. If Approve didn't actually extend expires_at, the row would
	// look expired now; it must not — approving must not hand the
	// manager a success response for a pairing that's about to become
	// unreachable to the replica.
	id, err := repo.CreatePendingRequest(ctx, "Almost Expired Till", "commit789", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}
	if err := repo.Approve(ctx, id, "tok-fresh", 10*time.Minute); err != nil {
		t.Fatalf("Approve on a near-expiry row: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // past the ORIGINAL 50ms expiry

	row, ok, err := repo.GetByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("expected the approved row still retrievable past its original expiry: ok=%v err=%v", ok, err)
	}
	if row.Token != "tok-fresh" {
		t.Fatalf("expected the fresh token, got %+v", row)
	}
}

func TestPairingRepo_ApproveTwiceReturnsErrNotPending(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	id, err := repo.CreatePendingRequest(ctx, "Double Approve Till", "commitABC", 10*time.Minute)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}
	if err := repo.Approve(ctx, id, "tok-1", 10*time.Minute); err != nil {
		t.Fatalf("first Approve: %v", err)
	}
	// A second approve (e.g. two concurrent manager clicks) must not
	// silently mint and leak a second token.
	err = repo.Approve(ctx, id, "tok-2", 10*time.Minute)
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected ErrNotPending on a second approve, got %v", err)
	}
	row, _, _ := repo.GetByID(ctx, id)
	if row.Token != "tok-1" {
		t.Fatalf("expected the row to keep the FIRST token, got %q", row.Token)
	}
}

func TestPairingRepo_DenyRemovesRow(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	id, err := repo.CreatePendingRequest(ctx, "Patio Till", "commit456", 10*time.Minute)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}

	if err := repo.Deny(ctx, id); err != nil {
		t.Fatalf("Deny: %v", err)
	}

	_, ok, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after deny: %v", err)
	}
	if ok {
		t.Fatal("expected the row to be gone after Deny, not just marked denied")
	}
}
