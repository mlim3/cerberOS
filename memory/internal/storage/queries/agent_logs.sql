-- name: CreateTaskExecution :exec
INSERT INTO agent_logs_schema.task_executions (
    id, task_id, agent_id, action_type, payload, status, error_context, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, NOW()
);

-- name: GetExecutionsByTaskID :many
SELECT * FROM agent_logs_schema.task_executions
WHERE task_id = $1
ORDER BY created_at ASC;
