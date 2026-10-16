// Package lantls serves the till's LAN port over TLS as well as plain HTTP
// (ADR-0114 §7, ut-docs#2736).
//
// The main till holds one self-signed ECDSA P-256 key under
// paths.Data("tls"). Peers will trust it by its SPKI SHA-256 pin, learned at
// pairing — never by names or dates — so the certificate carries no
// hostnames, and an expired cert, a new IP or a Pi with no RTC can't fail a
// request. Renewal re-issues the certificate on the same key, so the pin
// never changes by itself; a pin change is a key rotation (rotation.go,
// ut-docs#4409).
//
// TLS is never the cause of a failed request: if the key can't be loaded or
// created, the caller serves plain HTTP only, exactly as before this
// package existed.
package lantls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	privateKeyFile = "key.pem"
	certFile       = "cert.pem"

	// Validity is a fresh certificate's lifetime (ADR-0114 §7: 5 years).
	Validity = 5 * 365 * 24 * time.Hour
	// RenewBefore re-issues the certificate (same key) this long before
	// notAfter (ADR-0114 §7: 30 days).
	RenewBefore = 30 * 24 * time.Hour
)

// Cert is the till's LAN TLS identity.
type Cert struct {
	tlsCert tls.Certificate
	leaf    *x509.Certificate
	// pin is the hex SHA-256 of the certificate's SubjectPublicKeyInfo —
	// what a peer pins (PinOf). It depends only on the key, so renewal
	// keeps it.
	pin string

	// Key rotation (rotation.go, ut-docs#4409): nextPin is the pending
	// next key's pin, set at load or by RotateNow (ut-docs#4423); st is
	// rotation.json in dir. Both are read and written under mu.
	nextPin string
	dir     string
	mu      sync.Mutex
	st      rotationState
}

// TLSConfig serves this certificate: TLS 1.3 only (every peer is a till
// running this code) and HTTP/1.1 only, since the main-till link is a
// WebSocket upgrade, which HTTP/2 doesn't carry.
func (c *Cert) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{c.tlsCert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	}
}

// LoadOrCreate loads the LAN TLS key and certificate from dir (callers pass
// paths.Data("tls")), creating the key on first use and re-issuing the
// certificate on the same key when it is missing, doesn't match the key, or
// is within RenewBefore of expiry.
//
// Key rotation (rotation.go, ut-docs#4409) runs here too, so the pin it
// returns is the one served for the whole process lifetime.
func LoadOrCreate(dir string) (*Cert, error) { return loadOrCreate(dir, time.Now()) }

func loadOrCreate(dir string, now time.Time) (*Cert, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lantls: create %s: %w", dir, err)
	}
	if runtime.GOOS != "windows" {
		// MkdirAll leaves an existing directory's mode alone. Not fatal: a
		// data dir on a filesystem without Unix modes (vfat/exFAT) refuses
		// chmod, and that mustn't turn TLS off for good; the key file
		// itself is still created 0600.
		if err := os.Chmod(dir, 0o700); err != nil {
			log.Printf("[LAN TLS] could not restrict %s to 0700: %v", dir, err)
		}
	}
	keyPath := filepath.Join(dir, privateKeyFile)
	fi, statErr := os.Stat(keyPath)
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	var keyMtime time.Time
	if statErr == nil {
		keyMtime = fi.ModTime()
	}
	// Key rotation before renewal, so the certificate renewed below is the
	// one on the key actually served (ut-docs#4409).
	rot := &rotation{dir: dir, now: now, st: loadState(dir)}
	key, next := rot.run(key, errors.Is(statErr, fs.ErrNotExist), keyMtime)
	leaf, err := ensureLeaf(filepath.Join(dir, certFile), key, now)
	if err != nil {
		return nil, err
	}
	c := &Cert{
		tlsCert: tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf},
		leaf:    leaf,
		pin:     PinOf(leaf),
		dir:     dir,
		st:      rot.st,
	}
	if next != nil {
		// The pending next certificate is renewed like the current one;
		// a failure only means it is re-issued at the switch.
		if _, err := ensureLeaf(filepath.Join(dir, nextCertFile), next, now); err != nil {
			log.Printf("[LAN TLS] could not renew the next certificate: %v", err)
		}
		c.nextPin = rot.st.NextPin
	}
	return c, nil
}

// ensureLeaf loads the certificate at path, re-issuing it on key when it is
// missing, doesn't match the key, or is within RenewBefore of expiry.
func ensureLeaf(path string, key *ecdsa.PrivateKey, now time.Time) (*x509.Certificate, error) {
	leaf := loadLeaf(path, &key.PublicKey)
	if leaf != nil && !now.Add(RenewBefore).After(leaf.NotAfter) {
		return leaf, nil
	}
	der, err := issue(key, now)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	if leaf, err = x509.ParseCertificate(der); err != nil {
		return nil, fmt.Errorf("lantls: parse issued cert: %w", err)
	}
	return leaf, nil
}

// loadOrCreateKey reads the key, or creates one if the file doesn't exist.
// An unreadable or unparsable key is an error and is never replaced: a new
// key would silently change the pin every paired till trusts.
func loadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	key, err := readKey(path)
	if errors.Is(err, fs.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("lantls: generate key: %w", err)
		}
		if err := writeKey(path, key); err != nil {
			return nil, err
		}
		return key, nil
	}
	return key, err
}

// loadLeaf returns the stored certificate, or nil when it is missing,
// unparsable or for another key (all re-issued by the caller).
func loadLeaf(path string, pub *ecdsa.PublicKey) *x509.Certificate {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	if p, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !p.Equal(pub) {
		return nil
	}
	return leaf
}

func issue(key *ecdsa.PrivateKey, now time.Time) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("lantls: serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Universal Till LAN"},
		NotBefore:    now.Add(-time.Hour), // tolerate a peer's clock running a little behind
		NotAfter:     now.Add(Validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("lantls: issue cert: %w", err)
	}
	return der, nil
}

// writeAtomic writes via a temp file + rename so a power cut never leaves a
// half-written key or certificate.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("lantls: write %s: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return fmt.Errorf("lantls: write %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("lantls: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("lantls: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("lantls: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("lantls: write %s: %w", path, err)
	}
	// Persist the rename too: losing a just-created key to a power cut
	// would mean a new key — a new pin — on the next boot.
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir persists renames in dir (best effort; not on Windows).
func syncDir(dir string) {
	if runtime.GOOS != "windows" {
		if d, err := os.Open(dir); err == nil {
			_ = d.Sync()
			d.Close()
		}
	}
}
