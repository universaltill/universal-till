package lantls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return fi.Mode().Perm()
}

func TestLoadOrCreate_FreshDirCreatesP256KeyAndFiveYearCert(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "tls") // parent doesn't exist either
	c, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	if runtime.GOOS != "windows" {
		if m := mode(t, dir); m != 0o700 {
			t.Errorf("tls dir mode = %o, want 700", m)
		}
		if m := mode(t, filepath.Join(dir, privateKeyFile)); m != 0o600 {
			t.Errorf("key mode = %o, want 600", m)
		}
	}
	leaf := c.leaf
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("cert key = %T, want ECDSA P-256", leaf.PublicKey)
	}
	if got, want := leaf.NotAfter, t0.Add(Validity); !got.Equal(want) {
		t.Errorf("NotAfter = %v, want %v (5 years)", got, want)
	}
	if len(leaf.DNSNames)+len(leaf.IPAddresses) != 0 {
		t.Errorf("cert names %v %v: verification is pin-only, no names", leaf.DNSNames, leaf.IPAddresses)
	}
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		t.Errorf("not self-signed: %v", err)
	}
	if leaf.IsCA {
		t.Error("cert is a CA: it must only be able to serve, not sign")
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if c.pin != hex.EncodeToString(sum[:]) {
		t.Errorf("pin = %s, want SPKI SHA-256 %x", c.pin, sum)
	}
}

func TestLoadOrCreate_RestartKeepsPinAndCert(t *testing.T) {
	dir := t.TempDir()
	a, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadOrCreate(dir, t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if a.pin != b.pin {
		t.Fatalf("pin changed across restart: %s → %s", a.pin, b.pin)
	}
	if a.leaf.SerialNumber.Cmp(b.leaf.SerialNumber) != 0 {
		t.Errorf("cert re-issued on a plain restart")
	}
}

func TestLoadOrCreate_RenewsNearExpiryOnSameKey(t *testing.T) {
	dir := t.TempDir()
	a, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	later := t0.Add(Validity - RenewBefore + time.Hour) // inside the 30-day window
	b, err := loadOrCreate(dir, later)
	if err != nil {
		t.Fatal(err)
	}
	if a.pin != b.pin {
		t.Fatalf("renewal changed the pin: %s → %s", a.pin, b.pin)
	}
	if !b.leaf.NotAfter.Equal(later.Add(Validity)) {
		t.Errorf("renewed NotAfter = %v, want %v", b.leaf.NotAfter, later.Add(Validity))
	}
	// …and the renewed cert is what the next boot loads.
	c, err := loadOrCreate(dir, later.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.leaf.SerialNumber.Cmp(b.leaf.SerialNumber) != 0 {
		t.Errorf("renewed cert was not persisted")
	}
}

func TestLoadOrCreate_MissingOrForeignCertReissuedOnSameKey(t *testing.T) {
	dir := t.TempDir()
	a, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, certFile)); err != nil {
		t.Fatal(err)
	}
	b, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatalf("missing cert: %v", err)
	}
	if a.pin != b.pin {
		t.Fatalf("missing cert changed the pin")
	}

	// A cert for some other key (e.g. a half-restored backup) must not be
	// served: it is re-issued for the key we hold.
	other := t.TempDir()
	if _, err := loadOrCreate(other, t0); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.ReadFile(filepath.Join(other, certFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, certFile), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatalf("foreign cert: %v", err)
	}
	if c.pin != a.pin {
		t.Fatalf("foreign cert: pin %s, want our key's %s", c.pin, a.pin)
	}
}

func TestLoadOrCreate_CorruptKeyIsAnErrorAndLeftUntouched(t *testing.T) {
	dir := t.TempDir()
	kp := filepath.Join(dir, privateKeyFile)
	junk := []byte("not a key")
	if err := os.WriteFile(kp, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreate(dir, t0); err == nil {
		t.Fatal("corrupt key: want an error (serve plain HTTP), got a cert")
	}
	got, err := os.ReadFile(kp)
	if err != nil || string(got) != string(junk) {
		t.Fatalf("corrupt key was overwritten (%q, %v): a new key would silently change the pin", got, err)
	}
}

func TestLoadOrCreate_TLSConfig(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), t0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.TLSConfig()
	if cfg.MinVersion != 0x0304 { // tls.VersionTLS13
		t.Errorf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Errorf("NextProtos = %v, want [http/1.1] (the link is WebSocket over h1)", cfg.NextProtos)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want 1", len(cfg.Certificates))
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.SerialNumber.Cmp(c.leaf.SerialNumber) != 0 {
		t.Errorf("TLSConfig serves a different cert")
	}
}

func TestLoadOrCreate_WrongKeyTypeIsLeftUntouched(t *testing.T) {
	dir := t.TempDir()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	kp := filepath.Join(dir, privateKeyFile)
	want := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(kp, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreate(dir, t0); err == nil {
		t.Fatal("P-384 key: want an error")
	}
	if got, _ := os.ReadFile(kp); string(got) != string(want) {
		t.Fatal("non-P-256 key was overwritten")
	}
}
