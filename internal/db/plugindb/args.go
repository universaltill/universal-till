package plugindb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
)

const (
	maxArgs     = 64
	maxArgsJSON = 256 << 10
)

// DecodeArgs turns the guest's JSON args array into driver values: null, bool,
// string, int64, float64, and {"b64":"..."} for a BLOB. Anything else (nested
// arrays, other objects, more than 64 items, over 256 KiB) is refused.
func DecodeArgs(raw []byte) ([]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxArgsJSON {
		return nil, fmt.Errorf("%w: arguments over %d bytes", ErrStatementRefused, maxArgsJSON)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var items []json.RawMessage
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("%w: arguments are not a JSON array", ErrStatementRefused)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after arguments", ErrStatementRefused)
	}
	if len(items) > maxArgs {
		return nil, fmt.Errorf("%w: more than %d arguments", ErrStatementRefused, maxArgs)
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		v, err := decodeArg(it)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func decodeArg(it json.RawMessage) (any, error) {
	it = bytes.TrimSpace(it)
	bad := fmt.Errorf("%w: unsupported argument type", ErrStatementRefused)
	if len(it) == 0 {
		return nil, bad
	}
	switch it[0] {
	case 'n':
		if string(it) == "null" {
			return nil, nil
		}
		return nil, bad
	case 't', 'f':
		var b bool
		if json.Unmarshal(it, &b) != nil {
			return nil, bad
		}
		return b, nil
	case '"':
		var s string
		if json.Unmarshal(it, &s) != nil {
			return nil, bad
		}
		return s, nil
	case '{':
		var m map[string]json.RawMessage
		if json.Unmarshal(it, &m) != nil || len(m) != 1 {
			return nil, bad
		}
		enc, ok := m["b64"]
		if !ok {
			return nil, bad
		}
		var s string
		if json.Unmarshal(enc, &s) != nil {
			return nil, bad
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64 blob", ErrStatementRefused)
		}
		return b, nil
	case '[':
		return nil, bad
	default:
		s := string(it)
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, bad
		}
		return f, nil
	}
}
