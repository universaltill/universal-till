package data

import (
	"bytes"
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// users.list.v1 (ADR-0149 §6, ut-docs#3976): the staff list a plugin shows
// ("who did this service") — id, display name and active only. A PIN hash,
// a username or a role must never reach a plugin.
func TestUsersListView(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "users-view.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()

	mustExec(t, dbo, `INSERT INTO users(id, username, display_name, role, pin_hash, is_active) VALUES
		('u1', 'jsmith-login', 'zoe Stylist', 'manager', '$argon2id$secret-pin-hash', 1),
		('u2', 'retired-login~u2', 'Alex Barber', 'cashier', '$argon2id$other-hash', 0)`)

	v, ok := LookupCoreView("users.list.v1")
	if !ok {
		t.Fatal("users.list.v1 not registered")
	}
	if v.Permission != "view:users" {
		t.Fatalf("permission = %q, want view:users", v.Permission)
	}
	args, err := v.ParseArgs(nil)
	if err != nil || len(args) != 0 {
		t.Fatalf("ParseArgs(nil) = %v, %v; want no arguments", args, err)
	}
	if _, err := v.ParseArgs([]byte(`{"limit":1}`)); err == nil {
		t.Fatal("users.list.v1 accepted an argument")
	}

	out, err := RunCoreView(ctx, dbo.DB, v, args, CoreViewMaxResult)
	if err != nil {
		t.Fatal(err)
	}
	// The built-in "system" service identity is hidden, as on the Users
	// admin page; the seeded kiosk operator is listed. Inactive users are
	// listed with active=false; order is display name, case-insensitive.
	want := `[` +
		`{"id":"u2","display_name":"Alex Barber","active":false},` +
		`{"id":"kiosk","display_name":"Self-order kiosk","active":true},` +
		`{"id":"u1","display_name":"zoe Stylist","active":true}]`
	if string(out) != want {
		t.Fatalf("result:\n got  %s\n want %s", out, want)
	}
	for _, secret := range []string{"argon2id", "secret-pin-hash", "other-hash", "jsmith-login", "retired-login",
		"manager", "cashier", "pin", "username", "role"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("users.list.v1 leaks %q: %s", secret, out)
		}
	}
}
