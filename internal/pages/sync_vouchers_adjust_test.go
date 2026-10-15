package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// POST /api/sync/vouchers/{id}/adjust (ut-docs#4253): the main till's side
// of a manager balance correction forwarded by an additional till.

type syncVoucherAdjustResp struct {
	Data  *syncVoucherRow `json:"data"`
	Error any             `json:"error"`
}

func TestSyncVouchers_AdjustRequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	body := `{"adjustment_id":"adj-1","expected_balance_minor":1000,"new_balance_minor":500,"reason":"x","actor":"u1"}`
	for _, bearer := range []string{"", "wrong"} {
		if rec := postSyncVoucher(mux, "GS-1", "adjust", body, bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q: status = %d, want 401", bearer, rec.Code)
		}
	}
}

func TestSyncVouchers_AdjustValidatesBody(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	seedSyncVoucher(t, repo, "GS-V", 1000)
	cases := []struct {
		name, body, wantErr string
		want                int
	}{
		{"malformed", `{`, "invalid body", http.StatusBadRequest},
		{"no adjustment id", `{"expected_balance_minor":1000,"new_balance_minor":500,"reason":"x"}`, "adjustment_id required", http.StatusBadRequest},
		{"negative", `{"adjustment_id":"a","expected_balance_minor":1000,"new_balance_minor":-5,"reason":"x"}`, syncVoucherErrAdjustmentInvalid, http.StatusBadRequest},
		{"no reason", `{"adjustment_id":"a","expected_balance_minor":1000,"new_balance_minor":5,"reason":"  "}`, syncVoucherErrAdjustmentReason, http.StatusBadRequest},
		{"unknown voucher", `{"adjustment_id":"a","expected_balance_minor":1000,"new_balance_minor":5,"reason":"x"}`, "not found", http.StatusNotFound},
		{"balance changed", `{"adjustment_id":"a","expected_balance_minor":999,"new_balance_minor":5,"reason":"x"}`, syncVoucherErrBalanceChanged, http.StatusConflict},
		{"no change", `{"adjustment_id":"a","expected_balance_minor":1000,"new_balance_minor":1000,"reason":"x"}`, syncVoucherErrAdjustmentNoChange, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "GS-V"
			if tc.name == "unknown voucher" {
				id = "GS-NOPE"
			}
			rec := postSyncVoucher(mux, id, "adjust", tc.body, "bearer-t2")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("body %s missing %q", rec.Body.String(), tc.wantErr)
			}
		})
	}
	if v, _ := repo.GetVoucherBalance(context.Background(), nil, "GS-V"); v.BalanceMinor != 1000 {
		t.Fatalf("refusals changed the balance to %d", v.BalanceMinor)
	}
}

func TestSyncVouchers_AdjustAppliesAndIsIdempotent(t *testing.T) {
	mux, dp, dbase := newSyncVouchersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	repo := data.NewPOSRepo(dbase.DB)
	seedSyncVoucher(t, repo, "GS-A", 1000)
	body := `{"adjustment_id":"adj-1","expected_balance_minor":1000,"new_balance_minor":300,"reason":"damaged card","actor":"remote-user"}`

	for i := 0; i < 2; i++ {
		rec := postSyncVoucher(mux, "GS-A", "adjust", body, "bearer-t2")
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d (%s)", i, rec.Code, rec.Body.String())
		}
		var out syncVoucherAdjustResp
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data == nil {
			t.Fatalf("call %d: body %s (%v)", i, rec.Body.String(), err)
		}
		if out.Data.Balance != 300 || out.Data.Status != "active" || out.Data.ID != "GS-A" {
			t.Fatalf("call %d: answered %+v, want the post-correction row (300, active)", i, *out.Data)
		}
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-A' AND type = 'adjustment'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("adjustment rows after a retried call = %d, want 1", n)
	}
	var actor, reason string
	if err := dp.Db.QueryRow(`SELECT actor_user_id, reason FROM voucher_transactions WHERE id = 'adj-1'`).Scan(&actor, &reason); err != nil {
		t.Fatal(err)
	}
	if actor != "remote-user" || reason != "damaged card" {
		t.Fatalf("recorded actor %q reason %q", actor, reason)
	}
}
