package hpke

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// RFC 9180 Appendix A.1.1: DHKEM(X25519, HKDF-SHA256), HKDF-SHA256,
// AES-128-GCM, mode_base, and its sequence-number-0 encryption. The values
// are copied from the RFC's published test vectors (the same file Go's
// toolchain ships as crypto/internal/hpke/testdata/rfc9180-vectors.json).
const (
	a11Info         = "4f6465206f6e2061204772656369616e2055726e"
	a11SkEm         = "52c4a758a802cd8b936eceea314432798d5baf2d7e9235dc084ab1b9cfa2f736"
	a11PkEm         = "37fda3567bdbd628e88668c3c8d7e97d1d1253b6d4ea6d44c150f741f1bf4431"
	a11SkRm         = "4612c550263fc8ad58375df3f557aac531d26850903e55a9f23f21d8534e8ac8"
	a11PkRm         = "3948cfe0ad1ddb695d780e59077195da6c56506b027329794ab02bca80815c4d"
	a11Enc          = "37fda3567bdbd628e88668c3c8d7e97d1d1253b6d4ea6d44c150f741f1bf4431"
	a11SharedSecret = "fe0e18c9f024ce43799ae393c7e8fe8fce9d218875e8227b0187c04e7d2ea1fc"
	a11Key          = "4531685d41d65f03dc48f6b8302c05b0"
	a11BaseNonce    = "56d890e5accaaf011cff4b7d"
	a11Pt           = "4265617574792069732074727574682c20747275746820626561757479"
	a11Aad          = "436f756e742d30"
	a11Ct           = "f938558b5d72f1a23810b4be2ab4f84331acc02fc97babc53a52ae8218a355a96d8770ac83d07bea87e13c512a"
)

func TestRFC9180A11_SenderSchedule(t *testing.T) {
	skE, err := ecdh.X25519().NewPrivateKey(unhex(t, a11SkEm))
	if err != nil {
		t.Fatal(err)
	}
	skR, err := ecdh.X25519().NewPrivateKey(unhex(t, a11SkRm))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(skE.PublicKey().Bytes()); got != a11PkEm {
		t.Fatalf("pkEm = %s", got)
	}
	if got := hex.EncodeToString(skR.PublicKey().Bytes()); got != a11PkRm {
		t.Fatalf("pkRm = %s", got)
	}
	enc, s, err := SetupBaseS(skR.PublicKey(), skE, unhex(t, a11Info))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"shared_secret", hex.EncodeToString(s.SharedSecret), a11SharedSecret},
		{"key", hex.EncodeToString(s.Key), a11Key},
		{"base_nonce", hex.EncodeToString(s.BaseNonce), a11BaseNonce},
		{"enc", hex.EncodeToString(enc), a11Enc},
	} {
		if c.got != c.want {
			t.Fatalf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
	gotEnc, ct, err := SealDeterministic(skR.PublicKey(), skE, unhex(t, a11Info), unhex(t, a11Aad), unhex(t, a11Pt))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(gotEnc) != a11Enc {
		t.Fatalf("seal enc = %x", gotEnc)
	}
	if got := hex.EncodeToString(ct); got != a11Ct {
		t.Fatalf("ct = %s, want %s", got, a11Ct)
	}
}

func TestRFC9180A11_Open(t *testing.T) {
	skR, err := ecdh.X25519().NewPrivateKey(unhex(t, a11SkRm))
	if err != nil {
		t.Fatal(err)
	}
	s, err := SetupBaseR(skR, unhex(t, a11Enc), unhex(t, a11Info))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(s.SharedSecret) != a11SharedSecret || hex.EncodeToString(s.Key) != a11Key || hex.EncodeToString(s.BaseNonce) != a11BaseNonce {
		t.Fatalf("receiver schedule diverges: %x %x %x", s.SharedSecret, s.Key, s.BaseNonce)
	}
	pt, err := Open(skR, unhex(t, a11Enc), unhex(t, a11Info), unhex(t, a11Aad), unhex(t, a11Ct))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(pt, unhex(t, a11Pt)) {
		t.Fatalf("pt = %x", pt)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	skR, _ := ecdh.X25519().NewPrivateKey(unhex(t, a11SkRm))
	enc, info, aad, ct := unhex(t, a11Enc), unhex(t, a11Info), unhex(t, a11Aad), unhex(t, a11Ct)
	flip := func(b []byte) []byte { c := append([]byte(nil), b...); c[0] ^= 1; return c }
	cases := map[string]func() error{
		"aad":  func() error { _, err := Open(skR, enc, info, flip(aad), ct); return err },
		"info": func() error { _, err := Open(skR, enc, flip(info), aad, ct); return err },
		"ct":   func() error { _, err := Open(skR, enc, info, aad, flip(ct)); return err },
		"enc":  func() error { _, err := Open(skR, flip(enc), info, aad, ct); return err },
		"short enc": func() error {
			_, err := Open(skR, enc[:31], info, aad, ct)
			return err
		},
		"other key": func() error {
			other, _ := ecdh.X25519().NewPrivateKey(unhex(t, a11SkEm))
			_, err := Open(other, enc, info, aad, ct)
			return err
		},
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: tampered input opened", name)
		}
	}
}

// An all-zero public key (a low-order point) makes the X25519 output zero;
// RFC 9180 §7.1.4 requires rejecting it.
func TestOpenRejectsLowOrderEnc(t *testing.T) {
	skR, _ := ecdh.X25519().NewPrivateKey(unhex(t, a11SkRm))
	if _, err := Open(skR, make([]byte, 32), nil, nil, make([]byte, 20)); err == nil {
		t.Fatal("zero enc accepted")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	skR, _ := ecdh.X25519().NewPrivateKey(unhex(t, a11SkRm))
	skE, _ := ecdh.X25519().NewPrivateKey(unhex(t, a11SkEm))
	enc, ct, err := SealDeterministic(skR.PublicKey(), skE, []byte("i"), []byte("a"), []byte("4821"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(skR, enc, []byte("i"), []byte("a"), ct)
	if err != nil || string(pt) != "4821" {
		t.Fatalf("round trip: %q %v", pt, err)
	}
}
