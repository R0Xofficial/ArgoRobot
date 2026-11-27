// Package db is the PostgreSQL access: a connection pool and the schema.
//
// The schema is created when it is missing, so the bot runs on a new database and on an existing one. Only the
// tables the modules use are created here; any other table is never touched, except by the chat cleanup, which
// deletes a chat's rows from every table that exists.
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

// Migrate creates the tables (idempotent).
func (d *DB) Migrate(ctx context.Context) error {
	_, err := d.Pool.Exec(ctx, Schema)
	return err
}

// Schema holds the tables the modules use.
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
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS anti_channel_pin BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS clean_linked BOOLEAN NOT NULL DEFAULT FALSE;

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

CREATE TABLE IF NOT EXISTS blacklist (
    id             BIGSERIAL,
    chat_id        BIGINT NOT NULL,
    pattern        TEXT NOT NULL,
    action         TEXT,
    action_seconds INTEGER NOT NULL DEFAULT 0,
    reason         TEXT,
    added_by       BIGINT,
    PRIMARY KEY (chat_id, pattern)
);
CREATE TABLE IF NOT EXISTS blacklist_settings (
    chat_id        BIGINT PRIMARY KEY,
    mode           TEXT NOT NULL DEFAULT 'del',
    mode_seconds   INTEGER NOT NULL DEFAULT 0,
    delete_matches BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE TABLE IF NOT EXISTS approvals (
    chat_id          BIGINT NOT NULL,
    user_id          BIGINT NOT NULL,
    name             TEXT,
    approved_by      BIGINT,
    approved_by_name TEXT,
    approved_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id)
);
CREATE TABLE IF NOT EXISTS rules (
    chat_id       BIGINT PRIMARY KEY,
    content       TEXT NOT NULL DEFAULT '',
    media_type    TEXT,
    media_file_id TEXT
);
CREATE TABLE IF NOT EXISTS notes (
    chat_id       BIGINT NOT NULL,
    name          TEXT NOT NULL,
    content       TEXT NOT NULL DEFAULT '',
    media_type    TEXT,
    media_file_id TEXT,
    created_by    BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, name)
);

CREATE TABLE IF NOT EXISTS whitelist (
    user_id       BIGINT PRIMARY KEY,
    added_by      BIGINT,
    added_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    name          TEXT,
    added_by_name TEXT
);
CREATE TABLE IF NOT EXISTS disabled_commands (
    chat_id BIGINT NOT NULL,
    command TEXT   NOT NULL,
    PRIMARY KEY (chat_id, command)
);
CREATE TABLE IF NOT EXISTS connections (
    user_id      BIGINT PRIMARY KEY,
    chat_id      BIGINT NOT NULL,
    title        TEXT,
    chat_type    TEXT NOT NULL,
    connected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS modules (
    name    TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL
);

CREATE TABLE IF NOT EXISTS flood_settings (
    chat_id       BIGINT PRIMARY KEY,
    flood_limit   INTEGER NOT NULL DEFAULT 0,    -- more than this many messages in a row
    timer_count   INTEGER NOT NULL DEFAULT 0,    -- this many messages ...
    timer_seconds INTEGER NOT NULL DEFAULT 0,    -- ... within this many seconds
    mode          TEXT NOT NULL DEFAULT 'mute',  -- ban / kick / mute / tban / tmute
    mode_seconds  INTEGER NOT NULL DEFAULT 0,    -- length of a tban / tmute
    clear_flood   BOOLEAN NOT NULL DEFAULT FALSE,
    antichannel   BOOLEAN NOT NULL DEFAULT FALSE
);
ALTER TABLE flood_settings ALTER COLUMN flood_limit SET DEFAULT 0;
CREATE TABLE IF NOT EXISTS locks (
    chat_id BIGINT NOT NULL,
    item    TEXT NOT NULL,
    PRIMARY KEY (chat_id, item)
);
CREATE TABLE IF NOT EXISTS lock_settings (
    chat_id BIGINT PRIMARY KEY,
    warn    BOOLEAN NOT NULL DEFAULT FALSE
);
ALTER TABLE lock_settings ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'del';
ALTER TABLE lock_settings ADD COLUMN IF NOT EXISTS mode_seconds INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS lock_allowlist (
    chat_id BIGINT NOT NULL,
    entry   TEXT NOT NULL,
    PRIMARY KEY (chat_id, entry)
);
CREATE TABLE IF NOT EXISTS antiraid (
    chat_id          BIGINT PRIMARY KEY,
    enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    joins            INTEGER NOT NULL DEFAULT 10,       -- this many joins...
    window_seconds   INTEGER NOT NULL DEFAULT 60,       -- ...within this many seconds make a raid
    duration_seconds INTEGER NOT NULL DEFAULT 3600,     -- how long a raid lasts
    mode             TEXT NOT NULL DEFAULT 'mute',      -- what happens to newcomers: mute, kick or ban
    raid_until       TIMESTAMPTZ                        -- when the running raid ends
);
CREATE TABLE IF NOT EXISTS nightmode (
    chat_id      BIGINT PRIMARY KEY,
    enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    start_minute INTEGER NOT NULL DEFAULT 1380,   -- the night starts at this minute of the day (23:00)
    end_minute   INTEGER NOT NULL DEFAULT 420,    -- and ends at this one (07:00)
    zone         TEXT NOT NULL DEFAULT 'UTC',     -- the time zone the hours are in
    active       BOOLEAN NOT NULL DEFAULT FALSE,  -- the bot has closed the chat and not opened it yet
    saved        TEXT                             -- the chat's permissions from before the night (JSON)
);
CREATE TABLE IF NOT EXISTS greetings (
    chat_id               BIGINT PRIMARY KEY,
    welcome_on            BOOLEAN NOT NULL DEFAULT TRUE,
    welcome_content       TEXT,
    welcome_media_type    TEXT,
    welcome_media_file_id TEXT,
    goodbye_on            BOOLEAN NOT NULL DEFAULT TRUE,
    goodbye_content       TEXT,
    goodbye_media_type    TEXT,
    goodbye_media_file_id TEXT,
    welcome_mute          BOOLEAN NOT NULL DEFAULT FALSE,
    clean_welcome         BOOLEAN NOT NULL DEFAULT FALSE,
    clean_service         BOOLEAN NOT NULL DEFAULT FALSE,
    last_welcome_id       BIGINT
);
ALTER TABLE greetings ALTER COLUMN goodbye_on SET DEFAULT TRUE;
ALTER TABLE greetings ADD COLUMN IF NOT EXISTS clean_service_types TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS captcha_pending (
    id              BIGSERIAL PRIMARY KEY,
    chat_id         BIGINT NOT NULL,
    user_id         BIGINT NOT NULL,
    kind            TEXT   NOT NULL DEFAULT 'join',
    mode            TEXT   NOT NULL,
    phase           TEXT   NOT NULL DEFAULT 'captcha',
    question        TEXT,
    answer          TEXT,
    layout          TEXT,
    taken           TEXT   NOT NULL DEFAULT '',
    name            TEXT,
    title           TEXT,
    message_chat_id BIGINT,
    message_id      BIGINT,
    is_photo        BOOLEAN NOT NULL DEFAULT FALSE,
    join_message_id BIGINT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    unmute_at       TIMESTAMPTZ,
    kick_at         TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    UNIQUE (chat_id, user_id)
);
ALTER TABLE captcha_pending ADD COLUMN IF NOT EXISTS attached BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE captcha_pending ADD COLUMN IF NOT EXISTS welcomed BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS captcha_settings (
    chat_id      BIGINT PRIMARY KEY,
    enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    mode         TEXT    NOT NULL DEFAULT 'button',
    rules        BOOLEAN NOT NULL DEFAULT FALSE,
    mute_seconds INTEGER,
    kick         BOOLEAN NOT NULL DEFAULT FALSE,
    kick_seconds INTEGER NOT NULL DEFAULT 300,
    button_text  TEXT
);
CREATE TABLE IF NOT EXISTS gbans (
    user_id          BIGINT PRIMARY KEY,
    name             TEXT,
    reason           TEXT NOT NULL,
    admin_id         BIGINT,
    admin_name       TEXT,
    approved_by      BIGINT,
    approved_by_name TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS gban_requests (
    id                BIGSERIAL PRIMARY KEY,
    target_id         BIGINT NOT NULL UNIQUE,
    target_name       TEXT,
    kind              TEXT NOT NULL,
    reason            TEXT NOT NULL,
    requested_by      BIGINT NOT NULL,
    requested_by_name TEXT,
    chat_id           BIGINT NOT NULL,
    msg_id            BIGINT,
    thread_id         BIGINT,
    target_msg_id     BIGINT,
    origin_html       TEXT NOT NULL,
    deliveries        TEXT NOT NULL DEFAULT '[]',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE gban_requests ADD COLUMN IF NOT EXISTS deliveries TEXT NOT NULL DEFAULT '[]';
CREATE TABLE IF NOT EXISTS chat_prebans (
    chat_id    BIGINT NOT NULL,
    user_id    BIGINT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    until      TIMESTAMPTZ,
    by_id      BIGINT,
    by_name    TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id)
);
ALTER TABLE chat_prebans ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'ban';
CREATE TABLE IF NOT EXISTS gban_request_optout (
    user_id BIGINT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS gban_bans (
    user_id   BIGINT NOT NULL,
    chat_id   BIGINT NOT NULL,
    banned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS gban_bans_chat_idx ON gban_bans (chat_id);

CREATE TABLE IF NOT EXISTS federations (
    fed_id         TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    owner_id       BIGINT NOT NULL UNIQUE,
    owner_name     TEXT,
    notify         BOOLEAN NOT NULL DEFAULT TRUE,
    require_reason BOOLEAN NOT NULL DEFAULT FALSE,
    log_chat_id    BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS fed_admins (
    fed_id  TEXT NOT NULL,
    user_id BIGINT NOT NULL,
    name    TEXT,
    PRIMARY KEY (fed_id, user_id)
);
CREATE TABLE IF NOT EXISTS fed_chats (
    chat_id BIGINT PRIMARY KEY,
    fed_id  TEXT NOT NULL,
    quiet   BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS fed_chats_fed ON fed_chats (fed_id);
CREATE TABLE IF NOT EXISTS fed_bans (
    fed_id         TEXT NOT NULL,
    user_id        BIGINT NOT NULL,
    name           TEXT,
    reason         TEXT,
    banned_by      BIGINT,
    banned_by_name TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (fed_id, user_id)
);
CREATE INDEX IF NOT EXISTS fed_bans_user ON fed_bans (user_id);
CREATE TABLE IF NOT EXISTS fed_subs (
    fed_id TEXT NOT NULL,
    sub_id TEXT NOT NULL,
    PRIMARY KEY (fed_id, sub_id)
);
CREATE TABLE IF NOT EXISTS fed_applied (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (chat_id, user_id)
);
CREATE TABLE IF NOT EXISTS join_blacklist (
    chat_id BIGINT NOT NULL,
    kind    TEXT NOT NULL,
    pattern TEXT NOT NULL,
    PRIMARY KEY (chat_id, kind, pattern)
);
CREATE TABLE IF NOT EXISTS filters (
    id            BIGSERIAL,
    chat_id       BIGINT NOT NULL,
    pattern       TEXT NOT NULL,
    content       TEXT NOT NULL DEFAULT '',
    media_type    TEXT,
    media_file_id TEXT,
    created_by    BIGINT,
    PRIMARY KEY (chat_id, pattern)
);
CREATE TABLE IF NOT EXISTS afk (
    user_id       BIGINT PRIMARY KEY,
    name          TEXT,
    username      TEXT,
    reason        TEXT NOT NULL DEFAULT '',
    since         TIMESTAMPTZ NOT NULL DEFAULT now(),
    media_type    TEXT,
    media_file_id TEXT
);
CREATE TABLE IF NOT EXISTS report_optout (
    user_id BIGINT PRIMARY KEY
);
`
