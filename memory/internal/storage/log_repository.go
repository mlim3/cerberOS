package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
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

// SearchSystemEventsParams holds parameters for a full-text search across system event messages.
type SearchSystemEventsParams struct {
	Query       string      // plain-text search query; passed through plainto_tsquery
	ServiceName pgtype.Text // optional service name filter
	Limit       int32       // maximum results; 0 defaults to 20
}

// SearchSystemEvents performs a PostgreSQL full-text search on the message column of
// service_log_schema.system_events using plainto_tsquery (safe for arbitrary user input).
// Results are ordered by relevance (ts_rank) then recency.
func (r *LogRepository) SearchSystemEvents(ctx context.Context, arg SearchSystemEventsParams) ([]ServiceLogSchemaSystemEvent, error) {
	if arg.Query == "" {
		return []ServiceLogSchemaSystemEvent{}, nil
	}
	if arg.Limit <= 0 {
		arg.Limit = 20
	}
	if arg.Limit > 100 {
		arg.Limit = 100
	}

	const q = `
SELECT id, trace_id, service_name, severity, message, metadata, created_at
FROM service_log_schema.system_events
WHERE
    to_tsvector('english', message) @@ plainto_tsquery('english', $1)
    AND ($3::text IS NULL OR service_name = $3)
ORDER BY
    ts_rank(to_tsvector('english', message), plainto_tsquery('english', $1)) DESC,
    created_at DESC
LIMIT $2`

	rows, err := r.pool.Query(ctx, q, arg.Query, arg.Limit, arg.ServiceName)
	if err != nil {
		return nil, fmt.Errorf("failed to search system events: %w", err)
	}
	defer rows.Close()

	var items []ServiceLogSchemaSystemEvent
	for rows.Next() {
		var i ServiceLogSchemaSystemEvent
		if err := rows.Scan(&i.ID, &i.TraceID, &i.ServiceName, &i.Severity, &i.Message, &i.Metadata, &i.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan system event: %w", err)
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read system events: %w", err)
	}
	if items == nil {
		return []ServiceLogSchemaSystemEvent{}, nil
	}
	return items, nil
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
