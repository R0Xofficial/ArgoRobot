"""
The whitelist: users protected from global bans and federation bans.

Developers and sudo users manage it with /whitelist and /unwhitelist
(modules/root.py). Modules that ban people across chats ask `is_protected()`
before acting; it covers both the whitelist and the fixed immunity list
(core/immunity.py).

The list is small and read on every global action, so it is kept in memory and
reloaded after each change.
"""

import logging
from typing import Any

from core import database, immunity

log = logging.getLogger(__name__)

_entries: dict[int, dict[str, Any]] | None = None


async def _load() -> dict[int, dict[str, Any]]:
    global _entries
    rows = await database.fetch_all(
        "SELECT user_id, name, added_at, added_by, added_by_name FROM whitelist ORDER BY added_at, user_id"
    )
    _entries = {row["user_id"]: row for row in rows}
    log.info("Loaded %d whitelisted user(s)", len(_entries))
    return _entries


async def _all() -> dict[int, dict[str, Any]]:
    return _entries if _entries is not None else await _load()


async def is_whitelisted(user_id: int) -> bool:
    return user_id in await _all()


async def is_protected(user_id: int) -> bool:
    """Immune or whitelisted: no global or federation action may touch this user."""
    return immunity.is_immune(user_id) or await is_whitelisted(user_id)


async def add(user_id: int, added_by: int | None, name: str | None, added_by_name: str | None) -> bool:
    """Whitelist a user. False if they already were."""
    if await is_whitelisted(user_id):
        return False
    await database.execute(
        "INSERT INTO whitelist (user_id, added_by, name, added_by_name) VALUES ($1, $2, $3, $4) "
        "ON CONFLICT (user_id) DO NOTHING",
        user_id,
        added_by,
        name,
        added_by_name,
    )
    await _load()
    return True


async def remove(user_id: int) -> bool:
    """Take a user off the whitelist. False if they were not on it."""
    if not await is_whitelisted(user_id):
        return False
    await database.execute("DELETE FROM whitelist WHERE user_id = $1", user_id)
    await _load()
    return True


async def list_all() -> list[dict[str, Any]]:
    return list((await _all()).values())
