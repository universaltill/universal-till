package data

import (
	"context"
	"testing"
)

// ut-docs#4022: the seeded register is named after the till, else the shop;
// "Default Register" only when neither has a name.
func TestEnsureRegister_NamesSeededRegisterAfterTillThenShop(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		want     string
	}{
		{"till name wins", map[string]string{"till.name": " Counter ", "store.name": "Corner Cafe"}, "Counter"},
		{"shop name when till unnamed", map[string]string{"till.name": "  ", "store.name": "Corner Cafe"}, "Corner Cafe"},
		{"fallback when neither", nil, "Default Register"}, // store.name is still 001's "My Store" placeholder
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbo := newBatch8DB(t, "seed.db")
			for k, v := range tc.settings {
				mustExec(t, dbo, `INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, k, v)
			}
			id, err := NewPOSRepo(dbo.DB).EnsureRegister(context.Background())
			if err != nil {
				t.Fatalf("EnsureRegister: %v", err)
			}
			var got string
			if err := dbo.DB.QueryRow(`SELECT name FROM registers WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("seeded register name = %q, want %q", got, tc.want)
			}
		})
	}
}

// An inactive register already holding the till's name must not make the
// seed fail (registers.name is UNIQUE): it takes a suffixed name instead.
func TestEnsureRegister_SuffixesWhenNameTakenByInactiveRegister(t *testing.T) {
	dbo := newBatch8DB(t, "seed-taken.db")
	mustExec(t, dbo, `INSERT OR REPLACE INTO settings (key, value) VALUES ('till.name', 'Counter')`)
	mustExec(t, dbo, `INSERT INTO registers (id, name, is_active) VALUES ('reg-old', 'Counter', 0)`)
	id, err := NewPOSRepo(dbo.DB).EnsureRegister(context.Background())
	if err != nil {
		t.Fatalf("EnsureRegister: %v", err)
	}
	var got string
	if err := dbo.DB.QueryRow(`SELECT name FROM registers WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if id != "reg-default" || got != "Counter (2)" {
		t.Fatalf("got id=%q name=%q, want reg-default / %q", id, got, "Counter (2)")
	}
}

func TestRenameRegisterToFollowTill(t *testing.T) {
	dbo := newBatch8DB(t, "follow.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)
	mustExec(t, dbo, `INSERT INTO registers (id, name, is_active) VALUES ('r1', 'Front', 1), ('r2', 'Back', 1)`)

	if ok, err := repo.RenameRegisterToFollowTill(ctx, "r1", "Window"); err != nil || !ok {
		t.Fatalf("rename to a free name: ok=%v err=%v", ok, err)
	}
	// Taken by another register: left alone, no error.
	if ok, err := repo.RenameRegisterToFollowTill(ctx, "r1", "Back"); err != nil || ok {
		t.Fatalf("rename to a taken name: ok=%v err=%v, want false/nil", ok, err)
	}
	// Unchanged name and unknown id: no-ops.
	if ok, err := repo.RenameRegisterToFollowTill(ctx, "r1", "Window"); err != nil || ok {
		t.Fatalf("unchanged name: ok=%v err=%v", ok, err)
	}
	if ok, err := repo.RenameRegisterToFollowTill(ctx, "nope", "Other"); err != nil || ok {
		t.Fatalf("unknown id: ok=%v err=%v", ok, err)
	}
	var got string
	if err := dbo.DB.QueryRow(`SELECT name FROM registers WHERE id = 'r1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "Window" {
		t.Fatalf("r1 name = %q, want Window", got)
	}
}

func TestEnrolledRegisterID(t *testing.T) {
	dbo := newBatch8DB(t, "enrolled.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)
	mustExec(t, dbo, `INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json) VALUES
		('a1', 'system', 'till', 't1', 'till_enrolled', '{"name":"Bar","register_id":"r-bar2","register_name":"Bar (2)"}'),
		('a2', 'system', 'till', 't2', 'till_enrolled', '{"name":"Old","register_id":"","register_name":""}'),
		('a3', 'system', 'till', 't1', 'snapshot_served', '{"register_id":"wrong"}')`)
	for till, want := range map[string]string{"t1": "r-bar2", "t2": "", "t3": ""} {
		got, err := repo.EnrolledRegisterID(ctx, till)
		if err != nil || got != want {
			t.Fatalf("EnrolledRegisterID(%q) = %q, %v; want %q", till, got, err, want)
		}
	}
}
