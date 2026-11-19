// Package photoimport bulk-imports item photos by SKU (ut-docs#6527): a CSV
// of sku + image (a file inside a chosen folder, an entry of an uploaded ZIP,
// or an https URL). It only reads and validates; writing the photo is the
// caller's Store callback, so preview and apply share one code path.
package photoimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// MaxCSVBytes caps the CSV file; MaxRows caps rows per run.
const (
	MaxCSVBytes = 5 << 20
	MaxRows     = 5000
)

var (
	// ErrNoSKUColumn: the header row has no sku column.
	ErrNoSKUColumn = errors.New("photoimport: no sku column")
	// ErrCSVTooLarge: the file is over MaxCSVBytes or MaxRows rows.
	ErrCSVTooLarge = errors.New("photoimport: csv too large")
	// ErrBadCSV: the file is not readable as CSV.
	ErrBadCSV = errors.New("photoimport: unreadable csv")
)

// Row is one CSV data row. Line is the 1-based file line (header = 1).
type Row struct {
	Line int
	SKU  string
	Ref  string // file path, zip entry name or https URL; "" = look up <SKU>.<ext> in the ZIP
}

var skuHeaders = map[string]bool{"sku": true, "artikelnummer": true, "artikelnr": true, "artikel-nr": true}

var imageHeaders = map[string]bool{
	"image": true, "image_path": true, "image_url": true, "photo": true,
	"bild": true, "bildpfad": true, "foto": true, "imagepath": true, "imageurl": true,
}

// decodeText returns UTF-8 text: BOM stripped; bytes that are not valid
// UTF-8 are read as Windows-1252 (German Excel exports).
func decodeText(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(raw) {
		return string(raw)
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(raw)
	if err != nil {
		return strings.ToValidUTF8(string(raw), "?")
	}
	return string(out)
}

func detectDelimiter(text string) rune {
	first := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		first = text[:i]
	}
	best, n := ',', strings.Count(first, ",")
	for _, d := range []rune{';', '\t'} {
		if c := strings.Count(first, string(d)); c > n {
			best, n = d, c
		}
	}
	return best
}

// ParseCSV reads the sku and image columns. The image column is optional
// (a ZIP of <SKU>.<ext> files can stand alone); with it absent every Ref is "".
func ParseCSV(raw []byte) ([]Row, error) {
	if len(raw) > MaxCSVBytes {
		return nil, ErrCSVTooLarge
	}
	text := decodeText(raw)
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = detectDelimiter(text)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrNoSKUColumn
		}
		return nil, ErrBadCSV
	}
	skuCol, imgCol := -1, -1
	for i, h := range header {
		k := strings.ToLower(strings.TrimSpace(h))
		switch {
		case skuHeaders[k] && skuCol < 0:
			skuCol = i
		case imageHeaders[k] && imgCol < 0:
			imgCol = i
		}
	}
	if skuCol < 0 {
		return nil, ErrNoSKUColumn
	}
	var rows []Row
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrBadCSV
		}
		line, _ := r.FieldPos(0)
		row := Row{Line: line}
		if skuCol < len(rec) {
			row.SKU = strings.TrimSpace(rec[skuCol])
		}
		if imgCol >= 0 && imgCol < len(rec) {
			row.Ref = strings.TrimSpace(rec[imgCol])
		}
		if row.SKU == "" && row.Ref == "" {
			continue // blank line
		}
		rows = append(rows, row)
		if len(rows) > MaxRows {
			return nil, ErrCSVTooLarge
		}
	}
	return rows, nil
}
