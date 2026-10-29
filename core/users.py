"""
Phone book of the users the bot has seen.

Telegram only tells a bot about users it is in contact with: it cannot look up
an arbitrary @username or id. So the bot writes down everyone it meets (name,
id and @username) and uses this book later, for example to turn @Michalek123
into the right account when promoting someone, or to name the owner of an id.

Users are recorded by UsersMiddleware from every update: who wrote a message,
who was replied to, who was mentioned, who joined or left, who pressed a button.
To keep the database quiet, a user is written only when something changed (or
once an hour to refresh last_seen); everything else is served from memory.
"""

import logging
import time
from typing import Any, Iterable

from core import database

log = logging.getLogger(__name__)

# Telegram's own service accounts (system messages, anonymous admins, channels).
IGNORED_IDS = {777000, 1087968824, 136817688}

REFRESH_SECONDS = 3600
CACHE_LIMIT = 50_000

# user_id -> (name, username, time of the last write)
_cache: dict[int, tuple[str | None, str | None, float]] = {}

UPSERT = """
WITH freed AS (
    UPDATE users SET username = NULL
    WHERE lower(username) = lower($3) AND user_id <> $1
)
INSERT INTO users (user_id, name, username, is_bot)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET
    name = EXCLUDED.name,
    username = EXCLUDED.username,
    is_bot = EXCLUDED.is_bot,
    last_seen = now()
"""


# --- Collecting users from an update ----------------------------------------

def _entity_kind(entity: Any) -> str:
    return getattr(entity.type, "value", entity.type)


def _users_in_message(message: Any, add) -> None:
    add(getattr(message, "from_user", None))

    reply = getattr(message, "reply_to_message", None)
    if reply is not None:
        add(getattr(reply, "from_user", None))

    origin = getattr(message, "forward_origin", None)
    if origin is not None:
        add(getattr(origin, "sender_user", None))

    for member in getattr(message, "new_chat_members", None) or []:
        add(member)
    add(getattr(message, "left_chat_member", None))

    # A mention of a person without a @username: the link carries the account.
    for entities in (getattr(message, "entities", None), getattr(message, "caption_entities", None)):
        for entity in entities or []:
            if _entity_kind(entity) == "text_mention":
                add(getattr(entity, "user", None))


def collect(update: Any) -> list[Any]:
    """Every user an update tells us about, without duplicates."""
    found: dict[int, Any] = {}

    def add(user: Any) -> None:
        user_id = getattr(user, "id", None)
        if user_id is None or user_id in IGNORED_IDS:
            return
        found.setdefault(user_id, user)

    for name in ("message", "edited_message"):
        message = getattr(update, name, None)
        if message is not None:
            _users_in_message(message, add)

    callback = getattr(update, "callback_query", None)
    if callback is not None:
        add(callback.from_user)

    for name in ("my_chat_member", "chat_member"):
        event = getattr(update, name, None)
        if event is not None:
            add(event.from_user)
            add(event.old_chat_member.user)
            add(event.new_chat_member.user)

    for name in ("inline_query", "chosen_inline_result", "chat_join_request"):
        event = getattr(update, name, None)
        if event is not None:
            add(event.from_user)

    return list(found.values())


# --- Writing ----------------------------------------------------------------

async def remember(user: Any) -> None:
    """Record a user (anything with id, full_name and username)."""
    user_id = user.id
    if user_id in IGNORED_IDS:
        return

    name = getattr(user, "full_name", None) or None
    username = getattr(user, "username", None) or None
    now = time.monotonic()

    cached = _cache.get(user_id)
    if cached and cached[0] == name and cached[1] == username and now - cached[2] < REFRESH_SECONDS:
        return

    await database.execute(UPSERT, user_id, name, username, bool(getattr(user, "is_bot", False)))

    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[user_id] = (name, username, now)


async def remember_all(users: Iterable[Any]) -> None:
    for user in users:
        await remember(user)


# --- Reading ----------------------------------------------------------------

async def get(user_id: int) -> dict[str, Any] | None:
    """The stored record of a user id, or None if the bot never met them."""
    return await database.fetch_one(
        "SELECT user_id, name, username, is_bot, first_seen, last_seen "
        "FROM users WHERE user_id = $1",
        user_id,
    )


async def find_by_username(username: str) -> dict[str, Any] | None:
    """The user currently known under @username (case does not matter)."""
    return await database.fetch_one(
        "SELECT user_id, name, username, is_bot, first_seen, last_seen "
        "FROM users WHERE lower(username) = lower($1)",
        username.lstrip("@"),
    )


async def name_of(user_id: int) -> str | None:
    row = await get(user_id)
    return row["name"] if row else None
