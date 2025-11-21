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

func (l *Logger) Fatalf(format string, args ...any) {
	l.logf(Error, format, args...)
}
