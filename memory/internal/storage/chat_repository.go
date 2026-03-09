package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChatRepository provides access to chat messages storage.
type ChatRepository struct {
	pool    *pgxpool.Pool
	queries *Queries
}

var (
	ErrIdempotencyConflict = errors.New("idempotency key replayed with different payload")
	ErrSessionOwnership    = errors.New("session does not belong to user")
)

// NewChatRepository creates a new ChatRepository instance.
func NewChatRepository(pool *pgxpool.Pool) *ChatRepository {
	return &ChatRepository{
		pool:    pool,
		queries: New(pool),
	}
}

// CreateMessage inserts a new chat message into the database.
// It implements idempotency logic by checking for an existing message with the same idempotency key.
func (r *ChatRepository) CreateMessage(ctx context.Context, arg CreateChatMessageParams) (ChatSchemaMessage, error) {
	// First check if a message with the same session_id and idempotency_key exists.
	if arg.IdempotencyKey.Valid {
		existingMsg, err := r.queries.GetChatMessageByIdempotencyKey(ctx, GetChatMessageByIdempotencyKeyParams{
			SessionID:      arg.SessionID,
			IdempotencyKey: arg.IdempotencyKey,
		})

		// If a message was found, return it immediately without error to implement idempotency.
		if err == nil {
			if !sameMessagePayload(existingMsg, arg) {
				return ChatSchemaMessage{}, ErrIdempotencyConflict
			}
			return existingMsg, nil
		}

		// If the error is not pgx.ErrNoRows, it's a real error, return it.
		if !errors.Is(err, pgx.ErrNoRows) {
			return ChatSchemaMessage{}, fmt.Errorf("failed to check idempotency key: %w", err)
		}
	}

	// Message doesn't exist, create a new one.
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

func (r *ChatRepository) ValidateSessionOwnership(ctx context.Context, sessionID, userID pgtype.UUID) error {
	const q = `
SELECT EXISTS(
    SELECT 1
    FROM chat_schema.messages
    WHERE session_id = $1 AND user_id <> $2
)`
	var ownedByOther bool
	if err := r.pool.QueryRow(ctx, q, sessionID, userID).Scan(&ownedByOther); err != nil {
		return err
	}
	if ownedByOther {
		return ErrSessionOwnership
	}
	return nil
}

// ListMessagesBySession returns a list of messages for a given session.
func (r *ChatRepository) ListMessagesBySession(ctx context.Context, sessionID pgtype.UUID, limit int32) ([]ChatSchemaMessage, error) {
	if limit <= 0 {
		limit = 100 // Default limit if none or negative provided
	}

	messages, err := r.queries.ListChatMessagesBySession(ctx, ListChatMessagesBySessionParams{
		SessionID: sessionID,
		Limit:     limit,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list chat messages: %w", err)
	}

	// sqlc returns nil for empty slices, return empty slice instead of nil
	if messages == nil {
		return []ChatSchemaMessage{}, nil
	}

	return messages, nil
}
