// Package databusproxy implements HTTP proxy routes for aegis-databus when it
// uses AEGIS_ORCHESTRATOR_URL: DataBus calls /v1/databus/* on the orchestrator,
// which must forward to the Memory API under MEMORY_ENDPOINT (e.g. .../v1/memory/*).
package databusproxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// New returns a handler that reverse-proxies /v1/databus/* to the Memory HTTP API.
// memoryBaseURL is the full base of the Memory component REST API, for example
// http://memory-api:8081/v1/memory (no trailing slash).
func New(memoryBaseURL string) http.Handler {
	memoryBaseURL = strings.TrimSuffix(strings.TrimSpace(memoryBaseURL), "/")
	base, err := url.Parse(memoryBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "invalid MEMORY_ENDPOINT for databus proxy", http.StatusInternalServerError)
		})
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			path := req.URL.Path
			if strings.HasPrefix(path, "/v1/databus/") {
				path = strings.TrimPrefix(path, "/v1/databus/")
				if !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
			}
			req.URL.Scheme = base.Scheme
			req.URL.Host = base.Host
			req.URL.Path = strings.TrimSuffix(base.Path, "/") + path
			req.Host = base.Host
		},
	}
	return proxy
}
