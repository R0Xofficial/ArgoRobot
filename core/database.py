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
