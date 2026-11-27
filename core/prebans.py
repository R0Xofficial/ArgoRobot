"""
Bans and mutes saved for people Telegram won't let the bot touch yet.

A ban of an ID the bot has never seen is refused by Telegram ("user not found"). The ban is saved here
instead, and modules/bans.py applies it as soon as that person writes, joins or reacts in the chat.
A timed ban keeps its end time. A mute is saved the same way. One person has one saved action per chat,
and a ban outranks a mute.
"""

BAN = "ban"
MUTE = "mute"

from datetime import datetime, timezone

from core import database

_pending: set[tuple[int, int]] | None = None  # (chat, user): so a message costs no query


async def _known() -> set[tuple[int, int]]:
    global _pending
    if _pending is None:
        rows = await database.fetch_all("SELECT chat_id, user_id FROM chat_prebans")
        _pending = {(row["chat_id"], row["user_id"]) for row in rows}
    return _pending


async def add(
    chat_id: int, user_id: int, reason: str, until: datetime | None, by_id: int | None, by_name: str | None, kind: str = BAN
) -> None:
    # A mute never replaces a saved ban.
    await database.execute(
        """
        INSERT INTO chat_prebans (chat_id, user_id, reason, until, by_id, by_name, kind)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        ON CONFLICT (chat_id, user_id) DO UPDATE
            SET reason = EXCLUDED.reason, until = EXCLUDED.until, by_id = EXCLUDED.by_id, by_name = EXCLUDED.by_name,
                kind = EXCLUDED.kind
            WHERE EXCLUDED.kind = 'ban' OR chat_prebans.kind <> 'ban'
        """,
        chat_id, user_id, reason, until, by_id, by_name, kind,
    )
    (await _known()).add((chat_id, user_id))


async def pending(chat_id: int, user_id: int) -> bool:
    return (chat_id, user_id) in await _known()


async def take(chat_id: int, user_id: int) -> dict | None:
    """Remove and return the saved ban, unless it has run out in the meantime."""
    row = await database.fetch_one(
        "DELETE FROM chat_prebans WHERE chat_id = $1 AND user_id = $2 RETURNING reason, until, by_id, by_name, kind", chat_id, user_id
    )
    (await _known()).discard((chat_id, user_id))
    if row is None:
        return None
    until = row["until"]
    if until is not None and until <= datetime.now(timezone.utc):
        return None
    return row


async def remove(chat_id: int, user_id: int, kind: str = BAN) -> bool:
    """Forget a saved ban or mute of that kind (an unban, an unmute). True when there was one."""
    row = await database.fetch_one(
        "DELETE FROM chat_prebans WHERE chat_id = $1 AND user_id = $2 AND kind = $3 RETURNING user_id", chat_id, user_id, kind
    )
    if row is not None:
        (await _known()).discard((chat_id, user_id))
    return row is not None
