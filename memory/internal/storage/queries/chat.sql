-- name: CreateChatMessage :one
INSERT INTO chat_schema.messages (
    id, session_id, user_id, role, content, token_count, idempotency_key, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetChatMessageByIdempotencyKey :one
SELECT * FROM chat_schema.messages
WHERE session_id = $1 AND idempotency_key = $2;

-- name: ListChatMessagesBySession :many
SELECT * FROM chat_schema.messages
WHERE session_id = $1
ORDER BY created_at ASC
LIMIT $2;
