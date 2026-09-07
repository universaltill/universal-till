package plugins

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/secrets"
)

// ADR-0082 (ut-docs#1739) no-leak check, half one: the ask/authorize result
// redaction must catch every field NAME the plugin-settings credential
// heuristic (secrets.IsSecretSettingKey) seals on — a plugin that echoes
// its own sumup_api_key / password / auth_value into an answer must not put
// it in the log. Checked against the real marker list, not assumed: before
// this change only b64/token/secret were covered, so "sumup_api_key" and
// "auth_value" reached the log verbatim.
func TestSafeAskResultForLog_RedactsPluginSettingCredentialFieldNames(t *testing.T) {
	for _, field := range []string{
		"sumup_api_key", "stripe_secret_key", "apikey", "password", "db_passwd",
		"auth_value", "private_key", "access_token", "content_b64",
	} {
		if !secrets.IsSecretSettingKey(field) && field != "content_b64" {
			t.Fatalf("test premise: %q should be a heuristic-secret setting key", field)
		}
		out := `{"approved":true,"` + field + `":"cred_live_abc123"}`
		got := safeAskResultForLog(out)
		if strings.Contains(got, "cred_live_abc123") {
			t.Errorf("a small %s field was logged verbatim: %s", field, got)
		}
		if !strings.Contains(got, `"approved":true`) {
			t.Errorf("logged line should still show the small approved field, got: %s", got)
		}
	}
	// The heuristic's bare "key" rule is deliberately NOT mirrored: it is an
	// ordinary hook-answer field name and must stay readable.
	if got := safeAskResultForLog(`{"key":"stripe","approved":true}`); !strings.Contains(got, `"key":"stripe"`) {
		t.Errorf("a plain \"key\" field must not be redacted: %s", got)
	}
}

// Half two: a value carrying the sealed-value prefix is redacted regardless
// of what its field is called (top-level, nested, and one JSON-string
// encoding layer removed, exactly like the token cases).
func TestSafeAskResultForLog_RedactsSealedValueRegardlessOfFieldName(t *testing.T) {
	sealed := secrets.Prefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	cases := map[string]string{
		"top-level":       `{"approved":true,"reader_id":"` + sealed + `"}`,
		"nested":          `{"approved":true,"provider":{"reader_id":"` + sealed + `"}}`,
		"string-embedded": `{"approved":true,"provider":"{\"reader_id\":\"` + sealed + `\"}"}`,
	}
	for name, out := range cases {
		got := safeAskResultForLog(out)
		if strings.Contains(got, secrets.Prefix) {
			t.Errorf("%s: a sealed value was logged verbatim under a non-credential field name: %s", name, got)
		}
		if !strings.Contains(got, `"approved":true`) {
			t.Errorf("%s: logged line should still show the small approved field, got: %s", name, got)
		}
	}
	// A plain reader_id is untouched — the prefix, not the field, triggers.
	if got := safeAskResultForLog(`{"approved":true,"reader_id":"tmr_1"}`); !strings.Contains(got, `"reader_id":"tmr_1"`) {
		t.Errorf("a plain non-credential field must not be redacted: %s", got)
	}
}
