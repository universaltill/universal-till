package plugins

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// OpenBlobImage opens one of pluginID's committed blobs for core to serve to
// the till's own page as an image — a suggestion thumbnail (ADR-0121 §7,
// ut-docs#3957). Only the named plugin's store is reachable: the id and the
// name are validated exactly as the blob_* host functions do (no paths, no
// "..", no in-flight put), the joined path must stay directly inside the
// store, and a symlink or anything but a regular file is not a blob. The
// type is sniffed from the bytes, never taken from the name: JPEG, PNG and
// WebP only — never SVG or HTML, which could run script on the till's
// origin. On a nil error the caller owns f, positioned at the start.
func OpenBlobImage(pluginID, name string, maxBytes int64) (f *os.File, ctype string, size int64, err error) {
	if validatePluginID(pluginID) != nil || !validBlobName(name) {
		return nil, "", 0, ErrBlobNotFound
	}
	dir := (&hostState{pluginID: pluginID}).blobDir()
	p, ok := blobPath(dir, name)
	if !ok {
		return nil, "", 0, ErrBlobNotFound
	}
	// Lstat first: a symlink is refused, not followed out of the store.
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", 0, ErrBlobNotFound
	}
	if info.Size() > maxBytes {
		return nil, "", 0, ErrBlobTooLarge
	}
	f, err = os.Open(p)
	if err != nil {
		return nil, "", 0, ErrBlobNotFound
	}
	// Re-check the opened file: the path could have been swapped between
	// the Lstat and the Open.
	if st, serr := f.Stat(); serr != nil || !os.SameFile(info, st) {
		_ = f.Close()
		return nil, "", 0, ErrBlobNotFound
	}
	head := make([]byte, 512)
	n, rerr := io.ReadFull(f, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		_ = f.Close()
		return nil, "", 0, fmt.Errorf("%w: read: %v", ErrBlobNotFound, rerr)
	}
	ctype = http.DetectContentType(head[:n])
	if n == 0 || !blobImageTypes[ctype] {
		_ = f.Close()
		return nil, "", 0, ErrBlobNotImage
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, "", 0, fmt.Errorf("%w: seek: %v", ErrBlobNotFound, err)
	}
	return f, ctype, info.Size(), nil
}

// blobImageTypes: the sniffed types OpenBlobImage serves.
var blobImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

var (
	// ErrBlobNotFound: no such committed blob in the plugin's own store
	// (or the id or name is not a legal one).
	ErrBlobNotFound = errors.New("plugin blob not found")
	// ErrBlobNotImage: the blob is not a JPEG, PNG or WebP image.
	ErrBlobNotImage = errors.New("plugin blob is not a JPEG, PNG or WebP image")
	// ErrBlobTooLarge: the blob is over the caller's size cap.
	ErrBlobTooLarge = errors.New("plugin blob too large")
)

// ValidBlobName is validBlobName for callers outside this package — the
// plugin-view validator checks a suggestion's thumbnail name with it.
func ValidBlobName(name string) bool { return validBlobName(name) }
