"""Ban commands for chat admins"""

from contextlib import suppress
from datetime import datetime, timezone

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = """
Remove people from the chat, for good, for a while, or just to show them the door.

These commands are for chat admins with the right to ban users. My staff (Sudo and up) can use them too, unless the chat owner turned that off with /staffhelp.

Name the target by replying to their message, or with a @username or a numeric ID. Anything after it is the reason, which is shown in the answer.

I won't act against chat admins, my own staff or protected accounts, and I need the right to restrict users myself.

Times look like 30m, 2h, 3d or 1w, from 1 minute to 365 days.
"""

HELP_WHO = "admins"

HELP = [
    ("/ban <user> [reason]", "Ban a user until someone unbans them. Also works on channels that write into the chat."),
    ("/dban <user> [reason]", "Reply to a message: ban its author and delete that message."),
    ("/tban <user> <time> [reason]", "Ban a user for a limited time. They can come back when it ends."),
    ("/unban <user>", "Lift a ban so the user can join again."),
    ("/kick <user> [reason]", "Remove a user from the chat. They are not banned and can return with an invite link."),
]

router = Router(name="bans")


async def _ban(bot, chat_id: int, target) -> None:
    """Ban a user, or a channel that writes into the chat."""
    if target.is_chat:
        await perform(bot.ban_chat_sender_chat(chat_id, target.user_id))
    else:
        await perform(bot.ban_chat_member(chat_id, target.user_id))


async def _timed_ban(bot, chat_id: int, user_id: int, duration) -> None:
    until = datetime.now(timezone.utc) + duration
    await perform(bot.ban_chat_member(chat_id, user_id, until_date=until))


@router.message(cmd("ban", "dban"))
async def cmd_ban(message: Message, command: CommandObject, role: Role | None = None) -> None:
    deleting = command.command == "dban"

    started = await moderation.begin(message, command, role, allow_chats=True)
    if started is None:
        return
    target, actor = started

    replied = await moderation.require_reply(message) if deleting else None
    if deleting and replied is None:
        return

    try:
        await _ban(message.bot, message.chat.id, target)
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    if deleting:
        # Best effort: the message may already be gone.
        with suppress(TelegramAPIError):
            await message.bot.delete_message(message.chat.id, replied.message_id)

    await reply(
        message,
        f"{moderation.who(target)} was banned{moderation.by(actor)}"
        f"{moderation.reason_line(target.extra)}{moderation.staff_note(actor)}",
    )


@router.message(cmd("tban"))
async def cmd_tban(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role)
    if started is None:
        return
    target, actor = started

    try:
        duration, reason = moderation.split_duration(target.extra)
        await _timed_ban(message.bot, message.chat.id, target.user_id, duration)
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await reply(
        message,
        f"{moderation.who(target)} was banned for {moderation.format_duration(duration)}{moderation.by(actor)}"
        f"{moderation.reason_line(reason)}{moderation.staff_note(actor)}",
    )


@router.message(cmd("unban"))
async def cmd_unban(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role, allow_chats=True, punitive=False)
    if started is None:
        return
    target, actor = started

    if not target.is_chat:
        status = await moderation.member_status(message.bot, message.chat.id, target.user_id)
        if status in ("creator", "administrator"):
            await reply(message, "I can't take action against a chat admin.")
            return
        if status is not None and status != "kicked":
            await reply(message, f"{moderation.who(target)} isn't banned.")
            return

    try:
        if target.is_chat:
            await perform(message.bot.unban_chat_sender_chat(message.chat.id, target.user_id))
        else:
            await perform(message.bot.unban_chat_member(message.chat.id, target.user_id, only_if_banned=True))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await reply(message, f"{moderation.who(target)} was unbanned{moderation.by(actor)}{moderation.staff_note(actor)}")


@router.message(cmd("kick"))
async def cmd_kick(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role)
    if started is None:
        return
    target, actor = started

    try:
        # A ban followed by an unban removes the user but lets them come back.
        await perform(message.bot.ban_chat_member(message.chat.id, target.user_id))
        await perform(message.bot.unban_chat_member(message.chat.id, target.user_id, only_if_banned=True))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await reply(
        message,
        f"{moderation.who(target)} was kicked{moderation.by(actor)}"
        f"{moderation.reason_line(target.extra)}{moderation.staff_note(actor)}",
    )
