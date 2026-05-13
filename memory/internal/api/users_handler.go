package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	Role  string    `json:"role"`
}

// HandleListUsers returns every row in identity_schema.users as {id, email, role}.
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
	rootCount, _ := h.repo.CountRoots(r.Context())
	resp := make([]UserSummary, 0, len(users))
	for _, u := range users {
		resp = append(resp, UserSummary{
			ID:    uuid.UUID(u.ID.Bytes),
			Email: u.Email,
			Role:  u.Role,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"users":      resp,
		"root_count": rootCount,
	}))
}

// CreateUserRequest is the body for POST /api/v1/users.
type CreateUserRequest struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty"` // root, manager, user (default: user)
	ID    string `json:"id,omitempty"`   // optional client-provided UUID; generated if omitted
}

// HandleCreateUser creates (or returns existing) user keyed on email.
// First-user-is-root: if no root exists yet, the requested role is honored
// (so the IO first-run flow can pass role='root'); otherwise creating a
// root through this endpoint is rejected — only an existing root/manager
// (enforced upstream by IO) should be promoting users.
func (h *UsersHandler) HandleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body", err.Error())
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "email is required", "")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}

	// First-user-is-root: only allow role='root' when no root exists yet.
	if req.Role == "root" {
		rootCount, err := h.repo.CountRoots(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to count roots", err.Error())
			return
		}
		if rootCount > 0 {
			writeJSONError(w, http.StatusForbidden, "forbidden", "root user already exists", "")
			return
		}
	}

	// Resolve user ID: explicit > existing-by-email > new UUID.
	var userID pgtype.UUID
	if req.ID != "" {
		parsed, err := uuid.Parse(req.ID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid id", err.Error())
			return
		}
		// First-run signup claims the seed UUID, but a real teammate may already
		// have a row under this email (e.g. from a prior demo or seed import).
		// In that bootstrap case, reuse the existing row's UUID and promote it
		// instead of colliding with the unique email constraint and surfacing 500.
		existing, found, err := h.repo.GetUserByEmail(r.Context(), req.Email)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "lookup by email failed", err.Error())
			return
		}
		if found {
			if existing.ID.Bytes != parsed && req.Role != "root" {
				writeJSONError(w, http.StatusConflict, "conflict", "email already belongs to another user", "")
				return
			}
			userID = existing.ID
		} else {
			userID = pgtype.UUID{Bytes: parsed, Valid: true}
		}
	} else if existing, found, err := h.repo.GetUserByEmail(r.Context(), req.Email); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "lookup by email failed", err.Error())
		return
	} else if found {
		userID = existing.ID
	} else {
		newID, err := uuid.NewV7()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "uuid generation failed", err.Error())
			return
		}
		userID = pgtype.UUID{Bytes: newID, Valid: true}
	}

	created, err := h.repo.CreateUser(r.Context(), userID, req.Email, req.Role)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to create user", err.Error())
		return
	}

	// If we created (or already had) a row but the role is still 'user' while
	// the caller asked for something stronger (manager/root, validated above),
	// promote it explicitly. CreateUser uses ON CONFLICT DO UPDATE on email
	// only and does not change the role on conflict — that's intentional so
	// re-running an idempotent bootstrap doesn't downgrade a real user.
	if created.Role != req.Role {
		if err := h.repo.UpdateUserRole(r.Context(), created.ID, req.Role); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to set role", err.Error())
			return
		}
		created.Role = req.Role
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"user": UserSummary{
			ID:    uuid.UUID(created.ID.Bytes),
			Email: created.Email,
			Role:  created.Role,
		},
	}))
}
