package plugins

import (
	"context"
	"testing"
	"time"
)

func TestEventBus_SubscribePublish(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	// Install plugin with hooks and permissions
	manifest := &Manifest{
		ID:         "com.test.events",
		Name:       "Event Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{
				Event:  "sale.completed",
				Action: "test.onSale",
			},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Grant permission
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	// Create event bus
	bus := NewEventBus(db)

	// Subscribe
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Publish event
	saleEvent := SaleCompletedEvent{
		SaleID:        "sale-123",
		TotalCents:    1000,
		TaxCents:      100,
		PaymentMethod: "cash",
		CompletedAt:   time.Now(),
	}

	eventID, err := bus.PublishSaleCompleted(ctx, saleEvent)
	if err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	if eventID == "" {
		t.Error("expected non-empty event ID")
	}

	// Receive event
	select {
	case event := <-eventChan:
		if event.Type != "sale.completed" {
			t.Errorf("expected event type 'sale.completed', got '%s'", event.Type)
		}
		if event.ID != eventID {
			t.Errorf("expected event ID '%s', got '%s'", eventID, event.ID)
		}

		// Acknowledge
		if err := bus.Acknowledge(ctx, event.ID, manifest.ID, true, ""); err != nil {
			t.Errorf("Acknowledge failed: %v", err)
		}

	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_SubscribeWithoutHook(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Install plugin without hooks
	manifest := &Manifest{
		ID:         "com.test.nohook",
		Name:       "No Hook Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	bus := NewEventBus(db)

	// Try to subscribe - should fail
	_, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err == nil {
		t.Fatal("expected error when subscribing without hook, got nil")
	}
}

func TestEventBus_PublishWithoutPermission(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	// Install plugin with hooks but no permissions
	manifest := &Manifest{
		ID:         "com.test.noperm",
		Name:       "No Permission Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{
				Event:  "sale.completed",
				Action: "test.onSale",
			},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	// Don't grant permission

	bus := NewEventBus(db)

	// Subscribe (will succeed because hook exists)
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Publish event
	saleEvent := SaleCompletedEvent{
		SaleID:     "sale-456",
		TotalCents: 2000,
	}

	_, err = bus.PublishSaleCompleted(ctx, saleEvent)
	if err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	// Should NOT receive event (no permission)
	select {
	case <-eventChan:
		t.Error("expected no event due to missing permission")
	case <-time.After(100 * time.Millisecond):
		// Expected - no event received
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	// Install two plugins
	plugins := []string{"com.test.sub1", "com.test.sub2"}
	for _, pluginID := range plugins {
		manifest := &Manifest{
			ID:         pluginID,
			Name:       pluginID,
			Version:    "1.0.0",
			Entrypoint: "./test",
			Hooks: []ManifestHook{
				{
					Event:  "sale.completed",
					Action: "test.onSale",
				},
			},
			Permissions: []string{"events:receive"},
		}
		if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
			t.Fatalf("persist manifest: %v", err)
		}

		// Grant permission
		if err := GrantPermission(ctx, db, pluginID, "events:receive"); err != nil {
			t.Fatalf("grant permission: %v", err)
		}
	}

	bus := NewEventBus(db)

	// Subscribe both plugins
	chan1, err := bus.Subscribe(ctx, plugins[0], []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe plugin 1 failed: %v", err)
	}

	chan2, err := bus.Subscribe(ctx, plugins[1], []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe plugin 2 failed: %v", err)
	}

	// Publish event
	saleEvent := SaleCompletedEvent{
		SaleID:     "sale-789",
		TotalCents: 3000,
	}

	eventID, err := bus.PublishSaleCompleted(ctx, saleEvent)
	if err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	// Both should receive
	receivedCount := 0
	timeout := time.After(time.Second)

	for i := 0; i < 2; i++ {
		select {
		case event := <-chan1:
			if event.ID == eventID {
				receivedCount++
			}
		case event := <-chan2:
			if event.ID == eventID {
				receivedCount++
			}
		case <-timeout:
			t.Fatal("timeout waiting for events")
		}
	}

	if receivedCount != 2 {
		t.Errorf("expected 2 subscribers to receive event, got %d", receivedCount)
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.unsub",
		Name:       "Unsubscribe Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{
				Event:  "sale.completed",
				Action: "test.onSale",
			},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}

	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	bus := NewEventBus(db)

	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Unsubscribe
	bus.Unsubscribe(manifest.ID)

	// Wait a moment for unsubscribe to complete
	time.Sleep(50 * time.Millisecond)

	// Publish event
	saleEvent := SaleCompletedEvent{
		SaleID:     "sale-999",
		TotalCents: 4000,
	}

	_, err = bus.PublishSaleCompleted(ctx, saleEvent)
	if err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	// Channel should be closed - reading from it should return zero value immediately
	select {
	case event, ok := <-eventChan:
		if ok {
			t.Errorf("expected closed channel, but received event: %v", event)
		}
		// ok == false means channel was closed, which is expected
	case <-time.After(100 * time.Millisecond):
		t.Error("expected closed channel to return immediately")
	}
}

func TestEventBus_AcknowledgeError(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()
	bus := NewEventBus(db)

	// Acknowledge with error
	err := bus.Acknowledge(ctx, "event-123", "plugin-abc", false, "processing failed")
	if err != nil {
		t.Errorf("Acknowledge failed: %v", err)
	}

	// Verify audit log contains error details
	var details string
	err = db.QueryRowContext(ctx, `
		SELECT details FROM audit_log WHERE action = 'event_acknowledged'
	`).Scan(&details)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}

	// Should contain error message
	if details == "" {
		t.Error("expected details in audit log")
	}
}
