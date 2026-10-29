"""
Log destinations.

  * Developer log (DEV_LOG_CHAT): bot start/stop and error reports. Private,
    meant for the people running the bot. Falls back to the owner's PM.
  * Role log (ROLE_LOG_CHAT): promotions and demotions of staff. Meant to be a
    public group anyone can read. Falls back to the developer log.

Sending never raises: a missing log chat must not break a command.
"""

import logging

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError

from config import settings

log = logging.getLogger(__name__)


def dev_target() -> tuple[int, int | None]:
    """(chat_id, thread_id) of the developer log."""
    if settings.DEV_LOG_CHAT:
        return settings.DEV_LOG_CHAT, settings.DEV_LOG_THREAD_ID
    return settings.OWNER_ID, None


def role_target() -> tuple[int, int | None]:
    """(chat_id, thread_id) of the public role log."""
    if settings.ROLE_LOG_CHAT:
        return settings.ROLE_LOG_CHAT, settings.ROLE_LOG_THREAD_ID
    return dev_target()


async def _send(bot: Bot, target: tuple[int, int | None], text: str, what: str) -> bool:
    chat_id, thread_id = target
    try:
        await bot.send_message(
            chat_id,
            text,
            message_thread_id=thread_id,
        )
        return True
    except TelegramAPIError as exc:
        log.warning("Could not send %s to %s: %s", what, chat_id, exc)
        return False


async def dev_log(bot: Bot, text: str) -> bool:
    """Send a message to the developer log."""
    return await _send(bot, dev_target(), text, "developer log message")


async def role_log(bot: Bot, text: str) -> bool:
    """Send a message to the public role log."""
    return await _send(bot, role_target(), text, "role log message")
