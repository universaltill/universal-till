package approvalkey

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0222 §2.2: the recover message is pinned byte for byte by a vector
// file that ut-cloud's internal/approvalkey carries identically.
func TestRecoverMessageMatchesGoldenVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "recover_vector.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	st := storeWithSeed(t, m["seed_hex"])
	msg, err := RecoverMessage(m["store_id"], m["device_id"], m["challenge_id"], m["nonce"], m["expires_at"], m["public_key"])
	if err != nil || string(msg) != m["message"] {
		t.Fatalf("message = %q (%v), want %q", msg, err, m["message"])
	}
	kid, pub, sig, err := st.SignRecover(m["store_id"], m["device_id"], m["challenge_id"], m["nonce"], m["expires_at"])
	if err != nil {
		t.Fatal(err)
	}
	if pub != m["public_key"] || kid != m["kid"] || sig != m["signature"] {
		t.Fatalf("kid/pub/sig = %q %q %q, want %q %q %q", kid, pub, sig, m["kid"], m["public_key"], m["signature"])
	}
}

func TestRecoverMessageRefusesBadFields(t *testing.T) {
	ok := []string{"s", "d", "c", "n", "2026-01-01T00:00:00Z", "k"}
	for i := range ok {
		bad := append([]string(nil), ok...)
		bad[i] = ""
		if _, err := RecoverMessage(bad[0], bad[1], bad[2], bad[3], bad[4], bad[5]); err == nil {
			t.Fatalf("empty field %d accepted", i)
		}
		bad[i] = "a\nb"
		if _, err := RecoverMessage(bad[0], bad[1], bad[2], bad[3], bad[4], bad[5]); err == nil {
			t.Fatalf("line break in field %d accepted", i)
		}
	}
}

func TestRecoverDomainDiffersFromTheOthers(t *testing.T) {
	msg, _ := RecoverMessage("s", "d", "c", "n", "e", "k")
	if !strings.HasPrefix(string(msg), "universaltill/till-recover/v1\n") {
		t.Fatalf("message starts %q", msg)
	}
	for _, other := range []string{approvalDomain, popDomain, reregisterDomain} {
		if strings.HasPrefix(string(msg), other+"\n") {
			t.Fatalf("recover domain collides with %s", other)
		}
	}
}

// A till with no key never creates one to heal, and Has never creates either.
func TestHasAndSignRecoverNeverCreateAKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secrets", "approval-ed25519.key")
	s := NewAt(p)
	if s.Has() {
		t.Fatal("Has() true with no file")
	}
	if _, _, _, err := s.SignRecover("s", "d", "c", "n", "e"); err == nil {
		t.Fatal("SignRecover with no key succeeded")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("a key file was created: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Has() {
		t.Fatal("Has() true for an unparseable file")
	}
	if _, _, _, err := s.SignRecover("s", "d", "c", "n", "e"); err == nil {
		t.Fatal("SignRecover with an unusable key succeeded")
	}
	if b, _ := os.ReadFile(p); string(b) != "short" {
		t.Fatal("unusable file was overwritten")
	}
	if err := os.WriteFile(p, make([]byte, seedSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Has() {
		t.Fatal("Has() false for a good key")
	}
}

func TestSignRecoverVerifies(t *testing.T) {
	st := storeWithSeed(t, strings.Repeat("07", 32))
	kid, pub, sig, err := st.SignRecover("s", "d", "c", "n", "2026-12-01T00:00:00Z") // fixed-date:ok signed-message fixture, never compared with the clock
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := base64.RawURLEncoding.DecodeString(pub)
	msg, _ := RecoverMessage("s", "d", "c", "n", "2026-12-01T00:00:00Z", pub) // fixed-date:ok signed-message fixture, never compared with the clock
	raw, _ := base64.RawURLEncoding.DecodeString(sig)
	if !ed25519.Verify(pk, msg, raw) || kid != KID(pk) {
		t.Fatal("signature or kid wrong")
	}
}
