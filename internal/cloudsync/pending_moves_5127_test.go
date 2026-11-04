package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ut-docs#5127 CL-2b/UT-1b: every /v1/stores/sync answer — the 200 and the
// 402 plan_required (a till-born shop is usually unpaid) — carries the moves
// waiting for this till's approval; pushSync hands them to enroll.

const pmMoveID = "0b1c7e52-6a58-4f0e-9d3e-2f5f1f6a7b01"

// Relative to now so the fixture never expires (ut-docs#5468).
var pmExp = time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second).Format(time.RFC3339)

func pendingMovesCloud(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := map[string]any{
			"directives": []any{},
			"pending_moves": []any{map[string]any{
				"move_id": pmMoveID, "source_store": "store-1", "target_store": "store-t",
				"target_name": "Corner Café", "nonce": "bm9uY2U", "expires_at": pmExp,
			}},
		}
		if status == http.StatusPaymentRequired {
			d["entitlement"] = json.RawMessage(planRequiredBlock)
			w.Header().Set("Retry-After", "3600")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": d, "error": nil})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPushSyncCarriesPendingMovesOn200And402(t *testing.T) {
	for name, status := range map[string]int{"200": http.StatusOK, "402": http.StatusPaymentRequired} {
		t.Run(name, func(t *testing.T) {
			srv := pendingMovesCloud(t, status)
			cfg, d := bootEnrolledTill(t, srv.URL)
			t.Cleanup(func() { enroll.ApplySyncMoves(json.RawMessage(`[]`), nil) })
			settings := data.NewSettingsRepo(d.DB)
			ctx := context.Background()
			_, _ = pushSync(ctx, cfg, settings, buildSyncRequest(ctx, cfg, settings, Hooks{}))
			got := enroll.PendingMoves(cfg)
			if len(got) != 1 || got[0].MoveID != pmMoveID || got[0].TargetName != "Corner Café" {
				t.Fatalf("PendingMoves = %+v, want the sync's pending move", got)
			}
		})
	}
}
