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

	// A fake clock, not wall time: expires_at is stored at RFC3339 second
	// precision, so a real sub-second TTL fails whenever a second boundary
	// or a busy CI runner falls between create and approve (ut-docs#4040).
	clock := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return clock }

	// Approve a request with one second left on its ORIGINAL ttl, then
	// move past what that original deadline would have been. If Approve
	// didn't actually extend expires_at, the row would look expired now;
	// it must not — approving must not hand the manager a success
	// response for a pairing that's about to become unreachable to the
	// replica.
	id, err := repo.CreatePendingRequest(ctx, "Almost Expired Till", "commit789", 2*time.Second)
	if err != nil {
		t.Fatalf("CreatePendingRequest: %v", err)
	}
	clock = clock.Add(1 * time.Second)
	if err := repo.Approve(ctx, id, "tok-fresh", 10*time.Minute); err != nil {
		t.Fatalf("Approve on a near-expiry row: %v", err)
	}
	clock = clock.Add(5 * time.Second) // past the ORIGINAL 2s expiry

	row, ok, err := repo.GetByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("expected the approved row still retrievable past its original expiry: ok=%v err=%v", ok, err)
	}
	if row.Token != "tok-fresh" {
		t.Fatalf("expected the fresh token, got %+v", row)
	}

	// Control: the same timeline without the approve does expire, so the
	// assertion above is proving the extension, not a clock that's ignored.
	clock = time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)
	unapproved, err := repo.CreatePendingRequest(ctx, "Unapproved Till", "commitDEF", 2*time.Second)
	if err != nil {
		t.Fatalf("CreatePendingRequest (control): %v", err)
	}
	clock = clock.Add(6 * time.Second)
	if _, ok, err := repo.GetByID(ctx, unapproved); err != nil || ok {
		t.Fatalf("expected the unapproved row expired past its 2s ttl: ok=%v err=%v", ok, err)
	}
	if err := repo.Approve(ctx, unapproved, "tok-late", 10*time.Minute); !errors.Is(err, ErrNotPending) {
		t.Fatalf("Approve past the original expiry: got %v, want ErrNotPending", err)
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

// ut-docs#4091: the pin served on the request's connection is stored and
// read back by every read path; a plain-HTTP request stores none.
func TestPairingRepo_ServedPinRoundTrips(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)

	pinned, err := repo.CreatePendingRequestWithRole(ctx, "TLS Till", "c-tls", TillRoleAdditional, "ab12", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := repo.CreatePendingRequest(ctx, "Plain Till", "c-plain", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{pinned: "ab12", plain: ""}

	row, ok, err := repo.GetByID(ctx, pinned)
	if err != nil || !ok || row.ServedPin != "ab12" {
		t.Fatalf("GetByID: pin %q ok=%v err=%v, want ab12", row.ServedPin, ok, err)
	}
	for name, list := range map[string]func(context.Context) ([]PendingPairingRow, error){
		"ListPending": repo.ListPending, "ListPendingReadOnly": repo.ListPendingReadOnly,
	} {
		rows, err := list(ctx)
		if err != nil || len(rows) != 2 {
			t.Fatalf("%s: %d rows, err %v", name, len(rows), err)
		}
		for _, r := range rows {
			if r.ServedPin != want[r.ID] {
				t.Errorf("%s: %s pin %q, want %q", name, r.DeviceName, r.ServedPin, want[r.ID])
			}
		}
	}
}

// ut-docs#6796: a denied card_reader request leaves a tombstone until expiry
// so the phone can be told; a till request is still deleted outright.
func TestPairingRepo_DenyReaderLeavesTombstone(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)
	for _, approve := range []bool{false, true} {
		id, err := repo.CreatePendingRequestWithRole(ctx, "Phone", "c1", TillRoleCardReader, "", 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if approve {
			if err := repo.ApproveWithRole(ctx, id, "tok", "", time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		if err := repo.Deny(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := repo.Deny(ctx, id); err != nil { // idempotent
			t.Fatal(err)
		}
		row, ok, err := repo.GetByID(ctx, id)
		if err != nil || !ok {
			t.Fatalf("tombstone missing: ok=%v err=%v", ok, err)
		}
		if row.Status != "denied" || row.Token != "" {
			t.Fatalf("want denied with empty token, got %q %q", row.Status, row.Token)
		}
		if err := repo.ApproveWithRole(ctx, id, "tok2", "", time.Minute); !errors.Is(err, ErrNotPending) {
			t.Fatalf("approve on denied: want ErrNotPending, got %v", err)
		}
		if ok, _ := repo.Consume(ctx, id); ok {
			t.Fatal("a denied row must not be consumable")
		}
	}
	if l, _ := repo.ListPending(ctx); len(l) != 0 {
		t.Fatalf("denied row listed: %v", l)
	}
	if l, _ := repo.ListPendingReadOnly(ctx); len(l) != 0 {
		t.Fatalf("denied row listed read-only: %v", l)
	}
}

func TestPairingRepo_DenyTillRowIsDeleted(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)
	id, err := repo.CreatePendingRequestWithRole(ctx, "Till", "c1", "additional", "", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Deny(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.GetByID(ctx, id); ok {
		t.Fatal("till row should be deleted")
	}
}

func TestPairingRepo_TombstoneSweptAfterExpiry(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)
	now := time.Now()
	repo.now = func() time.Time { return now }
	id, _ := repo.CreatePendingRequestWithRole(ctx, "Phone", "c1", TillRoleCardReader, "", time.Minute)
	if err := repo.Deny(ctx, id); err != nil {
		t.Fatal(err)
	}
	repo.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, _, ok, _ := repo.GetByIDForPoll(ctx, id); !ok {
		t.Fatal("expired-unswept row should still be visible to the poll")
	}
	if _, err := repo.ListPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := repo.GetByIDForPoll(ctx, id); ok {
		t.Fatal("tombstone should be swept after expiry")
	}
}

func TestPairingRepo_GetByIDForPollReportsExpired(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)
	now := time.Now()
	repo.now = func() time.Time { return now }
	id, _ := repo.CreatePendingRequestWithRole(ctx, "Phone", "c1", TillRoleCardReader, "", time.Minute)
	row, expired, ok, err := repo.GetByIDForPoll(ctx, id)
	if err != nil || !ok || expired || row.ID != id {
		t.Fatalf("live row: ok=%v expired=%v err=%v", ok, expired, err)
	}
	repo.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, expired, ok, _ = repo.GetByIDForPoll(ctx, id)
	if !ok || !expired {
		t.Fatalf("want ok+expired, got ok=%v expired=%v", ok, expired)
	}
	if _, ok, _ := repo.GetByID(ctx, id); ok {
		t.Fatal("GetByID must still hide expired rows")
	}
	if _, _, ok, _ := repo.GetByIDForPoll(ctx, "nope"); ok {
		t.Fatal("unknown id")
	}
}

// T7 (ut-docs#7097, AC16): one pending request per requesting device. A new
// request from the same device replaces its older pending one; other devices,
// and requests that carry no device id (older tills), are untouched.
func TestPairingRepo_OnePendingRequestPerDevice(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)
	const phone = "till-11111111-1111-4111-8111-111111111111"
	const other = "till-22222222-2222-4222-8222-222222222222"
	mk := func(name, commit, dev string) string {
		t.Helper()
		id, err := repo.CreatePendingRequestForDevice(ctx, name, commit, TillRoleAdditional, "", dev, 10*time.Minute)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return id
	}
	mk("Phone", "c1", phone)
	mk("Phone", "c2", phone)
	last := mk("Phone", "c3", phone)
	otherID := mk("Tablet", "c4", other)
	legacy1 := mk("Old A", "c5", "")
	legacy2 := mk("Old A", "c6", "")

	list, err := repo.ListPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range list {
		got[p.ID] = true
	}
	if len(list) != 4 || !got[last] || !got[otherID] || !got[legacy1] || !got[legacy2] {
		t.Fatalf("pending = %+v, want exactly the newest phone request, the other device and both id-less requests", list)
	}
}

// An already approved request of the same device is not touched by a new one.
func TestPairingRepo_NewRequestKeepsAnApprovedOneOfTheSameDevice(t *testing.T) {
	ctx := context.Background()
	repo := newPairingTestRepo(t)
	const dev = "till-33333333-3333-4333-8333-333333333333"
	first, err := repo.CreatePendingRequestForDevice(ctx, "Phone", "c1", TillRoleAdditional, "", dev, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Approve(ctx, first, "tok", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePendingRequestForDevice(ctx, "Phone", "c2", TillRoleAdditional, "", dev, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	row, _, ok, err := repo.GetByIDForPoll(ctx, first)
	if err != nil || !ok || row.Status != "approved" {
		t.Fatalf("approved row after a new request: ok=%v status=%q err=%v", ok, row.Status, err)
	}
}

// T7 (ut-docs#7097): an enrolled till remembers the device id it joined with,
// so a re-join under a new name finds its row.
func TestTillsRepo_DeviceIDLookup(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "tills-device.db")
	repo := NewTillsRepo(d.DB)
	id, err := repo.InsertTill(ctx, "Phone", "h1")
	if err != nil {
		t.Fatal(err)
	}
	const dev = "till-66666666-6666-4666-8666-666666666666"
	if _, ok, err := repo.TillByDeviceID(ctx, dev); err != nil || ok {
		t.Fatalf("before: ok=%v err=%v, want not found", ok, err)
	}
	if err := repo.SetDeviceID(ctx, id, dev); err != nil {
		t.Fatal(err)
	}
	row, ok, err := repo.TillByDeviceID(ctx, dev)
	if err != nil || !ok || row.ID != id {
		t.Fatalf("after: %+v ok=%v err=%v, want row %s", row, ok, err, id)
	}
	if _, ok, _ := repo.TillByDeviceID(ctx, ""); ok {
		t.Fatal("an empty device id must never match a row")
	}
}
