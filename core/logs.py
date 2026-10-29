"""
Log destinations.

  * Developer log (DEV_LOG_CHAT): bot start/stop and error reports (tracebacks).
    Private, meant for the people running the bot. Falls back to the owner's PM.
  * User log (LOG_CHAT_ID): promotions and demotions of staff and, later, other
    public information. Meant to be a group anyone can read. Falls back to the
    developer log.

Sending never raises: a missing log chat must not break a command.
"""

import logging
from datetime import datetime, timezone

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError
from aiogram.types import LinkPreviewOptions

from config import settings
from core.text import user_ref

log = logging.getLogger(__name__)


def dev_target() -> tuple[int, int | None]:
    """(chat_id, thread_id) of the developer log."""
    if settings.DEV_LOG_CHAT:
        return settings.DEV_LOG_CHAT, settings.DEV_LOG_THREAD_ID
    return settings.OWNER_ID, None


def role_target() -> tuple[int, int | None]:
    """(chat_id, thread_id) of the user log."""
    if settings.LOG_CHAT_ID:
        return settings.LOG_CHAT_ID, settings.LOG_THREAD_ID
    return dev_target()


async def _send(
    bot: Bot, target: tuple[int, int | None], text: str, what: str, reply_markup=None
) -> bool:
    chat_id, thread_id = target
    try:
        await bot.send_message(
            chat_id,
            text,
            message_thread_id=thread_id,
            reply_markup=reply_markup,
            link_preview_options=LinkPreviewOptions(is_disabled=True),
        )
        return True
    except TelegramAPIError as exc:
        log.warning("Could not send %s to %s: %s", what, chat_id, exc)
        return False


async def dev_log(bot: Bot, text: str) -> bool:
    """Send a message to the developer log."""
    return await _send(bot, dev_target(), text, "developer log message")


async def role_log(bot: Bot, text: str, reply_markup=None) -> bool:
    """Send a message to the user log (role changes, global bans ...), optionally with buttons."""
    return await _send(bot, role_target(), text, "role log message", reply_markup)


def timestamp(when: datetime | None = None) -> str:
    """A time as shown in the logs: 2026-10-29 18:09:00 UTC, in a <code> tag."""
    when = (when or datetime.now(timezone.utc)).astimezone(timezone.utc)
    return f"<code>{when:%Y-%m-%d %H:%M:%S} UTC</code>"


def audit_text(tag: str, user_id: int, name: str | None, admin) -> str:
    """
    The user log entry for something an admin did to a user (a role change, a
    whitelist change ...):

        #TAG
        User: name [id]
        Timestamp: ...
        Admin: name [id]
    """
    return (
        f"<b>{tag}</b>\n"
        f"<b>User:</b> {user_ref(user_id, name)}\n"
        f"<b>Timestamp:</b> {timestamp()}\n"
        f"<b>Admin:</b> {user_ref(admin.id, admin.full_name)}"
    )
