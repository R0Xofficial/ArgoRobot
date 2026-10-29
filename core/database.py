"""
Async PostgreSQL access (asyncpg connection pool).

Queries use PostgreSQL placeholders ($1, $2, ...). The helpers below keep the
interface small so modules never deal with the pool directly:

    await database.execute("DELETE FROM roles WHERE user_id = $1", user_id)
    row  = await database.fetch_one("SELECT * FROM roles WHERE user_id = $1", user_id)
    rows = await database.fetch_all("SELECT user_id, role FROM roles")

Note: Telegram user and chat ids do not fit in 32 bits, so every id column
must be BIGINT.
"""

import asyncio
import logging
from typing import Any

import asyncpg

from config import settings

log = logging.getLogger(__name__)

_pool: asyncpg.Pool | None = None

CONNECT_ATTEMPTS = 6
CONNECT_DELAY = 3  # seconds between attempts

SCHEMA = """
CREATE TABLE IF NOT EXISTS roles (
    user_id  BIGINT PRIMARY KEY,
    role     SMALLINT NOT NULL,
    added_by BIGINT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Display names are stored at promotion time so /staff still has something to
-- show for people the bot cannot look up any more. Added later, hence ALTER.
ALTER TABLE roles ADD COLUMN IF NOT EXISTS name TEXT;
ALTER TABLE roles ADD COLUMN IF NOT EXISTS added_by_name TEXT;

-- Users protected from global and federation bans (see core/whitelist.py).
CREATE TABLE IF NOT EXISTS whitelist (
    user_id       BIGINT PRIMARY KEY,
    added_by      BIGINT,
    added_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    name          TEXT,
    added_by_name TEXT
);

-- Warnings given in a chat. Each has its own id, so it can be removed by id.
CREATE TABLE IF NOT EXISTS warns (
    id             BIGSERIAL PRIMARY KEY,
    chat_id        BIGINT NOT NULL,
    user_id        BIGINT NOT NULL,
    user_name      TEXT,
    reason         TEXT,
    warned_by      BIGINT,
    warned_by_name TEXT,
    warned_by_staff BOOLEAN NOT NULL DEFAULT FALSE,  -- given by the bot's staff, not by a chat admin
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE warns ADD COLUMN IF NOT EXISTS warned_by_staff BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS warns_user_idx ON warns (chat_id, user_id);

-- Per-chat warn limit; chats without a row use the default (see modules/warns.py).
CREATE TABLE IF NOT EXISTS warn_settings (
    chat_id    BIGINT PRIMARY KEY,
    warn_limit SMALLINT NOT NULL
);

-- Settings the owner of a chat controls (see core/chatsettings.py).
CREATE TABLE IF NOT EXISTS chat_settings (
    chat_id    BIGINT PRIMARY KEY,
    staff_help BOOLEAN NOT NULL DEFAULT TRUE
);
-- More switches of the chat's owner (see core/chatsettings.py).
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS gban_enforce BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS admin_errors BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS anon_admin BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS clean_commands BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE chat_settings ADD COLUMN IF NOT EXISTS reports BOOLEAN NOT NULL DEFAULT TRUE;

-- Antiflood settings of a chat (see core/flood.py). flood_limit and timer_count of 0 mean "off".
CREATE TABLE IF NOT EXISTS flood_settings (
    chat_id       BIGINT PRIMARY KEY,
    flood_limit   INTEGER NOT NULL DEFAULT 10,   -- more than this many messages in a row
    timer_count   INTEGER NOT NULL DEFAULT 0,    -- this many messages ...
    timer_seconds INTEGER NOT NULL DEFAULT 0,    -- ... within this many seconds
    mode          TEXT NOT NULL DEFAULT 'mute',  -- ban / kick / mute / tban / tmute
    mode_seconds  INTEGER NOT NULL DEFAULT 0,    -- length of a tban / tmute
    clear_flood   BOOLEAN NOT NULL DEFAULT FALSE,
    antichannel   BOOLEAN NOT NULL DEFAULT FALSE
);

-- Blacklisted words of a chat and what happens to who says them (see core/blacklist.py).
CREATE TABLE IF NOT EXISTS blacklist (
    id             BIGSERIAL,
    chat_id        BIGINT NOT NULL,
    pattern        TEXT NOT NULL,       -- normalized: lowercase, single spaces, modifier included
    action         TEXT,                -- NULL = the chat's default action
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

-- Welcome and goodbye messages of a chat and what goes with them (see core/greetings.py).
-- A NULL content means "the default message".
CREATE TABLE IF NOT EXISTS greetings (
    chat_id               BIGINT PRIMARY KEY,
    welcome_on            BOOLEAN NOT NULL DEFAULT TRUE,
    welcome_content       TEXT,
    welcome_media_type    TEXT,
    welcome_media_file_id TEXT,
    goodbye_on            BOOLEAN NOT NULL DEFAULT FALSE,
    goodbye_content       TEXT,
    goodbye_media_type    TEXT,
    goodbye_media_file_id TEXT,
    welcome_mute          BOOLEAN NOT NULL DEFAULT FALSE,
    clean_welcome         BOOLEAN NOT NULL DEFAULT FALSE,
    clean_service         BOOLEAN NOT NULL DEFAULT FALSE,
    last_welcome_id       BIGINT
);

-- Federations: groups of chats that share one ban list (see core/federations.py).
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
-- Where the bot itself banned someone because of a federation ban, so that lifting the
-- ban undoes only that.
CREATE TABLE IF NOT EXISTS fed_applied (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (chat_id, user_id)
);

-- Locks: kinds of messages a chat does not allow (see core/locks.py).
CREATE TABLE IF NOT EXISTS locks (
    chat_id BIGINT NOT NULL,
    item    TEXT NOT NULL,
    PRIMARY KEY (chat_id, item)
);
CREATE TABLE IF NOT EXISTS lock_settings (
    chat_id BIGINT PRIMARY KEY,
    warn    BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE IF NOT EXISTS lock_allowlist (
    chat_id BIGINT NOT NULL,
    entry   TEXT NOT NULL,
    PRIMARY KEY (chat_id, entry)
);

-- The rules of a chat (see core/rulestore.py).
CREATE TABLE IF NOT EXISTS rules (
    chat_id       BIGINT PRIMARY KEY,
    content       TEXT NOT NULL DEFAULT '',
    media_type    TEXT,
    media_file_id TEXT
);

-- Words that keep people out when their profile contains them (see core/joinblacklist.py).
CREATE TABLE IF NOT EXISTS join_blacklist (
    chat_id BIGINT NOT NULL,
    kind    TEXT NOT NULL,      -- name / username / bio / channel
    pattern TEXT NOT NULL,
    PRIMARY KEY (chat_id, kind, pattern)
);

-- Admins who turned off report alerts in their private chat with the bot (see core/reports.py).
CREATE TABLE IF NOT EXISTS report_optout (
    user_id BIGINT PRIMARY KEY
);

-- Notes of a chat (see core/notestore.py). content is the text as its author wrote it.
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

-- Saved replies to keywords (see core/filterstore.py). content is the text as its author wrote it.
CREATE TABLE IF NOT EXISTS filters (
    id            BIGSERIAL,
    chat_id       BIGINT NOT NULL,
    pattern       TEXT NOT NULL,       -- normalized: lowercase, single spaces, modifier included
    content       TEXT NOT NULL DEFAULT '',
    media_type    TEXT,                -- photo / video / animation / document / audio / voice / sticker / video_note
    media_file_id TEXT,
    created_by    BIGINT,
    PRIMARY KEY (chat_id, pattern)
);

-- Global bans (see core/gbans.py). admin_id is NULL for imported bans.
CREATE TABLE IF NOT EXISTS gbans (
    user_id          BIGINT PRIMARY KEY,
    name             TEXT,
    reason           TEXT NOT NULL,
    admin_id         BIGINT,
    admin_name       TEXT,
    approved_by      BIGINT,           -- the Sudo user who approved a Support user's request
    approved_by_name TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Requests of Support users, waiting for a Sudo user to approve or decline them.
-- One request per user at a time (the UNIQUE constraint makes that race-free).
CREATE TABLE IF NOT EXISTS gban_requests (
    id                BIGSERIAL PRIMARY KEY,
    target_id         BIGINT NOT NULL UNIQUE,
    target_name       TEXT,
    kind              TEXT NOT NULL,   -- gban, dgban or ungban
    reason            TEXT NOT NULL,
    requested_by      BIGINT NOT NULL,
    requested_by_name TEXT,
    chat_id           BIGINT NOT NULL,
    msg_id            BIGINT,
    thread_id         BIGINT,
    target_msg_id     BIGINT,
    origin_html       TEXT NOT NULL,   -- "Initiated From" line, rendered when the request was made
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Users a chat has approved: trusted there, so global bans, federation bans, locks
-- and the like don't apply to them (see core/approvals.py).
CREATE TABLE IF NOT EXISTS approvals (
    chat_id          BIGINT NOT NULL,
    user_id          BIGINT NOT NULL,
    name             TEXT,
    approved_by      BIGINT,
    approved_by_name TEXT,
    approved_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id)
);

-- People who are away from keyboard (see core/afk.py). One row per person: AFK is
-- global, not per chat.
CREATE TABLE IF NOT EXISTS afk (
    user_id       BIGINT PRIMARY KEY,
    name          TEXT,
    username      TEXT,                -- lowercase, to recognise @mentions without a query
    reason        TEXT NOT NULL DEFAULT '',
    since         TIMESTAMPTZ NOT NULL DEFAULT now(),
    media_type    TEXT,                -- photo / animation / video
    media_file_id TEXT
);

-- Chats where the bot itself banned a user because of a global ban, so that
-- lifting the ban only undoes what the global ban did.
CREATE TABLE IF NOT EXISTS gban_bans (
    user_id   BIGINT NOT NULL,
    chat_id   BIGINT NOT NULL,
    banned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS gban_bans_chat_idx ON gban_bans (chat_id);

-- Phone book: every user the bot has seen, so it can resolve @usernames and ids.
CREATE TABLE IF NOT EXISTS users (
    user_id    BIGINT PRIMARY KEY,
    name       TEXT,
    username   TEXT,
    is_bot     BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS users_username_idx ON users (lower(username));
-- The name split in two, for /info. Older rows fill in the next time the user is seen.
ALTER TABLE users ADD COLUMN IF NOT EXISTS first_name TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_name TEXT;

-- Groups and channels the bot is in. Cleaned of chats the bot has left, either
-- on a schedule or with /cleanchats (see core/chats.py).
CREATE TABLE IF NOT EXISTS chats (
    chat_id    BIGINT PRIMARY KEY,
    type       TEXT NOT NULL,
    title      TEXT,
    username   TEXT,
    bot_admin  BOOLEAN,              -- NULL until known
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Where each user has been seen (wrote, joined, left, reacted ...). Groundwork
-- for global bans and federations: "in which of my chats is this person?".
CREATE TABLE IF NOT EXISTS user_chats (
    user_id    BIGINT NOT NULL,
    chat_id    BIGINT NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS user_chats_chat_idx ON user_chats (chat_id);

CREATE TABLE IF NOT EXISTS modules (
    name    TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL
);
"""


async def init() -> None:
    """Connect (retrying while the server starts up) and create tables."""
    global _pool

    for attempt in range(1, CONNECT_ATTEMPTS + 1):
        try:
            _pool = await asyncpg.create_pool(
                dsn=settings.DATABASE_URL,
                min_size=1,
                max_size=10,
            )
            break
        except (OSError, asyncpg.CannotConnectNowError) as exc:
            if attempt == CONNECT_ATTEMPTS:
                log.critical("Could not connect to the database: %s", exc)
                raise
            log.warning(
                "Database not reachable (attempt %d/%d): %s",
                attempt,
                CONNECT_ATTEMPTS,
                exc,
            )
            await asyncio.sleep(CONNECT_DELAY)

    await _db().execute(SCHEMA)
    log.info("Database ready")


async def close() -> None:
    global _pool
    if _pool is not None:
        await _pool.close()
        _pool = None


def _db() -> asyncpg.Pool:
    if _pool is None:
        raise RuntimeError("Database is not initialized. Call database.init() first.")
    return _pool


async def execute(sql: str, *args: Any) -> None:
    await _db().execute(sql, *args)


async def fetch_one(sql: str, *args: Any) -> dict[str, Any] | None:
    row = await _db().fetchrow(sql, *args)
    return dict(row) if row else None


async def fetch_all(sql: str, *args: Any) -> list[dict[str, Any]]:
    rows = await _db().fetch(sql, *args)
    return [dict(row) for row in rows]
