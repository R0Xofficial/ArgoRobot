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
