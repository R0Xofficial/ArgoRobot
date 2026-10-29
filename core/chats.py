"""
Chats the bot is in, and where each user has been seen.

Two tables (see core/database.py):

  chats       groups and channels the bot is a member of
  user_chats  (user, chat) pairs: in which chats a person wrote, joined, left,
              reacted, pressed a button ...

Both are filled by UsersMiddleware from every update. They are the groundwork
for global bans and federations: "which of my chats is this person in?".

A chat is dropped (together with its user_chats rows) when the bot learns it
was removed: right away from a my_chat_member update, and by a cleanup that asks
Telegram about every stored chat. The cleanup runs on a schedule (the result
goes to the developer log) and by hand with /cleanchats (the result goes to
whoever ran it). It only removes a chat on definite evidence; when Telegram
cannot be reached the chat is kept and reported as unverified.

Like the phone book, writes are cached in memory so a busy group does not turn
into a stream of database queries.
"""

import asyncio
import logging
import time
from dataclasses import dataclass, field
from typing import Any

from aiogram import Bot
from aiogram.exceptions import (
    TelegramAPIError,
    TelegramBadRequest,
    TelegramForbiddenError,
    TelegramMigrateToChat,
    TelegramRetryAfter,
)

from core import blacklist, chatsettings, database, filterstore, flood, joinblacklist, locks, logs
from core.errors import report_exception
from core.text import esc
from core.users import CACHE_LIMIT, IGNORED_IDS, REFRESH_SECONDS

log = logging.getLogger(__name__)

CHAT_TYPES = ("group", "supergroup", "channel")
MEMBER_CHAT_TYPES = ("group", "supergroup")  # chats where people take part

CHECK_DELAY = 0.1            # seconds between Telegram calls while cleaning (about 10 a second)
FIRST_CLEANUP_DELAY = 600    # the first scheduled cleanup, after start-up

# chat_id -> (type, title, username, bot_admin, time of the last write)
_chats: dict[int, tuple[str, str | None, str | None, bool | None, float]] = {}
# (user_id, chat_id) -> time of the last write
_presence: dict[tuple[int, int], float] = {}


def _value(thing: Any) -> Any:
    """Enum members and plain strings alike."""
    return getattr(thing, "value", thing)


# --- Collecting from an update ------------------------------------------------

def chats_in(update: Any) -> list[Any]:
    """The groups and channels an update comes from."""
    found: dict[int, Any] = {}

    def add(chat: Any) -> None:
        if chat is not None and _value(chat.type) in CHAT_TYPES:
            found.setdefault(chat.id, chat)

    for name in ("message", "edited_message", "my_chat_member", "chat_member", "chat_join_request", "message_reaction"):
        event = getattr(update, name, None)
        if event is not None:
            add(getattr(event, "chat", None))
    callback = getattr(update, "callback_query", None)
    if callback is not None:
        add(getattr(getattr(callback, "message", None), "chat", None))
    return list(found.values())


def _present_in_message(message: Any, add) -> None:
    chat = message.chat
    add(message.from_user, chat)
    for member in getattr(message, "new_chat_members", None) or []:
        add(member, chat)
    add(getattr(message, "left_chat_member", None), chat)
    reply = getattr(message, "reply_to_message", None)
    if reply is not None:
        add(getattr(reply, "from_user", None), chat)


def presence(update: Any) -> list[tuple[int, int]]:
    """
    (user_id, chat_id) pairs: people who were in a group, going by an update.

    Only people certain to have been there count: whoever wrote, was replied to,
    joined or left. Someone merely mentioned or forwarded is not necessarily in the group.
    """
    pairs: dict[tuple[int, int], None] = {}

    def add(user: Any, chat: Any) -> None:
        user_id = getattr(user, "id", None)
        if user_id is None or user_id in IGNORED_IDS or chat is None:
            return
        if _value(chat.type) in MEMBER_CHAT_TYPES:
            pairs[(user_id, chat.id)] = None

    for name in ("message", "edited_message"):
        message = getattr(update, name, None)
        if message is not None:
            _present_in_message(message, add)

    callback = getattr(update, "callback_query", None)
    if callback is not None:
        add(callback.from_user, getattr(getattr(callback, "message", None), "chat", None))

    for name in ("chat_member", "my_chat_member"):
        event = getattr(update, name, None)
        if event is not None:
            add(event.from_user, event.chat)
            add(event.new_chat_member.user, event.chat)

    reaction = getattr(update, "message_reaction", None)
    if reaction is not None:
        add(getattr(reaction, "user", None), reaction.chat)

    return list(pairs)


# --- Writing ------------------------------------------------------------------

UPSERT_CHAT = """
INSERT INTO chats (chat_id, type, title, username, bot_admin)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (chat_id) DO UPDATE SET
    type = EXCLUDED.type,
    title = EXCLUDED.title,
    username = EXCLUDED.username,
    bot_admin = COALESCE(EXCLUDED.bot_admin, chats.bot_admin),
    last_seen = now()
"""

UPSERT_PRESENCE = """
INSERT INTO user_chats (user_id, chat_id)
VALUES ($1, $2)
ON CONFLICT (user_id, chat_id) DO UPDATE SET last_seen = now()
"""

# A data-modifying CTE runs even though nothing reads from it.
DELETE_CHAT = """
WITH gone AS (DELETE FROM user_chats WHERE chat_id = $1),
     bans AS (DELETE FROM gban_bans WHERE chat_id = $1)
DELETE FROM chats WHERE chat_id = $1
"""


def _forget(chat_id: int) -> None:
    """Drop the in-memory copies, so a chat the bot rejoins is written again."""
    _chats.pop(chat_id, None)
    for key in [key for key in _presence if key[1] == chat_id]:
        del _presence[key]


async def remember_chat(chat: Any, bot_admin: bool | None = None) -> None:
    kind = _value(chat.type)
    title = getattr(chat, "title", None) or None
    username = getattr(chat, "username", None) or None
    now = time.monotonic()

    cached = _chats.get(chat.id)
    if (
        cached
        and cached[:3] == (kind, title, username)
        and (bot_admin is None or cached[3] == bot_admin)
        and now - cached[4] < REFRESH_SECONDS
    ):
        return

    await database.execute(UPSERT_CHAT, chat.id, kind, title, username, bot_admin)

    if len(_chats) >= CACHE_LIMIT:
        _chats.clear()
    known_admin = bot_admin if bot_admin is not None else (cached[3] if cached else None)
    _chats[chat.id] = (kind, title, username, known_admin, now)


async def remember_presence(user_id: int, chat_id: int) -> None:
    now = time.monotonic()
    last = _presence.get((user_id, chat_id))
    if last is not None and now - last < REFRESH_SECONDS:
        return
    await database.execute(UPSERT_PRESENCE, user_id, chat_id)
    if len(_presence) >= CACHE_LIMIT:
        _presence.clear()
    _presence[(user_id, chat_id)] = now


async def remove_chat(chat_id: int) -> None:
    """Forget a chat and everything recorded about who was in it."""
    _forget(chat_id)
    await database.execute(DELETE_CHAT, chat_id)


async def migrate_chat(old_id: int, new_id: int) -> None:
    """A group became a supergroup: its id changed, but it is the same chat."""
    _forget(old_id)
    _forget(new_id)
    await database.execute(
        """
        INSERT INTO chats (chat_id, type, title, username, bot_admin, first_seen)
        SELECT $2, 'supergroup', title, username, bot_admin, first_seen FROM chats WHERE chat_id = $1
        ON CONFLICT (chat_id) DO NOTHING
        """,
        old_id,
        new_id,
    )
    await database.execute(
        "UPDATE user_chats SET chat_id = $2 WHERE chat_id = $1 "
        "AND user_id NOT IN (SELECT user_id FROM user_chats WHERE chat_id = $2)",
        old_id,
        new_id,
    )
    # Settings, warns and approvals follow the chat to its new id.
    for table in ("chat_settings", "warn_settings", "warns", "approvals", "gban_bans", "flood_settings", "blacklist", "blacklist_settings", "filters", "greetings", "join_blacklist", "notes", "rules", "locks", "lock_settings", "lock_allowlist"):
        await database.execute(f"UPDATE {table} SET chat_id = $2 WHERE chat_id = $1", old_id, new_id)
    chatsettings.forget(old_id)
    flood.forget(old_id)
    blacklist.forget(old_id)
    filterstore.forget(old_id)
    joinblacklist.forget(old_id)
    locks.forget(old_id)
    await remove_chat(old_id)


async def record(update: Any) -> None:
    """Everything an update tells us about chats and who was in them."""
    # The bot was removed: forget the chat instead of recording it.
    removed: set[int] = set()
    event = getattr(update, "my_chat_member", None)
    if event is not None and _value(event.new_chat_member.status) in ("left", "kicked"):
        removed.add(event.chat.id)
        await remove_chat(event.chat.id)

    message = getattr(update, "message", None)
    migrated_to = getattr(message, "migrate_to_chat_id", None) if message is not None else None
    if migrated_to:
        removed.add(message.chat.id)
        await migrate_chat(message.chat.id, migrated_to)

    for chat in chats_in(update):
        if chat.id in removed:
            continue
        bot_admin = None
        if event is not None and event.chat.id == chat.id:
            status = _value(event.new_chat_member.status)
            bot_admin = status in ("administrator", "creator")
        await remember_chat(chat, bot_admin)

    for user_id, chat_id in presence(update):
        if chat_id not in removed:
            await remember_presence(user_id, chat_id)


# --- Reading ------------------------------------------------------------------

async def all_chats() -> list[dict[str, Any]]:
    return await database.fetch_all("SELECT chat_id, type, title, username, bot_admin FROM chats ORDER BY chat_id")


async def chats_of_user(user_id: int) -> list[dict[str, Any]]:
    """Chats the bot has seen this user in."""
    return await database.fetch_all(
        "SELECT c.chat_id, c.type, c.title, c.username, c.bot_admin, uc.first_seen, uc.last_seen "
        "FROM user_chats uc JOIN chats c ON c.chat_id = uc.chat_id "
        "WHERE uc.user_id = $1 ORDER BY uc.last_seen DESC",
        user_id,
    )


# --- Cleanup ------------------------------------------------------------------

@dataclass
class CleanupResult:
    checked: int = 0
    removed: list[dict[str, Any]] = field(default_factory=list)
    moved: list[tuple[dict[str, Any], int]] = field(default_factory=list)
    unverified: int = 0


_lock = asyncio.Lock()


def is_cleaning() -> bool:
    return _lock.locked()


async def _bot_status(bot: Bot, chat_id: int, bot_id: int) -> tuple[str, Any]:
    """
    Whether the bot is still in a chat: ("present", is_admin), ("gone", None),
    ("moved", new_chat_id) or ("unknown", None). Only definite answers count.
    """
    for attempt in (1, 2):
        try:
            member = await bot.get_chat_member(chat_id, bot_id)
        except TelegramRetryAfter as exc:
            if attempt == 2:
                return "unknown", None
            await asyncio.sleep(min(float(exc.retry_after), 60))
            continue
        except TelegramMigrateToChat as exc:
            return "moved", exc.migrate_to_chat_id
        except TelegramForbiddenError:
            return "gone", None  # kicked, blocked or the chat no longer exists
        except TelegramBadRequest as exc:
            problem = str(exc).lower()
            if "chat not found" in problem or "not a member" in problem:
                return "gone", None
            return "unknown", None
        except TelegramAPIError:
            return "unknown", None  # network trouble, server errors, ...

        status = _value(member.status)
        if status in ("left", "kicked"):
            return "gone", None
        return "present", status in ("administrator", "creator")
    return "unknown", None


async def cleanup(bot: Bot) -> CleanupResult | None:
    """
    Check every stored chat and drop those the bot is no longer in.

    Returns None when another cleanup is already running.
    """
    if _lock.locked():
        return None
    async with _lock:
        result = CleanupResult()
        me = await bot.me()

        for row in await all_chats():
            result.checked += 1
            state, detail = await _bot_status(bot, row["chat_id"], me.id)

            if state == "gone":
                await remove_chat(row["chat_id"])
                result.removed.append(row)
            elif state == "moved":
                await migrate_chat(row["chat_id"], detail)
                result.moved.append((row, detail))
            elif state == "present":
                await database.execute(
                    "UPDATE chats SET bot_admin = $2, last_seen = now() WHERE chat_id = $1",
                    row["chat_id"],
                    detail,
                )
            else:
                result.unverified += 1
            await asyncio.sleep(CHECK_DELAY)

        return result


def _chat_line(row: dict[str, Any], extra: str = "") -> str:
    title = esc(row.get("title") or "Untitled")
    return f"{title} [<code>{row['chat_id']}</code>]{extra}"


def report(result: CleanupResult, title: str) -> str:
    lines = [
        f"<b>{esc(title)}</b>",
        "",
        f"Checked: <code>{result.checked}</code>",
        f"Removed: <code>{len(result.removed)}</code>",
    ]
    if result.moved:
        lines.append(f"Moved to a new id: <code>{len(result.moved)}</code>")
    if result.unverified:
        lines.append(f"Could not be verified: <code>{result.unverified}</code>")

    shown = 15
    if result.removed:
        lines += ["", "<b>Removed chats:</b>"]
        lines += [_chat_line(row) for row in result.removed[:shown]]
        if len(result.removed) > shown:
            lines.append(f"... and {len(result.removed) - shown} more.")
    return "\n".join(lines)


async def _scheduled_cleanups(bot: Bot, hours: float) -> None:
    await asyncio.sleep(FIRST_CLEANUP_DELAY)
    while True:
        try:
            result = await cleanup(bot)
            if result is not None:
                log.info(
                    "Chat cleanup: checked %d, removed %d, unverified %d",
                    result.checked,
                    len(result.removed),
                    result.unverified,
                )
                await logs.dev_log(bot, report(result, "Automatic chat cleanup"))
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            # One bad run must not end the schedule. The traceback goes to the developer log as a file.
            log.error("Scheduled chat cleanup failed: %r", exc)
            await report_exception(bot, exc)
        await asyncio.sleep(hours * 3600)


_task: asyncio.Task | None = None


def start_cleanup_schedule(bot: Bot, hours: float) -> None:
    """Start the scheduled cleanup. `hours` <= 0 turns it off."""
    global _task
    if hours <= 0 or _task is not None:
        return
    _task = asyncio.get_running_loop().create_task(_scheduled_cleanups(bot, hours), name="chat-cleanup")


async def stop_cleanup_schedule() -> None:
    global _task
    if _task is None:
        return
    _task.cancel()
    try:
        await _task
    except asyncio.CancelledError:
        pass
    _task = None
