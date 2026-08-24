package common

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// FriendlyBarcodeConflict turns a barcode-attach failure into an operator-
// facing, translated message — shared by the manual catalog form, the
// catalog import, and cloud directive processing (ut-docs#303). A
// *data.BarcodeConflictError names the conflicting item/variant by its
// name (falling back to its scannable code) instead of the raw internal
// ID that used to leak onto the screen; any other error becomes a generic
// translated message rather than raw Go/SQL error text. The original
// error (with the internal ID, if any) is always logged, so the detail
// isn't lost — just kept off the operator's screen.
func FriendlyBarcodeConflict(ctx context.Context, repo *data.CatalogRepo, locale string, err error) string {
	if errors.Is(err, data.ErrBarcodeNoSymbologyMatch) {
		// ADR-0059 §3's named rejection: the shop has disabled the default
		// catch-alls and this code matches none of what's left enabled. The
		// full detail (which code, which ids) is in the wrapped error, kept
		// on the log line rather than the operator's screen (same split as
		// the conflict-unknown case below).
		log.Printf("[catalog] barcode attach failed: %v", err)
		return httpx.T(locale, "catalog.error.barcode_no_symbology_match")
	}
	var conflict *data.BarcodeConflictError
	if !errors.As(err, &conflict) {
		log.Printf("[catalog] barcode attach failed: %v", err)
		return httpx.T(locale, "catalog.error.barcode_attach_failed")
	}
	label, lerr, ok := "", error(nil), false
	if conflict.TargetType == "variant" {
		var l data.VariantLabel
		l, ok, lerr = repo.GetVariantLabel(ctx, conflict.TargetID)
		if lerr == nil && ok {
			label = l.Name
			if label == "" {
				label = l.Code
			}
		}
	} else {
		var l data.ItemLabel
		l, ok, lerr = repo.GetItemLabel(ctx, conflict.TargetID)
		if lerr == nil && ok {
			label = l.Name
			if label == "" {
				label = l.Code
			}
		}
	}
	if label == "" {
		// A real lookup error is worth its own line — distinct from the
		// "row is just gone" (ok=false, lerr=nil) case — since it means
		// this degraded to the generic message because of a DB problem,
		// not a stale reference.
		log.Printf("[catalog] barcode conflict: %s %s could not be resolved to a name (found=%v, err=%v)",
			conflict.TargetType, conflict.TargetID, ok, lerr)
		return httpx.T(locale, "catalog.error.barcode_conflict_unknown")
	}
	return fmt.Sprintf(httpx.T(locale, "catalog.error.barcode_conflict"), label)
}
