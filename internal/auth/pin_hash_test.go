package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ADR-0115 §1 (ut-docs#2755): the main till stores exactly the pin_hash an
// additional till sends, so it must refuse anything that is not a
// well-formed HashPIN string -- a malformed value would make that operator
// unable to sign in anywhere, and an absurd iteration count would turn
// every PIN login (which verifies against every user's hash) into a DoS.
func TestValidatePINHash(t *testing.T) {
	good, err := HashPIN("4821")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePINHash(good); err != nil {
		t.Fatalf("a fresh HashPIN value must validate: %v", err)
	}
	parts := strings.Split(good, "$")
	for name, bad := range map[string]string{
		"empty":           "",
		"plaintext pin":   "4821",
		"wrong scheme":    "bcrypt$sha256$100000$" + parts[3] + "$" + parts[4],
		"wrong digest":    "pbkdf2$sha1$100000$" + parts[3] + "$" + parts[4],
		"too few parts":   "pbkdf2$sha256$100000$" + parts[3],
		"too many parts":  good + "$x",
		"iter not number": "pbkdf2$sha256$abc$" + parts[3] + "$" + parts[4],
		"iter zero":       "pbkdf2$sha256$0$" + parts[3] + "$" + parts[4],
		"iter absurd":     "pbkdf2$sha256$999999999$" + parts[3] + "$" + parts[4],
		"salt not b64":    "pbkdf2$sha256$100000$!!!$" + parts[4],
		"salt empty":      "pbkdf2$sha256$100000$$" + parts[4],
		"key not b64":     "pbkdf2$sha256$100000$" + parts[3] + "$!!!",
		"key empty":       "pbkdf2$sha256$100000$" + parts[3] + "$",
		"key short":       "pbkdf2$sha256$100000$" + parts[3] + "$QUJD",
	} {
		if err := ValidatePINHash(bad); err == nil {
			t.Errorf("%s: ValidatePINHash(%q) = nil, want an error", name, bad)
		}
	}
	// VerifyPIN still works on the shared parser.
	if !VerifyPIN("4821", good) || VerifyPIN("4822", good) {
		t.Fatal("VerifyPIN regressed")
	}
}

// ChangeOwnPINVia lets an additional till verify the current PIN locally
// (lockout accounting unchanged) and hand only the new hash to a persist
// callback (the main-till write-through) instead of the local SetUserPIN.
func TestChangeOwnPINVia_PersistsThroughCallback(t *testing.T) {
	db := openAuthTestDB(t)
	svc := NewService(db)
	ctx := context.Background()
	id, err := svc.Repo().CreateUser(ctx, "ana", "Ana", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := HashPIN("1111")
	if err := svc.Repo().SetUserPIN(ctx, id, old); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := svc.ChangeOwnPINVia(ctx, id, "1111", "2222", func(_ context.Context, hash string) error {
		got = hash
		return nil
	}); err != nil {
		t.Fatalf("ChangeOwnPINVia: %v", err)
	}
	if !VerifyPIN("2222", got) {
		t.Fatalf("callback got %q, not a hash of the new PIN", got)
	}
	// The callback owns persistence: the local row is untouched by the service.
	u, _, _ := svc.Repo().GetUser(ctx, id)
	if u.PinHash != old {
		t.Fatal("ChangeOwnPINVia wrote the local row itself; the callback owns persistence")
	}

	// A callback failure is returned as-is, so the caller can map it.
	sentinel := errors.New("main till unreachable")
	if err := svc.ChangeOwnPINVia(ctx, id, "1111", "3333", func(context.Context, string) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("callback error = %v, want the sentinel", err)
	}
	// Wrong current PIN never reaches the callback.
	called := false
	if err := svc.ChangeOwnPINVia(ctx, id, "9999", "3333", func(context.Context, string) error { called = true; return nil }); !errors.Is(err, ErrInvalidPIN) || called {
		t.Fatalf("wrong current PIN: err=%v called=%v", err, called)
	}
}
