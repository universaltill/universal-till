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
// rotating, redacted file at path, in addition to stdout/stderr. Calling it
// again switches files.
func AttachFile(path string) error {
	w, err := NewRotatingWriter(path, DefaultMaxFileBytes, DefaultMaxFiles)
	if err != nil {
		return err
	}
	fileMu.Lock()
	old := fileSink
	fileSink, filePath = w, path
	fileMu.Unlock()
	red := redactingWriter{w: w}
	L().log.SetOutput(teeWriter{os.Stdout, red})
	log.SetOutput(teeWriter{os.Stderr, red})
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// DetachFile returns logging to stdout/stderr only and closes the file.
func DetachFile() {
	fileMu.Lock()
	old := fileSink
	fileSink, filePath = nil, ""
	fileMu.Unlock()
	if old == nil {
		return
	}
	L().log.SetOutput(os.Stdout)
	log.SetOutput(os.Stderr)
	_ = old.Close()
}

// FilePath is the attached log file, "" when there is none.
func FilePath() string {
	fileMu.Lock()
	defer fileMu.Unlock()
	return filePath
}

// Stderr is where a process writes a plain diagnostic message (the desktop
// shell's fmt.Fprintln calls): stderr plus the redacted log file when one
// is attached.
func Stderr() io.Writer {
	fileMu.Lock()
	defer fileMu.Unlock()
	if fileSink == nil {
		return os.Stderr
	}
	return teeWriter{os.Stderr, timestampWriter{w: redactingWriter{w: fileSink}}}
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
