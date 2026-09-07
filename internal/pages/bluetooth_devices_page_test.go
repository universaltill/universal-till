package pages

// Bluetooth devices panel (universaltill/ut-docs#76): manager gating on all
// five routes, the page render (paired list, "not available" notice, access
// denied notice), and the four JSON endpoints — list, bounded scan, pair,
// forget — each on its success path and its BlueZ-unavailable/error path,
// with the raw D-Bus error never reaching the client (same rule as
// discoverPrimariesHandler / TestDiscoverPrintersAPI_*). Everything runs
// against fakeBluetoothClient through the newBluetoothClient seam: there is
// no Bluetooth adapter in CI, and real-hardware pairing is a documented
// Tester-side verification gap, not something this file pretends to cover.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/bluetooth"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// fakeBluetoothClient is a canned bluetooth.Client: every method returns
// what the test loaded, and records what it was asked so the test can pin
// the handler's contract (which address reached Pair, what timeout the scan
// ran with, that Close was called).
type fakeBluetoothClient struct {
	devices    []bluetooth.Device
	listErr    error
	candidates []bluetooth.Device
	scanErr    error
	pairErr    error
	forgetErr  error

	scanTimeout time.Duration
	paired      []string
	forgotten   []string
	closed      int

	// listCtxHadDeadline/scanCtxHadDeadline record whether the ctx the
	// handler passed in was itself bounded (ut-docs#1582: an unbounded
	// r.Context() on a read path lets a wedged bluetoothd park the handler
	// goroutine indefinitely — pair/forget already bound theirs, list/scan
	// didn't).
	listCtxHadDeadline bool
	scanCtxHadDeadline bool
}

func (f *fakeBluetoothClient) ListDevices(ctx context.Context) ([]bluetooth.Device, error) {
	_, f.listCtxHadDeadline = ctx.Deadline()
	return f.devices, f.listErr
}

func (f *fakeBluetoothClient) Scan(ctx context.Context, timeout time.Duration) ([]bluetooth.Device, error) {
	_, f.scanCtxHadDeadline = ctx.Deadline()
	f.scanTimeout = timeout
	return f.candidates, f.scanErr
}

func (f *fakeBluetoothClient) Pair(ctx context.Context, address string) error {
	f.paired = append(f.paired, address)
	return f.pairErr
}

func (f *fakeBluetoothClient) Forget(ctx context.Context, address string) error {
	f.forgotten = append(f.forgotten, address)
	return f.forgetErr
}

func (f *fakeBluetoothClient) Close() error {
	f.closed++
	return nil
}

// stubBluetooth swaps the newBluetoothClient seam for the fake (or for a
// constructor failure, when connectErr is set — the "no system bus on this
// box" case) for the duration of the test.
func stubBluetooth(t *testing.T, fake *fakeBluetoothClient, connectErr error) {
	t.Helper()
	orig := newBluetoothClient
	newBluetoothClient = func() (bluetooth.Client, error) {
		if connectErr != nil {
			return nil, connectErr
		}
		return fake, nil
	}
	t.Cleanup(func() { newBluetoothClient = orig })
}

func newBluetoothDevicesTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "bluetooth-devices-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	// audit_log.actor_id is a foreign key into users: the manager the
	// pair/forget tests act as has to exist for its audit row to land
	// (InsertAudit's error is deliberately swallowed by the handler, so
	// without this row the audit assertions would fail silently-by-design).
	if _, err := dbase.DB.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('m1','manager1','Manager','x','manager')`); err != nil {
		t.Fatal(err)
	}
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(dbase.DB)}
	mux := http.NewServeMux()
	registerBluetoothDevices(mux, d)
	return mux, d
}

var (
	btManager = auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	btCashier = auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
)

func btGet(mux *http.ServeMux, path string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func btPostJSON(mux *http.ServeMux, path, body string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type btEnvelope struct {
	Data struct {
		Devices []bluetooth.Device `json:"devices"`
		Address string             `json:"address"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func decodeBT(t *testing.T, rec *httptest.ResponseRecorder) btEnvelope {
	t.Helper()
	var out btEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// Same UT_AUTH=off reachability rule every sibling admin page follows
// (ut-docs#901/#902): the gate goes through canPerform, which has the
// dev/CI bypass — the docs-shots harness depends on it.
func TestBluetoothDevicesPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, &fakeBluetoothClient{}, nil)

	rec := btGet(mux, "/bluetooth-devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bluetooth-devices under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestBluetoothDevicesPermissions_AllFiveRoutesRejectCashier(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)

	if rec := btGet(mux, "/bluetooth-devices", &btCashier); rec.Code != http.StatusForbidden {
		t.Errorf("cashier GET /bluetooth-devices = %d, want 403", rec.Code)
	}
	if rec := btGet(mux, "/api/bluetooth-devices", &btCashier); rec.Code != http.StatusForbidden {
		t.Errorf("cashier list = %d, want 403", rec.Code)
	}
	if rec := btPostJSON(mux, "/api/bluetooth-devices/scan", "", &btCashier); rec.Code != http.StatusForbidden {
		t.Errorf("cashier scan = %d, want 403", rec.Code)
	}
	if rec := btPostJSON(mux, "/api/bluetooth-devices/pair", `{"address":"AA:BB:CC:DD:EE:01"}`, &btCashier); rec.Code != http.StatusForbidden {
		t.Errorf("cashier pair = %d, want 403", rec.Code)
	}
	if rec := btPostJSON(mux, "/api/bluetooth-devices/forget", `{"address":"AA:BB:CC:DD:EE:01"}`, &btCashier); rec.Code != http.StatusForbidden {
		t.Errorf("cashier forget = %d, want 403", rec.Code)
	}
	// No session at all (anonymous LAN client) is refused the same way.
	if rec := btPostJSON(mux, "/api/bluetooth-devices/scan", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("anonymous scan = %d, want 403", rec.Code)
	}
	if len(fake.paired)+len(fake.forgotten) != 0 || fake.scanTimeout != 0 {
		t.Fatalf("a refused request must never reach BlueZ: %+v", fake)
	}
}

func TestBluetoothDevicesPage_RendersPairedDevices(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{devices: []bluetooth.Device{
		{Address: "AA:BB:CC:DD:EE:01", Name: "Metum Scanner", Icon: "input-keyboard", Paired: true, Trusted: true, Connected: true},
		{Address: "AA:BB:CC:DD:EE:02", Name: "Kitchen Scale", Paired: true, Trusted: true, Connected: false},
	}}
	stubBluetooth(t, fake, nil)

	rec := btGet(mux, "/bluetooth-devices", &btManager)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bluetooth-devices = %d: %s", rec.Code, body)
	}
	for _, want := range []string{"Metum Scanner", "AA:BB:CC:DD:EE:01", "Kitchen Scale", "AA:BB:CC:DD:EE:02", `id="bt-scan-btn"`, `data-testid="bt-paired-list"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `data-testid="bluetooth-unavailable"`) {
		t.Error("a working adapter must not show the unavailable notice")
	}
	if fake.closed != 1 {
		t.Errorf("client must be closed after the page render, closed=%d", fake.closed)
	}
}

func TestBluetoothDevicesPage_NoBluetoothDegradesToNoticeNotError(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	// No system bus / no bluetoothd / no adapter: the page still renders
	// (200, full layout, scan disabled) with a status notice — never a
	// bare error, and never anything that could block a kiosk flow.
	stubBluetooth(t, nil, bluetooth.ErrUnavailable)

	rec := btGet(mux, "/bluetooth-devices", &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bluetooth-devices with no Bluetooth = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-testid="bluetooth-unavailable"`) {
		t.Fatal("expected the unavailable notice")
	}
}

func TestBluetoothDevicesPage_UnsupportedPlatformDoesNotBlameHardware(t *testing.T) {
	// Android (ut-docs#1643): the adapter can be present and healthy, but
	// there is no D-Bus/BlueZ transport. The page must show a distinct
	// notice that does not claim the adapter is missing or the service
	// isn't running — that would misdirect an operator toward a hardware
	// fix that cannot help.
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, nil, bluetooth.ErrUnsupportedPlatform)

	rec := btGet(mux, "/bluetooth-devices", &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bluetooth-devices on an unsupported platform = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="bluetooth-unsupported"`) {
		t.Fatal("expected the platform-unsupported notice")
	}
	if strings.Contains(body, `data-testid="bluetooth-unavailable"`) {
		t.Error("must not also show the hardware-missing notice")
	}
}

func TestBluetoothDevicesPage_AccessDeniedShowsPackagingHint(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	// The ADR-0078 policy file is missing: distinct notice, since this is
	// fixable by reinstalling the package, unlike a box with no adapter.
	stubBluetooth(t, &fakeBluetoothClient{listErr: bluetooth.ErrAccessDenied}, nil)

	rec := btGet(mux, "/bluetooth-devices", &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bluetooth-devices with access denied = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-testid="bluetooth-access-denied"`) {
		t.Fatal("expected the access-denied notice")
	}
}

func TestBluetoothDevicesPage_ListErrorRendersInlineWithoutLeakingIt(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, &fakeBluetoothClient{listErr: errors.New("org.bluez.Error.Failed: hci0: Input/output error (5)")}, nil)

	rec := btGet(mux, "/bluetooth-devices", &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bluetooth-devices on a list error = %d, want 200 with an inline error", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Input/output error") {
		t.Fatal("raw D-Bus error leaked into the page")
	}
	if !strings.Contains(rec.Body.String(), `class="login-error"`) {
		t.Fatal("expected the inline error line")
	}
}

func TestBluetoothListAPI_ReturnsEnvelopeAndEmptyArrayNotNull(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{devices: []bluetooth.Device{{Address: "AA:BB:CC:DD:EE:01", Name: "Metum Scanner", Paired: true, Connected: true}}}
	stubBluetooth(t, fake, nil)

	rec := btGet(mux, "/api/bluetooth-devices", &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeBT(t, rec)
	if out.Error != nil || len(out.Data.Devices) != 1 || out.Data.Devices[0].Name != "Metum Scanner" || !out.Data.Devices[0].Connected {
		t.Fatalf("unexpected payload: %s", rec.Body.String())
	}

	fake.devices = nil
	rec = btGet(mux, "/api/bluetooth-devices", &btManager)
	if !strings.Contains(rec.Body.String(), `"devices":[]`) {
		t.Fatalf("expected an empty array, not null: %s", rec.Body.String())
	}
}

func TestBluetoothListAPI_Unavailable503WithCode(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, nil, bluetooth.ErrUnavailable)

	rec := btGet(mux, "/api/bluetooth-devices", &btManager)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list with no Bluetooth = %d, want 503", rec.Code)
	}
	if out := decodeBT(t, rec); out.Error == nil || out.Error.Code != "bluetooth_unavailable" {
		t.Fatalf("want error.code bluetooth_unavailable, got %s", rec.Body.String())
	}
}

func TestBluetoothListAPI_UnsupportedPlatform503WithDistinctCode(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, nil, bluetooth.ErrUnsupportedPlatform)

	rec := btGet(mux, "/api/bluetooth-devices", &btManager)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list on an unsupported platform = %d, want 503", rec.Code)
	}
	if out := decodeBT(t, rec); out.Error == nil || out.Error.Code != "bluetooth_unsupported_platform" {
		t.Fatalf("want error.code bluetooth_unsupported_platform, got %s", rec.Body.String())
	}
}

func TestBluetoothScanAPI_ReturnsCandidatesWithBoundedTimeout(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{candidates: []bluetooth.Device{{Address: "AA:BB:CC:DD:EE:09", Name: "New Scanner", Icon: "input-keyboard"}}}
	stubBluetooth(t, fake, nil)

	rec := btPostJSON(mux, "/api/bluetooth-devices/scan", "", &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeBT(t, rec)
	if out.Error != nil || len(out.Data.Devices) != 1 || out.Data.Devices[0].Address != "AA:BB:CC:DD:EE:09" {
		t.Fatalf("unexpected payload: %s", rec.Body.String())
	}
	if fake.scanTimeout != discoverBluetoothTimeout {
		t.Fatalf("scan must run with the bounded per-click timeout %v, got %v", discoverBluetoothTimeout, fake.scanTimeout)
	}
	if !fake.scanCtxHadDeadline {
		t.Error("scan must bound its ctx with a deadline, not pass r.Context() through unbounded (ut-docs#1582)")
	}
	if fake.closed != 1 {
		t.Errorf("client must be closed after the scan, closed=%d", fake.closed)
	}

	fake.candidates = nil
	rec = btPostJSON(mux, "/api/bluetooth-devices/scan", "", &btManager)
	if !strings.Contains(rec.Body.String(), `"devices":[]`) {
		t.Fatalf("expected an empty array, not null: %s", rec.Body.String())
	}
}

func TestBluetoothScanAPI_FailureReturns500WithoutLeakingRawError(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, &fakeBluetoothClient{scanErr: errors.New("org.bluez.Error.NotReady: Resource Not Ready (hci0)")}, nil)

	rec := btPostJSON(mux, "/api/bluetooth-devices/scan", "", &btManager)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("scan failure = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hci0") || strings.Contains(rec.Body.String(), "NotReady") {
		t.Fatalf("raw D-Bus error leaked into the response: %s", rec.Body.String())
	}
	if out := decodeBT(t, rec); out.Error == nil || out.Error.Code != "bluetooth_error" {
		t.Fatalf("want error.code bluetooth_error, got %s", rec.Body.String())
	}
}

func TestBluetoothScanAPI_Unavailable503(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, &fakeBluetoothClient{scanErr: bluetooth.ErrUnavailable}, nil)

	rec := btPostJSON(mux, "/api/bluetooth-devices/scan", "", &btManager)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("scan with no adapter = %d, want 503", rec.Code)
	}
}

// ut-docs#1582 independent-review finding: a scan can power on the adapter
// and always starts a discovery session — real state mutation reachable by
// prefetch/link on a GET (SameSite=Lax). It must be POST-only now, same as
// pair/forget.
func TestBluetoothScanAPI_RejectsGET(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	stubBluetooth(t, &fakeBluetoothClient{}, nil)

	rec := btGet(mux, "/api/bluetooth-devices/scan", &btManager)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/bluetooth-devices/scan = %d, want 405 (scan mutates adapter state, must be POST)", rec.Code)
	}
}

// ut-docs#1582 independent-review finding: ListDevices is a single
// ObjectManager read with no discovery involved, but the handler still
// handed it an unbounded r.Context() — a wedged bluetoothd would park the
// goroutine indefinitely. pair/forget already bound theirs; list/the page
// render must too.
func TestBluetoothListAPI_BoundsContextWithTimeout(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)

	if rec := btGet(mux, "/api/bluetooth-devices", &btManager); rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.listCtxHadDeadline {
		t.Error("GET /api/bluetooth-devices must bound its ctx with a deadline (ut-docs#1582)")
	}

	fake.listCtxHadDeadline = false
	if rec := btGet(mux, "/bluetooth-devices", &btManager); rec.Code != http.StatusOK {
		t.Fatalf("page render = %d: %s", rec.Code, rec.Body.String())
	}
	if !fake.listCtxHadDeadline {
		t.Error("GET /bluetooth-devices must bound its ctx with a deadline (ut-docs#1582)")
	}
}

func TestBluetoothPairAPI_PairsNormalizedAddressAndAudits(t *testing.T) {
	mux, d := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)

	rec := btPostJSON(mux, "/api/bluetooth-devices/pair", `{"address":" aa:bb:cc:dd:ee:09 "}`, &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("pair = %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeBT(t, rec)
	if out.Error != nil || out.Data.Address != "AA:BB:CC:DD:EE:09" {
		t.Fatalf("unexpected payload: %s", rec.Body.String())
	}
	if len(fake.paired) != 1 || fake.paired[0] != "AA:BB:CC:DD:EE:09" {
		t.Fatalf("Pair must be called with the normalized address, got %v", fake.paired)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'bluetooth_device' AND entity_id = 'AA:BB:CC:DD:EE:09' AND action = 'bluetooth_device_pair' AND actor_id = 'm1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit row: n=%d err=%v", n, err)
	}
}

func TestBluetoothPairAPI_RejectsMalformedAddressBeforeTouchingBlueZ(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)

	for _, body := range []string{`{"address":"/org/bluez/hci0/dev_x"}`, `{"address":""}`, `{}`, `not json`, `{"address":"AA:BB:CC:DD:EE"}`} {
		rec := btPostJSON(mux, "/api/bluetooth-devices/pair", body, &btManager)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("pair with body %s = %d, want 400", body, rec.Code)
		}
	}
	if len(fake.paired) != 0 {
		t.Fatalf("a malformed address must never reach BlueZ: %v", fake.paired)
	}
}

func TestBluetoothPairAPI_ErrorPathsMapToStatusesWithoutLeaking(t *testing.T) {
	mux, d := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)
	body := `{"address":"AA:BB:CC:DD:EE:09"}`

	fake.pairErr = bluetooth.ErrNotFound
	if rec := btPostJSON(mux, "/api/bluetooth-devices/pair", body, &btManager); rec.Code != http.StatusNotFound {
		t.Errorf("pair unknown device = %d, want 404", rec.Code)
	}

	fake.pairErr = errors.Join(bluetooth.ErrPairingFailed, errors.New("bluetooth: pairing failed: org.bluez.Error.AuthenticationFailed: Authentication Failed"))
	rec := btPostJSON(mux, "/api/bluetooth-devices/pair", body, &btManager)
	if rec.Code != http.StatusConflict {
		t.Errorf("pair refused by device = %d, want 409", rec.Code)
	}
	if out := decodeBT(t, rec); out.Error == nil || out.Error.Code != "bluetooth_pairing_failed" {
		t.Errorf("want error.code bluetooth_pairing_failed, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "AuthenticationFailed") {
		t.Errorf("raw BlueZ error leaked: %s", rec.Body.String())
	}

	fake.pairErr = bluetooth.ErrAccessDenied
	if rec := btPostJSON(mux, "/api/bluetooth-devices/pair", body, &btManager); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("pair with policy missing = %d, want 503", rec.Code)
	} else if out := decodeBT(t, rec); out.Error == nil || out.Error.Code != "bluetooth_access_denied" {
		t.Errorf("want error.code bluetooth_access_denied, got %s", rec.Body.String())
	}

	fake.pairErr = errors.New("org.bluez.Error.Failed: hci0 unexpected")
	rec = btPostJSON(mux, "/api/bluetooth-devices/pair", body, &btManager)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hci0") {
		t.Errorf("generic pair failure = %d body=%s, want 500 with no raw error", rec.Code, rec.Body.String())
	}

	// None of the failures above may have written a "paired" audit row.
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'bluetooth_device'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed pairings must not be audited as pairings: n=%d err=%v", n, err)
	}
}

func TestBluetoothForgetAPI_ForgetsAndAudits(t *testing.T) {
	mux, d := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)

	rec := btPostJSON(mux, "/api/bluetooth-devices/forget", `{"address":"aa:bb:cc:dd:ee:01"}`, &btManager)
	if rec.Code != http.StatusOK {
		t.Fatalf("forget = %d: %s", rec.Code, rec.Body.String())
	}
	if len(fake.forgotten) != 1 || fake.forgotten[0] != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("Forget must be called with the normalized address, got %v", fake.forgotten)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'bluetooth_device' AND entity_id = 'AA:BB:CC:DD:EE:01' AND action = 'bluetooth_device_forget'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit row: n=%d err=%v", n, err)
	}
	if fake.closed != 1 {
		t.Errorf("client must be closed after forget, closed=%d", fake.closed)
	}
}

func TestBluetoothForgetAPI_ErrorPaths(t *testing.T) {
	mux, _ := newBluetoothDevicesTestMux(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)

	if rec := btPostJSON(mux, "/api/bluetooth-devices/forget", `{"address":"nope"}`, &btManager); rec.Code != http.StatusBadRequest {
		t.Errorf("forget malformed = %d, want 400", rec.Code)
	}
	fake.forgetErr = bluetooth.ErrNotFound
	if rec := btPostJSON(mux, "/api/bluetooth-devices/forget", `{"address":"AA:BB:CC:DD:EE:01"}`, &btManager); rec.Code != http.StatusNotFound {
		t.Errorf("forget unknown = %d, want 404", rec.Code)
	}
	fake.forgetErr = errors.New("org.bluez.Error.Failed: hci0 busy")
	rec := btPostJSON(mux, "/api/bluetooth-devices/forget", `{"address":"AA:BB:CC:DD:EE:01"}`, &btManager)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hci0") {
		t.Errorf("forget failure = %d body=%s, want 500 with no raw error", rec.Code, rec.Body.String())
	}
	stubBluetooth(t, nil, bluetooth.ErrUnavailable)
	if rec := btPostJSON(mux, "/api/bluetooth-devices/forget", `{"address":"AA:BB:CC:DD:EE:01"}`, &btManager); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("forget with no Bluetooth = %d, want 503", rec.Code)
	}
}
