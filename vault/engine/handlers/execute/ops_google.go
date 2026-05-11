package execute

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

// execGmailSendOAuth sends a plain-text email via the Gmail API using the
// user's stored google_oauth credential (requires gmail.send scope).
func execGmailSendOAuth(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	to, _ := params["to"].(string)
	subject, _ := params["subject"].(string)
	body, _ := params["body"].(string)
	if to == "" || subject == "" || body == "" {
		return opResult{err: fmt.Errorf("to, subject, and body are required"), code: ErrCodeInvalidParams}
	}

	rawMsg := buildPlainEmail(cred.Email, to, subject, body)
	encoded := base64.URLEncoding.EncodeToString(rawMsg)

	reqBody, _ := json.Marshal(map[string]any{"raw": encoded})
	respBody, statusCode, err := httpRequest(ctx, "POST",
		"https://gmail.googleapis.com/gmail/v1/users/me/messages/send",
		map[string]string{"Authorization": "Bearer " + token},
		strings.NewReader(string(reqBody)),
	)
	if err != nil {
		return opResult{err: fmt.Errorf("gmail api send failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("gmail api send error (status %d): %s", statusCode, string(respBody)), code: ErrCodeUpstreamError}
	}
	return opResult{result: map[string]any{"sent": true, "to": to, "subject": subject, "from": cred.Email}}
}

// execCalendarCreateEventOAuth creates a Google Calendar event via the
// Calendar API using the user's google_oauth credential.
// Falls back to returning a "add to calendar" URL if the API call fails
// (e.g. only calendar.readonly scope granted).
func execCalendarCreateEventOAuth(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	token, err := resolveAccessToken(ctx, cred)
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

	startUTC, err := parseICSTime(startStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid start time: %w", err), code: ErrCodeInvalidParams}
	}
	endUTC, err := parseICSTime(endStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid end time: %w", err), code: ErrCodeInvalidParams}
	}

	calendarID := "primary"
	if id, ok := params["calendar_id"].(string); ok && id != "" {
		calendarID = id
	}

	event := map[string]any{
		"summary":     title,
		"description": description,
		"location":    location,
		"start":       map[string]any{"dateTime": startUTC.Format(time.RFC3339), "timeZone": "UTC"},
		"end":         map[string]any{"dateTime": endUTC.Format(time.RFC3339), "timeZone": "UTC"},
	}
	reqBody, _ := json.Marshal(event)
	apiURL := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events",
		url.PathEscape(calendarID))

	respBody, statusCode, apiErr := httpRequest(ctx, "POST", apiURL,
		map[string]string{"Authorization": "Bearer " + token},
		strings.NewReader(string(reqBody)),
	)
	if apiErr == nil && statusCode < 400 {
		var created map[string]any
		_ = json.Unmarshal(respBody, &created)
		return opResult{result: map[string]any{
			"created":  true,
			"title":    title,
			"start":    startUTC.Format(time.RFC3339),
			"end":      endUTC.Format(time.RFC3339),
			"event_id": created["id"],
			"html_link": created["htmlLink"],
		}}
	}

	// Fallback: return a one-click "add to calendar" URL the agent can share.
	addURL := buildGoogleCalendarURL(title, description, location, startUTC, endUTC)
	return opResult{result: map[string]any{
		"created":             false,
		"add_to_calendar_url": addURL,
		"title":               title,
		"start":               startUTC.Format(time.RFC3339),
		"end":                 endUTC.Format(time.RFC3339),
		"message":             "Calendar write scope not available — click the URL to add this event manually",
	}}
}

// googleOAuthCredential is the JSON blob stored in OpenBao under
// users/<user_id>/credentials/google_oauth for each user that has connected
// their Google account via the Admin UI OAuth flow.
type googleOAuthCredential struct {
	Email        string `json:"email"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// parseGoogleOAuthCredential decodes and validates the stored credential blob.
func parseGoogleOAuthCredential(credential string) (googleOAuthCredential, error) {
	var c googleOAuthCredential
	if err := json.Unmarshal([]byte(credential), &c); err != nil {
		return c, fmt.Errorf("google_oauth credential is not a valid JSON object")
	}
	if c.AccessToken == "" {
		return c, fmt.Errorf("google_oauth credential missing access_token")
	}
	return c, nil
}

// resolveAccessToken returns a valid access token, refreshing it automatically
// when it is within 60 seconds of expiry. GOOGLE_CLIENT_ID and
// GOOGLE_CLIENT_SECRET must be present in the vault container's environment for
// refresh to succeed; if they are absent the stored token is returned as-is.
func resolveAccessToken(ctx context.Context, c googleOAuthCredential) (string, error) {
	if c.ExpiresAt != "" {
		expiry, err := time.Parse(time.RFC3339, c.ExpiresAt)
		if err == nil && time.Now().UTC().Add(60*time.Second).Before(expiry) {
			return c.AccessToken, nil
		}
	}

	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" || c.RefreshToken == "" {
		return c.AccessToken, nil
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", c.RefreshToken)
	form.Set("grant_type", "refresh_token")

	body, statusCode, err := httpRequest(ctx, "POST",
		"https://oauth2.googleapis.com/token",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		strings.NewReader(form.Encode()),
	)
	if err != nil || statusCode >= 400 {
		// Log status only — never log body (may contain credential material).
		slog.Info("google oauth token refresh failed", "status", statusCode, "err_present", err != nil)
		return c.AccessToken, nil
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return c.AccessToken, nil
	}
	return tok.AccessToken, nil
}

// execGmailSearch calls the Gmail API to search messages matching a query.
// credential is the JSON google_oauth blob; params: {query, max_results?}.
func execGmailSearch(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return opResult{err: fmt.Errorf("query parameter is required"), code: ErrCodeInvalidParams}
	}
	maxResults := 10
	if v, ok := params["max_results"].(float64); ok && v > 0 {
		maxResults = int(v)
	}
	if maxResults > 50 {
		maxResults = 50
	}

	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	apiURL := fmt.Sprintf(
		"https://gmail.googleapis.com/gmail/v1/users/me/messages?q=%s&maxResults=%d",
		url.QueryEscape(query), maxResults,
	)
	body, statusCode, err := httpGET(ctx, apiURL, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if err != nil {
		return opResult{err: fmt.Errorf("gmail search request failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("gmail API returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	var listResp struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
		ResultSizeEstimate int `json:"resultSizeEstimate"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return opResult{err: fmt.Errorf("failed to parse gmail list response"), code: ErrCodeUpstreamError}
	}
	if len(listResp.Messages) == 0 {
		return opResult{result: map[string]any{"messages": []any{}, "total": 0}}
	}

	// Fetch metadata (From, Subject, Date) for each message.
	results := make([]map[string]any, 0, len(listResp.Messages))
	for _, msg := range listResp.Messages {
		metaURL := fmt.Sprintf(
			"https://gmail.googleapis.com/gmail/v1/users/me/messages/%s?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date",
			url.PathEscape(msg.ID),
		)
		mb, sc, merr := httpGET(ctx, metaURL, map[string]string{"Authorization": "Bearer " + token})
		if merr != nil || sc >= 400 {
			results = append(results, map[string]any{"id": msg.ID, "error": "metadata fetch failed"})
			continue
		}
		var meta struct {
			ID      string `json:"id"`
			Snippet string `json:"snippet"`
			Payload struct {
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(mb, &meta); err != nil {
			results = append(results, map[string]any{"id": msg.ID})
			continue
		}
		entry := map[string]any{"id": meta.ID, "snippet": meta.Snippet}
		for _, h := range meta.Payload.Headers {
			switch h.Name {
			case "From":
				entry["from"] = h.Value
			case "Subject":
				entry["subject"] = h.Value
			case "Date":
				entry["date"] = h.Value
			}
		}
		results = append(results, entry)
	}

	return opResult{result: map[string]any{
		"messages": results,
		"total":    len(results),
	}}
}

// execGmailGetMessage fetches the full body and headers of a single Gmail message.
// credential is the JSON google_oauth blob; params: {message_id}.
func execGmailGetMessage(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	messageID, ok := params["message_id"].(string)
	if !ok || messageID == "" {
		return opResult{err: fmt.Errorf("message_id parameter is required"), code: ErrCodeInvalidParams}
	}

	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	apiURL := fmt.Sprintf(
		"https://gmail.googleapis.com/gmail/v1/users/me/messages/%s?format=full",
		url.PathEscape(messageID),
	)
	body, statusCode, err := httpGET(ctx, apiURL, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if err != nil {
		return opResult{err: fmt.Errorf("gmail get message failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("gmail API returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	var msg struct {
		ID      string `json:"id"`
		Snippet string `json:"snippet"`
		Payload struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
			Parts []struct {
				MimeType string `json:"mimeType"`
				Body     struct {
					Data string `json:"data"`
				} `json:"body"`
				Filename string `json:"filename"`
			} `json:"parts"`
			Body struct {
				Data string `json:"data"`
			} `json:"body"`
			MimeType string `json:"mimeType"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return opResult{err: fmt.Errorf("failed to parse gmail message response"), code: ErrCodeUpstreamError}
	}

	result := map[string]any{
		"id":      msg.ID,
		"snippet": msg.Snippet,
	}
	for _, h := range msg.Payload.Headers {
		switch h.Name {
		case "From", "To", "Subject", "Date", "Cc":
			result[strings.ToLower(h.Name)] = h.Value
		}
	}

	// Extract plain-text body.
	if msg.Payload.MimeType == "text/plain" && msg.Payload.Body.Data != "" {
		result["body"] = decodeBase64URL(msg.Payload.Body.Data)
	} else {
		for _, part := range msg.Payload.Parts {
			if part.MimeType == "text/plain" && part.Body.Data != "" {
				result["body"] = decodeBase64URL(part.Body.Data)
				break
			}
		}
	}

	var attachments []string
	for _, part := range msg.Payload.Parts {
		if part.Filename != "" {
			attachments = append(attachments, part.Filename)
		}
	}
	if len(attachments) > 0 {
		result["attachments"] = attachments
	}

	return opResult{result: result}
}

// execCalendarListEvents calls the Google Calendar API to list upcoming events.
// credential is the JSON google_oauth blob; params: {max_results?, time_min?, time_max?, calendar_id?}.
func execCalendarListEvents(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	calendarID := "primary"
	if v, ok := params["calendar_id"].(string); ok && v != "" {
		calendarID = v
	}
	maxResults := 10
	if v, ok := params["max_results"].(float64); ok && v > 0 {
		maxResults = int(v)
	}
	if maxResults > 50 {
		maxResults = 50
	}
	timeMin := time.Now().UTC().Format(time.RFC3339)
	if v, ok := params["time_min"].(string); ok && v != "" {
		timeMin = v
	}
	timeMax := time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	if v, ok := params["time_max"].(string); ok && v != "" {
		timeMax = v
	}

	apiURL := fmt.Sprintf(
		"https://www.googleapis.com/calendar/v3/calendars/%s/events?maxResults=%d&timeMin=%s&timeMax=%s&orderBy=startTime&singleEvents=true",
		url.PathEscape(calendarID),
		maxResults,
		url.QueryEscape(timeMin),
		url.QueryEscape(timeMax),
	)
	body, statusCode, err := httpGET(ctx, apiURL, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if err != nil {
		return opResult{err: fmt.Errorf("calendar list request failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("calendar API returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	var calResp struct {
		Items []struct {
			ID          string `json:"id"`
			Summary     string `json:"summary"`
			Description string `json:"description,omitempty"`
			Location    string `json:"location,omitempty"`
			Start       struct {
				DateTime string `json:"dateTime,omitempty"`
				Date     string `json:"date,omitempty"`
			} `json:"start"`
			End struct {
				DateTime string `json:"dateTime,omitempty"`
				Date     string `json:"date,omitempty"`
			} `json:"end"`
			Attendees []struct {
				Email          string `json:"email"`
				DisplayName    string `json:"displayName,omitempty"`
				ResponseStatus string `json:"responseStatus,omitempty"`
			} `json:"attendees,omitempty"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &calResp); err != nil {
		return opResult{err: fmt.Errorf("failed to parse calendar response"), code: ErrCodeUpstreamError}
	}

	events := make([]map[string]any, 0, len(calResp.Items))
	for _, item := range calResp.Items {
		start := item.Start.DateTime
		if start == "" {
			start = item.Start.Date
		}
		end := item.End.DateTime
		if end == "" {
			end = item.End.Date
		}
		ev := map[string]any{
			"id":     item.ID,
			"title":  item.Summary,
			"start":  start,
			"end":    end,
			"status": item.Status,
		}
		if item.Description != "" {
			ev["description"] = item.Description
		}
		if item.Location != "" {
			ev["location"] = item.Location
		}
		if len(item.Attendees) > 0 {
			attendees := make([]map[string]any, 0, len(item.Attendees))
			for _, a := range item.Attendees {
				at := map[string]any{"email": a.Email}
				if a.DisplayName != "" {
					at["name"] = a.DisplayName
				}
				if a.ResponseStatus != "" {
					at["status"] = a.ResponseStatus
				}
				attendees = append(attendees, at)
			}
			ev["attendees"] = attendees
		}
		events = append(events, ev)
	}

	return opResult{result: map[string]any{
		"events": events,
		"total":  len(events),
	}}
}

// decodeBase64URL decodes a URL-safe base64 string as used by the Gmail API.
func decodeBase64URL(s string) string {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s
	}
	return string(decoded)
}
