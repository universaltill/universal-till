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

// EventBus manages event distribution to plugins
type EventBus struct {
	db          *sql.DB
	mu          sync.RWMutex
	subscribers map[string][]EventSubscriber // event_type -> subscribers
}

// EventSubscriber represents a plugin subscribed to events
type EventSubscriber struct {
	PluginID   string
	EventTypes []string
	Channel    chan Event
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

// NewEventBus creates a new event bus
func NewEventBus(db *sql.DB) *EventBus {
	return &EventBus{
		db:          db,
		subscribers: make(map[string][]EventSubscriber),
	}
}

// Subscribe registers a plugin to receive events
func (eb *EventBus) Subscribe(ctx context.Context, pluginID string, eventTypes []string) (<-chan Event, error) {
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
	}

	// Register for each event type
	for _, eventType := range eventTypes {
		eb.subscribers[eventType] = append(eb.subscribers[eventType], subscriber)
	}

	return ch, nil
}

// Publish sends an event to all subscribed plugins
func (eb *EventBus) Publish(ctx context.Context, eventType string, payload interface{}) (string, error) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

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

	eb.mu.RLock()
	subscribers, exists := eb.subscribers[eventType]
	eb.mu.RUnlock()

	if !exists || len(subscribers) == 0 {
		// No subscribers for this event type
		return event.ID, nil
	}

	// Pre-check permissions for all subscribers (without holding lock)
	authorizedSubs := make([]EventSubscriber, 0, len(subscribers))
	for _, sub := range subscribers {
		if err := CheckPermission(ctx, eb.db, sub.PluginID, "events:receive"); err == nil {
			authorizedSubs = append(authorizedSubs, sub)
		}
	}

	for _, sub := range authorizedSubs {
		// Non-blocking send
		select {
		case sub.Channel <- event:
			// Event sent
		default:
			// Channel full, log warning
			fmt.Printf("warning: event channel full for plugin %s\n", sub.PluginID)
		}
	}

	// Audit the event publication
	if err := eb.auditEvent(ctx, event.ID, eventType, len(subscribers)); err != nil {
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
		INSERT INTO audit_log (action, entity_type, entity_id, details, created_at)
		VALUES ('event_acknowledged', 'plugin', ?, ?, ?)
	`, pluginID, details, now)

	return err
}

// auditEvent logs event publication to audit_log
func (eb *EventBus) auditEvent(ctx context.Context, eventID, eventType string, subscriberCount int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("event_type=%s, subscribers=%d", eventType, subscriberCount)

	_, err := eb.db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, details, created_at)
		VALUES ('event_published', 'event', ?, ?, ?)
	`, eventID, details, now)
	return err
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
