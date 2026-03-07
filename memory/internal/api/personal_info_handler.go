package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type PersonalInfoHandler struct {
	processor *logic.Processor
	repo      storage.Repository
}

func NewPersonalInfoHandler(processor *logic.Processor, repo storage.Repository) *PersonalInfoHandler {
	return &PersonalInfoHandler{
		processor: processor,
		repo:      repo,
	}
}

type SavePersonalInfoRequest struct {
	Content      string `json:"content"`
	SourceType   string `json:"sourceType"`
	SourceID     string `json:"sourceId"`
	ExtractFacts bool   `json:"extractFacts"`
}

func (h *PersonalInfoHandler) Save(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "User ID is required", nil))
		return
	}

	var req SavePersonalInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid JSON payload", nil))
		return
	}

	saveReq := logic.SaveRequest{
		UserID:       userId,
		Content:      req.Content,
		SourceType:   req.SourceType,
		SourceID:     req.SourceID,
		ExtractFacts: req.ExtractFacts,
	}

	resp, err := h.processor.SavePersonalInfo(r.Context(), saveReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "Failed to save personal info", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]interface{}{
		"chunkIds":           resp.ChunkIDs,
		"factIds":            resp.FactIDs,
		"sourceReferenceIds": resp.SourceReferenceIDs,
	}))
}

type QueryPersonalInfoRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"topK"`
}

func (h *PersonalInfoHandler) Query(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "User ID is required", nil))
		return
	}

	var req QueryPersonalInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid JSON payload", nil))
		return
	}

	if req.TopK <= 0 {
		req.TopK = 5
	}

	results, err := h.processor.SemanticQuery(r.Context(), userId, req.Query, req.TopK)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "Failed to query personal info", err.Error()))
		return
	}

	// Format results for response
	type QueryResultResponse struct {
		ChunkID          string        `json:"chunkId"`
		Text             string        `json:"text"`
		SimilarityScore  float64       `json:"similarityScore"`
		SourceReferences []interface{} `json:"sourceReferences"`
	}

	formattedResults := make([]QueryResultResponse, len(results))
	for i, res := range results {
		formattedRefs := make([]interface{}, len(res.SourceReferences))
		for j, ref := range res.SourceReferences {
			formattedRefs[j] = map[string]interface{}{
				"sourceReferenceId": h.formatUUID(ref.ID),
				"targetId":          h.formatUUID(ref.TargetID),
				"targetType":        ref.TargetType,
				"sourceId":          h.formatUUID(ref.SourceID),
				"sourceType":        ref.SourceType,
			}
		}
		formattedResults[i] = QueryResultResponse{
			ChunkID:          res.ChunkID,
			Text:             res.Text,
			SimilarityScore:  res.SimilarityScore,
			SourceReferences: formattedRefs,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]interface{}{
		"results": formattedResults,
	}))
}

func (h *PersonalInfoHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "User ID is required", nil))
		return
	}

	var userUUID pgtype.UUID
	if err := userUUID.Scan(userId); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid User ID format", nil))
		return
	}

	facts, err := h.repo.Querier().GetAllFacts(r.Context(), userUUID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "Failed to get facts", err.Error()))
		return
	}

	chunks, err := h.repo.Querier().GetAllChunks(r.Context(), userUUID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "Failed to get chunks", err.Error()))
		return
	}

	// Format response
	type FactResponse struct {
		FactID     string      `json:"factId"`
		UserID     string      `json:"userId"`
		Category   string      `json:"category"`
		FactKey    string      `json:"factKey"`
		FactValue  interface{} `json:"factValue"`
		Confidence float64     `json:"confidence"`
		Version    int32       `json:"version"`
		UpdatedAt  string      `json:"updatedAt"`
	}

	type ChunkResponse struct {
		ChunkID      string `json:"chunkId"`
		UserID       string `json:"userId"`
		RawText      string `json:"rawText"`
		ModelVersion string `json:"modelVersion"`
		CreatedAt    string `json:"createdAt"`
	}

	formattedFacts := make([]FactResponse, len(facts))
	for i, f := range facts {
		var fv interface{}
		json.Unmarshal(f.FactValue, &fv)
		formattedFacts[i] = FactResponse{
			FactID:     h.formatUUID(f.ID),
			UserID:     h.formatUUID(f.UserID),
			Category:   f.Category.String,
			FactKey:    f.FactKey,
			FactValue:  fv,
			Confidence: f.Confidence.Float64,
			Version:    f.Version.Int32,
			UpdatedAt:  f.UpdatedAt.Time.Format("2006-01-02T15:04:05Z"),
		}
	}

	formattedChunks := make([]ChunkResponse, len(chunks))
	for i, c := range chunks {
		formattedChunks[i] = ChunkResponse{
			ChunkID:      h.formatUUID(c.ID),
			UserID:       h.formatUUID(c.UserID),
			RawText:      c.RawText,
			ModelVersion: c.ModelVersion,
			CreatedAt:    c.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]interface{}{
		"facts":  formattedFacts,
		"chunks": formattedChunks,
	}))
}

type UpdateFactRequest struct {
	Category   string      `json:"category"`
	FactKey    string      `json:"factKey"`
	FactValue  interface{} `json:"factValue"`
	Confidence float64     `json:"confidence"`
	Version    int32       `json:"version"`
}

func (h *PersonalInfoHandler) UpdateFact(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	factId := r.PathValue("factId")

	if userId == "" || factId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "User ID and Fact ID are required", nil))
		return
	}

	var req UpdateFactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid JSON payload", nil))
		return
	}

	var userUUID, factUUID pgtype.UUID
	if err := userUUID.Scan(userId); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid User ID format", nil))
		return
	}
	if err := factUUID.Scan(factId); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid Fact ID format", nil))
		return
	}

	factBytes, err := json.Marshal(req.FactValue)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid fact value", nil))
		return
	}

	var cat pgtype.Text
	cat.Scan(req.Category)

	fact, err := h.repo.Querier().UpdateFactWithVersion(r.Context(), storage.UpdateFactWithVersionParams{
		UserID:     userUUID,
		ID:         factUUID,
		Category:   cat,
		FactKey:    req.FactKey,
		FactValue:  factBytes,
		Confidence: pgtype.Float8{Float64: req.Confidence, Valid: true},
		Version:    pgtype.Int4{Int32: req.Version, Valid: true}, // Optimistic Concurrency Check happens here
	})

	if err != nil {
		if err.Error() == "no rows in result set" {
			// Check if it exists at all to differentiate NotFound from Conflict
			_, checkErr := h.repo.Querier().GetFactByID(r.Context(), storage.GetFactByIDParams{
				UserID: userUUID,
				ID:     factUUID,
			})
			if checkErr != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(ErrorResponse("not_found", "Fact not found", nil))
				return
			}
			// It exists but version didn't match
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(ErrorResponse("conflict", "Version mismatch - fact has been modified", nil))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "Failed to update fact", err.Error()))
		return
	}

	var fv interface{}
	json.Unmarshal(fact.FactValue, &fv)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]interface{}{
		"fact": map[string]interface{}{
			"factId":     h.formatUUID(fact.ID),
			"userId":     h.formatUUID(fact.UserID),
			"category":   fact.Category.String,
			"factKey":    fact.FactKey,
			"factValue":  fv,
			"confidence": fact.Confidence.Float64,
			"version":    fact.Version.Int32,
			"updatedAt":  fact.UpdatedAt.Time.Format("2006-01-02T15:04:05Z"),
		},
	}))
}

func (h *PersonalInfoHandler) formatUUID(u pgtype.UUID) string {
	b := u.Bytes
	return string([]byte{
		hexChar(b[0] >> 4), hexChar(b[0] & 0x0f),
		hexChar(b[1] >> 4), hexChar(b[1] & 0x0f),
		hexChar(b[2] >> 4), hexChar(b[2] & 0x0f),
		hexChar(b[3] >> 4), hexChar(b[3] & 0x0f),
		'-',
		hexChar(b[4] >> 4), hexChar(b[4] & 0x0f),
		hexChar(b[5] >> 4), hexChar(b[5] & 0x0f),
		'-',
		hexChar(b[6] >> 4), hexChar(b[6] & 0x0f),
		hexChar(b[7] >> 4), hexChar(b[7] & 0x0f),
		'-',
		hexChar(b[8] >> 4), hexChar(b[8] & 0x0f),
		hexChar(b[9] >> 4), hexChar(b[9] & 0x0f),
		'-',
		hexChar(b[10] >> 4), hexChar(b[10] & 0x0f),
		hexChar(b[11] >> 4), hexChar(b[11] & 0x0f),
		hexChar(b[12] >> 4), hexChar(b[12] & 0x0f),
		hexChar(b[13] >> 4), hexChar(b[13] & 0x0f),
		hexChar(b[14] >> 4), hexChar(b[14] & 0x0f),
		hexChar(b[15] >> 4), hexChar(b[15] & 0x0f),
	})
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}
