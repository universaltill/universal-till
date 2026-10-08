//go:build !wasip1

package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDispatchAnswersByEventType(t *testing.T) {
	h := Handlers{
		"tax.rate.ask": func(e Event) (any, error) {
			var p struct {
				SKU string `json:"sku"`
			}
			if err := e.Decode(&p); err != nil {
				return nil, err
			}
			return map[string]any{"sku": p.SKU, "rate_bp": 2000}, nil
		},
	}
	out, err := Dispatch(h, []byte(`{"id":"1","type":"tax.rate.ask","payload":{"sku":"A1"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `{"rate_bp":2000,"sku":"A1"}`; got != want {
		t.Fatalf("answer = %s, want %s", got, want)
	}
}

func TestDispatchFallsBackToArgType(t *testing.T) {
	h := Handlers{"x.ask": func(Event) (any, error) { return json.RawMessage(`{"ok":1}`), nil }}
	out, err := Dispatch(h, []byte(`{"payload":{}}`), "x.ask")
	if err != nil || string(out) != `{"ok":1}` {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestDispatchUnknownEventIsNoOpinion(t *testing.T) {
	out, err := Dispatch(Handlers{}, []byte(`{"type":"nobody.listens"}`), "")
	if err != nil || out != nil {
		t.Fatalf("out=%q err=%v, want no answer and no error", out, err)
	}
}

func TestDispatchCatchAll(t *testing.T) {
	h := Handlers{"*": func(e Event) (any, error) { return []byte(e.Type), nil }}
	out, _ := Dispatch(h, []byte(`{"type":"a.b"}`), "")
	if string(out) != "a.b" {
		t.Fatalf("out=%q", out)
	}
}

func TestDispatchNilAnswerWritesNothing(t *testing.T) {
	h := Handlers{"e": func(Event) (any, error) { return nil, nil }}
	out, err := Dispatch(h, []byte(`{"type":"e"}`), "")
	if err != nil || out != nil {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestDispatchHandlerErrorKeepsAnswer(t *testing.T) {
	h := Handlers{"e": func(Event) (any, error) { return map[string]int{"code": -5}, ExitCode(3) }}
	out, err := Dispatch(h, []byte(`{"type":"e"}`), "")
	if string(out) != `{"code":-5}` {
		t.Fatalf("out=%s", out)
	}
	var ec ExitCode
	if !errors.As(err, &ec) || ec != 3 {
		t.Fatalf("err=%v, want ExitCode(3)", err)
	}
}

func TestDispatchBadEventJSON(t *testing.T) {
	if _, err := Dispatch(Handlers{}, []byte(`{`), ""); err == nil {
		t.Fatal("want an error for malformed event JSON")
	}
}

func TestStorageRoundTripAndNotFound(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	if _, err := StorageGet("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: err=%v, want ErrNotFound", err)
	}
	if err := StorageSet("k", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	v, err := StorageGet("k")
	if err != nil || string(v) != "v1" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}

// A value larger than the first buffer comes back whole: the retry with the
// reported size runs in the shared wrapper, so this exercises the wasm path's
// logic too.
func TestBufferABIRetriesOnceWithReportedSize(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	big := bytes.Repeat([]byte("x"), defaultBuf*3+7)
	h.Storage["big"] = big
	v, err := StorageGet("big")
	if err != nil || !bytes.Equal(v, big) {
		t.Fatalf("len=%d err=%v, want %d bytes", len(v), err, len(big))
	}
	if got := h.Calls["storage_get"]; got != 2 {
		t.Fatalf("storage_get calls = %d, want 2 (first + one retry)", got)
	}
}

func TestBufferABIGrowingValueIsTruncatedError(t *testing.T) {
	n := 0
	_, err := bufCall("op", 4, func(dst []byte) int32 {
		n++
		return int32(10 * n) // always bigger than the buffer it was given
	})
	if !errors.Is(err, ErrTruncated) || n != 2 {
		t.Fatalf("err=%v calls=%d, want ErrTruncated after exactly 2 calls", err, n)
	}
}

func TestDeniedFunctionReturnsErrDenied(t *testing.T) {
	h := NewFakeHost()
	h.Deny["storage_set"] = true
	UseFakeHost(t, h)
	err := StorageSet("k", []byte("v"))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err=%v, want ErrDenied", err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Op != "storage_set" || e.Code != -2 {
		t.Fatalf("err=%#v, want *Error{Op: storage_set, Code: -2}", err)
	}
}

func TestSettingsAndSecretSet(t *testing.T) {
	h := NewFakeHost()
	h.Settings["endpoint"] = "https://erp.example"
	h.SecretSettings["token"] = true
	UseFakeHost(t, h)
	v, err := SettingsGet("endpoint")
	if err != nil || v != "https://erp.example" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if err := SecretSet("token", "s3cr3t"); err != nil {
		t.Fatal(err)
	}
	if v, _ := SettingsGet("token"); v != "s3cr3t" {
		t.Fatalf("secret not readable via settings_get: %q", v)
	}
	if h.Secrets["token"] != "s3cr3t" {
		t.Fatalf("Secrets = %v", h.Secrets)
	}
}

func TestHTTPRequestRoundTrip(t *testing.T) {
	h := NewFakeHost()
	h.HTTP = func(r HTTPRequest) (HTTPResponse, error) {
		if r.Method != "POST" || r.URL != "https://api.example/x" || string(r.Body) != "hi" || r.Headers["A"] != "b" {
			t.Errorf("request = %+v", r)
		}
		return HTTPResponse{Status: 201, Headers: map[string]string{"X": "y"}, Body: []byte("made")}, nil
	}
	UseFakeHost(t, h)
	resp, err := HTTP(HTTPRequest{Method: "POST", URL: "https://api.example/x", Headers: map[string]string{"A": "b"}, Body: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 || string(resp.Body) != "made" || resp.Headers["X"] != "y" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHTTPNoHookIsDenied(t *testing.T) {
	UseFakeHost(t, NewFakeHost())
	if _, err := HTTP(HTTPRequest{URL: "https://a.example"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("err=%v, want ErrDenied", err)
	}
}

func TestHTTPStreamWriteStatusRead(t *testing.T) {
	h := NewFakeHost()
	h.HTTP = func(r HTTPRequest) (HTTPResponse, error) {
		return HTTPResponse{Status: 200, Body: append([]byte("echo:"), r.Body...)}, nil
	}
	UseFakeHost(t, h)
	s, err := HTTPOpen(HTTPRequest{Method: "POST", URL: "https://a.example/s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status()
	if err != nil || st.Status != 200 {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	body, err := io.ReadAll(s)
	if err != nil || string(body) != "echo:abc" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second close err=%v, want ErrNotFound", err)
	}
}

func TestViewQueryEventPublishJobProgress(t *testing.T) {
	h := NewFakeHost()
	h.Views = func(name string, args json.RawMessage) (json.RawMessage, error) {
		if name != "sales.by_day.v1" || string(args) != `{"days":7}` {
			t.Errorf("view %s %s", name, args)
		}
		return json.RawMessage(`[{"day":"2026-10-08"}]`), nil
	}
	UseFakeHost(t, h)
	rows, err := ViewQuery("sales.by_day.v1", map[string]int{"days": 7})
	if err != nil || string(rows) != `[{"day":"2026-10-08"}]` {
		t.Fatalf("rows=%s err=%v", rows, err)
	}
	if err := EventPublish("com.example.plugin.done", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if len(h.Published) != 1 || h.Published[0].Type != "com.example.plugin.done" || string(h.Published[0].Payload) != `{"n":1}` {
		t.Fatalf("published = %+v", h.Published)
	}
	if err := JobProgress(50, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outside a job: err=%v, want ErrNotFound", err)
	}
	h.InJob = true
	if err := JobProgress(150, "k"); err != nil {
		t.Fatal(err)
	}
	if len(h.Progress) != 1 || h.Progress[0].Pct != 100 {
		t.Fatalf("progress = %+v", h.Progress)
	}
}

func TestBlobs(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	w, err := BlobCreate("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Blobs["a.txt"]; ok {
		t.Fatal("blob visible before commit")
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	r, err := BlobOpen("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "hello" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	list, err := BlobList()
	if err != nil || len(list) != 1 || list[0].Name != "a.txt" || list[0].Size != 5 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if err := BlobDelete("a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := BlobOpen("a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

func TestUploadAndImportFile(t *testing.T) {
	h := NewFakeHost()
	tok := strings.Repeat("ab", 16)
	h.Uploads[tok] = []byte("csv,data")
	h.ImportFiles[7] = []byte("sku;name\n")
	UseFakeHost(t, h)

	u, err := UploadOpen(tok)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(u)
	if string(b) != "csv,data" {
		t.Fatalf("upload = %q", b)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := UploadOpen(tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("consumed token: err=%v, want ErrNotFound", err)
	}

	f := ImportFileHandle(7)
	if n, err := f.Size(); err != nil || n != 9 {
		t.Fatalf("size=%d err=%v", n, err)
	}
	b, _ = io.ReadAll(f)
	if string(b) != "sku;name\n" {
		t.Fatalf("import = %q", b)
	}
	_ = f.Close()
}

func TestTCP(t *testing.T) {
	h := NewFakeHost()
	var sent bytes.Buffer
	h.TCP = func(host string, port int) (io.ReadWriteCloser, error) {
		if host != "192.168.1.50" || port != 20007 {
			t.Errorf("dial %s:%d", host, port)
		}
		return &loopConn{in: strings.NewReader("ACK"), out: &sent}, nil
	}
	UseFakeHost(t, h)
	c, err := TCPOpen("192.168.1.50", 20007, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte{0x06, 0x00}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "ACK" {
		t.Fatalf("read %q err=%v", buf[:n], err)
	}
	if sent.String() != "\x06\x00" {
		t.Fatalf("sent %q", sent.String())
	}
	if again := TCPConnHandle(c.Handle()); again.Handle() != c.Handle() {
		t.Fatal("handle round trip")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLogRecorded(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	Logf("n=%d", 3)
	if len(h.Logs) != 1 || h.Logs[0] != "n=3" {
		t.Fatalf("logs = %v", h.Logs)
	}
}

type loopConn struct {
	in  io.Reader
	out io.Writer
}

func (c *loopConn) Read(p []byte) (int, error)  { return c.in.Read(p) }
func (c *loopConn) Write(p []byte) (int, error) { return c.out.Write(p) }
func (c *loopConn) Close() error                { return nil }

func TestEventTime(t *testing.T) {
	var got Event
	h := Handlers{"e": func(e Event) (any, error) { got = e; return nil, nil }}
	if _, err := Dispatch(h, []byte(`{"type":"e","timestamp":"2026-10-08T19:04:05.123Z"}`), ""); err != nil {
		t.Fatal(err)
	}
	ts, err := got.Time()
	if err != nil || ts.Year() != 2026 || ts.Nanosecond() != 123000000 {
		t.Fatalf("Time() = %v, %v", ts, err)
	}
}

// A view result is never over 256 KiB (the host answers ErrQuota past it), and
// each view_query call counts against the per-event cap — so even the largest
// allowed result must cost exactly one call.
func TestViewQueryLargestResultIsOneCall(t *testing.T) {
	h := NewFakeHost()
	big := append([]byte(`["`), append(bytes.Repeat([]byte("x"), viewMax-4), '"', ']')...)
	h.Views = func(string, json.RawMessage) (json.RawMessage, error) { return big, nil }
	UseFakeHost(t, h)
	out, err := ViewQuery("items.top.v1", nil)
	if err != nil || len(out) != viewMax {
		t.Fatalf("len=%d err=%v", len(out), err)
	}
	if n := h.Calls["view_query"]; n != 1 {
		t.Fatalf("view_query calls = %d, want 1", n)
	}
}
