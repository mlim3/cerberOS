package execute

import (
	"strings"
	"testing"
	"time"
)

func TestParseGmailCredential(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   bool
		wantEmail string
		wantPwd   string
	}{
		{
			name:      "valid 16-char password",
			input:     `{"email":"demo@gmail.com","app_password":"abcd1234efgh5678"}`,
			wantEmail: "demo@gmail.com",
			wantPwd:   "abcd1234efgh5678",
		},
		{
			name:      "spaces stripped from password (Google's xxxx xxxx xxxx xxxx format)",
			input:     `{"email":"demo@gmail.com","app_password":"abcd 1234 efgh 5678"}`,
			wantEmail: "demo@gmail.com",
			wantPwd:   "abcd1234efgh5678",
		},
		{name: "missing email", input: `{"app_password":"abcd1234efgh5678"}`, wantErr: true},
		{name: "missing password", input: `{"email":"demo@gmail.com"}`, wantErr: true},
		{name: "email without @", input: `{"email":"demo","app_password":"abcd1234efgh5678"}`, wantErr: true},
		{name: "password too short (account password by mistake)", input: `{"email":"demo@gmail.com","app_password":"short"}`, wantErr: true},
		{name: "password too long", input: `{"email":"demo@gmail.com","app_password":"thispasswordistoolong12345"}`, wantErr: true},
		{name: "not json", input: `not-a-json-blob`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := parseGmailCredential(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseGmailCredential(%q) = no error; want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGmailCredential(%q) error = %v; want nil", tt.input, err)
			}
			if c.Email != tt.wantEmail {
				t.Errorf("Email = %q; want %q", c.Email, tt.wantEmail)
			}
			if c.AppPassword != tt.wantPwd {
				t.Errorf("AppPassword = %q; want %q", c.AppPassword, tt.wantPwd)
			}
		})
	}
}

func TestParseGmailCredentialErrorMessageOmitsSecret(t *testing.T) {
	_, err := parseGmailCredential(`{"email":"x@gmail.com","app_password":"super-secret-do-not-leak"}`)
	if err == nil {
		t.Fatal("expected an error for invalid length")
	}
	if strings.Contains(err.Error(), "super-secret-do-not-leak") {
		t.Errorf("error message leaked the password: %v", err)
	}
}

func TestParseICSTime(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		wantUTC string
	}{
		{input: "2026-06-01T10:00:00-07:00", wantUTC: "2026-06-01T17:00:00Z"},
		{input: "2026-06-01T17:00:00Z", wantUTC: "2026-06-01T17:00:00Z"},
		{input: "2026-06-01T10:00", wantUTC: "2026-06-01T10:00:00Z"},
		{input: "not-a-date", wantErr: true},
		{input: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseICSTime(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseICSTime(%q) = no error; want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseICSTime(%q) error = %v", tt.input, err)
			}
			if got.Format(time.RFC3339) != tt.wantUTC {
				t.Errorf("parseICSTime(%q).UTC() = %s; want %s", tt.input, got.Format(time.RFC3339), tt.wantUTC)
			}
		})
	}
}

func TestICSEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello", "hello"},
		{"with, comma", "with\\, comma"},
		{"with; semicolon", "with\\; semicolon"},
		{"line\nbreak", "line\\nbreak"},
		{"back\\slash", "back\\\\slash"},
		{"strip\rcr", "stripcr"},
	}
	for _, tt := range tests {
		got := icsEscape(tt.in)
		if got != tt.want {
			t.Errorf("icsEscape(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeHeader(t *testing.T) {
	got := sanitizeHeader("Subject: hello\r\nBcc: attacker@evil.com")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("sanitizeHeader did not strip CR/LF: %q", got)
	}
}

func TestBuildICSContainsRequiredFields(t *testing.T) {
	start := time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	ics := buildICS("uid-123@cerberos.local", "demo@gmail.com", "demo@gmail.com",
		"Founders Coffee", "Notes here", "Sightglass SF", start, end)

	required := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"METHOD:REQUEST",
		"BEGIN:VEVENT",
		"UID:uid-123@cerberos.local",
		"DTSTART:20260601T170000Z",
		"DTEND:20260601T173000Z",
		"SUMMARY:Founders Coffee",
		"DESCRIPTION:Notes here",
		"LOCATION:Sightglass SF",
		"ORGANIZER;CN=demo@gmail.com:mailto:demo@gmail.com",
		"ATTENDEE;CN=demo@gmail.com;RSVP=TRUE:mailto:demo@gmail.com",
		"END:VEVENT",
		"END:VCALENDAR",
	}
	for _, want := range required {
		if !strings.Contains(ics, want) {
			t.Errorf("buildICS missing %q\n--- ics ---\n%s", want, ics)
		}
	}

	if !strings.Contains(ics, "\r\n") {
		t.Error("buildICS must use CRLF line endings per RFC 5545")
	}
}

func TestBuildCalendarInviteEmailIsMultipart(t *testing.T) {
	msg := buildCalendarInviteEmail("from@gmail.com", "to@gmail.com", "Title", "Body", "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	s := string(msg)
	if !strings.Contains(s, "multipart/alternative") {
		t.Error("expected multipart/alternative content type")
	}
	if !strings.Contains(s, "text/calendar") {
		t.Error("expected text/calendar part")
	}
	if !strings.Contains(s, "method=REQUEST") {
		t.Error("expected method=REQUEST in calendar part")
	}
	if !strings.Contains(s, "From: from@gmail.com") {
		t.Error("expected From header")
	}
	if !strings.Contains(s, "To: to@gmail.com") {
		t.Error("expected To header")
	}
	if !strings.Contains(s, "Subject: Invitation: Title") {
		t.Error("expected Subject header with Invitation: prefix")
	}
}

func TestExecGmailSendValidatesParams(t *testing.T) {
	cred := `{"email":"demo@gmail.com","app_password":"abcd1234efgh5678"}`
	bad := []map[string]any{
		{},
		{"to": "x@y.com"},
		{"to": "x@y.com", "subject": "hi"},
	}
	for _, params := range bad {
		res := execGmailSend(nil, cred, params)
		if res.err == nil {
			t.Errorf("expected validation error for params=%v", params)
		}
	}
}

func TestExecGmailSendBadCredential(t *testing.T) {
	res := execGmailSend(nil, "not-json", map[string]any{
		"to": "x@y.com", "subject": "hi", "body": "hello",
	})
	if res.err == nil {
		t.Error("expected error for invalid credential blob")
	}
	if res.code != ErrCodeCredentialUnavailable {
		t.Errorf("error code = %q; want %q", res.code, ErrCodeCredentialUnavailable)
	}
}

func TestExecGmailCalendarInviteValidatesTimes(t *testing.T) {
	cred := `{"email":"demo@gmail.com","app_password":"abcd1234efgh5678"}`
	res := execGmailCalendarInvite(nil, cred, map[string]any{
		"title": "X", "start": "garbage", "end": "2026-01-01T10:00:00Z",
	})
	if res.err == nil {
		t.Error("expected error for garbage start time")
	}
}
