package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Event data classes (ADR-0153 amendment 2026-10-15, ut-docs#4147).
//
// Decision 1 gates an ai.agent plugin's view_query on ai.send.<class>; an
// event carries till data too, and is not chosen by the agent, so its set
// lists every class the payload carries. A plugin whose installed manifest
// declares ai.agent gets an event only when every non-shop class in the set
// is granted, checked live at delivery in WasmRuntime.handleEvent — the one
// place every path (bus publish and ask, UI asks, jobs, schedule ticks)
// calls the guest. An event with no entry is refused (fail closed), as a
// view with no class is. Plugins without ai.agent are not checked at all.

// ErrEventDataClassDenied is the error handleEvent returns when it withholds
// an event from an ai.agent plugin. An error, not a nil answer: for a
// blocking delivery (payment authorize) a nil answer would read as
// approval. The refusal is audited (coalesced), so callers log it at Debug.
var ErrEventDataClassDenied = errors.New("event withheld from ai.agent plugin")

// eventDataClasses maps each core event type that can reach a plugin to
// the data classes its payload carries. An empty set carries no business
// data and needs no grant. Keys are string literals so this package does
// not import the pages that raise them; internal/pages pins its own
// constants to these keys.
var eventDataClasses = map[string][]string{
	// line items and totals, customer_id, cashier_id
	"sale.completed":  {data.DataClassSales, data.DataClassCustomers, data.DataClassStaff},
	"stock.adjusted":  {data.DataClassStock},
	"customer.erased": {data.DataClassCustomers},
	// the item photo and the catalog item it was matched to
	"catalog.identify":           {data.DataClassCatalog},
	"catalog.identify.confirmed": {data.DataClassCatalog},
	// the operator's own request and page parameters
	"ui.view.ask":           {},
	"ui.action.ask":         {},
	"plugin.button.pressed": {},
	// the outcome of the agent's own proposal
	"agent.action.result": {},
}

// EventDataClasses returns the data classes eventType carries; ok is false
// for an event core has not classified.
func EventDataClasses(eventType string) (classes []string, ok bool) {
	c, ok := eventDataClasses[eventType]
	return slices.Clone(c), ok
}

// eventClassesFor is EventDataClasses for one delivery to pluginID: its own
// events — its schedule ticks and what it published itself — carry nothing
// of core's. A hop-0 event under the plugin's prefix is NOT its own: core
// publishes manifest-named payment and button trigger events (sale id,
// amount, reference) under whatever name the manifest chose, so those go
// through the registry and are refused unless registered. Plugin ids can
// nest (com.acme, com.acme.ai), so a published event is its own only when
// the plugin is the publisher.
func eventClassesFor(pluginID string, ev Event) ([]string, bool) {
	own := ev.Scheduled || (ev.Hop > 0 && ev.Publisher == pluginID)
	if own && strings.HasPrefix(ev.Type, pluginID+".") {
		return nil, true
	}
	return EventDataClasses(ev.Type)
}

// eventGate is what Sync learned about a plugin's installed manifest, read
// once per load so the delivery path adds no DB or file read for a plugin
// without ai.agent (sale.completed and payment authorize run in checkout).
type eventGate uint8

const (
	eventGateNone       eventGate = iota // no ai.agent: not checked
	eventGateAIAgent                     // declares ai.agent: check classes
	eventGateUnreadable                  // manifest unreadable at load: refuse all
)

// installedEventGate reads pluginID's installed manifest for its gate. With
// no installed manifest (ok=false: manifest.json missing, no active version)
// the declared permission rows decide, so a lost manifest cannot turn an
// ai.agent plugin's gate off (the view path refuses in that state too).
func installedEventGate(ctx context.Context, db *sql.DB, pluginID string) (eventGate, error) {
	m, ok, err := InstalledManifest(ctx, db, pluginID)
	switch {
	case err != nil:
		return eventGateUnreadable, err
	case ok && m != nil:
		if slices.Contains(m.Permissions, AIAgentPermission) {
			return eventGateAIAgent, nil
		}
		return eventGateNone, nil
	}
	_, declared, err := data.NewPluginRepo(db).CheckPermission(ctx, pluginID, AIAgentPermission)
	switch {
	case err != nil:
		return eventGateUnreadable, err
	case declared:
		return eventGateAIAgent, nil
	}
	return eventGateNone, nil
}

// checkEventDataClasses is the delivery check, run before the guest is
// instantiated. nil = deliver. A refusal wraps ErrEventDataClassDenied; a
// grant lookup failure is a plain error (fail closed, logged loudly).
func (w *WasmRuntime) checkEventDataClasses(ctx context.Context, pluginID string, ev Event) error {
	w.mu.Lock()
	gate := w.eventGates[pluginID]
	db := w.db
	w.mu.Unlock()
	if gate == eventGateNone {
		return nil
	}
	if db == nil {
		return fmt.Errorf("event %s for %s: no database to check AI data classes", ev.Type, pluginID)
	}
	if gate == eventGateUnreadable {
		w.auditEventDenial(ctx, db, pluginID, ev.Type, AIAgentPermission, "installed manifest was unreadable at load")
		return fmt.Errorf("%w: %s: installed manifest of %s was unreadable at load", ErrEventDataClassDenied, ev.Type, pluginID)
	}
	classes, ok := eventClassesFor(pluginID, ev)
	if !ok {
		w.auditEventDenial(ctx, db, pluginID, ev.Type, AIAgentPermission, "event has no AI data class")
		return fmt.Errorf("%w: %s has no AI data class", ErrEventDataClassDenied, ev.Type)
	}
	repo := data.NewPluginRepo(db)
	for _, c := range classes {
		if c == data.DataClassShop {
			continue
		}
		perm := AISendPermissionPrefix + c
		// The repo check, not CheckPermission: that audits every denial,
		// and a busy till must not write one row per sale (coalesced below).
		granted, _, err := repo.CheckPermission(ctx, pluginID, perm)
		if err != nil {
			return fmt.Errorf("event %s for %s: check %s: %w", ev.Type, pluginID, perm, err)
		}
		if !granted {
			w.auditEventDenial(ctx, db, pluginID, ev.Type, perm, "payload carries "+c+" data, "+perm+" not granted")
			return fmt.Errorf("%w: %s needs %s", ErrEventDataClassDenied, ev.Type, perm)
		}
	}
	return nil
}

// auditEventDenial writes the permission_denied row for a withheld event,
// at most one per plugin and event type per publishedAuditInterval
// (ut-docs#3887's shape), carrying the count of refusals it coalesced.
func (w *WasmRuntime) auditEventDenial(ctx context.Context, db *sql.DB, pluginID, eventType, permission, why string) {
	ok, n := w.evDenials.allow(pluginID, eventType)
	if !ok {
		return
	}
	reason := fmt.Sprintf("event %s not delivered: %s (coalesced=%d earlier denials; further denials within %s are coalesced into the next entry)",
		eventType, why, n, publishedAuditInterval)
	if err := auditPermissionDenial(context.WithoutCancel(ctx), db, pluginID, permission, reason); err != nil {
		logging.L().Warnf("failed to audit event denial: %v", err)
	}
}

// eventDenialThrottle is the per (plugin, event type) audit throttle, the
// same shape as EventBus.shouldAuditPublishedDenied: a count is carried into
// the next row for its key; a trailing burst is not counted.
type eventDenialThrottle struct {
	mu  sync.Mutex
	now func() time.Time // test clock; nil = time.Now
	at  map[string]time.Time
	n   map[string]int
}

func (d *eventDenialThrottle) allow(pluginID, eventType string) (bool, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.at == nil {
		d.at = make(map[string]time.Time)
		d.n = make(map[string]int)
	}
	now := time.Now()
	if d.now != nil {
		now = d.now()
	}
	key := pluginID + "\x00" + eventType
	if last, ok := d.at[key]; ok && now.Sub(last) < publishedAuditInterval {
		d.n[key]++
		return false, 0
	}
	d.at[key] = now
	n := d.n[key]
	d.n[key] = 0
	return true, n
}

// logEventErr logs a handler error: a withheld event at Debug (its audit
// row covers it), anything else at Error.
func logEventErr(format string, err error, args ...any) {
	args = append(args, err)
	if errors.Is(err, ErrEventDataClassDenied) {
		logging.L().Debugf(format, args...)
		return
	}
	logging.L().Errorf(format, args...)
}

// handlerErrStatus is the event_dispatch status for a Blocking handler's
// error: "denied" for a withheld event (like a refused events:receive),
// "error" otherwise. The publish fails either way.
func handlerErrStatus(err error) string {
	if errors.Is(err, ErrEventDataClassDenied) {
		return "denied"
	}
	return "error"
}
