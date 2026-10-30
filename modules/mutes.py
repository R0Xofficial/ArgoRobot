"""Mute commands for chat admins"""

from contextlib import suppress
from datetime import datetime, timezone

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import ChatPermissions, Message

from core import moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = """
Take away someone's right to write without removing them from the chat.

These commands are for chat admins with the right to ban users. My staff (Sudo and up) can use them too, unless the chat owner turned that off with /staffhelp.

Name the target by replying to their message, or with a @username or a numeric ID. Anything after it is the reason, which is shown in the answer.

I won't act against chat admins, my own staff or protected accounts, and I need the right to restrict users myself.

Times look like 30m, 2h, 3d or 1w, from 1 minute to 365 days.
"""

HELP_WHO = "admins"

HELP = [
    ("/mute <user> [reason]", "Stop a user from writing until someone unmutes them."),
    ("/tmute <user> <time> [reason]", "Mute a user for a limited time. They can write again when it ends."),
    ("/unmute <user>", "Give a muted user back the right to write."),
    ("/smute <user> [reason]", "Silent mute: mutes, deletes your command and says nothing."),
    ("/stmute <user> <time> [reason]", "Silent timed mute. Alias: /stempmute."),
    ("/sunmute <user>", "Silent unmute."),
]

router = Router(name="mutes")

MUTED = ChatPermissions(can_send_messages=False)


async def _default_permissions(bot, chat_id: int) -> ChatPermissions:
    """What every ordinary member of the chat may do."""
    try:
        permissions = (await bot.get_chat(chat_id)).permissions
    except TelegramAPIError:
        permissions = None
    return permissions or ChatPermissions(can_send_messages=True)


async def _silently(message: Message) -> None:
    """Delete the command: silent actions leave no trace."""
    with suppress(TelegramAPIError):
        await message.bot.delete_message(message.chat.id, message.message_id)


@router.message(cmd("mute", "smute"))
async def cmd_mute(message: Message, command: CommandObject, role: Role | None = None) -> None:
    silent = command.command.lower() == "smute"
    started = await moderation.begin(message, command, role)
    if started is None:
        return
    target, actor = started

    try:
        await perform(message.bot.restrict_chat_member(message.chat.id, target.user_id, permissions=MUTED))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await moderation.log_action(message, "MUTED", actor, target, target.extra, *((("Silent", "yes"),) if silent else ()))
    if silent:
        await _silently(message)
        return
    await reply(
        message,
        f"{moderation.who(target)} was muted{moderation.by(actor)}"
        f"{moderation.reason_line(target.extra)}{moderation.staff_note(actor)}",
    )


@router.message(cmd("tmute", "stmute", "stempmute"))
async def cmd_tmute(message: Message, command: CommandObject, role: Role | None = None) -> None:
    silent = command.command.lower() in ("stmute", "stempmute")
    started = await moderation.begin(message, command, role)
    if started is None:
        return
    target, actor = started

    try:
        duration, reason = moderation.split_duration(target.extra)
        until = datetime.now(timezone.utc) + duration
        await perform(
            message.bot.restrict_chat_member(
                message.chat.id, target.user_id, permissions=MUTED, until_date=until
            )
        )
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await moderation.log_action(message, "TMUTED", actor, target, reason, ("Duration", moderation.format_duration(duration)), *((("Silent", "yes"),) if silent else ()))
    if silent:
        await _silently(message)
        return
    await reply(
        message,
        f"{moderation.who(target)} was muted for {moderation.format_duration(duration)}{moderation.by(actor)}"
        f"{moderation.reason_line(reason)}{moderation.staff_note(actor)}",
    )


@router.message(cmd("unmute", "sunmute"))
async def cmd_unmute(message: Message, command: CommandObject, role: Role | None = None) -> None:
    silent = command.command.lower() == "sunmute"
    started = await moderation.begin(message, command, role, punitive=False)
    if started is None:
        return
    target, actor = started

    member = await moderation.get_member(message.bot, message.chat.id, target.user_id)
    if member is not None:
        status = getattr(member.status, "value", member.status)
        if status in ("creator", "administrator"):
            await reply(message, "I can't take action against a chat admin.")
            return
        # Muted means restricted without the right to write.
        if status != "restricted" or getattr(member, "can_send_messages", False) is not False:
            await reply(message, f"{moderation.who(target)} isn't muted.")
            return

    try:
        permissions = await _default_permissions(message.bot, message.chat.id)
        await perform(message.bot.restrict_chat_member(message.chat.id, target.user_id, permissions=permissions))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await moderation.log_action(message, "UNMUTED", actor, target, "", *((("Silent", "yes"),) if silent else ()))
    if silent:
        await _silently(message)
        return
    await reply(message, f"{moderation.who(target)} was unmuted{moderation.by(actor)}{moderation.staff_note(actor)}")
