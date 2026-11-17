"""Approved users: trusted people that automatic measures skip"""

from contextlib import suppress

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, LinkPreviewOptions, Message

from core import approvals, federations, gbans, immunity, info_hooks, moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.roles import Role
from core.targets import TargetError, resolve_target
from core.text import esc, user_link, user_ref
from core.i18n import tr

# Approve trusted users so locks, blacklists, antiflood, global bans and federation
# bans stop applying to them. Useful when you trust someone but don't want to make
# them an admin. Approval is per chat, and admins can still ban, mute or warn an
# approved user by hand.
ABOUT = "approves.about"

HELP_WHO = "common.admins"

HELP = [
    ("/approve <user>", "approves.help.approve_a_trusted_user_in"),
    ("/unapprove <user>", "approves.help.take_a_user_s_approval"),
    ("/approval [user]", "approves.help.check_whether_a_user_you"),
    ("/approved", "approves.help.list_everyone_approved_in_this"),
    ("/disapproveall", "approves.help.take_away_the_approval_of", "common.chat_owner"),
]

router = Router(name="approves")

MAX_SHOWN = 60
CB_CLEAR = "approve:clear:"  # approve:clear:yes / approve:clear:no
NO_PREVIEW = LinkPreviewOptions(is_disabled=True)


async def _begin(message: Message, command: CommandObject, role: Role | None, *, need_target: bool = True):
    """Check the person (chat admins, or the bot's staff from Support up) and find the target."""
    actor = await moderation.begin_chat_command(message, role, staff_min=Role.SUPPORT)
    if actor is None:
        return None
    try:
        target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return None
    if immunity.is_immune(target.user_id):
        await reply(message, tr("common.i_can_t_take_action_against"))
        return None
    return target, actor


@router.message(cmd("approve"))
async def cmd_approve(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await _begin(message, command, role)
    if started is None:
        return
    target, actor = started
    bot = message.bot
    chat_id = message.chat.id
    who = user_link(target.user_id, target.name)

    if (
        target.user_id == bot.id
        or await moderation.is_chat_admin(bot, chat_id, target.user_id)
    ):
        await reply(message, tr("approves.that_user_can_t_be_approved"))
        return
    if not await approvals.approve(chat_id, target.user_id, target.name, actor.id, actor.name):
        await reply(message, tr("approves.this_user_is_already_approved"))
        return

    if await gbans.is_gbanned(target.user_id):
        # Lift the ban the global ban put on them in this chat, nothing else.
        with suppress(TelegramAPIError):
            await bot.unban_chat_member(chat_id, target.user_id, only_if_banned=True)
        await gbans.forget_ban(target.user_id, chat_id)
    if chat_id in await federations.applied_in(target.user_id):
        # Likewise for a ban a federation put on them in this chat.
        with suppress(TelegramAPIError):
            await bot.unban_chat_member(chat_id, target.user_id, only_if_banned=True)
        await federations.forget_applied(chat_id, target.user_id)
    await reply(
        message,
        tr("approves.was_approved_locks_blacklists_antiflood_and", who=who, by=moderation.by(actor)) + moderation.staff_note(actor),
    )


@router.message(cmd("unapprove"))
async def cmd_unapprove(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await _begin(message, command, role)
    if started is None:
        return
    target, actor = started
    if not await approvals.unapprove(message.chat.id, target.user_id):
        await reply(message, tr("approves.this_user_was_not_approved_in"))
        return
    await reply(
        message,
        tr("approves.is_no_longer_approved", user_id=user_link(target.user_id, target.name), by=moderation.by(actor))
        + moderation.staff_note(actor),
    )


@router.message(cmd("approval"))
async def cmd_approval(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if (command.args or "").strip() or moderation.replied_message(message):
        started = await _begin(message, command, role)
        if started is None:
            return
        target, _actor = started
        user_id, name = target.user_id, target.name
    else:
        if await moderation.begin_chat_command(message, role, staff_min=Role.SUPPORT) is None:
            return
        user_id, name = message.from_user.id, message.from_user.full_name

    who = user_ref(user_id, name)
    row = await approvals.get(message.chat.id, user_id)
    if row is None:
        await reply(message, tr("approves.is_not_approved_in_this_chat", who=who))
        return
    by = tr("approves.by", approved_by=user_ref(row['approved_by'], row['approved_by_name'])) if row["approved_by"] else ""
    await reply(message, tr("approves.is_approved_in_this_chat", who=who, by=by), link_preview_options=NO_PREVIEW)


@router.message(cmd("approved", "approvals", "approvedlist"))
async def cmd_approved(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role, staff_min=Role.SUPPORT) is None:
        return
    rows = await approvals.list_chat(message.chat.id)
    if not rows:
        await reply(message, tr("approves.there_are_no_approved_users_in"))
        return
    lines = [
        tr("approves.approved_users_in", title=esc(message.chat.title)),
        tr("approves.automatic_measures_leave_them_alone_in"),
        "",
    ]
    lines += [f"• {user_ref(row['user_id'], row['name'])}" for row in rows[:MAX_SHOWN]]
    if len(rows) > MAX_SHOWN:
        lines.append(f"…and {len(rows) - MAX_SHOWN} more.")
    await reply(message, "\n".join(lines), link_preview_options=NO_PREVIEW)


@router.message(cmd("disapproveall"))
async def cmd_disapproveall(message: Message, command: CommandObject, role: Role | None = None) -> None:
    try:
        await moderation.require_chat_owner(message)
    except moderation.ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len(await approvals.list_chat(message.chat.id))
    if not count:
        await reply(message, tr("approves.there_are_no_approved_users_in"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text=tr("approves.unapprove_all"), callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(
        message,
        tr("approves.unapprove_all_approved_users_of_this", count=count),
        reply_markup=keyboard,
    )


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_disapproveall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    status = await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id)
    if status != "creator":
        await callback.answer(tr("common.only_the_owner_of_this_chat"), show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await approvals.clear_chat(message.chat.id)
        await callback.answer()
        await edit(message, tr("approves.done_users_are_no_longer_approved", removed=removed), reply_markup=None)
    else:
        await callback.answer()
        await edit(message, tr("approves.cancelled_nobody_was_unapproved"), reply_markup=None)


async def _info_flags(bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    if chat_id is None:
        return []  # approval belongs to a chat
    return [(tr("approves.approved"), await approvals.is_approved(chat_id, user_id))]


info_hooks.register("approves", _info_flags)
