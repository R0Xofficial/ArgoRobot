"""
Async SQLite access (aiosqlite).

A single shared connection is used. aiosqlite runs queries in its own worker
thread, so awaiting them never blocks the event loop.
"""

import logging
from pathlib import Path
from typing import Any, Sequence

import aiosqlite

from config import settings

log = logging.getLogger(__name__)

_conn: aiosqlite.Connection | None = None

SCHEMA = """
CREATE TABLE IF NOT EXISTS roles (
    user_id  INTEGER PRIMARY KEY,
    role     INTEGER NOT NULL,
    added_by INTEGER,
    added_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS modules (
    name    TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL
);
"""


async def init() -> None:
    """Open the database and create tables if they do not exist."""
    global _conn

    path = Path(settings.DATABASE_PATH)
    path.parent.mkdir(parents=True, exist_ok=True)

    _conn = await aiosqlite.connect(path)
    _conn.row_factory = aiosqlite.Row
    await _conn.execute("PRAGMA journal_mode=WAL")
    await _conn.execute("PRAGMA foreign_keys=ON")
    await _conn.executescript(SCHEMA)
    await _conn.commit()

    log.info("Database ready: %s", path)


async def close() -> None:
    global _conn
    if _conn is not None:
        await _conn.close()
        _conn = None


def _db() -> aiosqlite.Connection:
    if _conn is None:
        raise RuntimeError("Database is not initialized. Call database.init() first.")
    return _conn


async def execute(sql: str, params: Sequence[Any] = ()) -> None:
    """Run a write statement and commit."""
    db = _db()
    await db.execute(sql, params)
    await db.commit()


async def fetch_one(sql: str, params: Sequence[Any] = ()) -> dict[str, Any] | None:
    async with _db().execute(sql, params) as cursor:
        row = await cursor.fetchone()
    return dict(row) if row else None


async def fetch_all(sql: str, params: Sequence[Any] = ()) -> list[dict[str, Any]]:
    async with _db().execute(sql, params) as cursor:
        rows = await cursor.fetchall()
    return [dict(row) for row in rows]
