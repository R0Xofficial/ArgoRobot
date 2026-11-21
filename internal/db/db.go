// Package db is the PostgreSQL access: a connection pool and the schema.
//
// The schema is the one of the Python version, table for table, so the Go bot can run on the same
// database. Only the tables the ported modules use are created here; the others are left to the Python
// version (or to modules ported later) and are never touched, except by the chat cleanup, which deletes
// a chat's rows from every table that exists.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ Pool *pgxpool.Pool }

// Connect opens the pool and checks it with a ping.
func Connect(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("bad DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cannot reach the database: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// Exec runs a statement and returns how many rows it touched.
func (d *DB) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := d.Pool.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

// ErrNoRows is returned by QueryRow helpers when nothing matched.
var ErrNoRows = pgx.ErrNoRows

// Row runs a query and scans the first row into dest; ErrNoRows when there is none.
func (d *DB) Row(ctx context.Context, sql string, args []any, dest ...any) error {
	return d.Pool.QueryRow(ctx, sql, args...).Scan(dest...)
}

// Found is like Row but reports a missing row as found=false instead of an error.
func (d *DB) Found(ctx context.Context, sql string, args []any, dest ...any) (bool, error) {
	err := d.Row(ctx, sql, args, dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Migrate creates the tables (idempotent, like the Python version's start-up).
func (d *DB) Migrate(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, Schema)
	return err
}

// Schema holds the tables used by the ported modules, copied from core/database.py.
const Schema = `
CREATE TABLE IF NOT EXISTS roles (
    user_id  BIGINT PRIMARY KEY,
    role     SMALLINT NOT NULL,
    added_by BIGINT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE roles ADD COLUMN IF NOT EXISTS name TEXT;
ALTER TABLE roles ADD COLUMN IF NOT EXISTS added_by_name TEXT;

CREATE TABLE IF NOT EXISTS warns (
    id             BIGSERIAL PRIMARY KEY,
    chat_id        BIGINT NOT NULL,
    user_id        BIGINT NOT NULL,
    user_name      TEXT,
    reason         TEXT,
    warned_by      BIGINT,
    warned_by_name TEXT,
    warned_by_staff BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE warns ADD COLUMN IF NOT EXISTS warned_by_staff BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS warns_user_idx ON warns (chat_id, user_id);

CREATE TABLE IF NOT EXISTS warn_settings (
    chat_id    BIGINT PRIMARY KEY,
    warn_limit SMALLINT NOT NULL
);
ALTER TABLE warn_settings ADD COLUMN IF NOT EXISTS strong BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE warn_settings ADD COLUMN IF NOT EXISTS limit_mode TEXT NOT NULL DEFAULT 'ban';
ALTER TABLE warn_settings ADD COLUMN IF NOT EXISTS limit_seconds INTEGER NOT NULL DEFAULT 0;
UPDATE warn_settings SET limit_mode = 'kick' WHERE strong = FALSE AND limit_mode = 'ban';

CREATE TABLE IF NOT EXISTS chat_logs (
    chat_id     BIGINT PRIMARY KEY,
    log_chat_id BIGINT NOT NULL,
    set_by      BIGINT
);

CREATE TABLE IF NOT EXISTS chat_settings (
    chat_id    BIGINT PRIMARY KEY,
    staff_help BOOLEAN NOT NULL DEFAULT TRUE
);
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS gban_enforce BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS language TEXT NOT NULL DEFAULT 'en';
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS admin_errors BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chat_settings ALTER COLUMN admin_errors SET DEFAULT TRUE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS anon_admin BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS clean_commands BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS reports BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS users (
    user_id    BIGINT PRIMARY KEY,
    name       TEXT,
    username   TEXT,
    is_bot     BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS users_username_idx ON users (lower(username));
ALTER TABLE users ADD COLUMN IF NOT EXISTS first_name TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_name TEXT;

CREATE TABLE IF NOT EXISTS chats (
    chat_id    BIGINT PRIMARY KEY,
    type       TEXT NOT NULL,
    title      TEXT,
    username   TEXT,
    bot_admin  BOOLEAN,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_chats (
    user_id    BIGINT NOT NULL,
    chat_id    BIGINT NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS user_chats_chat_idx ON user_chats (chat_id);
`
