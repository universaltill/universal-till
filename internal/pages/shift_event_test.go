package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5770: POST /api/shifts/open and /close publish the neutral
// shift.opened / shift.closed events (contract: ut-docs
// reference/contracts/shift-events.md) once the row has committed.

// subscribeShiftEvents registers an in-process subscriber to both shift
// events. With grant=false the plugin lacks events:receive.
func subscribeShiftEvents(t *testing.T, dp *common.Deps, grant bool) <-chan plugins.Event {
	t.Helper()
	const id = "com.test.accounting"
	seedFiscalSignHookPlugin(t, dp, id, shiftOpenedEvent, true)
	seedFiscalSignHookPlugin(t, dp, id, shiftClosedEvent, true)
	if !grant {
		if _, err := dp.Db.Exec(`DELETE FROM plugin_permissions WHERE plugin_id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	bus := plugins.SharedBus(dp.Db)
	ch, err := bus.Subscribe(context.Background(), id, []string{shiftOpenedEvent, shiftClosedEvent})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(bus.ResetSubscribers)
	return ch
}

func recvShiftEvent(t *testing.T, ch <-chan plugins.Event, wantType string) map[string]any {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != wantType {
			t.Fatalf("event type = %q, want %q", ev.Type, wantType)
		}
		var raw map[string]any
		if err := json.Unmarshal(ev.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		return raw
	case <-time.After(2 * time.Second):
		t.Fatalf("no %s event received", wantType)
		return nil
	}
}

func assertNoShiftEvent(t *testing.T, ch <-chan plugins.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected %s event: %s", ev.Type, ev.Payload)
	case <-time.After(100 * time.Millisecond):
	}
}

func shiftEventPublishedRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='event_published' AND data_json LIKE '%event_type=shift.%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedShiftRegister(t *testing.T, dp *common.Deps, id string) {
	t.Helper()
	if _, err := dp.Db.Exec(`INSERT INTO registers(id,name,is_active) VALUES(?, 'Front Till', 1)`, id); err != nil {
		t.Fatal(err)
	}
}

// openShiftOK opens a shift through the handler and returns the response
// shift_id.
func openShiftOK(t *testing.T, mux *http.ServeMux, body string) string {
	t.Helper()
	rec := postShiftJSON(t, mux, "/api/shifts/open", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			ShiftID string `json:"shift_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.ShiftID == "" {
		t.Fatalf("open response has no shift_id: %s (%v)", rec.Body.String(), err)
	}
	return env.Data.ShiftID
}

func TestShiftOpen_PublishesOneEventAfterCommit_ExactPayload(t *testing.T) {
	mux, dp := newShiftsAPITestDeps(t)
	seedShiftRegister(t, dp, "reg1")
	ch := subscribeShiftEvents(t, dp, true)

	id := openShiftOK(t, mux, `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":5000}`)
	raw := recvShiftEvent(t, ch, shiftOpenedEvent)
	assertNoShiftEvent(t, ch)

	if len(raw) != 6 {
		t.Fatalf("payload keys = %v, want exactly 6", raw)
	}
	var opened string
	if err := dp.Db.QueryRow(`SELECT opened_at FROM shifts WHERE id = ?`, id).Scan(&opened); err != nil {
		t.Fatalf("row not found: %v", err)
	}
	if raw["shift_id"] != id || raw["register_id"] != "reg1" || raw["cashier_id"] != "cashier1" ||
		raw["opened_at"] != opened || raw["currency"] != dp.CurrentState().Currency {
		t.Fatalf("unexpected payload %v (id %s, row opened_at %q)", raw, id, opened)
	}
	if f, ok := raw["opening_cash"].(float64); !ok || f != 5000 {
		t.Fatalf("opening_cash = %v, want number 5000", raw["opening_cash"])
	}
	if _, err := time.Parse(time.RFC3339, opened); err != nil {
		t.Fatalf("opened_at not RFC 3339: %v", err)
	}
}

func TestShiftClose_PublishesOneEventAfterCommit_ExactPayload(t *testing.T) {
	for _, tc := range []struct {
		name         string
		closing, skm int64
		wantExpected int64
	}{
		{"no skim", 9500, 0, 12500},
		{"with skim", 51110, 41110, 12500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UT_AUTH", "off")
			mux, dp := newShiftsAPITestDeps(t)
			seedShiftRegister(t, dp, "reg1")
			ch := subscribeShiftEvents(t, dp, true)

			id := openShiftOK(t, mux, `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":10000}`)
			recvShiftEvent(t, ch, shiftOpenedEvent)
			// Tester: backdate the open and top up the float so opened_at !=
			// closed_at and expected_cash != opening_cash; otherwise a payload
			// that swapped those fields would still match the row.
			if _, err := dp.Db.Exec(`UPDATE shifts SET opened_at = '2001-01-01T08:00:00Z' WHERE id = ?`, id); err != nil { // fixed-date:ok far past, no clock check
				t.Fatal(err)
			}
			if _, err := pos.RecordCashAdjustment(context.Background(), dp.Db, pos.CashAdjustmentInput{
				ShiftID: id, Type: "adjustment", Amount: 2500, Reason: "float top-up", ActorID: "cashier1",
			}); err != nil {
				t.Fatal(err)
			}

			body := `{"shift_id":"` + id + `","closing_cash":` + strconv.FormatInt(tc.closing, 10) + `,"skim":` + strconv.FormatInt(tc.skm, 10) +
				`,"note":"secret note","skim_reason":"secret reason"}`
			rec := postShiftJSON(t, mux, "/api/shifts/close", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
			}
			var env struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			raw := recvShiftEvent(t, ch, shiftClosedEvent)
			assertNoShiftEvent(t, ch)

			if len(raw) != 12 {
				t.Fatalf("payload keys = %v, want exactly 12", raw)
			}
			for _, banned := range []string{"note", "skim_reason", "count_protocol"} {
				if _, has := raw[banned]; has {
					t.Fatalf("payload must never carry %q", banned)
				}
			}
			// Each figure equals the HTTP response.
			for _, k := range []string{"closing_cash", "expected_cash", "variance", "skim", "new_float"} {
				resp, present := env.Data[k] // the response omits zero skim/variance
				if !present {
					resp = float64(0)
				}
				if raw[k] != resp {
					t.Errorf("%s: event %v != response %v", k, raw[k], resp)
				}
			}
			if f, _ := raw["expected_cash"].(float64); int64(f) != tc.wantExpected {
				t.Errorf("expected_cash = %v, want %d", raw["expected_cash"], tc.wantExpected)
			}
			// And the committed row.
			var opened, closed string
			var expected int64
			if err := dp.Db.QueryRow(`SELECT opened_at, closed_at, expected_cash FROM shifts WHERE id = ?`, id).Scan(&opened, &closed, &expected); err != nil {
				t.Fatal(err)
			}
			if raw["opened_at"] != opened || raw["closed_at"] != closed || raw["expected_cash"] != float64(expected) {
				t.Fatalf("event %v disagrees with row (%q, %q, %d)", raw, opened, closed, expected)
			}
			if raw["shift_id"] != id || raw["register_id"] != "reg1" || raw["cashier_id"] != "cashier1" ||
				raw["currency"] != dp.CurrentState().Currency {
				t.Fatalf("unexpected identity fields: %v", raw)
			}
			if f, _ := raw["opening_cash"].(float64); f != 10000 {
				t.Fatalf("opening_cash = %v, want 10000", raw["opening_cash"])
			}
		})
	}
}

func TestShiftEvents_NoEventOnRefusal(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newShiftsAPITestDeps(t)
	seedShiftRegister(t, dp, "reg1")
	ch := subscribeShiftEvents(t, dp, true)

	id := openShiftOK(t, mux, `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":10000}`)
	recvShiftEvent(t, ch, shiftOpenedEvent)

	// A second open on the same register fails.
	if rec := postShiftJSON(t, mux, "/api/shifts/open", `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":1}`); rec.Code == http.StatusOK {
		t.Fatalf("second open must fail, got 200")
	}
	assertNoShiftEvent(t, ch)
	// 400 validation errors.
	if rec := postShiftJSON(t, mux, "/api/shifts/open", `{"cashier_id":"cashier1","opening_cash":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"closing_cash":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	assertNoShiftEvent(t, ch)

	// A skim without a manager PIN: 403, shift stays open.
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/close",
		strings.NewReader(`shift_id=`+id+`&closing_cash=51110&skim=41110`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", rec.Code, rec.Body.String())
	}
	assertNoShiftEvent(t, ch)

	// A real close succeeds; closing it again is a 404 with no event.
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+id+`","closing_cash":10000}`); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	recvShiftEvent(t, ch, shiftClosedEvent)
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+id+`","closing_cash":10000}`); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
	assertNoShiftEvent(t, ch)
}

func TestShiftEvents_SubscriberWithoutPermissionGetsNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	seedShiftRegister(t, dp, "reg1")
	ch := subscribeShiftEvents(t, dp, false)

	id := openShiftOK(t, mux, `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":10000}`)
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+id+`","closing_cash":10000}`); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	assertNoShiftEvent(t, ch)
}

func TestShiftEvents_NoSubscriberWritesNoEventPublishedAudit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsAPITestDeps(t)
	seedShiftRegister(t, dp, "reg1")
	plugins.SharedBus(dp.Db).ResetSubscribers()
	oldO, oldC := publishShiftOpened, publishShiftClosed
	publishShiftOpened = func(context.Context, *plugins.EventBus, plugins.ShiftOpenedPayload) (string, error) {
		t.Fatal("Publish must not be called with no subscriber")
		return "", nil
	}
	publishShiftClosed = func(context.Context, *plugins.EventBus, plugins.ShiftClosedPayload) (string, error) {
		t.Fatal("Publish must not be called with no subscriber")
		return "", nil
	}
	t.Cleanup(func() { publishShiftOpened, publishShiftClosed = oldO, oldC })

	id := openShiftOK(t, mux, `{"register_id":"reg1","cashier_id":"cashier1","opening_cash":10000}`)
	if rec := postShiftJSON(t, mux, "/api/shifts/close", `{"shift_id":"`+id+`","closing_cash":10000}`); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	if n := shiftEventPublishedRows(t, dp.Db); n != 0 {
		t.Fatalf("event_published rows = %d, want 0", n)
	}
}

func TestShiftEvents_ZeroSubscriberAllocatesNothing(t *testing.T) {
	_, dp := newShiftsAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	opened := pos.OpenedShift{ShiftID: "s", RegisterID: "r", CashierID: "u", OpenedAt: "2026-01-01T00:00:00Z"}
	closed := pos.ClosedShift{ShiftID: "s", RegisterID: "r", CashierID: "u"}
	ctx := context.Background()
	if a := testing.AllocsPerRun(100, func() { dispatchShiftOpened(ctx, dp, opened) }); a != 0 {
		t.Fatalf("dispatchShiftOpened allocs/op = %v, want 0", a)
	}
	if a := testing.AllocsPerRun(100, func() { dispatchShiftClosed(ctx, dp, closed) }); a != 0 {
		t.Fatalf("dispatchShiftClosed allocs/op = %v, want 0", a)
	}
}
