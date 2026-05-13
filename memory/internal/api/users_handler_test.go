package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/pgvector/pgvector-go"
)

type stubUsersRepo struct {
	countRootsFunc     func(context.Context) (int, error)
	getUserByEmailFunc func(context.Context, string) (storage.UserRecord, bool, error)
	createUserFunc     func(context.Context, pgtype.UUID, string, string) (storage.UserRecord, error)
	updateUserRoleFunc func(context.Context, pgtype.UUID, string) error
}

func (s *stubUsersRepo) Querier() *storage.Queries { return nil }
func (s *stubUsersRepo) WithTx(context.Context, func(q *storage.Queries) error) error {
	return nil
}
func (s *stubUsersRepo) UserExists(context.Context, pgtype.UUID) (bool, error) { return false, nil }
func (s *stubUsersRepo) ListUsers(context.Context) ([]storage.UserRecord, error) { return nil, nil }
func (s *stubUsersRepo) CreateUser(ctx context.Context, id pgtype.UUID, email, role string) (storage.UserRecord, error) {
	return s.createUserFunc(ctx, id, email, role)
}
func (s *stubUsersRepo) GetUserByEmail(ctx context.Context, email string) (storage.UserRecord, bool, error) {
	return s.getUserByEmailFunc(ctx, email)
}
func (s *stubUsersRepo) GetUser(context.Context, pgtype.UUID) (storage.UserRecord, bool, error) {
	return storage.UserRecord{}, false, nil
}
func (s *stubUsersRepo) UpdateUserRole(ctx context.Context, userID pgtype.UUID, role string) error {
	if s.updateUserRoleFunc == nil {
		return nil
	}
	return s.updateUserRoleFunc(ctx, userID, role)
}
func (s *stubUsersRepo) CountRoots(ctx context.Context) (int, error) {
	return s.countRootsFunc(ctx)
}
func (s *stubUsersRepo) QueryChunksBySimilarity(context.Context, pgtype.UUID, pgvector.Vector, int32) ([]storage.PersonalInfoChunkMatch, error) {
	return nil, nil
}
func (s *stubUsersRepo) ListFacts(context.Context, pgtype.UUID, bool) ([]storage.FactRecord, error) {
	return nil, nil
}
func (s *stubUsersRepo) ArchiveFact(context.Context, pgtype.UUID, pgtype.UUID, string) error {
	return nil
}
func (s *stubUsersRepo) SupersedeFact(context.Context, pgtype.UUID, pgtype.UUID, pgtype.Text, string, []byte, float64) (pgtype.UUID, error) {
	return pgtype.UUID{}, nil
}

func TestHandleCreateUser_FirstRootBootstrapReusesExistingEmailRow(t *testing.T) {
	existingID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	var createdWith pgtype.UUID

	repo := &stubUsersRepo{
		countRootsFunc: func(context.Context) (int, error) {
			return 0, nil
		},
		getUserByEmailFunc: func(_ context.Context, email string) (storage.UserRecord, bool, error) {
			if email != "colby@example.com" {
				t.Fatalf("unexpected email lookup: %q", email)
			}
			return storage.UserRecord{
				ID:    pgtype.UUID{Bytes: existingID, Valid: true},
				Email: email,
				Role:  "user",
			}, true, nil
		},
		createUserFunc: func(_ context.Context, id pgtype.UUID, email, role string) (storage.UserRecord, error) {
			createdWith = id
			return storage.UserRecord{
				ID:    id,
				Email: email,
				Role:  role,
			}, nil
		},
	}

	h := NewUsersHandler(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{
		"email":"colby@example.com",
		"role":"root",
		"id":"00000000-0000-0000-0000-000000000001"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleCreateUser(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if createdWith.Bytes != existingID {
		t.Fatalf("CreateUser used id %s, want existing id %s", uuid.UUID(createdWith.Bytes), existingID)
	}

	var env struct {
		Ok   bool `json:"ok"`
		Data struct {
			User struct {
				ID    string `json:"id"`
				Email string `json:"email"`
				Role  string `json:"role"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Data.User.ID != existingID.String() {
		t.Fatalf("response id = %s, want %s", env.Data.User.ID, existingID.String())
	}
	if env.Data.User.Role != "root" {
		t.Fatalf("response role = %s, want root", env.Data.User.Role)
	}
}

func TestHandleCreateUser_ExplicitIDRejectsOtherUsersEmail(t *testing.T) {
	existingID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	repo := &stubUsersRepo{
		countRootsFunc: func(context.Context) (int, error) {
			return 1, nil
		},
		getUserByEmailFunc: func(_ context.Context, email string) (storage.UserRecord, bool, error) {
			return storage.UserRecord{
				ID:    pgtype.UUID{Bytes: existingID, Valid: true},
				Email: email,
				Role:  "user",
			}, true, nil
		},
		createUserFunc: func(context.Context, pgtype.UUID, string, string) (storage.UserRecord, error) {
			t.Fatal("CreateUser should not be called on conflicting email")
			return storage.UserRecord{}, nil
		},
	}

	h := NewUsersHandler(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{
		"email":"colby@example.com",
		"role":"user",
		"id":"00000000-0000-0000-0000-000000000001"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleCreateUser(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStubUsersRepoSatisfiesRepository(t *testing.T) {
	// Compile-time guard with a tiny runtime assertion to keep the stub honest.
	var _ storage.Repository = (*stubUsersRepo)(nil)
	if (&stubUsersRepo{}).Querier() != nil {
		t.Fatal("stub querier should remain nil")
	}
	_ = time.Now()
}
