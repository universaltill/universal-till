package catimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Header-row claims (ADR-0164 §2, ut-docs#4526). A file-rows import plugin
// declares the column names that identify its system's export
// (`file_headers`); the host reads the uploaded file's header row with
// HeaderRow and offers the file to the plugin when HeadersClaim holds. The
// till and ut-cloud call these same two functions, so a file claimed on
// one is claimed on the other. Neither runs plugin code.

// HeaderRowMaxBytes bounds how much of a CSV is read to find its header
// row; a longer first record is ErrHeaderRowTooLong.
const HeaderRowMaxBytes = 64 << 10

// ErrHeaderRowTooLong: the CSV's first record is longer than
// HeaderRowMaxBytes.
var ErrHeaderRowTooLong = errors.New("header row longer than the read limit")

// zipMagic is a ZIP local-file header: an .xlsx workbook is a ZIP.
var zipMagic = []byte("PK\x03\x04")

// utf8BOM is the byte-order mark Excel puts in front of a CSV.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ErrNotText: a file not named as a workbook holds a ZIP, so it has no
// text header row to read.
var ErrNotText = errors.New("ZIP content in a file not named as a workbook")

// workbookExts are the extensions HeaderRow opens with the workbook parser.
var workbookExts = map[string]bool{".xlsx": true, ".xlsm": true}

// HeaderRow returns the file's header row: an .xlsx/.xlsm workbook's first
// non-blank row of its first sheet, or a CSV's first record (a UTF-8 BOM
// stripped). Cells are as written; HeadersClaim does the folding. size is
// the file's length; fileName (only its extension is read) picks the
// parser — a ZIP is opened as a workbook only when it is named as one, and
// is ErrNotText otherwise.
func HeaderRow(r io.ReaderAt, size int64, fileName string) ([]string, error) {
	head := make([]byte, min(size, HeaderRowMaxBytes+1))
	n, err := r.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read header: %w", err)
	}
	head = head[:n]
	if bytes.HasPrefix(head, zipMagic) {
		if !workbookExts[strings.ToLower(filepath.Ext(fileName))] {
			return nil, ErrNotText
		}
		return xlsxHeaderRow(r, size)
	}
	if len(head) > HeaderRowMaxBytes {
		head = head[:HeaderRowMaxBytes]
		if !bytes.ContainsAny(head, "\r\n") {
			return nil, ErrHeaderRowTooLong
		}
	}
	// Strip a UTF-8 BOM before parsing: left in front of a quoted first
	// field it would keep that field's quotes in its value.
	body := bytes.TrimPrefix(head, utf8BOM)
	cr := csv.NewReader(bytes.NewReader(body))
	cr.Comma = sniffDelimiter(body)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	rec, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	// A record that ran to the end of a truncated read may be cut short.
	if int64(len(head)) < size && cr.InputOffset() >= int64(len(body)) && !bytes.HasSuffix(head, []byte("\n")) {
		return nil, ErrHeaderRowTooLong
	}
	return rec, nil
}

// sniffDelimiter is the separator a CSV's first record uses: whichever of
// comma, semicolon or tab is most frequent outside quotes (Epos Now and
// German Excel write ";"). A tie, or none, is the comma. Only the first
// record is read, so a later row never changes the answer (ut-docs#4726).
func sniffDelimiter(head []byte) rune {
	counts := map[byte]int{}
	inQuotes := false
scan:
	for _, c := range head {
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case inQuotes:
		case c == '\n' || c == '\r':
			break scan
		case c == ',' || c == ';' || c == '\t':
			counts[c]++
		}
	}
	best, bestN := byte(','), counts[',']
	for _, c := range []byte{';', '\t'} {
		if counts[c] > bestN {
			best, bestN = c, counts[c]
		}
	}
	return rune(best)
}

// xlsxHeaderRow is the first non-blank row of the first sheet, read with
// the row iterator (never the whole sheet) and the parser's unzip limit.
func xlsxHeaderRow(r io.ReaderAt, size int64) ([]string, error) {
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size), xlsxOpenOptions())
	if err != nil {
		return nil, fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("open xlsx: no worksheets")
	}
	rows, err := f.Rows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read xlsx: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		rec, err := rows.Columns()
		if err != nil {
			return nil, fmt.Errorf("read xlsx: %w", err)
		}
		if !isBlankRow(rec) {
			return rec, nil
		}
	}
	if err := rows.Error(); err != nil {
		return nil, fmt.Errorf("read xlsx: %w", err)
	}
	return nil, fmt.Errorf("read xlsx: no header row")
}

// HeadersClaim reports whether headers holds every name in want, compared
// trimmed and case-insensitively. An empty want claims nothing.
func HeadersClaim(headers, want []string) bool {
	if len(want) == 0 {
		return false
	}
	have := make(map[string]bool, len(headers))
	for _, h := range headers {
		have[foldHeader(h)] = true
	}
	for _, w := range want {
		if !have[foldHeader(w)] {
			return false
		}
	}
	return true
}

// foldHeader trims, collapses inner whitespace and lower-cases a column name.
func foldHeader(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
