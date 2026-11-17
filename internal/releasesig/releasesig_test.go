package releasesig

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

var data = []byte("abc  unitill-pos_1.0.0_linux_amd64.tar.gz\n")

func TestVerify(t *testing.T) {
	a, b := testKey(1), testKey(2)
	lineA := testSign(testSeed(1), data)
	lineB := testSign(testSeed(2), data)
	lineC := testSign(testSeed(3), data)
	tampered := append([]byte{}, data...)
	tampered[0] ^= 1

	longB64 := base64.StdEncoding.EncodeToString(make([]byte, 63))
	idA := a.ID
	many := strings.Repeat(lineA+"\n", 9)

	cases := []struct {
		name string
		keys []Key
		data []byte
		sig  string
		want error
		id   string
	}{
		{"good", []Key{a}, data, lineA + "\n", nil, a.ID},
		{"two lines second valid", []Key{a, b}, data, lineC + "\n" + lineB + "\n", nil, b.ID},
		{"unknown key only", []Key{a}, data, lineC + "\n", ErrUnknownKey, ""},
		{"bad signature", []Key{a}, tampered, lineA + "\n", ErrBadSignature, ""},
		{"wrong prefix", []Key{a}, data, "rsa " + idA + " " + strings.Fields(lineA)[2], ErrMalformed, ""},
		{"short id", []Key{a}, data, "ed25519 abcd " + strings.Fields(lineA)[2], ErrMalformed, ""},
		{"uppercase id", []Key{a}, data, "ed25519 " + strings.ToUpper(idA) + " " + strings.Fields(lineA)[2], ErrMalformed, ""},
		{"non base64", []Key{a}, data, "ed25519 " + idA + " !!!notb64", ErrMalformed, ""},
		{"sig not 64 bytes", []Key{a}, data, "ed25519 " + idA + " " + longB64, ErrMalformed, ""},
		{"junk next to valid", []Key{a}, data, lineA + "\njunk\n", ErrMalformed, ""},
		{"oversize", []Key{a}, data, lineA + "\n" + strings.Repeat("#", maxSigBytes), ErrMalformed, ""},
		{"too many lines", []Key{a}, data, many, ErrMalformed, ""},
		{"rotation A accepted by {A,B}", []Key{a, b}, data, lineA + "\n", nil, a.ID},
		{"rotation B accepted by {A,B}", []Key{a, b}, data, lineB + "\n", nil, b.ID},
		{"rotation A refused by {B}", []Key{b}, data, lineA + "\n", ErrUnknownKey, ""},
		{"no keys", nil, data, lineA + "\n", ErrNoKeys, ""},
		{"empty sig", []Key{a}, data, "", ErrMalformed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := Verify(c.keys, c.data, []byte(c.sig))
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if id != c.id {
				t.Fatalf("id = %q, want %q", id, c.id)
			}
		})
	}
}

func TestParseKeys(t *testing.T) {
	if ks, err := ParseKeys(""); err != nil || len(ks) != 0 {
		t.Fatalf("empty: %v %v", ks, err)
	}
	ks, err := ParseKeys(testPubHex(1) + "," + testPubHex(2))
	if err != nil || len(ks) != 2 || ks[0].ID != KeyID(testPub(1)) {
		t.Fatalf("two: %v %v", ks, err)
	}
	five := []string{testPubHex(1), testPubHex(2), testPubHex(3), testPubHex(4), testPubHex(5)}
	for name, in := range map[string]string{
		"five":      strings.Join(five, ","),
		"dupes":     testPubHex(1) + "," + testPubHex(1),
		"bad hex":   strings.Repeat("zz", 32),
		"short":     testPubHex(1)[:62],
		"upper":     strings.ToUpper(testPubHex(1)),
		"space":     testPubHex(1) + ", " + testPubHex(2),
		"trailing,": testPubHex(1) + ",",
	} {
		if _, err := ParseKeys(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestKeyIDStable(t *testing.T) {
	if got := KeyID(testPub(1)); got != KeyID(testPub(1)) || len(got) != 16 || got == KeyID(testPub(2)) {
		t.Fatalf("KeyID = %q", got)
	}
}
