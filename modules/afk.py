"""
Storage of AFK ("away from keyboard") statuses (the commands are in modules/afk.py).

Every message of every chat has to be checked against this: is the sender back,
and does the message mention or answer someone who is away? So all statuses are
kept in memory, loaded once; there are only ever as many as people who are away
right now. The database makes them survive a restart.
"""

import logging
from datetime import datetime, timezone
from typing import Any

from core import database

log = logging.getLogger(__name__)

MAX_REASON = 200

_records: dict[int, dict[str, Any]] | None = None
_by_username: dict[str, int] = {}


async def _load() -> dict[int, dict[str, Any]]:
    global _records
    if _records is None:
        rows = await database.fetch_all(
            "SELECT user_id, name, username, reason, since, media_type, media_file_id FROM afk"
        )
        _records = {row["user_id"]: row for row in rows}
        _by_username.clear()
        _by_username.update({row["username"]: row["user_id"] for row in rows if row["username"]})
        log.info("Loaded %d AFK status(es)", len(_records))
    return _records


async def get(user_id: int) -> dict[str, Any] | None:
    return (await _load()).get(user_id)


async def anyone_away() -> bool:
    return bool(await _load())


async def by_username(username: str) -> dict[str, Any] | None:
    records = await _load()
    user_id = _by_username.get(username.lstrip("@").lower())
    return records.get(user_id) if user_id is not None else None


async def set_away(
    user_id: int,
    name: str | None,
    username: str | None,
    reason: str,
    media_type: str | None = None,
    media_file_id: str | None = None,
) -> dict[str, Any]:
    """Mark a user as away from now on (or replace an earlier status)."""
    records = await _load()
    row = {
        "user_id": user_id,
        "name": name,
        "username": username.lower() if username else None,
        "reason": reason,
        "since": datetime.now(timezone.utc),
        "media_type": media_type if media_file_id else None,
        "media_file_id": media_file_id if media_type else None,
    }
    await database.execute(
        "INSERT INTO afk (user_id, name, username, reason, since, media_type, media_file_id) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7) "
        "ON CONFLICT (user_id) DO UPDATE SET name = EXCLUDED.name, username = EXCLUDED.username, "
        "reason = EXCLUDED.reason, since = EXCLUDED.since, media_type = EXCLUDED.media_type, "
        "media_file_id = EXCLUDED.media_file_id",
        user_id,
        row["name"],
        row["username"],
        reason,
        row["since"],
        row["media_type"],
        row["media_file_id"],
    )
    old = records.get(user_id)
    if old and old["username"]:
        _by_username.pop(old["username"], None)
    records[user_id] = row
    if row["username"]:
        _by_username[row["username"]] = user_id
    return row


async def clear(user_id: int) -> dict[str, Any] | None:
    """The user is back. Returns the status that ended, or None if they were not away."""
    records = await _load()
    old = records.pop(user_id, None)
    if old is None:
        return None
    if old["username"]:
        _by_username.pop(old["username"], None)
    await database.execute("DELETE FROM afk WHERE user_id = $1", user_id)
    return old
