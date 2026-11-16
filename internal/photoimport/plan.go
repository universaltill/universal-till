package photoimport

import (
	"context"
	"encoding/csv"
	"errors"
	"image"
	"io"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/imaging"
)

// MaxTotalBytes caps the bytes read in one run (all images together).
const MaxTotalBytes = 1 << 30

// Outcome codes of one row. StatusAdd/StatusReplace/StatusSkipHasPhoto are
// not failures; every other code is and lands in the failed-rows CSV.
const (
	StatusAdd          = "add"
	StatusReplace      = "replace"
	StatusSkipHasPhoto = "skipped_has_photo"
	StatusUnknownSKU   = "unknown_sku"
	StatusAmbiguousSKU = "ambiguous_sku"
	StatusDuplicateSKU = "duplicate_sku"
	StatusVariantSKU   = "variant_sku"
	StatusNoSource     = "no_source"
	StatusOutside      = "outside_folder"
	StatusNotFound     = "not_found"
	StatusNotRegular   = "not_regular"
	StatusTooBig       = "too_big"
	StatusNotImage     = "not_image"
	StatusImageTooBig  = "image_too_large"
	StatusFetchFailed  = "fetch_failed"
	StatusBlockedURL   = "blocked_url"
	StatusStoreFailed  = "store_failed"
	StatusTotalLimit   = "total_limit"
)

// IsFailure reports whether a status is a refused row.
func IsFailure(status string) bool {
	switch status {
	case StatusAdd, StatusReplace, StatusSkipHasPhoto:
		return false
	}
	return true
}

// Catalog is what the plan needs to know about existing items.
type Catalog interface {
	// ItemsBySKU returns the ids of items carrying sku (case-insensitive);
	// variantOnly is true when no item matches but a variant does.
	ItemsBySKU(ctx context.Context, sku string) (ids []string, variantOnly bool, err error)
	// HasPhoto reports whether the item already has a real photo (a library
	// tile or icon does not count).
	HasPhoto(ctx context.Context, itemID string) (bool, error)
}

// URLFetcher downloads one https image.
type URLFetcher interface {
	Fetch(ctx context.Context, url string, max int64) ([]byte, error)
}

// Options configures one run. Preview and apply differ only in Commit.
type Options struct {
	Folder  string // chosen photos folder; "" = file paths refused
	Zip     *Zip   // optional uploaded archive
	Replace bool   // replace an existing photo (default off)
	Commit  bool   // false = preview: nothing is stored
	Catalog Catalog
	Fetcher URLFetcher // nil = URL rows refused
	// Store writes a prepared photo for an item (commit only).
	Store func(ctx context.Context, itemID string, img image.Image) error
}

// RowResult is the outcome of one CSV row.
type RowResult struct {
	Line   int
	SKU    string
	Status string
}

// Report is a whole run.
type Report struct {
	Committed bool
	Rows      []RowResult
	Counts    map[string]int
}

// RowsFromZip builds the rows of a ZIP-only run (file name = SKU).
func RowsFromZip(z *Zip) []Row {
	stems := z.Stems()
	rows := make([]Row, 0, len(stems))
	for i, s := range stems {
		rows = append(rows, Row{Line: i + 1, SKU: s})
	}
	return rows
}

// Run evaluates every row once, one image in memory at a time. With
// Commit it also stores the photos; the decisions are the same code either
// way, so a preview is exactly what the apply will do (barring changes in
// the files or catalogue between the two).
func Run(ctx context.Context, rows []Row, o Options) (*Report, error) {
	rep := &Report{Committed: o.Commit, Counts: map[string]int{}}
	seen := map[string]int{}
	for _, r := range rows {
		seen[strings.ToLower(r.SKU)]++
	}
	var total int64
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		st, err := processRow(ctx, r, seen[strings.ToLower(r.SKU)] > 1, &total, o)
		if err != nil {
			return rep, err
		}
		rep.Rows = append(rep.Rows, RowResult{Line: r.Line, SKU: r.SKU, Status: st})
		rep.Counts[st]++
	}
	return rep, nil
}

func processRow(ctx context.Context, r Row, dup bool, total *int64, o Options) (string, error) {
	if r.SKU == "" {
		return StatusUnknownSKU, nil
	}
	if dup {
		return StatusDuplicateSKU, nil
	}
	ids, variantOnly, err := o.Catalog.ItemsBySKU(ctx, r.SKU)
	if err != nil {
		return "", err
	}
	switch {
	case len(ids) > 1:
		return StatusAmbiguousSKU, nil
	case len(ids) == 0 && variantOnly:
		return StatusVariantSKU, nil
	case len(ids) == 0:
		return StatusUnknownSKU, nil
	}
	itemID := ids[0]
	has, err := o.Catalog.HasPhoto(ctx, itemID)
	if err != nil {
		return "", err
	}
	if has && !o.Replace {
		return StatusSkipHasPhoto, nil
	}
	if *total >= MaxTotalBytes {
		return StatusTotalLimit, nil
	}
	raw, fail := load(ctx, r, o)
	if fail != "" {
		return fail, nil
	}
	*total += int64(len(raw))
	img, err := imaging.PrepareThumb(raw)
	if err != nil {
		if errors.Is(err, imaging.ErrTooManyPixels) {
			return StatusImageTooBig, nil
		}
		return StatusNotImage, nil
	}
	status := StatusAdd
	if has {
		status = StatusReplace
	}
	if o.Commit {
		if err := o.Store(ctx, itemID, img); err != nil {
			return StatusStoreFailed, nil
		}
	}
	return status, nil
}

// load returns the row's image bytes or a failure status.
func load(ctx context.Context, r Row, o Options) ([]byte, string) {
	ref := r.Ref
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		if o.Fetcher == nil {
			return nil, StatusBlockedURL
		}
		b, err := o.Fetcher.Fetch(ctx, ref, MaxImageBytes)
		return b, mapErr(err)
	}
	if o.Zip != nil {
		f, amb := o.Zip.Find(r.SKU, ref)
		if amb {
			return nil, StatusDuplicateSKU
		}
		if f != nil {
			b, err := Read(f, MaxImageBytes)
			return b, mapErr(err)
		}
	}
	if ref == "" {
		if o.Zip == nil {
			return nil, StatusNoSource
		}
		return nil, StatusNotFound
	}
	if o.Folder == "" {
		if o.Zip != nil {
			return nil, StatusNotFound // a source was given; the file is not in it
		}
		return nil, StatusNoSource
	}
	p, err := ResolveInFolder(o.Folder, ref)
	if err != nil {
		return nil, mapErr(err)
	}
	b, err := ReadRegular(p, MaxImageBytes)
	return b, mapErr(err)
}

func mapErr(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrOutside):
		return StatusOutside
	case errors.Is(err, ErrNotRegular):
		return StatusNotRegular
	case errors.Is(err, ErrTooBig):
		return StatusTooBig
	case errors.Is(err, ErrBlocked):
		return StatusBlockedURL
	case errors.Is(err, ErrFetch):
		return StatusFetchFailed
	}
	return StatusNotFound
}

// WriteFailedCSV writes the refused rows (line, sku, reason) as CSV. A SKU
// that could start a spreadsheet formula is prefixed so it is shown as text.
func (r *Report) WriteFailedCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"line", "sku", "reason"})
	for _, row := range r.Rows {
		if !IsFailure(row.Status) {
			continue
		}
		sku := row.SKU
		if sku != "" && strings.ContainsRune("=+-@\t\r", rune(sku[0])) {
			sku = "'" + sku
		}
		_ = cw.Write([]string{strconv.Itoa(row.Line), sku, row.Status})
	}
	cw.Flush()
	return cw.Error()
}
