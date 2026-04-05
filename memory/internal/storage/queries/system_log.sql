-- name: CreateSystemEvent :one
INSERT INTO service_log_schema.system_events (
    id, trace_id, service_name, severity, message, metadata, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: ListSystemEvents :many
SELECT * FROM service_log_schema.system_events
WHERE
    (sqlc.narg('service_name')::text IS NULL OR service_name = sqlc.narg('service_name')) AND
    (sqlc.narg('severity')::text IS NULL OR severity = sqlc.narg('severity'))
ORDER BY created_at DESC
LIMIT $1;
