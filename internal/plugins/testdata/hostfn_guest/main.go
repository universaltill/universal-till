//go:build wasip1

// Test guest for the "ut" host functions (docs: wasm-runtime.md v2), built on
// the Go guest SDK (ADR-0121 F4, ut-docs#3951) so the host tests exercise the
// SDK's bindings. Reads the event, exercises storage + http, and records
// every outcome in plugin storage so the host-side test can assert on it.
//
// Codes reported: 0 for success, the host's negative code for a failure —
// except where a field is documented as a length (settings_get, the raw
// http_request probes).
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// raw on purpose: the SDK's HTTP always starts with a large buffer, so it
// cannot make the deliberately undersized (4-byte) buffer-ABI probe the
// ut-docs#754 retry-cache tests need.
//
//go:wasmimport ut http_request
func rawHTTPRequest(rPtr, rLen, dstPtr, dstCap uint32) int32

// raw on purpose: the SDK clamps pct to 0–100 guest-side, so it cannot send
// the out-of-range pct that proves the host clamps too.
//
//go:wasmimport ut job_progress
func rawJobProgress(pct, kPtr, kLen uint32) int32

// raw on purpose: the device_info mode with a "cap" probes the buffer ABI
// with a deliberately small buffer; the SDK's wrappers never send one.
//
//go:wasmimport ut device_id_get
func rawDeviceIDGet(dstPtr, dstCap uint32) int32

//go:wasmimport ut device_local_ips_get
func rawDeviceLocalIPsGet(dstPtr, dstCap uint32) int32

//go:wasmimport ut device_timezone_get
func rawDeviceTimezoneGet(dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

// httpRaw is one raw http_request call into a buffer of dstCap bytes.
func httpRaw(req []byte, dstCap int) (int32, []byte) {
	buf := make([]byte, dstCap)
	rp, rl := ptrOf(req)
	bp, bc := ptrOf(buf)
	n := rawHTTPRequest(rp, rl, bp, bc)
	runtime.KeepAlive(req)
	runtime.KeepAlive(buf)
	return n, buf
}

// code maps an SDK result back to the host's numeric return: 0 for success,
// the negative host code for a *plugin.Error, -3 for any other failure.
func code(err error) int32 {
	var e *plugin.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return -3
	}
	return 0
}

type payload struct {
	URL     string   `json:"url"`
	Mode    string   `json:"mode"`
	URLs    []string `json:"urls"`
	Method  string   `json:"method"`       // default GET
	BodyB64 string   `json:"body_b64"`     // request body, base64
	Key     string   `json:"key"`          // secret_set mode
	Value   string   `json:"value"`        // secret_set mode
	Size    int      `json:"size"`         // secret_set mode: value of this many bytes instead
	BadUTF8 bool     `json:"invalid_utf8"` // secret_set mode: send a non-UTF-8 value
	// publish / publish_fail modes (ut-docs#3871).
	PublishType    string          `json:"publish_type"`
	PublishPayload json.RawMessage `json:"publish_payload"`
	// sleep / job_progress modes (ut-docs#3908).
	SleepMS     int    `json:"sleep_ms"`
	Pct         uint32 `json:"pct"`
	ProgressKey string `json:"progress_key"`
	// device_info mode (ADR-0140): a small destination buffer for the
	// buffer-ABI probe; 0 → the SDK wrappers.
	Cap int `json:"cap"`
}

func main() { plugin.Run(plugin.Handlers{"*": handle}) }

// store records results under "results"; a failure ends the module with
// exit 1 (Run prints the error to stderr).
func store(results []byte) error {
	if err := plugin.StorageSet("results", results); err != nil {
		return fmt.Errorf("storing results failed: %d", code(err))
	}
	return nil
}

// storeAndPrint is store, then the results as the answer.
func storeAndPrint(results []byte) (any, error) {
	if err := store(results); err != nil {
		return nil, err
	}
	return append(results, '\n'), nil
}

func handle(ev plugin.Event) (any, error) {
	var p payload
	_ = ev.Decode(&p)
	if p.Method == "" {
		p.Method = "GET"
	}
	plugin.Logf("guest running, url=%s mode=%s", p.URL, p.Mode)

	switch p.Mode {
	case "publish", "publish_fail":
		// event_publish (ADR-0121 §3): print the host's return code; in
		// publish_fail mode exit non-zero afterwards — the host must still
		// deliver what it accepted.
		c := code(plugin.EventPublish(p.PublishType, p.PublishPayload))
		out := []byte(fmt.Sprintf("{\"publish_code\":%d}\n", c))
		if p.Mode == "publish_fail" {
			return out, plugin.ExitCode(1)
		}
		return out, nil
	case "sleep":
		// A long call (ADR-0121 §8): sleep, then answer.
		time.Sleep(time.Duration(p.SleepMS) * time.Millisecond)
		return []byte("{\"slept\":true}\n"), nil
	case "job_progress":
		// job_progress (ADR-0121 §3/§8): print the host's return code.
		var c int32
		if p.Pct > 100 {
			k := []byte(p.ProgressKey)
			kp, kl := ptrOf(k)
			c = rawJobProgress(p.Pct, kp, kl)
			runtime.KeepAlive(k)
		} else {
			c = code(plugin.JobProgress(int(p.Pct), p.ProgressKey))
		}
		return []byte(fmt.Sprintf("{\"progress_code\":%d}\n", c)), nil
	case "http_retry":
		return runHTTPRetry(p.URL)
	case "http_retry_diff":
		return runHTTPRetryDiff(p.URLs)
	case "http_repeat_same":
		return runHTTPRepeatSame(p.URL)
	case "http_retry_then_repeat":
		return runHTTPRetryThenRepeat(p.URL)
	case "clock":
		return nil, runClock()
	case "secret_set":
		val := []byte(p.Value)
		if p.Size > 0 {
			val = make([]byte, p.Size)
			for i := range val {
				val[i] = 'x'
			}
		}
		if p.BadUTF8 {
			val = []byte{0xff, 0xfe, 'x'}
		}
		return nil, runSecretSet(p.Key, val)
	case "http_len":
		return runHTTPLen(p.URL)
	case "device_info":
		return nil, runDeviceInfo(p.Cap)
	}

	// Storage round-trip.
	setCode := code(plugin.StorageSet("greeting", []byte("hello from wasm")))
	got, _ := plugin.StorageGet("greeting")
	roundtrip := string(got) == "hello from wasm"

	// HTTP call (host enforces net:<host> permission).
	httpStatus := 0
	httpBody := ""
	reqBody, _ := base64.StdEncoding.DecodeString(p.BodyB64)
	resp, err := plugin.HTTP(plugin.HTTPRequest{Method: p.Method, URL: p.URL, Body: reqBody})
	httpCode := code(err)
	if err == nil {
		httpStatus = resp.Status
		httpBody = base64.StdEncoding.EncodeToString(resp.Body)
	}

	// Read a plugin setting via settings_get: the code is the value's
	// length, as the buffer ABI returns it.
	settingVal, err := plugin.SettingsGet("endpoint")
	sCode := int32(len(settingVal))
	if err != nil {
		sCode = code(err)
	}

	results, _ := json.Marshal(map[string]any{
		"set_code":     setCode,
		"roundtrip":    roundtrip,
		"http_code":    httpCode,
		"http_status":  httpStatus,
		"http_body":    httpBody,
		"setting_code": sCode,
		"setting_val":  settingVal,
	})
	return storeAndPrint(results)
}

// getRequest is the request bytes every raw-probe mode sends, identical
// across the probe and its retries.
func getRequest(url string) []byte {
	req, _ := json.Marshal(map[string]any{"method": "GET", "url": url, "body_b64": ""})
	return req
}

func bodyOf(n int32, buf []byte) string {
	if n <= 0 || int(n) > len(buf) {
		return ""
	}
	var resp struct {
		BodyB64 string `json:"body_b64"`
	}
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return ""
	}
	return resp.BodyB64
}

// runHTTPRetry is the ut-docs#754 proof: it issues ONE http_request call
// with a deliberately undersized destination buffer (4 bytes — far smaller
// than any real response), which per the buffer ABI returns the FULL
// response length and instructs the guest to "call again with a bigger
// buffer." It does exactly that: the SAME request bytes, a buffer big
// enough this time. The host-side test counts real HTTP hits at the
// server, so a fixed cache correctly serves the second call without
// touching the network again — while an unfixed host would hit the server
// twice for what the guest sees as one logical call. Both calls are raw:
// the retry must reuse the probe's exact bytes.
func runHTTPRetry(url string) (any, error) {
	req := getRequest(url)
	firstCode, _ := httpRaw(req, 4)
	secondCode, buf := httpRaw(req, 64*1024)
	results, _ := json.Marshal(map[string]any{
		"first_code":  firstCode,
		"second_code": secondCode,
		"second_body": bodyOf(secondCode, buf),
	})
	return storeAndPrint(results)
}

// sdkGet is one adequately-buffered GET through the SDK: its code (0 or the
// host's error) and the base64 body.
func sdkGet(url string) (int32, string) {
	resp, err := plugin.HTTP(plugin.HTTPRequest{Method: "GET", URL: url})
	if err != nil {
		return code(err), ""
	}
	return 0, base64.StdEncoding.EncodeToString(resp.Body)
}

// runHTTPRetryDiff is the false-positive guard for the #754 cache: TWO
// genuinely different requests (different URLs), each with an adequately
// sized buffer up front — never an undersized-buffer retry. Both must
// reach the server: the cache must key on the exact request bytes, not
// just "the plugin made an http_request call before."
func runHTTPRetryDiff(urls []string) (any, error) {
	results := map[string]any{}
	for i, u := range urls {
		c, body := sdkGet(u)
		results[fmt.Sprintf("code_%d", i)] = c
		results[fmt.Sprintf("body_%d", i)] = body
	}
	out, _ := json.Marshal(results)
	return storeAndPrint(out)
}

// runHTTPRepeatSame is the ut-docs#754 review's F1 false-positive guard: it
// makes the SAME request TWICE in a row, each time with a generously sized
// buffer up front — never an undersized-buffer retry. This is a poll loop
// or a deliberate duplicate submission, not the buffer-ABI retry the #754
// cache exists for, and both calls must reach the server: caching every
// successful call unconditionally (the review's original diff) silently
// collapsed exactly this into one live call.
func runHTTPRepeatSame(url string) (any, error) {
	firstCode, firstBody := sdkGet(url)
	secondCode, secondBody := sdkGet(url)
	results, _ := json.Marshal(map[string]any{
		"first_code":  firstCode,
		"first_body":  firstBody,
		"second_code": secondCode,
		"second_body": secondBody,
	})
	return storeAndPrint(results)
}

// runHTTPRetryThenRepeat proves the cache clears once fully served: an
// undersized-buffer call (miss, caches on overflow) is followed by a
// big-buffer retry with the SAME bytes (hit, clears the cache since this
// buffer holds the whole response) — then a THIRD call, same bytes, big
// buffer again. That third call is not part of any pending retry; the
// cache must already be empty, so it goes out for real. All raw: every
// call must carry the probe's exact bytes.
func runHTTPRetryThenRepeat(url string) (any, error) {
	req := getRequest(url)
	firstCode, _ := httpRaw(req, 4)
	secondCode, buf2 := httpRaw(req, 64*1024)
	thirdCode, buf3 := httpRaw(req, 64*1024)
	results, _ := json.Marshal(map[string]any{
		"first_code":  firstCode,
		"second_code": secondCode,
		"second_body": bodyOf(secondCode, buf2),
		"third_code":  thirdCode,
		"third_body":  bodyOf(thirdCode, buf3),
	})
	return storeAndPrint(results)
}

// runHTTPLen (ut-docs#3226) fetches one URL the way a real guest fetching a
// large CRL would — through the SDK, whose buffer-ABI retry (first buffer
// too small → one retry at the reported size, served from the host's
// cache) carries a body past its first buffer — and records only the
// status and the decoded body length, so the host-side test can pin the
// response cap without round-tripping megabytes through plugin storage.
func runHTTPLen(url string) (any, error) {
	resp, err := plugin.HTTP(plugin.HTTPRequest{Method: "GET", URL: url})
	res := map[string]any{"http_code": code(err)}
	if err == nil {
		res["http_status"] = resp.Status
		res["body_len"] = len(resp.Body)
	}
	results, _ := json.Marshal(res)
	return storeAndPrint(results)
}

// runClock reports what the guest sees as wall and monotonic time
// (ADR-0121 §3: WASI clock_time_get must return the host's real clocks, not
// wazero's fake 2022-01-01 epoch).
func runClock() error {
	start := time.Now()
	wall := start.UnixNano()
	time.Sleep(20 * time.Millisecond)
	elapsed := time.Since(start)
	results, _ := json.Marshal(map[string]any{
		"wall_unix_nano": wall,
		"elapsed_nano":   elapsed.Nanoseconds(),
	})
	return store(results)
}

// runSecretSet calls secret_set(key, val), then reads the key back through
// settings_get, and records both codes (get_code is the value's length on
// success, as the buffer ABI returns it).
func runSecretSet(key string, val []byte) error {
	secretCode := code(plugin.SecretSet(key, string(val)))
	got, err := plugin.SettingsGet(key)
	getCode := int32(len(got))
	if err != nil {
		getCode = code(err)
	}
	results, _ := json.Marshal(map[string]any{
		"secret_code": secretCode,
		"get_code":    getCode,
		"get_val":     got,
	})
	return store(results)
}

// runDeviceInfo records device_id_get, device_local_ips_get and
// device_timezone_get (ADR-0140): each return code (the value's length, or
// the host's error) and, on success, the value. With cap > 0 it calls raw
// with a cap-byte buffer, so the code is the host's full length.
func runDeviceInfo(dstCap int) error {
	results := map[string]any{}
	if dstCap > 0 {
		for name, fn := range map[string]func(uint32, uint32) int32{
			"id": rawDeviceIDGet, "ips": rawDeviceLocalIPsGet, "timezone": rawDeviceTimezoneGet,
		} {
			buf := make([]byte, dstCap)
			bp, bc := ptrOf(buf)
			c := fn(bp, bc)
			runtime.KeepAlive(buf)
			results[name+"_code"] = c
			if c > 0 && int(c) <= dstCap {
				results[name+"_val"] = string(buf[:c])
			}
		}
	} else {
		record := func(name, val string, err error) {
			if err != nil {
				results[name+"_code"] = code(err)
				return
			}
			results[name+"_code"] = len(val)
			results[name+"_val"] = val
		}
		id, err := plugin.DeviceID()
		record("id", id, err)
		ips, err := plugin.DeviceLocalIPs()
		var ipsJSON string
		if err == nil {
			b, _ := json.Marshal(ips)
			ipsJSON = string(b)
		}
		record("ips", ipsJSON, err)
		tz, err := plugin.DeviceTimezone()
		record("timezone", tz, err)
	}
	out, _ := json.Marshal(results)
	return store(out)
}
