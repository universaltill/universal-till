package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedHex(n byte) string { return hex.EncodeToString(bytes.Repeat([]byte{n}, 32)) }
func pubHex(n byte) string {
	return hex.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{n}, 32)).Public().(ed25519.PublicKey))
}

func TestSignThenVerifyAndDmgReSign(t *testing.T) {
	dir := t.TempDir()
	sums := filepath.Join(dir, "checksums.txt")
	os.WriteFile(sums, []byte("abc  x\n"), 0o644)
	sig := filepath.Join(dir, "checksums.txt.sig")
	env := func(k string) string {
		return map[string]string{"UT_RELEASE_SIGNING_KEY": seedHex(1), "UT_RELEASE_KEYS_HEX": pubHex(1) + "," + pubHex(2)}[k]
	}
	var out bytes.Buffer
	if err := run([]string{"sign", "--checksums", sums, "--out", sig}, &out, env); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--checksums", sums, "--sig", sig}, &out, env); err != nil {
		t.Fatal(err)
	}
	// dmg-attach shape: checksums change, old sig kept, new line appended.
	old := filepath.Join(dir, "old.sig")
	os.Rename(sig, old)
	os.WriteFile(sums, []byte("abc  x\ndef  y\n"), 0o644)
	if err := run([]string{"sign", "--checksums", sums, "--existing-sig", old, "--out", sig}, &out, env); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--checksums", sums, "--sig", sig}, &out, env); err != nil {
		t.Fatal(err)
	}
	// A tampered file fails.
	os.WriteFile(sums, []byte("zzz  x\n"), 0o644)
	if err := run([]string{"verify", "--checksums", sums, "--sig", sig}, &out, env); err == nil {
		t.Fatal("tampered checksums verified")
	}
}

func TestSignRefusesKeyNotInList(t *testing.T) {
	dir := t.TempDir()
	sums := filepath.Join(dir, "c")
	os.WriteFile(sums, []byte("a  b\n"), 0o644)
	env := func(k string) string {
		return map[string]string{"UT_RELEASE_SIGNING_KEY": seedHex(9), "UT_RELEASE_KEYS_HEX": pubHex(1)}[k]
	}
	err := run([]string{"sign", "--checksums", sums, "--out", filepath.Join(dir, "o")}, &bytes.Buffer{}, env)
	if err == nil || !strings.Contains(err.Error(), "not in UT_RELEASE_KEYS_HEX") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), seedHex(9)) {
		t.Fatal("seed leaked")
	}
}

func TestBadSeedNeverEchoed(t *testing.T) {
	env := func(k string) string {
		return map[string]string{"UT_RELEASE_SIGNING_KEY": "SECRETnothex", "UT_RELEASE_KEYS_HEX": pubHex(1)}[k]
	}
	err := run([]string{"sign", "--checksums", "x", "--out", "y"}, &bytes.Buffer{}, env)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

// macos-dmg-attach can be re-run: the .sig must stay at two lines.
func TestDmgReSignReRunsStayBounded(t *testing.T) {
	dir := t.TempDir()
	sums := filepath.Join(dir, "checksums.txt")
	sig := filepath.Join(dir, "checksums.txt.sig")
	env := func(k string) string {
		return map[string]string{"UT_RELEASE_SIGNING_KEY": seedHex(1), "UT_RELEASE_KEYS_HEX": pubHex(1) + "," + pubHex(2)}[k]
	}
	var out bytes.Buffer
	os.WriteFile(sums, []byte("abc  x\n"), 0o644)
	if err := run([]string{"sign", "--checksums", sums, "--out", sig}, &out, env); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		old := filepath.Join(dir, "old.txt")
		b, _ := os.ReadFile(sums)
		os.WriteFile(old, b, 0o644)
		os.WriteFile(sums, []byte("abc  x\nd"+string(rune('a'+i))+"  y.dmg\n"), 0o644)
		next := filepath.Join(dir, "new.sig")
		if err := run([]string{"sign", "--checksums", sums, "--existing-sig", sig, "--keep-checksums", old, "--out", next}, &out, env); err != nil {
			t.Fatal(err)
		}
		os.Rename(next, sig)
		for _, f := range []string{old, sums} {
			if err := run([]string{"verify", "--checksums", f, "--sig", sig}, &out, env); err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
		}
		raw, _ := os.ReadFile(sig)
		if n := bytes.Count(raw, []byte("\n")); n > 2 {
			t.Fatalf("run %d: %d lines", i, n)
		}
	}
}
