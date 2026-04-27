package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChatRepository provides access to conversation and message storage.
type ChatRepository struct {
	pool    *pgxpool.Pool
	queries *Queries
}

type ConversationRecord struct {
	ID                 pgtype.UUID        `json:"id"`
	UserID             pgtype.UUID        `json:"user_id"`
	Title              string             `json:"title"`
	CreatedAt          pgtype.Timestamptz `json:"created_at"`
	UpdatedAt          pgtype.Timestamptz `json:"updated_at"`
	LastMessagePreview string             `json:"last_message_preview"`
	MessageCount       int32              `json:"message_count"`
	LatestTaskID       pgtype.UUID        `json:"latest_task_id"`
	LatestTaskStatus   pgtype.Text        `json:"latest_task_status"`
}

type TaskRecord struct {
	ID                  pgtype.UUID        `json:"id"`
	ConversationID      pgtype.UUID        `json:"conversation_id"`
	UserID              pgtype.UUID        `json:"user_id"`
	OrchestratorTaskRef pgtype.Text        `json:"orchestrator_task_ref"`
	TraceID             pgtype.Text        `json:"trace_id"`
	Status              string             `json:"status"`
	InputSummary        pgtype.Text        `json:"input_summary"`
	CreatedAt           pgtype.Timestamptz `json:"created_at"`
	UpdatedAt           pgtype.Timestamptz `json:"updated_at"`
	CompletedAt         pgtype.Timestamptz `json:"completed_at"`
}

var (
	ErrIdempotencyConflict   = errors.New("idempotency key replayed with different payload")
	ErrConversationOwnership = errors.New("conversation does not belong to user")
	ErrConversationNotFound  = errors.New("conversation not found")
)

// NewChatRepository creates a new ChatRepository instance.
func NewChatRepository(pool *pgxpool.Pool) *ChatRepository {
	return &ChatRepository{
		pool:    pool,
		queries: New(pool),
	}
}

// EnsureSchema applies the chat schema expectations needed by the current code,
// including upgrading older databases that still use session_id.
func (r *ChatRepository) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'chat_schema'
          AND table_name = 'messages'
          AND column_name = 'session_id'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'chat_schema'
          AND table_name = 'messages'
          AND column_name = 'conversation_id'
    ) THEN
        ALTER TABLE chat_schema.messages RENAME COLUMN session_id TO conversation_id;
    END IF;
END $$;`,
		`ALTER INDEX IF EXISTS chat_schema.idx_messages_session_id RENAME TO idx_messages_conversation_id;`,
		`ALTER INDEX IF EXISTS chat_schema.idx_messages_session_idempotency RENAME TO idx_messages_conversation_idempotency;`,
		`CREATE TABLE IF NOT EXISTS chat_schema.conversations (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT 'New Conversation',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`,
		`CREATE INDEX IF NOT EXISTS idx_conversations_user_id ON chat_schema.conversations(user_id);`,
		`CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON chat_schema.conversations(updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS chat_schema.tasks (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES chat_schema.conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    orchestrator_task_ref TEXT,
    trace_id VARCHAR(64),
    status VARCHAR(50) NOT NULL DEFAULT 'awaiting_feedback',
    input_summary TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);`,
		`CREATE INDEX IF NOT EXISTS idx_chat_tasks_conversation_id ON chat_schema.tasks(conversation_id, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_chat_tasks_user_id ON chat_schema.tasks(user_id, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_chat_tasks_orchestrator_task_ref ON chat_schema.tasks(orchestrator_task_ref);`,
		`INSERT INTO chat_schema.conversations (id, user_id, title, created_at, updated_at)
SELECT
    m.conversation_id,
    (ARRAY_AGG(m.user_id ORDER BY m.created_at ASC))[1],
    LEFT(COALESCE((ARRAY_AGG(CASE WHEN m.role = 'user' THEN m.content END ORDER BY m.created_at ASC))[1], 'New Conversation'), 255),
    MIN(m.created_at),
    MAX(m.created_at)
FROM chat_schema.messages m
LEFT JOIN chat_schema.conversations c ON c.id = m.conversation_id
WHERE c.id IS NULL
GROUP BY m.conversation_id;`,
	}

	for _, stmt := range statements {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure chat schema: %w", err)
		}
	}
	return nil
}

// CreateMessage inserts a new chat message into the database.
// It implements idempotency logic by checking for an existing message with the same idempotency key.
func (r *ChatRepository) CreateMessage(ctx context.Context, arg CreateChatMessageParams) (ChatSchemaMessage, error) {
	if arg.IdempotencyKey.Valid {
		existingMsg, err := r.queries.GetChatMessageByIdempotencyKey(ctx, GetChatMessageByIdempotencyKeyParams{
			ConversationID: arg.ConversationID,
			IdempotencyKey: arg.IdempotencyKey,
		})
		if err == nil {
			if !sameMessagePayload(existingMsg, arg) {
				return ChatSchemaMessage{}, ErrIdempotencyConflict
			}
			return existingMsg, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ChatSchemaMessage{}, fmt.Errorf("failed to check idempotency key: %w", err)
		}
	}

	msg, err := r.queries.CreateChatMessage(ctx, arg)
	if err != nil {
		return ChatSchemaMessage{}, fmt.Errorf("failed to create chat message: %w", err)
	}
	return msg, nil
}

func sameMessagePayload(existing ChatSchemaMessage, incoming CreateChatMessageParams) bool {
	if existing.UserID != incoming.UserID {
		return false
	}
	if existing.Role != incoming.Role || existing.Content != incoming.Content {
		return false
	}
	if existing.TokenCount.Valid != incoming.TokenCount.Valid {
		return false
	}
	if existing.TokenCount.Valid && existing.TokenCount.Int32 != incoming.TokenCount.Int32 {
		return false
	}
	return true
}

func (r *ChatRepository) UserExists(ctx context.Context, userID pgtype.UUID) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM identity_schema.users WHERE id = $1)`
	var exists bool
	err := r.pool.QueryRow(ctx, q, userID).Scan(&exists)
	return exists, err
}

func (r *ChatRepository) ValidateConversationOwnership(ctx context.Context, conversationID, userID pgtype.UUID) error {
	const q = `
SELECT user_id
FROM chat_schema.conversations
WHERE id = $1`
	var owner pgtype.UUID
	if err := r.pool.QueryRow(ctx, q, conversationID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConversationNotFound
		}
		return err
	}
	if owner != userID {
		return ErrConversationOwnership
	}
	return nil
}

func (r *ChatRepository) CreateConversation(ctx context.Context, conversationID, userID pgtype.UUID, title string, now time.Time) (ConversationRecord, bool, error) {
	const existingQ = `
SELECT id, user_id, title, created_at, updated_at
FROM chat_schema.conversations
WHERE id = $1`
	var existing ConversationRecord
	if err := r.pool.QueryRow(ctx, existingQ, conversationID).Scan(
		&existing.ID,
		&existing.UserID,
		&existing.Title,
		&existing.CreatedAt,
		&existing.UpdatedAt,
	); err == nil {
		if existing.UserID != userID {
			return ConversationRecord{}, false, ErrConversationOwnership
		}
		if updated, ok, err := r.maybeUpdateConversationTitle(ctx, existing, title, now); err != nil {
			return ConversationRecord{}, false, err
		} else if ok {
			return updated, false, nil
		}
		return existing, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ConversationRecord{}, false, fmt.Errorf("fetch conversation: %w", err)
	}

	title = defaultConversationTitle(title)
	const insertQ = `
INSERT INTO chat_schema.conversations (id, user_id, title, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, title, created_at, updated_at`
	var rec ConversationRecord
	if err := r.pool.QueryRow(ctx, insertQ, conversationID, userID, title, now, now).Scan(
		&rec.ID,
		&rec.UserID,
		&rec.Title,
		&rec.CreatedAt,
		&rec.UpdatedAt,
	); err != nil {
		return ConversationRecord{}, false, fmt.Errorf("create conversation: %w", err)
	}
	return rec, true, nil
}

func (r *ChatRepository) maybeUpdateConversationTitle(ctx context.Context, existing ConversationRecord, title string, now time.Time) (ConversationRecord, bool, error) {
	title = defaultConversationTitle(title)
	if !shouldReplaceConversationTitle(existing.Title, title) {
		return ConversationRecord{}, false, nil
	}

	const q = `
UPDATE chat_schema.conversations
SET title = $2, updated_at = $3
WHERE id = $1
RETURNING id, user_id, title, created_at, updated_at`

	var updated ConversationRecord
	if err := r.pool.QueryRow(ctx, q, existing.ID, title, now).Scan(
		&updated.ID,
		&updated.UserID,
		&updated.Title,
		&updated.CreatedAt,
		&updated.UpdatedAt,
	); err != nil {
		return ConversationRecord{}, false, fmt.Errorf("update conversation title: %w", err)
	}
	return updated, true, nil
}

func (r *ChatRepository) SetConversationTitle(ctx context.Context, conversationID, userID pgtype.UUID, title string, now time.Time) (ConversationRecord, bool, error) {
	title = defaultConversationTitle(title)
	if isPlaceholderConversationTitle(title) {
		return ConversationRecord{}, false, nil
	}

	const existingQ = `
SELECT id, user_id, title, created_at, updated_at
FROM chat_schema.conversations
WHERE id = $1`

	var existing ConversationRecord
	if err := r.pool.QueryRow(ctx, existingQ, conversationID).Scan(
		&existing.ID,
		&existing.UserID,
		&existing.Title,
		&existing.CreatedAt,
		&existing.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConversationRecord{}, false, ErrConversationNotFound
		}
		return ConversationRecord{}, false, fmt.Errorf("fetch conversation: %w", err)
	}
	if existing.UserID != userID {
		return ConversationRecord{}, false, ErrConversationOwnership
	}
	if strings.TrimSpace(existing.Title) == title {
		return existing, false, nil
	}

	const q = `
UPDATE chat_schema.conversations
SET title = $2, updated_at = $3
WHERE id = $1
RETURNING id, user_id, title, created_at, updated_at`

	var updated ConversationRecord
	if err := r.pool.QueryRow(ctx, q, conversationID, title, now).Scan(
		&updated.ID,
		&updated.UserID,
		&updated.Title,
		&updated.CreatedAt,
		&updated.UpdatedAt,
	); err != nil {
		return ConversationRecord{}, false, fmt.Errorf("set conversation title: %w", err)
	}
	return updated, true, nil
}

func (r *ChatRepository) CreateTask(
	ctx context.Context,
	taskID pgtype.UUID,
	conversationID pgtype.UUID,
	userID pgtype.UUID,
	orchestratorTaskRef pgtype.Text,
	traceID pgtype.Text,
	status string,
	inputSummary pgtype.Text,
	now time.Time,
) (TaskRecord, bool, error) {
	if err := r.ValidateConversationOwnership(ctx, conversationID, userID); err != nil {
		return TaskRecord{}, false, err
	}

	const existingQ = `
SELECT id, conversation_id, user_id, orchestrator_task_ref, trace_id, status, input_summary, created_at, updated_at, completed_at
FROM chat_schema.tasks
WHERE id = $1`
	var existing TaskRecord
	if err := r.pool.QueryRow(ctx, existingQ, taskID).Scan(
		&existing.ID,
		&existing.ConversationID,
		&existing.UserID,
		&existing.OrchestratorTaskRef,
		&existing.TraceID,
		&existing.Status,
		&existing.InputSummary,
		&existing.CreatedAt,
		&existing.UpdatedAt,
		&existing.CompletedAt,
	); err == nil {
		if existing.UserID != userID || existing.ConversationID != conversationID {
			return TaskRecord{}, false, ErrConversationOwnership
		}
		return existing, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return TaskRecord{}, false, fmt.Errorf("fetch task: %w", err)
	}

	if status == "" {
		status = "awaiting_feedback"
	}

	const insertQ = `
INSERT INTO chat_schema.tasks (
    id, conversation_id, user_id, orchestrator_task_ref, trace_id, status, input_summary, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
RETURNING id, conversation_id, user_id, orchestrator_task_ref, trace_id, status, input_summary, created_at, updated_at, completed_at`
	var rec TaskRecord
	if err := r.pool.QueryRow(
		ctx,
		insertQ,
		taskID,
		conversationID,
		userID,
		orchestratorTaskRef,
		traceID,
		status,
		inputSummary,
		now,
	).Scan(
		&rec.ID,
		&rec.ConversationID,
		&rec.UserID,
		&rec.OrchestratorTaskRef,
		&rec.TraceID,
		&rec.Status,
		&rec.InputSummary,
		&rec.CreatedAt,
		&rec.UpdatedAt,
		&rec.CompletedAt,
	); err != nil {
		return TaskRecord{}, false, fmt.Errorf("create task: %w", err)
	}
	return rec, true, nil
}

func (r *ChatRepository) GetTask(ctx context.Context, taskID, userID pgtype.UUID) (TaskRecord, error) {
	const q = `
SELECT id, conversation_id, user_id, orchestrator_task_ref, trace_id, status, input_summary, created_at, updated_at, completed_at
FROM chat_schema.tasks
WHERE id = $1 AND user_id = $2`
	var rec TaskRecord
	if err := r.pool.QueryRow(ctx, q, taskID, userID).Scan(
		&rec.ID,
		&rec.ConversationID,
		&rec.UserID,
		&rec.OrchestratorTaskRef,
		&rec.TraceID,
		&rec.Status,
		&rec.InputSummary,
		&rec.CreatedAt,
		&rec.UpdatedAt,
		&rec.CompletedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TaskRecord{}, ErrConversationNotFound
		}
		return TaskRecord{}, fmt.Errorf("get task: %w", err)
	}
	return rec, nil
}

func (r *ChatRepository) EnsureConversation(ctx context.Context, conversationID, userID pgtype.UUID, title string, now time.Time) error {
	_, _, err := r.CreateConversation(ctx, conversationID, userID, title, now)
	return err
}

func (r *ChatRepository) TouchConversation(ctx context.Context, conversationID pgtype.UUID, updatedAt time.Time) error {
	const q = `
UPDATE chat_schema.conversations
SET updated_at = $2
WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, conversationID, updatedAt); err != nil {
		return fmt.Errorf("touch conversation: %w", err)
	}
	return nil
}

// ListMessagesByConversation returns a list of messages for a given conversation.
func (r *ChatRepository) ListMessagesByConversation(ctx context.Context, conversationID pgtype.UUID, limit int32) ([]ChatSchemaMessage, error) {
	if limit <= 0 {
		limit = 100
	}

	messages, err := r.queries.ListChatMessagesByConversation(ctx, ListChatMessagesByConversationParams{
		ConversationID: conversationID,
		Limit:          limit,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list chat messages: %w", err)
	}
	if messages == nil {
		return []ChatSchemaMessage{}, nil
	}
	return messages, nil
}

func (r *ChatRepository) ListConversationsByUser(ctx context.Context, userID pgtype.UUID, limit int32) ([]ConversationRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
SELECT
    c.id,
    c.user_id,
    c.title,
    c.created_at,
    c.updated_at,
    COALESCE(last_msg.content, '') AS last_message_preview,
    COALESCE(msg_count.count, 0) AS message_count,
    latest_task.id AS latest_task_id,
    latest_task.status AS latest_task_status
FROM chat_schema.conversations c
LEFT JOIN LATERAL (
    SELECT content
    FROM chat_schema.messages m
    WHERE m.conversation_id = c.id
    ORDER BY m.created_at DESC
    LIMIT 1
) AS last_msg ON true
LEFT JOIN LATERAL (
    SELECT COUNT(*)::int AS count
    FROM chat_schema.messages m
    WHERE m.conversation_id = c.id
) AS msg_count ON true
LEFT JOIN LATERAL (
    SELECT t.id, t.status
    FROM chat_schema.tasks t
    WHERE t.conversation_id = c.id
    ORDER BY t.updated_at DESC, t.created_at DESC
    LIMIT 1
) AS latest_task ON true
WHERE c.user_id = $1
ORDER BY c.updated_at DESC
LIMIT $2`

	rows, err := r.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var out []ConversationRecord
	for rows.Next() {
		var rec ConversationRecord
		if err := rows.Scan(
			&rec.ID,
			&rec.UserID,
			&rec.Title,
			&rec.CreatedAt,
			&rec.UpdatedAt,
			&rec.LastMessagePreview,
			&rec.MessageCount,
			&rec.LatestTaskID,
			&rec.LatestTaskStatus,
		); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	if out == nil {
		out = []ConversationRecord{}
	}
	return out, nil
}

func defaultConversationTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "New Conversation"
	}
	if len(title) > 255 {
		return title[:255]
	}
	return title
}

func shouldReplaceConversationTitle(existingTitle, incomingTitle string) bool {
	existingTitle = strings.TrimSpace(existingTitle)
	incomingTitle = strings.TrimSpace(incomingTitle)

	if incomingTitle == "" || existingTitle == incomingTitle {
		return false
	}

	return isPlaceholderConversationTitle(existingTitle) && !isPlaceholderConversationTitle(incomingTitle)
}

func isPlaceholderConversationTitle(title string) bool {
	switch strings.TrimSpace(title) {
	case "", "New Conversation", "New Task":
		return true
	default:
		return false
	}
}
