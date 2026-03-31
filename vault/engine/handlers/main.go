// Package handlers implements the vault HTTP API (inject and direct secret CRUD).
package handlers

import (
	"net/http"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/handlers/inject"
	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// Server composes route handlers for the vault HTTP API.
type Server struct {
	inj *inject.Handler
	sec *secrets.Handler
}

// New returns a Server wired for inject and secret routes.
func New(pp *preprocessor.Preprocessor, auditor *audit.Logger, manager secretmanager.SecretManager) *Server {
	return &Server{
		inj: &inject.Handler{PP: pp, Auditor: auditor},
		sec: &secrets.Handler{Manager: manager, Auditor: auditor},
	}
}

// Inject handles POST /inject.
func (s *Server) Inject(w http.ResponseWriter, r *http.Request) {
	s.inj.Inject(w, r)
}

// SecretGet handles POST /secrets/get.
func (s *Server) SecretGet(w http.ResponseWriter, r *http.Request) {
	s.sec.SecretGet(w, r)
}

// SecretPut handles POST /secrets/put.
func (s *Server) SecretPut(w http.ResponseWriter, r *http.Request) {
	s.sec.SecretPut(w, r)
}

// SecretDelete handles POST /secrets/delete.
func (s *Server) SecretDelete(w http.ResponseWriter, r *http.Request) {
	s.sec.SecretDelete(w, r)
}

// Type aliases for callers and tests that decode JSON without importing subpackages.
type (
	ErrorResponse     = common.ErrorResponse
	SecretGetResponse = secrets.SecretGetResponse
)
