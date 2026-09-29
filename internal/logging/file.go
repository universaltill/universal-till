package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ResolveFile decides whether the till writes a log file and where
// (ut-docs#2720). envVal is UT_LOG_FILE:
//
//   - "0"/"off"/"false"/"no" → no file;
//   - ""                      → on for desktop/server OSes at
//     <dataDir>/logs/<name>, off on android/ios (logcat / the OS log hold
//     it there, and the app sandbox isn't user-browsable);
//   - "1"/"on"/"true"/"yes"   → on at the default path, on any OS;
//   - anything else           → that file path, made absolute.
func ResolveFile(envVal, dataDir, goos, name string) (string, bool) {
	def := filepath.Join(dataDir, "logs", name)
	switch strings.ToLower(strings.TrimSpace(envVal)) {
	case "0", "off", "false", "no":
		return "", false
	case "1", "on", "true", "yes":
		return def, true
	case "":
		if goos == "android" || goos == "ios" {
			return "", false
		}
		return def, true
	}
	// A relative path is resolved now, so the log never lands in whatever
	// the working directory happens to be (a shortcut's "Start in", the
	// service manager's /). If even that fails, use the default path.
	abs, err := filepath.Abs(strings.TrimSpace(envVal))
	if err != nil {
		return def, true
	}
	return abs, true
}

// teeWriter writes to every writer and never fails: on a Windows GUI launch
// stdout/stderr are invalid handles, and an io.MultiWriter would stop at
// that first error and never reach the file — the exact field problem this
// file exists to fix.
type teeWriter []io.Writer

func (t teeWriter) Write(p []byte) (int, error) {
	for _, w := range t {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

var (
	fileMu   sync.Mutex
	fileSink *RotatingWriter
	filePath string
)

// AttachFile starts writing every log line — this package's logger AND the
// standard library "log" package many till packages still use — to a
// rotating file at path, in addition to stdout/stderr, all redacted. Calling it
// again switches files. app.go attaches the log file before taking the
// data-dir lock (ut-docs#1097), so a second instance that is about to be
// refused briefly appends to the same till.log — harmless (both writes are
// whole lines; the refused process exits), noted by ut-docs#2728.
func AttachFile(path string) error {
	w, err := NewRotatingWriter(path, DefaultMaxFileBytes, DefaultMaxFiles)
	if err != nil {
		return err
	}
	fileMu.Lock()
	old := fileSink
	fileSink, filePath = w, path
	fileMu.Unlock()
	// Redact once, then fan out: the console copy is as redacted as the
	// file (ut-docs#3145). teeWriter never fails, so neither does this.
	L().log.SetOutput(redactingWriter{w: teeWriter{os.Stdout, w}})
	log.SetOutput(redactingWriter{w: teeWriter{os.Stderr, w}})
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// DetachFile returns logging to (redacted) stdout/stderr only and closes
// the file.
func DetachFile() {
	fileMu.Lock()
	old := fileSink
	fileSink, filePath = nil, ""
	fileMu.Unlock()
	if old == nil {
		return
	}
	L().log.SetOutput(redactingWriter{w: os.Stdout})
	log.SetOutput(redactingWriter{w: os.Stderr})
	_ = old.Close()
}

// FilePath is the attached log file, "" when there is none.
func FilePath() string {
	fileMu.Lock()
	defer fileMu.Unlock()
	return filePath
}

// Stderr is where a process writes a plain diagnostic message (the desktop
// shell's fmt.Fprintln calls): redacted stderr plus the log file, with a
// timestamp, when one is attached (ut-docs#3145).
func Stderr() io.Writer {
	fileMu.Lock()
	defer fileMu.Unlock()
	if fileSink == nil {
		return redactingWriter{w: os.Stderr}
	}
	return redactingWriter{w: teeWriter{os.Stderr, timestampWriter{w: fileSink}}}
}

// timestampWriter prefixes each write with the same RFC3339 timestamp this
// package's logger uses, so plain fmt.Fprint messages line up in the file.
type timestampWriter struct{ w io.Writer }

func (t timestampWriter) Write(p []byte) (int, error) {
	line := make([]byte, 0, len(p)+26)
	line = append(line, time.Now().Format(time.RFC3339)...)
	line = append(line, ' ')
	line = append(line, p...)
	if _, err := t.w.Write(line); err != nil {
		return 0, err
	}
	return len(p), nil
}
