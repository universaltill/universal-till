package pages

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/credheal"
)

// ADR-0222 §4.7 / AC12: the heal engine's states are plain words in the status
// bar, each with at most one link, and "check the internet" never shows for a
// refused credential.

func pinHealStatus(t *testing.T, st credheal.Status) {
	t.Helper()
	orig := healChipStatus
	healChipStatus = func() credheal.Status { return st }
	t.Cleanup(func() { healChipStatus = orig })
}

func TestCloudHealChip_EachStateRendersItsWordsAndExactlyOneButton(t *testing.T) {
	soon := time.Now().Add(5 * time.Hour)
	const codeHref = "/settings?reconnect=1#pair-details"
	for _, c := range []struct {
		name     string
		st       credheal.Status
		claimed  bool
		short    string // the chip's visible words (ux-notes 2, 3)
		sentence string // the chip's title, HTML-escaped
		button   string // "" = no button
		href     string // where the one link goes
	}{
		{"healing", credheal.Status{State: credheal.StateHealing, Reason: credheal.ReasonReconnecting}, true,
			"Reconnecting…", "This till is reconnecting to your shop. Selling works as normal. Nothing needs doing.", "", "/tills?tab=this"},
		{"waiting", credheal.Status{State: credheal.StateWaiting, Reason: credheal.ReasonKeyQuarantined, Until: soon}, true,
			"Reconnects ", "This till reconnects to your shop by itself on ", "", "/tills?tab=this"},
		{"cannot prove, owned", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonCannotProve}, true,
			"Needs a code to reconnect", "because it can&#39;t prove who it is. Selling works as normal. To reconnect it: on my.universaltill.com open Tills, choose this till, then Make a code, and type the code here.", "Reconnect with a code", codeHref},
		{"cannot prove, ownerless", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonCannotProve}, false,
			"Can&#39;t reconnect by itself", "its shop has no online account to get a code from.", "Register as a new shop", "/settings#registration"},
		{"removed", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonRemoved}, true,
			"Removed from your shop", "This till was removed from your shop&#39;s account. Its sales are saved on this till, and it can still sell. To connect it again, ask the shop owner for a code and type it here.", "Enter a code", codeHref},
		{"replaced", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonReplaced, Detail: "Back till"}, true,
			"Replaced by Back till", "Back till replaced this till and is now the main till of your shop. This till can still sell, but its sales no longer reach your account. To keep using it, join it to Back till as an additional till.", "Join as an additional till", "/tills?tab=add&amp;need=join"},
		{"replaced, no name", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonReplaced}, true,
			"This till was replaced", "Another till replaced this one and is now the main till of your shop.", "Join as an additional till", "/tills?tab=add&amp;need=join"},
		{"moved", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonMoved, Detail: "store-4d5df8a3"}, true,
			"Moved to another shop", "This till now belongs to another shop. Its connection to the old shop has ended.", "Open Till registration", "/settings#registration"},
		{"shop deleted", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonStoreDeleted}, true,
			"Shop deleted from its account", "The owner can restore the shop on my.universaltill.com. This till then reconnects by itself within a day.", "", "/tills?tab=this"},
		{"role conflict", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonRoleConflict}, true,
			"Another till is the main till", "so this till can&#39;t reconnect as the main till.", "Join as an additional till", "/tills?tab=add&amp;need=join"},
		{"in use elsewhere", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonInUseElsewhere}, true,
			"In use on two devices", "Set the other one up again as a new additional till.", "Open Till registration", codeHref},
	} {
		t.Run(c.name, func(t *testing.T) {
			mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
			pinHealStatus(t, c.st)
			if c.claimed {
				claimStore(t, d)
			}
			body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
			if !strings.Contains(body, `data-cloud-auth="heal"`) {
				t.Fatalf("not a heal chip: %s", body)
			}
			if !strings.Contains(body, `title="`) || !strings.Contains(body, c.sentence) {
				t.Fatalf("title lacks the sentence %q: %s", c.sentence, body)
			}
			// ONE link: the pill itself, carrying the short text and the button label.
			if n := strings.Count(body, "<a "); n != 1 {
				t.Fatalf("%d links, want exactly 1 (the pill is the one control): %s", n, body)
			}
			if !strings.Contains(body, `href="`+c.href+`"`) {
				t.Fatalf("want the link to %s: %s", c.href, body)
			}
			if !strings.Contains(body, `>`+c.short) {
				t.Fatalf("visible text must start with %q: %s", c.short, body)
			}
			if c.button != "" {
				if !strings.Contains(body, `data-testid="sb-cloud-heal-action">`+c.button+`<`) {
					t.Fatalf("button label %q missing: %s", c.button, body)
				}
			} else if strings.Contains(body, "sb-cloud-heal-action") {
				t.Fatalf("this state has no button: %s", body)
			}
			if strings.Contains(body, "internet") {
				t.Fatalf("a refused credential must never say 'internet': %s", body)
			}
			// A cashier gets the words and the ask-a-manager line, never a link.
			cash := getAs(mux, "/ui/cloud-auth-chip", &cashUser).Body.String()
			if strings.Contains(cash, "href=") || strings.Contains(cash, "sb-cloud-heal-action") {
				t.Fatalf("cashier must not get a link: %s", cash)
			}
			if !strings.Contains(cash, "Ask a manager to open Settings.") || !strings.Contains(cash, c.sentence) {
				t.Fatalf("cashier title = sentence + ask-a-manager: %s", cash)
			}
		})
	}
}

// pair_again keeps today's chips; ok shows nothing from the engine.
func TestCloudHealChip_OkAndPairAgainStayOut(t *testing.T) {
	for _, st := range []credheal.Status{
		{State: credheal.StateOK},
		{State: credheal.StateNeedsPerson, Reason: credheal.ReasonPairAgain},
	} {
		mux, _ := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
		pinHealStatus(t, st)
		rec := getAs(mux, "/ui/cloud-auth-chip", &mgrUser)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
			t.Fatalf("state %+v: body %q, want an empty 200", st, rec.Body.String())
		}
	}
}

// AC12: the first refused call (before the third) already shows the chip, and
// the "check the internet connection" chip steps aside.
func TestCloudHealChip_ShowsFromTheFirstRefusedCall(t *testing.T) {
	mux, _ := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: false}) // fewer than 3 401s
	pinHealStatus(t, credheal.Status{State: credheal.StateHealing, Reason: credheal.ReasonReconnecting})
	body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
	if !strings.Contains(body, "Reconnecting…") {
		t.Fatalf("no heal chip after one 401: %q", body)
	}
}
