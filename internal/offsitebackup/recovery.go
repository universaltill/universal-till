package offsitebackup

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"strings"
)

// The key the owner saves (design section 15, E4): "UTK1-" followed by the
// Crockford base32 of generation (u16 big endian) || key (32 bytes) || the
// first 4 bytes of SHA-256 of those 34 bytes, in groups of 4 characters
// separated by "-". The checksum catches a mistyped or damaged copy before
// anything is decrypted.

const recoveryPrefix = "UTK1-"

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// ErrBadRecoveryKey: the text is not a complete, undamaged saved key.
var ErrBadRecoveryKey = errors.New("offsitebackup: that is not a valid saved backup key")

// EncodeRecovery renders generation and key as the string to save or print.
func EncodeRecovery(generation int, dek []byte) string {
	raw := make([]byte, 0, 2+KeySize+4)
	raw = binary.BigEndian.AppendUint16(raw, uint16(generation))
	raw = append(raw, dek...)
	sum := sha256.Sum256(raw)
	raw = append(raw, sum[:4]...)
	enc := crockford.EncodeToString(raw)
	zero(raw)
	var b strings.Builder
	b.WriteString("UTK1")
	for i := 0; i < len(enc); i += 4 {
		b.WriteByte('-')
		b.WriteString(enc[i:min(i+4, len(enc))])
	}
	return b.String()
}

// DecodeRecovery parses a saved key. It forgives case, spaces and dashes in
// the wrong place, and the look-alikes O, I and L, as Crockford base32 does.
func DecodeRecovery(s string) (int, []byte, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "UTK1") {
		return 0, nil, ErrBadRecoveryKey
	}
	s = s[len("UTK1"):]
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	raw, err := crockford.DecodeString(b.String())
	if err != nil || len(raw) != 2+KeySize+4 || crockford.EncodeToString(raw) != b.String() {
		return 0, nil, ErrBadRecoveryKey
	}
	sum := sha256.Sum256(raw[:2+KeySize])
	if !bytesEqual(sum[:4], raw[2+KeySize:]) {
		zero(raw)
		return 0, nil, ErrBadRecoveryKey
	}
	gen := int(binary.BigEndian.Uint16(raw[:2]))
	dek := append([]byte(nil), raw[2:2+KeySize]...)
	zero(raw)
	if gen < 1 {
		zero(dek)
		return 0, nil, ErrBadRecoveryKey
	}
	return gen, dek, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := range a {
		d |= a[i] ^ b[i]
	}
	return d == 0
}
