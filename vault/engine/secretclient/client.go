package secretclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// Client calls the secretstore service to resolve secret placeholders.
// It implements preprocessor.SecretStore via the Resolve method.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{},
	}
}

type resolveRequest struct {
	Keys []string `json:"keys"`
}

type resolveResponse struct {
	Secrets map[string]string `json:"secrets"`
}

// Resolve sends a batch of secret keys to the secretstore and returns the
// resolved key→value map. A single HTTP call regardless of key count.
func (c *Client) Resolve(keys []string) (map[string]string, error) {
	body, err := json.Marshal(resolveRequest{Keys: keys})
	if err != nil {
		return nil, fmt.Errorf("secretclient: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/secrets/resolve", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("secretclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Engine-Token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("secretclient: request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("secretclient: unauthorized — check SECRET_STORE_TOKEN")
	case http.StatusNotFound:
		return nil, fmt.Errorf("secretclient: one or more secrets not found")
	default:
		return nil, fmt.Errorf("secretclient: unexpected status %d", resp.StatusCode)
	}

	var result resolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("secretclient: decode response: %w", err)
	}

	return result.Secrets, nil
}
