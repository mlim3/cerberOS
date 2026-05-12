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

	// Pre-create conflict check — query freeBusy for the requested window so
	// the response can warn the agent (and downstream the user) when the slot
	// is already taken. We do NOT abort on conflict; the event still gets
	// created (the user explicitly asked for this time), and the warning is
	// surfaced in the result for the agent to mention in its reply.
	conflicts := lookupBusy(ctx, token, calendarID, startUTC, endUTC)

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
		result := map[string]any{
			"created":   true,
			"title":     title,
			"start":     startUTC.Format(time.RFC3339),
			"end":       endUTC.Format(time.RFC3339),
			"event_id":  created["id"],
			"html_link": created["htmlLink"],
		}
		if len(conflicts) > 0 {
			result["conflicts"] = conflicts
			result["warning"] = fmt.Sprintf("Double-booked — this slot overlaps %d existing event(s). Created anyway as requested; mention this to the user.", len(conflicts))
		}
		return opResult{result: result}
	}

	// Fallback: return a one-click "add to calendar" URL the agent can share.
	addURL := buildGoogleCalendarURL(title, description, location, startUTC, endUTC)
	fallback := map[string]any{
		"created":             false,
		"add_to_calendar_url": addURL,
		"title":               title,
		"start":               startUTC.Format(time.RFC3339),
		"end":                 endUTC.Format(time.RFC3339),
		"message":             "Calendar write scope not available — click the URL to add this event manually",
	}
	if len(conflicts) > 0 {
		fallback["conflicts"] = conflicts
		fallback["warning"] = fmt.Sprintf("Note: this slot already overlaps %d existing event(s).", len(conflicts))
	}
	return opResult{result: fallback}
}

// execCalendarFreeBusy queries Google Calendar's freeBusy endpoint for the
// given window and returns the list of busy intervals per calendar.
// params: {time_min, time_max, calendar_id?}
func execCalendarFreeBusy(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	timeMinStr, _ := params["time_min"].(string)
	timeMaxStr, _ := params["time_max"].(string)
	if timeMinStr == "" || timeMaxStr == "" {
		return opResult{err: fmt.Errorf("time_min and time_max are required (ISO 8601 datetimes)"), code: ErrCodeInvalidParams}
	}
	timeMin, err := parseICSTime(timeMinStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid time_min: %w", err), code: ErrCodeInvalidParams}
	}
	timeMax, err := parseICSTime(timeMaxStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid time_max: %w", err), code: ErrCodeInvalidParams}
	}

	calendarID := "primary"
	if id, ok := params["calendar_id"].(string); ok && id != "" {
		calendarID = id
	}

	busy := lookupBusy(ctx, token, calendarID, timeMin, timeMax)
	return opResult{result: map[string]any{
		"calendar_id": calendarID,
		"time_min":    timeMin.Format(time.RFC3339),
		"time_max":    timeMax.Format(time.RFC3339),
		"busy":        busy,
		"free":        len(busy) == 0,
	}}
}

// lookupBusy returns the busy intervals on calendarID overlapping [start,end].
// Errors are swallowed — callers that need conflict detection treat an empty
// slice as "no known conflicts" so a freeBusy outage never blocks a create.
func lookupBusy(ctx context.Context, token, calendarID string, start, end time.Time) []map[string]string {
	reqBody, _ := json.Marshal(map[string]any{
		"timeMin": start.Format(time.RFC3339),
		"timeMax": end.Format(time.RFC3339),
		"items":   []map[string]string{{"id": calendarID}},
	})
	respBody, statusCode, err := httpRequest(ctx, "POST",
		"https://www.googleapis.com/calendar/v3/freeBusy",
		map[string]string{"Authorization": "Bearer " + token},
		strings.NewReader(string(reqBody)),
	)
	if err != nil || statusCode >= 400 {
		return nil
	}
	var parsed struct {
		Calendars map[string]struct {
			Busy []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"busy"`
		} `json:"calendars"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil
	}
	cal, ok := parsed.Calendars[calendarID]
	if !ok {
		return nil
	}
	out := make([]map[string]string, 0, len(cal.Busy))
	for _, b := range cal.Busy {
		out = append(out, map[string]string{"start": b.Start, "end": b.End})
	}
	return out
}

// execCalendarUpdateEvent moves or edits an existing Google Calendar event.
// params: {event_id (required), calendar_id?, title?, start?, end?, description?, location?}
// Only provided fields are updated. When start+end change, runs a freeBusy
// check on the new window and surfaces conflicts as a warning.
func execCalendarUpdateEvent(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	eventID, _ := params["event_id"].(string)
	if eventID == "" {
		return opResult{err: fmt.Errorf("event_id is required"), code: ErrCodeInvalidParams}
	}
	calendarID := "primary"
	if id, ok := params["calendar_id"].(string); ok && id != "" {
		calendarID = id
	}

	patch := map[string]any{}
	if v, ok := params["title"].(string); ok && v != "" {
		patch["summary"] = v
	}
	if v, ok := params["description"].(string); ok {
		patch["description"] = v
	}
	if v, ok := params["location"].(string); ok {
		patch["location"] = v
	}

	var newStart, newEnd time.Time
	timeChanged := false
	if s, ok := params["start"].(string); ok && s != "" {
		t, err := parseICSTime(s)
		if err != nil {
			return opResult{err: fmt.Errorf("invalid start: %w", err), code: ErrCodeInvalidParams}
		}
		newStart = t
		patch["start"] = map[string]any{"dateTime": t.Format(time.RFC3339), "timeZone": "UTC"}
		timeChanged = true
	}
	if s, ok := params["end"].(string); ok && s != "" {
		t, err := parseICSTime(s)
		if err != nil {
			return opResult{err: fmt.Errorf("invalid end: %w", err), code: ErrCodeInvalidParams}
		}
		newEnd = t
		patch["end"] = map[string]any{"dateTime": t.Format(time.RFC3339), "timeZone": "UTC"}
		timeChanged = true
	}
	if len(patch) == 0 {
		return opResult{err: fmt.Errorf("no updatable fields supplied (title, start, end, description, location)"), code: ErrCodeInvalidParams}
	}

	// If both new times provided, check freeBusy for the new window (excluding this event).
	var conflicts []map[string]string
	if timeChanged && !newStart.IsZero() && !newEnd.IsZero() {
		raw := lookupBusy(ctx, token, calendarID, newStart, newEnd)
		// Drop any busy interval that exactly matches this event's intended new window
		// (Google will report this event itself as busy once it's moved).
		for _, b := range raw {
			if b["start"] == newStart.Format(time.RFC3339) && b["end"] == newEnd.Format(time.RFC3339) {
				continue
			}
			conflicts = append(conflicts, b)
		}
	}

	reqBody, _ := json.Marshal(patch)
	apiURL := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events/%s",
		url.PathEscape(calendarID), url.PathEscape(eventID))
	respBody, statusCode, apiErr := httpRequest(ctx, "PATCH", apiURL,
		map[string]string{"Authorization": "Bearer " + token},
		strings.NewReader(string(reqBody)),
	)
	if apiErr != nil {
		return opResult{err: fmt.Errorf("calendar update failed: %w", apiErr), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("calendar update error (status %d): %s", statusCode, string(respBody)), code: ErrCodeUpstreamError}
	}
	var updated map[string]any
	_ = json.Unmarshal(respBody, &updated)
	result := map[string]any{
		"updated":   true,
		"event_id":  eventID,
		"html_link": updated["htmlLink"],
		"summary":   updated["summary"],
	}
	if timeChanged {
		result["start"] = newStart.Format(time.RFC3339)
		result["end"] = newEnd.Format(time.RFC3339)
	}
	if len(conflicts) > 0 {
		result["conflicts"] = conflicts
		result["warning"] = fmt.Sprintf("Updated time overlaps %d other event(s). Surface this to the user.", len(conflicts))
	}
	return opResult{result: result}
}

// execCalendarFindFreeSlot scans a time window for the first contiguous free
// slot of the requested duration. Walks forward in 15-minute increments,
// optionally restricted to working hours.
// params: {duration_minutes (required), time_min (required), time_max (required),
//          calendar_id?, working_hours_start? (e.g. "09:00"), working_hours_end? (e.g. "17:00")}
func execCalendarFindFreeSlot(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}
	token, err := resolveAccessToken(ctx, cred)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	durationMin := 0
	if v, ok := params["duration_minutes"].(float64); ok {
		durationMin = int(v)
	}
	if durationMin <= 0 {
		return opResult{err: fmt.Errorf("duration_minutes must be a positive integer"), code: ErrCodeInvalidParams}
	}
	tMinStr, _ := params["time_min"].(string)
	tMaxStr, _ := params["time_max"].(string)
	if tMinStr == "" || tMaxStr == "" {
		return opResult{err: fmt.Errorf("time_min and time_max are required"), code: ErrCodeInvalidParams}
	}
	tMin, err := parseICSTime(tMinStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid time_min: %w", err), code: ErrCodeInvalidParams}
	}
	tMax, err := parseICSTime(tMaxStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid time_max: %w", err), code: ErrCodeInvalidParams}
	}
	if !tMax.After(tMin) {
		return opResult{err: fmt.Errorf("time_max must be after time_min"), code: ErrCodeInvalidParams}
	}

	calendarID := "primary"
	if v, ok := params["calendar_id"].(string); ok && v != "" {
		calendarID = v
	}

	// Optional working hours filter (interpreted in the time_min's location).
	whStartH, whStartM, whEndH, whEndM, useWH := -1, 0, -1, 0, false
	if v, ok := params["working_hours_start"].(string); ok && v != "" {
		if _, err := fmt.Sscanf(v, "%d:%d", &whStartH, &whStartM); err == nil {
			useWH = true
		}
	}
	if v, ok := params["working_hours_end"].(string); ok && v != "" {
		_, _ = fmt.Sscanf(v, "%d:%d", &whEndH, &whEndM)
	}

	busy := lookupBusy(ctx, token, calendarID, tMin, tMax)
	type interval struct{ start, end time.Time }
	intervals := make([]interval, 0, len(busy))
	for _, b := range busy {
		s, err1 := time.Parse(time.RFC3339, b["start"])
		e, err2 := time.Parse(time.RFC3339, b["end"])
		if err1 == nil && err2 == nil {
			intervals = append(intervals, interval{s, e})
		}
	}

	duration := time.Duration(durationMin) * time.Minute
	step := 15 * time.Minute
	// Walk in 15-minute increments aligned to time_min.
	for slotStart := tMin; !slotStart.Add(duration).After(tMax); slotStart = slotStart.Add(step) {
		slotEnd := slotStart.Add(duration)

		// Working hours check (compare in slotStart's location).
		if useWH && whStartH >= 0 && whEndH >= 0 {
			loc := slotStart.Location()
			dayStart := time.Date(slotStart.Year(), slotStart.Month(), slotStart.Day(), whStartH, whStartM, 0, 0, loc)
			dayEnd := time.Date(slotStart.Year(), slotStart.Month(), slotStart.Day(), whEndH, whEndM, 0, 0, loc)
			if slotStart.Before(dayStart) || slotEnd.After(dayEnd) {
				continue
			}
		}

		// Conflict check.
		overlap := false
		for _, iv := range intervals {
			if slotStart.Before(iv.end) && iv.start.Before(slotEnd) {
				overlap = true
				break
			}
		}
		if !overlap {
			return opResult{result: map[string]any{
				"found":            true,
				"start":            slotStart.Format(time.RFC3339),
				"end":              slotEnd.Format(time.RFC3339),
				"duration_minutes": durationMin,
				"calendar_id":      calendarID,
			}}
		}
	}
	return opResult{result: map[string]any{
		"found":   false,
		"message": fmt.Sprintf("No free %d-minute slot found between %s and %s", durationMin, tMinStr, tMaxStr),
	}}
}

// execGmailWaitForReplies polls Gmail for replies matching the criteria,
// returning when at least min_count match or when max_wait_seconds elapse.
//
// IMPORTANT: vault.execute has a hard 300s timeout, so this can wait at most
// ~5 minutes per call. For longer waits (e.g. H1's 1-hour deadline) the agent
// should loop this call up to 12 times, persisting state across iterations via
// the memory component.
//
// params: {subject_contains?, from?, since (RFC3339, required),
//          min_count? (default 1), poll_interval_seconds? (default 30),
//          max_wait_seconds? (default 240)}
func execGmailWaitForReplies(ctx context.Context, credential string, params map[string]any) opResult {
	cred, err := parseGoogleOAuthCredential(credential)
	if err != nil {
		return opResult{err: err, code: ErrCodeCredentialUnavailable}
	}

	sinceStr, _ := params["since"].(string)
	if sinceStr == "" {
		return opResult{err: fmt.Errorf("since (RFC3339) is required so only fresh replies are counted"), code: ErrCodeInvalidParams}
	}
	since, err := parseICSTime(sinceStr)
	if err != nil {
		return opResult{err: fmt.Errorf("invalid since: %w", err), code: ErrCodeInvalidParams}
	}

	subjectContains, _ := params["subject_contains"].(string)
	fromAddr, _ := params["from"].(string)

	minCount := 1
	if v, ok := params["min_count"].(float64); ok && int(v) > 0 {
		minCount = int(v)
	}
	pollInterval := 30
	if v, ok := params["poll_interval_seconds"].(float64); ok && int(v) > 0 {
		pollInterval = int(v)
	}
	maxWait := 240
	if v, ok := params["max_wait_seconds"].(float64); ok && int(v) > 0 {
		if int(v) > 270 { // keep margin under 300s vault deadline
			maxWait = 270
		} else {
			maxWait = int(v)
		}
	}

	// Build Gmail search query. Restrict to in:inbox so we don't count outbound
	// emails the user just sent (those land in /sent and match the same time
	// window). Also exclude messages from the user's own address as a belt-and-
	// suspenders filter (some Gmail accounts route self-cc'd mail to inbox).
	queryParts := []string{
		"in:inbox",
		fmt.Sprintf("-from:%s", cred.Email),
		fmt.Sprintf("after:%d", since.Unix()),
	}
	if subjectContains != "" {
		queryParts = append(queryParts, fmt.Sprintf("subject:%q", subjectContains))
	}
	if fromAddr != "" {
		queryParts = append(queryParts, fmt.Sprintf("from:%s", fromAddr))
	}
	query := strings.Join(queryParts, " ")

	deadline := time.Now().Add(time.Duration(maxWait) * time.Second)
	start := time.Now()

	for {
		token, err := resolveAccessToken(ctx, cred)
		if err != nil {
			return opResult{err: err, code: ErrCodeCredentialUnavailable}
		}
		apiURL := fmt.Sprintf(
			"https://gmail.googleapis.com/gmail/v1/users/me/messages?q=%s&maxResults=20",
			url.QueryEscape(query),
		)
		body, sc, err := httpGET(ctx, apiURL, map[string]string{"Authorization": "Bearer " + token})
		if err == nil && sc < 400 {
			var listResp struct {
				Messages []struct {
					ID string `json:"id"`
				} `json:"messages"`
			}
			if json.Unmarshal(body, &listResp) == nil && len(listResp.Messages) > 0 {
				// Fetch metadata for each, then drop anything that's actually outbound.
				replies := make([]map[string]any, 0, len(listResp.Messages))
				for _, m := range listResp.Messages {
					metaURL := fmt.Sprintf(
						"https://gmail.googleapis.com/gmail/v1/users/me/messages/%s?format=metadata&metadataHeaders=From&metadataHeaders=Subject",
						url.PathEscape(m.ID),
					)
					mb, msc, merr := httpGET(ctx, metaURL, map[string]string{"Authorization": "Bearer " + token})
					entry := map[string]any{"id": m.ID}
					fromHeader := ""
					if merr == nil && msc < 400 {
						var meta struct {
							Snippet string `json:"snippet"`
							Payload struct {
								Headers []struct {
									Name, Value string
								} `json:"headers"`
							} `json:"payload"`
						}
						if json.Unmarshal(mb, &meta) == nil {
							entry["snippet"] = meta.Snippet
							for _, h := range meta.Payload.Headers {
								switch h.Name {
								case "From":
									entry["from"] = h.Value
									fromHeader = h.Value
								case "Subject":
									entry["subject"] = h.Value
								}
							}
						}
					}
					// Skip if the sender is the user themselves (any outbound that
					// leaked through the in:inbox filter).
					if fromHeader != "" && strings.Contains(strings.ToLower(fromHeader), strings.ToLower(cred.Email)) {
						continue
					}
					replies = append(replies, entry)
				}
				// When we've hit the threshold, do one extra grace poll after a
				// short delay so replies that arrived at the same time (within the
				// same poll window) are also captured before we return.
				if len(replies) >= minCount {
					// Short grace period to collect stragglers that arrived concurrently.
					gracePeriod := 15 * time.Second
					if time.Now().Add(gracePeriod).Before(deadline) {
						select {
						case <-ctx.Done():
						case <-time.After(gracePeriod):
						}
						// One additional fetch to pick up any replies that landed
						// in the same burst.
						token2, err2 := resolveAccessToken(ctx, cred)
						if err2 == nil {
							body2, sc2, err2 := httpGET(ctx, fmt.Sprintf(
								"https://gmail.googleapis.com/gmail/v1/users/me/messages?q=%s&maxResults=20",
								url.QueryEscape(query),
							), map[string]string{"Authorization": "Bearer " + token2})
							if err2 == nil && sc2 < 400 {
								var listResp2 struct {
									Messages []struct {
										ID string `json:"id"`
									} `json:"messages"`
								}
								if json.Unmarshal(body2, &listResp2) == nil {
									seen := make(map[string]bool, len(replies))
									for _, r := range replies {
										if id, ok := r["id"].(string); ok {
											seen[id] = true
										}
									}
									for _, m2 := range listResp2.Messages {
										if seen[m2.ID] {
											continue
										}
										metaURL2 := fmt.Sprintf(
											"https://gmail.googleapis.com/gmail/v1/users/me/messages/%s?format=metadata&metadataHeaders=From&metadataHeaders=Subject",
											url.PathEscape(m2.ID),
										)
										mb2, msc2, merr2 := httpGET(ctx, metaURL2, map[string]string{"Authorization": "Bearer " + token2})
										entry2 := map[string]any{"id": m2.ID}
										fromHeader2 := ""
										if merr2 == nil && msc2 < 400 {
											var meta2 struct {
												Snippet string `json:"snippet"`
												Payload struct {
													Headers []struct{ Name, Value string } `json:"headers"`
												} `json:"payload"`
											}
											if json.Unmarshal(mb2, &meta2) == nil {
												entry2["snippet"] = meta2.Snippet
												for _, h := range meta2.Payload.Headers {
													switch h.Name {
													case "From":
														entry2["from"] = h.Value
														fromHeader2 = h.Value
													case "Subject":
														entry2["subject"] = h.Value
													}
												}
											}
										}
										if fromHeader2 != "" && strings.Contains(strings.ToLower(fromHeader2), strings.ToLower(cred.Email)) {
											continue
										}
										replies = append(replies, entry2)
									}
								}
							}
						}
					}
					return opResult{result: map[string]any{
						"found":           true,
						"count":           len(replies),
						"min_count":       minCount,
						"messages":        replies,
						"elapsed_seconds": int(time.Since(start).Seconds()),
						"query":           query,
					}}
				}
			}
		}

		// Bail when the next poll would push us past the deadline.
		if time.Now().Add(time.Duration(pollInterval) * time.Second).After(deadline) {
			return opResult{result: map[string]any{
				"found":           false,
				"count":           0,
				"min_count":       minCount,
				"elapsed_seconds": int(time.Since(start).Seconds()),
				"timed_out":       true,
				"query":           query,
				"hint":            "Call again with the same `since` to keep waiting (max 5 min per call).",
			}}
		}

		select {
		case <-ctx.Done():
			return opResult{err: fmt.Errorf("wait cancelled: %w", ctx.Err()), code: ErrCodeUpstreamError}
		case <-time.After(time.Duration(pollInterval) * time.Second):
			// continue polling
		}
	}
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
