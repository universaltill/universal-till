package data

import (
	"context"
	"testing"
)

// ut-docs#5782: the Current Shift card shows the cashier's name, not the id.
// Chain: display_name -> username -> raw cashier_id -> "".
func TestCurrentOpenShift_CashierName(t *testing.T) {
	cases := []struct {
		name     string
		cashier  string
		users    [][3]string // id, username, display_name
		wantName string
	}{
		{"display name wins", "u1", [][3]string{{"u1", "alice", "Alice Jones"}}, "Alice Jones"},
		{"empty display name falls to username", "u1", [][3]string{{"u1", "alice", ""}}, "alice"},
		{"no users row falls to raw id", "ghost", nil, "ghost"},
		{"empty cashier id yields empty", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := b8OpenDB(t, "shift-cashier-name.db")
			ctx := context.Background()
			repo := NewPOSRepo(d.DB)
			if _, ok, err := repo.CurrentOpenShift(ctx); err != nil || ok {
				t.Fatalf("no shift: ok=%v err=%v", ok, err)
			}
			// A cashier with no users row (deleted user / legacy placeholder)
			// can't be inserted under FK enforcement: pin one connection
			// and switch FKs off for it, as a restored old DB would look.
			d.SetMaxOpenConns(1)
			mustExec(t, d, `PRAGMA foreign_keys = OFF`)
			for _, u := range tc.users {
				mustExec(t, d, `INSERT INTO users (id, username, display_name, role) VALUES (?,?,?,'cashier')`, u[0], u[1], u[2])
			}
			mustExec(t, d, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front',1)`)
			mustExec(t, d, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('s1','reg1',?,?,100)`,
				tc.cashier, b8At(iwAnchor()))
			s, ok, err := repo.CurrentOpenShift(ctx)
			if err != nil || !ok {
				t.Fatalf("open shift: ok=%v err=%v", ok, err)
			}
			if s.CashierName != tc.wantName {
				t.Errorf("CashierName = %q, want %q", s.CashierName, tc.wantName)
			}
			if s.CashierID != tc.cashier {
				t.Errorf("CashierID = %q, want raw %q", s.CashierID, tc.cashier)
			}
		})
	}
}
