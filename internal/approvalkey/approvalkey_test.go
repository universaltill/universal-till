package approvalkey

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
)

type vector struct {
	SeedHex   string `json:"seed_hex"`
	PublicKey string `json:"public_key_b64url"`
	KID       string `json:"kid"`
	Approval  struct {
		Decision    string `json:"decision"`
		MoveID      string `json:"move_id"`
		DeviceID    string `json:"device_id"`
		SourceStore string `json:"source_store"`
		TargetStore string `json:"target_store"`
		Nonce       string `json:"nonce"`
		ExpiresAt   string `json:"expires_at"`
	} `json:"approval"`
	ApprovalMessage    string `json:"approval_message"`
	ApprovalSignature  string `json:"approval_signature_b64url"`
	DeclineMessage     string `json:"decline_message"`
	DeclineSignature   string `json:"decline_signature_b64url"`
	PopDeviceID        string `json:"pop_device_id"`
	PopStore           string `json:"pop_store_external_id"`
	PopMessage         string `json:"pop_message"`
	Pop                string `json:"pop_b64url"`
	PopPairRegisterMsg string `json:"pop_pair_register_message"`
	PopPairRegister    string `json:"pop_pair_register_b64url"`
}

func loadVector(t *testing.T) vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "approval-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func storeWithSeed(t *testing.T, seedHex string) *Store {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "secrets", "approval-ed25519.key")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	return NewAt(p)
}

func (v vector) approval() Approval {
	a := v.Approval
	return Approval{Decision: a.Decision, MoveID: a.MoveID, DeviceID: a.DeviceID, SourceStore: a.SourceStore,
		TargetStore: a.TargetStore, Nonce: a.Nonce, ExpiresAt: a.ExpiresAt}
}

// The shared golden vector (generated independently with Python
// cryptography): ut-cloud CL-2a/CL-2b must assert the same bytes.
func TestGoldenVector(t *testing.T) {
	v := loadVector(t)
	s := storeWithSeed(t, v.SeedHex)

	rep := s.Report(v.PopDeviceID, v.PopStore)
	if rep == nil {
		t.Fatal("no report")
	}
	if rep["kid"] != v.KID || rep["public_key"] != v.PublicKey {
		t.Fatalf("kid/public key = %v, want %s / %s", rep, v.KID, v.PublicKey)
	}
	if rep["pop"] != v.Pop {
		t.Fatalf("pop = %s, want %s", rep["pop"], v.Pop)
	}
	if got := string(PopMessage(v.PopDeviceID, v.PopStore, v.PublicKey)); got != v.PopMessage {
		t.Fatalf("pop message bytes differ:\n%q\n%q", got, v.PopMessage)
	}

	// Pair/register: the store is not known yet, so its line is empty.
	if got := string(PopMessage(v.PopDeviceID, "", v.PublicKey)); got != v.PopPairRegisterMsg {
		t.Fatalf("pair/register pop message differs:\n%q\n%q", got, v.PopPairRegisterMsg)
	}
	if got := s.Report(v.PopDeviceID, "")["pop"]; got != v.PopPairRegister {
		t.Fatalf("pair/register pop = %s, want %s", got, v.PopPairRegister)
	}

	a := v.approval()
	msg, err := ApprovalMessage(a)
	if err != nil {
		t.Fatal(err)
	}
	if string(msg) != v.ApprovalMessage {
		t.Fatalf("approval message bytes differ:\n%q\n%q", msg, v.ApprovalMessage)
	}
	sig, err := s.SignApproval(a)
	if err != nil {
		t.Fatal(err)
	}
	if sig != v.ApprovalSignature {
		t.Fatalf("signature = %s, want %s", sig, v.ApprovalSignature)
	}
	a.Decision = "decline"
	sig, err = s.SignApproval(a)
	if err != nil || sig != v.DeclineSignature {
		t.Fatalf("decline signature = %s (%v), want %s", sig, err, v.DeclineSignature)
	}
	dm, _ := ApprovalMessage(a)
	if string(dm) != v.DeclineMessage {
		t.Fatalf("decline message differs")
	}
}

func TestSignatureVerifiesAndIsBound(t *testing.T) {
	v := loadVector(t)
	s := storeWithSeed(t, v.SeedHex)
	pub, _ := base64.RawURLEncoding.DecodeString(v.PublicKey)
	sig, _ := s.SignApproval(v.approval())
	raw, _ := base64.RawURLEncoding.DecodeString(sig)
	if !ed25519.Verify(pub, []byte(v.ApprovalMessage), raw) {
		t.Fatal("signature does not verify")
	}
	// A signature for approve must not verify for any changed field.
	for name, mut := range map[string]func(*Approval){
		"decision": func(a *Approval) { a.Decision = "decline" },
		"move":     func(a *Approval) { a.MoveID += "x" },
		"device":   func(a *Approval) { a.DeviceID += "x" },
		"source":   func(a *Approval) { a.SourceStore += "x" },
		"target":   func(a *Approval) { a.TargetStore += "x" },
		"nonce":    func(a *Approval) { a.Nonce += "x" },
		"expiry":   func(a *Approval) { a.ExpiresAt = "2026-10-29T09:30:01Z" },
	} {
		a := v.approval()
		mut(&a)
		m, err := ApprovalMessage(a)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ed25519.Verify(pub, m, raw) {
			t.Fatalf("signature still verifies after changing %s", name)
		}
	}
}

func TestApprovalMessageRefusesBadFields(t *testing.T) {
	v := loadVector(t)
	for name, mut := range map[string]func(*Approval){
		"bad decision":   func(a *Approval) { a.Decision = "maybe" },
		"newline in id":  func(a *Approval) { a.MoveID = "a\nb" },
		"newline target": func(a *Approval) { a.TargetStore = "t\n" },
		"empty device":   func(a *Approval) { a.DeviceID = "" },
		"empty nonce":    func(a *Approval) { a.Nonce = "" },
		"empty expiry":   func(a *Approval) { a.ExpiresAt = "" },
	} {
		a := v.approval()
		mut(&a)
		if _, err := ApprovalMessage(a); err == nil {
			t.Fatalf("%s: want an error", name)
		}
		s := storeWithSeed(t, v.SeedHex)
		if sig, err := s.SignApproval(a); err == nil || sig != "" {
			t.Fatalf("%s: signed a bad message", name)
		}
	}
}

func TestLoadOrCreateGeneratesOnceWithSafeModes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-yet", "secrets", "approval-ed25519.key")
	s := NewAt(p)
	k1, err := s.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key file: %v, %d bytes", err, len(raw))
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v, want 0700", fi.Mode().Perm())
		}
	}
	k2, err := NewAt(p).LoadOrCreate()
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("second load changed the key (%v)", err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestUnusableFileIsLeftAloneAndNothingIsReportedOrSigned(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secrets", "approval-ed25519.key")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewAt(p)
	if s.Report("d", "s") != nil {
		t.Fatal("report from an unusable key")
	}
	v := loadVector(t)
	if sig, err := s.SignApproval(v.approval()); err == nil || sig != "" {
		t.Fatal("signed with an unusable key")
	}
	if b, _ := os.ReadFile(p); string(b) != "short" {
		t.Fatal("unusable file was overwritten")
	}
}

func TestKIDRule(t *testing.T) {
	v := loadVector(t)
	pub, _ := base64.RawURLEncoding.DecodeString(v.PublicKey)
	if got := KID(pub); got != v.KID || len(got) != 16 || got != strings.ToLower(got) {
		t.Fatalf("KID = %s", got)
	}
}

// Shared follows paths.Data, so a test (or a data-dir change) that moves the
// root gets its own store, and the same root gets the same store (its
// log-once flags survive across check-ins).
func TestSharedFollowsTheDataRoot(t *testing.T) {
	orig := paths.DataDir()
	t.Cleanup(func() { paths.Init(orig) })
	paths.Init(t.TempDir())
	a := Shared()
	if a != Shared() {
		t.Fatal("Shared returned a different store for the same data root")
	}
	if want := paths.Data("secrets", "approval-ed25519.key"); a.path != want {
		t.Fatalf("path = %s, want %s", a.path, want)
	}
	paths.Init(t.TempDir())
	if b := Shared(); b == a || b.path == a.path {
		t.Fatal("Shared kept the old data root")
	}
}

func TestLooseModesAreTightened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes do not apply")
	}
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "approval-ed25519.key")
	s := NewAt(p)
	if _, err := s.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", fi.Mode().Perm())
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAt(p).LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
	}
}
