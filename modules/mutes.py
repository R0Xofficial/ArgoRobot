"""Mute commands for chat admins"""

from contextlib import suppress
from datetime import datetime, timezone

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, ChatPermissions, Message

from core import moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc
from core.i18n import tr

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = "mutes.about"

HELP_WHO = "common.admins"

HELP = [
    ("/mute <user> [reason]", "mutes.help.stop_a_user_from_writing"),
    ("/tmute <user> <time> [reason]", "mutes.help.mute_a_user_for_a"),
    ("/unmute <user>", "mutes.help.give_a_muted_user_back"),
    ("/smute <user> [reason]", "mutes.help.silent_mute_mutes_deletes_your"),
    ("/stmute <user> <time> [reason]", "mutes.help.silent_timed_mute_alias_stempmute"),
    ("/sunmute <user>", "mutes.help.silent_unmute"),
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

    await moderation.log_action(message, "MUTED", actor, target, target.extra, *(((lambda: (tr("chatlog.silent"), tr("common.yes"))),) if silent else ()))
    if silent:
        await _silently(message)
        return
    await reply(
        message,
        tr("mutes.was_muted", who=moderation.who(target), by=moderation.by(actor), reason_line=moderation.reason_line(target.extra), staff_note=moderation.staff_note(actor)),
        reply_markup=moderation.undo_keyboard("mute", target),
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

    await moderation.log_action(message, "TMUTED", actor, target, reason, lambda: (tr("chatlog.duration"), moderation.format_duration(duration)), *(((lambda: (tr("chatlog.silent"), tr("common.yes"))),) if silent else ()))
    if silent:
        await _silently(message)
        return
    await reply(
        message,
        tr("mutes.was_muted_for", who=moderation.who(target), format_duration=moderation.format_duration(duration), by=moderation.by(actor), reason_line=moderation.reason_line(reason), staff_note=moderation.staff_note(actor)),
        reply_markup=moderation.undo_keyboard("mute", target),
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
            await reply(message, tr("common.i_can_t_take_action_against_2"))
            return
        # Muted means restricted without the right to write.
        if status != "restricted" or getattr(member, "can_send_messages", False) is not False:
            await reply(message, tr("mutes.isn_t_muted", who=moderation.who(target)))
            return

    try:
        permissions = await _default_permissions(message.bot, message.chat.id)
        await perform(message.bot.restrict_chat_member(message.chat.id, target.user_id, permissions=permissions))
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    await moderation.log_action(message, "UNMUTED", actor, target, "", *(((lambda: (tr("chatlog.silent"), tr("common.yes"))),) if silent else ()))
    if silent:
        await _silently(message)
        return
    await reply(message, tr("mutes.was_unmuted", who=moderation.who(target), by=moderation.by(actor), staff_note=moderation.staff_note(actor)))


@router.callback_query(F.data.startswith(moderation.CB_UNDO + "mute:"))
async def cb_unmute(callback: CallbackQuery, role: Role | None = None) -> None:
    """The Unmute button under a mute."""
    user_id = moderation.parse_undo(callback, "mute")
    allowed = await moderation.authorize_callback(callback, role)
    if allowed is None:
        return
    message, actor = allowed
    if user_id is None or user_id < 0:
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    bot = callback.bot
    target = await moderation.target_by_id(bot, message.chat.id, user_id)
    member = await moderation.get_member(bot, message.chat.id, user_id)
    status = getattr(getattr(member, "status", None), "value", getattr(member, "status", None))
    if member is not None and (status != "restricted" or getattr(member, "can_send_messages", False) is not False):
        await callback.answer(tr("mutes.they_aren_t_muted_any_more"), show_alert=True)
        with suppress(TelegramAPIError):
            await message.edit_reply_markup(reply_markup=None)
        return
    try:
        permissions = await _default_permissions(bot, message.chat.id)
        await perform(bot.restrict_chat_member(message.chat.id, user_id, permissions=permissions))
    except ModerationError as exc:
        await callback.answer(str(exc), show_alert=True)
        return
    await callback.answer()
    await moderation.log_action(message, "UNMUTED", actor, target, "", lambda: (tr("chatlog.undo"), tr("common.yes")))
    await edit(message, tr("mutes.was_unmuted", who=moderation.who(target), by=moderation.by(actor), staff_note=moderation.staff_note(actor)), reply_markup=None)
