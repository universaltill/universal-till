package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ut-docs#2769 review (S1): marketplace.token is now this till's own,
// non-reissuable cloud credential (ADR-0116) and sync.bearer the LAN one.
// The manager's "All settings" card printed every row in plain text — a
// screenshot in a bug report would leak it — and its inline editor could
// overwrite it. Neither may happen.
func TestSettings_CredentialsNeverShownNorEditable(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	secrets := map[string]string{
		"marketplace.token":     "dev-credential-0123456789abcdef",
		"marketplace.device_id": "device-id-fedcba9876543210",
		"sync.bearer":           "lan-bearer-00112233445566778899",
	}
	for k, v := range secrets {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	body := getAs(mux, "/settings", &mgrUser).Body.String()
	for k, v := range secrets {
		if strings.Contains(body, v) {
			t.Errorf("/settings shows the value of %s", k)
		}
	}

	for k, v := range secrets {
		rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {k}, "value": {"overwritten"}}, &mgrUser)
		if rec.Code != http.StatusForbidden {
			t.Errorf("upsert %s: code %d, want 403", k, rec.Code)
		}
		if got, _, _ := d.Settings.Get(ctx, k); got != v {
			t.Errorf("upsert overwrote %s: %q", k, got)
		}
	}
}
