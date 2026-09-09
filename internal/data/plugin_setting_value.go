package data

import (
	"encoding/json"
	"strings"
)

// EncodeMapSettingValue is the single write-side seam for a map-typed
// plugin_settings.value_json: marshal v, then JSON-string-wrap the result.
// That's the same on-disk shape internal/pages/plugin_settings_page.go's
// generic scalar-setting save path already produces for every OTHER setting
// type (it does a plain json.Marshal(val) on the submitted string, which for
// any Go string is itself a JSON-string encoding) — a map-typed setting's
// canonical shape is a JSON string whose *content* is the map's own JSON,
// one wrap deeper. ut-docs#1269: MergeAdditiveJSONMapSetting used to write
// the raw unwrapped object instead, so the on-disk shape silently depended
// on which write path touched a setting last; this is the fix.
func EncodeMapSettingValue(v any) (string, error) {
	marshaled, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	wrapped, err := json.Marshal(string(marshaled))
	if err != nil {
		return "", err
	}
	return string(wrapped), nil
}

// DecodeMapSettingValue is the read-side match for EncodeMapSettingValue:
// unwrap one level of JSON-string encoding, falling back to the raw stored
// text unchanged when it isn't string-wrapped — a pre-existing raw-object
// (or bare JSON null) row written before this shape became canonical.
//
// The leading-quote check (rather than just attempting the string unmarshal
// and using its result) matters for a stored bare `null`: json.Unmarshal of
// the JSON literal null into a non-pointer string target is a documented
// no-op — it returns a nil error and leaves the target at its zero value
// (""), which is indistinguishable from "successfully unwrapped to an empty
// string" if we don't check the shape first. That would turn a bare `null`
// into "" here, and the caller's subsequent json.Unmarshal([]byte(""), ...)
// then fails on "unexpected end of JSON input" — a regression from today's
// behavior, where a bare `null` is left alone and handled by the caller's
// own null-into-map handling. Checking for a leading `"` avoids ever
// touching a non-string-JSON value (object, array, number, bool, or null).
func DecodeMapSettingValue(stored string) string {
	trimmed := strings.TrimSpace(stored)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return stored
	}
	var v string
	if json.Unmarshal([]byte(stored), &v) != nil {
		return stored
	}
	return v
}
