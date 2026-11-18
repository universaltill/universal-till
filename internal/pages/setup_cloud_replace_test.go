package pages

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5058 (design §3.3 T1, §4.2-§4.3): a replace code on the wizard's
// Main till path. The cloud answers 409 replace_confirm_required; the till
// shows "Replace <old>?" in place and holds nothing; Replace sends the same
// request again with replace_device_id; the "Your shop" step then names the
// replaced till and no longer offers "This is not my shop".

const replaceConfirmReply = `{"data":{"existing_till":{"device_id":"till-old","name":"Counter <b>till</b>","last_used_at":"2026-10-25T18:04:00Z"},` +
	`"store":{"name":"Cloud Cafe","country":"GB","currency":"GBP","currency_decimals":2,"catalog_master":"cloud"}},` +
	`"error":{"code":"replace_confirm_required","message":"x"}}`

const replacedAnswer = `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"merchant-1","device_id":"x","device_token":"` + pairPageToken + `",` +
	`"store":{"name":"Cloud Cafe","country":"DE","currency":"EUR","currency_decimals":2,"catalog_master":"cloud"},` +
	`"replaced":{"device_id":"till-old","name":"Counter till"}},"error":null}`

func postCloudPairForm(mux *http.ServeMux, nonce string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cloudPairReq("/api/setup/cloud-pair", nonce, form, nil))
	return rec
}

func TestCloudPairReplaceCodeShowsConfirmPanelAndHoldsNothing(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	stub.set(409, replaceConfirmReply)
	rec := doCloudPair(mux, nonce, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("code=%d redirect=%q, want an in-place panel", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	for _, want := range []string{
		`data-testid="setup-cloud-replace"`,
		`name="replace_device_id" value="till-old"`,
		`name="confirm_replace" value="1"`,
		"2026⁠-⁠10⁠-⁠25 18:04 UTC",
		"Sales made on",
		"staff names", // cloud-master: the products come back
	} {
		if !strings.Contains(html.UnescapeString(body), want) {
			t.Errorf("panel lacks %q:\n%s", want, body)
		}
	}
	// The cloud's name is escaped, never markup.
	if strings.Contains(body, "<b>till</b>") {
		t.Fatal("the old till's name was rendered as HTML")
	}
	if _, sent := stub.lastBody()["replace_device_id"]; sent {
		t.Fatal("the first, unconfirmed request already sent replace_device_id")
	}
	if page := getSetup(mux, "?lang=en", "en").Body.String(); strings.Contains(page, "step: 8") {
		t.Fatal("an unconfirmed replace held a pairing")
	}
}

func TestCloudPairReplaceTillMasterShopSaysProductsAreNotCopied(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	stub.set(409, strings.Replace(replaceConfirmReply, `"catalog_master":"cloud"`, `"catalog_master":"till"`, 1))
	body := html.UnescapeString(doCloudPair(mux, nonce, nil).Body.String())
	if !strings.Contains(body, "sample products") || strings.Contains(body, "staff names") {
		t.Fatalf("till-master panel: %s", body)
	}
}

func TestCloudPairReplaceConfirmSendsTargetAndOpensYourShopWithoutNotMine(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	stub.set(200, replacedAnswer)
	rec := postCloudPairForm(mux, nonce, url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {"till-old"}, "confirm_replace": {"1"}})
	if rec.Header().Get("HX-Redirect") != "/setup" {
		t.Fatalf("confirm: code=%d body=%s", rec.Code, rec.Body.String())
	}
	b := stub.lastBody()
	if b["replace_device_id"] != "till-old" || b["first_till"] != true {
		t.Fatalf("confirm request = %v", b)
	}
	page := getSetup(mux, "?lang=en", "en").Body.String()
	if !strings.Contains(page, "step: 8") || !strings.Contains(page, `data-testid="setup-cloud-replaced"`) || !strings.Contains(page, "Replaced: \u2068Counter till\u2069") {
		t.Fatalf("Your shop after a replace lacks the replaced note:\n%s", page)
	}
	if strings.Contains(page, "/api/setup/cloud-unpair") {
		t.Fatal(`"This is not my shop" is offered after a replace already happened`)
	}
}

func TestCloudPairOrdinaryYourShopStillOffersNotMine(t *testing.T) {
	mux, _, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	page := getSetup(mux, "?lang=en", "en").Body.String()
	if !strings.Contains(page, "/api/setup/cloud-unpair") || strings.Contains(page, `data-testid="setup-cloud-replaced"`) {
		t.Fatal("an ordinary pairing lost 'This is not my shop' or shows a replaced note")
	}
}

func TestCloudPairStaleReplaceFieldWithoutConfirmIsNotSent(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	postCloudPairForm(mux, nonce, url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {"till-old"}})
	if _, sent := stub.lastBody()["replace_device_id"]; sent {
		t.Fatal("replace_device_id travelled without the Replace button's confirm_replace=1")
	}
}

func TestCloudPairConfirmWithoutTargetNeverReachesTheCloud(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	for _, id := range []string{"", strings.Repeat("x", 201)} {
		postCloudPairForm(mux, nonce, url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {id}, "confirm_replace": {"1"}})
	}
	if stub.calls.Load() != 0 {
		t.Fatalf("a confirm with no valid target reached the cloud (%d calls)", stub.calls.Load())
	}
}

func TestCloudPairConfirmWithWrongNonceIsRefused(t *testing.T) {
	mux, _, stub, _ := cloudWizard(t)
	rec := postCloudPairForm(mux, "nope", url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {"till-old"}, "confirm_replace": {"1"}})
	if rec.Code != http.StatusForbidden || stub.calls.Load() != 0 {
		t.Fatalf("wrong nonce: code=%d calls=%d", rec.Code, stub.calls.Load())
	}
}

func TestCloudPairConfirmIgnoredByCloudDoesNotLoop(t *testing.T) {
	// A cloud before CL-2 ignores replace_device_id and answers the same 409.
	mux, _, stub, nonce := cloudWizard(t)
	stub.set(409, replaceConfirmReply)
	body := html.UnescapeString(postCloudPairForm(mux, nonce, url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {"till-old"}, "confirm_replace": {"1"}}).Body.String())
	if strings.Contains(body, "setup-cloud-replace") || !strings.Contains(body, "cannot replace a till yet") {
		t.Fatalf("an ignored confirm showed the panel again: %s", body)
	}
}

func TestCloudPairReplaceErrorsShownInPlace(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	confirm := url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {"till-old"}, "confirm_replace": {"1"}}
	for name, tc := range map[string]struct {
		reply string
		want  []string
	}{
		"online":         {`{"data":{"last_seen":"2026-10-26T10:01:00Z"},"error":{"code":"main_till_online"}}`, []string{"still connected", "2026⁠-⁠10⁠-⁠26 10:01 UTC"}},
		"target changed": {`{"data":null,"error":{"code":"replace_target_changed"}}`, []string{"made for a different till"}},
		"needs owner":    {`{"data":null,"error":{"code":"replace_needs_owner"}}`, []string{"can only add a till"}},
		"main till only": {`{"data":null,"error":{"code":"replace_code_main_till_only"}}`, []string{"replaces the shop's main till"}},
	} {
		stub.set(409, tc.reply)
		rec := postCloudPairForm(mux, nonce, confirm)
		body := html.UnescapeString(rec.Body.String())
		for _, w := range tc.want {
			if rec.Header().Get("HX-Redirect") != "" || !strings.Contains(body, w) {
				t.Errorf("%s: body=%s, want %q", name, body, w)
			}
		}
	}
}

func TestCloudPairStoreHasMainTillNamesItTwiceAndTheTime(t *testing.T) {
	mux, _, stub, nonce := cloudWizard(t)
	stub.set(409, `{"data":{"existing_till":{"name":"Front counter","last_used_at":"2026-10-20T09:30:00Z"}},"error":{"code":"store_has_till"}}`)
	body := html.UnescapeString(doCloudPair(mux, nonce, nil).Body.String())
	if strings.Count(body, "Front counter") != 2 || !strings.Contains(body, "2026⁠-⁠10⁠-⁠20 09:30 UTC") || !strings.Contains(body, "Replace this till") {
		t.Fatalf("store_has_till: %s", body)
	}
}

// Settings pairing with a replace code (the cloud's 409
// replace_code_main_till_only) says where the code belongs.
func TestSettingsPairWithAReplaceCodeSaysWhereItBelongs(t *testing.T) {
	err := fmt.Errorf("pair: %w", &enroll.RegisterHTTPError{Op: "pair", Status: 409, Code: "replace_code_main_till_only"})
	if got := enrolFailureKey(err); got != "setup.cloud.error.replace_main_till_only" {
		t.Fatalf("enrolFailureKey = %q", got)
	}
}

// ut-docs#5058 T4 (design-ut3 §3, N7): a replacement main till numbers its
// receipts under its own prefix, so it never repeats the dead till's numbers.
func TestCloudSetupAfterReplaceGivesANewReceiptPrefix(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		mux, d, stub, nonce := cloudWizard(t)
		stub.set(200, replacedAnswer)
		postCloudPairForm(mux, nonce, url.Values{"code": {pairPageCode}, "name": {"Till 1"}, "replace_device_id": {"till-old"}, "confirm_replace": {"1"}})
		if failCommit {
			prev := commitCloudPairing
			commitCloudPairing = func(context.Context, *common.Deps, *enroll.FirstTillPairing) error { return errors.New("boom") }
			t.Cleanup(func() { commitCloudPairing = prev })
		}
		finalCloudPost(mux, nonce, nil)
		got, _, _ := d.Settings.Get(t.Context(), "sync.receipt_prefix")
		if !regexp.MustCompile(`^M[0-9a-f]{4}([0-9a-f]{4})?-$`).MatchString(got) {
			t.Fatalf("failCommit=%v: sync.receipt_prefix = %q, want an M<hex>- prefix", failCommit, got)
		}
		no, err := data.NewPOSRepo(d.Db).NextReceiptNo(t.Context(), nil)
		if err != nil || !strings.HasPrefix(no, got) {
			t.Fatalf("failCommit=%v: first receipt %q (%v), want prefix %q", failCommit, no, err, got)
		}
	}
}

func TestCloudSetupWithoutReplaceKeepsNoReceiptPrefix(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	finalCloudPost(mux, nonce, nil)
	if got, _, _ := d.Settings.Get(t.Context(), "sync.receipt_prefix"); got != "" {
		t.Fatalf("sync.receipt_prefix = %q, want empty", got)
	}
}

// ut-docs#5332: "Remove, then add" - the owner removed the main till on my.
// and a new device joins with an ordinary code. The cloud says had_main_till,
// so the till numbers under its own M<8hex>- prefix (same as T4, no replaced).
func hadMainTillAnswer(flag string) string {
	return `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"merchant-1","device_id":"x","device_token":"` + pairPageToken + `",` +
		`"store":{"name":"Cloud Cafe","country":"DE","currency":"EUR","currency_decimals":2,"catalog_master":"cloud"` + flag + `}},"error":null}`
}

func TestCloudSetupAfterRemoveGivesANewReceiptPrefix(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		mux, d, stub, nonce := cloudWizard(t)
		stub.set(200, hadMainTillAnswer(`,"had_main_till":true`))
		doCloudPair(mux, nonce, nil)
		if failCommit {
			prev := commitCloudPairing
			commitCloudPairing = func(context.Context, *common.Deps, *enroll.FirstTillPairing) error { return errors.New("boom") }
			t.Cleanup(func() { commitCloudPairing = prev })
		}
		finalCloudPost(mux, nonce, nil)
		got, _, _ := d.Settings.Get(t.Context(), "sync.receipt_prefix")
		if !regexp.MustCompile(`^M[0-9a-f]{8}-$`).MatchString(got) {
			t.Fatalf("failCommit=%v: sync.receipt_prefix = %q, want M<8hex>-", failCommit, got)
		}
		no, err := data.NewPOSRepo(d.Db).NextReceiptNo(t.Context(), nil)
		if err != nil || no != got+"000000001" {
			t.Fatalf("failCommit=%v: first receipt %q (%v), want %q", failCommit, no, err, got+"000000001")
		}
	}
}

func TestCloudSetupHadMainTillFalseKeepsNoReceiptPrefix(t *testing.T) {
	for name, flag := range map[string]string{"false": `,"had_main_till":false`, "absent": ``} {
		mux, d, stub, nonce := cloudWizard(t)
		stub.set(200, hadMainTillAnswer(flag))
		doCloudPair(mux, nonce, nil)
		finalCloudPost(mux, nonce, nil)
		if got, _, _ := d.Settings.Get(t.Context(), "sync.receipt_prefix"); got != "" {
			t.Fatalf("%s: sync.receipt_prefix = %q, want empty", name, got)
		}
	}
}
