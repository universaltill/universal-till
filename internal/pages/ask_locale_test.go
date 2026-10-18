package pages

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// ut-docs#4472: an ask carries ?lang= matched onto an installed locale,
// never the raw value — "TR-tr" arrives as "tr", an unknown or oversized
// value as the shop default.
func TestPluginAskLocale_MatchesInstalledLocale(t *testing.T) {
	newDataAPITestDeps(t) // real web/locales i18n, default "en"
	for _, tc := range []struct{ lang, want string }{
		{"tr", "tr"},
		{"TR-tr", "tr"},
		{"fa_IR", "fa"},
		{"xx", "en"},
		{"<script>", "en"},
		{"", "en"},
	} {
		r := httptest.NewRequest("POST", "/api/data/import?lang="+tc.lang, nil)
		if got := pluginAskLocale(r); got != tc.want {
			t.Errorf("?lang=%q: got %q, want %q", tc.lang, got, tc.want)
		}
	}
}

// ut-docs#4472 review: the import ask payloads had no field pin, so the
// SDK's plugin.ImportRequest could drift from them unnoticed.
func TestImportRequestPayloads_PinnedFields(t *testing.T) {
	assertJSONFields(t, "importRequestPayload", reflect.TypeOf(importRequestPayload{}), []string{
		"entry_key", "entities", "file_handle", "file_name", "file_size", "locale",
	})
	assertJSONFields(t, "apiImportRequestPayload", reflect.TypeOf(apiImportRequestPayload{}), []string{
		"entry_key", "entities", "mode", "source", "locale",
	})
	assertJSONFields(t, "fileRowsRequestPayload", reflect.TypeOf(fileRowsRequestPayload{}), []string{
		"entry_key", "entities", "file_handle", "file_name", "file_size", "mode", "source", "locale",
	})
}
