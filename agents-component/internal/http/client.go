// Package http provides a thin HTTP client for making direct outbound calls to
// public URLs. This is the ONLY package in agents-component permitted to make
// outbound HTTP connections other than internal/comms (which handles NATS).
//
// Permitted use: public web.fetch and web.extract calls where no credentials
// are required. Credentialed external calls (e.g. web.search via Tavily) must
// still route through internal/credentials → Orchestrator → Vault.
//
// This package must never accept, inject, log, or forward credential material
// of any kind. The AuditFunc callback logs URL, method, and status code only —
// never the response body.
package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultMaxBodyBytes = 10 * 1024 * 1024 // 10 MB

// Response is the result of a completed HTTP call.
type Response struct {
	StatusCode int
	Body       []byte
	Headers    map[string][]string
	ElapsedMS  int64
}

// AuditFunc is called after each outbound request completes. Implementations
// must be non-blocking. Only URL, method, status code, and elapsed time are
// passed — the response body must never be forwarded to audit logs.
type AuditFunc func(url, method string, statusCode int, elapsedMS int64)

// Client makes direct HTTP calls to public URLs.
type Client interface {
	Get(ctx context.Context, url string, headers map[string]string) (*Response, error)
	Post(ctx context.Context, url string, headers map[string]string, body []byte) (*Response, error)
}

type client struct {
	http    *http.Client
	maxBody int64
	audit   AuditFunc
}

// Option configures a Client.
type Option func(*client)

// WithMaxBody sets the maximum response body size in bytes (default: 10 MB).
// Responses exceeding this limit are truncated — the caller receives the first
// n bytes and no error.
func WithMaxBody(n int64) Option { return func(c *client) { c.maxBody = n } }

// WithAudit registers a callback fired after every request. Use this to emit
// AuditEventHTTPOutbound events via the audit subject in agents-component.
func WithAudit(f AuditFunc) Option { return func(c *client) { c.audit = f } }

// New returns a Client with the given timeout. The timeout is a hard deadline
// for the entire request including connect, write, and read phases.
// Maximum permitted timeout is 120 seconds; values above this are clamped.
func New(timeout time.Duration, opts ...Option) Client {
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}
	c := &client{
		http:    &http.Client{Timeout: timeout},
		maxBody: defaultMaxBodyBytes,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *client) Get(ctx context.Context, url string, headers map[string]string) (*Response, error) {
	return c.do(ctx, http.MethodGet, url, headers, nil)
}

func (c *client) Post(ctx context.Context, url string, headers map[string]string, body []byte) (*Response, error) {
	return c.do(ctx, http.MethodPost, url, headers, body)
}

func (c *client) do(ctx context.Context, method, url string, headers map[string]string, body []byte) (*Response, error) {
	start := time.Now()

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http: build request %s %s: %w", method, url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: request %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody))
	if err != nil {
		return nil, fmt.Errorf("http: read response body: %w", err)
	}

	elapsed := time.Since(start).Milliseconds()
	if c.audit != nil {
		c.audit(url, method, resp.StatusCode, elapsed)
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Headers:    map[string][]string(resp.Header),
		ElapsedMS:  elapsed,
	}, nil
}
