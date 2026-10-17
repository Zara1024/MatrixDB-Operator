package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Client wraps a database/sql.DB connection pool to a specific MySQL instance.
type Client struct {
	db   *sql.DB
	Host string
	Port int
}

// NewClient initializes a connection pool to a MySQL node with appropriate timeouts.
func NewClient(host string, port int, user, password string) (*Client, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?timeout=5s&readTimeout=10s&writeTimeout=10s&interpolateParams=true",
		user, password, host, port)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open mysql connection to %s:%d: %w", host, port, err)
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(2 * time.Minute)

	return &Client{
		db:   db,
		Host: host,
		Port: port,
	}, nil
}

// Ping checks if the MySQL instance is reachable and accepting queries.
func (c *Client) Ping(ctx context.Context) error {
	if c.db == nil {
		return fmt.Errorf("client database is nil")
	}
	return c.db.PingContext(ctx)
}

// ExecContext executes a query without returning any rows.
func (c *Client) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.db == nil {
		return nil, fmt.Errorf("client database is nil")
	}
	return c.db.ExecContext(ctx, query, args...)
}

// QueryRowContext executes a query that is expected to return at most one row.
func (c *Client) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

// QueryContext executes a query that returns rows.
func (c *Client) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.db == nil {
		return nil, fmt.Errorf("client database is nil")
	}
	return c.db.QueryContext(ctx, query, args...)
}

// Close closes the underlying connection pool.
func (c *Client) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}
