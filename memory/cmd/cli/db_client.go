package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

// dbClient implements MemoryClient using direct database connections
type dbClient struct {
	db            *storage.PostgresDB
	piProcessor   *logic.Processor
	chatRepo      *storage.ChatRepository
	agentLogsRepo *storage.AgentLogsRepository
	logRepo       *storage.LogRepository
	piRepo        *storage.BaseRepository // Expose direct repo access for facts
}

// NewDBClient initializes a direct database connection
func NewDBClient(ctx context.Context, dbURL string) (MemoryClient, error) {
	// Parse DB URL or use default config
	var cfg storage.Config

	if dbURL != "" && dbURL != "env" {
		// Just parse it manually for simplicity or pass it as a URL string to pgxpool if we had one
		// Since storage.NewPostgresDB takes a Config, we'll try to extract what we can
		// Or easier: we can update storage to accept a URL, but for now we'll set the defaults
		// based on environment, but actually the CLI is meant to run with DB_USER etc.
		// Let's modify this to just use the URL string directly with pgxpool to bypass Config
		// if a full URL is provided. But since storage.NewPostgresDB requires Config...

		// For the sake of CLI, if dbURL is a DSN string (postgres://...), we should let pgxpool handle it directly.
		// However, storage.PostgresDB requires a Config. So we'll parse the DSN into Config if possible.
	}

	// We'll rely on the environment variables mostly, but if dbURL is provided and is a valid DSN,
	// we should probably just use it.

	// Wait, the easiest way is to let pgxpool parse the DSN.
	// Let's just use the environment variables as the source of truth for the DB Client
	cfg = storage.Config{
		Host:     getEnvOrDefault("DB_HOST", "localhost"),
		Port:     getEnvOrDefault("DB_PORT", "5432"),
		User:     getEnvOrDefault("DB_USER", "postgres"),
		Password: getEnvOrDefault("DB_PASSWORD", "postgres"),
		Database: getEnvOrDefault("DB_NAME", "memory_os"),
	}

	// If a custom URL was provided, we'll just set it to use the DSN directly in a pgxpool.
	// But since we need `*storage.PostgresDB`, we'll just use the Config.
	db, err := storage.NewPostgresDB(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	pool := db.GetPool()

	// Initialize repos and logic
	piRepo := &storage.BaseRepository{Pool: pool}
	chatRepo := storage.NewChatRepository(pool)
	agentLogsRepo := storage.NewAgentLogsRepository(pool)
	logRepo := storage.NewLogRepository(pool)

	embeddingDim, err := strconv.Atoi(getEnvOrDefault("EMBEDDING_DIM", "768"))
	if err != nil {
		return nil, fmt.Errorf("invalid EMBEDDING_DIM: %w", err)
	}
	embeddingPromptStyle := getEnvOrDefault("EMBEDDING_PROMPT_STYLE", "embeddinggemma")
	embedder, err := logic.NewTEIEmbedder(
		os.Getenv("EMBEDDING_API_URL"),
		os.Getenv("EMBEDDING_MODEL"),
		embeddingDim,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize embedding client: %w", err)
	}

	piProcessor := logic.NewProcessor(piRepo, embedder, logic.WithPromptStyle(embeddingPromptStyle))

	return &dbClient{
		db:            db,
		piProcessor:   piProcessor,
		chatRepo:      chatRepo,
		agentLogsRepo: agentLogsRepo,
		logRepo:       logRepo,
		piRepo:        piRepo,
	}, nil
}

func (c *dbClient) QueryFacts(ctx context.Context, userID uuid.UUID, query string, topK int) ([]Fact, error) {
	results, err := c.piProcessor.SemanticQuery(ctx, userID.String(), query, topK)
	if err != nil {
		return nil, fmt.Errorf("semantic query failed: %w", err)
	}

	var facts []Fact
	for _, r := range results {
		chunkID, err := uuid.Parse(r.ChunkID)
		if err != nil {
			continue // skip invalid UUIDs
		}
		facts = append(facts, Fact{
			ID:      chunkID,
			Content: r.Text,
		})
	}
	if facts == nil {
		facts = []Fact{}
	}
	return facts, nil
}

func (c *dbClient) GetAllFacts(ctx context.Context, userID uuid.UUID) ([]Fact, error) {
	var userUUID pgtype.UUID
	if err := userUUID.Scan(userID.String()); err != nil {
		return nil, err
	}

	dbFacts, err := c.piRepo.Querier().GetAllFacts(ctx, userUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get all facts: %w", err)
	}

	var facts []Fact
	for _, f := range dbFacts {
		id, _ := uuid.Parse(formatUUID(f.ID))
		facts = append(facts, Fact{
			ID:      id,
			Content: decodeFactContent(f.FactValue),
		})
	}
	if facts == nil {
		facts = []Fact{}
	}
	return facts, nil
}

func (c *dbClient) SaveFact(ctx context.Context, userID uuid.UUID, fact string) error {
	var userUUID pgtype.UUID
	if err := userUUID.Scan(userID.String()); err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	exists, err := c.piRepo.UserExists(ctx, userUUID)
	if err != nil {
		return fmt.Errorf("failed to validate user: %w", err)
	}
	if !exists {
		return fmt.Errorf("user not found")
	}

	factValue, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("failed to encode fact value: %w", err)
	}

	factID := uuid.Must(uuid.NewV7())
	keySeed := strings.ToLower(strings.TrimSpace(fact))
	if len(keySeed) > 48 {
		keySeed = keySeed[:48]
	}
	keySeed = strings.ReplaceAll(keySeed, " ", "_")
	if keySeed == "" {
		keySeed = "cli_fact"
	}

	_, err = c.piRepo.Querier().UpsertFact(ctx, storage.UpsertFactParams{
		ID:        pgtype.UUID{Bytes: factID, Valid: true},
		UserID:    userUUID,
		Category:  pgtype.Text{String: "CLI", Valid: true},
		FactKey:   fmt.Sprintf("cli_%s_%s", keySeed, factID.String()[:8]),
		FactValue: factValue,
		Confidence: pgtype.Float8{
			Float64: 1.0,
			Valid:   true,
		},
		Version: pgtype.Int4{Int32: 1, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("failed to save fact: %w", err)
	}

	return nil
}

func (c *dbClient) GetConversationHistory(ctx context.Context, conversationID uuid.UUID, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}

	chatIDColumn, err := c.resolveChatIDColumn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve chat history schema: %w", err)
	}

	rows, err := c.db.GetPool().Query(ctx, fmt.Sprintf(`
SELECT id, role, content, created_at
FROM chat_schema.messages
WHERE %s = $1
ORDER BY created_at ASC
LIMIT $2`, chatIDColumn), conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat history: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var (
			id        uuid.UUID
			role      string
			content   string
			createdAt time.Time
		)
		if err := rows.Scan(&id, &role, &content, &createdAt); err != nil {
			return nil, fmt.Errorf("failed to scan chat history row: %w", err)
		}
		messages = append(messages, Message{
			ID:        id,
			Role:      role,
			Content:   content,
			CreatedAt: createdAt.String(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate chat history rows: %w", err)
	}
	if messages == nil {
		messages = []Message{}
	}

	return messages, nil
}

func (c *dbClient) resolveChatIDColumn(ctx context.Context) (string, error) {
	rows, err := c.db.GetPool().Query(ctx, `
SELECT column_name
FROM information_schema.columns
WHERE table_schema = 'chat_schema'
  AND table_name = 'messages'
  AND column_name IN ('session_id', 'conversation_id')`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	foundConversation := false
	for rows.Next() {
		var columnName string
		if err := rows.Scan(&columnName); err != nil {
			return "", err
		}
		if columnName == "session_id" {
			return "session_id", nil
		}
		if columnName == "conversation_id" {
			foundConversation = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if foundConversation {
		return "conversation_id", nil
	}
	return "", fmt.Errorf("neither session_id nor conversation_id exists on chat_schema.messages")
}

func (c *dbClient) GetAgentExecutions(ctx context.Context, taskID uuid.UUID, limit int) ([]AgentExecution, error) {
	var taskUUID pgtype.UUID
	if err := taskUUID.Scan(taskID.String()); err != nil {
		return nil, err
	}

	executions, err := c.agentLogsRepo.GetExecutionsByTaskIDLimit(ctx, taskUUID, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to get agent executions: %w", err)
	}

	var res []AgentExecution
	for _, ex := range executions {
		id, _ := uuid.Parse(formatUUID(ex.ID))
		res = append(res, AgentExecution{
			ID:        id,
			TaskID:    taskID,
			Status:    ex.Status,
			CreatedAt: ex.CreatedAt.Time.String(),
		})
	}
	if res == nil {
		res = []AgentExecution{}
	}
	return res, nil
}

func (c *dbClient) GetSystemEvents(ctx context.Context, limit int) ([]SystemEvent, error) {
	events, err := c.logRepo.ListSystemEvents(ctx, storage.ListSystemEventsParams{
		Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get system events: %w", err)
	}

	var res []SystemEvent
	for _, e := range events {
		id, _ := uuid.Parse(formatUUID(e.ID))
		res = append(res, SystemEvent{
			ID:        id,
			EventType: string(e.Severity.String), // Mapping severity to event_type for CLI
			Message:   e.Message,
			CreatedAt: e.CreatedAt.Time.String(),
		})
	}
	if res == nil {
		res = []SystemEvent{}
	}
	return res, nil
}

func (c *dbClient) ListVaultSecrets(ctx context.Context, userID uuid.UUID) ([]VaultSecret, error) {
	// Our direct DB access doesn't easily list vault secrets because we encrypt them,
	// and there is no ListSecrets in vault.sql.go based on the grep.
	// But CLI should probably not dump secrets directly without logic decrypting it,
	// or we can just say it's not supported in DB mode yet, since we are using vault api key internally.
	return nil, fmt.Errorf("ListVaultSecrets is not supported directly via DB client right now. Please use the HTTP API Client")
}

func (c *dbClient) Close() error {
	c.db.Close()
	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func formatUUID(u pgtype.UUID) string {
	b := u.Bytes
	// 8-4-4-4-12
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func decodeFactContent(raw []byte) string {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if v, ok := obj["text"].(string); ok {
			return v
		}
	}

	return string(raw)
}
