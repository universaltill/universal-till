package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0217 / ux-notes rule 6: one sentence per reason, the same words and the
// same single button on Tills > This till, Settings > Till registration and
// Settings > Backups as on the chip.

func needsPerson(reason string) credheal.Status {
	return credheal.Status{State: credheal.StateNeedsPerson, Reason: reason}
}

func TestTillsThis_HealBannerHasTheSentenceAndOneButton(t *testing.T) {
	for _, c := range []struct {
		name, reason, sentence, button, href string
	}{
		{"cannot prove", credheal.ReasonCannotProve, "because it can&#39;t prove who it is.", "Reconnect with a code", "/settings?reconnect=1#pair-details"},
		{"removed", credheal.ReasonRemoved, "ask the shop owner for a code", "Enter a code", "/settings?reconnect=1#pair-details"},
		{"replaced", credheal.ReasonReplaced, "Another till replaced this one", "Join as an additional till", "/tills?tab=add&amp;need=join"},
		{"role conflict", credheal.ReasonRoleConflict, "so this till can&#39;t reconnect as the main till.", "Join as an additional till", "/tills?tab=add&amp;need=join"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mux, d := tillsTabsMux(t, nil, nil)
			claimStore(t, d)
			pinHealStatus(t, needsPerson(c.reason))
			body := tabsGet(mux, "/tills?tab=this").Body.String()
			i := strings.Index(body, `data-testid="tills-heal-banner"`)
			if i < 0 {
				t.Fatalf("no heal banner: %s", body)
			}
			banner := body[i:]
			if j := strings.Index(banner, "</div>"); j > 0 {
				banner = banner[:j]
			}
			if !strings.Contains(banner, c.sentence) {
				t.Errorf("banner lacks %q: %s", c.sentence, banner)
			}
			if n := strings.Count(banner, "<a "); n != 1 || !strings.Contains(banner, `href="`+c.href+`"`) || !strings.Contains(banner, ">"+c.button+"<") {
				t.Errorf("want exactly one button %q to %s, got %d links: %s", c.button, c.href, n, banner)
			}
			if strings.Contains(body, `data-testid="tills-replaced-banner"`) {
				t.Errorf("the old replaced banner must not show beside the heal banner (one wording, one button)")
			}
		})
	}
}

func TestTillsThis_HealingAndWaitingHaveNoBannerNoButton(t *testing.T) {
	for _, st := range []credheal.Status{
		{State: credheal.StateHealing, Reason: credheal.ReasonReconnecting},
		{State: credheal.StateWaiting, Reason: credheal.ReasonKeyQuarantined, Until: time.Now().Add(3 * time.Hour)},
	} {
		mux, _ := tillsTabsMux(t, nil, nil)
		pinHealStatus(t, st)
		body := tabsGet(mux, "/tills?tab=this").Body.String()
		if strings.Contains(body, `data-testid="tills-heal-banner"`) || strings.Contains(body, "Reconnect with a code") {
			t.Fatalf("%s: a self-healing till shows no banner or button: %s", st.State, body)
		}
		if !strings.Contains(body, "Nothing needs doing.") {
			t.Fatalf("%s: the status sentence must say nothing needs doing: %s", st.State, body)
		}
	}
}

func TestTillsThis_SaysWhenItReconnectedByItself(t *testing.T) {
	mux, _ := tillsTabsMux(t, nil, nil)
	pinHealStatus(t, credheal.Status{State: credheal.StateOK, LastHealedAt: time.Now().Add(-2 * time.Hour)})
	body := tabsGet(mux, "/tills?tab=this").Body.String()
	if !strings.Contains(body, `data-testid="tills-last-reconnected"`) || !strings.Contains(body, "Reconnected by itself on ") {
		t.Fatalf("no history line: %s", body)
	}
	pinHealStatus(t, credheal.Status{State: credheal.StateOK})
	if body := tabsGet(mux, "/tills?tab=this").Body.String(); strings.Contains(body, "Reconnected by itself") {
		t.Fatalf("history line without a heal: %s", body)
	}
}

func TestBackupsCard_SaysReconnectingByItselfVersusAPersonStep(t *testing.T) {
	for _, c := range []struct {
		name    string
		st      credheal.Status
		owned   bool
		want    string
		link    bool
		notWant string
	}{
		{"healing", credheal.Status{State: credheal.StateHealing, Reason: credheal.ReasonReconnecting}, true, "Copies to your account are paused while this till reconnects. They start again by themselves.", false, "Reconnect it with a code"},
		{"cannot prove", needsPerson(credheal.ReasonCannotProve), true, "Reconnect it with a code.", true, "paused while"},
		{"ownerless", needsPerson(credheal.ReasonCannotProve), false, "Reconnect it in Settings &gt; Till registration.", true, "paused while"},
		{"shop deleted", needsPerson(credheal.ReasonStoreDeleted), true, "This shop was deleted from its online account", false, "Reconnect it"},
		{"in use", needsPerson(credheal.ReasonInUseElsewhere), true, "in use on two devices", true, "paused while"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mux, d, _ := offsiteMux(t, true)
			ctx := context.Background()
			_ = d.Settings.Set(ctx, data.OffsiteFailingSinceKey, time.Now().UTC().Format(time.RFC3339))
			if c.owned {
				_ = d.Settings.Set(ctx, entitlement.KeyStoreClaimed, "true")
			} else {
				_ = d.Settings.Set(ctx, entitlement.KeyStoreClaimed, "false")
			}
			pinHealStatus(t, c.st)
			b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
			if !strings.Contains(b, c.want) || strings.Contains(b, c.notWant) {
				t.Fatalf("card wants %q and not %q: %s", c.want, c.notWant, b)
			}
			if got := strings.Contains(b, `data-testid="backup-cloud-reason-link"`); got != c.link {
				t.Fatalf("reason link = %v, want %v: %s", got, c.link, b)
			}
			if strings.Contains(b, "Backups on this till carry on.") == false {
				t.Fatalf("must say local backups carry on: %s", b)
			}
		})
	}
}

func TestSettings_HealNoticeAndTheCodeFieldOpenFromTheButton(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	claimStore(t, d)
	pinHealStatus(t, needsPerson(credheal.ReasonCannotProve))
	plain := renderSettingsAs(t, mux, mgrUser, false)
	if !strings.Contains(plain, `data-testid="heal-notice"`) || !strings.Contains(plain, "because it can&#39;t prove who it is.") {
		t.Fatalf("no heal notice in Till registration: %s", plain)
	}
	if strings.Contains(plain, `<details class="set-divider" id="pair-details" open`) {
		t.Fatalf("the code field opens only from the button (?reconnect=1)")
	}
	rec := getAs(mux, "/settings?reconnect=1", &mgrUser)
	if !strings.Contains(rec.Body.String(), `id="pair-details" open`) {
		t.Fatalf("?reconnect=1 must render the code field open: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "autofocus") {
		t.Fatalf("nothing may pop a keyboard unasked (ux-notes 3.11)")
	}
}
