// Package offsitebackup copies the main till's daily snapshot off the till,
// encrypted on the till, and brings a cloud backup back onto a replacement
// till after the shop owner approves it (ADR-0199, ut-docs#5066).
//
// Nothing here sits on the sale path: the uploader is its own goroutine, it
// reads only files under backups/, and the per-backup key lives in memory for
// the length of one seal or one open.
package offsitebackup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Format v1 (design note §2). Header, 32 bytes:
//
//	0  4  "UTBK"
//	4  1  version 0x01
//	5  1  suite 0x01 = gzip + AES-256-GCM STREAM + HKDF-SHA256
//	6  1  chunk-size exponent 18 (plaintext chunk = 262144 bytes)
//	7  1  reserved 0
//	8  16 backup_id (raw UUID bytes)
//	24 7  nonce prefix (crypto/rand)
//	31 1  reserved 0
//
// Body: chunks i = 0..n-1, each u32be(len) || AES-256-GCM(key, nonce_i, p_i,
// aad = header) with nonce_i = prefix || u32be(i) || last (0x01 on the final
// chunk). The reader rejects truncation, reordering, trailing bytes and any
// tampering as ErrCorrupt.
const (
	// HeaderSize is the length of the fixed header.
	HeaderSize = 32
	// ChunkSize is the plaintext size of every chunk but the last.
	ChunkSize = 1 << 18
	gcmTag    = 16

	formatVersion = 0x01
	suiteV1       = 0x01
	chunkExp      = 18
	// KeySize is the AES-256 key length.
	KeySize = 32
)

var magic = [4]byte{'U', 'T', 'B', 'K'}

// ErrCorrupt means the stream is not a valid, complete backup for this key
// and id: wrong key, wrong id, damaged, truncated or extended.
var ErrCorrupt = errors.New("offsitebackup: corrupt or wrong-key backup")

// Seal reads plaintext from src and writes the encrypted stream to w. It
// holds at most two chunks in memory (one of look-ahead, so the last chunk
// can be marked), whatever the input size.
func Seal(w io.Writer, key []byte, backupID [16]byte, src io.Reader) error {
	var prefix [7]byte
	if _, err := rand.Read(prefix[:]); err != nil {
		return fmt.Errorf("offsitebackup: nonce prefix: %w", err)
	}
	return seal(w, key, backupID, prefix, src)
}

func header(backupID [16]byte, prefix [7]byte) [HeaderSize]byte {
	var h [HeaderSize]byte
	copy(h[0:4], magic[:])
	h[4] = formatVersion
	h[5] = suiteV1
	h[6] = chunkExp
	copy(h[8:24], backupID[:])
	copy(h[24:31], prefix[:])
	return h
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("offsitebackup: key must be %d bytes", KeySize)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func nonce(prefix [7]byte, i uint32, last bool) [12]byte {
	var n [12]byte
	copy(n[0:7], prefix[:])
	binary.BigEndian.PutUint32(n[7:11], i)
	if last {
		n[11] = 1
	}
	return n
}

func seal(w io.Writer, key []byte, backupID [16]byte, prefix [7]byte, src io.Reader) error {
	aead, err := newGCM(key)
	if err != nil {
		return err
	}
	h := header(backupID, prefix)
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	cur := make([]byte, ChunkSize)
	next := make([]byte, ChunkSize)
	out := make([]byte, 4, 4+ChunkSize+gcmTag)
	n, rerr := io.ReadFull(src, cur)
	if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
		return rerr
	}
	var idx uint32
	for {
		last := rerr != nil // short or empty read: this is the final chunk
		var nn int
		if !last {
			nn, rerr = io.ReadFull(src, next)
			if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
				return rerr
			}
			// EOF with nothing more means cur was the last chunk.
			if rerr == io.EOF {
				last = true
			}
		}
		nc := nonce(prefix, idx, last)
		out = out[:4]
		out = aead.Seal(out, nc[:], cur[:n], h[:])
		binary.BigEndian.PutUint32(out[:4], uint32(len(out)-4))
		if _, err := w.Write(out); err != nil {
			return err
		}
		if last {
			return nil
		}
		idx++
		cur, next = next, cur
		n = nn
	}
}

// ParseHeader checks a header and returns its backup id and nonce prefix. It
// is what the cloud runs on part 1 (magic, version, suite, chunk size).
func ParseHeader(h []byte) (backupID [16]byte, prefix [7]byte, err error) {
	if len(h) < HeaderSize || h[0] != magic[0] || h[1] != magic[1] || h[2] != magic[2] || h[3] != magic[3] ||
		h[4] != formatVersion || h[5] != suiteV1 || h[6] != chunkExp || h[7] != 0 || h[31] != 0 {
		return backupID, prefix, ErrCorrupt
	}
	copy(backupID[:], h[8:24])
	copy(prefix[:], h[24:31])
	return backupID, prefix, nil
}

// Open decrypts the stream src into dst. It verifies every chunk before
// writing it, but a truncated or extended stream is only known at the end:
// the caller must discard whatever dst received when Open returns an error.
func Open(dst io.Writer, key []byte, wantID [16]byte, src io.Reader) error {
	aead, err := newGCM(key)
	if err != nil {
		return err
	}
	var h [HeaderSize]byte
	if _, err := io.ReadFull(src, h[:]); err != nil {
		return corrupt(err)
	}
	id, prefix, err := ParseHeader(h[:])
	if err != nil || id != wantID {
		return ErrCorrupt
	}
	// readLen returns the next chunk length, or clean=true at a clean EOF.
	readLen := func() (n uint32, clean bool, err error) {
		var b [4]byte
		_, err = io.ReadFull(src, b[:])
		if err == io.EOF {
			return 0, true, nil
		}
		if err != nil {
			return 0, false, corrupt(err)
		}
		return binary.BigEndian.Uint32(b[:]), false, nil
	}
	buf := make([]byte, ChunkSize+gcmTag)
	plain := make([]byte, 0, ChunkSize)
	n, clean, err := readLen()
	if err != nil {
		return err
	}
	if clean {
		return ErrCorrupt // no chunk at all: no final chunk was seen
	}
	var idx uint32
	for {
		if n < gcmTag || n > ChunkSize+gcmTag {
			return ErrCorrupt
		}
		if _, err := io.ReadFull(src, buf[:n]); err != nil {
			return corrupt(err)
		}
		nextN, last, err := readLen()
		if err != nil {
			return err
		}
		nc := nonce(prefix, idx, last)
		pt, err := aead.Open(plain[:0], nc[:], buf[:n], h[:])
		if err != nil {
			return ErrCorrupt
		}
		if _, err := dst.Write(pt); err != nil {
			return err
		}
		if last {
			return nil
		}
		idx++
		n = nextN
	}
}

func corrupt(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return ErrCorrupt
	}
	return err
}
