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
from core.text import esc, user_link

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
    ("/kick <user> [reason]", "Remove a user from the chat. They are not banned and can return with an invite link. Alias: /punch."),
    ("/kickme", "Remove yourself from the chat. Alias: /punchme.", "everyone"),
    ("/sban <user> [reason]", "Silent ban: bans, deletes your command and the message you replied to, and says nothing."),
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


async def _silently(message: Message, replied: Message | None = None) -> None:
    """Delete the command (and the message it was about): silent actions leave no trace."""
    for gone in (message, replied):
        if gone is not None:
            with suppress(TelegramAPIError):
                await message.bot.delete_message(message.chat.id, gone.message_id)


@router.message(cmd("ban", "dban", "sban"))
async def cmd_ban(message: Message, command: CommandObject, role: Role | None = None) -> None:
    name = command.command.lower()
    deleting = name in ("dban", "sban")
    silent = name == "sban"

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

    await moderation.log_action(message, "BANNED", actor, target, target.extra, *((("Silent", "yes"),) if silent else ()))
    if silent:
        await _silently(message, replied)
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

    await moderation.log_action(message, "TBANNED", actor, target, reason, ("Duration", moderation.format_duration(duration)))
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

    await moderation.log_action(message, "UNBANNED", actor, target)
    await reply(message, f"{moderation.who(target)} was unbanned{moderation.by(actor)}{moderation.staff_note(actor)}")


@router.message(cmd("kick", "punch"))
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

    await moderation.log_action(message, "KICKED", actor, target, target.extra)
    await reply(
        message,
        f"{moderation.who(target)} was kicked{moderation.by(actor)}"
        f"{moderation.reason_line(target.extra)}{moderation.staff_note(actor)}",
    )


@router.message(cmd("kickme", "punchme"))
async def cmd_kickme(message: Message) -> None:
    """Anyone may remove themselves. Admins can't be removed by a bot, so they are told so."""
    user = message.from_user
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if user is None or message.sender_chat is not None:
        await reply(message, "Send this as yourself, not as an anonymous admin or a channel.")
        return
    if await moderation.is_chat_admin(message.bot, message.chat.id, user.id):
        await reply(message, "I can't remove an admin of this chat. Step down first, or leave on your own.")
        return
    try:
        await perform(message.bot.ban_chat_member(message.chat.id, user.id))
        await perform(message.bot.unban_chat_member(message.chat.id, user.id, only_if_banned=True))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return
    me = moderation.Actor(user.id, user.full_name)
    await moderation.log_action(message, "KICKED", me, moderation.Target(user.id, user.full_name), "Used /kickme")
    await reply(message, f"{user_link(user.id, user.full_name)} kicked themselves out. They can come back with an invite link.", quote=False)
