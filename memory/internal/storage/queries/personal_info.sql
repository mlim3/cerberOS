-- name: InsertChunk :one
INSERT INTO personal_info_schema.personal_info_chunks (
    id, user_id, raw_text, embedding, model_version
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: QueryChunks :many
SELECT *
FROM personal_info_schema.personal_info_chunks
WHERE user_id = $1
ORDER BY embedding <=> $2
LIMIT $3;

-- name: UpsertFact :one
INSERT INTO personal_info_schema.user_facts (
    id, user_id, category, fact_key, fact_value, confidence, version
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (id) DO UPDATE
SET
    category = EXCLUDED.category,
    fact_key = EXCLUDED.fact_key,
    fact_value = EXCLUDED.fact_value,
    confidence = EXCLUDED.confidence,
    version = user_facts.version + 1,
    updated_at = NOW()
WHERE user_facts.version = EXCLUDED.version
RETURNING *;

-- name: CreateSourceReference :one
INSERT INTO personal_info_schema.source_references (
    id, user_id, target_id, target_type, source_id, source_type
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetSourceReferencesByTarget :many
SELECT *
FROM personal_info_schema.source_references
WHERE user_id = $1 AND target_id = $2;

-- name: GetAllFacts :many
SELECT *
FROM personal_info_schema.user_facts
WHERE user_id = $1;

-- name: GetAllChunks :many
SELECT id, user_id, raw_text, model_version, created_at
FROM personal_info_schema.personal_info_chunks
WHERE user_id = $1;

-- name: GetFactByID :one
SELECT *
FROM personal_info_schema.user_facts
WHERE user_id = $1 AND id = $2;

-- name: UpdateFactWithVersion :one
UPDATE personal_info_schema.user_facts
SET
    category = $3,
    fact_key = $4,
    fact_value = $5,
    confidence = $6,
    version = version + 1,
    updated_at = NOW()
WHERE user_id = $1 AND id = $2 AND version = $7
RETURNING *;

-- name: DeleteFact :execrows
DELETE FROM personal_info_schema.user_facts
WHERE user_id = $1 AND id = $2;
