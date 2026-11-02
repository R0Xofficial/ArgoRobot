"""
Welcome and goodbye messages of a chat (modules/greetings.py).

One row per chat holds both messages, their on/off switches and the other greeting
settings. A chat without a row, or a message that was never set, uses the defaults.
"""

from typing import Any

from core import database

DEFAULT_WELCOME = "Welcome to {chatname}, {mention}! Behave. Or don't, I'll handle it. 😏"
DEFAULT_GOODBYE = "{first} left. The chat is a little quieter now. 👋"

DEFAULTS: dict[str, Any] = {
    "welcome_on": True,
    "welcome_content": None,
    "welcome_media_type": None,
    "welcome_media_file_id": None,
    "goodbye_on": True,
    "goodbye_content": None,
    "goodbye_media_type": None,
    "goodbye_media_file_id": None,
    "welcome_mute": False,
    "clean_welcome": False,
    "clean_service": False,
    "last_welcome_id": None,
}


async def get(chat_id: int) -> dict[str, Any]:
    row = await database.fetch_one("SELECT * FROM greetings WHERE chat_id = $1", chat_id)
    return {**DEFAULTS, **{key: row[key] for key in DEFAULTS if row is not None and key in row}}


async def update(chat_id: int, **columns: Any) -> None:
    """Change some settings of a chat; the others keep their value."""
    unknown = set(columns) - set(DEFAULTS)
    if unknown or not columns:
        raise ValueError(f"unknown greeting settings: {sorted(unknown)}")
    names = list(columns)
    placeholders = ", ".join(f"${i + 2}" for i in range(len(names)))
    assignments = ", ".join(f"{name} = EXCLUDED.{name}" for name in names)
    await database.execute(
        f"INSERT INTO greetings (chat_id, {', '.join(names)}) VALUES ($1, {placeholders}) "
        f"ON CONFLICT (chat_id) DO UPDATE SET {assignments}",
        chat_id,
        *columns.values(),
    )


def message_of(settings: dict[str, Any], kind: str) -> tuple[str, str | None, str | None]:
    """(text, media type, media file id) to send for "welcome" or "goodbye"."""
    content = settings[f"{kind}_content"]
    media_type = settings[f"{kind}_media_type"]
    if content is None and media_type is None:
        return (DEFAULT_WELCOME if kind == "welcome" else DEFAULT_GOODBYE), None, None
    return content or "", media_type, settings[f"{kind}_media_file_id"]
