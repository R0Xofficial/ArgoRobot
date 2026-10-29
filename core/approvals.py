"""
Approved users: people a chat's admins trust without making them admins.

An approved user is exempt from the automatic measures of the bot in that chat
only: global bans (modules/gbans.py), federation bans, and any lock, blacklist
or antiflood a module adds later. Such a module only has to ask:

    if await approvals.is_approved(chat_id, user_id):
        return   # leave them alone

Approval never stops an admin from banning, muting or warning the person by hand:
admins still decide who stays in their chat.

Managed with /approve and friends (modules/approves.py).
"""

from typing import Any

from core import database


async def approve(chat_id: int, user_id: int, name: str | None, by: int | None, by_name: str | None) -> bool:
    """Approve a user in a chat. False if they already were."""
    row = await database.fetch_one(
        "INSERT INTO approvals (chat_id, user_id, name, approved_by, approved_by_name) "
        "VALUES ($1, $2, $3, $4, $5) ON CONFLICT (chat_id, user_id) DO NOTHING RETURNING user_id",
        chat_id,
        user_id,
        name,
        by,
        by_name,
    )
    return row is not None


async def unapprove(chat_id: int, user_id: int) -> bool:
    row = await database.fetch_one(
        "DELETE FROM approvals WHERE chat_id = $1 AND user_id = $2 RETURNING user_id", chat_id, user_id
    )
    return row is not None


async def is_approved(chat_id: int, user_id: int) -> bool:
    row = await database.fetch_one(
        "SELECT 1 AS found FROM approvals WHERE chat_id = $1 AND user_id = $2", chat_id, user_id
    )
    return row is not None


async def get(chat_id: int, user_id: int) -> dict[str, Any] | None:
    return await database.fetch_one(
        "SELECT user_id, name, approved_by, approved_by_name, approved_at "
        "FROM approvals WHERE chat_id = $1 AND user_id = $2",
        chat_id,
        user_id,
    )


async def list_chat(chat_id: int) -> list[dict[str, Any]]:
    return await database.fetch_all(
        "SELECT user_id, name, approved_at FROM approvals WHERE chat_id = $1 ORDER BY approved_at, user_id",
        chat_id,
    )


async def clear_chat(chat_id: int) -> int:
    """Remove every approval of a chat. Returns how many there were."""
    rows = await database.fetch_all("DELETE FROM approvals WHERE chat_id = $1 RETURNING user_id", chat_id)
    return len(rows)
