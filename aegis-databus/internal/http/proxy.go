package http

import (
	"io"
	"net/http"
)

// ProxyToNATSMonitoring forwards requests to NATS HTTP monitoring port (Interface 4).
// EDD: GET /varz, /connz, /jsz expose server stats, connections, JetStream.
func ProxyToNATSMonitoring(natsHTTPBase string) func(http.ResponseWriter, *http.Request) {
	client := &http.Client{}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, natsHTTPBase+path, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}
