package execute

// E1 — read access to Google Calendar via the per-calendar "Secret address in
// iCal format" URL. This sidesteps OAuth completely: the URL is itself a
// secret bearer token. The manager pastes it into the Admin UI; we store it
// alongside the existing Gmail demo credential JSON blob.
//
// The parser is intentionally minimal — it covers the subset of RFC 5545
// (iCalendar) that Google Calendar emits for a single-account export:
//
//   • Line unfolding: per RFC 5545 §3.1, a single logical line MAY be split
//     across multiple physical lines by inserting a CRLF followed by a
//     single linear-whitespace character (space or tab). The parser joins
//     these before tokenising.
//   • VEVENT extraction: only top-level VEVENT blocks are recognised.
//     VTIMEZONE, VALARM, and VFREEBUSY blocks are skipped.
//   • DTSTART / DTEND: three forms are recognised —
//        DTSTART:20260512T220000Z              (UTC)
//        DTSTART;TZID=America/Los_Angeles:20260512T150000 (floating in zone)
//        DTSTART;VALUE=DATE:20260512           (all-day)
//   • SUMMARY, LOCATION, DESCRIPTION: best-effort string fields.
//   • RRULE: a single-line passthrough is preserved in the output so the
//     downstream LLM/UX can note that the event recurs; expanding recurrence
//     into concrete instances is intentionally out-of-scope for v1.
//
// What this DOES NOT do (deliberate v1 scope):
//   • Expand recurring events. A weekly meeting will surface ONCE with its
//     base DTSTART and an "rrule" string field. Sufficient for the FP
//     demo prompt "what is my next event" provided the user has at least
//     one non-recurring event in range (we tell them so in the demo notes).
//   • Honour EXDATE / RDATE.
//   • Resolve TZID against the VTIMEZONE block — we trust the iso label and
//     return the raw datetime + zone label; the LLM can format from there.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// execGmailCalendarList fetches the manager-configured "Secret address in
// iCal format" URL, parses upcoming VEVENTs, and returns the next N sorted
// by start time. Past events are filtered out.
//
// Parameters:
//   - max_results (optional, default 10, max 50)
//   - within_days (optional, default 30) — upper bound on how far ahead to
//     return events. Keeps the response tight when a calendar contains
//     many far-future events.
func execGmailCalendarList(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGmailCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	if cred.CalendarICalURL == "" {
		return opResult{
			err: fmt.Errorf("calendar iCal URL not configured — manager must paste the calendar's " +
				"'Secret address in iCal format' from Google Calendar settings into the Admin UI"),
			code: ErrCodeCredentialUnavailable,
		}
	}

	maxResults := 10
	if v, ok := params["max_results"]; ok {
		switch n := v.(type) {
		case float64:
			maxResults = int(n)
		case int:
			maxResults = n
		}
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 50 {
		maxResults = 50
	}

	withinDays := 30
	if v, ok := params["within_days"]; ok {
		switch n := v.(type) {
		case float64:
			withinDays = int(n)
		case int:
			withinDays = n
		}
	}
	if withinDays < 1 {
		withinDays = 1
	}
	if withinDays > 365 {
		withinDays = 365
	}

	body, statusCode, err := httpGET(ctx, cred.CalendarICalURL, nil)
	if err != nil {
		return opResult{err: fmt.Errorf("calendar fetch failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("calendar fetch returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	events := parseICalEvents(string(body))
	now := time.Now().UTC()
	horizon := now.AddDate(0, 0, withinDays)
	upcoming := make([]icalEvent, 0, len(events))
	for _, ev := range events {
		// Use end-time when present, otherwise start-time, to avoid
		// hiding an in-progress event that started yesterday but ends
		// today.
		ref := ev.Start
		if !ev.End.IsZero() {
			ref = ev.End
		}
		if ref.Before(now) {
			continue
		}
		if ev.Start.After(horizon) {
			continue
		}
		upcoming = append(upcoming, ev)
	}
	sort.Slice(upcoming, func(i, j int) bool {
		return upcoming[i].Start.Before(upcoming[j].Start)
	})
	if len(upcoming) > maxResults {
		upcoming = upcoming[:maxResults]
	}

	out := make([]map[string]any, 0, len(upcoming))
	for _, ev := range upcoming {
		item := map[string]any{
			"summary":  ev.Summary,
			"start":    ev.Start.Format(time.RFC3339),
			"all_day":  ev.AllDay,
			"timezone": ev.TZID,
		}
		if !ev.End.IsZero() {
			item["end"] = ev.End.Format(time.RFC3339)
		}
		if ev.Location != "" {
			item["location"] = ev.Location
		}
		if ev.Description != "" {
			item["description"] = ev.Description
		}
		if ev.RRule != "" {
			item["rrule"] = ev.RRule
		}
		out = append(out, item)
	}
	return opResult{result: map[string]any{
		"events":       out,
		"event_count":  len(out),
		"now":          now.Format(time.RFC3339),
		"horizon_days": withinDays,
		// Heads-up surfaced to the agent so its reply can caveat
		// the recurring-event limitation when relevant.
		"recurring_event_note": "Recurring events appear only with their base DTSTART; the 'rrule' field indicates recurrence but is not expanded.",
	}}
}

// icalEvent is the normalised shape we return per VEVENT.
type icalEvent struct {
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	TZID        string // raw TZID label if present (e.g. "America/Los_Angeles")
	RRule       string // raw RRULE string if present
}

// parseICalEvents extracts VEVENT blocks from an iCalendar document.
// Robustness: unknown properties are ignored; malformed VEVENTs are
// skipped rather than failing the whole parse.
func parseICalEvents(body string) []icalEvent {
	lines := unfoldICalLines(body)
	var events []icalEvent
	var current *icalEvent
	depth := 0 // nesting depth so VALARM inside VEVENT doesn't confuse us
	inEvent := false
	for _, line := range lines {
		switch {
		case strings.EqualFold(line, "BEGIN:VEVENT"):
			current = &icalEvent{}
			inEvent = true
			depth = 1
		case strings.EqualFold(line, "END:VEVENT") && inEvent:
			if current != nil && !current.Start.IsZero() {
				events = append(events, *current)
			}
			current = nil
			inEvent = false
			depth = 0
		case inEvent && strings.HasPrefix(strings.ToUpper(line), "BEGIN:"):
			depth++
		case inEvent && strings.HasPrefix(strings.ToUpper(line), "END:"):
			depth--
		case inEvent && depth == 1:
			applyICalProperty(current, line)
		}
	}
	return events
}

// unfoldICalLines collapses RFC 5545 folded continuation lines and trims
// trailing CR. A continuation line is one whose first character is a space
// or horizontal tab — that whitespace is dropped and the remainder is
// appended to the prior logical line.
func unfoldICalLines(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	raw := strings.Split(body, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += line[1:]
			continue
		}
		out = append(out, line)
	}
	return out
}

// applyICalProperty parses a single iCal property line and updates ev.
// Property format: NAME[;PARAM=val;...]:VALUE
func applyICalProperty(ev *icalEvent, line string) {
	if ev == nil {
		return
	}
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return
	}
	head := line[:colon]
	value := line[colon+1:]
	semi := strings.IndexByte(head, ';')
	name := head
	params := ""
	if semi >= 0 {
		name = head[:semi]
		params = head[semi+1:]
	}
	switch strings.ToUpper(name) {
	case "SUMMARY":
		ev.Summary = unescapeICalText(value)
	case "DESCRIPTION":
		ev.Description = unescapeICalText(value)
	case "LOCATION":
		ev.Location = unescapeICalText(value)
	case "DTSTART":
		t, allDay, tzid := parseICalDateTime(value, params)
		ev.Start = t
		ev.AllDay = allDay
		if tzid != "" {
			ev.TZID = tzid
		}
	case "DTEND":
		t, _, tzid := parseICalDateTime(value, params)
		ev.End = t
		if ev.TZID == "" && tzid != "" {
			ev.TZID = tzid
		}
	case "RRULE":
		ev.RRule = value
	}
}

// parseICalDateTime returns (utc-time, all-day, tzid-label). It accepts:
//   - YYYYMMDDTHHMMSSZ                — explicit UTC
//   - YYYYMMDDTHHMMSS with TZID param — local time in the named zone
//   - YYYYMMDDTHHMMSS without TZID    — floating; assumed UTC for sorting
//   - YYYYMMDD with VALUE=DATE        — all-day
//
// Failed parses return a zero time; callers filter those out.
func parseICalDateTime(value, params string) (time.Time, bool, string) {
	tzid := ""
	valueType := ""
	for _, p := range strings.Split(params, ";") {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.ToUpper(kv[0]) {
		case "TZID":
			tzid = kv[1]
		case "VALUE":
			valueType = strings.ToUpper(kv[1])
		}
	}
	value = strings.TrimSpace(value)
	if strings.EqualFold(valueType, "DATE") || (len(value) == 8 && !strings.Contains(value, "T")) {
		if t, err := time.Parse("20060102", value); err == nil {
			return t.UTC(), true, tzid
		}
		return time.Time{}, true, tzid
	}
	if strings.HasSuffix(value, "Z") {
		if t, err := time.Parse("20060102T150405Z", value); err == nil {
			return t.UTC(), false, tzid
		}
		return time.Time{}, false, tzid
	}
	loc := time.UTC
	if tzid != "" {
		if l, err := time.LoadLocation(tzid); err == nil {
			loc = l
		}
	}
	if t, err := time.ParseInLocation("20060102T150405", value, loc); err == nil {
		return t.UTC(), false, tzid
	}
	return time.Time{}, false, tzid
}

// unescapeICalText reverses the RFC 5545 §3.3.11 text escaping rules used
// in SUMMARY / DESCRIPTION / LOCATION. The full list is small: \n, \\,
// \;, \,. Everything else is passed through unchanged.
func unescapeICalText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
				i++
				continue
			case '\\', ';', ',':
				b.WriteByte(s[i+1])
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
