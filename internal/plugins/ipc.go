package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
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

// SaleCompletedEvent represents sale completion data
type SaleCompletedEvent struct {
	SaleID        string         `json:"sale_id"`
	TotalCents    int64          `json:"total_cents"`
	TaxCents      int64          `json:"tax_cents"`
	CustomerID    string         `json:"customer_id"`
	LineItems     []SaleLineItem `json:"line_items"`
	PaymentMethod string         `json:"payment_method"`
	CompletedAt   time.Time      `json:"completed_at"`
}

// SaleLineItem represents an item in a sale
type SaleLineItem struct {
	ItemID         string `json:"item_id"`
	Name           string `json:"name"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	TotalCents     int64  `json:"total_cents"`
}

// EventHandler executes a blocking handler for an event; returning an error signals rollback.
type EventHandler func(ctx context.Context, event Event) error

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

// SetEventMode configures the dispatch mode for an event type
// This allows runtime configuration of blocking vs non-blocking behavior
func (eb *EventBus) SetEventMode(eventType string, mode EventDispatchMode) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.eventModes[eventType] = mode
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
	for _, eventType := range eventTypes {
		var count int
		err := eb.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM plugin_hooks
			WHERE plugin_id = ? AND event = ? AND is_active = 1
		`, pluginID, eventType).Scan(&count)
		if err != nil {
			return nil, fmt.Errorf("check hooks: %w", err)
		}
		if count == 0 {
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
	// Encode payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
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
		return event.ID, nil
	}

	mode := eb.GetEventMode(eventType)
	dispatched := 0

	for _, sub := range subscribers {
		if err := CheckPermission(ctx, eb.db, sub.PluginID, "events:receive"); err != nil {
			eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "denied", err.Error())
			if mode == Blocking {
				return "", fmt.Errorf("event %s denied for plugin %s: %w", eventType, sub.PluginID, err)
			}
			continue
		}

		switch mode {
		case Blocking:
			if sub.Handler == nil {
				msg := "blocking event requires handler"
				eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "error", msg)
				return "", fmt.Errorf("%s: %s", sub.PluginID, msg)
			}
			if err := sub.Handler(ctx, event); err != nil {
				eb.auditDispatch(ctx, event.ID, eventType, sub.PluginID, "error", err.Error())
				return "", fmt.Errorf("blocking event %s failed for plugin %s: %w", eventType, sub.PluginID, err)
			}
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

	return event.ID, nil
}

// Acknowledge records event acknowledgment from a plugin
func (eb *EventBus) Acknowledge(ctx context.Context, eventID, pluginID string, success bool, errorMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	status := "success"
	if !success {
		status = "error"
	}

	details := fmt.Sprintf("event_id=%s, status=%s", eventID, status)
	if errorMsg != "" {
		details += fmt.Sprintf(", error=%s", errorMsg)
	}

	_, err := eb.db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, data_json, created_at)
		VALUES ('event_acknowledged', 'plugin', ?, ?, ?)
	`, pluginID, details, now)

	return err
}

// auditEvent logs event publication to audit_log
func (eb *EventBus) auditEvent(ctx context.Context, eventID, eventType string, subscriberCount int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("event_type=%s, subscribers=%d", eventType, subscriberCount)

	_, err := eb.db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, data_json, created_at)
		VALUES ('event_published', 'event', ?, ?, ?)
	`, eventID, details, now)
	return err
}

// auditDispatch logs per-plugin dispatch results. Errors are swallowed to avoid blocking core flows.
func (eb *EventBus) auditDispatch(ctx context.Context, eventID, eventType, pluginID, status, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("event_type=%s, plugin_id=%s, status=%s", eventType, pluginID, status)
	if errMsg != "" {
		details += fmt.Sprintf(", error=%s", errMsg)
	}

	if _, err := eb.db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, data_json, created_at)
		VALUES ('event_dispatch', 'plugin', ?, ?, ?)
	`, pluginID, details, now); err != nil {
		fmt.Printf("warning: failed to audit dispatch: %v\n", err)
	}
}

// PublishSaleCompleted is a helper to publish sale.completed events
func (eb *EventBus) PublishSaleCompleted(ctx context.Context, saleEvent SaleCompletedEvent) (string, error) {
	return eb.Publish(ctx, "sale.completed", saleEvent)
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
}
