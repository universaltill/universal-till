package plugin

// File-rows import entries (ADR-0164 §2, ut-docs#4526). An import entry
// declaring "source": "file" parses the uploaded file and hands catalogue
// rows back with ImportRowsSubmit; the host applies them. Its
// import.requested.ask runs in the import sandbox — on the till and in
// ut-cloud's import host alike — where only FileImportHostFunctions act:
// every other host function answers ErrDenied (no network, no settings or
// secrets, no storage, no events). Test it natively with
// FakeHost.FileImport.

// FileImportHostFunctions is the whole host surface of a file-rows ask.
var FileImportHostFunctions = []string{"log_write", "import_file_size", "import_file_read", "import_file_close", "import_rows_submit"}

// ImportRequest is an import.requested.ask payload. A file entry gets
// FileHandle/FileName/FileSize; a file-rows ("file") or API ("api") entry
// also gets Mode, "preview" or "commit" — the host decides what a commit
// writes, so a plugin parses and submits the same rows either way.
type ImportRequest struct {
	EntryKey   string   `json:"entry_key"`
	Entities   []string `json:"entities"`
	FileHandle int32    `json:"file_handle"`
	FileName   string   `json:"file_name"`
	FileSize   int64    `json:"file_size"`
	Mode       string   `json:"mode"`
	Source     string   `json:"source"`
}

// File is the staged upload behind FileHandle (an io.Reader).
func (r ImportRequest) File() *ImportFile { return ImportFileHandle(r.FileHandle) }
