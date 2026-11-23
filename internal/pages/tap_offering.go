package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6455 G-6 (ADR-0125 section 5, ADR-0198 sections 3.4, 3.9, 6.10): a
// tap entry (a payment entry declaring tap: true, or a tap_to_pay reader row)
// is offered ONLY on the shop's main or standalone till, and only while the
// cashier has not declared the till offline (a tap needs the internet).
// Self-order kiosks, satellites and additional tills never offer one in v1.

// readerSourceJoined is the effective source of a tender on a joined (additional
// or satellite) till: a cashier's till like readerSourcePOS, but with no tap row.
const readerSourceJoined = "joined"

// effectiveReaderSource narrows a cashier source to "joined" on a till that is
// not the main or standalone one (ownTillRole is non-empty only for joined tills).
func effectiveReaderSource(ctx context.Context, d *common.Deps, source string) string {
	if source == readerSourcePOS && d.Settings != nil && ownTillRole(ctx, d) != "" {
		return readerSourceJoined
	}
	return source
}

// tapOfferedOn reports whether this source may be offered a tap entry.
func tapOfferedOn(source string) bool { return source == readerSourcePOS }

// withoutTapEntries drops every tap entry from methods (a list of tender
// methods for a surface that never offers one). A failed plugin-list read keeps
// the list: the tender gate (tapOfferGate) still refuses a tap leg there.
func withoutTapEntries(ctx context.Context, d *common.Deps, methods []data.PaymentMethod) []data.PaymentMethod {
	out := make([]data.PaymentMethod, 0, len(methods))
	for _, m := range methods {
		if _, isTap := tapEntry(ctx, d, m.ID); isTap {
			continue
		}
		out = append(out, m)
	}
	return out
}

// tapOfferGate refuses, before any reader, fiscal start or authorize, a tender
// leg on a tap entry from a surface that must not offer one, or while the sale
// is declared offline. Nothing is started or charged.
func tapOfferGate(ctx context.Context, d *common.Deps, source string, offline bool, methods []string) error {
	if tapOfferedOn(effectiveReaderSource(ctx, d, source)) && !offline {
		return nil
	}
	for _, m := range methods {
		// A failed plugin-list read refuses too: this surface must not offer a
		// tap, and it cannot prove the leg is not one (fail closed).
		if _, isTap, err := tapEntryErr(ctx, d, m); isTap || err != nil {
			return &tapStartFailedError{Method: m}
		}
	}
	return nil
}
