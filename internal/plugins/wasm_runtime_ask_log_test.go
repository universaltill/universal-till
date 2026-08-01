package plugins

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWasmResultLogLine_GatesOnAskSuffix exercises the exact call
// HandleEvent makes (wasmResultLogLine is its only logging call for a
// handler's stdout result) — an earlier draft of this fix only unit-tested
// the redaction helper in isolation, and an independent review found that
// reverting just HandleEvent's call site (while leaving the helper intact)
// left the whole test suite green: the real ut-docs#202 defect could have
// been silently reintroduced. Testing wasmResultLogLine directly, with
// exact-format assertions on both branches, closes that gap.
func TestWasmResultLogLine_GatesOnAskSuffix(t *testing.T) {
	// Non-".ask" events (e.g. sale.completed) must keep the exact original,
	// unredacted format -- explicitly out of scope for ut-docs#202.
	nonAskOut := `{"anything":"` + strings.Repeat("Z", 5000) + `"}`
	if got, want := wasmResultLogLine("com.test.plugin", "sale.completed", nonAskOut),
		`[wasm:com.test.plugin] result: `+nonAskOut; got != want {
		t.Errorf("non-.ask event's log line changed format (want byte-identical to the pre-fix line)")
	}

	// ".ask" events must go through redaction -- if HandleEvent's wiring to
	// this function were ever reverted, this is the assertion that catches it.
	bigPayload := strings.Repeat("A", 5000)
	askOut := `{"ok":true,"content_b64":"` + bigPayload + `"}`
	got := wasmResultLogLine("com.test.plugin", "export.requested.ask", askOut)
	if strings.Contains(got, bigPayload) {
		t.Fatalf("wasmResultLogLine did not redact a large field on an .ask event")
	}
	if !strings.HasPrefix(got, "[wasm:com.test.plugin] result (export.requested.ask, ") {
		t.Errorf("expected event type + byte count in the .ask log line, got: %s", got)
	}
}

// TestSafeAskResultForLog_RedactsOversizedField proves a large content_b64
// field on an ".ask" response never reaches the log verbatim (ut-docs#202,
// found during independent review of ut-docs#189 — export.requested.ask's
// full exported dataset was landing at INFO level in wasm_runtime.go).
// Small fields (ok, filename, message) must survive so the line stays
// useful for debugging.
func TestSafeAskResultForLog_RedactsOversizedField(t *testing.T) {
	bigPayload := strings.Repeat("A", 5000) // stands in for a real base64 export
	out := `{"ok":true,"filename":"export.csv","content_b64":"` + bigPayload + `","message":"count=2 sum=2000"}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, bigPayload) {
		t.Fatalf("logged line still contains the full content_b64 payload verbatim")
	}
	for _, want := range []string{`"ok":true`, `"filename":"export.csv"`, `"message":"count=2 sum=2000"`} {
		if !strings.Contains(got, want) {
			t.Errorf("logged line missing %s, still needed for debugging: %s", want, got)
		}
	}
	if !strings.Contains(got, "5002 bytes") { // raw JSON value includes the surrounding quotes
		t.Errorf("logged line should note the omitted field's size, got: %s", got)
	}
	if len(got) > 500 {
		t.Errorf("logged line still large after redaction: %d bytes", len(got))
	}
}

// TestSafeAskResultForLog_RedactsBlobFieldByNameEvenWhenSmall proves a
// SMALL content_b64 is still redacted — byte-size alone isn't the risk
// here (the ticket that filed ut-docs#202 is GDPR-adjacent): a two-line
// export can still carry a real customer's name/email, and that must never
// reach the log just because the blob happened to be small.
func TestSafeAskResultForLog_RedactsBlobFieldByNameEvenWhenSmall(t *testing.T) {
	// base64 of "date,customer,email,total\n2026-08-01,John Smith,john@example.com,20.00"
	const smallPIIBlob = "ZGF0ZSxjdXN0b21lcixlbWFpbCx0b3RhbApuMjAyNi0wOC0wMSxKb2huIFNtaXRoLGpvaG5AZXhhbXBsZS5jb20sMjAuMDA="
	out := `{"ok":true,"content_b64":"` + smallPIIBlob + `"}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, smallPIIBlob) {
		t.Fatalf("a small content_b64 field was logged verbatim -- still leaks PII even though it's small: %s", got)
	}
	if !strings.Contains(got, `"ok":true`) {
		t.Errorf("logged line should still show the small ok field, got: %s", got)
	}
}

// TestSafeAskResultForLog_CapsOverallSizeWhenManySmallFieldsAddUp proves
// many individually-small fields (each under maxAskFieldBytes, so none is
// redacted on its own) can't sum to an unbounded log line -- e.g. a report
// plugin answering one field per sale row.
func TestSafeAskResultForLog_CapsOverallSizeWhenManySmallFieldsAddUp(t *testing.T) {
	fields := make([]string, 400)
	for i := range fields {
		fields[i] = `"row_` + strconv.Itoa(i) + `":"` + strings.Repeat("v", 150) + `"`
	}
	out := "{" + strings.Join(fields, ",") + "}"

	got := safeAskResultForLog(out)

	if len(got) > 600 { // maxAskLogBytes (500) plus the truncation suffix
		t.Errorf("many-small-fields response was not capped: %d bytes", len(got))
	}
	if len(out) <= len(got) {
		t.Fatalf("test setup didn't produce a response larger than the cap: %d bytes", len(out))
	}
}

// TestSafeAskResultForLog_LeavesSmallResponseUnchanged proves a normal-sized
// ".ask" answer (e.g. tax.rate.ask's {"rate_bp":N}) keeps its existing log
// behavior — this fix must not degrade debugging of the common case.
func TestSafeAskResultForLog_LeavesSmallResponseUnchanged(t *testing.T) {
	out := `{"rate_bp":2000}`
	if got := safeAskResultForLog(out); got != out {
		t.Errorf("small response should be unchanged, got %q want %q", got, out)
	}
}

// TestSafeAskResultForLog_FallsBackToTruncationForNonObjectOutput proves
// non-JSON-object ".ask" output (a plugin bug, or a handler that doesn't
// answer in the expected shape) still can't dump an unbounded blob into the
// log — it's hard-truncated instead of redacted field-by-field.
func TestSafeAskResultForLog_FallsBackToTruncationForNonObjectOutput(t *testing.T) {
	bigPayload := strings.Repeat("B", 5000)
	got := safeAskResultForLog(bigPayload)

	if strings.Contains(got, bigPayload) {
		t.Fatalf("logged line still contains the full non-JSON payload verbatim")
	}
	if !strings.Contains(got, "5000 bytes total") {
		t.Errorf("truncated line should note the total size, got: %s", got)
	}
}

// TestSafeAskResultForLog_TruncationIsValidUTF8 proves a truncated line
// never splits a multi-byte rune (e.g. an RTL locale's export filename) —
// a plain byte-slice cut can produce invalid UTF-8 that garbles the log.
func TestSafeAskResultForLog_TruncationIsValidUTF8(t *testing.T) {
	// Multi-byte runes (€, 3 bytes each in UTF-8) straddling likely cut
	// points around maxAskLogBytes.
	bigPayload := "x" + strings.Repeat("€", 400)
	got := safeAskResultForLog(bigPayload)

	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8: %q", got)
	}
}
