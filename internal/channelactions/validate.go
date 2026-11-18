package channelactions

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
)

// Validation of the values a staff member types for a ship or cancel action,
// before anything is queued. It mirrors reference/contracts/
// channel-order-action-v1.json and ut-cloud's internal/channelorder
// (writeback.go): the values go to a customer-facing sales channel, so the
// cloud validates them again and a till that queued a bad one would only see
// it refused later. The schema's tracking_url pattern uses lookaheads, which
// Go's RE2 cannot compile; like ut-cloud the till keeps the RE2 part and
// checks the lookaheads' cases (IP host, digits-and-dots host, localhost,
// userinfo) by hand. validate_test.go runs the schema's own patterns under
// node over the same vectors and fails if this code is ever looser.

// CancelReasons are the contract's cancel reasons, in display order.
var CancelReasons = []string{"customer_request", "out_of_stock", "fraud", "other"}

// ValidCancelReason reports whether s is one of CancelReasons.
func ValidCancelReason(s string) bool {
	for _, r := range CancelReasons {
		if s == r {
			return true
		}
	}
	return false
}

const (
	maxCarrierRunes = 64
	maxTrackingLen  = 64
	maxTrackingURL  = 512
)

var (
	trackingNumberRe = regexp.MustCompile(`^[A-Za-z0-9 ._/-]+$`)
	// The schema's tracking_url pattern (RFC 3986 path, query and fragment
	// after host[:port]) without its lookaheads.
	trackingURLRe = regexp.MustCompile(`^https://(?:[A-Za-z0-9-]+)(?:\.[A-Za-z0-9-]+)+(?::[0-9]+)?(?:/(?:[A-Za-z0-9._~!$&'()*+,;=:@/-]|%[0-9A-Fa-f]{2})*(?:\?(?:[A-Za-z0-9._~!$&'()*+,;=:@/?-]|%[0-9A-Fa-f]{2})*)?(?:#(?:[A-Za-z0-9._~!$&'()*+,;=:@/?-]|%[0-9A-Fa-f]{2})*)?)?$`)
)

// Printable reports whether s is valid UTF-8 with no control (C0, DEL, C1),
// soft hyphen, line/paragraph separator, bidi or zero-width format
// character: the contract's `text`.
func Printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r <= 0x1f, r >= 0x7f && r <= 0x9f, r == 0xad:
			return false
		case r >= 0x200b && r <= 0x200f, r >= 0x2028 && r <= 0x202e, r >= 0x2060 && r <= 0x2069, r == 0xfeff:
			return false
		case unicode.Is(unicode.Cf, r):
			return false
		}
	}
	return true
}

// ValidateShipment applies the contract's shipment rules. The caller trims
// surrounding spaces first. It returns the form field that failed
// ("carrier", "tracking_number" or "tracking_url") and the locale key of the
// message, or "", "" when the shipment is valid. Fields are checked in form
// order, so the first failure is the one shown.
func ValidateShipment(s data.ChannelShipment) (field, msgKey string) {
	switch {
	case s.Carrier == "":
		return "carrier", "channel_orders.error.carrier_required"
	case utf8.RuneCountInString(s.Carrier) > maxCarrierRunes || !Printable(s.Carrier):
		return "carrier", "channel_orders.error.carrier_invalid"
	}
	if s.TrackingNumber != "" && (len(s.TrackingNumber) > maxTrackingLen || !Printable(s.TrackingNumber) || !trackingNumberRe.MatchString(s.TrackingNumber)) {
		return "tracking_number", "channel_orders.error.tracking_number_invalid"
	}
	if s.TrackingURL != "" && !validTrackingURL(s.TrackingURL) {
		return "tracking_url", "channel_orders.error.tracking_url_invalid"
	}
	return "", ""
}

func validTrackingURL(u string) bool {
	if len(u) > maxTrackingURL || !trackingURLRe.MatchString(u) {
		return false
	}
	host := strings.TrimPrefix(u, "https://")
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// The schema keeps IP literals out; this also refuses a dotted name that
	// parses as an address.
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	// ... and digits-and-dots hosts (1.2.3).
	if strings.Trim(host, "0123456789.") == "" {
		return false
	}
	lower := strings.ToLower(host)
	return lower != "localhost" && !strings.HasSuffix(lower, ".localhost")
}
