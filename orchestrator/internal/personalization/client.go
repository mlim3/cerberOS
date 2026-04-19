// Package personalization fetches user-scoped facts from the Memory service's
// personal_info API and returns them as short strings suitable for injection
// into planner / agent prompts. It is deliberately small and tolerant:
//   - Any network or decode error is returned so the caller can proceed without
//     personalization; personalization is a quality-of-life feature, never a
//     precondition for task execution.
//   - The HTTP client is wrapped with otelhttp so `traceparent` propagates to
//     the Memory service and the span appears nested in Tempo.
package personalization

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Client fetches personal_info from the Memory service over HTTP.
type Client struct {
	base string
	http *http.Client
}

// New constructs a Client. baseURL is the Memory service root (e.g.
// http://memory-api:8081). An empty or clearly-invalid URL returns nil so
// callers can easily treat personalization as optional.
func New(baseURL string) *Client {
	b := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if b == "" {
		return nil
	}
	return &Client{
		base: b,
		http: &http.Client{
			Timeout:   3 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

type factEnvelope struct {
	Data struct {
		Facts []struct {
			Category  string      `json:"category"`
			FactKey   string      `json:"factKey"`
			FactValue interface{} `json:"factValue"`
		} `json:"facts"`
	} `json:"data"`
}

// FetchFacts returns up to maxFacts short strings describing the user's
// stored facts. When the user has no facts or the Memory service is
// unavailable, returns a nil slice and no error — personalization is optional.
func (c *Client) FetchFacts(ctx context.Context, userID string, maxFacts int) ([]string, error) {
	if c == nil || userID == "" {
		return nil, nil
	}
	url := fmt.Sprintf("%s/api/v1/personal_info/%s/all", c.base, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build personalization request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil // optional: treat transport error as "no personalization"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// The default user is not provisioned in Memory yet — safe to ignore.
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("personalization: unexpected status %d", resp.StatusCode)
	}
	var env factEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode personalization response: %w", err)
	}
	out := make([]string, 0, len(env.Data.Facts))
	for _, f := range env.Data.Facts {
		key := strings.TrimSpace(f.FactKey)
		val := formatValue(f.FactValue)
		if key == "" || val == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s = %s", key, val))
		if maxFacts > 0 && len(out) >= maxFacts {
			break
		}
	}
	return out, nil
}

func formatValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
