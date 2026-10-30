"""
Managing a group from a private chat (/connect, /disconnect, /connection in modules/connection.py).

An admin connects their private chat with me to one group. From then on the group's admin
commands they send me privately (/ban @someone, /lock url, /filters ...) act on that group,
and I answer privately. Nothing about their rights changes: every command still checks
the person against the group itself, so a connection never lets anyone do more than they
could do there.

The ConnectionMiddleware (core/middlewares.py) swaps the chat of such a command for the group
before the handlers see it; reply() (core/helpers.py) sends the answer to `origin`, the
private chat the command came from.
"""

import logging
from contextvars import ContextVar
from typing import Any

from core import database, disabled
from core.users import CACHE_LIMIT

log = logging.getLogger(__name__)

GROUP_TYPES = ("group", "supergroup")

# Modules whose commands are about running the group.
CONNECTABLE = frozenset({
    "bans", "mutes", "warns", "locks", "blacklists", "filters", "notes", "greetings", "rules",
    "approves", "promote", "antiflood", "joinblacklists", "admin", "captcha",
})

# Commands of those modules that don't make sense from a private chat: they delete the command
# itself, act on whoever sent it, or show a saved message that would then land in the group.
NOT_REMOTE = frozenset({
    "sban", "dban", "smute", "stmute", "stempmute", "sunmute", "dwarn",
    "kickme", "punchme", "get", "rules", "welcome", "goodbye",
})

# The private chat a connected command came from; None while handling anything else.
origin: ContextVar[int | None] = ContextVar("connection_origin", default=None)


class ConnectError(Exception):
    """A connection could not be made; the text says why (shown to the person)."""


_cache: dict[int, dict[str, Any] | None] = {}


async def get(user_id: int) -> dict[str, Any] | None:
    """The group this person is connected to: {chat_id, title, chat_type}, or None."""
    if user_id in _cache:
        return _cache[user_id]
    row = await database.fetch_one("SELECT chat_id, title, chat_type FROM connections WHERE user_id = $1", user_id)
    found = dict(row) if row else None
    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[user_id] = found
    return found


async def save(user_id: int, chat_id: int, title: str | None, chat_type: str) -> None:
    await database.execute(
        "INSERT INTO connections (user_id, chat_id, title, chat_type) VALUES ($1, $2, $3, $4) "
        "ON CONFLICT (user_id) DO UPDATE SET chat_id = EXCLUDED.chat_id, title = EXCLUDED.title, "
        "chat_type = EXCLUDED.chat_type, connected_at = now()",
        user_id, chat_id, title, chat_type,
    )
    _cache[user_id] = {"chat_id": chat_id, "title": title, "chat_type": chat_type}


async def clear(user_id: int) -> bool:
    """Disconnect. False when there was no connection."""
    if await get(user_id) is None:
        return False
    await database.execute("DELETE FROM connections WHERE user_id = $1", user_id)
    _cache[user_id] = None
    return True


async def drop_chat(chat_id: int) -> list[int]:
    """Nobody stays connected to a chat I have left or that turned connections off. Returns who was."""
    rows = await database.fetch_all("SELECT user_id FROM connections WHERE chat_id = $1", chat_id)
    await database.execute("DELETE FROM connections WHERE chat_id = $1", chat_id)
    for user_id, record in list(_cache.items()):
        if record is not None and record["chat_id"] == chat_id:
            _cache[user_id] = None
    return [row["user_id"] for row in rows]


async def drop_all() -> list[dict]:
    """End every connection (the module was turned off for the whole bot). Returns the ones that were open."""
    rows = await database.fetch_all("SELECT user_id, chat_id, title FROM connections")
    await database.execute("DELETE FROM connections")
    _cache.clear()
    return [dict(row) for row in rows]


async def switched_off(chat_id: int | None = None) -> str | None:
    """Why connections can't be used (turned off for the whole bot, or in that group), else None."""
    from core import registry

    info = registry.get("connection")
    if info is not None and not info.enabled:
        return "Connections are switched off."
    if chat_id is not None and await disabled.module_off(chat_id, "connection"):
        return "Connections are switched off in that group."
    return None


def forget_chat(old_id: int, new_id: int) -> None:
    """A group changed its id: the cached connections must follow (the rows are moved in core/chats.py)."""
    for record in _cache.values():
        if record is not None and record["chat_id"] == old_id:
            record["chat_id"] = new_id


async def connect(bot, user_id: int, reference: int | str) -> dict[str, Any]:
    """
    Connect a person to a group they administer. `reference` is a chat id or @username.
    Raises ConnectError with the reason to show when that isn't possible.
    """
    from aiogram.exceptions import TelegramAPIError  # imported late: keeps this module cheap to import
    from core import moderation

    try:
        chat = await bot.get_chat(reference)
    except TelegramAPIError:
        raise ConnectError("I can't find that chat. Check the ID or username, and make sure I am in the group.") from None
    if getattr(chat, "type", None) not in GROUP_TYPES:
        raise ConnectError("You can only connect to a group.")
    why = await switched_off(chat.id)
    if why:
        raise ConnectError(why)
    if await moderation.member_status(bot, chat.id, bot.id) in (None, "left", "kicked"):
        raise ConnectError("I am not in that group.")
    status = await moderation.member_status(bot, chat.id, user_id)
    if status is None:
        raise ConnectError("I couldn't check your rights in that group.")
    if status not in ("creator", "administrator"):
        raise ConnectError("Only admins of that group can connect to it.")
    await save(user_id, chat.id, getattr(chat, "title", None), chat.type)
    return _cache[user_id]


def command_name(text: str, prefixes: tuple[str, ...] | list[str], my_username: str | None) -> str | None:
    """The command word of a message (lower case, without prefix or @bot), None when it isn't for me."""
    word = (text or "").split(maxsplit=1)[0] if (text or "").strip() else ""
    if len(word) < 2 or not word.startswith(tuple(prefixes)):
        return None
    name, _, addressed = word[1:].partition("@")
    if addressed and (my_username or "").lower() != addressed.lower():
        return None
    return name.lower() or None


def is_remote(name: str) -> bool:
    """Whether /name, sent privately by a connected admin, is run for the group."""
    entry = disabled.lookup(name)
    return entry is not None and entry[1] in CONNECTABLE and name.lower() not in NOT_REMOTE


def rewrite(message: Any, record: dict[str, Any]) -> Any:
    """
    A copy of a private message that looks like it was sent in the connected group.

    The message id is zeroed and the reply is dropped: both belong to the private chat, and
    must never point at a message of the group.
    """
    from aiogram.types import Chat

    chat = Chat(id=record["chat_id"], type=record["chat_type"], title=record["title"])
    try:
        chat = chat.as_(message.bot)
    except AttributeError:
        pass
    return message.model_copy(
        update={"chat": chat, "message_id": 0, "reply_to_message": None, "message_thread_id": None, "is_topic_message": None}
    )
