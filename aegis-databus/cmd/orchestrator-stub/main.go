// STUB: Simulates Orchestrator proxy for DataBus → Orchestrator → Memory demo.
// Receives storage requests at /v1/databus/* and forwards to Memory.
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

const (
	defaultAddr      = ":8091"
	defaultMemoryURL = "http://localhost:8090/v1/memory"
)

func main() {
	memoryURL := os.Getenv("AEGIS_MEMORY_URL")
	if memoryURL == "" {
		memoryURL = defaultMemoryURL
	}
	memoryURL = strings.TrimSuffix(memoryURL, "/")

	base, _ := url.Parse(memoryURL)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// /v1/databus/outbox/pending -> {memoryURL}/outbox/pending
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
		},
	}

	// Handle request body for reverse proxy (httputil.ReverseProxy buffers it)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/databus/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := os.Getenv("ORCHESTRATOR_STUB_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	log.Printf("orchestrator-stub listening on %s, forwarding to %s", addr, memoryURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
