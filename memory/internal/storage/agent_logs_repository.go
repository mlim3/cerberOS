package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AgentLogsRepository struct {
	*BaseRepository
}

func NewAgentLogsRepository(pool *pgxpool.Pool) *AgentLogsRepository {
	return &AgentLogsRepository{
		BaseRepository: &BaseRepository{Pool: pool},
	}
}

func (r *AgentLogsRepository) CreateTaskExecution(ctx context.Context, req CreateTaskExecutionParams) error {
	queries := New(r.Pool)
	return queries.CreateTaskExecution(ctx, req)
}

func (r *AgentLogsRepository) GetExecutionsByTaskID(ctx context.Context, taskID pgtype.UUID) ([]AgentLogsSchemaTaskExecution, error) {
	queries := New(r.Pool)
	return queries.GetExecutionsByTaskID(ctx, taskID)
}

func (r *AgentLogsRepository) GetExecutionsByTaskIDLimit(ctx context.Context, taskID pgtype.UUID, limit int32) ([]AgentLogsSchemaTaskExecution, error) {
	if limit <= 0 {
		limit = 100
	}

	const q = `
SELECT id, task_id, agent_id, action_type, payload, status, error_context, created_at
FROM agent_logs_schema.task_executions
WHERE task_id = $1
ORDER BY created_at ASC
LIMIT $2`

	rows, err := r.Pool.Query(ctx, q, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get task executions: %w", err)
	}
	defer rows.Close()

	executions := make([]AgentLogsSchemaTaskExecution, 0)
	for rows.Next() {
		var e AgentLogsSchemaTaskExecution
		if err := rows.Scan(
			&e.ID,
			&e.TaskID,
			&e.AgentID,
			&e.ActionType,
			&e.Payload,
			&e.Status,
			&e.ErrorContext,
			&e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan task execution: %w", err)
		}
		executions = append(executions, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read task executions: %w", err)
	}

	return executions, nil
}
