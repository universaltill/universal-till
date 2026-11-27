package pages

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#7099 / ADR-0220: a voucher sold on the till works in the shop only.
// Nothing the till says about a voucher may suggest a webshop or online use.
var webClaim = regexp.MustCompile(`(?i)\b(online|web ?shop|web ?store|shopify|website|e-?commerce|checkout online)\b`)

func TestVoucherTextsNoWebClaim(t *testing.T) {
	raw, err := os.ReadFile("web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var en map[string]string
	if err := json.Unmarshal(raw, &en); err != nil {
		t.Fatal(err)
	}
	const note = "pos.gift_voucher.in_store_only"
	if v := en[note]; !strings.Contains(strings.ToLower(v), "this shop only") || !strings.Contains(strings.ToLower(v), "not online") {
		t.Fatalf("%s = %q must say it works in this shop only, not online", note, v)
	}
	n := 0
	for k, v := range en {
		if !strings.Contains(strings.ToLower(k), "voucher") || k == note {
			continue
		}
		n++
		if webClaim.MatchString(v) {
			t.Errorf("voucher text %s promises web use: %q", k, v)
		}
	}
	if n < 20 {
		t.Fatalf("scanned only %d voucher keys: the filter is wrong", n)
	}
	popup, err := os.ReadFile("web/ui/partials/gift_voucher_popup.html")
	if err != nil || !strings.Contains(string(popup), `T "`+note+`"`) {
		t.Fatalf("the gift voucher popup must show %s (err %v)", note, err)
	}
}
