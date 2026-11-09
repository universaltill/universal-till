package pages

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// boundedAskPlugin (ut-docs#4160): the shared bounded ask under the view
// asks and the settings options ask.

const askPluginID = "com.test.asker"

type pagesAskRig struct{ d *common.Deps }

// newPagesAskRig installs askPluginID with events:receive on a fresh DB.
func newPagesAskRig(t *testing.T) *pagesAskRig {
	t.Helper()
	d, _ := pluginPageTestDeps(t)
	seedTestPlugin(t, d.Db, askPluginID, "Asker", "1.0.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('ask-er',?,'events:receive',1)`, askPluginID); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(d.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	return &pagesAskRig{d: d}
}

func (r *pagesAskRig) subscribe(t *testing.T, event string, h plugins.EventHandler) {
	t.Helper()
	if _, err := r.d.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES(?,?,?,'settings',1)`, "h-"+event, askPluginID, event); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SharedBus(r.d.Db).SubscribeWithHandler(t.Context(), askPluginID, []string{event}, h); err != nil {
		t.Fatal(err)
	}
}

func askHarness(t *testing.T, h plugins.EventHandler) *pagesAskRig {
	t.Helper()
	r := newPagesAskRig(t)
	r.subscribe(t, "x.test.ask", h)
	return r
}

func TestBoundedAskPlugin_ReturnsAnswer(t *testing.T) {
	r := askHarness(t, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"a":1}`), nil
	})
	raw, ok, err := boundedAskPlugin(t.Context(), r.d, askPluginID, "x.test.ask", map[string]any{}, time.Second)
	if err != nil || !ok || string(raw) != `{"a":1}` {
		t.Fatalf("got %q %v %v", raw, ok, err)
	}
}

func TestBoundedAskPlugin_HandlerIgnoringContextReturnsByTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	r := askHarness(t, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		<-release // ignores ctx
		return nil, nil
	})
	start := time.Now()
	_, ok, err := boundedAskPlugin(t.Context(), r.d, askPluginID, "x.test.ask", map[string]any{}, 100*time.Millisecond)
	if err == nil || ok {
		t.Fatalf("want timeout error, got ok=%v err=%v", ok, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestBoundedAskPlugin_PanicIsError(t *testing.T) {
	r := askHarness(t, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		panic("boom")
	})
	_, ok, err := boundedAskPlugin(t.Context(), r.d, askPluginID, "x.test.ask", map[string]any{}, time.Second)
	if err == nil || ok {
		t.Fatalf("want error, got ok=%v err=%v", ok, err)
	}
}

func TestBoundedAskPlugin_DeclinedIsNotAnswered(t *testing.T) {
	r := askHarness(t, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return nil, nil
	})
	raw, ok, err := boundedAskPlugin(t.Context(), r.d, askPluginID, "x.test.ask", map[string]any{}, time.Second)
	if err != nil || ok || raw != nil {
		t.Fatalf("got %q %v %v", raw, ok, err)
	}
}
