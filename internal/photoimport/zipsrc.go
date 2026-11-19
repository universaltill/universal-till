package photoimport

import (
	"archive/zip"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
)

// Zip limits.
const (
	MaxZipEntries = 10000
)

// ErrBadZip: the upload is not a readable ZIP or has too many entries.
var ErrBadZip = errors.New("photoimport: unreadable zip")

// Zip indexes an uploaded archive by file name. Entries are only ever read
// in memory (never extracted to disk), so path tricks inside names cannot
// write anywhere; names are matched by their base name only.
type Zip struct {
	byName  map[string]*zip.File // lower-cased base name -> entry
	bySKU   map[string]*zip.File // lower-cased base name without extension
	dupSKU  map[string]bool
	dupName map[string]bool // lower-cased base names carried by two entries
}

// OpenZip indexes the archive in ra.
func OpenZip(ra io.ReaderAt, size int64) (*Zip, error) {
	if !centralDirOK(ra, size) {
		return nil, ErrBadZip
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil || len(zr.File) > MaxZipEntries {
		return nil, ErrBadZip
	}
	z := &Zip{byName: map[string]*zip.File{}, bySKU: map[string]*zip.File{}, dupSKU: map[string]bool{}, dupName: map[string]bool{}}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() {
			continue
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		base := path.Base(name)
		if base == "." || base == "/" || strings.HasPrefix(base, ".") {
			continue
		}
		key := strings.ToLower(base)
		if _, dup := z.byName[key]; dup {
			z.dupName[key] = true
			z.dupSKU[strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))] = true
		}
		z.byName[key] = f
		stem := strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
		if _, dup := z.bySKU[stem]; dup {
			z.dupSKU[stem] = true
		}
		z.bySKU[stem] = f
	}
	return z, nil
}

// Find returns the entry for a CSV ref (its base name) or, with an empty
// ref, the entry named <sku>.<ext>. ambiguous is true when two entries
// would answer.
func (z *Zip) Find(sku, ref string) (f *zip.File, ambiguous bool) {
	if ref == "" {
		stem := strings.ToLower(sku)
		return z.bySKU[stem], z.dupSKU[stem]
	}
	base := path.Base(strings.ReplaceAll(ref, `\`, "/"))
	key := strings.ToLower(base)
	return z.byName[key], z.dupName[key]
}

// Stems lists the SKU stems of every entry, sorted (for ZIP-only runs, so
// line numbers are stable between preview and apply).
func (z *Zip) Stems() []string {
	out := make([]string, 0, len(z.bySKU))
	for _, f := range z.bySKU {
		b := path.Base(strings.ReplaceAll(f.Name, `\`, "/"))
		out = append(out, strings.TrimSuffix(b, path.Ext(b)))
	}
	sort.Strings(out)
	return out
}

// Read reads one entry, refusing a declared or actual size over max.
func Read(f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, ErrTooBig
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrNotFound
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, ErrNotFound
	}
	if int64(len(b)) > max {
		return nil, ErrTooBig
	}
	return b, nil
}

// centralDirOK reads the end-of-central-directory record and refuses an
// archive that declares more entries (or a bigger directory) than we accept,
// BEFORE archive/zip builds one object per record (a directory made of
// millions of empty records would otherwise exhaust memory).
func centralDirOK(ra io.ReaderAt, size int64) bool {
	const eocdLen = 22
	if size < eocdLen {
		return false
	}
	n := int64(eocdLen + 65535)
	if n > size {
		n = size
	}
	buf := make([]byte, n)
	if _, err := ra.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return false
	}
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if buf[i] == 'P' && buf[i+1] == 'K' && buf[i+2] == 5 && buf[i+3] == 6 {
			entries := int(buf[i+10]) | int(buf[i+11])<<8
			cdSize := uint32(buf[i+12]) | uint32(buf[i+13])<<8 | uint32(buf[i+14])<<16 | uint32(buf[i+15])<<24
			// 0xFFFF / 0xFFFFFFFF = Zip64 markers: far over the caps anyway.
			cdOff := int64(buf[i+16]) | int64(buf[i+17])<<8 | int64(buf[i+18])<<16 | int64(buf[i+19])<<24
			eocdPos := size - n + int64(i)
			// archive/zip keeps reading records until one is invalid, and
			// compares the count only modulo 65536: pin the directory to
			// exactly [offset, offset+size) ending at this record.
			return entries <= MaxZipEntries && cdSize <= maxZipDirBytes && cdOff+int64(cdSize) == eocdPos
		}
	}
	return false
}

// maxZipDirBytes bounds the central directory (names + extra fields).
const maxZipDirBytes = 4 << 20
