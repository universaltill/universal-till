package pages

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/clock"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/problemclass"
)

// View model of the back-office "Recent problems" card (ut-docs#6978): the
// raw problems ring, grouped by area with a plain reason and next step. Pure
// presentation; the ring and heartbeat are untouched.

type problemLine struct {
	Severity   problemclass.Severity
	SevKey     string // backoffice.problems.severity.<sev>
	TagClass   string // "warn" | "danger"
	ReasonKey  string
	NextKey    string
	Count      int
	CountLabel string // "3 times" (only meaningful when Count > 1)
	Newest     time.Time
	LastSeen   string   // "Last seen 14:32"
	Raw        []string // RFC 3339 UTC + level + Msg, newest first, never cut
	Snippet    string   // distinguishing start of the raw message, unclassified rows only
}

type problemGroup struct {
	Area     problemclass.Area
	TitleKey string
	Lines    []problemLine
}

type problemsView struct {
	Worst       problemclass.Severity
	WorstKey    string
	TagClass    string // "ok" | "warn" | "danger"
	Groups      []problemGroup
	SupportText string
}

// rawProblemLine is the verbatim, untruncated form shown under "technical
// details" and in the copy text.
func rawProblemLine(p logging.Problem) string {
	return p.At.UTC().Format(time.RFC3339) + " " + p.Level + " " + p.Msg
}

func sevKey(s problemclass.Severity) string { return "backoffice.problems.severity." + s.String() }

func tagClass(s problemclass.Severity) string {
	switch s {
	case problemclass.SeverityFailed:
		return "danger"
	case problemclass.SeverityAttention:
		return "warn"
	}
	return "ok"
}

// problemWhen renders local "15:04" for today, else the locale's date and
// time; digits follow the locale (review L1).
func problemWhen(t, now time.Time, locale string) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return httpx.LocalizeDigits(t.Format("15:04"), locale)
	}
	return httpx.FormatDateTime(t, locale)
}

// pageProblems is what the card shows: open problems, with the heartbeat's
// age cut on unkeyed lines so a stale one-off warning does not linger (they
// are never resolved otherwise). Keyed lines never age out.
func pageProblems(now time.Time) []logging.Problem {
	return logging.OpenProblems(now, problemReportMaxAge)
}

const snippetRunes = 80

// snippetOf is the first line of msg cut to snippetRunes runes.
func snippetOf(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	r := []rune(msg)
	if len(r) > snippetRunes {
		return string(r[:snippetRunes]) + "…"
	}
	return msg
}

// buildProblemsView expects problems newest first (logging.OpenProblems).
func buildProblemsView(locale string, problems []logging.Problem, startupLine string) problemsView {
	now := clock.Now()
	type bucket struct {
		area problemclass.Area
		line problemLine
	}
	var buckets []*bucket
	index := map[string]*bucket{}
	for _, p := range problems {
		c := problemclass.Classify(p)
		k := string(c.Area) + "|" + string(c.Known)
		if c.Known == "" {
			k = string(c.Area) + "|" + p.Msg
		}
		b := index[k]
		if b == nil {
			b = &bucket{area: c.Area, line: problemLine{ReasonKey: c.ReasonKey, NextKey: c.NextKey}}
			if c.Area == problemclass.AreaOther && c.Known == "" {
				b.line.Snippet = snippetOf(p.Msg)
			}
			index[k] = b
			buckets = append(buckets, b)
		}
		l := &b.line
		l.Count++
		l.Raw = append(l.Raw, rawProblemLine(p))
		if c.Severity > l.Severity {
			l.Severity = c.Severity
		}
		if p.At.After(l.Newest) {
			l.Newest = p.At
		}
	}

	v := problemsView{WorstKey: sevKey(problemclass.SeverityOK), TagClass: "ok"}
	for _, a := range problemclass.Areas {
		var lines []problemLine
		for _, b := range buckets {
			if b.area != a {
				continue
			}
			l := b.line
			l.SevKey = sevKey(l.Severity)
			l.TagClass = tagClass(l.Severity)
			l.LastSeen = fmt.Sprintf(httpx.T(locale, "backoffice.problems.last_seen"), problemWhen(l.Newest, now, locale))
			if l.Count > 1 {
				l.CountLabel = httpx.TM(locale, "backoffice.problems.count_plural", map[string]any{"count": l.Count})
			}
			if l.Severity > v.Worst {
				v.Worst = l.Severity
			}
			lines = append(lines, l)
		}
		if len(lines) == 0 {
			continue
		}
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].Newest.After(lines[j].Newest) })
		v.Groups = append(v.Groups, problemGroup{Area: a, TitleKey: "backoffice.problems.area." + string(a), Lines: lines})
	}
	v.WorstKey = sevKey(v.Worst)
	v.TagClass = tagClass(v.Worst)
	v.SupportText = problemsSupportText(locale, v, problems, startupLine)
	return v
}

// problemsSupportText is the plain-text block behind "Copy for support".
func problemsSupportText(locale string, v problemsView, problems []logging.Problem, startupLine string) string {
	t := func(k string) string { return httpx.T(locale, "backoffice.problems."+k) }
	var b strings.Builder
	b.WriteString(t("copy.title") + "\n")
	if startupLine != "" {
		b.WriteString(t("copy.till") + ": " + startupLine + "\n")
	}
	b.WriteString("\n")
	b.WriteString(t("copy.summary") + "\n")
	if len(v.Groups) == 0 {
		b.WriteString(t("copy.none") + "\n")
	}
	for _, g := range v.Groups {
		for _, l := range g.Lines {
			fmt.Fprintf(&b, "[%s] %s — %s | %s %s | %s",
				httpx.T(locale, g.TitleKey), httpx.T(locale, l.SevKey),
				httpx.T(locale, l.ReasonKey), t("next_label"), httpx.T(locale, l.NextKey),
				fmt.Sprintf(httpx.T(locale, "backoffice.problems.last_seen"), l.Newest.UTC().Format(time.RFC3339)))
			if l.Count > 1 {
				b.WriteString(" | " + l.CountLabel)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n" + t("copy.raw") + "\n")
	for _, p := range problems {
		b.WriteString(rawProblemLine(p) + "\n")
	}
	return b.String()
}
