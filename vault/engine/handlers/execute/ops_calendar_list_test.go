package execute

import (
	"strings"
	"testing"
	"time"
)

// Minimal calendar covering the three DTSTART forms the parser must handle
// (UTC, TZID, all-day-DATE), an in-progress event whose end-time keeps it in
// the upcoming window, a folded SUMMARY line, and a VTIMEZONE block that
// must be skipped without leaking properties into the parse.
const sampleICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//cerberOS test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:America/Los_Angeles\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19701101T020000\r\n" +
	"TZOFFSETFROM:-0700\r\n" +
	"TZOFFSETTO:-0800\r\n" +
	"TZNAME:PST\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:utc-event@cerberos.test\r\n" +
	"SUMMARY:UTC Demo\r\n" +
	"DTSTART:21000101T100000Z\r\n" +
	"DTEND:21000101T110000Z\r\n" +
	"LOCATION:Zoom\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:tz-event@cerberos.test\r\n" +
	// Producer puts the inter-word space BEFORE the CRLF; the
	// continuation's leading whitespace is dropped during unfolding,
	// so the reconstructed SUMMARY reads naturally.
	"SUMMARY:Zoned Demo with a really long title that \r\n" +
	" got folded onto two lines\r\n" +
	"DTSTART;TZID=America/Los_Angeles:21000102T150000\r\n" +
	"DTEND;TZID=America/Los_Angeles:21000102T160000\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:allday-event@cerberos.test\r\n" +
	"SUMMARY:All-day Demo\r\n" +
	"DTSTART;VALUE=DATE:21000103\r\n" +
	"DTEND;VALUE=DATE:21000104\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:recurring-event@cerberos.test\r\n" +
	"SUMMARY:Weekly sync\r\n" +
	"DTSTART:21000105T170000Z\r\n" +
	"DTEND:21000105T173000Z\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=MO\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:past-event@cerberos.test\r\n" +
	"SUMMARY:Already Happened\r\n" +
	"DTSTART:19990101T100000Z\r\n" +
	"DTEND:19990101T110000Z\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestParseICalEvents_AllFormsAndFolding(t *testing.T) {
	events := parseICalEvents(sampleICS)
	if got, want := len(events), 5; got != want {
		t.Fatalf("expected %d VEVENTs (VTIMEZONE must be skipped), got %d", want, got)
	}

	byUID := func(summary string) *icalEvent {
		for i := range events {
			if events[i].Summary == summary {
				return &events[i]
			}
		}
		return nil
	}

	utc := byUID("UTC Demo")
	if utc == nil {
		t.Fatal("UTC event missing")
	}
	wantStart := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	if !utc.Start.Equal(wantStart) {
		t.Errorf("UTC DTSTART parse: got %v want %v", utc.Start, wantStart)
	}
	if utc.Location != "Zoom" {
		t.Errorf("LOCATION: got %q want %q", utc.Location, "Zoom")
	}

	zoned := byUID("Zoned Demo with a really long title that got folded onto two lines")
	if zoned == nil {
		t.Fatal("line-folded zoned event missing — SUMMARY unfold failed")
	}
	if zoned.TZID != "America/Los_Angeles" {
		t.Errorf("TZID label: got %q want %q", zoned.TZID, "America/Los_Angeles")
	}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err == nil {
		wantStart = time.Date(2100, 1, 2, 15, 0, 0, 0, loc).UTC()
		if !zoned.Start.Equal(wantStart) {
			t.Errorf("zoned DTSTART parse: got %v want %v", zoned.Start, wantStart)
		}
	}

	allday := byUID("All-day Demo")
	if allday == nil || !allday.AllDay {
		t.Fatal("all-day event missing or AllDay flag not set")
	}

	recurring := byUID("Weekly sync")
	if recurring == nil {
		t.Fatal("recurring event missing")
	}
	if !strings.Contains(recurring.RRule, "FREQ=WEEKLY") {
		t.Errorf("RRULE not captured: got %q", recurring.RRule)
	}
}

func TestUnescapeICalText(t *testing.T) {
	cases := map[string]string{
		`hello\, world`:    "hello, world",
		`line1\nline2`:     "line1\nline2",
		`a\\b`:             `a\b`,
		`semi\;split`:      "semi;split",
		`no escapes at all`: "no escapes at all",
	}
	for in, want := range cases {
		if got := unescapeICalText(in); got != want {
			t.Errorf("unescapeICalText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseICalDateTime_AllForms(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		params    string
		wantUTC   time.Time
		wantAllDay bool
		wantTZID  string
		wantZero  bool
	}{
		{
			name:    "UTC zulu",
			value:   "21000101T100000Z",
			wantUTC: time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC),
		},
		{
			name:     "all-day VALUE=DATE",
			value:    "21000103",
			params:   "VALUE=DATE",
			wantUTC:  time.Date(2100, 1, 3, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:    "all-day implicit by length",
			value:   "21000103",
			wantUTC: time.Date(2100, 1, 3, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:     "TZID America/Los_Angeles",
			value:    "21000102T150000",
			params:   "TZID=America/Los_Angeles",
			wantTZID: "America/Los_Angeles",
			// wantUTC computed below since it depends on tzdata
		},
		{
			name:     "garbage value yields zero time",
			value:    "not-a-date",
			wantZero: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, allDay, tzid := parseICalDateTime(c.value, c.params)
			if c.wantZero {
				if !got.IsZero() {
					t.Errorf("expected zero time for malformed value; got %v", got)
				}
				return
			}
			if c.name == "TZID America/Los_Angeles" {
				if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
					want := time.Date(2100, 1, 2, 15, 0, 0, 0, loc).UTC()
					if !got.Equal(want) {
						t.Errorf("got %v want %v", got, want)
					}
				}
			} else if !got.Equal(c.wantUTC) {
				t.Errorf("got %v want %v", got, c.wantUTC)
			}
			if allDay != c.wantAllDay {
				t.Errorf("allDay: got %v want %v", allDay, c.wantAllDay)
			}
			if tzid != c.wantTZID {
				t.Errorf("tzid: got %q want %q", tzid, c.wantTZID)
			}
		})
	}
}

func TestUnfoldICalLines_DropsLeadingWhitespace(t *testing.T) {
	in := "FIRST\r\n" +
		"SUMMARY:Hello\r\n" +
		" World\r\n" + // space-folded continuation
		"\tand more\r\n" + // tab-folded continuation
		"LAST\r\n"
	out := unfoldICalLines(in)
	want := []string{"FIRST", "SUMMARY:HelloWorldand more", "LAST", ""}
	if len(out) != len(want) {
		t.Fatalf("line count: got %d want %d (%v)", len(out), len(want), out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("line %d: got %q want %q", i, out[i], want[i])
		}
	}
}
