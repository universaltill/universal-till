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

// ADR-0222 §9: the rejoin message and the code hash are pinned byte for byte
// by a vector file that ut-cloud's internal/approvalkey carries identically.
func TestRejoinGoldenVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "rejoin_vector.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		SeedHex    string   `json:"seed_hex"`
		PublicKey  string   `json:"public_key"`
		DeviceID   string   `json:"device_id"`
		TypedCodes []string `json:"typed_codes"`
		CodeHash   string   `json:"code_hash"`
		Message    string   `json:"message"`
		Signature  string   `json:"signature"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.TypedCodes) == 0 {
		t.Fatal("vector has no typed codes")
	}
	for _, typed := range v.TypedCodes {
		h, ok := RejoinCodeHash(typed)
		if !ok || h != v.CodeHash {
			t.Fatalf("RejoinCodeHash(%q) = %q, %v, want %q", typed, h, ok, v.CodeHash)
		}
	}
	msg, err := RejoinMessage(v.DeviceID, v.PublicKey, v.CodeHash)
	if err != nil || string(msg) != v.Message {
		t.Fatalf("message = %q (%v), want %q", msg, err, v.Message)
	}
	st := storeWithSeed(t, v.SeedHex)
	kid, pub, sig, err := st.SignRejoin(v.DeviceID, v.CodeHash)
	if err != nil {
		t.Fatal(err)
	}
	raw64, _ := base64.RawURLEncoding.DecodeString(pub)
	if pub != v.PublicKey || sig != v.Signature || kid != KID(ed25519.PublicKey(raw64)) {
		t.Fatalf("kid/pub/sig = %q %q %q, want pub %q sig %q", kid, pub, sig, v.PublicKey, v.Signature)
	}
}

func TestRejoinMessageShapeAndFieldChecks(t *testing.T) {
	hash := strings.Repeat("a1", 32)
	msg, err := RejoinMessage("d", "k", hash)
	if err != nil {
		t.Fatal(err)
	}
	if want := "universaltill/till-rejoin/v1\nd\nk\n" + hash; string(msg) != want {
		t.Fatalf("message = %q, want %q", msg, want)
	}
	for name, f := range map[string][3]string{
		"empty device":   {"", "k", hash},
		"newline device": {"a\nb", "k", hash},
		"empty key":      {"d", "", hash},
		"newline key":    {"d", "a\rb", hash},
		"short hash":     {"d", "k", hash[:63]},
		"long hash":      {"d", "k", hash + "a"},
		"upper hash":     {"d", "k", strings.ToUpper(hash)},
		"non-hex hash":   {"d", "k", strings.Repeat("g", 64)},
	} {
		if _, err := RejoinMessage(f[0], f[1], f[2]); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestRejoinCodeHashRejectsMalformed(t *testing.T) {
	for name, code := range map[string]string{
		"empty":     "",
		"short":     "K7QX29P",
		"long":      "K7QX29PMK",
		"long-pad":  "K7QX29PM2",
		"long sign": "ſ7QX29PM", // U+017F upper-cases to S: must be refused first
		"zero":      "K7QX29P0",
		"letter O":  "K7QX29PO",
		"one":       "K7QX29P1",
		"letter I":  "K7QX29PI",
		"punct":     "K7QX.29PM",
	} {
		if h, ok := RejoinCodeHash(code); ok || h != "" {
			t.Fatalf("%s: RejoinCodeHash(%q) = %q, %v, want refused", name, code, h, ok)
		}
	}
}

// A till with no usable key never creates one to rejoin (ADR-0222 §6).
func TestSignRejoinNeverCreatesAKey(t *testing.T) {
	hash := strings.Repeat("0", 64)
	p := filepath.Join(t.TempDir(), "secrets", "approval-ed25519.key")
	s := NewAt(p)
	if _, _, _, err := s.SignRejoin("d", hash); err == nil {
		t.Fatal("SignRejoin with no key succeeded")
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
	if _, _, _, err := NewAt(p).SignRejoin("d", hash); err == nil {
		t.Fatal("SignRejoin with a garbage key succeeded")
	}
	if got, _ := os.ReadFile(p); string(got) != "short" {
		t.Fatal("garbage key file was rewritten")
	}
}

func TestRejoinDomainDiffersFromTheOthers(t *testing.T) {
	msg, _ := RejoinMessage("d", "k", strings.Repeat("0", 64))
	if !strings.HasPrefix(string(msg), "universaltill/till-rejoin/v1\n") {
		t.Fatalf("message starts %q", msg)
	}
	for _, other := range []string{approvalDomain, popDomain, reregisterDomain, recoverDomain} {
		if strings.HasPrefix(string(msg), other+"\n") {
			t.Fatalf("rejoin domain collides with %s", other)
		}
	}
}
