package logging

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/clock"
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
//
// Key, when set (WarnProblemf), names a condition that has a paired
// recovery; ResolveProblems(key) marks every entry for it Resolved once the
// condition clears, so the heartbeat stops reporting it as open while the
// line stays in the ring for bug-report bundles (ut-docs#2798).
type Problem struct {
	At       time.Time
	Level    string
	Msg      string
	Key      string
	Resolved bool
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

// OpenProblems returns the newest-first Problems that are still open: not
// resolved (ResolveProblems) and, for an unkeyed line, no older than maxAge
// before now. An unkeyed problem that hasn't repeated within maxAge has aged
// out — the cloud's "Attention needed" must not keep showing a condition the
// till got over hours ago (ut-docs#2798). A keyed one never ages out: it is
// logged once per condition and stays open until ResolveProblems says the
// condition is over — a main till down since Friday is still down on
// Sunday. maxAge <= 0 disables the age cut.
func OpenProblems(now time.Time, maxAge time.Duration) []Problem {
	var out []Problem
	for _, p := range Recent() {
		if p.Resolved {
			continue
		}
		if maxAge > 0 && p.Key == "" && now.Sub(p.At) > maxAge {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ResolveProblems marks every open Problem logged under key resolved and
// reports how many it closed; the caller logs the recovery (at INFO) when
// that is non-zero. An empty key resolves nothing.
func ResolveProblems(key string) int {
	if key == "" {
		return 0
	}
	recentMu.Lock()
	defer recentMu.Unlock()
	n := 0
	for i := range recentBuf {
		if recentBuf[i].Key == key && !recentBuf[i].Resolved {
			recentBuf[i].Resolved = true
			n++
		}
	}
	return n
}

func remember(level Level, key, msg string) {
	if level < Warn {
		return
	}
	recentMu.Lock()
	defer recentMu.Unlock()
	// clock.Now (not time.Now) so the back-office "recent problems" panel's
	// rendered timestamps are byte-stable under `make docs-shots` (ut-docs#930).
	// Outside the docs-shots harness clock.Now IS time.Now — real log time.
	recentBuf = append(recentBuf, Problem{At: clock.Now().UTC(), Level: level.String(), Msg: msg, Key: key})
	if len(recentBuf) > recentCap {
		recentBuf = recentBuf[len(recentBuf)-recentCap:]
	}
}

// logf is the internal helper.
func (l *Logger) logf(level Level, format string, args ...any) {
	l.logKeyf(level, "", format, args...)
}

// logKeyf is logf with a Problem key (see Problem.Key).
func (l *Logger) logKeyf(level Level, key, format string, args ...any) {
	if l == nil {
		return
	}
	if capture.Load() != nil {
		teeCapture(level, fmt.Sprintf(format, args...))
	}
	if level < l.level {
		return
	}

	ts := time.Now().Format(time.RFC3339)
	// Format: 2025-01-01T12:00:00Z [INFO] message
	msg := fmt.Sprintf(format, args...)
	remember(level, key, msg)
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

// WarnProblemf logs at WARN like Warnf, tagging the Problems entry with key:
// a condition with a paired recovery that calls ResolveProblems(key) when it
// clears (ut-docs#2798). Plain Warnf is for problems with no such recovery.
func (l *Logger) WarnProblemf(key, format string, args ...any) {
	l.logKeyf(Warn, key, format, args...)
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
