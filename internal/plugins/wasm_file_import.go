package plugins

// The import sandbox (ADR-0164 §2, ut-docs#4526). An import entry declaring
// "source": "file" is a file-rows entry: it parses the uploaded file and
// hands ADR-0051 rows back through import_rows_submit, and core applies them
// with internal/catsync. Its import.requested.ask carries a file sink
// (NewFileImportRowSink), and that instance may call only
// FileImportHostFunctions: stateFrom finds no caller for any other host
// function, which answers denied (stateErr) without acting — no network
// (http_*, tcp_*), no settings or secrets, no storage, blobs, views, events,
// proposals, device info or uploads. WASI has no preopened directory and no
// environment for any plugin (wasm_runtime.go).
//
// The point is parity: ut-cloud's import host serves the same five
// functions to the same signed .wasm, so a file entry that works on a till
// works in the cloud, and neither can reach a credential.

// FileImportHostFunctions is the whole host surface of a file-rows ask. It
// mirrors the guest SDK's plugin.FileImportHostFunctions
// (TestFileImportHostFunctions_MirrorSDK) and ut-cloud's import host.
var FileImportHostFunctions = []string{"log_write", "import_file_size", "import_file_read", "import_file_close", "import_rows_submit"}

// ImportFileRowsMaxBytes caps the file a file-rows entry is handed: 32 MiB,
// the same bound ut-cloud's import job has (ADR-0164 §4), so a file the
// till takes is one the cloud takes. Its rows are capped by
// ImportRowsPerRun / ImportBytesPerRun like any import run.
const ImportFileRowsMaxBytes int64 = 32 << 20
