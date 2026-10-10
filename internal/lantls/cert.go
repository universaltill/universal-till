// Package lantls serves the till's LAN port over TLS as well as plain HTTP
// (ADR-0114 §7, ut-docs#2736).
//
// The main till holds one self-signed ECDSA P-256 key under
// paths.Data("tls"). Peers will trust it by its SPKI SHA-256 pin, learned at
// pairing — never by names or dates — so the certificate carries no
// hostnames, and an expired cert, a new IP or a Pi with no RTC can't fail a
// request. Renewal re-issues the certificate on the same key, so the pin
// never changes by itself; a pin change is a key rotation (ut-docs#2737).
//
// TLS is never the cause of a failed request: if the key can't be loaded or
// created, the caller serves plain HTTP only, exactly as before this
// package existed.
package lantls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
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
	pin     string
}

// Pin is the hex SHA-256 of the certificate's SubjectPublicKeyInfo — what a
// peer pins. It depends only on the key, so renewal keeps it.
func (c *Cert) Pin() string { return c.pin }

// Leaf is the parsed certificate being served.
func (c *Cert) Leaf() *x509.Certificate { return c.leaf }

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
	key, err := loadOrCreateKey(filepath.Join(dir, privateKeyFile))
	if err != nil {
		return nil, err
	}
	leaf := loadLeaf(filepath.Join(dir, certFile), &key.PublicKey)
	if leaf == nil || now.Add(RenewBefore).After(leaf.NotAfter) {
		der, err := issue(key, now)
		if err != nil {
			return nil, err
		}
		if err := writeAtomic(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		if leaf, err = x509.ParseCertificate(der); err != nil {
			return nil, fmt.Errorf("lantls: parse issued cert: %w", err)
		}
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return &Cert{
		tlsCert: tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf},
		leaf:    leaf,
		pin:     hex.EncodeToString(sum[:]),
	}, nil
}

// loadOrCreateKey reads the key, or creates one if the file doesn't exist.
// An unreadable or unparsable key is an error and is never replaced: a new
// key would silently change the pin every paired till trusts.
func loadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("lantls: generate key: %w", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("lantls: marshal key: %w", err)
		}
		if err := writeAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lantls: read key: %w", err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("lantls: %s is not PEM (left as is)", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("lantls: parse key %s (left as is): %w", path, err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("lantls: %s is not an ECDSA P-256 key (left as is)", path)
	}
	return key, nil
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
	if runtime.GOOS != "windows" {
		if d, err := os.Open(filepath.Dir(path)); err == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	return nil
}
