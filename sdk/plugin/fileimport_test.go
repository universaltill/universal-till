//go:build !wasip1

package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// ADR-0164 §2 (ut-docs#4526): a file-rows import entry is tested natively
// under the same rule the till and ut-cloud enforce — FakeHost.FileImport
// leaves only FileImportHostFunctions callable.
func TestFakeHost_FileImportSandbox(t *testing.T) {
	h := NewFakeHost()
	h.FileImport = true
	h.Settings["k"] = "v"
	h.Storage["k"] = []byte("v")
	h.ImportFiles[3] = []byte("ref,title\n1,Tea\n")
	UseFakeHost(t, h)

	ev := Event{Type: "import.requested.ask", Payload: json.RawMessage(`{"entry_key":"export","entities":["items"],"file_handle":3,"file_name":"x.csv","file_size":16,"mode":"preview","source":"file"}`)}
	var req ImportRequest
	if err := ev.Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req.Mode != "preview" || req.Source != "file" || req.FileHandle != 3 || req.FileName != "x.csv" || req.FileSize != 16 || len(req.Entities) != 1 {
		t.Fatalf("decoded %+v", req)
	}
	b, err := io.ReadAll(req.File())
	if err != nil || string(b) != "ref,title\n1,Tea\n" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if _, err := ImportRowsSubmit("items", []map[string]any{{"external_id": "1", "name": "Tea"}}); err != nil {
		t.Fatalf("rows submit in the sandbox: %v", err)
	}
	Log("allowed")

	if _, err := SettingsGet("k"); !errors.Is(err, ErrDenied) {
		t.Errorf("settings_get in the sandbox: %v, want ErrDenied", err)
	}
	if _, err := StorageGet("k"); !errors.Is(err, ErrDenied) {
		t.Errorf("storage_get in the sandbox: %v, want ErrDenied", err)
	}
	h.HTTP = func(HTTPRequest) (HTTPResponse, error) { return HTTPResponse{Status: 200}, nil }
	if _, err := HTTP(HTTPRequest{Method: "GET", URL: "https://example.com/"}); !errors.Is(err, ErrDenied) {
		t.Errorf("http_request in the sandbox: %v, want ErrDenied", err)
	}
}

func TestFileImportHostFunctions(t *testing.T) {
	want := map[string]bool{"log_write": true, "import_file_size": true, "import_file_read": true, "import_file_close": true, "import_rows_submit": true}
	if len(FileImportHostFunctions) != len(want) {
		t.Fatalf("FileImportHostFunctions = %v", FileImportHostFunctions)
	}
	for _, f := range FileImportHostFunctions {
		if !want[f] {
			t.Errorf("unexpected %q", f)
		}
	}
}

// Every SDK call the sandbox refuses really is refused by the fake, even
// the ones a FakeHost.Deny entry cannot reach.
func TestFakeHost_FileImportRefusesTheRest(t *testing.T) {
	h := NewFakeHost()
	h.FileImport = true
	h.InJob = true
	h.DeviceTimezone = "+01:00"
	h.Uploads["0123456789abcdef0123456789abcdef"] = []byte("x")
	UseFakeHost(t, h)
	if _, err := DeviceTimezone(); !errors.Is(err, ErrDenied) {
		t.Errorf("device_timezone_get: %v", err)
	}
	if err := JobProgress(10, ""); !errors.Is(err, ErrDenied) {
		t.Errorf("job_progress: %v", err)
	}
	if _, err := UploadOpen("0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrDenied) {
		t.Errorf("upload_open: %v", err)
	}
	if err := EventPublish("com.example.plugin.x", nil); !errors.Is(err, ErrDenied) {
		t.Errorf("event_publish: %v", err)
	}
	if _, err := BlobList(); !errors.Is(err, ErrDenied) {
		t.Errorf("blob_list: %v", err)
	}
}

// The close calls are refused in the sandbox too (-2), as the till's
// import sandbox answers every host function outside
// FileImportHostFunctions — a fake that answered 0 would hide it.
func TestFakeHost_FileImportRefusesCloses(t *testing.T) {
	h := NewFakeHost()
	h.FileImport = true
	UseFakeHost(t, h)
	if err := (&HTTPStream{h: 1}).Close(); !errors.Is(err, ErrDenied) {
		t.Errorf("http_close: %v", err)
	}
	if err := (&TCPConn{h: 1}).Close(); !errors.Is(err, ErrDenied) {
		t.Errorf("tcp_close: %v", err)
	}
	if err := (&Upload{h: 1}).Close(); !errors.Is(err, ErrDenied) {
		t.Errorf("upload_close: %v", err)
	}
}

// ut-docs#4472: the ask carries the operator's UI language; an older till
// omits it and Locale decodes empty.
func TestImportRequest_DecodesLocale(t *testing.T) {
	var req ImportRequest
	if err := json.Unmarshal([]byte(`{"entry_key":"export","mode":"preview","locale":"de"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Locale != "de" {
		t.Fatalf("Locale = %q, want de", req.Locale)
	}
	var old ImportRequest
	if err := json.Unmarshal([]byte(`{"entry_key":"export"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Locale != "" {
		t.Fatalf("Locale = %q from a pre-#4472 till, want empty", old.Locale)
	}
}
