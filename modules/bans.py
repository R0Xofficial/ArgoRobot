"""Ban commands for chat admins"""

from contextlib import suppress
from datetime import datetime, timezone

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, Message

from core import moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc, user_link
from core.i18n import tr

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = "bans.about"

HELP_WHO = "common.admins"

HELP = [
    ("/ban <user> [reason]", "bans.help.ban_a_user_until_someone"),
    ("/dban <user> [reason]", "bans.help.reply_to_a_message_ban"),
    ("/tban <user> <time> [reason]", "bans.help.ban_a_user_for_a"),
    ("/unban <user>", "bans.help.lift_a_ban_so_the"),
    ("/kick <user> [reason]", "bans.help.remove_a_user_from_the"),
    ("/kickme", "bans.help.remove_yourself_from_the_chat", "common.everyone"),
    ("/sban <user> [reason]", "bans.help.silent_ban_bans_deletes_your"),
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

    await moderation.log_action(message, "BANNED", actor, target, target.extra, *(((lambda: (tr("chatlog.silent"), tr("common.yes"))),) if silent else ()))
    if silent:
        await _silently(message, replied)
        return
    if deleting:
        # Best effort: the message may already be gone.
        with suppress(TelegramAPIError):
            await message.bot.delete_message(message.chat.id, replied.message_id)

    await reply(
        message,
        tr("bans.was_banned", who=moderation.who(target), by=moderation.by(actor), reason_line=moderation.reason_line(target.extra), staff_note=moderation.staff_note(actor)),
        reply_markup=moderation.undo_keyboard("ban", target),
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

    await moderation.log_action(message, "TBANNED", actor, target, reason, lambda: (tr("chatlog.duration"), moderation.format_duration(duration)))
    await reply(
        message,
        tr("bans.was_banned_for", who=moderation.who(target), format_duration=moderation.format_duration(duration), by=moderation.by(actor), reason_line=moderation.reason_line(reason), staff_note=moderation.staff_note(actor)),
        reply_markup=moderation.undo_keyboard("ban", target),
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
            await reply(message, tr("common.i_can_t_take_action_against_2"))
            return
        if status is not None and status != "kicked":
            await reply(message, tr("bans.isn_t_banned", who=moderation.who(target)))
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
    await reply(message, tr("bans.was_unbanned", who=moderation.who(target), by=moderation.by(actor), staff_note=moderation.staff_note(actor)))


@router.callback_query(F.data.startswith(moderation.CB_UNDO + "ban:"))
async def cb_unban(callback: CallbackQuery, role: Role | None = None) -> None:
    """The Unban button under a ban."""
    user_id = moderation.parse_undo(callback, "ban")
    allowed = await moderation.authorize_callback(callback, role)
    if allowed is None:
        return
    message, actor = allowed
    if user_id is None:
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    bot = callback.bot
    target = await moderation.target_by_id(bot, message.chat.id, user_id)
    if not target.is_chat:
        status = await moderation.member_status(bot, message.chat.id, user_id)
        if status is not None and status != "kicked":
            await callback.answer(tr("bans.they_aren_t_banned_any_more"), show_alert=True)
            with suppress(TelegramAPIError):
                await message.edit_reply_markup(reply_markup=None)
            return
    try:
        if target.is_chat:
            await perform(bot.unban_chat_sender_chat(message.chat.id, user_id))
        else:
            await perform(bot.unban_chat_member(message.chat.id, user_id, only_if_banned=True))
    except ModerationError as exc:
        await callback.answer(str(exc), show_alert=True)
        return
    await callback.answer()
    await moderation.log_action(message, "UNBANNED", actor, target, "", lambda: (tr("chatlog.undo"), tr("common.yes")))
    await edit(message, tr("bans.was_unbanned", who=moderation.who(target), by=moderation.by(actor), staff_note=moderation.staff_note(actor)), reply_markup=None)


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
        tr("bans.was_kicked", who=moderation.who(target), by=moderation.by(actor), reason_line=moderation.reason_line(target.extra), staff_note=moderation.staff_note(actor)),
    )


@router.message(cmd("kickme", "punchme"))
async def cmd_kickme(message: Message) -> None:
    """Anyone may remove themselves. Admins can't be removed by a bot, so they are told so."""
    user = message.from_user
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if user is None or message.sender_chat is not None:
        await reply(message, tr("common.send_this_as_yourself_not_as"))
        return
    if await moderation.is_chat_admin(message.bot, message.chat.id, user.id):
        await reply(message, tr("bans.i_can_t_remove_an_admin"))
        return
    try:
        await perform(message.bot.ban_chat_member(message.chat.id, user.id))
        await perform(message.bot.unban_chat_member(message.chat.id, user.id, only_if_banned=True))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return
    await moderation.log_self(message, "KICKED", user.id, user.full_name, lambda: tr("bans.used_kickme"))
    await reply(message, tr("bans.kicked_themselves_out_they_can_come", id=user_link(user.id, user.full_name)), quote=False)
