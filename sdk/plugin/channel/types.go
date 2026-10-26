package channel

import "encoding/json"

// The wire types of the sales-channel contract v1. Money is an integer in
// minor units plus an ISO currency, tax is basis points, time is RFC 3339
// UTC. Every record carries external_id and external_version; cloud_id is
// ours, stored on the channel record where the channel allows it.

// Identity is what every exchanged record carries (contract section 2).
type Identity struct {
	ExternalID      string `json:"external_id"`
	ExternalVersion string `json:"external_version,omitempty"`
	CloudID         string `json:"cloud_id,omitempty"`
}

// Listing is a product with its variants (record kind "listing").
type Listing struct {
	Identity
	Title        string    `json:"title"`
	Description  string    `json:"description,omitempty"`
	Status       string    `json:"status,omitempty"` // active | draft | archived
	CategoryPath []string  `json:"category_path,omitempty"`
	Variants     []Variant `json:"variants,omitempty"`
	Images       []Image   `json:"images,omitempty"`
}

// Variant is one sellable variant of a Listing.
type Variant struct {
	ExternalID     string            `json:"external_id"`
	CloudID        string            `json:"cloud_id,omitempty"`
	SKU            string            `json:"sku,omitempty"`
	Barcodes       []string          `json:"barcodes,omitempty"`
	Options        map[string]string `json:"options,omitempty"`
	PriceMinor     int64             `json:"price_minor"`
	Currency       string            `json:"currency"`
	CompareAtMinor int64             `json:"compare_at_minor,omitempty"`
	TaxIncluded    bool              `json:"tax_included,omitempty"`
	Tracked        bool              `json:"tracked,omitempty"`
}

// Image is a product image reference.
type Image struct {
	ExternalID string `json:"external_id"`
	URL        string `json:"url"`
	Position   int    `json:"position,omitempty"`
}

// Category (record kind "category").
type Category struct {
	Identity
	Name string   `json:"name"`
	Path []string `json:"path,omitempty"`
}

// StockObserved reports the quantity the channel holds (record kind
// "stock_observed"). The channel's number is an observation; UT decides.
type StockObserved struct {
	Identity
	Variant   string `json:"variant"`
	Location  string `json:"location,omitempty"`
	Available int64  `json:"available"`
}

// Order (record kinds "order", "order_update" and "refund"; contract
// sections 2 and 9.1). It carries no customer name, address, email or phone:
// those are fetched live through AskContact and never stored. Money is
// integer minor units and tax is basis points. Every field except
// CloudID, PaymentState and Customer is required by the cloud intake, so
// none of them uses omitempty: a zero amount, a 0 tax_bp or a false
// tax_included must be on the wire, or the record is rejected
// (invalid_order, bad_amount, bad_tax_rate, ...). Identity is
// shared with the other kinds, so its ExternalVersion keeps omitempty here;
// an order must still set it (the cloud rejects a missing external_version).
type Order struct {
	Identity
	OrderNumber         string       `json:"order_number"`
	PlacedAt            string       `json:"placed_at"`
	Currency            string       `json:"currency"`
	Status              string       `json:"status"` // as the channel reports it; the cloud owns the canonical status
	PaymentState        string       `json:"payment_state,omitempty"`
	Lines               []OrderLine  `json:"lines"`
	ShippingMinor       int64        `json:"shipping_minor"`
	ShippingTaxBP       int64        `json:"shipping_tax_bp"`
	ShippingTaxIncluded bool         `json:"shipping_tax_included"`
	OrderDiscountMinor  int64        `json:"order_discount_minor"`
	TaxAddedMinor       int64        `json:"tax_added_minor"`
	RefundTotalMinor    int64        `json:"refund_total_minor"`
	TotalMinor          int64        `json:"total_minor"`
	Customer            *CustomerRef `json:"customer,omitempty"`
}

// OrderLine is one line of an Order. Name is the product title (the one
// `name` the personal-data gate lets through, section 9.3): keep it at most
// 250 bytes. DiscountLineMinor is the discount on the whole line, not per
// unit. Qty is the quantity ordered and is never lowered by a refund;
// RefundedQuantity and RestockedQuantity are cumulative, and
// RestockedQuantity never exceeds RefundedQuantity, which never exceeds Qty.
type OrderLine struct {
	ExternalID        string `json:"external_id"`
	VariantExternalID string `json:"variant_external_id,omitempty"`
	SKU               string `json:"sku"` // "" when the line is not linked to a SKU
	Name              string `json:"name"`
	Qty               int64  `json:"qty"`
	UnitPriceMinor    int64  `json:"unit_price_minor"`
	TaxBP             int64  `json:"tax_bp"`
	TaxIncluded       bool   `json:"tax_included"`
	DiscountLineMinor int64  `json:"discount_line_minor"`
	RefundedQuantity  int64  `json:"refunded_quantity"`
	RestockedQuantity int64  `json:"restocked_quantity"`
}

// CustomerRef is the only customer data an order carries.
type CustomerRef struct {
	ExternalID string `json:"external_id"`
	Country    string `json:"country,omitempty"`
}

// OrderUpdate (record kind "order_update") is the same full-order shape as
// Order, not a delta (contract 9.1): the kind only says why the plugin sends
// it. Send the whole current order.
type OrderUpdate = Order

// Refund (record kind "refund") is likewise the whole current order with the
// refund fields (RefundTotalMinor, each line's RefundedQuantity and
// RestockedQuantity) filled in (contract 9.1).
type Refund = Order

// Customer (record kind "customer"), only when customer sync is on.
type Customer struct {
	Identity
	Country string `json:"country,omitempty"`
}

// SubmitResult is the host's answer for one record of a Submit batch.
type SubmitResult struct {
	ExternalID string `json:"external_id"`
	Status     string `json:"status"` // ResultAccepted | ResultUnchanged | ResultRejected
	Reason     string `json:"reason,omitempty"`
}

// OutboxOp is one pending outbound operation. ID is its idempotency key:
// the guest passes it back to the channel and to OutboxAck.
type OutboxOp struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	ExternalID      string          `json:"external_id,omitempty"`
	ExternalVersion string          `json:"external_version,omitempty"`
	CloudID         string          `json:"cloud_id,omitempty"`
	Expect          *int64          `json:"expect,omitempty"` // stock.set: last known channel value
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// Ack acknowledges one OutboxOp. An Error with Retryable false dead-letters
// the operation.
type Ack struct {
	ExternalID      string `json:"external_id,omitempty"`
	ExternalVersion string `json:"external_version,omitempty"`
	// RefundedAmountMinor is, on the success of a `refund.create`, the refund
	// money (integer minor units) the channel moved for it, read back from the
	// channel (sales-channel-v1 §8.5, ut-docs#4904). The cloud counts it
	// instead of the operation's whole max_amount_minor when it works out the
	// next refund's ceiling. Optional, and never together with Error. Send it
	// only to a host that lists it: an older host refuses an ack object with
	// an unknown field (-4).
	RefundedAmountMinor *int64    `json:"refunded_amount_minor,omitempty"`
	Error               *AckError `json:"error,omitempty"`
}

// AckError is a failed operation.
type AckError struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// HTTPRequest is a request the host sends on the guest's behalf. URL must be
// https on the connection's own account host; a guest is never told its
// account, so it writes https://{account}/path (that exact prefix, followed by
// "/", "?", "#" or nothing) and the host fills in the verified account before
// its egress rules run. The host adds the credential header, so a guest never
// sets (and cannot read) it. CostHint is optional
// (for example a GraphQL query cost) and only feeds pacing. Throttle is
// optional too: the channel's cost bucket as the guest last observed it, so
// the host can space requests to what the channel will actually allow.
type HTTPRequest struct {
	Method   string            `json:"method,omitempty"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     []byte            `json:"-"`
	CostHint int               `json:"cost_hint,omitempty"`
	Throttle *Throttle         `json:"throttle,omitempty"`
}

// Throttle is a channel's cost bucket as the guest last observed it, for
// example Shopify's extensions.cost.throttleStatus (currentlyAvailable,
// maximumAvailable, restoreRate; round its floats to whole points). Report
// it once, on the next HTTPRequest after the response it was read from, and
// never replay it: each report resets the host's estimate as of now. The
// host spends CostHint against it and waits for the refill instead of
// sending into a THROTTLED answer. The host clamps every value, and ignores a
// report whose Maximum or RestoreRate is not positive. Absent means no report.
type Throttle struct {
	Available   int `json:"available"`    // points available now
	Maximum     int `json:"maximum"`      // size of the bucket
	RestoreRate int `json:"restore_rate"` // points restored per second
}

// HTTPResponse is the proxied answer. The body is capped by the host.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// ConnectTestResult answers AskConnectTest (read-only).
type ConnectTestResult struct {
	AccountName string   `json:"account_name"`
	Scopes      []string `json:"scopes,omitempty"`
	Locations   []string `json:"locations,omitempty"`
	Products    int64    `json:"products"`
	Orders      int64    `json:"orders"`
}

// OAuthCallbackInput is the AskOAuthCallback payload: parameters the host
// already verified. It never carries the client secret or a code.
type OAuthCallbackInput struct {
	Params map[string]string `json:"params"`
}

// OAuthCallbackResult is what the guest returns: the account and the
// granted scopes. The host checks the account against the manifest's pattern
// and suffix again.
type OAuthCallbackResult struct {
	Account string   `json:"account"`
	Scopes  []string `json:"scopes,omitempty"`
}

// WebhookInput is the AskWebhook payload: headers and the raw body of a
// webhook the HOST has already verified.
type WebhookInput struct {
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

// WebhookResult is what the guest returns. Event is one of the Event*
// constants. The host takes the tenant from its own verification, never from
// this output. OrderIDs is for EventCustomerRedact only: the external order
// ids the channel says belong to the erased customer, so the host can flag
// orders that carry no customer id (ut-docs#4929).
type WebhookResult struct {
	Account     string   `json:"account"`
	DedupeID    string   `json:"dedupe_id"`
	Event       string   `json:"event"`
	ResourceIDs []string `json:"resource_ids,omitempty"`
	OrderIDs    []string `json:"order_ids,omitempty"`
	// OrderIDsTruncated is for EventCustomerRedact only: the channel named
	// more than the host's 10,000 order ids; OrderIDs holds the first 10,000
	// distinct ones (ut-docs#5002).
	OrderIDsTruncated bool `json:"order_ids_truncated,omitempty"`
}

// ContactInput / ContactResult: AskContact returns an order's customer
// contact details for display only; the host never stores or logs them.
type ContactInput struct {
	OrderExternalID string `json:"order_external_id"`
}

// ContactResult is shown to the person who opened the order.
type ContactResult struct {
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
	Phone   string `json:"phone,omitempty"`
	Address string `json:"address,omitempty"`
}

// FetchInput names the resources AskFetch re-fetches.
type FetchInput struct {
	Event       string   `json:"event,omitempty"`
	ResourceIDs []string `json:"resource_ids"`
}
