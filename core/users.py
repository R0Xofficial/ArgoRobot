"""
Phone book of the users the bot has seen.

Telegram only tells a bot about users it is in contact with: it cannot look up
an arbitrary @username or id. So the bot writes down everyone it meets (name,
id and @username) and uses this book later, for example to turn @Michalek123
into the right account when promoting someone, or to name the owner of an id.

Users are recorded by UsersMiddleware from every update. Telegram only sends a
bot the update types it asks for, so UPDATE_TYPES lists everything that carries
people (main.py requests them all). Telegram sends nothing when someone changes
their name or @username; the change is noticed the next time that person shows up
in any update.

Besides updates, the bot asks Telegram for the administrators of every group it
sees (a bot cannot list ordinary members) and records them too.

To keep the database quiet, a user is written only when something changed (or
once an hour to refresh last_seen); everything else is served from memory.
"""

import asyncio
import logging
import time
from typing import Any, Iterable

from aiogram.exceptions import TelegramAPIError

from core import database

log = logging.getLogger(__name__)

# Telegram's own service accounts (system messages, anonymous admins, channels).
IGNORED_IDS = {777000, 1087968824, 136817688}

# Update types that carry users. Some only arrive when the bot is an admin of
# the chat (chat_member, message_reaction) or has the feature enabled (inline).
UPDATE_TYPES = [
    "message",
    "edited_message",
    "callback_query",
    "inline_query",
    "chosen_inline_result",
    "chat_member",
    "my_chat_member",
    "chat_join_request",
    "message_reaction",
    "poll_answer",
    "chat_boost",
    "removed_chat_boost",
]

REFRESH_SECONDS = 3600
CACHE_LIMIT = 50_000
ADMIN_REFRESH_SECONDS = 6 * 3600

# user_id -> (name, username, time of the last write)
_cache: dict[int, tuple[str | None, str | None, float]] = {}

UPSERT = """
WITH freed AS (
    UPDATE users SET username = NULL
    WHERE lower(username) = lower($3) AND user_id <> $1
)
INSERT INTO users (user_id, name, username, is_bot, first_name, last_name)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id) DO UPDATE SET
    name = EXCLUDED.name,
    username = EXCLUDED.username,
    is_bot = EXCLUDED.is_bot,
    first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    last_seen = now()
"""


# --- Collecting users from an update ----------------------------------------

def _entity_kind(entity: Any) -> str:
    return getattr(entity.type, "value", entity.type)


def _users_in_message(message: Any, add, depth: int = 1) -> None:
    add(getattr(message, "from_user", None))
    add(getattr(message, "via_bot", None))

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

    # The message being replied to and a pinned message, one level deep.
    if depth > 0:
        for nested in (getattr(message, "reply_to_message", None), getattr(message, "pinned_message", None)):
            if nested is not None:
                _users_in_message(nested, add, depth - 1)


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
            add(getattr(getattr(event, "invite_link", None), "creator", None))

    request = getattr(update, "chat_join_request", None)
    if request is not None:
        add(request.from_user)
        add(getattr(getattr(request, "invite_link", None), "creator", None))

    for name in ("inline_query", "chosen_inline_result"):
        event = getattr(update, name, None)
        if event is not None:
            add(event.from_user)

    reaction = getattr(update, "message_reaction", None)
    if reaction is not None:
        add(getattr(reaction, "user", None))

    poll_answer = getattr(update, "poll_answer", None)
    if poll_answer is not None:
        add(getattr(poll_answer, "user", None))

    for name in ("chat_boost", "removed_chat_boost"):
        event = getattr(update, name, None)
        if event is not None:
            source = getattr(getattr(event, "boost", event), "source", None)
            add(getattr(source, "user", None))

    return list(found.values())


def group_chats(update: Any) -> list[int]:
    """Ids of the groups an update came from."""
    chats: dict[int, None] = {}

    def add(chat: Any) -> None:
        if chat is None:
            return
        if getattr(chat.type, "value", chat.type) in ("group", "supergroup"):
            chats[chat.id] = None

    for name in ("message", "edited_message", "my_chat_member", "chat_member", "chat_join_request", "message_reaction"):
        event = getattr(update, name, None)
        if event is not None:
            add(getattr(event, "chat", None))
    callback = getattr(update, "callback_query", None)
    if callback is not None:
        add(getattr(getattr(callback, "message", None), "chat", None))
    return list(chats)


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

    await database.execute(
        UPSERT,
        user_id,
        name,
        username,
        bool(getattr(user, "is_bot", False)),
        getattr(user, "first_name", None) or None,
        getattr(user, "last_name", None) or None,
    )

    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[user_id] = (name, username, now)


async def remember_all(users: Iterable[Any]) -> None:
    for user in users:
        await remember(user)


# --- Group administrators ---------------------------------------------------

_admins_checked: dict[int, float] = {}
_admin_tasks: set[asyncio.Task] = set()


def schedule_admin_refresh(bot: Any, chat_id: int, force: bool = False) -> None:
    """
    Record the administrators of a group, in the background.

    A bot cannot list ordinary members, but Telegram does tell it who the admins
    are. Done at most every few hours per group (or right away when `force` is
    set, e.g. when the bot has just been added), and never delays an update.
    """
    now = time.monotonic()
    last = _admins_checked.get(chat_id)
    if not force and last is not None and now - last < ADMIN_REFRESH_SECONDS:
        return

    if len(_admins_checked) >= CACHE_LIMIT:
        _admins_checked.clear()
    _admins_checked[chat_id] = now  # marked now so a burst of updates starts only one request

    task = asyncio.get_running_loop().create_task(_refresh_admins(bot, chat_id))
    _admin_tasks.add(task)
    task.add_done_callback(_admin_tasks.discard)


async def _refresh_admins(bot: Any, chat_id: int) -> None:
    try:
        admins = await bot.get_chat_administrators(chat_id)
        await remember_all(admin.user for admin in admins)
    except TelegramAPIError as exc:
        log.debug("Could not read the administrators of %s: %s", chat_id, exc)
    except Exception:
        log.debug("Could not record the administrators of %s", chat_id, exc_info=True)


# --- Reading ----------------------------------------------------------------

async def get(user_id: int) -> dict[str, Any] | None:
    """The stored record of a user id, or None if the bot never met them."""
    return await database.fetch_one(
        "SELECT user_id, name, first_name, last_name, username, is_bot, first_seen, last_seen "
        "FROM users WHERE user_id = $1",
        user_id,
    )


async def find_by_username(username: str) -> dict[str, Any] | None:
    """The user currently known under @username (case does not matter)."""
    return await database.fetch_one(
        "SELECT user_id, name, first_name, last_name, username, is_bot, first_seen, last_seen "
        "FROM users WHERE lower(username) = lower($1)",
        username.lstrip("@"),
    )


async def name_of(user_id: int) -> str | None:
    row = await get(user_id)
    return row["name"] if row else None
