// Package problemclass turns a raw problems-ring line (logging.Problem) into
// something an owner can read: an area, a severity and the locale keys of a
// plain reason and a next step (ut-docs#6978). It is pure (no DB, no network)
// and works on the log text, so it knows only the till's own subsystem
// prefixes; moving to keyed problems at the producers is a follow-up and only
// has to change the tables below.
package problemclass

import (
	"strings"

	"github.com/universaltill/universal-till/internal/logging"
)

// Area is the part of the till a problem belongs to.
type Area string

const (
	AreaCloudSync Area = "cloud_sync"
	AreaPlugins   Area = "plugins"
	AreaPayments  Area = "payments"
	AreaDatabase  Area = "database"
	AreaNetwork   Area = "network"
	AreaOther     Area = "other"
)

// Areas is the fixed display order.
var Areas = []Area{AreaCloudSync, AreaPlugins, AreaPayments, AreaDatabase, AreaNetwork, AreaOther}

// Severity of a problem line.
type Severity int

const (
	SeverityOK Severity = iota
	SeverityAttention
	SeverityFailed
)

// String is the stable id used in CSS classes, test ids and locale keys.
func (s Severity) String() string {
	switch s {
	case SeverityAttention:
		return "attention"
	case SeverityFailed:
		return "failed"
	}
	return "ok"
}

// SeverityOf maps a log level: ERROR and FATAL are Failed, anything else
// (WARN) Needs attention.
func SeverityOf(level string) Severity {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "ERROR", "FATAL":
		return SeverityFailed
	}
	return SeverityAttention
}

// Known identifies a specific, well-understood error ("" = none).
type Known string

const (
	KnownNotMainTill       Known = "not_main_till"
	KnownTillTooOld        Known = "till_too_old"
	KnownChecksumMismatch  Known = "checksum_mismatch"
	KnownPluginExit        Known = "plugin_exit"
	KnownNoCatalogSnapshot Known = "no_catalog_snapshot"
)

// Class is the classification of one problem.
type Class struct {
	Area      Area
	Severity  Severity
	Known     Known
	ReasonKey string
	NextKey   string
}

type knownRule struct {
	id          Known
	substrings  []string
	defaultArea Area
}

// knownRules are tried in order; the first with any substring in the
// lower-cased message wins.
var knownRules = []knownRule{
	{KnownNotMainTill, []string{"not_main_till"}, AreaCloudSync},
	{KnownTillTooOld, []string{"till_too_old"}, AreaCloudSync},
	{KnownChecksumMismatch, []string{"checksum mismatch"}, AreaPlugins},
	{KnownPluginExit, []string{"module closed with exit_code(", "wasm handler: exit code"}, AreaPlugins},
	{KnownNoCatalogSnapshot, []string{"no catalog snapshot"}, AreaPlugins},
}

type areaRule struct {
	area        Area
	msgPrefixes []string
	keyPrefixes []string
}

// areaRules are tried in order (cloud_sync first, so "cloudsync:" is cloud
// sync, while the LAN main-till link ("sync.*"
// keys) is network). Prefixes are lower case.
var areaRules = []areaRule{
	{AreaCloudSync, []string{"cloudsync", "[cloudsync]", "cloudlink", "cloud ", "catalogpull:", "catalog pull", "enrolment:", "enrol till:", "issuereport:", "stock upload", "releasenotes:"}, []string{"cloudsync", "cloud"}},
	{AreaPlugins, []string{"wasm", "[wasm", "plugin", "[plugin", "[pluginupdatescheduler]", "[pluginstore]", "[marketplaceinstaller]", "[verifier]", "[download]", "[revocationchecker]", "[installreporter]", "[rollback]", "catalog snapshot:", "builtinlayouts:", "[selfupdate]", "update v", "android update"}, []string{"plugin", "wasm", "selfupdate.", "androidupdate."}},
	{AreaPayments, []string{"paybroker:", "devicepay:", "tap:", "readerlink:", "card readers:", "payment", "psp"}, []string{"pay"}},
	{AreaDatabase, []string{"[backup]", "[housekeeping]", "offsitebackup:", "restore:", "restore jump:", "begin tx:", "db:", "database", "sqlite", "disk"}, []string{"disk.", "backup"}},
	{AreaNetwork, []string{"lantls:", "lan discovery:", "lan ", "join:", "grp:", "sync link:", "sync:", "sync pull:", "sync push:", "builtin locale bundle:", "sales mirror", "unexpected primary call", "network", "dns"}, []string{"lan", "grp", "net", "sync."}},
}

const keyPrefix = "backoffice.problems."

// Classify maps a problem to its area, severity and message keys.
func Classify(p logging.Problem) Class {
	m := strings.ToLower(strings.TrimSpace(p.Msg))
	key := strings.ToLower(p.Key)
	c := Class{Severity: SeverityOf(p.Level)}

	var known *knownRule
	for i := range knownRules {
		for _, s := range knownRules[i].substrings {
			if strings.Contains(m, s) {
				known = &knownRules[i]
				break
			}
		}
		if known != nil {
			break
		}
	}

	c.Area = AreaOther
	matched := false
	for _, r := range areaRules {
		if matchesArea(r, m, key) {
			c.Area, matched = r.area, true
			break
		}
	}
	if known != nil {
		c.Known = known.id
		if !matched {
			c.Area = known.defaultArea
		}
		c.ReasonKey = keyPrefix + string(known.id) + ".reason"
		c.NextKey = keyPrefix + string(known.id) + ".next"
		return c
	}
	c.ReasonKey = keyPrefix + "generic." + string(c.Area) + ".reason"
	c.NextKey = keyPrefix + "generic." + string(c.Area) + ".next"
	return c
}

func matchesArea(r areaRule, msg, key string) bool {
	for _, p := range r.msgPrefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	if key != "" {
		for _, p := range r.keyPrefixes {
			if strings.HasPrefix(key, p) {
				return true
			}
		}
	}
	return false
}

// AllKeys lists every reason/next locale key Classify can return (for the
// en.json coverage test).
func AllKeys() []string {
	var out []string
	for _, r := range knownRules {
		out = append(out, keyPrefix+string(r.id)+".reason", keyPrefix+string(r.id)+".next")
	}
	for _, a := range Areas {
		out = append(out, keyPrefix+"generic."+string(a)+".reason", keyPrefix+"generic."+string(a)+".next")
	}
	return out
}
