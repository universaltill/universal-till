//go:build wasip1

// Test guest for the "ut" host functions (docs: wasm-runtime.md v2).
// Reads the event from stdin, exercises storage + http, and records every
// outcome in plugin storage so the host-side test can assert on it.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"
)

//go:wasmimport ut log_write
func logWrite(ptr, n uint32)

//go:wasmimport ut storage_get
func storageGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut storage_set
func storageSet(kPtr, kLen, vPtr, vLen uint32) int32

//go:wasmimport ut http_request
func httpRequest(rPtr, rLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut settings_get
func settingsGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut secret_set
func secretSet(kPtr, kLen, vPtr, vLen uint32) int32

//go:wasmimport ut event_publish
func eventPublish(tPtr, tLen, pPtr, pLen uint32) int32

//go:wasmimport ut job_progress
func jobProgress(pct, kPtr, kLen uint32) int32

//go:wasmimport ut device_id_get
func deviceIDGet(dstPtr, dstCap uint32) int32

//go:wasmimport ut device_local_ips_get
func deviceLocalIPsGet(dstPtr, dstCap uint32) int32

//go:wasmimport ut device_timezone_get
func deviceTimezoneGet(dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func set(key string, val []byte) int32 {
	kp, kl := ptrOf([]byte(key))
	vp, vl := ptrOf(val)
	return storageSet(kp, kl, vp, vl)
}

func get(key string) ([]byte, int32) {
	kp, kl := ptrOf([]byte(key))
	buf := make([]byte, 64*1024)
	bp, bc := ptrOf(buf)
	n := storageGet(kp, kl, bp, bc)
	if n < 0 {
		return nil, n
	}
	return buf[:n], n
}

func logf(format string, args ...any) {
	msg := []byte(fmt.Sprintf(format, args...))
	p, n := ptrOf(msg)
	logWrite(p, n)
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var event struct {
		Payload struct {
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
			// device_info mode (ADR-0140): destination buffer size, 0 → 4096.
			Cap int `json:"cap"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(raw, &event)
	if event.Payload.Method == "" {
		event.Payload.Method = "GET"
	}
	logf("guest running, url=%s mode=%s", event.Payload.URL, event.Payload.Mode)

	if event.Payload.Mode == "publish" || event.Payload.Mode == "publish_fail" {
		// event_publish (ADR-0121 §3): print the host's return code; in
		// publish_fail mode exit non-zero afterwards — the host must still
		// deliver what it accepted.
		tp, tl := ptrOf([]byte(event.Payload.PublishType))
		pp, pl := ptrOf(event.Payload.PublishPayload)
		code := eventPublish(tp, tl, pp, pl)
		fmt.Printf("{\"publish_code\":%d}\n", code)
		if event.Payload.Mode == "publish_fail" {
			os.Exit(1)
		}
		return
	}
	if event.Payload.Mode == "sleep" {
		// A long call (ADR-0121 §8): sleep, then answer.
		time.Sleep(time.Duration(event.Payload.SleepMS) * time.Millisecond)
		fmt.Println(`{"slept":true}`)
		return
	}
	if event.Payload.Mode == "job_progress" {
		// job_progress (ADR-0121 §3/§8): print the host's return code.
		kp, kl := ptrOf([]byte(event.Payload.ProgressKey))
		code := jobProgress(event.Payload.Pct, kp, kl)
		fmt.Printf("{\"progress_code\":%d}\n", code)
		return
	}
	if event.Payload.Mode == "http_retry" {
		runHTTPRetry(event.Payload.URL)
		return
	}
	if event.Payload.Mode == "http_retry_diff" {
		runHTTPRetryDiff(event.Payload.URLs)
		return
	}
	if event.Payload.Mode == "http_repeat_same" {
		runHTTPRepeatSame(event.Payload.URL)
		return
	}
	if event.Payload.Mode == "http_retry_then_repeat" {
		runHTTPRetryThenRepeat(event.Payload.URL)
		return
	}
	if event.Payload.Mode == "clock" {
		runClock()
		return
	}
	if event.Payload.Mode == "secret_set" {
		val := []byte(event.Payload.Value)
		if event.Payload.Size > 0 {
			val = make([]byte, event.Payload.Size)
			for i := range val {
				val[i] = 'x'
			}
		}
		if event.Payload.BadUTF8 {
			val = []byte{0xff, 0xfe, 'x'}
		}
		runSecretSet(event.Payload.Key, val)
		return
	}
	if event.Payload.Mode == "device_info" {
		runDeviceInfo(event.Payload.Cap)
		return
	}
	if event.Payload.Mode == "http_len" {
		runHTTPLen(event.Payload.URL)
		return
	}

	// Storage round-trip.
	setCode := set("greeting", []byte("hello from wasm"))
	got, _ := get("greeting")
	roundtrip := string(got) == "hello from wasm"

	// HTTP call (host enforces net:<host> permission).
	reqJSON, _ := json.Marshal(map[string]any{
		"method": event.Payload.Method, "url": event.Payload.URL, "body_b64": event.Payload.BodyB64,
	})
	rp, rl := ptrOf(reqJSON)
	buf := make([]byte, 300*1024)
	bp, bc := ptrOf(buf)
	httpCode := httpRequest(rp, rl, bp, bc)
	httpStatus := 0
	httpBody := ""
	if httpCode > 0 {
		var resp struct {
			Status  int    `json:"status"`
			BodyB64 string `json:"body_b64"`
		}
		if err := json.Unmarshal(buf[:httpCode], &resp); err == nil {
			httpStatus = resp.Status
			httpBody = resp.BodyB64
		}
	}

	// Read a plugin setting via the settings_get host function.
	skp, skl := ptrOf([]byte("endpoint"))
	sbuf := make([]byte, 4096)
	sbp, sbc := ptrOf(sbuf)
	sCode := settingsGet(skp, skl, sbp, sbc)
	settingVal := ""
	if sCode > 0 {
		settingVal = string(sbuf[:sCode])
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
	if code := set("results", results); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
	fmt.Println(string(results))
}

// runHTTPRetry is the ut-docs#754 proof: it issues ONE http_request call
// with a deliberately undersized destination buffer (4 bytes — far smaller
// than any real response), which per the buffer ABI returns the FULL
// response length and instructs the guest to "call again with a bigger
// buffer." It does exactly that: the SAME request bytes, a buffer big
// enough this time. The host-side test counts real HTTP hits at the
// server, so a fixed cache correctly serves the second call without
// touching the network again — while an unfixed host would hit the server
// twice for what the guest sees as one logical call.
func runHTTPRetry(url string) {
	reqJSON, _ := json.Marshal(map[string]any{"method": "GET", "url": url, "body_b64": ""})
	rp, rl := ptrOf(reqJSON)

	smallBuf := make([]byte, 4)
	sp, sc := ptrOf(smallBuf)
	firstCode := httpRequest(rp, rl, sp, sc)

	bigBuf := make([]byte, 64*1024)
	bp, bc := ptrOf(bigBuf)
	secondCode := httpRequest(rp, rl, bp, bc)

	secondBody := ""
	if secondCode > 0 {
		var resp struct {
			BodyB64 string `json:"body_b64"`
		}
		if err := json.Unmarshal(bigBuf[:secondCode], &resp); err == nil {
			secondBody = resp.BodyB64
		}
	}
	results, _ := json.Marshal(map[string]any{
		"first_code":  firstCode,
		"second_code": secondCode,
		"second_body": secondBody,
	})
	if code := set("results", results); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
	fmt.Println(string(results))
}

// runHTTPRetryDiff is the false-positive guard for the #754 cache: TWO
// genuinely different requests (different URLs), each with an adequately
// sized buffer up front — never an undersized-buffer retry. Both must
// reach the server: the cache must key on the exact request bytes, not
// just "the plugin made an http_request call before."
func runHTTPRetryDiff(urls []string) {
	results := map[string]any{}
	for i, u := range urls {
		reqJSON, _ := json.Marshal(map[string]any{"method": "GET", "url": u, "body_b64": ""})
		rp, rl := ptrOf(reqJSON)
		buf := make([]byte, 64*1024)
		bp, bc := ptrOf(buf)
		code := httpRequest(rp, rl, bp, bc)
		body := ""
		if code > 0 {
			var resp struct {
				BodyB64 string `json:"body_b64"`
			}
			if err := json.Unmarshal(buf[:code], &resp); err == nil {
				body = resp.BodyB64
			}
		}
		results[fmt.Sprintf("code_%d", i)] = code
		results[fmt.Sprintf("body_%d", i)] = body
	}
	out, _ := json.Marshal(results)
	if code := set("results", out); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// runHTTPRepeatSame is the ut-docs#754 review's F1 false-positive guard: it
// makes the SAME request TWICE in a row, each time with a generously sized
// buffer up front — never an undersized-buffer retry. This is a poll loop
// or a deliberate duplicate submission, not the buffer-ABI retry the #754
// cache exists for, and both calls must reach the server: caching every
// successful call unconditionally (the review's original diff) silently
// collapsed exactly this into one live call.
func runHTTPRepeatSame(url string) {
	reqJSON, _ := json.Marshal(map[string]any{"method": "GET", "url": url, "body_b64": ""})
	rp, rl := ptrOf(reqJSON)

	call := func() (int32, string) {
		buf := make([]byte, 64*1024)
		bp, bc := ptrOf(buf)
		code := httpRequest(rp, rl, bp, bc)
		body := ""
		if code > 0 {
			var resp struct {
				BodyB64 string `json:"body_b64"`
			}
			if err := json.Unmarshal(buf[:code], &resp); err == nil {
				body = resp.BodyB64
			}
		}
		return code, body
	}

	firstCode, firstBody := call()
	secondCode, secondBody := call()

	results, _ := json.Marshal(map[string]any{
		"first_code":  firstCode,
		"first_body":  firstBody,
		"second_code": secondCode,
		"second_body": secondBody,
	})
	if code := set("results", results); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
	fmt.Println(string(results))
}

// runHTTPRetryThenRepeat proves the cache clears once fully served: an
// undersized-buffer call (miss, caches on overflow) is followed by a
// big-buffer retry with the SAME bytes (hit, clears the cache since this
// buffer holds the whole response) — then a THIRD call, same bytes, big
// buffer again. That third call is not part of any pending retry; the
// cache must already be empty, so it goes out for real.
func runHTTPRetryThenRepeat(url string) {
	reqJSON, _ := json.Marshal(map[string]any{"method": "GET", "url": url, "body_b64": ""})
	rp, rl := ptrOf(reqJSON)

	smallBuf := make([]byte, 4)
	sp, sc := ptrOf(smallBuf)
	firstCode := httpRequest(rp, rl, sp, sc)

	callBig := func() (int32, string) {
		buf := make([]byte, 64*1024)
		bp, bc := ptrOf(buf)
		code := httpRequest(rp, rl, bp, bc)
		body := ""
		if code > 0 {
			var resp struct {
				BodyB64 string `json:"body_b64"`
			}
			if err := json.Unmarshal(buf[:code], &resp); err == nil {
				body = resp.BodyB64
			}
		}
		return code, body
	}
	secondCode, secondBody := callBig()
	thirdCode, thirdBody := callBig()

	results, _ := json.Marshal(map[string]any{
		"first_code":  firstCode,
		"second_code": secondCode,
		"second_body": secondBody,
		"third_code":  thirdCode,
		"third_body":  thirdBody,
	})
	if code := set("results", results); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
	fmt.Println(string(results))
}

// runHTTPLen (ut-docs#3226) fetches one URL through the buffer ABI the way a
// real guest fetching a large CRL would — a 4-byte probe returns the full
// length, then a retry into a buffer that size — and records only the
// status and the DECODED body length, so the host-side test can pin the
// response cap without round-tripping megabytes through plugin storage.
func runHTTPLen(url string) {
	reqJSON, _ := json.Marshal(map[string]any{"method": "GET", "url": url, "body_b64": ""})
	rp, rl := ptrOf(reqJSON)
	probe := make([]byte, 4)
	pp, pc := ptrOf(probe)
	code := httpRequest(rp, rl, pp, pc)
	res := map[string]any{"http_code": code}
	if code > 0 {
		buf := make([]byte, code)
		bp, bc := ptrOf(buf)
		code2 := httpRequest(rp, rl, bp, bc)
		res["http_code"] = code2
		if code2 == code {
			var resp struct {
				Status  int    `json:"status"`
				BodyB64 string `json:"body_b64"`
			}
			if err := json.Unmarshal(buf, &resp); err == nil {
				n := len(resp.BodyB64) / 4 * 3
				for i := len(resp.BodyB64) - 1; i >= 0 && resp.BodyB64[i] == '='; i-- {
					n--
				}
				res["http_status"] = resp.Status
				res["body_len"] = n
			}
		}
	}
	results, _ := json.Marshal(res)
	if c := set("results", results); c != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", c)
		os.Exit(1)
	}
	fmt.Println(string(results))
}

// runClock reports what the guest sees as wall and monotonic time
// (ADR-0121 §3: WASI clock_time_get must return the host's real clocks, not
// wazero's fake 2022-01-01 epoch).
func runClock() {
	start := time.Now()
	wall := start.UnixNano()
	time.Sleep(20 * time.Millisecond)
	elapsed := time.Since(start)
	results, _ := json.Marshal(map[string]any{
		"wall_unix_nano": wall,
		"elapsed_nano":   elapsed.Nanoseconds(),
	})
	if code := set("results", results); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
}

// runSecretSet calls secret_set(key, val), then reads the key back through
// settings_get, and records both codes.
func runSecretSet(key string, val []byte) {
	kp, kl := ptrOf([]byte(key))
	vp, vl := ptrOf(val)
	code := secretSet(kp, kl, vp, vl)
	sbuf := make([]byte, 128*1024)
	sbp, sbc := ptrOf(sbuf)
	getCode := settingsGet(kp, kl, sbp, sbc)
	got := ""
	if getCode > 0 {
		got = string(sbuf[:getCode])
	}
	results, _ := json.Marshal(map[string]any{
		"secret_code": code,
		"get_code":    getCode,
		"get_val":     got,
	})
	if c := set("results", results); c != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", c)
		os.Exit(1)
	}
}

// runDeviceInfo calls device_id_get, device_local_ips_get and
// device_timezone_get (ADR-0140) with a dstCap-byte buffer and records each
// return code and, when it fit, the bytes written.
func runDeviceInfo(dstCap int) {
	if dstCap <= 0 {
		dstCap = 4096
	}
	results := map[string]any{}
	for name, fn := range map[string]func(uint32, uint32) int32{
		"id":       deviceIDGet,
		"ips":      deviceLocalIPsGet,
		"timezone": deviceTimezoneGet,
	} {
		buf := make([]byte, dstCap)
		bp, bc := ptrOf(buf)
		code := fn(bp, bc)
		results[name+"_code"] = code
		if code > 0 && int(code) <= dstCap {
			results[name+"_val"] = string(buf[:code])
		}
	}
	out, _ := json.Marshal(results)
	if c := set("results", out); c != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", c)
		os.Exit(1)
	}
}
