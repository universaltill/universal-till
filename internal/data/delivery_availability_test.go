package data

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ADR-0215 decision 2 / ADR-0201 §2 (ut-docs#6583, S7a till side): an item's
// "unavailable until" and the store's busy/pause state are neutral signals.
// The till is the truth; nothing here blocks checkout.

func availabilityEnv(t *testing.T) (*DeliveryAvailabilityRepo, func(q string, args ...any) float64) {
	t.Helper()
	d, _ := openChannelOrders(t)
	mustExecCO(t, d, `INSERT INTO users (id, username, display_name, role) VALUES ('u1','u1','Staff One','manager')`)
	return NewDeliveryAvailabilityRepo(d), func(q string, args ...any) float64 { return scalarCO(t, d, q, args...) }
}

func TestAvailability_SetItemUnavailableStoresFieldAuditsAndQueuesSignal(t *testing.T) {
	repo, n := availabilityEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	until := now.Add(6 * time.Hour)
	if err := repo.SetItemUnavailable(ctx, "itm1", until, "u1", now); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := repo.db.QueryRow(`SELECT unavailable_until FROM items WHERE id='itm1'`).Scan(&got); err != nil || got != until.Format(time.RFC3339) {
		t.Fatalf("unavailable_until = %q %v", got, err)
	}
	if c := n(`SELECT COUNT(*) FROM audit_log WHERE action='item_unavailable_set' AND entity_id='itm1' AND actor_id='u1'`); c != 1 {
		t.Fatalf("audit rows = %v", c)
	}
	sigs, err := repo.PendingSignals(ctx, 10)
	if err != nil || len(sigs) != 1 || sigs[0].Kind != SignalAvailabilitySet {
		t.Fatalf("signals = %+v %v", sigs, err)
	}
	var p struct {
		Items []struct {
			ItemID    string `json:"item_id"`
			SKU       string `json:"sku"`
			Available bool   `json:"available"`
			Until     string `json:"until"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(sigs[0].PayloadJSON), &p); err != nil || len(p.Items) != 1 ||
		p.Items[0].ItemID != "itm1" || p.Items[0].SKU != "LS-M-BLU" || p.Items[0].Available || p.Items[0].Until != until.Format(time.RFC3339) {
		t.Fatalf("payload = %s %v", sigs[0].PayloadJSON, err)
	}
}

func TestAvailability_IndefiniteAndClear(t *testing.T) {
	repo, _ := availabilityEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.SetItemUnavailable(ctx, "itm1", time.Time{}, "u1", now); err != nil {
		t.Fatal(err)
	}
	var got string
	_ = repo.db.QueryRow(`SELECT unavailable_until FROM items WHERE id='itm1'`).Scan(&got)
	if got != UnavailableIndefinitely {
		t.Fatalf("indefinite stored as %q", got)
	}
	if err := repo.ClearItemUnavailable(ctx, "itm1", "u1", now); err != nil {
		t.Fatal(err)
	}
	_ = repo.db.QueryRow(`SELECT unavailable_until FROM items WHERE id='itm1'`).Scan(&got)
	if got != "" {
		t.Fatalf("cleared = %q", got)
	}
	sigs, _ := repo.PendingSignals(ctx, 10)
	if len(sigs) != 2 || !json.Valid([]byte(sigs[1].PayloadJSON)) {
		t.Fatalf("signals = %+v", sigs)
	}
}

func TestAvailability_UnknownItemAndPastTimeRefused(t *testing.T) {
	repo, _ := availabilityEnv(t)
	now := time.Now().UTC()
	if err := repo.SetItemUnavailable(context.Background(), "nope", now.Add(time.Hour), "u1", now); err == nil {
		t.Fatal("unknown item must be refused")
	}
	if err := repo.SetItemUnavailable(context.Background(), "itm1", now.Add(-time.Hour), "u1", now); err == nil {
		t.Fatal("a time in the past must be refused")
	}
}

func TestAvailability_ExpiryReturnsItemAndQueuesAvailableSignal(t *testing.T) {
	repo, n := availabilityEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = repo.SetItemUnavailable(ctx, "itm1", now.Add(time.Hour), "u1", now)
	_ = repo.SetItemUnavailable(ctx, "itm2", time.Time{}, "u1", now)
	cleared, err := repo.ExpireDue(ctx, now.Add(2*time.Hour))
	if err != nil || cleared != 1 {
		t.Fatalf("cleared = %d %v", cleared, err)
	}
	var a, b string
	_ = repo.db.QueryRow(`SELECT unavailable_until FROM items WHERE id='itm1'`).Scan(&a)
	_ = repo.db.QueryRow(`SELECT unavailable_until FROM items WHERE id='itm2'`).Scan(&b)
	if a != "" || b != UnavailableIndefinitely {
		t.Fatalf("itm1=%q itm2=%q", a, b)
	}
	if c := n(`SELECT COUNT(*) FROM audit_log WHERE action='item_unavailable_expired'`); c != 1 {
		t.Fatalf("expiry audit = %v", c)
	}
	if again, _ := repo.ExpireDue(ctx, now.Add(2*time.Hour)); again != 0 {
		t.Fatalf("second expiry cleared %d", again)
	}
}

func TestAvailability_SnapshotIsTheTillsTruthForReconcile(t *testing.T) {
	repo, _ := availabilityEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = repo.SetItemUnavailable(ctx, "itm1", now.Add(time.Hour), "u1", now)
	_ = repo.SetService(ctx, ServiceState{State: ServicePaused, Reason: "busy", PrepMinutes: 40}, "u1", now)
	snap, err := repo.Snapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Unavailable) != 1 || snap.Unavailable[0].ItemID != "itm1" || snap.Service.State != ServicePaused || snap.Service.PrepMinutes != 40 {
		t.Fatalf("snapshot = %+v", snap)
	}
	// An expired item is not in the snapshot even before ExpireDue ran.
	snap, _ = repo.Snapshot(ctx, now.Add(2*time.Hour))
	if len(snap.Unavailable) != 0 {
		t.Fatalf("expired item in snapshot: %+v", snap.Unavailable)
	}
	// Re-asserting queues one availability.set with every unavailable item and one service.set: till wins on a provider unsnooze.
	if err := repo.Reassert(ctx, now, "u1"); err != nil {
		t.Fatal(err)
	}
	sigs, _ := repo.PendingSignals(ctx, 20)
	kinds := map[string]int{}
	for _, s := range sigs {
		kinds[s.Kind]++
	}
	if kinds[SignalAvailabilitySet] != 2 || kinds[SignalServiceSet] != 2 {
		t.Fatalf("signals by kind = %v", kinds)
	}
}

func TestAvailability_ServiceStateAuditedValidatedAndQueued(t *testing.T) {
	repo, n := availabilityEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.SetService(ctx, ServiceState{State: "closed"}, "u1", now); err == nil {
		t.Fatal("unknown state must be refused")
	}
	if err := repo.SetService(ctx, ServiceState{State: ServicePaused, Reason: "x", PrepMinutes: DeliveryMaxPrepMinutes + 1}, "u1", now); err == nil {
		t.Fatal("prep minutes out of range must be refused")
	}
	if err := repo.SetService(ctx, ServiceState{State: ServicePaused, Reason: "busy", Until: now.Add(time.Hour).Format(time.RFC3339)}, "u1", now); err != nil {
		t.Fatal(err)
	}
	st, err := repo.Service(ctx)
	if err != nil || st.State != ServicePaused || st.Reason != "busy" {
		t.Fatalf("service = %+v %v", st, err)
	}
	if c := n(`SELECT COUNT(*) FROM audit_log WHERE action='delivery_service_set' AND actor_id='u1'`); c != 1 {
		t.Fatalf("audit = %v", c)
	}
	sigs, _ := repo.PendingSignals(ctx, 10)
	if len(sigs) != 1 || sigs[0].Kind != SignalServiceSet {
		t.Fatalf("signals = %+v", sigs)
	}
	st0, _ := NewDeliveryAvailabilityRepo(repo.db).Service(ctx)
	if st0.State != ServicePaused {
		t.Fatal("service state must persist")
	}
}

func TestAvailability_DefaultServiceIsOpen(t *testing.T) {
	repo, _ := availabilityEnv(t)
	st, err := repo.Service(context.Background())
	if err != nil || st.State != ServiceOpen {
		t.Fatalf("default = %+v %v", st, err)
	}
}

func TestAvailability_MarkSent(t *testing.T) {
	repo, _ := availabilityEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = repo.SetService(ctx, ServiceState{State: ServicePaused}, "u1", now)
	sigs, _ := repo.PendingSignals(ctx, 10)
	if err := repo.MarkSent(ctx, []string{sigs[0].SignalID}, now); err != nil {
		t.Fatal(err)
	}
	if left, _ := repo.PendingSignals(ctx, 10); len(left) != 0 {
		t.Fatalf("pending after sent = %+v", left)
	}
}

// Nothing blocks checkout: an unavailable item is still sellable, because no
// sale path reads the field.
func TestAvailability_UnavailableItemStillSellsAtTheTill(t *testing.T) {
	repo, _ := availabilityEnv(t)
	now := time.Now().UTC()
	if err := repo.SetItemUnavailable(context.Background(), "itm1", time.Time{}, "u1", now); err != nil {
		t.Fatal(err)
	}
	// The same lookup the till's scan uses must still find it.
	var id string
	if err := repo.db.QueryRow(`SELECT id FROM items WHERE sku = 'LS-M-BLU' AND is_active = 1`).Scan(&id); err != nil || id != "itm1" {
		t.Fatalf("item lookup = %q %v", id, err)
	}
}

// The structural half of "nothing blocks checkout": the only non-test code
// that mentions the field is the availability repo itself, so no sale, basket
// or payment path can read it.
func TestAvailability_NoOtherCodeReadsTheField(t *testing.T) {
	var hits []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if b, rerr := os.ReadFile(path); rerr == nil && strings.Contains(string(b), "unavailable_until") &&
			filepath.Base(path) != "delivery_availability_repo.go" && filepath.Base(path) != "delivery_api.go" /* names the JSON key only */ {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("code outside the availability repo reads unavailable_until: %v", hits)
	}
}
