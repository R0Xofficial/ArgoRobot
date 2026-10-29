"""Delete messages in bulk"""

import logging
from contextlib import suppress

from aiogram import Router
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.types import Message

from core import moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, ModerationError, perform
from core.roles import Role
from core.text import esc

log = logging.getLogger(__name__)

ABOUT = """
Clean up a chat. /del removes one message, /purge removes a whole stretch of them: everything from the message you reply to down to your command.

These are for chat admins with the right to delete messages, and I need that right too. Telegram only lets me delete messages that are less than 48 hours old, and in one go I clear at most 1000 messages. Reply to a newer message if the stretch is longer.
"""

HELP = [
    ("/del", "Delete the message you reply to, and your command with it. /delete does the same.", "admins"),
    ("/purge", "Delete every message from the one you reply to down to your command. I confirm when it is done.", "admins"),
    ("/spurge", "Like /purge, but silent: nothing is said afterwards.", "admins"),
]

router = Router(name="purges")

RIGHT = "can_delete_messages"
RIGHTS_REFUSED = "I can't delete messages here. Make me an admin with permission to delete messages."
MAX_PURGE = 1000       # messages cleared by one command
BATCH = 100            # Telegram deletes at most this many messages per request


async def _begin(message: Message, role: Role | None):
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return None
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=RIGHT)


@router.message(cmd("del", "delete"))
async def cmd_del(message: Message, role: Role | None = None) -> None:
    if await _begin(message, role) is None:
        return
    target = await moderation.require_reply(message)
    if target is None:
        return
    try:
        await perform(message.bot.delete_message(message.chat.id, target.message_id), RIGHTS_REFUSED)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest:
        await reply(message, "I can't delete that message. It may be gone already, or too old.")
        return
    with suppress(TelegramAPIError):
        await message.delete()  # the command itself


async def _purge(message: Message, role: Role | None, silent: bool) -> None:
    if await _begin(message, role) is None:
        return
    target = await moderation.require_reply(message)
    if target is None:
        return
    first, last = target.message_id, message.message_id
    if first >= last:
        await reply(message, "Reply to a message that came before your command.")
        return
    count = last - first + 1
    if count > MAX_PURGE:
        await reply(message, f"That is too many messages: I clear at most {MAX_PURGE} at a time. Reply to a newer message.")
        return

    ids = list(range(first, last + 1))
    try:
        for start in range(0, len(ids), BATCH):
            await perform(message.bot.delete_messages(message.chat.id, ids[start : start + BATCH]), RIGHTS_REFUSED)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        log.info("Purge in chat %s failed: %s", message.chat.id, exc)
        await reply(message, "I couldn't delete those messages. They may be too old.")
        return
    if not silent:
        await reply(message, f"Purge complete: up to {count} messages deleted.", quote=False)


@router.message(cmd("purge"))
async def cmd_purge(message: Message, role: Role | None = None) -> None:
    await _purge(message, role, silent=False)


@router.message(cmd("spurge"))
async def cmd_spurge(message: Message, role: Role | None = None) -> None:
    await _purge(message, role, silent=True)
