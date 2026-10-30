"""
Notes: messages saved under a name that anyone in the chat can ask for (the commands
are in modules/notes.py).

A note is text in the HTML of core/markup.py, optionally with a photo, video, GIF,
sticker, document, audio or voice message, kept as the person wrote it and turned into a
message only when it is sent (core/sender.py). Names are one word, case-insensitive.
"""

import re
from typing import Any

from core import database

MAX_NOTES = 300
MAX_NAME = 64
_NAME = re.compile(r"^[\w.\-]{1,%d}$" % MAX_NAME)


class NoteNameError(ValueError):
    """The name is not acceptable; the message says why."""


def check_name(text: str) -> str:
    """The stored form of a name typed by a person (a leading # is allowed). Raises NoteNameError."""
    name = text.strip().lstrip("#").lower()
    if not name:
        raise NoteNameError("A note needs a name.")
    if not _NAME.match(name):
        raise NoteNameError(
            f"A note name is a single word of at most {MAX_NAME} letters, digits, dots, dashes or underscores."
        )
    return name


async def get(chat_id: int, name: str) -> dict[str, Any] | None:
    return await database.fetch_one(
        "SELECT name, content, media_type, media_file_id FROM notes WHERE chat_id = $1 AND name = $2", chat_id, name
    )


async def names(chat_id: int) -> list[str]:
    rows = await database.fetch_all("SELECT name FROM notes WHERE chat_id = $1 ORDER BY name", chat_id)
    return [row["name"] for row in rows]


async def save(
    chat_id: int,
    name: str,
    content: str,
    media_type: str | None,
    media_file_id: str | None,
    created_by: int | None,
) -> str:
    """Save a note. Returns "added", "updated" or "full" (the chat has no room for another)."""
    exists = await database.fetch_one("SELECT 1 AS found FROM notes WHERE chat_id = $1 AND name = $2", chat_id, name)
    if exists is None:
        count = await database.fetch_one("SELECT count(*) AS total FROM notes WHERE chat_id = $1", chat_id)
        if count and count["total"] >= MAX_NOTES:
            return "full"
    await database.execute(
        "INSERT INTO notes (chat_id, name, content, media_type, media_file_id, created_by) "
        "VALUES ($1, $2, $3, $4, $5, $6) "
        "ON CONFLICT (chat_id, name) DO UPDATE SET content = EXCLUDED.content, "
        "media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id",
        chat_id, name, content, media_type, media_file_id, created_by,
    )
    return "updated" if exists is not None else "added"


async def remove(chat_id: int, name: str) -> bool:
    row = await database.fetch_one("DELETE FROM notes WHERE chat_id = $1 AND name = $2 RETURNING name", chat_id, name)
    return row is not None


async def clear(chat_id: int) -> int:
    rows = await database.fetch_all("DELETE FROM notes WHERE chat_id = $1 RETURNING name", chat_id)
    return len(rows)
