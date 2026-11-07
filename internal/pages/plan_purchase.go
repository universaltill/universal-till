package pages

import (
	"net/http"
	"runtime"
)

// planPurchasePlatform is runtime.GOOS, a var only so tests can render the
// iPhone/iPad variant (ut-docs#3434): an App Store app may not offer, or
// point to, a way to buy a plan outside in-app purchase (App Review 3.1.1 /
// 3.1.3), so on iOS the till shows plan status only. It has no Renew /
// Choose-a-plan panel, no QR to the plans page and no "renew it in your
// account" wording. Same pattern as bluetoothPlatform.
var planPurchasePlatform = runtime.GOOS

// planPurchaseAllowedOn is false only for "ios" (gomobile -target=ios and
// iossimulator both build GOOS=ios). A platform, not a country or vendor,
// so core neutrality (#2848) does not apply.
func planPurchaseAllowedOn(goos string) bool { return goos != "ios" }

// planPurchaseAllowed is the one predicate every plan-purchase surface reads.
func planPurchaseAllowed() bool { return planPurchaseAllowedOn(planPurchasePlatform) }

// refusePlanPurchase answers 404 and reports true when this host may not
// offer plan purchase (iOS). Called first by the three panel endpoints,
// before any form parsing, PIN prompt, cloud check-in, audit row, QR or
// enroll.PlansURL: on iOS the feature does not exist.
func refusePlanPurchase(w http.ResponseWriter, r *http.Request) bool {
	if planPurchaseAllowed() {
		return false
	}
	http.NotFound(w, r)
	return true
}
