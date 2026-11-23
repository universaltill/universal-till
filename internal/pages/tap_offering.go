package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6455 G-6 (ADR-0125 section 5, ADR-0198 sections 3.4, 3.9, 6.10): a
// tap entry (a payment entry declaring tap: true, or a tap_to_pay reader row)
// is offered ONLY on the shop's main or standalone till or on an additional
// till whose role is explicitly recorded (ADR-0198 Amendment 2026-11-23), and
// only while the cashier has not declared the till offline (a tap needs the
// internet). Self-order kiosks and satellites never offer one, nor does a
// joined till whose role is not explicitly known (fail closed).

// readerSourceJoined is the effective source of a tender on a joined (additional
// or satellite) till: a cashier's till like readerSourcePOS, but with no tap row.
const readerSourceJoined = "joined"

// readerSourceAdditional is the effective source of a tender on a joined till
// whose role is EXPLICITLY additional: it may take a tap on its own device.
const readerSourceAdditional = "additional"

// effectiveReaderSource narrows a cashier source to "joined" on a till that is
// not the main or standalone one (ownTillRole is non-empty only for joined
// tills), or to "additional" when that till's role is explicitly additional.
// Satellites and joined tills with an unrecorded role stay "joined".
func effectiveReaderSource(ctx context.Context, d *common.Deps, source string) string {
	if source == readerSourcePOS && d.Settings != nil && ownTillRole(ctx, d) != "" {
		if role, explicit := tapJoinedRole(ctx, d); explicit && role == data.TillRoleAdditional {
			return readerSourceAdditional
		}
		return readerSourceJoined
	}
	return source
}

// tapOfferedOn reports whether this source may be offered a tap entry.
func tapOfferedOn(source string) bool {
	return source == readerSourcePOS || source == readerSourceAdditional
}

// tapEntryCapable reports whether a tap entry can be offered on this (main,
// standalone or additional) till now (ADR-0198 2, conditions 2 and 4): this
// device's native Tap to Pay is ready for the entry's provider, or, on a main
// or standalone till only, an active paired phone reader is registered for the
// entry (that path needs no local bridge, ADR-0198 6; an additional till never
// uses a phone reader, O-3). A read error is "not capable" (fail closed).
func tapEntryCapable(ctx context.Context, d *common.Deps, method, provider string) bool {
	if provider != "" && devicepay.Capabilities()[provider].Ready {
		return true
	}
	if effectiveReaderSource(ctx, d, readerSourcePOS) == readerSourceAdditional {
		return false
	}
	rows, err := data.NewCardReaderRepo(d.Db).ListActive(ctx)
	if err != nil {
		return false
	}
	for _, c := range rows {
		if c.MethodKey == method && c.Kind == data.CardReaderKindPhone {
			return true
		}
	}
	return false
}

// withoutUnreadyTapEntries drops the tap entries this till cannot take now
// (see tapEntryCapable) from a main-till tender list.
func withoutUnreadyTapEntries(ctx context.Context, d *common.Deps, methods []data.PaymentMethod) []data.PaymentMethod {
	out := make([]data.PaymentMethod, 0, len(methods))
	for _, m := range methods {
		if provider, isTap := tapEntry(ctx, d, m.ID); isTap && !tapEntryCapable(ctx, d, m.ID, provider) {
			continue
		}
		out = append(out, m)
	}
	return out
}

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
		// ADR-0198 2 (ut-docs#6458): on the main till a tap entry also needs
		// a ready device (or a paired phone reader). A crafted request for a
		// tap entry that is not offered is refused before any authorize.
		for _, m := range methods {
			provider, isTap, err := tapEntryErr(ctx, d, m)
			if err != nil {
				return &tapStartFailedError{Method: m}
			}
			if isTap && !tapEntryCapable(ctx, d, m, provider) {
				return &tapStartFailedError{Method: m, NotReady: true}
			}
		}
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

// tapRefundMainOnly reports whether method is a tap entry on an additional
// till: such a till never sends a refund (the cloud refuses it, ADR-0198
// Amendment 2026-11-23); the card tap is refunded on the main till.
func tapRefundMainOnly(ctx context.Context, d *common.Deps, method string) bool {
	if d.Settings == nil || effectiveReaderSource(ctx, d, readerSourcePOS) != readerSourceAdditional {
		return false
	}
	_, isTap := tapEntry(ctx, d, method)
	return isTap
}
