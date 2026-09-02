// Package recovery is the boot-failure recovery screen (ut-docs#1436,
// ADR-0075): when internal/app.Run hits a startup failure an operator could
// plausibly fix (retry, restore a backup, wait out a stale lock), it serves
// a minimal recovery page on the till's normal address instead of exiting —
// every shell (Android WebView, desktop native webview, Pi kiosk Chromium)
// already just points a browser view at that address, so this is the whole
// recovery UI for all of them, with no per-shell native code. See the ADR
// for the full design rationale and its explicitly-out-of-scope list
// (diagnostics-send, restore/reset actions, any shell-native change — those
// are ut-docs#1437/#1438/#1439/#1440).
package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/universaltill/universal-till/internal/db"
)

// Kind categorizes a startup failure for the recovery page's message and
// (later) which recovery actions apply — a migration failure gets safe-mode
// read access (this card), disk-full gets a pointed "free up space" message
// rather than a generic one, etc. Kept as a small closed string set rather
// than an error-code enum: this package's only consumers are its own HTML
// template and tests, not a cross-repo wire contract.
type Kind string

const (
	KindMigration Kind = "migration"
	KindDBOpen    Kind = "db_open"
	KindDiskFull  Kind = "disk_full"
)

// Failure is what the recovery page renders and a support call references.
type Failure struct {
	Kind Kind
	// Detail is the underlying error's message — shown on screen (this is
	// an operator/support-facing diagnostic screen, not customer-facing;
	// nothing here is a secret) so a phone call can read it out directly.
	Detail string
	// RefCode is a short, human-readable-over-the-phone code distinguishing
	// one failure occurrence from another. Not a security token — just
	// enough entropy that two different incidents don't look identical.
	RefCode string
}

// Classify decides whether a startup error is one recovery mode should
// handle (serve the recovery page, offer Retry) versus one that must stay a
// hard exit exactly as today.
//
// Deliberately NOT recoverable: db.ErrDataDirLocked. ut-docs#1097 fixed a
// real incident (two live servers against the same SQLite file for ~19
// minutes) by making a second process refuse to start outright — its own
// doc comment says call sites must treat it as fatal. Recovery mode's Retry
// re-attempts against the SAME address (never a different one, so it can't
// reproduce #1097's actual bug), but showing a friendly "Retry" button on a
// screen that could be read as "it's fine to keep trying" is a needless risk
// to take on a safety property that already works — internal/app.Run
// doesn't even route this error through Classify, it stays on the existing
// fatal path unchanged. This test-guards the classifier itself in case a
// future caller wires it in without re-reading this comment.
//
// Recoverable: a failure from the boot phase AFTER the data-directory lock
// is already safely held by this process — running migrations (#1412),
// applying a staged restore, or opening/pinging the SQLite file at all
// (corrupt file, disk full). All of these are operator-actionable (retry
// once the cause is fixed, restore a backup, free up disk) and none of them
// risk a second writer against the same data — this process already
// exclusively owns the data directory by the time any of them can fail.
func Classify(err error) (Failure, bool) {
	if err == nil {
		return Failure{}, false
	}
	if errors.Is(err, db.ErrDataDirLocked) {
		return Failure{}, false
	}

	msg := err.Error()
	kind := KindDBOpen
	switch {
	case strings.Contains(msg, "no space left on device"), strings.Contains(msg, "disk full"):
		kind = KindDiskFull
	case strings.Contains(msg, "run migrations:"), strings.Contains(msg, "exec migration"):
		kind = KindMigration
	}

	return Failure{
		Kind:    kind,
		Detail:  msg,
		RefCode: newRefCode(),
	}, true
}

// newRefCode returns a short, uppercase, phone-call-friendly code (e.g.
// "A3F9-2C1B"). Not cryptographically meaningful — just enough entropy to
// distinguish one incident from the next one; crypto/rand only because it's
// the stdlib's readily-available source of bytes, not for any security
// property.
func newRefCode() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	s := strings.ToUpper(hex.EncodeToString(b[:]))
	return s[:4] + "-" + s[4:]
}
