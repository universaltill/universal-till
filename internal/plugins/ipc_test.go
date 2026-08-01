package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestEventBus_NonBlockingAuditsAndContinues(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	// Allowed plugin
	allow := &Manifest{
		ID:         "plugin-allow",
		Name:       "Allow",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "sale.completed", Action: "onSale"},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, allow, InstallOptions{}); err != nil {
		t.Fatalf("persist allow: %v", err)
	}
	if err := GrantPermission(ctx, db, allow.ID, "events:receive"); err != nil {
		t.Fatalf("grant allow: %v", err)
	}

	// Denied plugin (no permission granted)
	deny := &Manifest{
		ID:         "plugin-deny",
		Name:       "Deny",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "sale.completed", Action: "onSale"},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, deny, InstallOptions{}); err != nil {
		t.Fatalf("persist deny: %v", err)
	}

	bus := NewEventBus(db)

	allowCh, err := bus.Subscribe(ctx, allow.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("subscribe allow: %v", err)
	}
	denyCh, err := bus.Subscribe(ctx, deny.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("subscribe deny: %v", err)
	}

	_, err = bus.PublishSaleCompleted(ctx, SaleCompletedEvent{SaleID: "sale-abc", TotalCents: 100})
	if err != nil {
		t.Fatalf("publish non-blocking: %v", err)
	}

	// allowed plugin receives
	select {
	case <-allowCh:
	case <-time.After(time.Second):
		t.Fatal("expected allow plugin to receive event")
	}

	// denied plugin should not receive
	select {
	case ev := <-denyCh:
		t.Fatalf("expected no event for denied plugin, got %v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	// Audit should record both enqueue and denial
	rows, err := db.QueryContext(ctx, `
		SELECT data_json FROM audit_log WHERE action = 'event_dispatch'
	`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()

	var hasEnqueued, hasDenied bool
	for rows.Next() {
		var details string
		if err := rows.Scan(&details); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		if strings.Contains(details, "plugin_id=plugin-allow") && strings.Contains(details, "status=enqueued") {
			hasEnqueued = true
		}
		if strings.Contains(details, "plugin_id=plugin-deny") && strings.Contains(details, "status=denied") {
			hasDenied = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if !hasEnqueued || !hasDenied {
		t.Fatalf("expected audit entries for enqueue (%v) and deny (%v)", hasEnqueued, hasDenied)
	}
}

func TestEventBus_BlockingRollsBackOnError(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "plugin-block",
		Name:       "Blocker",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "sale.completed", Action: "onSale"},
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
	bus.SetEventMode("sale.completed", Blocking)

	handlerErr := errors.New("handler failed")
	if _, err := bus.SubscribeWithHandler(ctx, manifest.ID, []string{"sale.completed"}, func(ctx context.Context, event Event) (json.RawMessage, error) {
		return nil, handlerErr
	}); err != nil {
		t.Fatalf("subscribe with handler: %v", err)
	}

	if _, err := bus.PublishSaleCompleted(ctx, SaleCompletedEvent{SaleID: "sale-block", TotalCents: 500}); err == nil {
		t.Fatal("expected blocking publish to fail")
	} else if !strings.Contains(err.Error(), handlerErr.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}

	var details string
	err := db.QueryRowContext(ctx, `
		SELECT data_json FROM audit_log 
		WHERE action = 'event_dispatch' AND entity_id = ? 
		ORDER BY id DESC LIMIT 1
	`, manifest.ID).Scan(&details)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if !strings.Contains(details, "status=error") {
		t.Fatalf("expected error status in audit details, got %s", details)
	}
}

// TestEventBus_Ask covers the value-returning hook (internal/pos.
// TaxRateAsker's real-world use): no subscriber → (nil, false, nil), a
// subscriber that declines (empty response) → also (nil, false, nil), a
// subscriber that answers → its response, and a handler error aborts the
// whole Ask instead of silently falling back.
func TestEventBus_Ask(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	manifest := &Manifest{
		ID:          "plugin-asker",
		Name:        "Asker",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Hooks:       []ManifestHook{{Event: "tax.rate.ask", Action: "rate"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	bus := NewEventBus(db)
	bus.SetEventMode("tax.rate.ask", Blocking)

	// No subscriber yet.
	if resp, ok, err := bus.Ask(ctx, "tax.rate.ask", map[string]any{}); err != nil || ok || resp != nil {
		t.Fatalf("expected no answer with no subscriber, got resp=%s ok=%v err=%v", resp, ok, err)
	}

	answer := json.RawMessage(`{"rate_bp":700}`)
	decline := false
	handlerErr := errors.New("asker: crashed")
	fail := false
	if _, err := bus.SubscribeWithHandler(ctx, manifest.ID, []string{"tax.rate.ask"}, func(ctx context.Context, event Event) (json.RawMessage, error) {
		if fail {
			return nil, handlerErr
		}
		if decline {
			return nil, nil
		}
		return answer, nil
	}); err != nil {
		t.Fatalf("subscribe with handler: %v", err)
	}

	resp, ok, err := bus.Ask(ctx, "tax.rate.ask", map[string]any{})
	if err != nil || !ok || string(resp) != string(answer) {
		t.Fatalf("expected answer %s, got resp=%s ok=%v err=%v", answer, resp, ok, err)
	}

	decline = true
	if resp, ok, err := bus.Ask(ctx, "tax.rate.ask", map[string]any{}); err != nil || ok || resp != nil {
		t.Fatalf("expected no answer when handler declines, got resp=%s ok=%v err=%v", resp, ok, err)
	}
	decline = false

	fail = true
	if _, ok, err := bus.Ask(ctx, "tax.rate.ask", map[string]any{}); err == nil || ok {
		t.Fatalf("expected handler error to propagate, got ok=%v err=%v", ok, err)
	} else if !strings.Contains(err.Error(), handlerErr.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEventBus_AskPlugin_DoesNotAcceptAnswerFromOtherSubscriber is the
// regression for the bug an independent review found in ut-docs#189: a
// caller that already resolved WHICH installed plugin should answer (e.g.
// by entries[].key) must not silently accept a different plugin's answer
// to the same event type. Ask() broadcasts to the first answering
// subscriber; AskPlugin() must not.
func TestEventBus_AskPlugin_DoesNotAcceptAnswerFromOtherSubscriber(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	for _, id := range []string{"plugin-right", "plugin-wrong"} {
		manifest := &Manifest{
			ID: id, Name: id, Version: "1.0.0", Entrypoint: "./test",
			Hooks:       []ManifestHook{{Event: "export.requested.ask", Action: "export"}},
			Permissions: []string{"events:receive"},
		}
		if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
			t.Fatalf("persist manifest %s: %v", id, err)
		}
		if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
			t.Fatalf("grant permission %s: %v", id, err)
		}
	}

	bus := NewEventBus(db)
	bus.SetEventMode("export.requested.ask", Blocking)

	// "wrong" subscribes first and always answers — if AskPlugin ever
	// broadcasts like Ask does, this is the answer that would leak through.
	if _, err := bus.SubscribeWithHandler(ctx, "plugin-wrong", []string{"export.requested.ask"},
		func(ctx context.Context, event Event) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true,"message":"answered by wrong plugin"}`), nil
		}); err != nil {
		t.Fatalf("subscribe wrong: %v", err)
	}

	// "right" is the plugin the caller actually resolved (e.g. via
	// entries[].key → owning pluginID) but hasn't answered yet in this
	// sub-test — AskPlugin targeting it must get nothing, NOT wrong's answer.
	if resp, ok, err := bus.AskPlugin(ctx, "plugin-right", "export.requested.ask", map[string]any{}); err != nil || ok || resp != nil {
		t.Fatalf("expected no answer (plugin-right has no handler yet), got resp=%s ok=%v err=%v", resp, ok, err)
	}

	rightAnswer := json.RawMessage(`{"ok":true,"message":"answered by right plugin"}`)
	if _, err := bus.SubscribeWithHandler(ctx, "plugin-right", []string{"export.requested.ask"},
		func(ctx context.Context, event Event) (json.RawMessage, error) {
			return rightAnswer, nil
		}); err != nil {
		t.Fatalf("subscribe right: %v", err)
	}

	resp, ok, err := bus.AskPlugin(ctx, "plugin-right", "export.requested.ask", map[string]any{})
	if err != nil || !ok || string(resp) != string(rightAnswer) {
		t.Fatalf("expected right plugin's answer %s, got resp=%s ok=%v err=%v", rightAnswer, resp, ok, err)
	}

	// Ask() (unrestricted) would return whichever subscriber answers
	// first — confirms this is genuinely a broadcast-vs-targeted
	// difference, not a coincidence of subscription order.
	if resp, ok, err := bus.Ask(ctx, "export.requested.ask", map[string]any{}); err != nil || !ok || string(resp) != `{"ok":true,"message":"answered by wrong plugin"}` {
		t.Fatalf("sanity check failed: expected broadcast Ask to hit the first (wrong) subscriber, got resp=%s ok=%v err=%v", resp, ok, err)
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
		SELECT data_json FROM audit_log WHERE action = 'event_acknowledged'
	`).Scan(&details)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}

	// Should contain error message
	if details == "" {
		t.Error("expected details in audit log")
	}
}
