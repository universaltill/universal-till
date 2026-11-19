package offsitebackup

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRecoveryStringRoundTripAndShape(t *testing.T) {
	dek := make([]byte, KeySize)
	for i := range dek {
		dek[i] = byte(i * 7)
	}
	s := EncodeRecovery(3, dek)
	if !strings.HasPrefix(s, "UTK1-") {
		t.Fatalf("prefix: %s", s)
	}
	body := strings.TrimPrefix(s, "UTK1-")
	for i, g := range strings.Split(body, "-") {
		if len(g) > 4 || len(g) == 0 {
			t.Fatalf("group %d is %q", i, g)
		}
	}
	if strings.ContainsAny(body, "ILOU") {
		t.Errorf("not Crockford base32: %s", body)
	}
	gen, got, err := DecodeRecovery(s)
	if err != nil || gen != 3 || !bytes.Equal(got, dek) {
		t.Fatalf("round trip: %d %v", gen, err)
	}
	// Forgiving of what a person types: case, spaces, O/I/L look-alikes.
	loose := strings.ToLower(strings.ReplaceAll(s, "-", " "))
	if g, d, err := DecodeRecovery(loose); err != nil || g != 3 || !bytes.Equal(d, dek) {
		t.Fatalf("loose input: %v", err)
	}
	if Fingerprint(got) != Fingerprint(dek) {
		t.Error("fingerprint changed")
	}
}

func TestRecoveryStringRejectsDamage(t *testing.T) {
	dek := bytes.Repeat([]byte{0x5a}, KeySize)
	s := EncodeRecovery(1, dek)
	// Change one character: the checksum catches it.
	b := []byte(s)
	i := len(b) - 3
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	if _, _, err := DecodeRecovery(string(b)); !errors.Is(err, ErrBadRecoveryKey) {
		t.Fatalf("damaged string accepted: %v", err)
	}
	for _, bad := range []string{"", "UTK1-", "UTK2-" + s[5:], s[:len(s)-4], s + "0000", "hello"} {
		if _, _, err := DecodeRecovery(bad); !errors.Is(err, ErrBadRecoveryKey) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}
