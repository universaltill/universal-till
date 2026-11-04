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
	lineA := Sign(testSeed(1), data)
	lineB := Sign(testSeed(2), data)
	lineC := Sign(testSeed(3), data)
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

func TestAppendKeepsOldLines(t *testing.T) {
	l1, l2 := Sign(testSeed(1), data), Sign(testSeed(2), data)
	out := Append([]byte(l1), l2)
	if string(out) != l1+"\n"+l2+"\n" {
		t.Fatalf("got %q", out)
	}
	if string(Append(nil, l1)) != l1+"\n" {
		t.Fatal("nil existing")
	}
	if _, err := Verify([]Key{testKey(1), testKey(2)}, data, out); err != nil {
		t.Fatal(err)
	}
}

// Re-running macos-dmg-attach must not grow the .sig (ADR-0178): each run
// re-signs a changed checksums.txt, keeping only lines that still cover a
// checksums.txt a till could read.
func TestAppendPrunedIdempotentOverReRuns(t *testing.T) {
	pre := []byte("aaa  x\n")
	sig := Append(nil, Sign(testSeed(1), pre)) // checksums job
	cur := pre
	for run := 0; run < 12; run++ {
		old := cur
		// each re-run rebuilds the .dmg: a new line content every time
		cur = []byte("aaa  x\nd" + string(rune('a'+run)) + "d  y.dmg\n")
		sig = AppendPruned(sig, Sign(testSeed(1), cur), testPub(1), old)
		lines := strings.Count(string(sig), "\n")
		if lines > 2 {
			t.Fatalf("run %d: %d lines, want <= 2", run, lines)
		}
		for _, d := range [][]byte{old, cur} {
			if _, err := Verify([]Key{testKey(1)}, d, sig); err != nil {
				t.Fatalf("run %d: %v", run, err)
			}
		}
	}
	// Re-signing the same bytes does not grow the file.
	n := string(sig)
	again := AppendPruned(sig, Sign(testSeed(1), cur), testPub(1), cur)
	if c := strings.Count(string(again), "\n"); c != 1 {
		t.Fatalf("same-bytes re-sign left %d lines (%s)", c, n)
	}
	// Another key's line is never dropped.
	other := Sign(testSeed(2), pre)
	out := AppendPruned([]byte(other+"\n"), Sign(testSeed(1), cur), testPub(1), cur)
	if !strings.Contains(string(out), other) {
		t.Fatal("other key's line dropped")
	}
}
