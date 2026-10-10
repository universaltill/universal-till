package server

import (
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestWithLANTLS_ServesTLSAndCreatesKey(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "tls")
	ln := withLANTLS(inner, dir, false)
	defer ln.Close()
	if ln == inner {
		t.Fatal("listener not wrapped: TLS not served")
	}
	if _, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil {
		t.Fatalf("key not created under the tls dir: %v", err)
	}
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	c, err := tls.Dial("tcp", inner.Addr().String(), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // handshake only
	if err != nil {
		t.Fatalf("TLS handshake on the till port: %v", err)
	}
	c.Close()
}

// TLS is never the cause of a failed request: an unusable key leaves the
// till serving plain HTTP.
func TestWithLANTLS_BadKeyFallsBackToPlain(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ln := withLANTLS(inner, dir, false); ln != inner {
		t.Fatal("bad key: want the plain listener back unchanged")
	}
}

func TestWithLANTLS_DemoStaysPlain(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	dir := filepath.Join(t.TempDir(), "tls")
	if ln := withLANTLS(inner, dir, true); ln != inner {
		t.Fatal("demo till: want plain listener")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("demo till created a LAN key dir (%v)", err)
	}
}
