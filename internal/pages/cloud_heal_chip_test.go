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

func TestCloudHealChip_EachStateRendersItsWordsAndAtMostOneLink(t *testing.T) {
	soon := time.Now().Add(5 * time.Hour)
	for _, c := range []struct {
		name    string
		st      credheal.Status
		claimed bool
		want    string
		link    string // "" = no link
	}{
		{"healing", credheal.Status{State: credheal.StateHealing, Reason: credheal.ReasonReconnecting}, true, "Reconnecting this till to your shop. Selling works as normal.", ""},
		{"waiting", credheal.Status{State: credheal.StateWaiting, Reason: credheal.ReasonKeyQuarantined, Until: soon}, true, "This till reconnects by itself on ", ""},
		{"cannot prove, owned", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonCannotProve}, true, "This till can&#39;t prove who it is. Get a code on my.universaltill.com to reconnect it.", "/tills?tab=this"},
		{"cannot prove, ownerless", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonCannotProve}, false, "and its shop has no account.", "/settings#registration"},
		{"removed", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonRemoved}, true, "This till was removed from your shop&#39;s account.", "/tills?tab=this"},
		{"replaced", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonReplaced, Detail: "Back till"}, true, "This till was replaced by Back till.", "/tills?tab=add"},
		{"replaced, no name", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonReplaced}, true, "replaced by the new main till.", "/tills?tab=add"},
		{"moved", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonMoved, Detail: "Other shop"}, true, "This till now belongs to Other shop.", "/settings#registration"},
		{"shop deleted", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonStoreDeleted}, true, "This shop was deleted from its account.", ""},
		{"role conflict", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonRoleConflict}, true, "Another till is the main till now.", "/tills?tab=add"},
		{"in use elsewhere", credheal.Status{State: credheal.StateNeedsPerson, Reason: credheal.ReasonInUseElsewhere}, true, "in use on two devices", "/settings#registration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
			pinHealStatus(t, c.st)
			if c.claimed {
				claimStore(t, d)
			}
			body := getAs(mux, "/ui/cloud-auth-chip", &mgrUser).Body.String()
			if !strings.Contains(body, `data-cloud-auth="heal"`) || !strings.Contains(body, c.want) {
				t.Fatalf("body = %s\nwant a heal chip with %q", body, c.want)
			}
			links := strings.Count(body, "href=")
			wantLinks := 1 // the chip itself links to Settings for a manager
			if c.link != "" {
				wantLinks = 2
				if !strings.Contains(body, `href="`+c.link+`"`) {
					t.Fatalf("no link to %s in %s", c.link, body)
				}
			}
			if links != wantLinks {
				t.Fatalf("%d links in %s, want %d (the chip plus at most one action)", links, body, wantLinks)
			}
			if strings.Contains(body, "internet") {
				t.Fatalf("a refused credential must never say 'internet': %s", body)
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
	if !strings.Contains(body, "Reconnecting this till to your shop") {
		t.Fatalf("no heal chip after one 401: %q", body)
	}
}
