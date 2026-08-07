package logging

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Level represents the log level.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
	Fatal
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	case Fatal:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// parseLevel converts a string (e.g. "debug") into a Level.
func parseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug
	case "info":
		return Info
	case "warn", "warning":
		return Warn
	case "error":
		return Error
	case "fatal":
		return Fatal
	default:
		return Info
	}
}

// Logger wraps the standard log.Logger with levels.
type Logger struct {
	level Level
	log   *log.Logger
}

// global logger instance and once-init
var (
	defaultLogger *Logger
	once          sync.Once
)

// Init initializes the global logger based on environment variables.
// It is safe to call multiple times; initialization happens only once.
//
// Env vars:
//
//	UT_LOG_LEVEL   – debug|info|warn|error (default: info)
func Init() {
	once.Do(func() {
		lvl := parseLevel(os.Getenv("UT_LOG_LEVEL"))

		// You can swap os.Stdout with a file or multi-writer later.
		base := log.New(os.Stdout, "", 0)

		defaultLogger = &Logger{
			level: lvl,
			log:   base,
		}

		defaultLogger.Infof("logging initialised with level=%s", lvl.String())
	})
}

// L returns the global logger. It calls Init() lazily if needed.
func L() *Logger {
	if defaultLogger == nil {
		Init()
	}
	return defaultLogger
}

// Problem is one recent warn/error line, kept in memory so the cloud sync
// heartbeat can report the shop's problems (ADR-0018 problems feed). The
// buffer is process-local and small — it is a digest, not a log store.
type Problem struct {
	At    time.Time
	Level string
	Msg   string
}

const recentCap = 50

var recentMu sync.Mutex
var recentBuf []Problem

// Recent returns the newest-first warn/error lines seen by this process.
func Recent() []Problem {
	recentMu.Lock()
	defer recentMu.Unlock()
	out := make([]Problem, len(recentBuf))
	// Stored oldest-first; reverse on the way out.
	for i, p := range recentBuf {
		out[len(recentBuf)-1-i] = p
	}
	return out
}

// ResetRecent clears the Problems ring. Test-only: recentBuf is a single
// process-global buffer, so a package test asserting "exactly N Problems"
// (ut-docs#404) is otherwise at the mercy of whatever every other test in
// the same binary — run before it, or still finishing a background loop
// concurrently — happened to log, right up to silently wrapping the
// 50-entry cap and making an exact count meaningless. There is no
// production caller: nothing should ever want to discard the shop's
// problem history outside a test process.
func ResetRecent() {
	recentMu.Lock()
	defer recentMu.Unlock()
	recentBuf = nil
}

func remember(level Level, msg string) {
	if level < Warn {
		return
	}
	recentMu.Lock()
	defer recentMu.Unlock()
	recentBuf = append(recentBuf, Problem{At: time.Now().UTC(), Level: level.String(), Msg: msg})
	if len(recentBuf) > recentCap {
		recentBuf = recentBuf[len(recentBuf)-recentCap:]
	}
}

// logf is the internal helper.
func (l *Logger) logf(level Level, format string, args ...any) {
	if l == nil {
		return
	}
	if level < l.level {
		return
	}

	ts := time.Now().Format(time.RFC3339)
	// Format: 2025-01-01T12:00:00Z [INFO] message
	msg := fmt.Sprintf(format, args...)
	remember(level, msg)
	l.log.Printf("%s [%s] %s", ts, level.String(), msg)
}

// Public helpers:

func (l *Logger) Debugf(format string, args ...any) {
	l.logf(Debug, format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.logf(Info, format, args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	l.logf(Warn, format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.logf(Error, format, args...)
}

// Fatalf logs at Error level and then terminates the process. Callers rely on
// this NOT returning (e.g. main.go treats a failed db.Open as fatal and would
// otherwise fall through to a nil-pointer deref). Never make this return.
func (l *Logger) Fatalf(format string, args ...any) {
	l.logf(Error, format, args...)
	os.Exit(1)
}
