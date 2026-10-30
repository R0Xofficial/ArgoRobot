"""
Storage of global bans (the commands and the enforcement are in modules/gbans.py).

A global ban is a record saying "this user is not welcome in any chat that uses
the bot". Three small tables back it:

  * gbans            who is banned, why, and by whom
  * gban_requests    what Support users asked for and Sudo users have not decided yet
  * gban_bans        the chats where the bot itself removed someone because of a
                     global ban, so that lifting the ban undoes only that

The ids of banned users are kept in memory: every message of every chat is
checked against them, and that check must not cost a database query.
"""

import logging
from datetime import timedelta
from typing import Any, Iterable

from core import database

log = logging.getLogger(__name__)

REQUEST_LIFETIME = timedelta(days=7)  # an unanswered request stops blocking new ones after this

_ids: set[int] | None = None


# --- Bans ---------------------------------------------------------------------

async def _all_ids() -> set[int]:
    global _ids
    if _ids is None:
        rows = await database.fetch_all("SELECT user_id FROM gbans")
        _ids = {row["user_id"] for row in rows}
        log.info("Loaded %d global ban(s)", len(_ids))
    return _ids


async def is_gbanned(user_id: int) -> bool:
    return user_id in await _all_ids()


async def get(user_id: int) -> dict[str, Any] | None:
    if user_id not in await _all_ids():
        return None
    return await database.fetch_one(
        "SELECT user_id, name, reason, admin_id, admin_name, approved_by, approved_by_name, created_at "
        "FROM gbans WHERE user_id = $1",
        user_id,
    )


async def add(
    user_id: int,
    name: str | None,
    reason: str,
    admin_id: int,
    admin_name: str | None,
    approved_by: int | None = None,
    approved_by_name: str | None = None,
) -> dict[str, Any] | None:
    """
    Ban a user globally, or change the reason of an existing ban (the time and
    the admin are updated too). Returns the previous record, if there was one.
    """
    old = await get(user_id)
    await database.execute(
        "INSERT INTO gbans (user_id, name, reason, admin_id, admin_name, approved_by, approved_by_name) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7) "
        "ON CONFLICT (user_id) DO UPDATE SET name = EXCLUDED.name, reason = EXCLUDED.reason, "
        "admin_id = EXCLUDED.admin_id, admin_name = EXCLUDED.admin_name, "
        "approved_by = EXCLUDED.approved_by, approved_by_name = EXCLUDED.approved_by_name, "
        "created_at = now()",
        user_id,
        name,
        reason,
        admin_id,
        admin_name,
        approved_by,
        approved_by_name,
    )
    (await _all_ids()).add(user_id)
    return old


async def remove(user_id: int) -> dict[str, Any] | None:
    """Lift a global ban. Returns the removed record, or None if there was none."""
    row = await database.fetch_one(
        "DELETE FROM gbans WHERE user_id = $1 "
        "RETURNING user_id, name, reason, admin_id, admin_name, created_at",
        user_id,
    )
    (await _all_ids()).discard(user_id)
    return row


async def import_many(rows: Iterable[tuple]) -> int:
    """
    Add bans that do not exist yet (users already banned are left alone). A row is
    (user id, reason) or (user id, reason, name). Imported bans have no admin.
    Returns how many were added.
    """
    rows = list(rows)
    if not rows:
        return 0
    added = await database.fetch_all(
        "INSERT INTO gbans (user_id, reason, name) "
        "SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[]) "
        "ON CONFLICT (user_id) DO NOTHING RETURNING user_id",
        [row[0] for row in rows],
        [row[1] for row in rows],
        [row[2] if len(row) > 2 else None for row in rows],
    )
    ids = await _all_ids()
    ids.update(row["user_id"] for row in added)
    return len(added)


async def all_bans() -> list[dict[str, Any]]:
    """Every global ban, oldest first, as rows for core.banfiles (banned_by_name = the admin)."""
    return await database.fetch_all(
        "SELECT user_id, name, reason, admin_name AS banned_by_name, created_at FROM gbans ORDER BY created_at, user_id"
    )


async def count() -> int:
    return len(await _all_ids())


# --- Requests of Support users ------------------------------------------------

async def has_pending(target_id: int) -> bool:
    await _expire_requests(target_id)
    row = await database.fetch_one("SELECT 1 AS found FROM gban_requests WHERE target_id = $1", target_id)
    return row is not None


async def _expire_requests(target_id: int) -> None:
    await database.execute(
        "DELETE FROM gban_requests WHERE target_id = $1 AND created_at < now() - $2::interval",
        target_id,
        REQUEST_LIFETIME,
    )


async def create_request(
    *,
    target_id: int,
    target_name: str | None,
    kind: str,
    reason: str,
    requested_by: int,
    requested_by_name: str | None,
    chat_id: int,
    msg_id: int | None,
    thread_id: int | None,
    target_msg_id: int | None,
    origin_html: str,
) -> int | None:
    """Save a request. Returns its id, or None when the user already has a pending one."""
    await _expire_requests(target_id)
    row = await database.fetch_one(
        "INSERT INTO gban_requests (target_id, target_name, kind, reason, requested_by, requested_by_name, "
        "chat_id, msg_id, thread_id, target_msg_id, origin_html) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) "
        "ON CONFLICT (target_id) DO NOTHING RETURNING id",
        target_id,
        target_name,
        kind,
        reason,
        requested_by,
        requested_by_name,
        chat_id,
        msg_id,
        thread_id,
        target_msg_id,
        origin_html,
    )
    return row["id"] if row else None


async def claim_request(request_id: int) -> dict[str, Any] | None:
    """
    Take a request out of the queue. Whoever gets the row decides it; anyone
    pressing a button a moment later gets None, so a request is never handled twice.
    """
    return await database.fetch_one("DELETE FROM gban_requests WHERE id = $1 RETURNING *", request_id)


async def drop_request(request_id: int) -> None:
    await database.execute("DELETE FROM gban_requests WHERE id = $1", request_id)


# --- Where the bot removed someone ----------------------------------------------

async def record_ban(user_id: int, chat_id: int) -> None:
    await database.execute(
        "INSERT INTO gban_bans (user_id, chat_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", user_id, chat_id
    )


async def banned_chats(user_id: int) -> list[int]:
    rows = await database.fetch_all("SELECT chat_id FROM gban_bans WHERE user_id = $1", user_id)
    return [row["chat_id"] for row in rows]


async def forget_bans(user_id: int) -> None:
    await database.execute("DELETE FROM gban_bans WHERE user_id = $1", user_id)


async def forget_ban(user_id: int, chat_id: int) -> None:
    await database.execute("DELETE FROM gban_bans WHERE user_id = $1 AND chat_id = $2", user_id, chat_id)
