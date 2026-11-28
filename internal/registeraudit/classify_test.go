package registeraudit

import (
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

func TestClassify(t *testing.T) {
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour)
	recent := now.Add(-5 * 24 * time.Hour)
	gone := func(r data.RegisterAuditRow) data.RegisterAuditRow {
		r.IsActive, r.HasLink, r.DeviceID, r.TillID, r.TillExists = true, true, "dev-1", "t1", false
		return r
	}
	tests := []struct {
		name       string
		row        data.RegisterAuditRow
		ctx        Context
		wantState  string
		wantSafe   bool
		wantAuto   bool
		wantInEvid string
	}{
		{"self is never offered", data.RegisterAuditRow{IsSelf: true, IsActive: true}, Context{ActiveCount: 3}, StateSelf, false, false, "own register"},
		{"retired stays retired", data.RegisterAuditRow{IsActive: false, RetiredReason: "revoked"}, Context{ActiveCount: 3}, StateRetired, false, false, "revoked"},
		{"enrolled till is live", data.RegisterAuditRow{IsActive: true, HasLink: true, TillID: "t1", TillExists: true, DeviceID: "dev-1"}, Context{ActiveCount: 3}, StateLive, false, false, ""},
		{"no link, no till is unknown", data.RegisterAuditRow{IsActive: true}, Context{ActiveCount: 3}, StateUnknown, false, false, "no link"},
		{"till removed, quiet 60 days: safe and auto", gone(data.RegisterAuditRow{LastSale: old}), Context{Now: now, ActiveCount: 3}, StateOldEntry, true, true, "removed"},
		{"till removed, sale 5 days ago: not yet", gone(data.RegisterAuditRow{LastSale: recent}), Context{Now: now, ActiveCount: 3}, StateOldEntry, false, false, "5 days ago"},
		{"till removed, no sales ever: safe", gone(data.RegisterAuditRow{}), Context{Now: now, ActiveCount: 3}, StateOldEntry, true, true, ""},
		{"device id unknown: safe by hand, never auto", func() data.RegisterAuditRow {
			r := gone(data.RegisterAuditRow{LastSale: old})
			r.DeviceID = ""
			return r
		}(), Context{Now: now, ActiveCount: 3}, StateOldEntry, true, false, "never auto-retired"},
		{"superseded is safe even with a recent sale", func() data.RegisterAuditRow {
			r := gone(data.RegisterAuditRow{LastSale: recent})
			r.SupersededBy = "dev-2"
			return r
		}(), Context{Now: now, ActiveCount: 3}, StateSuperseded, true, true, "dev-2"},
		{"last active is never safe", gone(data.RegisterAuditRow{LastSale: old}), Context{Now: now, ActiveCount: 1}, StateOldEntry, false, false, "last active"},
		{"unsent sales block", gone(data.RegisterAuditRow{LastSale: old, UnsentSales: 2}), Context{Now: now, ActiveCount: 3}, StateOldEntry, false, false, "2 unsent sales"},
		{"open shift blocks", gone(data.RegisterAuditRow{LastSale: old, OpenShift: true}), Context{Now: now, ActiveCount: 3}, StateOldEntry, false, false, "open shift"},
		{"till row present but the account lost it", func() data.RegisterAuditRow {
			r := gone(data.RegisterAuditRow{LastSale: old})
			r.TillExists = true
			return r
		}(), Context{Now: now, ActiveCount: 3, TillGone: true}, StateOldEntry, true, true, "no longer in the account"},
		{"cloud unknown never makes a present till gone", data.RegisterAuditRow{IsActive: true, HasLink: true, TillID: "t1", TillExists: true, DeviceID: "d", LastSale: old}, Context{Now: now, ActiveCount: 3, TillGone: false}, StateLive, false, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.row, tc.ctx)
			if got.State != tc.wantState || got.RetireSafe != tc.wantSafe || got.AutoRetireOK != tc.wantAuto {
				t.Fatalf("state=%q safe=%v auto=%v (%s); want %q %v %v", got.State, got.RetireSafe, got.AutoRetireOK, got.Evidence, tc.wantState, tc.wantSafe, tc.wantAuto)
			}
			if tc.wantInEvid != "" && !strings.Contains(got.Evidence, tc.wantInEvid) {
				t.Fatalf("evidence %q does not mention %q", got.Evidence, tc.wantInEvid)
			}
		})
	}
}
