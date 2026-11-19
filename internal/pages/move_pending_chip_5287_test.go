package pages

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/netreach"
)

// ut-docs#5287 item 1: a read-only, anonymous status-bar chip tells every
// role (cashiers too, the sale screen included) that a move of this till
// waits for a manager. It names nobody and has no controls.

const movePendingText = "Till move waiting for a manager"

func applyMoves(t *testing.T, moves ...map[string]any) {
	t.Helper()
	if moves == nil {
		moves = []map[string]any{} // "[]" clears; "null" means the field was absent
	}
	b, err := json.Marshal(moves)
	if err != nil {
		t.Fatal(err)
	}
	enroll.ApplySyncMoves(b, nil)
}

func moveBlock(id, source, exp string) map[string]any {
	return map[string]any{"move_id": id, "source_store": source, "target_store": mvTarget,
		"target_name": mvName, "nonce": mvNonce, "expires_at": exp}
}

func TestMovePendingChip_CashierOnSaleScreenAnonymous(t *testing.T) {
	mux, _, _ := moveApprovalDeps(t)
	rec := getWithUserFromPage(mux, "/ui/move-pending-chip", &cashUser, "/")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, movePendingText) || !strings.Contains(body, `data-testid="sb-move-pending"`) {
		t.Fatalf("cashier chip = %d %s", rec.Code, body)
	}
	for _, bad := range []string{"href", "<button", "<form", "<dialog", "showModal", mvName, mvTarget, mvID, mvExp[:10]} {
		if strings.Contains(body, bad) {
			t.Fatalf("cashier chip leaks/controls %q: %s", bad, body)
		}
	}
}

func TestMovePendingChip_ManagerLinksEarliestMove(t *testing.T) {
	mux, _, _ := moveApprovalDeps(t)
	// Fresh ids: re-sending mvID with another expiry is ignored by enroll
	// ("keeping the one first shown"), which would hide what this pins.
	earlier, laterID := uuid.NewString(), uuid.NewString()
	soon := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	later := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	applyMoves(t, moveBlock(laterID, mvStore, later), moveBlock(earlier, mvStore, soon))
	body := getWithUserFromPage(mux, "/ui/move-pending-chip", &mgrUser, "/").Body.String()
	if !strings.Contains(body, `href="/settings/cloud/move/`+earlier+`"`) || !strings.Contains(body, movePendingText) {
		t.Fatalf("manager chip = %s", body)
	}
	if strings.Contains(body, mvName) || strings.Contains(body, laterID) {
		t.Fatalf("manager chip must stay anonymous: %s", body)
	}
}

func TestMovePendingChip_EmptyWithoutOpenMove(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Format(time.RFC3339)
	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"other source store", func(t *testing.T) { applyMoves(t, moveBlock(uuid.NewString(), "store-other", mvExp)) }},
		{"expired inside the grace", func(t *testing.T) { applyMoves(t, moveBlock(uuid.NewString(), mvStore, past)) }},
		{"no moves", func(t *testing.T) { applyMoves(t) }},
	}
	for _, tc := range cases {
		for _, role := range []struct {
			name string
			user *auth.User
		}{{"cashier", &cashUser}, {"manager", &mgrUser}} {
			u := role.user
			t.Run(tc.name+"/"+role.name, func(t *testing.T) {
				mux, _, _ := moveApprovalDeps(t)
				tc.setup(t)
				rec := getWithUserFromPage(mux, "/ui/move-pending-chip", u, "/")
				if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
					t.Fatalf("want empty 200, got %d %q", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestMovePendingChip_JoinedTillEmpty(t *testing.T) {
	mux, d, _ := moveApprovalDeps(t)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://192.168.1.5:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := getWithUserFromPage(mux, "/ui/move-pending-chip", &mgrUser, "/"); rec.Body.Len() != 0 {
		t.Fatalf("a joined till got the chip: %s", rec.Body.String())
	}
}

func TestMovePendingChip_OfflineNoNetwork(t *testing.T) {
	mux, d, cloud := moveApprovalDeps(t)
	d.NetReach = netreach.New(netreach.Options{Endpoint: "https://cloud.example.test/api", Client: &http.Client{Transport: failTransport{}}})
	before := cloud.calls.Load()
	body := getWithUserFromPage(mux, "/ui/move-pending-chip", &cashUser, "/").Body.String()
	if !strings.Contains(body, movePendingText) || cloud.calls.Load() != before {
		t.Fatalf("offline chip = %s (calls %d -> %d)", body, before, cloud.calls.Load())
	}
}

func TestOpenPendingMove(t *testing.T) {
	now := time.Now()
	if _, ok := openPendingMove(nil, now); ok {
		t.Fatal("empty list must have no open move")
	}
	gone := enroll.PendingMove{MoveID: "a", ExpiresAt: now.Add(-time.Hour)}
	open := enroll.PendingMove{MoveID: "b", ExpiresAt: now.Add(time.Hour)}
	if _, ok := openPendingMove([]enroll.PendingMove{gone}, now); ok {
		t.Fatal("all-expired list must have no open move")
	}
	if p, ok := openPendingMove([]enroll.PendingMove{gone, open}, now); !ok || p.MoveID != "b" {
		t.Fatalf("got %+v %v, want the open move", p, ok)
	}
}
