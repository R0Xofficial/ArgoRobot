"""Delete messages in bulk"""

import logging
from contextlib import suppress

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.types import Message

from core import moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, ModerationError, perform
from core.roles import Role
from core.text import esc
from core.i18n import tr

log = logging.getLogger(__name__)

ABOUT = "purges.about"

HELP = [
    ("/del", "purges.help.delete_the_message_you_reply", "common.admins"),
    ("/purge [reason]", "purges.help.delete_every_message_from_the", "common.admins"),
    ("/spurge", "purges.help.like_purge_but_silent_nothing", "common.admins"),
]

router = Router(name="purges")

RIGHT = "can_delete_messages"
RIGHTS_REFUSED = "purges.rights_refused"
MAX_PURGE = 1000       # messages cleared by one command
MAX_REASON = 500       # characters of a reason that are shown
BATCH = 100            # Telegram deletes at most this many messages per request


async def _begin(message: Message, role: Role | None):
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return None
    return await moderation.begin_chat_command(message, role, right=RIGHT)


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
        await reply(message, tr("purges.i_can_t_delete_that_message"))
        return
    with suppress(TelegramAPIError):
        await message.delete()  # the command itself


async def _purge(message: Message, role: Role | None, silent: bool, reason: str | None = None) -> None:
    if await _begin(message, role) is None:
        return
    target = await moderation.require_reply(message)
    if target is None:
        return
    first, last = target.message_id, message.message_id
    if first >= last:
        await reply(message, tr("purges.reply_to_a_message_that_came"))
        return
    count = last - first + 1
    if count > MAX_PURGE:
        await reply(message, tr("purges.that_is_too_many_messages_i", MAX_PURGE=MAX_PURGE))
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
        await reply(message, tr("purges.i_couldn_t_delete_those_messages"))
        return
    if not silent:
        text = tr("purges.purge_complete_up_to_messages_deleted", count=count)
        if reason:
            text += tr("purges.reason", v=esc(reason[:MAX_REASON]))
        await reply(message, text, quote=False)


@router.message(cmd("purge"))
async def cmd_purge(message: Message, role: Role | None = None, command: CommandObject | None = None) -> None:
    reason = ((command.args if command else None) or "").strip() or None
    await _purge(message, role, silent=False, reason=reason)


@router.message(cmd("spurge"))
async def cmd_spurge(message: Message, role: Role | None = None) -> None:
    await _purge(message, role, silent=True)
