package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/mlim3/cerberOS/memory/internal/logic"
)

// SkillCacheHandler handles skill upsert and semantic search requests from the
// Orchestrator. Clients are the Agents Component (via Orchestrator proxy) —
// never direct agent processes.
type SkillCacheHandler struct {
	proc *logic.SkillCacheProcessor
}

func NewSkillCacheHandler(proc *logic.SkillCacheProcessor) *SkillCacheHandler {
	return &SkillCacheHandler{proc: proc}
}

func skillCacheJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

type upsertSkillRequest struct {
	Domain      string          `json:"domain"`
	Name        string          `json:"name"`
	Origin      string          `json:"origin"`
	Description string          `json:"description"`
	Payload     json.RawMessage `json:"payload"`
	SeedHash    string          `json:"seed_hash"`
}

// Upsert handles POST /api/v1/skills/cache
func (h *SkillCacheHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	var req upsertSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("skill_cache upsert: invalid request body", "error", err)
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}
	if req.Domain == "" || req.Name == "" {
		slog.Warn("skill_cache upsert: missing domain or name", "domain", req.Domain, "name", req.Name)
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "domain and name are required", nil))
		return
	}

	slog.Info("skill_cache upsert: received",
		"domain", req.Domain, "name", req.Name, "origin", req.Origin,
		"description_len", len(req.Description), "has_description", req.Description != "")

	if err := h.proc.Upsert(r.Context(), logic.UpsertSkillRequest{
		Domain:      req.Domain,
		Name:        req.Name,
		Origin:      req.Origin,
		Description: req.Description,
		Payload:     req.Payload,
		SeedHash:    req.SeedHash,
	}); err != nil {
		slog.Error("skill_cache upsert: processor failed", "domain", req.Domain, "name", req.Name, "error", err)
		skillCacheJSON(w, http.StatusInternalServerError, ErrorResponse("internal", "skill upsert failed", nil))
		return
	}

	slog.Info("skill_cache upsert: success", "domain", req.Domain, "name", req.Name)
	skillCacheJSON(w, http.StatusCreated, SuccessResponse(map[string]interface{}{"status": "ok"}))
}

type searchSkillsRequest struct {
	Query  string `json:"query"`
	Domain string `json:"domain"`
	TopK   int    `json:"top_k"`
}

// Search handles POST /api/v1/skills/cache/search
func (h *SkillCacheHandler) Search(w http.ResponseWriter, r *http.Request) {
	var req searchSkillsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("skill_cache search: invalid request body", "error", err)
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}
	if req.Query == "" {
		slog.Warn("skill_cache search: empty query")
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "query is required", nil))
		return
	}

	slog.Info("skill_cache search: received",
		"query_len", len(req.Query), "domain", req.Domain, "top_k", req.TopK)

	results, err := h.proc.SemanticSearch(r.Context(), req.Query, req.Domain, req.TopK)
	if err != nil {
		slog.Error("skill_cache search: processor failed", "error", err)
		skillCacheJSON(w, http.StatusInternalServerError, ErrorResponse("internal", "skill search failed", nil))
		return
	}

	slog.Info("skill_cache search: success", "result_count", len(results), "domain", req.Domain)
	skillCacheJSON(w, http.StatusOK, SuccessResponse(map[string]interface{}{"results": results}))
}

// ListByDomain handles GET /api/v1/skills/cache/{domain}
func (h *SkillCacheHandler) ListByDomain(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if domain == "" {
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "domain is required", nil))
		return
	}

	results, err := h.proc.ListByDomain(r.Context(), domain)
	if err != nil {
		skillCacheJSON(w, http.StatusInternalServerError, ErrorResponse("internal", "list skills failed", nil))
		return
	}

	skillCacheJSON(w, http.StatusOK, SuccessResponse(map[string]interface{}{"skills": results}))
}

// Delete handles DELETE /api/v1/skills/cache/{domain}/{name}
func (h *SkillCacheHandler) Delete(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	name := r.PathValue("name")
	if domain == "" || name == "" {
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "domain and name are required", nil))
		return
	}

	if err := h.proc.Delete(r.Context(), domain, name); err != nil {
		skillCacheJSON(w, http.StatusInternalServerError, ErrorResponse("internal", "skill delete failed", nil))
		return
	}

	skillCacheJSON(w, http.StatusOK, SuccessResponse(map[string]interface{}{"status": "deleted"}))
}

// CheckSeedHash handles POST /api/v1/skills/cache/seed-check
func (h *SkillCacheHandler) CheckSeedHash(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SeedHash string `json:"seed_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		skillCacheJSON(w, http.StatusBadRequest, ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}

	present, err := h.proc.IsSeedHashPresent(r.Context(), req.SeedHash)
	if err != nil {
		skillCacheJSON(w, http.StatusInternalServerError, ErrorResponse("internal", "seed hash check failed", nil))
		return
	}

	skillCacheJSON(w, http.StatusOK, SuccessResponse(map[string]interface{}{"present": present}))
}
