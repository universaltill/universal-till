package secrets

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	const plaintext = `"sk_live_abc123"`
	sealed, err := Seal(key, []byte(plaintext))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, Prefix) {
		t.Fatalf("sealed value %q must carry the %q prefix", sealed, Prefix)
	}
	if strings.Contains(sealed, "sk_live") {
		t.Fatalf("sealed value leaks the plaintext: %q", sealed)
	}
	if !IsSealed(sealed) {
		t.Fatal("IsSealed must be true for a Seal output")
	}
	got, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("round trip: got %q want %q", got, plaintext)
	}
}

// Two seals of the same plaintext must differ (random nonce): equal
// ciphertexts would let a DB reader tell that two shops/keys share a value.
func TestSealUsesFreshNonce(t *testing.T) {
	key := testKey(t)
	a, _ := Seal(key, []byte("same"))
	b, _ := Seal(key, []byte("same"))
	if a == b {
		t.Fatalf("two seals of the same plaintext must not be identical: %q", a)
	}
}

func TestSealRejectsWrongKeyLength(t *testing.T) {
	if _, err := Seal([]byte("short"), []byte("x")); err == nil {
		t.Fatal("Seal must reject a non-32-byte key")
	}
	if _, err := Open([]byte("short"), Prefix+"AAAA"); err == nil {
		t.Fatal("Open must reject a non-32-byte key")
	}
}

// A legacy plaintext value (no prefix) is a different condition from a
// sealed-but-undecryptable one: the caller returns the former as-is and
// treats the latter as "not configured". Open must let it tell them apart.
func TestOpenDistinguishesNotSealedFromCorrupt(t *testing.T) {
	key := testKey(t)

	if IsSealed(`"legacy plaintext"`) {
		t.Fatal("a value without the prefix must not report as sealed")
	}
	_, err := Open(key, `"legacy plaintext"`)
	if !errors.Is(err, ErrNotSealed) {
		t.Fatalf("Open on a plaintext value: want ErrNotSealed, got %v", err)
	}

	// Prefix present, but the payload is not base64.
	_, err = Open(key, Prefix+"***not base64***")
	if err == nil || errors.Is(err, ErrNotSealed) {
		t.Fatalf("Open on a corrupt sealed value must fail with a non-ErrNotSealed error, got %v", err)
	}
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("corrupt sealed value: want ErrOpen, got %v", err)
	}

	// Prefix present, valid base64, but too short to hold a nonce.
	_, err = Open(key, Prefix+"AAAA")
	if err == nil || errors.Is(err, ErrNotSealed) || !errors.Is(err, ErrOpen) {
		t.Fatalf("Open on a truncated sealed value: want ErrOpen, got %v", err)
	}
}

func TestOpenWithWrongKeyNeverReturnsGarbage(t *testing.T) {
	sealed, err := Seal(testKey(t), []byte("the real value"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(testKey(t), sealed)
	if err == nil {
		t.Fatalf("Open with the wrong key must fail, got plaintext %q", got)
	}
	if !errors.Is(err, ErrOpen) || errors.Is(err, ErrNotSealed) {
		t.Fatalf("wrong key: want ErrOpen (not ErrNotSealed), got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("wrong key must return no plaintext, got %q", got)
	}
}

// A single flipped ciphertext byte must fail authentication (GCM tag).
func TestOpenDetectsTampering(t *testing.T) {
	key := testKey(t)
	sealed, _ := Seal(key, []byte("amount=100"))
	b := []byte(sealed)
	// Flip a character deep in the base64 payload (past the prefix and
	// nonce) so the tag check, not base64 decoding, is what rejects it.
	i := len(b) - 6
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	if _, err := Open(key, string(b)); err == nil {
		t.Fatal("tampered ciphertext must not open")
	}
}

func TestIsSecretSettingKey(t *testing.T) {
	yes := []string{"stripe_secret_key", "sumup_api_key", "API_KEY", "apikey", "auth_value",
		"password", "db_passwd", "access_token", "private_key", "key", "signing_key"}
	no := []string{"endpoint", "endpoint_url", "reader_id", "takeaway_rate_overrides", "keyboard_layout", "monkey"}
	for _, k := range yes {
		if !IsSecretSettingKey(k) {
			t.Errorf("IsSecretSettingKey(%q) = false, want true", k)
		}
	}
	for _, k := range no {
		if IsSecretSettingKey(k) {
			t.Errorf("IsSecretSettingKey(%q) = true, want false", k)
		}
	}
}
