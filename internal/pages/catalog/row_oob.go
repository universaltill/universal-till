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
}

// buildCatalogRows assembles the initial table's row view models from the
// whole-catalog listing maps — the one remaining place a full-list render
// is correct (the /catalog page's first paint).
func buildCatalogRows(items []catalogtypes.ItemInput, barcodes map[string][]string, variants map[string][]data.VariantView) []catalogRowVM {
	rows := make([]catalogRowVM, 0, len(items))
	for _, itm := range items {
		rows = append(rows, catalogRowVM{Item: itm, Barcodes: barcodes[itm.ID], Variants: variants[itm.ID]})
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
func writeCatalogRowOOB(w io.Writer, r *http.Request, repo *data.CatalogRepo, funcs template.FuncMap, itemID string, insert bool) error {
	ctx := r.Context()
	itm, ok, err := repo.GetItem(ctx, itemID)
	if err != nil {
		return err
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
			renderRowFragment(w, r, funcs, "catalog_empty_row_append_oob", nil)
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
	// Copy before adding taxCodeName — funcs may be shared with the
	// caller's own panel render.
	rowFuncs := make(template.FuncMap, len(funcs)+1)
	maps.Copy(rowFuncs, funcs)
	rowFuncs["taxCodeName"] = func(*string) string { return taxName }
	name := "catalog_row_update_oob"
	if insert {
		name = "catalog_row_insert_oob"
	}
	renderRowFragment(w, r, rowFuncs, name, catalogRowVM{
		Item: itm, Barcodes: barcodes, Variants: variants, OOB: !insert,
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
