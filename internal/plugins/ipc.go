package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
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
}

// SaleCompletedEvent is the payload published on "sale.completed". It is the
// stable contract external-integration plugins (ERP/accounting connectors:
// SAP, Dynamics/LS Central, …) consume to mirror each sale into another
// system. Money is integer minor units; quantities are decimal (weighed goods).
type SaleCompletedEvent struct {
	SaleID        string         `json:"sale_id"`
	ReceiptNo     string         `json:"receipt_no"`
	SaleType      string         `json:"sale_type"` // "sale" | "return"
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
}

// SalePayment is one tender applied to a sale (ERP contract).
type SalePayment struct {
	Method      string `json:"method"`
	AmountCents int64  `json:"amount_cents"`
	Reference   string `json:"reference"`
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
// plugins after every Manager.Reload.
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

// GetEventMode returns the dispatch mode for an event type
// Defaults to NonBlocking if not explicitly configured
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

// Subscribe registers a plugin to receive events
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

// publish is Publish's real implementation; it additionally returns the
// last successful Blocking handler's raw response so PublishAuthorize can
// surface it without duplicating the dispatch/permission/audit logic above.
func (eb *EventBus) publish(ctx context.Context, eventType string, payload interface{}) (eventID string, resp json.RawMessage, err error) {
	// Encode payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshal payload: %w", err)
	}

	event := Event{
		ID:        uuid.NewString(),
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payloadBytes,
	}

	// Snapshot subscribers
	eb.mu.RLock()
	subscribers, exists := eb.subscribers[eventType]
	eb.mu.RUnlock()

	if !exists || len(subscribers) == 0 {
		_ = eb.auditEvent(ctx, event.ID, eventType, 0)
		return event.ID, nil, nil
	}

	mode := eb.GetEventMode(eventType)
	dispatched := 0

	for _, sub := range subscribers {
		if err := CheckPermission(ctx, eb.dbHandle(), sub.PluginID, "events:receive"); err != nil {
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "denied", err.Error())
			if mode == Blocking {
				return "", nil, fmt.Errorf("event %s denied for plugin %s: %w", eventType, sub.PluginID, err)
			}
			continue
		}

		switch mode {
		case Blocking:
			if sub.Handler == nil {
				msg := "blocking event requires handler"
				eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "error", msg)
				return "", nil, fmt.Errorf("blocking event %s failed for plugin %s: %s", eventType, sub.PluginID, msg)
			}
			// Most Blocking callers only care about accept/reject (payment
			// authorization); PublishAuthorize is how a caller opts into
			// also reading the handler's raw response.
			handlerResp, err := sub.Handler(ctx, event)
			if err != nil {
				eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "error", err.Error())
				return "", nil, fmt.Errorf("blocking event %s failed for plugin %s: %w", eventType, sub.PluginID, err)
			}
			resp = handlerResp
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "success", "")
			dispatched++
		default:
			select {
			case sub.Channel <- event:
				eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "enqueued", "")
				dispatched++
			default:
				eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "dropped", "channel full")
				fmt.Printf("warning: event channel full for plugin %s\n", sub.PluginID)
			}
		}
	}

	if err := eb.auditEvent(ctx, event.ID, eventType, dispatched); err != nil {
		fmt.Printf("warning: failed to audit event: %v\n", err)
	}

	return event.ID, resp, nil
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
			return nil, false, fmt.Errorf("ask %s failed for plugin %s: %w", eventType, sub.PluginID, err)
		}
		eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "success", "")
		if len(resp) == 0 {
			continue // handler ran but declined to answer — try the next subscriber
		}
		return resp, true, nil
	}
	return nil, false, nil
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

// Acknowledge records event acknowledgment from a plugin
func (eb *EventBus) Acknowledge(ctx context.Context, eventID, pluginID string, success bool, errorMsg string) error {
	status := "success"
	if !success {
		status = "error"
	}

	details := fmt.Sprintf("event_id=%s, status=%s", eventID, status)
	if errorMsg != "" {
		details += fmt.Sprintf(", error=%s", errorMsg)
	}

	return data.NewPluginRepo(eb.dbHandle()).InsertAuditRaw(ctx, nil, "event_acknowledged", "plugin", pluginID, details, time.Now())
}

// auditEvent logs event publication to audit_log
func (eb *EventBus) auditEvent(ctx context.Context, eventID, eventType string, subscriberCount int) error {
	details := fmt.Sprintf("event_type=%s, subscribers=%d", eventType, subscriberCount)
	return data.NewPluginRepo(eb.dbHandle()).InsertAuditRaw(ctx, nil, "event_published", "event", eventID, details, time.Now())
}

// auditDispatch logs per-plugin dispatch results. Errors are swallowed to avoid blocking core flows.
func (eb *EventBus) auditDispatch(ctx context.Context, eventID, eventType, pluginID, status, errMsg string) {
	details := fmt.Sprintf("event_type=%s, plugin_id=%s, status=%s", eventType, pluginID, status)
	if errMsg != "" {
		details += fmt.Sprintf(", error=%s", errMsg)
	}

	if err := data.NewPluginRepo(eb.dbHandle()).InsertAuditRaw(ctx, nil, "event_dispatch", "plugin", pluginID, details, time.Now()); err != nil {
		fmt.Printf("warning: failed to audit dispatch: %v\n", err)
	}
}

// PublishSaleCompleted is a helper to publish sale.completed events
func (eb *EventBus) PublishSaleCompleted(ctx context.Context, saleEvent SaleCompletedEvent) (string, error) {
	return eb.Publish(ctx, "sale.completed", saleEvent)
}

// PublishStockAdjusted is a helper to publish stock.adjusted events
func (eb *EventBus) PublishStockAdjusted(ctx context.Context, stockEvent StockAdjustedEvent) (string, error) {
	return eb.Publish(ctx, "stock.adjusted", stockEvent)
}

// Unsubscribe removes a plugin's subscription
func (eb *EventBus) Unsubscribe(pluginID string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for eventType, subs := range eb.subscribers {
		filtered := make([]EventSubscriber, 0)
		for _, sub := range subs {
			if sub.PluginID != pluginID {
				filtered = append(filtered, sub)
			} else {
				// Close the channel
				close(sub.Channel)
			}
		}
		eb.subscribers[eventType] = filtered
	}
	eb.generation++
}
