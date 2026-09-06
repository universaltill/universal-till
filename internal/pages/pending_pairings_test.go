package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// --- GET /ui/tills/pending-pairings: the primary-side approve/deny card
// list (ADR-0033 part 3/3, ut-docs#185). ---

func TestPendingPairingsUI_RequiresManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPendingPairingsUI_ListsPendingWithMatchingVerificationCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	postPairRequest(t, mux, "Kitchen Till", commitOf("ui-secret-1"), "10.0.0.30:1234")

	req := httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Kitchen Till") {
		t.Fatalf("expected the pending request's device name in the rendered list, got: %s", body)
	}

	// Cross-check against the JSON API's OWN reported code (not a value
	// recomputed locally from derivedVerificationCode, which would pass
	// even if this partial's handler called a different/broken derivation
	// — the point is confirming the partial matches what the JSON endpoint,
	// and thus a real replica, actually sees).
	jreq := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	jrec := httptest.NewRecorder()
	mux.ServeHTTP(jrec, jreq)
	var jout struct {
		Data struct {
			Pending []struct {
				VerificationCode string `json:"verification_code"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal(jrec.Body.Bytes(), &jout); err != nil {
		t.Fatal(err)
	}
	if len(jout.Data.Pending) != 1 {
		t.Fatalf("expected one pending request from the JSON API, got %+v", jout.Data.Pending)
	}
	wantCode := jout.Data.Pending[0].VerificationCode
	if !strings.Contains(body, wantCode) {
		t.Fatalf("expected verification code %q (from the JSON API) in the rendered list, got: %s", wantCode, body)
	}
}

func TestPendingPairingsUI_EmptyStateWhenNonePending(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Checking only the status code can't distinguish a real empty-state
	// render from a blank body with the poll trigger silently dropped
	// (which would leave the card frozen, never refreshing again) — assert
	// both the empty-state copy and that polling continues.
	// Whether i18n renders the translated copy or the raw key depends on
	// process-global state another test in this package may have already
	// initialized (config.I18n is a package-level singleton) — order-
	// dependent either way, so assert on structure instead: the empty
	// branch never renders a <table>, only the non-empty branch does.
	body := rec.Body.String()
	if strings.Contains(body, "<table") {
		t.Fatalf("expected the empty-state branch (no table) when nothing is pending, got: %s", body)
	}
	if !strings.Contains(body, `hx-trigger="every 30s"`) {
		t.Fatalf("expected the list to keep polling even when empty, got: %s", body)
	}
}

// TestPendingPairingsUI_RendersWrongPINFeedbackWiring locks in the fix for a
// real UX gap independent review found: the approve/deny mini-forms use
// hx-swap="none" with no error target, so a wrong manager PIN (403) left
// the manager with zero visible feedback (htmx doesn't swap non-2xx
// responses). This can't be tested via the approve/deny endpoint's own
// response (that's correctly unchanged, still a 403) — it's the FORM
// markup that must carry an hx-on::after-request handler wired to a
// visible error element.
func TestPendingPairingsUI_RendersWrongPINFeedbackWiring(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	postPairRequest(t, mux, "Kitchen Till", commitOf("pin-feedback-secret"), "10.0.0.40:1234")

	req := httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hx-on::after-request") {
		t.Fatalf("expected a wrong-PIN feedback handler wired on the approve/deny forms, got: %s", body)
	}
	if !strings.Contains(body, "hidden") {
		t.Fatalf("expected a hidden-by-default error element the handler can reveal, got: %s", body)
	}
}

// --- GET /ui/pairing-notice: the nav-level dismissible notice (ut-docs#1551),
// mounted by base.html on every page so a manager sees a pending pairing
// request without navigating to /tills and refreshing. ---

func TestPairingNoticeUI_RequiresManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/pairing-notice", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// Same "silent empty" convention as GET /ui/bugreport-chip and the
	// other nav.html/base.html placeholder fragments — a caller without
	// permission gets 200 + nothing, never a 403 splashed on every page.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without a manager session, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body without a manager session, got: %s", rec.Body.String())
	}
}

func TestPairingNoticeUI_EmptyWhenNothingPending(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pairing-notice", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body when nothing is pending, got: %s", rec.Body.String())
	}
}

func TestPairingNoticeUI_RendersCountAndFingerprintWhenPending(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	postPairRequest(t, mux, "Kitchen Till", commitOf("notice-secret-1"), "10.0.0.60:1234")
	postPairRequest(t, mux, "Bar Till", commitOf("notice-secret-2"), "10.0.0.61:1234")

	req := httptest.NewRequest(http.MethodGet, "/ui/pairing-notice", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="pairing-notice"`) {
		t.Fatalf("expected the notice element, got: %s", body)
	}
	if !strings.Contains(body, "2") {
		t.Fatalf("expected the pending count (2) rendered, got: %s", body)
	}
	// The dismiss script keys off this attribute to decide whether the
	// operator already dismissed THIS exact set of pending requests — it
	// must actually be populated, not an empty string, or every render
	// would compare equal and the notice would never re-show after a
	// dismiss+new-request.
	if !strings.Contains(body, `data-fingerprint="`) || strings.Contains(body, `data-fingerprint=""`) {
		t.Fatalf("expected a non-empty data-fingerprint, got: %s", body)
	}
	if !strings.Contains(body, `href="/tills"`) {
		t.Fatalf("expected a link to /tills, got: %s", body)
	}
}

func TestPairingNoticeUI_EmptyOnReplicaEvenWithLocalPendingRows(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	postPairRequest(t, mux, "Kitchen Till", commitOf("notice-secret-3"), "10.0.0.62:1234")

	// Pairing approval only ever happens on the primary — a till that is
	// itself a replica must never show this notice, regardless of what
	// PairingRepo happens to hold locally.
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://10.0.0.1:8080"); err != nil {
		t.Fatalf("set sync.primary_url: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/pairing-notice", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body on a replica till, got: %s", rec.Body.String())
	}
}

// TestPairingNoticeMount_KeepsPollingAndUsesADistinctID locks in the fix
// for a real bug an independent review caught, invisible to every handler-
// level test above: base.html's placeholder for GET /ui/pairing-notice
// originally carried hx-swap="outerHTML". htmx's outerHTML swap REPLACES
// the element carrying the hx-trigger, so the very first poll that found
// nothing pending (the empty-body branch, exactly like
// TestPairingNoticeUI_EmptyWhenNothingPending above) destroyed the
// placeholder along with its own polling trigger — verified in a real
// browser to permanently stop polling after exactly one request, in both
// directions: a request arriving after that first empty poll never showed
// the notice at all, and a request already pending at load never cleared
// once resolved (the literal "must clear itself... never a stale notice"
// acceptance criterion, failing). This is a markup-only regression no
// handler test can see, so it asserts on base.html's actual source rather
// than on any rendered response.
func TestPairingNoticeMount_KeepsPollingAndUsesADistinctID(t *testing.T) {
	chdirRoot(t)
	b, err := os.ReadFile("web/ui/layouts/base.html")
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	body := string(b)
	i := strings.Index(body, `hx-get="/ui/pairing-notice"`)
	if i < 0 {
		t.Fatalf("expected the pairing-notice placeholder to still exist in base.html")
	}
	// The whole placeholder <div ...> tag, not the full file, so a
	// coincidental hx-swap="outerHTML" written on some unrelated element
	// elsewhere in the file can't false-pass or false-fail this.
	start := strings.LastIndex(body[:i], "<div")
	end := strings.Index(body[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("could not isolate the placeholder <div> tag")
	}
	tag := body[start : i+end+1]
	if strings.Contains(tag, `hx-swap="outerHTML"`) {
		t.Fatalf("the pairing-notice placeholder must NOT use hx-swap=\"outerHTML\" — an empty poll response would destroy the element along with its own hx-trigger, permanently stopping all future polling. Got: %s", tag)
	}
	if !strings.Contains(tag, `hx-trigger="load, every 30s"`) {
		t.Fatalf("expected the placeholder to keep polling every 30s, got: %s", tag)
	}
	// The rendered partial's own root also uses id="pairing-notice"
	// (pairing_notice.html) -- with the default innerHTML swap the two
	// coexist as nested elements, so they must NOT share an id: the
	// partial's own dismiss script resolves getElementById("pairing-
	// notice"), and a duplicate id would make that ambiguous, at best
	// resolving to this placeholder instead of the real banner (which
	// carries no data-fingerprint), breaking the dismiss/re-show logic
	// entirely.
	if strings.Contains(tag, `id="pairing-notice"`) {
		t.Fatalf("the placeholder must use a DIFFERENT id from pairing_notice.html's own root element (\"pairing-notice\"), got: %s", tag)
	}
}

// --- Additive HX-Refresh header on the existing approve/deny handlers
// (#184) — must not change their JSON contract, only add a header on
// success. ---

func TestApprovePairRequest_SetsHXRefreshOnSuccess(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "HX Till", commitOf("hx-secret-1"), "10.0.0.31:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 approving, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh: true on a successful approve, got %q", rec.Header().Get("HX-Refresh"))
	}
}

func TestDenyPairRequest_SetsHXRefreshOnSuccess(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "HX Till 2", commitOf("hx-secret-2"), "10.0.0.32:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/deny", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 denying, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh: true on a successful deny, got %q", rec.Header().Get("HX-Refresh"))
	}
}

func TestApprovePairRequest_NoHXRefreshOnPINFailure(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "HX Till 3", commitOf("hx-secret-3"), "10.0.0.33:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") == "true" {
		t.Fatal("must not set HX-Refresh on a failed approve — that would wipe the PIN-required error from view")
	}
}

// TestPendingPairingsUI_PINFieldsBoundToTheirOwnDeviceAndShowBusyFeedback
// guards ut-docs#1540's remaining two UI defects on this list: with two
// pending requests, each row's manager-PIN field must be visibly
// associated with its own device (not read as one form wanting two PINs),
// and pressing approve/deny must give some in-flight feedback rather than
// leaving the operator looking at a button that appears to do nothing
// (same hx-indicator/hx-disabled-elt pattern as import.html's ut-docs#1510
// fix).
func TestPendingPairingsUI_PINFieldsBoundToTheirOwnDeviceAndShowBusyFeedback(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	postPairRequest(t, mux, "Kitchen Till", commitOf("bind-secret-1"), "10.0.0.41:1234")
	postPairRequest(t, mux, "Bar Till", commitOf("bind-secret-2"), "10.0.0.42:1234")

	req := httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Assert the placeholder specifically, not just "the device name appears
	// somewhere": the aria-label alone would satisfy a looser check, and the
	// point is that a SIGHTED operator can tell the two rows' PIN boxes
	// apart. The fixed half leads, so a long device name (or a long locale)
	// clips the name rather than the field's purpose.
	pin := httpx.T("en", "tills.pairing.manager_pin")
	for _, device := range []string{"Kitchen Till", "Bar Till"} {
		if !strings.Contains(body, `placeholder="`+pin+` — `+device+`"`) {
			t.Fatalf("expected the PIN field for %q to be visibly labelled with its own device name, got: %s", device, body)
		}
		if !strings.Contains(body, `aria-label="`+pin+` — `+device+`"`) {
			t.Fatalf("expected the PIN field for %q to carry a matching aria-label, got: %s", device, body)
		}
	}
	if !strings.Contains(body, "hx-indicator=") || !strings.Contains(body, "hx-disabled-elt=") {
		t.Fatalf("expected approve/deny to show in-flight busy feedback (hx-indicator + hx-disabled-elt), got: %s", body)
	}
}

// ut-docs#1548: before this fix, Approve and Deny were two separate <form>s
// each carrying its own manager_pin input — with two pending requests that
// was FOUR boxes on screen, and an operator who filled one then pressed the
// other button submitted an empty PIN. Assert exactly ONE manager_pin input
// per pending row, both forms referencing it via hx-include rather than
// each declaring their own.
func TestPendingPairingsUI_OneManagerPINInputPerRequest(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)

	postPairRequest(t, mux, "Kitchen Till", commitOf("dedup-secret-1"), "10.0.0.51:1234")
	postPairRequest(t, mux, "Bar Till", commitOf("dedup-secret-2"), "10.0.0.52:1234")

	req := httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	gotPINInputs := strings.Count(body, `name="manager_pin"`)
	if gotPINInputs != 2 {
		t.Fatalf("expected exactly 1 manager_pin input per pending request (2 requests -> 2 inputs), got %d in: %s",
			gotPINInputs, body)
	}
	// Both forms must pull the PIN via hx-include (an id-anchored selector,
	// one per request) rather than each declaring its own input.
	gotIncludes := strings.Count(body, `hx-include="#pin-`)
	if gotIncludes != 4 { // 2 requests x (approve form + deny form)
		t.Fatalf("expected approve AND deny forms to hx-include the shared PIN input for both requests (4 total), got %d in: %s",
			gotIncludes, body)
	}
}
