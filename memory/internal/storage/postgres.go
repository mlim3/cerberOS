package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresDB represents a connection to the PostgreSQL database.
type PostgresDB struct {
	pool *pgxpool.Pool
}

// Config holds the configuration for connecting to the PostgreSQL database.
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
}

// NewPostgresDB creates a new PostgresDB instance and establishes a connection pool.
// It uses dependency injection rather than global variables.
func NewPostgresDB(ctx context.Context, cfg Config) (*PostgresDB, error) {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// You can customize poolConfig further here (e.g., MaxConns, MinConns)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Verify the connection
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database during initialization: %w", err)
	}

	return &PostgresDB{
		pool: pool,
	}, nil
}

// Close closes the database connection pool.
func (db *PostgresDB) Close() {
	if db.pool != nil {
		db.pool.Close()
	}
}

// Ping checks if the database is reachable.
// This is used for the health check endpoint.
func (db *PostgresDB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	return db.pool.Ping(ctx)
}

// GetPool returns the underlying pgxpool.Pool.
func (db *PostgresDB) GetPool() *pgxpool.Pool {
	return db.pool
}
