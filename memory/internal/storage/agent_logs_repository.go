package storage

import (
	"context"

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
