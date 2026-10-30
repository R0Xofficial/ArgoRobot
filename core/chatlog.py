"""
Chat logs: a chat's moderation actions, copied to a chat the admins chose (/setlog).

Every moderation command calls moderation.log_action(), which ends up in record() here.
A chat without a log costs one cached lookup. Sending never raises: a log that can't be
reached must not break a command.
"""

import logging
import time

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError
from aiogram.types import LinkPreviewOptions

from core import database
from core.text import esc
from core.users import CACHE_LIMIT

log = logging.getLogger(__name__)

# chat_id -> log chat id, or None when the chat has no log
_cache: dict[int, int | None] = {}
_complained: dict[int, float] = {}
COMPLAIN_EVERY = 3600.0


async def get(chat_id: int) -> int | None:
    """The chat that receives this chat's log, or None."""
    if chat_id in _cache:
        return _cache[chat_id]
    row = await database.fetch_one("SELECT log_chat_id FROM chat_logs WHERE chat_id = $1", chat_id)
    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[chat_id] = row["log_chat_id"] if row else None
    return _cache[chat_id]


async def set_log(chat_id: int, log_chat_id: int, set_by: int | None = None) -> None:
    await database.execute(
        "INSERT INTO chat_logs (chat_id, log_chat_id, set_by) VALUES ($1, $2, $3) "
        "ON CONFLICT (chat_id) DO UPDATE SET log_chat_id = EXCLUDED.log_chat_id, set_by = EXCLUDED.set_by",
        chat_id,
        log_chat_id,
        set_by,
    )
    _cache[chat_id] = log_chat_id


async def unset_log(chat_id: int) -> bool:
    row = await database.fetch_one("DELETE FROM chat_logs WHERE chat_id = $1 RETURNING chat_id", chat_id)
    _cache[chat_id] = None
    return row is not None


def forget(chat_id: int) -> None:
    _cache.pop(chat_id, None)


def entry(
    tag: str,
    chat,
    admin: str | None = None,
    user: str | None = None,
    reason: str = "",
    details: tuple[tuple[str, str], ...] = (),
) -> str:
    """
        #BANNED
        Chat: title [id]
        Admin: name
        User: name [id]
        Reason: ...
    Admin, user and details are HTML made by the caller; the reason is plain text.
    """
    title = esc(getattr(chat, "title", None) or "Chat")
    lines = [f"<b>#{tag}</b>", f"<b>Chat:</b> {title} [<code>{chat.id}</code>]"]
    if admin:
        lines.append(f"<b>Admin:</b> {admin}")
    if user:
        lines.append(f"<b>User:</b> {user}")
    for label, value in details:
        lines.append(f"<b>{esc(label)}:</b> {value}")
    if reason.strip():
        lines.append(f"<b>Reason:</b> {esc(reason.strip()[:300])}")
    return "\n".join(lines)


async def record(
    bot: Bot,
    chat,
    tag: str,
    admin: str | None = None,
    user: str | None = None,
    reason: str = "",
    details: tuple[tuple[str, str], ...] = (),
) -> bool:
    """Send one entry to the chat's log, if it has one. Returns whether something was sent."""
    try:
        target = await get(chat.id)
    except Exception:
        log.exception("Could not read the log of %s", chat.id)
        return False
    if target is None:
        return False
    try:
        await bot.send_message(
            chat_id=target,
            text=entry(tag, chat, admin, user, reason, details),
            link_preview_options=LinkPreviewOptions(is_disabled=True),
        )
        return True
    except TelegramAPIError as exc:
        now = time.monotonic()
        if now - _complained.get(chat.id, -COMPLAIN_EVERY) >= COMPLAIN_EVERY:
            _complained[chat.id] = now
            log.warning("Could not write the log of %s to %s: %s", chat.id, target, exc)
        return False
