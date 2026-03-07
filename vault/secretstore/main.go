package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type resolveRequest struct {
	Keys []string `json:"keys"`
}

type resolveResponse struct {
	Secrets map[string]string `json:"secrets"`
}

type server struct {
	manager SecretManager
	token   string
}

// engineOnly is middleware that rejects any request not carrying the shared
// engine token. This is the single access-control gate — no other service
// should know the token or reach this port.
func (s *server) engineOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Engine-Token") != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Keys) == 0 {
		http.Error(w, "keys must not be empty", http.StatusBadRequest)
		return
	}

	secrets, err := s.manager.GetSecrets(req.Keys)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resolveResponse{Secrets: secrets})
}

func main() {
	token := os.Getenv("ENGINE_TOKEN")
	if token == "" {
		log.Fatal("ENGINE_TOKEN env var is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8001"
	}

	s := &server{
		manager: NewMockSecretManager(),
		token:   token,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/secrets/resolve", s.engineOnly(s.handleResolve))

	log.Printf("secretstore listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
