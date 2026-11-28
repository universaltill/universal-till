package pages

// ut-docs#6959: Settings > Till registration on an already claimed shop shows
// a calm "linked" line instead of the Claim button, and folds the Pair form
// under a collapsed "Reconnect" <details>. Unclaimed or unknown keeps the
// Claim button (ut-docs#867 elevated POST) and the open Pair form.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
)

func settingsBodyRegistered6959(t *testing.T, claimed string) string {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	kv := newMemKV()
	kv.m["marketplace.store_id"] = "test-6959-store"
	kv.m["marketplace.token"] = "test-6959-token"
	enroll.Init(t.Context(), &config.Config{}, kv, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	if claimed != "" {
		if err := d.Settings.Set(t.Context(), entitlement.KeyStoreClaimed, claimed); err != nil {
			t.Fatal(err)
		}
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	return rec.Body.String()
}

var pairDetailsTag6959 = regexp.MustCompile(`<details[^>]*id="pair-details"[^>]*>`)

func TestSettingsRegistration_ClaimedShowsLinkedState6959(t *testing.T) {
	body := settingsBodyRegistered6959(t, "true")
	if strings.Contains(body, `hx-post="/api/enrol/claim-code"`) || strings.Contains(body, `id="claim-code-msg"`) {
		t.Errorf("claimed shop must not show the Claim button")
	}
	if !strings.Contains(body, `data-testid="store-linked"`) || !strings.Contains(body, "This store is linked to your Universal Till ID.") {
		t.Errorf("claimed shop lacks the linked line")
	}
	// ut-docs#7173: the reconnect block is always visible (accent card, no
	// <details> fold); the id stays so old links and the heal button land.
	if tag := pairDetailsTag6959.FindString(body); tag != "" {
		t.Errorf("reconnect block must be a plain visible block, not <details>: %s", tag)
	}
	if !strings.Contains(body, `id="pair-details"`) {
		t.Fatalf("reconnect block (id=pair-details) missing")
	}
	if !strings.Contains(body, `hx-post="/api/enrol/pair"`) || !strings.Contains(body, "Reconnect this till to a shop") {
		t.Errorf("pair form / reconnect summary missing when claimed")
	}
}

func TestSettingsRegistration_UnclaimedOrUnknownKeepsClaimButton6959(t *testing.T) {
	for name, v := range map[string]string{"unclaimed": "false", "unknown": ""} {
		t.Run(name, func(t *testing.T) {
			body := settingsBodyRegistered6959(t, v)
			if !strings.Contains(body, `hx-post="/api/enrol/claim-code"`) {
				t.Errorf("%s: Claim button missing", name)
			}
			if strings.Contains(body, `data-testid="store-linked"`) {
				t.Errorf("%s: must not show linked line", name)
			}
			// A closed-or-open <details> without <summary> makes the browser
			// paint its own English "Details" label (found driving the page).
			if tag := pairDetailsTag6959.FindString(body); tag != "" {
				t.Errorf("%s: unclaimed pair form must be a plain block, not <details>: %s", name, tag)
			}
			if !strings.Contains(body, `id="pair-details"`) {
				t.Errorf("%s: pair block missing", name)
			}
			if !strings.Contains(body, `hx-post="/api/enrol/pair"`) {
				t.Errorf("%s: pair form missing", name)
			}
		})
	}
}
