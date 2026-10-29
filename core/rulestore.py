"""
The rules of a chat (modules/rules.py).

One row per chat: text in the Markdown of core/markup.py, optionally with media, kept as
the admin wrote it and turned into a message only when it is sent (core/sender.py).
"""

from typing import Any

from core import database


async def get(chat_id: int) -> dict[str, Any] | None:
    """The rules as {content, media_type, media_file_id}, or None when none are set."""
    return await database.fetch_one(
        "SELECT content, media_type, media_file_id FROM rules WHERE chat_id = $1", chat_id
    )


async def save(chat_id: int, content: str, media_type: str | None, media_file_id: str | None) -> None:
    await database.execute(
        "INSERT INTO rules (chat_id, content, media_type, media_file_id) VALUES ($1, $2, $3, $4) "
        "ON CONFLICT (chat_id) DO UPDATE SET content = EXCLUDED.content, "
        "media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id",
        chat_id, content, media_type, media_file_id,
    )


async def remove(chat_id: int) -> bool:
    row = await database.fetch_one("DELETE FROM rules WHERE chat_id = $1 RETURNING chat_id", chat_id)
    return row is not None
