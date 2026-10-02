package fiscal

// Customer-document gate (ADR-0124, ut-docs#3169): whether this till may
// produce a document the customer sees or receives — a printed or
// on-screen receipt, a reprint, a designer test receipt — in a market
// whose shipped data says a shadow till must issue none (a market that
// requires certified invoicing software, where the shop's existing
// certified system must be the only source of such documents).
//
// The market fact is DATA — country_settings.shadow_customer_documents and
// its compiled floor in data.builtinCountryDefaults — never a country-code
// test in this package (core neutrality, #2888). This package does not
// import internal/data: the caller hands in the compiled floor as a func
// and the stored row through CountryDocumentsReader.
//
// Deliberately a separate decision type from the tender gate's Decision,
// so the two can never be confused at a call site. And deliberately no
// settings reader: neither fiscal.system_of_record nor any
// fiscal.signing_device_* posture key can lift a forbidden market. Both
// are owner-settable, and neither proves a certified issuing route
// (ADR-0124 §2 "There is no lift"; lifting is #2956/#2958's ADR).

import (
	"context"
	"fmt"
	"strings"
)

// DocumentsDecision is CustomerDocuments' answer.
type DocumentsDecision int

const (
	// DocumentsAllowed: customer documents may be produced as normal.
	DocumentsAllowed DocumentsDecision = iota
	// DocumentsSuppressedShadow: the shop's market forbids customer
	// documents from a shadow till. Sales are still recorded in full; only
	// the documents are withheld.
	DocumentsSuppressedShadow
)

// ShadowDocumentsForbidden is the stored column value that suppresses
// customer documents. Anything else (including "allowed") does not.
const ShadowDocumentsForbidden = "forbidden"

// CountryDocumentsReader reads one country's stored
// shadow_customer_documents value. found=false means no row for the code.
// data.CountrySettingsRepo satisfies it.
type CountryDocumentsReader interface {
	ShadowCustomerDocuments(ctx context.Context, code string) (value string, found bool, err error)
}

// CustomerDocuments decides ADR-0124 §2, steps 1–4. No network and no
// plugin call, so it can never block or slow a sale.
//
//  1. The country is trimmed and upper-cased here: the stored store.country
//     is not normalised.
//  2. If the compiled builtin floor says forbidden, the answer is
//     DocumentsSuppressedShadow with no I/O at all, so it cannot fail open.
//  3. Otherwise the stored row decides: "forbidden" suppresses; any other
//     value, or no row, allows.
//  4. A read error there returns DocumentsAllowed plus the error, for the
//     caller to log: a market with a receipt OBLIGATION (ADR-0089 D3) must
//     never lose its receipt because a column it does not use could not be
//     read. A forbidden market was already decided at step 2.
func CustomerDocuments(ctx context.Context, country string, builtinForbidden func(code string) bool, countries CountryDocumentsReader) (DocumentsDecision, error) {
	code := strings.ToUpper(strings.TrimSpace(country))
	if builtinForbidden != nil && builtinForbidden(code) {
		return DocumentsSuppressedShadow, nil
	}
	if countries == nil || code == "" {
		return DocumentsAllowed, nil
	}
	v, found, err := countries.ShadowCustomerDocuments(ctx, code)
	if err != nil {
		return DocumentsAllowed, fmt.Errorf("read shadow_customer_documents for %s: %w", code, err)
	}
	if found && strings.TrimSpace(v) == ShadowDocumentsForbidden {
		return DocumentsSuppressedShadow, nil
	}
	return DocumentsAllowed, nil
}
