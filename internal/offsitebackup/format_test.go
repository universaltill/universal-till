package offsitebackup

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

var (
	testKey = bytes.Repeat([]byte{0x42}, KeySize)
	testID  = [16]byte{0x6f, 0x1c, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
)

func randBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sealBytes(t testing.TB, pt []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Seal(&out, testKey, testID, bytes.NewReader(pt)); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 3*ChunkSize + 17, 5 << 20} {
		pt := randBytes(t, n)
		ct := sealBytes(t, pt)
		var got bytes.Buffer
		if err := Open(&got, testKey, testID, bytes.NewReader(ct)); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(got.Bytes(), pt) {
			t.Fatalf("n=%d: round trip differs", n)
		}
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	pt := randBytes(t, 3*ChunkSize+5) // four chunks
	ct := sealBytes(t, pt)
	chunkLen := 4 + ChunkSize + gcmTag
	chunk := func(i int) []byte { return ct[HeaderSize+i*chunkLen : HeaderSize+(i+1)*chunkLen] }

	swapped := append([]byte(nil), ct...)
	copy(swapped[HeaderSize:], chunk(1))
	copy(swapped[HeaderSize+chunkLen:], chunk(0))

	dropped := append(append([]byte(nil), ct[:HeaderSize+chunkLen]...), ct[HeaderSize+2*chunkLen:]...)
	flippedHeader := append([]byte(nil), ct...)
	flippedHeader[24] ^= 1 // nonce prefix: also bound into the AAD
	badMagic := append([]byte(nil), ct...)
	badMagic[0] = 'X'
	flippedBody := append([]byte(nil), ct...)
	flippedBody[HeaderSize+100] ^= 1
	tooLong := append([]byte(nil), ct...)
	tooLong[HeaderSize+3] ^= 0xff // length > max
	otherID := append([]byte(nil), ct...)
	otherID[8] ^= 1

	cases := map[string]struct {
		in  []byte
		key []byte
	}{
		"truncated at chunk boundary": {ct[:HeaderSize+3*chunkLen], testKey},
		"truncated mid chunk":         {ct[:len(ct)-3], testKey},
		"header only":                 {ct[:HeaderSize], testKey},
		"short header":                {ct[:10], testKey},
		"empty":                       {nil, testKey},
		"dropped chunk":               {dropped, testKey},
		"swapped chunks":              {swapped, testKey},
		"flipped header byte":         {flippedHeader, testKey},
		"bad magic":                   {badMagic, testKey},
		"other backup id in header":   {otherID, testKey},
		"flipped body byte":           {flippedBody, testKey},
		"absurd chunk length":         {tooLong, testKey},
		"trailing bytes":              {append(append([]byte(nil), ct...), 0), testKey},
		"trailing garbage chunk":      {append(append([]byte(nil), ct...), ct[HeaderSize:HeaderSize+chunkLen]...), testKey},
		"wrong key":                   {ct, bytes.Repeat([]byte{7}, KeySize)},
	}
	for name, c := range cases {
		var out bytes.Buffer
		err := Open(&out, c.key, testID, bytes.NewReader(c.in))
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
	// A different expected id is refused too.
	if err := Open(&bytes.Buffer{}, testKey, [16]byte{9}, bytes.NewReader(ct)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong expected id: %v", err)
	}
}

func TestParseHeader(t *testing.T) {
	ct := sealBytes(t, []byte("x"))
	id, _, err := ParseHeader(ct[:HeaderSize])
	if err != nil || id != testID {
		t.Fatalf("id=%x err=%v", id, err)
	}
	bad := append([]byte(nil), ct[:HeaderSize]...)
	bad[4] = 2
	if _, _, err := ParseHeader(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("version 2 accepted: %v", err)
	}
}

// The golden vector is the contract with ut-cloud: its header parser test
// copies the first 32 bytes (golden_v1.hex, first line). Regenerate only with
// UPDATE_GOLDEN=1 and a format version bump.
var (
	goldenKey    = bytes.Repeat([]byte{0x00, 0x11, 0x22, 0x33}, 8)
	goldenID     = [16]byte{0x6f, 0x1c, 0xa0, 0x01, 0x11, 0x22, 0x43, 0x44, 0x85, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc}
	goldenPrefix = [7]byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7}
	goldenPlain  = []byte("universal till golden vector v1")
)

func TestGoldenVector(t *testing.T) {
	var out bytes.Buffer
	if err := seal(&out, goldenKey, goldenID, goldenPrefix, bytes.NewReader(goldenPlain)); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/golden_v1.hex"
	line := hex.EncodeToString(out.Bytes()[:HeaderSize]) + "\n" + hex.EncodeToString(out.Bytes()) + "\n"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != line {
		t.Fatalf("golden vector changed:\n got %s\nwant %s", line, want)
	}
	// And the stored bytes still open with the fixed key.
	lines := bytes.Split(bytes.TrimSpace(want), []byte("\n"))
	raw, _ := hex.DecodeString(string(lines[1]))
	var pt bytes.Buffer
	if err := Open(&pt, goldenKey, goldenID, bytes.NewReader(raw)); err != nil || !bytes.Equal(pt.Bytes(), goldenPlain) {
		t.Fatalf("golden open: %v %q", err, pt.Bytes())
	}
}
