package catalog

// Row-level out-of-band responses for catalog mutations (ut-docs#1363).
//
// Before this, every catalog mutation re-rendered and OOB-swapped the
// ENTIRE unbounded items table (4 whole-catalog queries + a full-table
// render per mutation — attaching one barcode paid all of it). Now each
// mutation's response carries only fragments for the ONE row it affects:
//
//   - an in-place row replacement  (hx-swap-oob="true" on #catalog-row-<id>)
//   - or a row insert              (hx-swap-oob="beforeend:#catalog-tbody")
//   - or a row removal             (hx-swap-oob="delete" on #catalog-row-<id>)
//   - plus the #catalog-empty-row placeholder when it must (dis)appear.
//
// The client requests all of these with a swap:none primary target, so the
// OOB fragments are the entire UI update. Templates live in
// web/ui/partials/catalog_row.html.
//
// Known accepted limitation: the empty-state placeholder's appear/disappear
// decision is made from THIS request's own before/after server state, not
// from what a given browser tab currently shows. Two tills viewing /catalog
// concurrently can drift out of sync on it (e.g. tab A loaded while the
// catalog was empty; tab B creates the first item; tab A then creates a
// second — A's own request sees another active item already exists, so it
// never clears its stale placeholder). Cosmetic, self-heals on reload; the
// old whole-table re-render self-healed this as a side effect, this
// protocol doesn't.
//
// The thumbnail column (ut-docs#1842) is NOT given the same treatment,
// deliberately — review found that a per-ROW fragment genuinely cannot
// carry the fix: adding/removing a <td> only ever touches the one row a
// mutation is about, but the column's <th> lives in a <thead> no row
// fragment can reach, and every OTHER row in the table would silently
// disagree with it. So a mutation that flips whether ANY active item has
// a thumbnail — the first photo ever added to an all-text catalog, or
// deactivating the last imaged item — re-renders the WHOLE table as one
// OOB swap instead of a row fragment; see writeWholeTableOOB below and
// its caller in handlers.go, which snapshots the answer BEFORE the
// mutation runs so it has something to compare the fresh, post-mutation
// answer against.

import (
	"bytes"
	"errors"
	"html/template"
	"io"
	"maps"
	"net/http"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// catalogRowVM is one items-table row's view model (catalog_row.html's
// dot). OOB false renders a plain row (the initial /catalog table, and
// the insert fragment whose wrapper tbody carries the swap directive);
// OOB true adds hx-swap-oob="true" for an in-place replacement of the
// existing #catalog-row-<id> element.
type catalogRowVM struct {
	Item     catalogtypes.ItemInput
	Barcodes []string
	Variants []data.VariantView
	OOB      bool
	// ImageURL is this item's real thumbnail path (item_images,
	// role=thumbnail), resolved by the caller — never guessed from the
	// item id (ut-docs#1842). Empty when the item has no thumbnail.
	ImageURL string
	// ShowThumbColumn is a page-level decision (HasAnyThumbnail: does ANY
	// currently-active item have a thumbnail), carried on every row so
	// the row-level OOB templates need no separate argument. writeCatalogRowOOB
	// only ever emits a row fragment carrying this when it's UNCHANGED
	// from before the mutation — see that function's transition check —
	// so by the time a row fragment reaches the client, ShowThumbColumn
	// always agrees with the <thead> that's already in the DOM.
	ShowThumbColumn bool
}

// buildCatalogRows assembles the initial table's row view models from the
// whole-catalog listing maps — the one remaining place a full-list render
// is correct (the /catalog page's first paint). showThumbColumn is the
// same page-level HasAnyThumbnail decision the caller uses for the
// table's own <th>, threaded onto every row so catalog_row.html renders
// the header and every cell off one consistent fact.
func buildCatalogRows(items []catalogtypes.ItemInput, barcodes map[string][]string, variants map[string][]data.VariantView, thumbnails map[string]string, showThumbColumn bool) []catalogRowVM {
	rows := make([]catalogRowVM, 0, len(items))
	for _, itm := range items {
		rows = append(rows, catalogRowVM{
			Item: itm, Barcodes: barcodes[itm.ID], Variants: variants[itm.ID],
			ImageURL: thumbnails[itm.ID], ShowThumbColumn: showThumbColumn,
		})
	}
	return rows
}

// renderRowFragment renders one named template from catalog_row.html into
// w, buffered so a render failure can never truncate-corrupt a response
// that already carries earlier fragments.
func renderRowFragment(w io.Writer, r *http.Request, funcs template.FuncMap, name string, dot any) {
	var buf bytes.Buffer
	bw := newBufResponseWriter(&buf)
	httpx.RenderWith(files(
		filepath.Join("web", "ui", "partials", "catalog_row.html"),
	), funcs)(name, dot)(bw, r)
	_, _ = w.Write(buf.Bytes())
}

// writeCatalogRowOOB writes the OOB fragment(s) needed after a mutation on
// itemID: an updated/inserted row, and — only when the empty-state
// placeholder needs to appear or disappear — that row too. Callers that
// also render another primary response body (the variants panel) write
// this as an additional fragment alongside their own content; callers with
// no other primary content write ONLY this as the entire response body.
//
// insert selects the append mode for a newly created item; everything else
// is an in-place update. An item that is missing or inactive is answered
// with a row DELETE fragment instead (deactivation is how rows leave the
// table), plus the empty-state placeholder append when no active item
// remains.
//
// NOTE on when a fragment is emitted at all: this is about matching each
// fragment to DOM reality, not dodging an htmx error — an OOB swap whose
// target selector matches nothing (oobSwap in the vendored htmx 1.9.12)
// takes its "no target" branch off `document.querySelectorAll(...)` being
// falsy, which it never is (an empty NodeList is truthy; only an invalid
// *selector* would throw), so that branch is dead code and an
// over-emitted delete is a silent no-op in this htmx version — verified
// live (double-deactivate: the console stays empty). The guards below
// exist anyway because "only emit what's true" is simpler to reason about
// than "emit unconditionally and rely on htmx to swallow the mismatch":
// the empty-state placeholder's append/delete are emitted only when the
// server can tell the placeholder is/isn't actually showing (no other
// active item exists / did exist), which is also just correct regardless
// of htmx's error behavior.
//
// hadThumbColumn is the caller's pre-mutation snapshot of HasAnyThumbnail
// (ut-docs#1842 review F1/F2) — captured before the mutation ran, so it's
// comparable against the fresh post-mutation answer computed below. When
// they disagree, no row fragment is emitted at all: this function instead
// delegates to writeWholeTableOOB, which re-renders the entire table
// (correcting the <thead> and every sibling row, not just itemID's own)
// as a single OOB swap. That's deliberately the ONLY path that can change
// whether the thumbnail column exists — every other response in this file
// keeps ShowThumbColumn equal to what the caller already had, so a plain
// row fragment can never disagree with the <thead> already in the DOM.
func writeCatalogRowOOB(w io.Writer, r *http.Request, repo *data.CatalogRepo, funcs template.FuncMap, itemID string, insert bool, hadThumbColumn bool) error {
	ctx := r.Context()
	itm, ok, err := repo.GetItem(ctx, itemID)
	if err != nil {
		return err
	}
	showThumbColumn, err := repo.HasAnyThumbnail(ctx)
	if err != nil {
		return err
	}
	if showThumbColumn != hadThumbColumn {
		return writeWholeTableOOB(w, r, repo, funcs)
	}
	if !ok || !itm.IsActive {
		if insert {
			// A brand-new item created inactive was never in the table:
			// nothing to delete, nothing to update.
			return nil
		}
		renderRowFragment(w, r, funcs, "catalog_row_delete", itemID)
		hasActive, err := repo.HasActiveItems(ctx)
		if err != nil {
			return err
		}
		if !hasActive {
			renderRowFragment(w, r, funcs, "catalog_empty_row_append_oob", emptyRowColspan(showThumbColumn))
		}
		return nil
	}
	barcodes, err := repo.ItemBarcodesFor(ctx, itemID)
	if err != nil {
		return err
	}
	variants, err := repo.ItemVariantsFor(ctx, itemID)
	if err != nil {
		return err
	}
	thumbURL, err := repo.ItemThumbnailFor(ctx, itemID)
	if err != nil {
		return err
	}
	// A single-row render only ever needs ONE tax code's name — unlike the
	// full table (ListAllTaxCodes, a whole-table read), resolve just this
	// item's via the existing single-row lookup (ut-docs#1363 review: this
	// was the last of the original finding's 4 whole-catalog queries still
	// running on every mutation). Same semantics as taxCodeNameFunc: no tax
	// code (nil/empty TaxCodeID) or an unresolvable one both render "".
	taxName := ""
	if itm.TaxCodeID != nil && *itm.TaxCodeID != "" {
		if tc, err := repo.GetTaxCode(ctx, *itm.TaxCodeID); err == nil {
			taxName = tc.Name
		} else if !errors.Is(err, data.ErrTaxCodeNotFound) {
			return err
		}
	}
	// Same single-row rationale as taxName above (ut-docs#1430): the items
	// table's category column needs just this one item's category name, not
	// a whole-table lookup read.
	categoryName := ""
	if itm.CategoryID != nil && *itm.CategoryID != "" {
		if l, err := repo.GetLookup(ctx, "categories", *itm.CategoryID); err == nil {
			categoryName = l.Name
		} else if !errors.Is(err, data.ErrLookupNotFound) {
			return err
		}
	}
	// Copy before adding taxCodeName/categoryName — funcs may be shared
	// with the caller's own panel render.
	rowFuncs := make(template.FuncMap, len(funcs)+2)
	maps.Copy(rowFuncs, funcs)
	rowFuncs["taxCodeName"] = func(*string) string { return taxName }
	rowFuncs["categoryName"] = func(*string) string { return categoryName }
	name := "catalog_row_update_oob"
	if insert {
		name = "catalog_row_insert_oob"
	}
	renderRowFragment(w, r, rowFuncs, name, catalogRowVM{
		Item: itm, Barcodes: barcodes, Variants: variants, OOB: !insert,
		ImageURL: thumbURL, ShowThumbColumn: showThumbColumn,
	})
	if insert {
		otherActive, err := repo.HasOtherActiveItems(ctx, itemID)
		if err != nil {
			return err
		}
		if !otherActive {
			// First active item: the placeholder row is showing — clear it.
			renderRowFragment(w, r, rowFuncs, "catalog_empty_row_delete", nil)
		}
	}
	return nil
}

// catalogBaseCols is the items table's column count with the thumbnail
// column removed (name, sku, price, unit, tax, category, actions) — the
// empty-state placeholder's colspan must track whichever count is
// actually true this request (ut-docs#1842), or it silently mis-spans.
const catalogBaseCols = 7

// emptyRowColspan is catalog_empty_row's dot: the placeholder row's
// colspan, matching whether the thumbnail column currently exists.
func emptyRowColspan(showThumbColumn bool) int {
	if showThumbColumn {
		return catalogBaseCols + 1
	}
	return catalogBaseCols
}

// writeWholeTableOOB re-renders the ENTIRE items table as one
// hx-swap-oob="true" swap of #catalog-table (ut-docs#1842 review F1/F2) —
// the rare fallback writeCatalogRowOOB reaches for only when a mutation
// flips whether the thumbnail column exists at all, which a single row's
// fragment cannot express (the <thead>'s <th> and every OTHER row are
// out of reach from there). Same data-gathering shape as the /catalog GET
// handler's initial render (handlers.go) — kept independent rather than
// factored together, since the GET handler also needs page chrome (nav,
// menu snapshot, theme) this fragment-only response has no use for.
func writeWholeTableOOB(w io.Writer, r *http.Request, repo *data.CatalogRepo, funcs template.FuncMap) error {
	ctx := r.Context()
	items, err := repo.ListItems(ctx)
	if err != nil {
		return err
	}
	cats, brands, err := listLookups(ctx, repo)
	if err != nil {
		return err
	}
	taxCodes, err := repo.ListAllTaxCodes(ctx)
	if err != nil {
		return err
	}
	barcodes, err := repo.ItemBarcodes(ctx)
	if err != nil {
		return err
	}
	variants, err := repo.ItemVariants(ctx)
	if err != nil {
		return err
	}
	thumbnails, err := repo.ItemThumbnails(ctx)
	if err != nil {
		return err
	}
	hasThumbnails := false
	for _, itm := range items {
		if thumbnails[itm.ID] != "" {
			hasThumbnails = true
			break
		}
	}
	// Copy before adding the whole-table lookups — funcs may be shared
	// with the caller's own panel render, same reasoning as rowFuncs above.
	tableFuncs := make(template.FuncMap, len(funcs)+3)
	maps.Copy(tableFuncs, funcs)
	tableFuncs["taxCodeName"] = taxCodeNameFunc(taxCodes)
	tableFuncs["categoryName"] = lookupNameFunc(cats)
	tableFuncs["brandName"] = lookupNameFunc(brands)
	var buf bytes.Buffer
	bw := newBufResponseWriter(&buf)
	httpx.RenderWith(files(
		filepath.Join("web", "ui", "partials", "catalog_table.html"),
		filepath.Join("web", "ui", "partials", "catalog_row.html"),
	), tableFuncs)("catalog_table", map[string]any{
		"Rows":          buildCatalogRows(items, barcodes, variants, thumbnails, hasThumbnails),
		"HasThumbnails": hasThumbnails,
		"EmptyColspan":  emptyRowColspan(hasThumbnails),
		"OOBSwap":       true,
	})(bw, r)
	_, err = w.Write(buf.Bytes())
	return err
}
