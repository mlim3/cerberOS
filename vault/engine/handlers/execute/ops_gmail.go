package execute

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// gmailCredential is the JSON shape stored in OpenBao for the Gmail demo
// account credential. email + app_password are required. calendar_ical_url
// is OPTIONAL — populated only when the manager wants read access to the
// account's Google Calendar via its "Secret address in iCal format" (a
// per-calendar token URL that doesn't require OAuth). Storing it alongside
// the Gmail credential keeps E1's plumbing on the existing admin section
// instead of inventing a second credential type.
//
// The app password is a 16-char Google App Password
// (https://myaccount.google.com/apppasswords) — never an account password
// and never an OAuth token.
type gmailCredential struct {
	Email           string `json:"email"`
	AppPassword     string `json:"app_password"`
	CalendarICalURL string `json:"calendar_ical_url,omitempty"`
}

// parseGmailCredential decodes the credential blob and validates required
// fields. Errors are deliberately generic — never leak credential details.
// calendar_ical_url is NOT validated here because most operations
// (gmail_send, calendar_create_event) don't need it; the calendar_list
// op enforces presence itself when called.
func parseGmailCredential(credential string) (gmailCredential, error) {
	var c gmailCredential
	if err := json.Unmarshal([]byte(credential), &c); err != nil {
		return c, fmt.Errorf("gmail credential is not a valid JSON object")
	}
	if c.Email == "" || c.AppPassword == "" {
		return c, fmt.Errorf("gmail credential missing email or app_password")
	}
	if !strings.Contains(c.Email, "@") {
		return c, fmt.Errorf("gmail credential email is not a valid address")
	}
	c.AppPassword = strings.ReplaceAll(c.AppPassword, " ", "")
	if len(c.AppPassword) != 16 {
		return c, fmt.Errorf("gmail app password must be 16 characters (Google App Password format)")
	}
	return c, nil
}

// execGmailSend sends a plain-text email via Gmail SMTP submission (port 587 + STARTTLS).
// credential is a JSON {email, app_password}; params: {to, subject, body}.
func execGmailSend(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGmailCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	to, _ := params["to"].(string)
	subject, _ := params["subject"].(string)
	body, _ := params["body"].(string)
	if to == "" || subject == "" || body == "" {
		return opResult{err: fmt.Errorf("to, subject, and body are required"), code: ErrCodeInvalidParams}
	}

	msg := buildPlainEmail(cred.Email, to, subject, body)
	if err := sendViaGmailSMTP(ctx, cred, []string{to}, msg); err != nil {
		return opResult{err: fmt.Errorf("gmail send failed: %w", err), code: ErrCodeUpstreamError}
	}
	return opResult{result: map[string]any{
		"sent":      true,
		"to":        to,
		"subject":   subject,
		"from":      cred.Email,
		"transport": "smtp.gmail.com:587",
	}}
}

// execGmailCalendarInvite sends a calendar invite via Gmail SMTP by attaching
// an iCalendar (.ics) REQUEST. When the recipient opens the email in Gmail,
// they get a one-click "Add to Calendar" button — no Calendar API needed.
//
// params: {title, start, end, description?, location?, to?}
// If `to` is omitted, the invite is sent to the demo account itself, which is
// the typical demo flow (the user opens the demo Gmail and accepts).
func execGmailCalendarInvite(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGmailCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	title, _ := params["title"].(string)
	startStr, _ := params["start"].(string)
	endStr, _ := params["end"].(string)
	if title == "" || startStr == "" || endStr == "" {
		return opResult{err: fmt.Errorf("title, start, and end are required (ISO 8601 datetimes)"), code: ErrCodeInvalidParams}
	}
	description, _ := params["description"].(string)
	location, _ := params["location"].(string)
	to, _ := params["to"].(string)
	if to == "" {
		to = cred.Email
	}

	startUTC, err := parseICSTime(startStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid start time: %w", err), code: ErrCodeInvalidParams}
	}
	endUTC, err := parseICSTime(endStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid end time: %w", err), code: ErrCodeInvalidParams}
	}

	uid := newUID() + "@cerberos.local"
	ics := buildICS(uid, cred.Email, to, title, description, location, startUTC, endUTC)
	addURL := buildGoogleCalendarURL(title, description, location, startUTC, endUTC)
	msg := buildCalendarInviteEmail(cred.Email, to, title, description, location, addURL, ics, startUTC, endUTC)

	if err := sendViaGmailSMTP(ctx, cred, []string{to}, msg); err != nil {
		return opResult{err: fmt.Errorf("gmail calendar invite failed: %w", err), code: ErrCodeUpstreamError}
	}
	return opResult{result: map[string]any{
		"sent":     true,
		"to":       to,
		"from":     cred.Email,
		"uid":      uid,
		"title":    title,
		"start":    startUTC.Format(time.RFC3339),
		"end":      endUTC.Format(time.RFC3339),
		"location": location,
	}}
}

// sendViaGmailSMTP performs the SMTP exchange against smtp.gmail.com:587 with
// PLAIN auth + STARTTLS (handled internally by smtp.SendMail when the server
// advertises STARTTLS). Context cancellation is best-effort; net/smtp does not
// natively accept a context, so we wrap the call in a goroutine with a select.
func sendViaGmailSMTP(ctx context.Context, cred gmailCredential, to []string, msg []byte) error {
	auth := smtp.PlainAuth("", cred.Email, cred.AppPassword, "smtp.gmail.com")
	addr := "smtp.gmail.com:587"

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, cred.Email, to, msg)
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("smtp send cancelled: %w", ctx.Err())
	case err := <-done:
		return err
	}
}

func buildPlainEmail(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader(subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// buildCalendarInviteEmail builds a multipart email with three alternatives
// (text/plain, text/html with a one-click "Add to Google Calendar" button, and
// text/calendar; method=REQUEST). The HTML button is the primary demo UX —
// Gmail suppresses its native inline RSVP block when the recipient IS the
// organizer (the demo's typical "send to self" case), so the click-through
// link is what makes the demo feel one-click.
func buildCalendarInviteEmail(from, to, title, description, location, addURL, ics string, start, end time.Time) []byte {
	boundary := "cerberos-cal-" + newUID()
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader("Invitation: "+title) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(buildPlainBody(title, description, location, addURL, start, end))
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(buildHTMLBody(title, description, location, addURL, start, end))
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/calendar; charset=\"UTF-8\"; method=REQUEST\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(ics)
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// buildGoogleCalendarURL builds a https://calendar.google.com/calendar/render
// "TEMPLATE" link that pre-fills an event when clicked. One click and the
// recipient lands on Google Calendar's confirmation page — no .ics import.
func buildGoogleCalendarURL(title, description, location string, start, end time.Time) string {
	q := url.Values{}
	q.Set("action", "TEMPLATE")
	q.Set("text", title)
	q.Set("dates", start.UTC().Format("20060102T150405Z")+"/"+end.UTC().Format("20060102T150405Z"))
	if description != "" {
		q.Set("details", description)
	}
	if location != "" {
		q.Set("location", location)
	}
	return "https://calendar.google.com/calendar/render?" + q.Encode()
}

func buildPlainBody(title, description, location, addURL string, start, end time.Time) string {
	var b strings.Builder
	b.WriteString(title + "\r\n")
	b.WriteString("When:  " + start.UTC().Format("Mon Jan 2 2006, 15:04 MST") + " – " + end.UTC().Format("15:04 MST") + "\r\n")
	if location != "" {
		b.WriteString("Where: " + location + "\r\n")
	}
	if description != "" {
		b.WriteString("\r\n" + description + "\r\n")
	}
	b.WriteString("\r\nAdd to Google Calendar (one click):\r\n" + addURL + "\r\n")
	return b.String()
}

func buildHTMLBody(title, description, location, addURL string, start, end time.Time) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><body style=\"font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#202124;max-width:560px;\">")
	b.WriteString("<h2 style=\"margin:0 0 12px 0;\">" + htmlEscape(title) + "</h2>")
	b.WriteString("<p style=\"margin:0 0 4px 0;color:#5f6368;\"><b>When:</b> " + htmlEscape(start.UTC().Format("Mon Jan 2 2006, 15:04 MST")) + " – " + htmlEscape(end.UTC().Format("15:04 MST")) + "</p>")
	if location != "" {
		b.WriteString("<p style=\"margin:0 0 4px 0;color:#5f6368;\"><b>Where:</b> " + htmlEscape(location) + "</p>")
	}
	if description != "" {
		b.WriteString("<p style=\"margin:16px 0;\">" + htmlEscape(description) + "</p>")
	}
	b.WriteString("<p style=\"margin:24px 0;\"><a href=\"" + htmlAttrEscape(addURL) + "\" style=\"display:inline-block;background:#1a73e8;color:#ffffff;text-decoration:none;padding:12px 20px;border-radius:6px;font-weight:600;\">Add to Google Calendar</a></p>")
	b.WriteString("<p style=\"margin:16px 0 0 0;color:#80868b;font-size:12px;\">One click adds this to your calendar. An <code>.ics</code> attachment is also included for other calendar apps.</p>")
	b.WriteString("</body></html>")
	return b.String()
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func htmlAttrEscape(s string) string {
	s = htmlEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

func buildICS(uid, organizer, attendee, title, description, location string, start, end time.Time) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//cerberOS//FP-Stefan//EN\r\n")
	b.WriteString("METHOD:REQUEST\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + uid + "\r\n")
	b.WriteString("DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("DTSTART:" + start.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("DTEND:" + end.UTC().Format("20060102T150405Z") + "\r\n")
	b.WriteString("SUMMARY:" + icsEscape(title) + "\r\n")
	if description != "" {
		b.WriteString("DESCRIPTION:" + icsEscape(description) + "\r\n")
	}
	if location != "" {
		b.WriteString("LOCATION:" + icsEscape(location) + "\r\n")
	}
	b.WriteString("ORGANIZER;CN=" + icsEscape(organizer) + ":mailto:" + organizer + "\r\n")
	b.WriteString("ATTENDEE;CN=" + icsEscape(attendee) + ";RSVP=TRUE:mailto:" + attendee + "\r\n")
	b.WriteString("STATUS:CONFIRMED\r\n")
	b.WriteString("SEQUENCE:0\r\n")
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

func parseICSTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse %q as ISO 8601 datetime", s)
}

// icsEscape escapes characters per RFC 5545 §3.3.11 (TEXT value type).
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// sanitizeHeader strips CR/LF from a header value to prevent SMTP header
// injection. Subjects/from/to addresses are constructed from agent-supplied
// params so this defense is mandatory.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func newUID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	const hex = "0123456789abcdef"
	out := make([]byte, len(buf)*2)
	for i, b := range buf {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}
