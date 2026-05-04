package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

// UsersHandler exposes the demo-mode user switcher list. This is a roster of
// who can be selected from the IO dropdown — NOT an authenticated identity
// provider. Real auth (MT-1) replaces the dropdown entirely.
type UsersHandler struct {
	repo   storage.Repository
	logger *slog.Logger
}

func NewUsersHandler(repo storage.Repository, logger *slog.Logger) *UsersHandler {
	return &UsersHandler{repo: repo, logger: logger}
}

type UserSummary struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
}

// HandleListUsers returns every row in identity_schema.users as {id, email}.
// @Summary List users (demo mode)
// @Description Returns the roster of users for the IO user-switcher dropdown.
// @Tags users
// @Produce json
// @Success 200 {object} map[string][]UserSummary "OK"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/users [get]
func (h *UsersHandler) HandleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.ListUsers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to list users", err.Error())
		return
	}
	resp := make([]UserSummary, 0, len(users))
	for _, u := range users {
		resp = append(resp, UserSummary{
			ID:    uuid.UUID(u.ID.Bytes),
			Email: u.Email,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"users": resp}))
}
