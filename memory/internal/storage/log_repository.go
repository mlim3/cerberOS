package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LogRepository provides access to system log events storage.
type LogRepository struct {
	pool    *pgxpool.Pool
	queries *Queries
}

// NewLogRepository creates a new LogRepository instance.
func NewLogRepository(pool *pgxpool.Pool) *LogRepository {
	return &LogRepository{
		pool:    pool,
		queries: New(pool),
	}
}

// CreateSystemEvent inserts a new system event into the database.
func (r *LogRepository) CreateSystemEvent(ctx context.Context, arg CreateSystemEventParams) (ServiceLogSchemaSystemEvent, error) {
	event, err := r.queries.CreateSystemEvent(ctx, arg)
	if err != nil {
		return ServiceLogSchemaSystemEvent{}, fmt.Errorf("failed to create system event: %w", err)
	}

	return event, nil
}

// ListSystemEvents returns a list of system events matching the given filters.
func (r *LogRepository) ListSystemEvents(ctx context.Context, arg ListSystemEventsParams) ([]ServiceLogSchemaSystemEvent, error) {
	if arg.Limit <= 0 {
		arg.Limit = 100 // Default limit if none or negative provided
	}

	events, err := r.queries.ListSystemEvents(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("failed to list system events: %w", err)
	}

	// sqlc returns nil for empty slices, return empty slice instead of nil
	if events == nil {
		return []ServiceLogSchemaSystemEvent{}, nil
	}

	return events, nil
}
