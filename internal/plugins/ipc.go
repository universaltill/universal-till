package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// EventDispatchMode defines how events are delivered to plugins
type EventDispatchMode int

const (
	// NonBlocking: Events are delivered asynchronously. Plugin errors are logged and audited
	// but do not affect the core transaction. This is the default for most events.
	// Use for: sale.viewed, inventory.adjusted, report.generated
	NonBlocking EventDispatchMode = iota

	// Blocking: Events are delivered synchronously within the transaction. Plugin errors
	// trigger a rollback of the entire transaction to maintain DB integrity.
	// Use for: payment.authorize, sale.validate (pre-completion hooks)
	// Note: Blocking events must be explicitly configured; most events default to non-blocking.
	Blocking
)

// EventBus manages event distribution to plugins
type EventBus struct {
	db          *sql.DB
	mu          sync.RWMutex
	subscribers map[string][]EventSubscriber // event_type -> subscribers
	eventModes  map[string]EventDispatchMode // event_type -> dispatch mode
	generation  uint64                       // bumped whenever the subscriber set changes

	// dropWarnMu/dropWarnedAt throttle the "channel full" diagnostic (see
	// channelFullWarnInterval) — separate from mu because publish() holds
	// mu.RLock() across the whole dispatch loop and this must be safe to
	// touch from inside that critical section without a reentrant lock.
	dropWarnMu   sync.Mutex
	dropWarnedAt map[string]time.Time

	// pubAuditMu guards the plugin-published-event audit throttles (see
	// publishedAuditInterval, ut-docs#3887). Own mutex, never eb.mu:
	// EnqueuePublished holds eb.mu.RLock across its loop.
	pubAuditMu     sync.Mutex
	pubPublishedAt map[string]time.Time // publisher plugin ID -> last event_published row
	pubCoalesced   map[string]int       // publisher plugin ID -> publishes without a row since then
	pubDeniedAt    map[string]time.Time // subscriber ID + "\x00" + event type -> last denied row
	pubDeniedN     map[string]int       // same key -> denials without a row since then
	now            func() time.Time     // test clock for the throttles; nil = time.Now

	// lastSaleCompleted is the UnixNano of the latest PublishSaleCompleted
	// (0 = none): the wasm schedule ticker's sale-burst pause reads it
	// (ADR-0121 §8, ut-docs#3161).
	lastSaleCompleted atomic.Int64
}

// channelFullWarnInterval bounds how often publish() performs the "channel
// full" audit write + stdout warning for a given plugin. Without this, a
// publisher racing a subscriber whose channel never drains (a wedged plugin,
// or — as found investigating ut-docs#674 — a test deliberately hammering
// Publish with no backoff) drives an unbounded number of synchronous SQLite
// audit writes and raw stdout writes in a tight loop: one regression test
// alone produced ~16,700 of each in under 16 seconds, all serialized through
// the single audit-log DB connection and the shared stdout mutex. That is a
// genuine, unbounded resource amplifier found while investigating
// ut-docs#674's CI contention report — independent review could not
// reproduce that specific incident (a double `go test ./...` run, in
// several patterns, pre- and post-fix) to confirm this as ITS root cause,
// so treat this as a worthwhile hardening against a real amplifier this
// codebase has, not a confirmed fix for that incident. One second is a
// judgement call, not a precisely-derived number: short enough that an
// operator/log still sees a wedged plugin promptly, long enough that even a
// permanently-stuck plugin in a 24/7 till writes at most ~86,400 audit rows/
// day instead of ~864,000. Every occurrence still gets its first-ever audit/
// log entry immediately; only the redundant repeats within the window are
// coalesced (and the coalesced entry says so — see the "further drops...
// coalesced" wording below), so the anomaly is never silently invisible.
const channelFullWarnInterval = time.Second

// shouldWarnChannelFull reports whether enough time has passed since the
// last "channel full" diagnostic for pluginID to fire another one, and
// records this call as the most recent one if so.
func (eb *EventBus) shouldWarnChannelFull(pluginID string) bool {
	eb.dropWarnMu.Lock()
	defer eb.dropWarnMu.Unlock()
	if eb.dropWarnedAt == nil {
		eb.dropWarnedAt = make(map[string]time.Time)
	}
	now := time.Now()
	if last, ok := eb.dropWarnedAt[pluginID]; ok && now.Sub(last) < channelFullWarnInterval {
		return false
	}
	eb.dropWarnedAt[pluginID] = now
	return true
}

// publishedAuditInterval bounds the audit_log rows a plugin-published event
// stream (event_publish, ADR-0121 §3) can write. A plugin may publish 20
// events/s; before ut-docs#3887 each one wrote an event_published row plus an
// event_dispatch row per subscriber into the same audit_log that holds GoBD
// journal entries — ~1.7-3.5M rows/day from a single plugin. Now: no
// per-subscriber "enqueued" row, at most one event_published row per
// publishing plugin per interval (<= 1440/day) carrying a coalesced=N count of
// that plugin's earlier publishes (any type) that got no row, and at most one
// "denied" row per subscriber + event type per interval, likewise counted.
// A count is carried into the NEXT row for its key, so a trailing burst with
// no later publish (plugin quiet, disabled, till restarted) is not counted —
// an accepted gap: the first row of the burst is always written, so the
// stream stays visible. Core (hop-0) events are unaffected.
const publishedAuditInterval = time.Minute

func (eb *EventBus) clock() time.Time {
	if eb.now != nil {
		return eb.now()
	}
	return time.Now()
}

// shouldAuditPublished reports whether this publish by publisherID gets its
// own event_published row, and the number of this plugin's publishes (any
// type) since its previous row that did not — carried into this row; a
// trailing burst is never counted (see publishedAuditInterval).
func (eb *EventBus) shouldAuditPublished(publisherID string) (bool, int) {
	eb.pubAuditMu.Lock()
	defer eb.pubAuditMu.Unlock()
	if eb.pubPublishedAt == nil {
		eb.pubPublishedAt = make(map[string]time.Time)
		eb.pubCoalesced = make(map[string]int)
	}
	now := eb.clock()
	if last, ok := eb.pubPublishedAt[publisherID]; ok && now.Sub(last) < publishedAuditInterval {
		eb.pubCoalesced[publisherID]++
		return false, 0
	}
	eb.pubPublishedAt[publisherID] = now
	n := eb.pubCoalesced[publisherID]
	eb.pubCoalesced[publisherID] = 0
	return true, n
}

// shouldAuditPublishedDenied is the per (subscriber, event type) throttle for
// the "denied" row of a published event; the int is the denials since the
// previous row that got none, as shouldAuditPublished.
func (eb *EventBus) shouldAuditPublishedDenied(pluginID, eventType string) (bool, int) {
	eb.pubAuditMu.Lock()
	defer eb.pubAuditMu.Unlock()
	if eb.pubDeniedAt == nil {
		eb.pubDeniedAt = make(map[string]time.Time)
		eb.pubDeniedN = make(map[string]int)
	}
	key := pluginID + "\x00" + eventType
	now := eb.clock()
	if last, ok := eb.pubDeniedAt[key]; ok && now.Sub(last) < publishedAuditInterval {
		eb.pubDeniedN[key]++
		return false, 0
	}
	eb.pubDeniedAt[key] = now
	n := eb.pubDeniedN[key]
	eb.pubDeniedN[key] = 0
	return true, n
}

// SetDB rebinds the bus to a live database handle (see SharedBus).
func (eb *EventBus) SetDB(db *sql.DB) {
	eb.mu.Lock()
	eb.db = db
	eb.mu.Unlock()
}

// dbHandle returns the current database handle under the read lock.
func (eb *EventBus) dbHandle() *sql.DB {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return eb.db
}

// EventSubscriber represents a plugin subscribed to events
type EventSubscriber struct {
	PluginID   string
	EventTypes []string
	Channel    chan Event
	Handler    EventHandler
}

// Event represents a POS event
type Event struct {
	ID        string    `json:"event_id"`
	Type      string    `json:"event_type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   []byte    `json:"payload"`
	// Hop is the publish chain depth (ADR-0121 §3): 0 for an event core
	// raised, the handled event's hop + 1 for one a plugin raised through
	// event_publish. A publish while handling a hop-3 event is dropped.
	Hop int `json:"hop,omitempty"`
	// Scheduled marks a `schedules[]` tick the host raised for one plugin
	// (ADR-0121 §8, ut-docs#3161). A tick is ordinary work whatever its name
	// — a plugin may name one `<id>.foo.ask` — so it never takes a reserved
	// sale-path slot or an event-class deadline floor (isSalePathCall,
	// timeoutForEvent). Host-side only: never serialised.
	Scheduled bool `json:"-"`
}

// SaleCompletedEvent is the payload published on "sale.completed". It is the
// stable contract external-integration plugins (ERP/accounting connectors:
// SAP, Dynamics/LS Central, …) consume to mirror each sale into another
// system. Money is integer minor units; quantities are decimal (weighed goods).
type SaleCompletedEvent struct {
	SaleID    string `json:"sale_id"`
	ReceiptNo string `json:"receipt_no"`
	SaleType  string `json:"sale_type"` // "sale" | "return"
	// OrderType (ut-docs#1181, ADR-0073): the sale's derived summary —
	// "" (dine-in), "takeaway" or "mixed". Additive; omitted when dine-in.
	OrderType     string         `json:"order_type,omitempty"`
	Currency      string         `json:"currency"`
	SubtotalCents int64          `json:"subtotal_cents"`
	DiscountCents int64          `json:"discount_cents"`
	TaxCents      int64          `json:"tax_cents"`
	TotalCents    int64          `json:"total_cents"`
	CustomerID    string         `json:"customer_id"`
	RegisterID    string         `json:"register_id"`
	CashierID     string         `json:"cashier_id"`
	PaymentMethod string         `json:"payment_method"` // primary method (convenience)
	Payments      []SalePayment  `json:"payments"`
	LineItems     []SaleLineItem `json:"line_items"`
	CompletedAt   time.Time      `json:"completed_at"`
}

// SaleLineItem represents an item in a sale (ERP contract).
type SaleLineItem struct {
	ItemID         string  `json:"item_id"`
	VariantID      string  `json:"variant_id"`
	SKU            string  `json:"sku"`
	Name           string  `json:"name"`
	Quantity       float64 `json:"quantity"`
	UnitPriceCents int64   `json:"unit_price_cents"`
	DiscountCents  int64   `json:"discount_cents"`
	TaxRateBP      int     `json:"tax_rate_bp"`
	TaxCents       int64   `json:"tax_cents"`
	TotalCents     int64   `json:"total_cents"`
	// OrderType (ut-docs#1181, ADR-0073): this line's own mode, "" (dine-in,
	// omitted) or "takeaway" — never "mixed". Additive.
	OrderType string `json:"order_type,omitempty"`
}

// SalePayment is one tender applied to a sale (ERP contract).
type SalePayment struct {
	Method      string `json:"method"`
	AmountCents int64  `json:"amount_cents"`
	Reference   string `json:"reference"`
	// Card-present reconciliation fields (ut-docs#543) -- empty unless the
	// payment method supplied them. See data.CardPresentFields.
	MaskedPAN  string `json:"masked_pan,omitempty"`
	AuthCode   string `json:"auth_code,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`
}

// StockAdjustedEvent is the payload published on "stock.adjusted" whenever an
// item's on-hand quantity changes (a sale removes stock, a refund/return adds
// it back, plus manual adjustments and goods received). It is the stable
// contract external-integration plugins (ERP/inventory connectors — SAP,
// Dynamics/LS Central, … per ADR-0014) consume to keep external stock levels
// in sync. Quantities are decimal to support weighed goods: a negative
// delta_qty reduces stock, a positive one increases it.
type StockAdjustedEvent struct {
	ItemID     string    `json:"item_id"`
	VariantID  string    `json:"variant_id"`
	SKU        string    `json:"sku"`
	DeltaQty   float64   `json:"delta_qty"`         // signed change in on-hand units
	NewQty     float64   `json:"new_qty,omitempty"` // resulting on-hand, when readily available
	Reason     string    `json:"reason"`            // "sale" | "refund" | "adjustment" | "received"
	Location   string    `json:"location"`
	AdjustedAt time.Time `json:"adjusted_at"`
}

// CustomerErasedEvent is the payload published on "customer.erased"
// (ut-docs#3435, ADR-0121 §2's neutral-event seam) after a GDPR erasure:
// on the till where an operator erased the customer, and on every replica
// whose admin pull prunes that customer. Plugins that keep their own copy of
// a customer (loyalty, CRM, integration connectors) are expected to delete
// it on receipt — the host guarantees delivery to events:receive
// subscribers, not what a plugin's own code does with it. The id only:
// never the name or contact data, which are exactly what was erased.
type CustomerErasedEvent struct {
	CustomerID string `json:"customer_id"`
}

// EventHandler executes a blocking handler for an event; returning a non-nil
// error signals rollback/failure. The returned json.RawMessage is the
// handler's answer for "ask" style hooks (EventBus.Ask) where a plugin
// computes and returns a VALUE, e.g. a country-specific tax-rate override —
// nil when the hook is accept/reject only (e.g. payment authorization,
// which Publish's Blocking mode only ever inspects the error from).
type EventHandler func(ctx context.Context, event Event) (json.RawMessage, error)

// NewEventBus creates a new event bus
func NewEventBus(db *sql.DB) *EventBus {
	eb := &EventBus{
		db:          db,
		subscribers: make(map[string][]EventSubscriber),
		eventModes:  make(map[string]EventDispatchMode),
	}

	// Configure default event modes
	// Non-blocking (default): Most events should not block the core transaction
	eb.eventModes["sale.completed"] = NonBlocking
	eb.eventModes["stock.adjusted"] = NonBlocking
	eb.eventModes["customer.erased"] = NonBlocking
	eb.eventModes["sale.viewed"] = NonBlocking
	eb.eventModes["inventory.adjusted"] = NonBlocking
	eb.eventModes["report.generated"] = NonBlocking
	eb.eventModes["shift.opened"] = NonBlocking
	eb.eventModes["shift.closed"] = NonBlocking

	// Blocking: Only critical validation/authorization events should block
	// These are not yet implemented but reserved for future use:
	// eb.eventModes["payment.authorize"] = Blocking
	// eb.eventModes["sale.validate"] = Blocking

	return eb
}

// ResetSubscribers clears all in-memory subscriptions and closes their
// channels so drainer goroutines exit; the wasm runtime re-subscribes active
// plugins after every Manager.Reload. Safe to call concurrently with an
// in-flight publish (ut-docs#504): publish holds eb.mu.RLock for its whole
// dispatch loop and this method needs the exclusive Lock, so no channel a
// live publish might still send on can be closed here.
func (eb *EventBus) ResetSubscribers() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	closed := map[chan Event]bool{}
	for _, subs := range eb.subscribers {
		for _, sub := range subs {
			if sub.Channel != nil && !closed[sub.Channel] {
				closed[sub.Channel] = true
				close(sub.Channel)
			}
		}
	}
	eb.subscribers = make(map[string][]EventSubscriber)
	eb.generation++
}

// Generation identifies the current answer-relevant plugin state: it changes
// whenever a plugin (un)subscribes, and whenever BumpGeneration reports an
// out-of-band change. A caller caching answers from a blocking ".ask" hook
// (see pluginTaxRateAsker) drops its cache the moment the generation moves.
func (eb *EventBus) Generation() uint64 {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return eb.generation
}

// BumpGeneration invalidates cached ".ask" answers without touching the
// subscriber set. Call it after any mutation that can change what a
// subscribed plugin would answer for the same payload even though nothing
// (re)subscribed: a plugin_settings write (both shipped tax plugins read a
// setting via the settings_get host fn inside their ask handler) or a
// permission grant/revoke (Ask skips permission-denied subscribers).
// Mutations that go through Manager.Reload don't need it — ResetSubscribers
// already bumps.
func (eb *EventBus) BumpGeneration() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.generation++
}

// SetEventMode configures the dispatch mode for an event type
// This allows runtime configuration of blocking vs non-blocking behavior
func (eb *EventBus) SetEventMode(eventType string, mode EventDispatchMode) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.eventModes[eventType] = mode
}

// HasSubscribers reports whether any plugin is subscribed to an event —
// the tender path uses it to decide if a payment needs authorization.
func (eb *EventBus) HasSubscribers(eventType string) bool {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.subscribers[eventType]) > 0
}

// SubscriberIDs returns the plugin IDs subscribed to an event, in
// subscription (dispatch) order, de-duplicated, as a fresh slice (nil when
// nobody is subscribed). Non-exclusive ".ask" hooks use it to tell which
// plugins an AskFrom winner out-ranked (ut-docs#2955).
func (eb *EventBus) SubscriberIDs(eventType string) []string {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	var ids []string
	seen := make(map[string]bool, len(eb.subscribers[eventType]))
	for _, sub := range eb.subscribers[eventType] {
		if seen[sub.PluginID] {
			continue
		}
		seen[sub.PluginID] = true
		ids = append(ids, sub.PluginID)
	}
	return ids
}

// GetEventMode returns the dispatch mode for an event type
// Defaults to NonBlocking if not explicitly configured.
//
// No production caller: publish() deliberately inlines this lookup rather
// than calling it, because it already holds eb.mu and a recursive RLock
// can deadlock once a writer is pending (see the reentrancy note there,
// ut-docs#504). Kept as a declared test helper (ut-docs#1566): it is the
// read side of the live SetEventMode, and wasm_sync_test.go asserts through
// it that WasmRuntime.Sync marks ".ask" and ".refund" hooks Blocking (Sync
// applies the same rule to ".authorize", which no test reads back) — the
// only read path for that state short of poking eventModes.
func (eb *EventBus) GetEventMode(eventType string) EventDispatchMode {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	if mode, exists := eb.eventModes[eventType]; exists {
		return mode
	}
	return NonBlocking // Safe default
}

func (eb *EventBus) subscribe(ctx context.Context, pluginID string, eventTypes []string, handler EventHandler) (<-chan Event, error) {
	// Verify plugin has hooks for these events
	repo := data.NewPluginRepo(eb.dbHandle())
	for _, eventType := range eventTypes {
		hasHook, err := repo.HasActiveHook(ctx, pluginID, eventType)
		if err != nil {
			return nil, fmt.Errorf("check hooks: %w", err)
		}
		if !hasHook {
			return nil, fmt.Errorf("no active hook for event %s", eventType)
		}
	}

	eb.mu.Lock()
	defer eb.mu.Unlock()

	// Create event channel
	ch := make(chan Event, 100)

	subscriber := EventSubscriber{
		PluginID:   pluginID,
		EventTypes: eventTypes,
		Channel:    ch,
		Handler:    handler,
	}

	// Register for each event type
	for _, eventType := range eventTypes {
		eb.subscribers[eventType] = append(eb.subscribers[eventType], subscriber)
	}
	eb.generation++

	return ch, nil
}

// Subscribe registers a plugin to receive events on the returned channel
// only, with no blocking handler — the non-blocking half of the bus's two
// dispatch modes. No production caller: the WASM runtime (the only shipped
// plugin runtime, ADR-0001) always subscribes through SubscribeWithHandler
// (WasmRuntime.Sync), since every hook it registers may also be published
// Blocking. Kept as a declared test helper (ut-docs#1566): 26 call sites
// across ten `_test.go` files in this package (the 26th is ut-docs#2242's
// TestEventBus_Unsubscribe_MultiEventTypeSharedChannel) exercise the
// channel dispatch path, permission gating, ut-docs#791 payload redaction,
// the channel-full throttle and the shared-channel Unsubscribe contract
// through it, and it is a one-line wrapper over the same subscribe() the
// live path uses, so it can't drift from it.
func (eb *EventBus) Subscribe(ctx context.Context, pluginID string, eventTypes []string) (<-chan Event, error) {
	return eb.subscribe(ctx, pluginID, eventTypes, nil)
}

// SubscribeWithHandler registers a plugin to receive events using a blocking handler.
func (eb *EventBus) SubscribeWithHandler(ctx context.Context, pluginID string, eventTypes []string, handler EventHandler) (<-chan Event, error) {
	return eb.subscribe(ctx, pluginID, eventTypes, handler)
}

// Publish sends an event to all subscribed plugins honoring dispatch mode:
// - Non-blocking: enqueue to subscriber channels, audit denials/drops, never rollback.
// - Blocking: execute handlers synchronously, return error on failure to allow rollback.
func (eb *EventBus) Publish(ctx context.Context, eventType string, payload interface{}) (string, error) {
	id, _, err := eb.publish(ctx, eventType, payload)
	return id, err
}

// PublishAuthorize behaves exactly like Publish for a Blocking event — same
// accept/reject/permission-denial semantics, same rollback-on-error contract
// — but also returns the responding plugin's raw response instead of
// discarding it. Payment authorize callers use this to read back
// plugin-reported data (e.g. a reader-captured tip amount) alongside the
// approve/decline verdict. resp is nil when there's nothing to report (no
// subscriber, a non-blocking event, or a handler that answered with an
// empty body).
func (eb *EventBus) PublishAuthorize(ctx context.Context, eventType string, payload interface{}) (json.RawMessage, error) {
	_, resp, err := eb.publish(ctx, eventType, payload)
	return resp, err
}

// PublishAuthorizeWithID behaves exactly like PublishAuthorize but uses id
// as the event's id instead of minting a fresh one (ut-docs#1762). A
// payment authorize/refund gate that may be retried (the operator re-
// tapping Pay after a decline/timeout on the SAME tender attempt) needs
// the retry to carry the SAME id, since that id is what a fiscal-device or
// payment-gateway plugin forwards downstream as its own idempotency/
// request key — a fresh id per retry defeats the device's own de-dup and
// can double-charge/double-print. Every other caller keeps getting a
// fresh id via Publish/PublishAuthorize, unaffected by this.
func (eb *EventBus) PublishAuthorizeWithID(ctx context.Context, id, eventType string, payload interface{}) (json.RawMessage, error) {
	_, resp, err := eb.publishWithID(ctx, id, eventType, payload)
	return resp, err
}

// publish is Publish's real implementation; it additionally returns the
// last successful Blocking handler's raw response so PublishAuthorize can
// surface it without duplicating the dispatch/permission/audit logic above.
func (eb *EventBus) publish(ctx context.Context, eventType string, payload interface{}) (eventID string, resp json.RawMessage, err error) {
	return eb.publishWithID(ctx, uuid.NewString(), eventType, payload)
}

// publishWithID is publish's real implementation, taking the event id as a
// parameter instead of always minting one (ut-docs#1762) — see
// PublishAuthorizeWithID's doc comment for why a caller sometimes needs to
// supply its own.
func (eb *EventBus) publishWithID(ctx context.Context, id, eventType string, payload interface{}) (eventID string, resp json.RawMessage, err error) {
	// Encode payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshal payload: %w", err)
	}

	event := Event{
		ID:        id,
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payloadBytes,
	}

	// ut-docs#791: sale.completed's card-present reconciliation fields
	// (masked PAN, auth code, terminal/trace ID — ut-docs#543) are gated
	// on their own permission, same shape as the sales/stock export
	// ledgers (ut-docs#228, data_api.go) — a plugin missing the grant
	// still gets the event (line items, totals, non-card payment method),
	// just with those specific fields blanked, rather than being denied
	// the whole sale.completed subscription it may genuinely need for ERP
	// sync. redactedPayloadBytes is computed once, outside the per-
	// subscriber loop below, and reused for every subscriber that lacks
	// the grant.
	//
	// Deserializes payloadBytes back into a SaleCompletedEvent rather than
	// type-asserting the original payload interface{} — a type assertion
	// against the concrete SaleCompletedEvent value would fail open
	// (redactedPayloadBytes stays nil, every subscriber gets the full
	// payload unredacted) the moment a future caller passes
	// *SaleCompletedEvent instead of a value; json.Marshal produces
	// identical bytes for both, so round-tripping through JSON is
	// immune to that. json.Unmarshal here can only fail on malformed
	// JSON, which can't happen against bytes this function just produced
	// itself via json.Marshal above — but even so, failure still leaves
	// redactedPayloadBytes nil, which the dispatch loop below treats as
	// "no redaction configured for this event type" and sends the full
	// event.Payload to everyone. That's acceptable ONLY because
	// eventType == "sale.completed" is gated behind PublishSaleCompleted
	// being the sole production caller (ipc.go, always a value); it is
	// not a general-purpose fail-closed guarantee for arbitrary payloads.
	var redactedPayloadBytes []byte
	if eventType == "sale.completed" {
		var saleEvent SaleCompletedEvent
		if uerr := json.Unmarshal(payloadBytes, &saleEvent); uerr == nil {
			redactedPayloadBytes, err = json.Marshal(redactCardPresentFields(saleEvent))
			if err != nil {
				return "", nil, fmt.Errorf("marshal redacted payload: %w", err)
			}
		}
	}

	// Hold the read lock across the ENTIRE dispatch loop, not just the
	// subscriber snapshot (ut-docs#504). ResetSubscribers takes the
	// exclusive Lock before closing any subscriber channel, so holding the
	// RLock until every send/handler call below has finished makes the two
	// mutually exclusive — no channel this publish might still send on can
	// be closed mid-dispatch ("send on closed channel", the panic #503
	// fixed for the shutdown path only; this covers Manager.Reload —
	// plugin install/uninstall — too).
	//
	// Reentrancy: nothing inside this critical section may reacquire
	// eb.mu — a recursive RLock can deadlock once a writer is pending
	// (sync.RWMutex documented behavior). Hence the direct eb.db /
	// eb.eventModes field reads below (we already hold the lock) and the
	// ...WithDB audit variants instead of dbHandle()/GetEventMode()/
	// auditDispatch(), all of which self-RLock. Blocking
	// handlers (WasmRuntime.HandleEvent), CheckPermission, and the
	// payments:reconciliation data.PluginRepo.CheckPermission call
	// (ut-docs#791) are DB/wazero only and never touch EventBus.
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	db := eb.db
	subscribers, exists := eb.subscribers[eventType]

	if !exists || len(subscribers) == 0 {
		_ = eb.auditEventWithDB(ctx, db, event.ID, eventType, 0)
		return event.ID, nil, nil
	}

	// Inline GetEventMode (incl. its NonBlocking default) — see the
	// reentrancy note above.
	mode, haveMode := eb.eventModes[eventType]
	if !haveMode {
		mode = NonBlocking
	}
	dispatched := 0

	for _, sub := range subscribers {
		if err := CheckPermission(ctx, db, sub.PluginID, "events:receive"); err != nil {
			eb.auditDispatchWithDB(ctx, db, event.ID, eventType, sub.PluginID, "denied", err.Error())
			if mode == Blocking {
				return "", nil, fmt.Errorf("event %s denied for plugin %s: %w", eventType, sub.PluginID, err)
			}
			continue
		}

		// ut-docs#791: this subscriber's own copy of the event, redacted
		// unless it holds payments:reconciliation. Goes straight to
		// data.PluginRepo.CheckPermission (the same primitive
		// CheckPermissionGranted wraps) rather than CheckPermissionGranted
		// itself, deliberately skipping its audit-on-denial: for every
		// OTHER permission, a denial is an exceptional attempted access
		// worth a row in audit_log. Here it's the opposite — most
		// sale.completed subscribers (plain ERP/accounting connectors)
		// will never declare this permission at all, so "not granted" is
		// the expected, permanent steady state, checked on every single
		// sale for every such subscriber. Auditing it the normal way
		// would write one denial row per sale per ungranted subscriber
		// forever, into the same audit_log the GoBD-relevant journal
		// entries live in, drowning out genuinely exceptional denials —
		// unlike data_api.go's sales:read/inventory:read precedent, which
		// only runs once per on-demand export request, not once per sale.
		// A repo-layer error is still fail-closed (err != nil → granted
		// false → redact), just silent rather than audited — consistent
		// with a best-effort, non-blocking event publish never aborting
		// on this class of failure.
		subEvent := event
		if redactedPayloadBytes != nil {
			granted, _, permErr := data.NewPluginRepo(db).CheckPermission(ctx, sub.PluginID, "payments:reconciliation")
			if permErr != nil {
				logging.L().Warnf("payments:reconciliation check failed for plugin %s: %v", sub.PluginID, permErr)
			}
			if !granted {
				subEvent.Payload = redactedPayloadBytes
			}
		}

		switch mode {
		case Blocking:
			if sub.Handler == nil {
				msg := "blocking event requires handler"
				eb.auditDispatchWithDB(ctx, db, event.ID, eventType, sub.PluginID, "error", msg)
				return "", nil, fmt.Errorf("blocking event %s failed for plugin %s: %s", eventType, sub.PluginID, msg)
			}
			// Most Blocking callers only care about accept/reject (payment
			// authorization); PublishAuthorize is how a caller opts into
			// also reading the handler's raw response.
			//
			// Release eb.mu around the handler call itself (ut-docs#504
			// review finding, defense in depth alongside
			// WithCloseOnContextDone above): a Blocking handler can run
			// for its full timeout (or longer, if that enforcement ever
			// fails again) and, unlike the non-blocking case below, never
			// touches sub.Channel or anything else guarded by eb.mu — mode
			// is fixed for this whole publish() call (computed once from
			// eventType, not per-subscriber), so every OTHER subscriber
			// this loop visits is Blocking too and equally never sends on
			// a channel. So a concurrent ResetSubscribers/Subscribe/
			// SetEventMode/BumpGeneration racing this specific call can't
			// touch anything this call still needs. Re-locked immediately
			// after, before any return path below, so the function's
			// single deferred eb.mu.RUnlock() stays correct (exactly one
			// RUnlock for exactly one held lock at exit).
			eb.mu.RUnlock()
			handlerResp, err := sub.Handler(ctx, subEvent)
			eb.mu.RLock()
			if err != nil {
				eb.auditDispatchWithDB(ctx, db, event.ID, eventType, sub.PluginID, "error", err.Error())
				return "", nil, fmt.Errorf("blocking event %s failed for plugin %s: %w", eventType, sub.PluginID, err)
			}
			resp = handlerResp
			eb.auditDispatchWithDB(ctx, db, event.ID, eventType, sub.PluginID, "success", "")
			dispatched++
		default:
			if eb.enqueueWithDB(ctx, db, sub, subEvent, true) {
				dispatched++
			}
		}
	}

	if err := eb.auditEventWithDB(ctx, db, event.ID, eventType, dispatched); err != nil {
		logging.L().Warnf("failed to audit event: %v", err)
	}

	return event.ID, resp, nil
}

// EnqueuePublished delivers a plugin-published event (event_publish,
// ADR-0121 §3) the non-blocking way only: enqueued onto each subscriber's
// channel, with the same events:receive check and channel-full handling as
// Publish's non-blocking branch. It never calls a Blocking handler, whatever
// mode the event type has — a published event is never delivered inside
// anyone's call, and its drainer runs it as ordinary work.
//
// Audit is coalesced (ut-docs#3887, see publishedAuditInterval): no
// per-subscriber "enqueued" row, one event_published row per publisherID per
// interval, one "denied" row per subscriber + type per interval; "dropped"
// is unchanged.
func (eb *EventBus) EnqueuePublished(ctx context.Context, publisherID string, ev Event) {
	// RLock across the sends: ResetSubscribers can't close a channel
	// mid-dispatch (ut-docs#504). Nothing below re-takes eb.mu.
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	db := eb.db
	dispatched := 0
	for _, sub := range eb.subscribers[ev.Type] {
		if err := CheckPermission(ctx, db, sub.PluginID, "events:receive"); err != nil {
			if ok, n := eb.shouldAuditPublishedDenied(sub.PluginID, ev.Type); ok {
				reason := fmt.Sprintf("%s (coalesced=%d earlier denials; further denials within %s are coalesced into the next entry)", err.Error(), n, publishedAuditInterval)
				eb.auditDispatchWithDB(ctx, db, ev.ID, ev.Type, sub.PluginID, "denied", reason)
			}
			continue
		}
		if eb.enqueueWithDB(ctx, db, sub, ev, false) {
			dispatched++
		}
	}
	if ok, coalesced := eb.shouldAuditPublished(publisherID); ok {
		if err := eb.auditPublishedWithDB(ctx, db, publisherID, ev.ID, ev.Type, dispatched, coalesced); err != nil {
			logging.L().Warnf("failed to audit event: %v", err)
		}
	}
}

// enqueueWithDB is the non-blocking send shared by publish and
// EnqueuePublished: a full channel drops the event with a throttled audit
// row and warning. auditEnqueued=false skips the per-subscriber "enqueued"
// row (the published path, ut-docs#3887). Caller holds eb.mu (RLock).
// Reports whether it was sent.
func (eb *EventBus) enqueueWithDB(ctx context.Context, db *sql.DB, sub EventSubscriber, ev Event, auditEnqueued bool) bool {
	select {
	case sub.Channel <- ev:
		if auditEnqueued {
			eb.auditDispatchWithDB(ctx, db, ev.ID, ev.Type, sub.PluginID, "enqueued", "")
		}
		return true
	default:
		if eb.shouldWarnChannelFull(sub.PluginID) {
			// This audit row / log line stands for a BURST of drops, not a
			// single one: every further drop for this plugin within
			// channelFullWarnInterval is coalesced into it. Say so in the
			// record itself — otherwise a reader of audit_log would
			// reasonably (and wrongly) infer that an event with no
			// "dropped" row was delivered, which after throttling is no
			// longer a safe inference.
			reason := fmt.Sprintf("channel full (further drops within %s coalesced into this entry)", channelFullWarnInterval)
			eb.auditDispatchWithDB(ctx, db, ev.ID, ev.Type, sub.PluginID, "dropped", reason)
			logging.L().Warnf("event channel full for plugin %s (further drops within %s suppressed)", sub.PluginID, channelFullWarnInterval)
		}
		return false
	}
}

// Ask sends a blocking event to subscribed plugins and returns the first
// answering plugin's response — for hooks where a plugin computes and
// returns a VALUE rather than just accepting/rejecting (e.g. a country-
// specific tax-rate override; core has no built-in notion of any country's
// rules, see internal/pos.TaxRateAsker). (ok=false, nil error) means no
// installed plugin answered — the caller falls back to its own default.
// Unlike Publish, at most one plugin is expected to answer a given event
// type (mirrors how e.g. payment methods are matched 1:1 by entry key); the
// first subscriber that returns a non-empty response wins, others are not
// consulted. A handler error still aborts the whole Ask (same failure
// semantics as Publish's Blocking mode).
func (eb *EventBus) Ask(ctx context.Context, eventType string, payload interface{}) (json.RawMessage, bool, error) {
	resp, _, ok, err := eb.AskFrom(ctx, eventType, payload)
	return resp, ok, err
}

// AskFrom is Ask that also names WHICH plugin answered (pluginID is ""
// when nobody did, or on error) — for callers that need the answering
// plugin's identity for a diagnostic event (ADR-0092 §2's plugin-ask
// lifecycle, ut-docs#2169) without inspecting the answer itself.
func (eb *EventBus) AskFrom(ctx context.Context, eventType string, payload interface{}) (json.RawMessage, string, bool, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, "", false, fmt.Errorf("marshal payload: %w", err)
	}
	event := Event{
		ID:        uuid.NewString(),
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payloadBytes,
	}

	eb.mu.RLock()
	subscribers := eb.subscribers[eventType]
	eb.mu.RUnlock()

	for _, sub := range subscribers {
		if sub.Handler == nil {
			continue
		}
		if err := CheckPermission(ctx, eb.dbHandle(), sub.PluginID, "events:receive"); err != nil {
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "denied", err.Error())
			continue
		}
		resp, err := sub.Handler(ctx, event)
		if err != nil {
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "error", err.Error())
			return nil, sub.PluginID, false, fmt.Errorf("ask %s failed for plugin %s: %w", eventType, sub.PluginID, err)
		}
		eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "success", "")
		if len(resp) == 0 {
			continue // handler ran but declined to answer — try the next subscriber
		}
		return resp, sub.PluginID, true, nil
	}
	return nil, "", false, nil
}

// AskPlugin is Ask restricted to a single, already-identified plugin —
// for callers that resolved WHICH installed plugin should answer before
// asking (e.g. a specific entries[] key, matched 1:1 to its owning
// pluginID) and must not accept an answer from any other subscriber of the
// same event type. Unlike Ask, which is for "any plugin with an opinion
// may answer" hooks (tax.rate.ask), this is for "exactly this plugin must
// answer" hooks — broadcasting here would let an unrelated installed
// plugin silently answer on the targeted plugin's behalf.
func (eb *EventBus) AskPlugin(ctx context.Context, pluginID, eventType string, payload interface{}) (json.RawMessage, bool, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal payload: %w", err)
	}
	event := Event{
		ID:        uuid.NewString(),
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payloadBytes,
	}

	eb.mu.RLock()
	subscribers := eb.subscribers[eventType]
	eb.mu.RUnlock()

	for _, sub := range subscribers {
		if sub.PluginID != pluginID || sub.Handler == nil {
			continue
		}
		if err := CheckPermission(ctx, eb.dbHandle(), sub.PluginID, "events:receive"); err != nil {
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "denied", err.Error())
			continue
		}
		resp, err := sub.Handler(ctx, event)
		if err != nil {
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "error", err.Error())
			return nil, false, fmt.Errorf("ask %s failed for plugin %s: %w", eventType, sub.PluginID, err)
		}
		eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "success", "")
		if len(resp) == 0 {
			continue // handler ran but declined to answer — no other subscriber is eligible
		}
		return resp, true, nil
	}
	return nil, false, nil
}

// auditEventWithDB logs event publication to audit_log, with the db handle
// passed in explicitly for callers that already hold eb.mu (publish,
// ut-docs#504) — calling dbHandle() there would be a recursive RLock, which
// can deadlock once a writer waits.
func (eb *EventBus) auditEventWithDB(ctx context.Context, db *sql.DB, eventID, eventType string, subscriberCount int) error {
	details := fmt.Sprintf("event_type=%s, subscribers=%d", eventType, subscriberCount)
	return data.NewPluginRepo(db).InsertAuditRaw(ctx, nil, "event_published", "event", eventID, details, time.Now())
}

// auditPublishedWithDB is the coalesced event_published row for a
// plugin-published event (ut-docs#3887): coalesced=N is the number of this
// publisher's publishes, of any event type, that got no row since its
// previous one.
func (eb *EventBus) auditPublishedWithDB(ctx context.Context, db *sql.DB, publisherID, eventID, eventType string, subscriberCount, coalesced int) error {
	details := fmt.Sprintf("event_type=%s, subscribers=%d, plugin_id=%s, coalesced=%d (publishes of any type by this plugin since its previous entry; further publishes within %s are coalesced into the next entry)",
		eventType, subscriberCount, publisherID, coalesced, publishedAuditInterval)
	return data.NewPluginRepo(db).InsertAuditRaw(ctx, nil, "event_published", "event", eventID, details, eb.clock())
}

// auditDispatch logs per-plugin dispatch results. Errors are swallowed to avoid blocking core flows.
func (eb *EventBus) auditDispatch(ctx context.Context, eventID, eventType, pluginID, status, errMsg string) {
	eb.auditDispatchWithDB(ctx, eb.dbHandle(), eventID, eventType, pluginID, status, errMsg)
}

// auditDispatchWithDB is auditDispatch with the db handle passed in
// explicitly, for callers that already hold eb.mu (publish, ut-docs#504) —
// see auditEventWithDB.
func (eb *EventBus) auditDispatchWithDB(ctx context.Context, db *sql.DB, eventID, eventType, pluginID, status, errMsg string) {
	details := fmt.Sprintf("event_type=%s, plugin_id=%s, status=%s", eventType, pluginID, status)
	if errMsg != "" {
		details += fmt.Sprintf(", error=%s", errMsg)
	}

	if err := data.NewPluginRepo(db).InsertAuditRaw(ctx, nil, "event_dispatch", "plugin", pluginID, details, time.Now()); err != nil {
		logging.L().Warnf("failed to audit dispatch: %v", err)
	}
}

// PublishSaleCompleted is a helper to publish sale.completed events
func (eb *EventBus) PublishSaleCompleted(ctx context.Context, saleEvent SaleCompletedEvent) (string, error) {
	// A sale just committed: the wasm schedule ticker holds due ticks while
	// sales keep landing (ADR-0121 §8 "pauses all ticks during a sale commit
	// burst", ut-docs#3161).
	eb.markSaleCompleted(time.Now())
	return eb.Publish(ctx, "sale.completed", saleEvent)
}

// markSaleCompleted records t as the latest sale commit.
func (eb *EventBus) markSaleCompleted(t time.Time) {
	eb.lastSaleCompleted.Store(t.UnixNano())
}

// lastSaleCompletedAt is when PublishSaleCompleted last ran on this bus;
// zero if never.
func (eb *EventBus) lastSaleCompletedAt() time.Time {
	ns := eb.lastSaleCompleted.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// redactCardPresentFields returns a copy of ev with every payment's
// card-present reconciliation fields (ut-docs#543: MaskedPAN, AuthCode,
// TerminalID, TraceID) cleared. Used by publish() (ut-docs#791) to build
// the payload delivered to sale.completed subscribers that lack the
// payments:reconciliation permission — everything else about the sale
// (totals, line items, the non-card payment method/amount/reference) is
// unaffected, since most ERP/accounting connectors need the sale, not the
// card data. Field-clearing (not dropping Payments entirely) so a
// connector's payment-method/amount reconciliation against its own ledger
// still works; the omitempty tags on those four fields mean a cleared
// field marshals as absent, not as an empty string, so an ungranted
// subscriber can't distinguish "no card-present payment" from "redacted"
// from the payload shape alone — same "can't tell no-data from
// no-permission" contract the sales/stock export ledgers already use
// (ut-docs#228, ADR referenced in reference/plugin-manifest.md).
func redactCardPresentFields(ev SaleCompletedEvent) SaleCompletedEvent {
	if len(ev.Payments) == 0 {
		return ev
	}
	redacted := ev
	redacted.Payments = make([]SalePayment, len(ev.Payments))
	for i, p := range ev.Payments {
		p.MaskedPAN = ""
		p.AuthCode = ""
		p.TerminalID = ""
		p.TraceID = ""
		redacted.Payments[i] = p
	}
	return redacted
}

// PublishStockAdjusted is a helper to publish stock.adjusted events
func (eb *EventBus) PublishStockAdjusted(ctx context.Context, stockEvent StockAdjustedEvent) (string, error) {
	return eb.Publish(ctx, "stock.adjusted", stockEvent)
}

// PublishCustomerErased is a helper to publish customer.erased events.
func (eb *EventBus) PublishCustomerErased(ctx context.Context, ev CustomerErasedEvent) (string, error) {
	return eb.Publish(ctx, "customer.erased", ev)
}

// Unsubscribe removes one plugin's subscriptions and closes its channel.
// No production caller: the WASM runtime never unsubscribes a single
// plugin — WasmRuntime.Sync rebuilds the whole subscriber set on every
// Manager.Reload via ResetSubscribers (which also closes the channels), and
// WasmRuntime.Close does the same at shutdown. Kept as a declared test
// helper (ut-docs#1566): TestEventBus_Unsubscribe pins the closed-channel
// contract, and the crash-isolation integration test uses it to simulate a
// plugin dropping out between subscribe and publish. Safe alongside a live
// publish for the same reason ResetSubscribers is (exclusive Lock vs.
// publish's held RLock). Dedupes by channel (ut-docs#2242) the same way
// ResetSubscribers does: a plugin subscribed to >=2 event types in one
// Subscribe/SubscribeWithHandler call shares one channel across those
// types, so closing it once per event-type occurrence would panic on the
// second close.
func (eb *EventBus) Unsubscribe(pluginID string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	closed := map[chan Event]bool{}
	for eventType, subs := range eb.subscribers {
		filtered := make([]EventSubscriber, 0)
		for _, sub := range subs {
			if sub.PluginID != pluginID {
				filtered = append(filtered, sub)
			} else if sub.Channel != nil && !closed[sub.Channel] {
				closed[sub.Channel] = true
				close(sub.Channel)
			}
		}
		eb.subscribers[eventType] = filtered
	}
	eb.generation++
}
