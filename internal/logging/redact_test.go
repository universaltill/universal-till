package logging

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretSamples are token-looking strings that must never reach the log
// file (ut-docs#2720 AC: same no-secrets rule as the diagnostics stream).
var secretSamples = []struct{ line, secret string }{
	{"auth header Authorization: Bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc", "9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc"},
	{"calling cloud with bearer abc123DEF456ghi789", "abc123DEF456ghi789"},
	{"merchant_token=s3cr3t-Merchant-T0ken", "s3cr3t-Merchant-T0ken"},
	{`settings {"client_secret":"hunter2-is-the-pw"}`, "hunter2-is-the-pw"},
	{"UT_MARKETPLACE_CLIENT_SECRET: topsecretvalue", "topsecretvalue"},
	{"admin password = CorrectHorse", "CorrectHorse"},
	{"login pin=4821 accepted", "4821"},
	{"GET https://user:p4ssw0rd@cloud.example.com/api", "p4ssw0rd"},
	{"GET /v1/devices?store_id=abc&access_token=q1w2e3r4t5y6", "q1w2e3r4t5y6"},
	{"id token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U seen", "eyJhbGciOiJIUzI1NiJ9"},
	{"sync bearer 2b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfe issued", "2b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfe"},
	{"raw key Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo in body", "Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo"},
	{"Cookie: ut_session=Q2FzaGllclNlc3Npb24xMjM", "Q2FzaGllclNlc3Npb24xMjM"},

	// pin, suffix-tolerant (ut-docs#2728 — secretKVRe's old \bpin only
	// matched the bare word, so "_"/"-" joined variants slipped through).
	{"admin_pin=1234 set", "1234"},
	{"user_pin=7788 verified", "7788"},
	{"manager-pin=9911 override", "9911"},
	{"pin_code=4455 dispatched", "4455"},
	{"pin_hash=deadbeefcafefeed reset", "deadbeefcafefeed"},
	{`config {"admin_pin":"1234"}`, "1234"},

	// otp / totp / hotp one-time codes (ut-docs#2728).
	{"otp=778899 sent", "778899"},
	{"totp=112233 accepted", "112233"},
	{"otp_code=445566 rejected", "445566"},
	{`mfa {"otp":"123456"}`, "123456"},
	{"hotp=998877 counter advanced", "998877"},

	// session tokens/ids (ut-docs#2728) — not the bare counting words.
	{"session=abcXYZ123 token issued", "abcXYZ123"},
	{"ut_session=Q2FzaGllclNlc3Npb24xMjM set", "Q2FzaGllclNlc3Npb24xMjM"},
	{"session_id=sxyz9988abcd active", "sxyz9988abcd"},
	{"sessionid=sess1234ABCD active", "sess1234ABCD"},
	{`state {"session":"sess-abc123XYZ"}`, "sess-abc123XYZ"},

	// OAuth / one-time codes (ut-docs#2728) — query-string ?code=/&code=
	// and named keys, not diagnostic "code" words.
	{"GET /oauth/callback?code=AbCdEf123456&state=xyz", "AbCdEf123456"},
	{"GET /oauth/callback?state=xyz&code=AbCdEf123456", "AbCdEf123456"},
	{"auth_code=aC0de1234567 exchanged", "aC0de1234567"},
	{"authorization_code=aC0de7654321 exchanged", "aC0de7654321"},
	{"pairing_code=PA1R2C0DE55 shown", "PA1R2C0DE55"},
	{"redeem_code=RDM123456AB applied", "RDM123456AB"},
	{"device_code=DEV1234ABCD polled", "DEV1234ABCD"},
	{"user_code=USR1234ABCD shown", "USR1234ABCD"},
	{"verification_code=VER1234ABCD sent", "VER1234ABCD"},
	{"recovery_code=REC1234ABCD used", "REC1234ABCD"},
	{"reset_code=RST1234ABCD emailed", "RST1234ABCD"},
	{"otp_code=OTP1234ABCD sent", "OTP1234ABCD"},

	// PANs (ut-docs#2728) — Luhn-valid, IIN 2-6, contiguous or grouped.
	{"card 4111111111111111 charged", "4111111111111111"},
	{"card 5555555555554444 charged", "5555555555554444"},
	{"amex 378282246310005 charged", "378282246310005"},
	{"card 4111 1111 1111 1111 charged", "4111 1111 1111 1111"},
	{"card 4111-1111-1111-1111 charged", "4111-1111-1111-1111"},
	{"amex 3782 822463 10005 charged", "3782 822463 10005"},

	// PANs glued to a neighbouring number with the same separator — the
	// whole run fails Luhn/length, the card inside it must still go.
	{"tender 2 4111111111111111", "4111111111111111"},
	{"table 12 4111 1111 1111 1111", "4111 1111 1111 1111"},
	{"qty 3 5555-5555-5555-4444", "5555-5555-5555-4444"},
	{"4111111111111111 5 items", "4111111111111111"},
	{"4111 1111 1111 1111 7 items", "4111 1111 1111 1111"},
	{"amount 1 378282246310005", "378282246310005"},
	{"ref 12 34 4111 1111 1111 1111 ok", "4111 1111 1111 1111"},
	{"ids 1234-5678 4111 1111 1111 1111", "4111 1111 1111 1111"},
}

func TestRedactRemovesSecrets(t *testing.T) {
	for _, s := range secretSamples {
		got := Redact(s.line)
		if strings.Contains(got, s.secret) {
			t.Errorf("secret survived redaction:\n in: %s\nout: %s", s.line, got)
		}
		if !strings.Contains(got, redactedMark) {
			t.Errorf("no %s marker in %q", redactedMark, got)
		}
	}
}

// Redaction must not destroy what makes the log useful for diagnosis:
// ids, versions, paths, hosts and ordinary prose survive unchanged.
func TestRedactKeepsDiagnosticDetail(t *testing.T) {
	keep := []string{
		"startup: version=v0.22.2 os=windows/amd64 data_dir=\"C:\\Users\\shop\\AppData\\Local\\UniversalTill\" cloud_host=cloud.universaltill.com enrolled=yes store=…9c1d role=primary",
		"enrolment: till registered with marketplace as 5b0e2a39-8c1f-4d7a-9b6e-3f1a2c4d9c1d (device till-0f6a1b2c-3d4e-4f50-8a9b-0c1d2e3f4a5b)",
		"plugin ut-plugin-payment-sumup 1.4.2 installed from /Users/shop/Library/Application Support/UniversalTill/plugins/ut-plugin-payment-sumup/1.4.2",
		"cloudsync: heartbeat failed (will retry): Post \"https://cloud.universaltill.com/api/v1/heartbeat\": dial tcp: lookup cloud.universaltill.com: no such host",
		"Universal Till POS starting...",
		"shipping pinned items: 3",
		"data dir /tmp/TestRun_WritesLogFileWithStartupLine3162494016/001 ready",
		"plugin ut-plugin-fiscal-de-tse2-signing-adapter-v2 loaded",

		// pin: only a real pin key, suffix-tolerant, is a secret (ut-docs#2728).
		"shipping=3 confirmed",
		"pinned=true",
		"spin=2 remaining",
		"pinning strategy updated",

		// otp/totp/hotp: no boundary → not a secret key (ut-docs#2728).
		"photpx=1 recorded",

		// session: counts are diagnostic, not secrets (ut-docs#2728).
		"sessions=3 active",
		"session_count=3 active",

		// code: diagnostic codes, not OAuth/one-time codes (ut-docs#2728).
		"status code=404 returned",
		"exit code=1",
		"country_code=DE store",
		"currency_code=EUR amount",
		"error code: E42 raised",

		// PANs: Luhn-invalid, timestamp, order number, UUID all survive
		// (ut-docs#2728).
		"card 4111111111111112 declined",
		"event at ms=1727430000000 recorded",
		"order number 100045 confirmed",
		"qty 2 4111111111111112 declined",
		// A card-valid prefix inside a longer contiguous digit run is not
		// cut out — spans only start/end on group boundaries.
		"ref 41111111111111110000 stored",
	}
	for _, line := range keep {
		if got := Redact(line); got != line {
			t.Errorf("redaction changed a harmless line:\n in: %s\nout: %s", line, got)
		}
	}
}

// End to end: a token logged through the package logger (and through the
// stdlib log package, which much of the till still uses) is redacted in the
// file.
func TestAttachedFileNeverContainsToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "till.log")
	if err := AttachFile(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(DetachFile)

	const tok = "9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc"
	L().Infof("cloud call Authorization: Bearer %s", tok)
	L().Warnf("merchant_token=%s rejected", "s3cr3t-Merchant-T0ken")
	log.Printf("sync bearer %s issued", tok)
	L().Infof("marker line")
	DetachFile()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "marker line") {
		t.Fatalf("log file missing lines: %q", out)
	}
	for _, secret := range []string{tok, "s3cr3t-Merchant-T0ken"} {
		if strings.Contains(out, secret) {
			t.Fatalf("token reached the log file: %q", out)
		}
	}
	if !strings.Contains(out, "sync bearer "+redactedMark) {
		t.Fatalf("stdlib log line not captured (redacted) in file: %q", out)
	}
}
