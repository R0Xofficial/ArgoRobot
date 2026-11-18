"""Federations: one ban list for many chats"""

import asyncio
from types import SimpleNamespace
import logging
import re
import time
from contextlib import suppress
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware, Bot, F, Router
from aiogram.exceptions import TelegramAPIError, TelegramRetryAfter
from aiogram.filters import CommandObject
from aiogram.types import (
    BufferedInputFile,
    CallbackQuery,
    ChatMemberUpdated,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
    MessageReactionUpdated,
)

from core.banfiles import FORMATS, MAX_IMPORT_BYTES, parse_import, render_export, stamp as _stamp
from core import approvals, banguard, devnotify, federations, immunity, info_hooks, moderation, roles, users
from core.federations import FederationError
from core.help_index import Section
from core.filters import HasRole, cmd
from core.roles import Role
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, ModerationError
from core.targets import TargetError, replied_message, resolve_target, sender_as_target
from core.text import esc, user_link, user_ref, SWITCH as _ON_OFF
from core import i18n
from core.i18n import tr

log = logging.getLogger(__name__)

# After global bans (-100): a federation ban removes someone before anything else looks at them.
PRIORITY = -95

ABOUT = "federations.about"

OWNER = "federations.owner"
FED_ADMINS = "federations.fed_admins"
S_OWNER, S_ADMIN, S_USER = Section("federations.sections"), Section("federations.sections_2"), Section("federations.sections_3")

HELP = [
    ("/newfed <name>", "federations.help.create_a_federation_you_can", S_OWNER, OWNER),
    ("/renamefed <name>", "federations.help.rename_your_federation_private_chat", S_OWNER, OWNER),
    ("/delfed", "federations.help.delete_your_federation_nobody_is", S_OWNER, OWNER),
    ("/fedtransfer <user>", "federations.help.give_your_federation_to_a", S_OWNER, OWNER),
    ("/fedpromote <user>", "federations.help.make_a_user_a_federation", S_OWNER, OWNER),
    ("/feddemote <user>", "federations.help.take_a_federation_admin_s", S_OWNER, OWNER),
    ("/fednotif <on|off>", "federations.help.get_a_private_message_about", S_OWNER, OWNER),
    ("/fedreason <on|off>", "federations.help.make_federation_bans_require_a", S_OWNER, OWNER),
    ("/subfed <FedID>", "federations.help.inherit_the_bans_of_another", S_OWNER, OWNER),
    ("/unsubfed <FedID>", "federations.help.stop_inheriting_its_bans", S_OWNER, OWNER),
    ("/fedexport [csv|minicsv|json|human]", "federations.help.get_the_ban_list_as", S_OWNER, OWNER),
    ("/fedimport [overwrite|keep] [csv|minicsv|json|human]", "federations.help.reply_to_a_file_or", S_OWNER, OWNER),
    ("/setfedlog", "federations.help.use_this_chat_as_the", S_OWNER, OWNER),
    ("/unsetfedlog", "federations.help.stop_logging_your_federation_in", S_OWNER, OWNER),
    ("/fban <user> [reason]", "federations.help.ban_a_user_in_this", S_ADMIN, FED_ADMINS),
    ("/dfban <reason>", "federations.help.reply_to_a_message_delete", S_ADMIN, FED_ADMINS),
    ("/unfban <user>", "federations.help.lift_a_federation_ban", S_ADMIN, FED_ADMINS),
    ("/clearfban <user> [FedID] [reason]", "federations.help.bot_staff_only_lift_a", S_ADMIN, Role.DEVELOPER),
    ("/feddemoteme <FedID>", "federations.help.give_up_your_admin_rights", S_ADMIN, FED_ADMINS),
    ("/myfeds", "federations.help.list_the_federations_you_own", S_ADMIN, FED_ADMINS),
    ("/fedinfo [FedID]", "federations.help.show_a_federation_owner_admins", S_USER),
    ("/fedadmins [FedID]", "federations.help.list_the_owner_and_admins", S_USER),
    ("/fedsubs [FedID]", "federations.help.list_the_federations_a_federation", S_USER),
    ("/joinfed <FedID>", "federations.help.join_this_chat_to_a", "common.chat_owner", S_USER),
    ("/leavefed", "federations.help.take_this_chat_out_of", "common.chat_owner", S_USER),
    ("/chatfed", "federations.help.show_the_federation_this_chat", S_USER),
    ("/quietfed <on|off>", "federations.help.choose_whether_i_announce_removals", "common.admins", S_USER),
    ("/fedstat [user] [FedID]", "federations.help.the_federations_you_or_another", S_USER),
]

HELP_NOTES = [
    (
        "federations.note.getting_started",
        "federations.note.1_create_it_newfed_my",
    ),
    (
        "federations.note.files",
        "federations.note.csv_user_id_name_reason",
        S_OWNER,
    ),
]

router = Router(name="federations")

_FED_ID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", re.IGNORECASE)
EXPORT_COOLDOWN = 30.0
RECENT_SECONDS = 30
MAX_LIST_CHARS = 3500  # a longer /fedstat list is sent as a file
RIGHT = "can_change_info"
CB_DELETE, CB_TRANSFER, CB_PROMOTE, CB_REASON = "fd:", "fx:", "fp:", "fr:"
REASON_TTL = 300.0  # seconds an "Update reason?" question stays valid
_pending_reasons: dict[tuple[int, int], tuple] = {}  # (chat, message) of a "Update Reason?" question -> what was asked

_exported: dict[int, float] = {}
_recent: dict[tuple[int, int], float] = {}
_tasks: set[asyncio.Task] = set()


# --- Small helpers ----------------------------------------------------------------------------

def _yes_no(value: bool) -> str:
    return tr("common.yes") if value else tr("common.no")


def _item(label: str, value: str) -> str:
    return f"• <b>{label}:</b> {value}"


def _chat_ref(title: str | None, username: str | None, chat_id: int) -> str:
    """A chat's name for a log: a link to it when it is public."""
    name = esc(title or str(chat_id))
    return f'<a href="https://t.me/{esc(username)}">{name}</a>' if username else name


def _spawn(coroutine) -> None:
    task = asyncio.get_running_loop().create_task(coroutine)
    _tasks.add(task)
    task.add_done_callback(_tasks.discard)


async def _person(message: Message):
    """The person who sent a command, or None (after saying why) when they wrote anonymously."""
    user = message.from_user
    if user is None or message.sender_chat is not None:
        await reply(message, tr("common.send_this_as_yourself_not_as"))
        return None
    return user


async def _private(message: Message) -> bool:
    """Owner commands are only taken in a private chat with the bot (otherwise False, after saying so)."""
    if message.chat.type == "private":
        return True
    await reply(message, tr("federations.this_command_only_works_in_a"))
    return False


async def _own_fed(message: Message, *, private: bool = False):
    """(user, federation) of the person who owns one, or None after saying what is wrong."""
    if private and not await _private(message):
        return None
    user = await _person(message)
    if user is None:
        return None
    fed = await federations.owned_by(user.id)
    if fed is None:
        await reply(message, tr("federations.you_don_t_own_a_federation"))
        return None
    return user, fed


def _fed_name(fed: dict[str, Any]) -> str:
    return f"<b>{esc(fed['name'])}</b>"


def _own_line(bot: Bot, fed: dict[str, Any]) -> str:
    """Fed: the name as its owner set it (a @tag in it is theirs; nothing is added)."""
    return _item(tr("federations.fed"), esc(fed["name"]))


def _other_lines(bot: Bot, label: str, fed: dict[str, Any] | None, fed_id: str | None = None) -> str:
    """The other federation of a subscription, as two lines: <label>: its name and <label>ID: <code>id</code>."""
    name = esc(fed["name"]) if fed else tr("federations.that_federation")
    identifier = fed["fed_id"] if fed else fed_id
    return f"{_item(label, name)}\n{_item(label + tr("common.id"), f'<code>{esc(identifier)}</code>')}"


def _owner_link(fed: dict[str, Any]) -> str:
    return user_link(fed["owner_id"], fed["owner_name"])


async def _event(bot: Bot, fed: dict[str, Any], text) -> None:
    """
    Tell the log chat of the federation, and its owner when they asked for private messages.
    `text` is a function that writes the message: each one is written in the language of the chat it goes to (the federation log chat, the owner's private chat).
    """
    sent = False
    if fed["log_chat_id"]:
        try:
            async with i18n.speaking(fed["log_chat_id"]):
                body = text()
            await bot.send_message(fed["log_chat_id"], body, link_preview_options=LinkPreviewOptions(is_disabled=True))
            sent = True
        except TelegramAPIError as exc:
            log.debug("Federation log %s unreachable: %s", fed["log_chat_id"], exc)
    if fed["notify"] and not (sent and fed["log_chat_id"] == fed["owner_id"]):
        with suppress(TelegramAPIError):
            async with i18n.speaking(fed["owner_id"]):
                body = text()
            await bot.send_message(fed["owner_id"], body, link_preview_options=LinkPreviewOptions(is_disabled=True))


async def _admin_event(bot: Bot, fed: dict[str, Any], promoted: bool, user_id: int, who: str) -> None:
    """Tell the federation's log that someone became or stopped being one of its admins."""
    def text() -> str:
        title, label = (tr("federations.new_fed_promotion"), tr("federations.promoted_fedadmin")) if promoted else (tr("federations.new_fed_demotion"), tr("federations.demoted_fedadmin"))
        return f"<b>{title}</b>\n{_own_line(bot, fed)}\n{_item(label, who)}\n{_item(tr("federations.user_id"), f'<code>{user_id}</code>')}"

    await _event(bot, fed, text)


async def _resolve_fed(message: Message, argument: str, user) -> dict[str, Any] | None:
    """The federation an info command is about: the ID given, else the chat's, else the person's own."""
    argument = argument.strip()
    if argument:
        if not _FED_ID.match(argument):
            await reply(message, tr("federations.that_is_not_a_federation_id"))
            return None
        fed = await federations.get(argument.lower())
        if fed is None:
            await reply(message, tr("federations.i_can_t_find_a_federation"))
        return fed
    if message.chat.type in GROUP_TYPES:
        fed = await federations.chat_fed(message.chat.id)
        if fed is not None:
            return fed
    if user is not None:
        mine = await federations.feds_of(user.id)
        if len(mine) == 1:
            return mine[0]
        if len(mine) > 1:
            await reply(message, tr("federations.you_are_in_several_federations_give"))
            return None
    await reply(message, tr("federations.this_chat_is_not_in_a"))
    return None


async def _fed_to_ban_in(message: Message, user) -> dict[str, Any] | None:
    """The federation a fed admin acts in: the chat's, or in private the only one they administer."""
    if message.chat.type in GROUP_TYPES:
        fed = await federations.chat_fed(message.chat.id)
        if fed is None:
            await reply(message, tr("federations.this_chat_is_not_in_a_2"))
            return None
        if not await federations.is_admin(fed, user.id):
            await reply(message, tr("federations.you_are_not_an_admin_of"))
            return None
        return fed
    mine = await federations.feds_of(user.id)
    if not mine:
        await reply(message, tr("federations.you_are_not_an_admin_of_2"))
        return None
    if len(mine) > 1:
        await reply(message, tr("federations.you_administer_several_federations_use_this"))
        return None
    return mine[0]


async def _target(message: Message, command: CommandObject, *, allow_bots: bool = False):
    """The user a command names. allow_bots: bans may name a bot (spam bots are banned too); roles may not."""
    try:
        return await resolve_target(message, command, allow_bots=allow_bots)
    except TargetError as exc:
        await reply(message, esc(str(exc)))
        return None


# --- Owner commands ---------------------------------------------------------------------------

@router.message(cmd("newfed"))
async def cmd_newfed(message: Message, command: CommandObject) -> None:
    if not await _private(message):
        return
    user = await _person(message)
    if user is None:
        return
    if not (command.args or "").strip():
        await reply(message, tr("federations.use_newfed_lt_name_gt"))
        return
    try:
        fed = await federations.create(command.args, user.id, user.full_name)
    except FederationError as exc:
        await reply(message, esc(exc))
        return
    await reply(
        message,
        tr("federations.federation_created")
        + "\n".join([_item(tr("federations.name"), f"<code>{esc(fed['name'])}</code>"), _item(tr("common.id"), f"<code>{fed['fed_id']}</code>")])
        + tr("federations.join_a_chat_to_it_with"),
    )
    devnotify.fed_created(message.bot, fed, user.id, user.full_name)


@router.message(cmd("renamefed"))
async def cmd_renamefed(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message, private=True)
    if mine is None:
        return
    _, fed = mine
    if not (command.args or "").strip():
        await reply(message, tr("federations.use_renamefed_lt_name_gt"))
        return
    try:
        name = await federations.rename(fed["fed_id"], command.args)
    except FederationError as exc:
        await reply(message, esc(exc))
        return
    devnotify.fed_renamed(message.bot, fed, name, message.from_user.id, message.from_user.full_name)
    await reply(message, tr("federations.your_federation_is_now_called", name=esc(name)))


@router.message(cmd("delfed"))
async def cmd_delfed(message: Message) -> None:
    mine = await _own_fed(message, private=True)
    if mine is None:
        return
    user, fed = mine
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[
            InlineKeyboardButton(text="Delete", callback_data=f"{CB_DELETE}y:{user.id}"),
            InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_DELETE}n:{user.id}"),
        ]]
    )
    await reply(
        message,
        f"Delete the federation {_fed_name(fed)}? Its chats, admins, bans and subscriptions are forgotten. People it banned stay banned in their chats. This can't be undone.",
        reply_markup=keyboard,
    )


@router.callback_query(F.data.startswith(CB_DELETE))
async def cb_delfed(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, owner = callback.data.split(":")
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    if callback.from_user.id != int(owner):
        await callback.answer(tr("federations.only_the_owner_of_the_federation"), show_alert=True)
        return
    fed = await federations.owned_by(callback.from_user.id)
    await callback.answer()
    if answer != "y" or fed is None:
        await edit(message, tr("federations.cancelled_nothing_was_deleted") if answer != "y" else tr("federations.you_no_longer_own_a_federation"), reply_markup=None)
        return
    await federations.delete(fed["fed_id"])
    await edit(message, tr("federations.the_federation_is_deleted", fed=_fed_name(fed)), reply_markup=None)
    devnotify.fed_deleted(callback.bot, fed, callback.from_user.id, callback.from_user.full_name)


@router.message(cmd("fedtransfer"))
async def cmd_fedtransfer(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message, private=True)
    if mine is None:
        return
    user, fed = mine
    target = await _target(message, command)
    if target is None:
        return
    if target.is_chat or target.kind == "bot":
        await reply(message, tr("federations.a_federation_can_only_belong_to"))
        return
    if target.user_id == user.id:
        await reply(message, tr("federations.you_already_own_it"))
        return
    if await federations.owned_by(target.user_id) is not None:
        await reply(message, tr("common.that_person_already_owns_a_federation"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[
            InlineKeyboardButton(text=tr("federations.accept"), callback_data=f"{CB_TRANSFER}y:{target.user_id}:{user.id}"),
            InlineKeyboardButton(text=tr("common.decline"), callback_data=f"{CB_TRANSFER}n:{target.user_id}:{user.id}"),
        ]]
    )
    await reply(
        message,
        tr("federations.wants_to_give_you_the_federation", user_id=user_link(target.user_id, target.name), id=user_link(user.id, user.full_name), fed=_fed_name(fed)),
        reply_markup=keyboard,
    )


@router.callback_query(F.data.startswith(CB_TRANSFER))
async def cb_fedtransfer(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, new_id, old_id = callback.data.split(":")
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    giver = callback.from_user.id == int(old_id)
    if callback.from_user.id != int(new_id) and not (giver and answer != "y"):
        await callback.answer(tr("federations.only_the_person_it_is_offered"), show_alert=True)
        return
    await callback.answer()
    fed = await federations.owned_by(int(old_id))
    if answer != "y":
        await edit(message, tr("federations.the_transfer_was_cancelled_by_the") if giver else tr("federations.the_transfer_was_declined"), reply_markup=None)
        return
    if fed is None:
        await edit(message, tr("federations.that_federation_is_no_longer_theirs"), reply_markup=None)
        return
    try:
        await federations.transfer(fed["fed_id"], callback.from_user.id, callback.from_user.full_name)
    except FederationError as exc:
        await edit(message, esc(exc), reply_markup=None)
        return
    await edit(message, tr("federations.the_federation_now_belongs_to", fed=_fed_name(fed), id=user_link(callback.from_user.id, callback.from_user.full_name)), reply_markup=None)
    devnotify.fed_transferred(callback.bot, fed, callback.from_user.id, callback.from_user.full_name)
    await _event(
        callback.bot,
        {**fed, "owner_id": callback.from_user.id},
        lambda: tr("federations.new_fedowner", v=_own_line(callback.bot, fed), item=_item(tr("federations.previous_owner"), user_ref(fed['owner_id'], fed['owner_name'])), item2=_item(tr("federations.new_owner"), user_ref(callback.from_user.id, callback.from_user.full_name))),
    )


@router.message(cmd("fedpromote", "fpromote"))
async def cmd_fedpromote(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    user, fed = mine
    target = await _target(message, command)
    if target is None:
        return
    if target.is_chat or target.kind == "bot":
        await reply(message, tr("federations.only_people_can_be_federation_admins"))
        return
    if await federations.is_admin(fed, target.user_id):
        await reply(message, tr("federations.that_person_already_is_an_admin"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[
            InlineKeyboardButton(text=tr("federations.accept"), callback_data=f"{CB_PROMOTE}y:{target.user_id}:{fed['fed_id']}"),
            InlineKeyboardButton(text=tr("common.decline"), callback_data=f"{CB_PROMOTE}n:{target.user_id}:{fed['fed_id']}"),
        ]]
    )
    await reply(
        message,
        tr("federations.wants_to_make_you_an_admin", user_id=user_link(target.user_id, target.name), id=user_link(user.id, user.full_name), fed=_fed_name(fed)),
        reply_markup=keyboard,
    )


@router.callback_query(F.data.startswith(CB_PROMOTE))
async def cb_fedpromote(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, user_id, fed_id = callback.data.split(":")
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    fed = await federations.get(fed_id)
    giver = fed is not None and callback.from_user.id == fed["owner_id"]
    if callback.from_user.id != int(user_id) and not (giver and answer != "y"):
        await callback.answer(tr("federations.only_the_person_it_is_offered"), show_alert=True)
        return
    await callback.answer()
    if answer != "y":
        await edit(message, tr("federations.the_offer_was_cancelled_by_the") if giver else tr("federations.the_offer_was_declined"), reply_markup=None)
        return
    if fed is None:
        await edit(message, tr("federations.that_federation_no_longer_exists"), reply_markup=None)
        return
    try:
        await federations.add_admin(fed_id, callback.from_user.id, callback.from_user.full_name)
    except FederationError as exc:
        await edit(message, esc(exc), reply_markup=None)
        return
    who = user_link(callback.from_user.id, callback.from_user.full_name)
    await edit(message, tr("federations.is_now_an_admin_of_the", who=who, fed=_fed_name(fed)), reply_markup=None)
    await _admin_event(callback.bot, fed, True, callback.from_user.id, who)


@router.message(cmd("feddemote", "fdemote"))
async def cmd_feddemote(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    target = await _target(message, command)
    if target is None:
        return
    if not await federations.remove_admin(fed["fed_id"], target.user_id):
        await reply(message, tr("federations.that_person_is_not_an_admin"))
        return
    who = user_link(target.user_id, target.name)
    await reply(message, tr("federations.is_no_longer_an_admin_of", who=who, fed=_fed_name(fed)))
    await _admin_event(message.bot, fed, False, target.user_id, who)


@router.message(cmd("feddemoteme"))
async def cmd_feddemoteme(message: Message, command: CommandObject) -> None:
    user = await _person(message)
    if user is None:
        return
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, tr("federations.use_feddemoteme_lt_fedid_gt"))
        return
    fed = await federations.get(argument.lower())
    if fed is None:
        await reply(message, tr("federations.i_can_t_find_a_federation"))
        return
    if fed["owner_id"] == user.id:
        await reply(message, tr("federations.you_own_that_federation_transfer_it"))
        return
    if not await federations.remove_admin(fed["fed_id"], user.id):
        await reply(message, tr("federations.you_are_not_an_admin_of_3"))
        return
    await reply(message, tr("federations.you_are_no_longer_an_admin", fed=_fed_name(fed)))
    await _admin_event(message.bot, fed, False, user.id, user_link(user.id, user.full_name))


async def _toggle(message: Message, command: CommandObject, field: str, name: str) -> None:
    mine = await _own_fed(message, private=field == "require_reason")
    if mine is None:
        return
    _, fed = mine
    argument = (command.args or "").strip().lower()
    if not argument:
        await reply(message, tr("federations.is_for", name=name, opt=tr("common.on") if fed[field] else tr("common.off"), fed=_fed_name(fed)))
        return
    if argument not in _ON_OFF:
        await reply(message, tr("federations.use_on_or_off"))
        return
    await federations.update(fed["fed_id"], **{field: _ON_OFF[argument]})
    await reply(message, tr("federations.is_now_for", name=name, opt=tr("common.on") if _ON_OFF[argument] else tr("common.off"), fed=_fed_name(fed)))


@router.message(cmd("fednotif"))
async def cmd_fednotif(message: Message, command: CommandObject) -> None:
    await _toggle(message, command, "notify", tr("federations.private_notifications"))


@router.message(cmd("fedreason"))
async def cmd_fedreason(message: Message, command: CommandObject) -> None:
    await _toggle(message, command, "require_reason", tr("federations.requiring_a_reason"))


@router.message(cmd("subfed"))
async def cmd_subfed(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, tr("federations.use_subfed_lt_fedid_gt"))
        return
    other = await federations.get(argument.lower())
    if other is None:
        await reply(message, tr("federations.i_can_t_find_a_federation"))
        return
    try:
        await federations.subscribe(fed["fed_id"], other["fed_id"])
    except FederationError as exc:
        await reply(message, esc(exc))
        return
    await reply(message, tr("federations.now_inherits_the_bans_of_your", fed=_fed_name(fed), other=_fed_name(other)))
    by = _item(tr("federations.by"), user_ref(message.from_user.id, message.from_user.full_name))
    bot = message.bot
    await _event(bot, fed, lambda: tr("federations.new_fedsubscribed", v=_own_line(bot, fed), v2=_other_lines(bot, "SubbedFed", other), by=by))
    await _event(bot, other, lambda: tr("federations.new_fedsubscriber", v=_own_line(bot, other), v2=_other_lines(bot, "SubbedFed", fed), by=by))


@router.message(cmd("unsubfed"))
async def cmd_unsubfed(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, tr("federations.use_unsubfed_lt_fedid_gt"))
        return
    other = await federations.get(argument.lower())
    if not await federations.unsubscribe(fed["fed_id"], argument.lower()):
        await reply(message, tr("federations.your_federation_is_not_subscribed_to"))
        return
    name = _fed_name(other) if other else tr("federations.that_federation")
    await reply(message, tr("federations.no_longer_inherits_the_bans_of", fed=_fed_name(fed), name=name))
    by = _item(tr("federations.by"), user_ref(message.from_user.id, message.from_user.full_name))
    bot = message.bot
    await _event(bot, fed, lambda: tr("federations.un_fedsubscribed", v=_own_line(bot, fed), v2=_other_lines(bot, "UnsubbedFed", other, argument.lower()), by=by))
    if other is not None:
        await _event(bot, other, lambda: tr("federations.un_fedsubscriber", v=_own_line(bot, other), v2=_other_lines(bot, "UnsubbedFed", fed), by=by))


@router.message(cmd("setfedlog"))
async def cmd_setfedlog(message: Message) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("federations.send_this_in_the_group_that"))
        return
    # Whoever owns a federation must also be an admin here, or the log of a federation could be pointed at anyone's chat.
    if not await moderation.is_chat_admin(message.bot, message.chat.id, message.from_user.id):
        await reply(message, tr("federations.only_an_admin_of_this_chat"))
        return
    await federations.update(fed["fed_id"], log_chat_id=message.chat.id)
    await reply(message, tr("federations.this_chat_is_now_the_log", fed=_fed_name(fed)))


@router.message(cmd("unsetfedlog"))
async def cmd_unsetfedlog(message: Message) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    if not fed["log_chat_id"]:
        await reply(message, tr("federations.your_federation_has_no_log_chat"))
        return
    await federations.update(fed["fed_id"], log_chat_id=None)
    await reply(message, tr("federations.is_no_longer_logged_in_a", fed=_fed_name(fed)))


# --- Export and import ------------------------------------------------------------------------

@router.message(cmd("fedexport"))
async def cmd_fedexport(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    user, fed = mine
    fmt = (command.args or "csv").strip().lower() or "csv"
    if fmt not in FORMATS:
        await reply(message, tr("federations.use_fedexport_csv_minicsv_json_or"))
        return
    now = time.monotonic()
    if now - _exported.get(user.id, -EXPORT_COOLDOWN) < EXPORT_COOLDOWN:
        await reply(message, tr("common.wait_a_little_before_exporting_again"))
        return
    rows = await federations.bans(fed["fed_id"])
    if not rows:
        await reply(message, tr("federations.your_federation_has_no_bans_to"))
        return
    _exported[user.id] = now
    data, extension = render_export(rows, fmt)
    await message.bot.send_document(
        message.chat.id,
        BufferedInputFile(data, filename=f"fedbans_{fed['fed_id'][:8]}.{extension}"),
        caption=tr("federations.bans_of", rows=len(rows), name=esc(fed['name'])),
        message_thread_id=message.message_thread_id if message.is_topic_message else None,
    )


@router.message(cmd("fedimport"))
async def cmd_fedimport(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    user, fed = mine
    words = (command.args or "").lower().split()
    overwrite = "overwrite" in words
    fmt = next((w for w in words if w in FORMATS), None)
    if any(w not in FORMATS and w not in ("overwrite", "keep") for w in words):
        await reply(message, tr("federations.use_fedimport_overwrite_keep_csv_minicsv"))
        return
    document = getattr(message, "document", None) or getattr(replied_message(message), "document", None)
    if document is None:
        await reply(message, tr("federations.reply_to_a_file_with_the"))
        return
    if (document.file_size or 0) > MAX_IMPORT_BYTES:
        await reply(message, tr("federations.that_file_is_too_big_at"))
        return
    try:
        raw = (await message.bot.download(document.file_id)).read()
        rows = parse_import(raw.decode("utf-8", errors="replace"), fmt)
    except ValueError as exc:
        await reply(message, esc(exc))
        return
    except TelegramAPIError:
        await reply(message, tr("common.i_couldn_t_download_that_file"))
        return
    status = await reply(message, tr("federations.importing_bans_please_wait", rows=len(rows))) if len(rows) > 2000 else None
    admins = {a["user_id"] for a in await federations.admins(fed["fed_id"])} | {fed["owner_id"], message.bot.id}
    kept = [r for r in rows if r[0] not in admins and not immunity.is_immune(r[0])]
    added = await federations.import_bans(fed["fed_id"], kept, overwrite, user.id, user.full_name)
    skipped = len(rows) - added
    send = (lambda m, text: edit(status, text)) if status is not None else reply
    await send(
        message,
        tr("federations.imported_ban_into", added=added, opt='s' if added != 1 else '', fed=_fed_name(fed))
        + (tr("common.skipped_already_banned_or_protected", skipped=skipped) if skipped else ""),
    )
    await _event(message.bot, fed, lambda: tr("federations.new_fedbans", v=_own_line(message.bot, fed), item=_item(tr("common.added"), f'<code>{added}</code>')))


# --- Fed admin commands -----------------------------------------------------------------------

async def _protected(bot: Bot, fed: dict[str, Any], target) -> str | None:
    """Why this target can't be banned in the federation, or None."""
    if target.is_chat:
        return tr("federations.only_people_can_be_banned_in")
    if target.user_id == bot.id:
        return tr("federations.i_m_not_banning_myself")
    if immunity.is_immune(target.user_id) or await roles.get_role(target.user_id) is not None:
        return tr("federations.that_account_is_protected_and_can")
    if await federations.is_admin(fed, target.user_id):
        return tr("federations.that_person_is_an_admin_of")
    return None


def _ban_text(title: str, fed: dict[str, Any], admin, target_id: int, target_name: str | None, reason: str | None, fed_line: str | None = None, *, previous: tuple[str | None] | None = None) -> str:
    """`previous` is given when a reason is being replaced: a one-item tuple with the old reason (None when there was none)."""
    lines = [
        f"<b>{title}</b>",
        fed_line or _item(tr("federations.fed"), esc(fed['name'])),
        _item(tr("federations.fedadmin"), user_ref(admin.id, admin.full_name)),
        _item(tr("common.user"), user_link(target_id, target_name)),
        _item(tr("federations.user_id"), f"<code>{target_id}</code>"),
    ]
    if previous is not None:
        lines.append(_item(tr("federations.previous_reason"), f"<code>{esc(previous[0])}</code>" if previous[0] else tr("federations.none")))
    if reason:
        lines.append(_item(tr("federations.new_reason") if previous is not None else tr("federations.reason"), f"<code>{esc(reason)}</code>"))
    return "\n".join(lines)


async def _ban_here(message: Message, user_id: int) -> None:
    """A fban given in a chat takes effect there at once if the person is in it; in the other chats of the federation it does when the person next acts."""
    # Only when they are in the chat: in an administrative group nobody is a member and a ban
    # there would only add a row to its ban list.
    if (
        message.chat.type in GROUP_TYPES
        and await moderation.is_in_chat(message.bot, message.chat.id, user_id)
        and not await _spared(message.bot, message.chat.id, user_id)
    ):
        await _ban_in(message.bot, message.chat.id, user_id)


@router.message(cmd("fban", "dfban"))
async def cmd_fban(message: Message, command: CommandObject) -> None:
    deleting = command.command.lower() == "dfban"
    user = await _person(message)
    if user is None:
        return
    fed = await _fed_to_ban_in(message, user)
    if fed is None:
        return
    replied = replied_message(message)
    if deleting:
        # Like /dgban: the replied-to message names the person, everything typed is the reason.
        if replied is None:
            await reply(message, tr("common.reply_to_the_message_of_the"))
            return
        target = sender_as_target(replied)
        if target.is_chat or target.user_id in users.IGNORED_IDS:
            await reply(message, tr("common.that_message_was_not_sent_by"))
            return
        target.extra = (command.args or "").strip()
    else:
        target = await _target(message, command, allow_bots=True)
        if target is None:
            return
    problem = await _protected(message.bot, fed, target)
    if problem:
        await reply(message, problem)
        return
    reason = target.extra.strip()
    if fed["require_reason"] and not reason:
        await reply(message, tr("federations.this_federation_requires_a_reason_dfban") if deleting else tr("federations.this_federation_requires_a_reason_fban"))
        return
    if deleting:
        with suppress(TelegramAPIError):  # best effort: the message may be gone already
            await message.bot.delete_message(message.chat.id, replied.message_id)
    existing = await federations.get_ban(fed["fed_id"], target.user_id)
    if existing is not None:
        who = user_link(target.user_id, target.name)
        if not reason or reason == (existing["reason"] or ""):
            await _ban_here(message, target.user_id)  # they may still be in this chat
            await reply(message, tr("federations.user_is_already_fbanned_in", who=who, fed=_fed_name(fed)) + (tr("federations.with_reason", reason=esc(existing['reason'])) if existing["reason"] else "."))
            return
        keyboard = InlineKeyboardMarkup(
            inline_keyboard=[[
                InlineKeyboardButton(text="Update reason", callback_data=f"{CB_REASON}y:{fed['fed_id']}:{target.user_id}"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_REASON}n:{fed['fed_id']}:{target.user_id}"),
            ]]
        )
        sent = await reply(
            message,
            tr("federations.user_is_already_fbanned_in", who=who, fed=_fed_name(fed))
            + (tr("federations.with_reason", reason=esc(existing['reason'])) if existing["reason"] else tr("federations.with_no_reason"))
            + tr("federations.would_you_like_to_update_this", reason=esc(reason)),
            reply_markup=keyboard,
        )
        now = time.monotonic()
        for key in [k for k, v in _pending_reasons.items() if now - v[4] > REASON_TTL]:
            del _pending_reasons[key]
        if len(_pending_reasons) > 500:
            _pending_reasons.clear()
        _pending_reasons[(message.chat.id, sent.message_id)] = (target.name, reason, user.id, user.full_name, now)
        return
    await federations.ban(fed["fed_id"], target.user_id, target.name, reason, user.id, user.full_name)
    await _ban_here(message, target.user_id)
    def build() -> str:
        return _ban_text(tr("federations.new_fedban"), fed, user, target.user_id, target.name, reason, _own_line(message.bot, fed))

    await reply(message, build())
    await _event(message.bot, fed, build)


@router.callback_query(F.data.startswith(CB_REASON))
async def cb_fedreason_update(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, fed_id, user_id = callback.data.split(":")
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    fed = await federations.get(fed_id)
    if fed is None or not await federations.is_admin(fed, callback.from_user.id):
        await callback.answer(tr("federations.only_the_admins_of_this_federation"), show_alert=True)
        return
    key = (message.chat.id, message.message_id)
    pending = _pending_reasons.pop(key, None)
    await callback.answer()
    if answer != "y":
        await edit(message, tr("federations.the_reason_was_not_changed"), reply_markup=None)
        return
    if pending is None or time.monotonic() - pending[4] > REASON_TTL:
        await edit(message, tr("federations.this_request_has_expired_send_fban"), reply_markup=None)
        return
    name, reason, by_id, by_name, _ = pending
    old = await federations.ban(fed_id, int(user_id), name, reason, by_id, by_name)
    previous = ((old or {}).get("reason") or None,)  # what the reason was until now: nobody should have to wonder
    def build() -> str:
        return _ban_text(tr("federations.fedban_reason_update"), fed, SimpleNamespace(id=by_id, full_name=by_name), int(user_id), name, reason, _own_line(callback.bot, fed), previous=previous)

    text = build()
    await edit(message, text, reply_markup=None)
    await _event(callback.bot, fed, build)


@router.message(cmd("unfban"))
async def cmd_unfban(message: Message, command: CommandObject) -> None:
    user = await _person(message)
    if user is None:
        return
    fed = await _fed_to_ban_in(message, user)
    if fed is None:
        return
    target = await _target(message, command, allow_bots=True)
    if target is None:
        return
    if await federations.unban(fed["fed_id"], target.user_id) is None:
        await reply(message, tr("federations.is_not_banned_in", user_id=user_link(target.user_id, target.name), fed=_fed_name(fed)))
        return
    def build() -> str:
        return _ban_text(tr("federations.un_fedban"), fed, user, target.user_id, target.name, None, _own_line(message.bot, fed))

    await reply(message, build())
    await _event(message.bot, fed, build)
    _spawn(_lift(message.bot, fed["fed_id"], target.user_id))


@router.message(cmd("clearfban"), HasRole(Role.DEVELOPER))
async def cmd_clearfban(message: Message, command: CommandObject) -> None:
    """Staff override: remove a (possibly bogus) ban from federations the staff member doesn't own."""
    user = await _person(message)
    if user is None:
        return
    tokens = (command.args or "").split()
    user_part = [] if message.reply_to_message else tokens[:1]
    rest = tokens[len(user_part):]
    fed_id = None
    if rest and re.fullmatch(r"[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}", rest[0]):
        fed_id, rest = rest[0].lower(), rest[1:]
    reason = " ".join(rest)[:federations.MAX_REASON] or None
    if fed_id is not None and await federations.get(fed_id) is None:
        await reply(message, tr("federations.no_federation_has_that_id"))
        return
    try:
        target = await resolve_target(message, SimpleNamespace(args=" ".join(user_part), command=command.command), allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(str(exc)))
        return

    rows = await federations.banned_in(target.user_id)
    if fed_id is not None:
        rows = [row for row in rows if row["fed_id"] == fed_id]
    if not rows:
        await reply(message, tr("federations.has_no_federation_ban_to_clear", user_id=user_link(target.user_id, target.name)))
        return

    lines = [tr("federations.federation_bans_cleared"), _item(tr("common.user"), user_link(target.user_id, target.name))]
    if reason:
        lines.append(_item(tr("federations.reason"), f"<code>{esc(reason)}</code>"))
    for row in rows:
        if await federations.unban(row["fed_id"], target.user_id) is None:
            continue
        fed = await federations.get(row["fed_id"])
        lines.append(_item(tr("federations.fed"), esc(row["name"])))
        if fed is not None:
            await _event(
                message.bot, fed,
                lambda fed=fed: _ban_text(tr("federations.un_fedban"), fed, user, target.user_id, target.name, reason, _own_line(message.bot, fed))
                + f"\n\n<i>{tr(moderation.STAFF_NOTE)}</i>",
            )
        _spawn(_lift(message.bot, row["fed_id"], target.user_id))
    await reply(message, "\n".join(lines))


@router.message(cmd("myfeds"))
async def cmd_myfeds(message: Message) -> None:
    if not await _private(message):
        return
    user = await _person(message)
    if user is None:
        return
    mine = await federations.feds_of(user.id)
    if not mine:
        await reply(message, tr("federations.you_are_not_an_admin_of_2"))
        return
    lines = [tr("federations.your_federations")]
    for fed in mine:
        role = tr("common.owner") if fed["owner_id"] == user.id else tr("common.admin")
        lines.append(f"• <b>{esc(fed['name'])}</b> ({role})\n  <code>{fed['fed_id']}</code>")
    await reply(message, "\n".join(lines))


# --- Everyone's commands ----------------------------------------------------------------------

@router.message(cmd("fedinfo"))
async def cmd_fedinfo(message: Message, command: CommandObject) -> None:
    if (command.args or "").strip():
        fed = await _resolve_fed(message, command.args, message.from_user)
    else:
        # Without an ID: the federation this person owns, whatever chat it is sent in.
        fed = await federations.owned_by(message.from_user.id)
        if fed is None:
            await reply(message, tr("federations.you_don_t_own_a_federation_2"))
    if fed is None:
        return
    fed_id = fed["fed_id"]
    subs = await federations.subs_of(fed_id)
    lines = [
        tr("federations.fed_info"),
        _item(tr("federations.fedid"), f"<code>{fed_id}</code>"),
        _item(tr("federations.name"), esc(fed["name"])),
        _item(tr("common.owner"), f"{_owner_link(fed)} [<code>{fed['owner_id']}</code>]"),
        _item(tr("federations.admins"), f"<code>{len(await federations.admins(fed_id))}</code>"),
        _item(tr("federations.banned_users"), f"<code>{await federations.ban_count(fed_id)}</code>"),
        _item(tr("federations.connected_chats"), f"<code>{await federations.chat_count(fed_id)}</code>"),
        _item(tr("federations.subscribers"), f"<code>{len(await federations.subscribers_of(fed_id))}</code>"),
        "",
    ]
    if subs:
        lines.append("<b>Subscribed to:</b>")
        for sub_id in subs:
            other = await federations.get(sub_id)
            lines.append(f"• {esc(other['name']) if other else tr("common.unknown")} (<code>{sub_id}</code>)")
    else:
        lines.append("Not subscribed to any federation.")
    await reply(message, "\n".join(lines))


@router.message(cmd("fedadmins"))
async def cmd_fedadmins(message: Message, command: CommandObject) -> None:
    fed = await _resolve_fed(message, command.args or "", message.from_user)
    if fed is None:
        return
    lines = [tr("federations.admins_of", name=esc(fed['name'])), _item(tr("common.owner"), _owner_link(fed))]
    for admin in await federations.admins(fed["fed_id"]):
        lines.append(f"• {user_link(admin['user_id'], admin['name'])}")
    if len(lines) == 2:
        lines.append("It has no other admins.")
    await reply(message, "\n".join(lines))


@router.message(cmd("fedsubs"))
async def cmd_fedsubs(message: Message, command: CommandObject) -> None:
    fed = await _resolve_fed(message, command.args or "", message.from_user)
    if fed is None:
        return
    subs = await federations.subs_of(fed["fed_id"])
    followers = len(await federations.subscribers_of(fed["fed_id"]))
    lines = [tr("federations.number_of_subscribed_feds", followers=followers), ""]
    if not subs:
        lines.append(f"{_fed_name(fed)} is not subscribed to any federation.")
    else:
        lines.append("Subscribed to the following feds:")
        for sub_id in subs:
            other = await federations.get(sub_id)
            lines.append(f"- {esc(other['name']) if other else tr("common.unknown")} (<code>{sub_id}</code>)")
    await reply(message, "\n".join(lines))


@router.message(cmd("chatfed"))
async def cmd_chatfed(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    fed = await federations.chat_fed(message.chat.id)
    if fed is None:
        await reply(message, tr("federations.this_chat_is_not_in_a_3"))
        return
    await reply(
        message,
        "\n".join([
            tr("federations.chat_federation"),
            _item(tr("federations.name"), f"<code>{esc(fed['name'])}</code>"),
            _item(tr("common.id"), f"<code>{fed['fed_id']}</code>"),
            _item(tr("common.owner"), _owner_link(fed)),
        ]),
    )


@router.message(cmd("joinfed"))
async def cmd_joinfed(message: Message, command: CommandObject) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, tr("federations.use_joinfed_lt_fedid_gt"))
        return
    current = await federations.chat_fed(message.chat.id)
    if current is not None:
        await reply(message, tr("federations.this_chat_is_already_in_leave", current=_fed_name(current)))
        return
    fed = await federations.get(argument.lower())
    if fed is None:
        await reply(message, tr("federations.i_can_t_find_a_federation"))
        return
    await federations.join(message.chat.id, fed["fed_id"])
    await reply(message, tr("federations.this_chat_joined_the_federation_people", fed=_fed_name(fed)))
    await _event(message.bot, fed, lambda: tr("federations.new_fedchat", v=_own_line(message.bot, fed), item=_item(tr("federations.chat"), _chat_ref(message.chat.title, message.chat.username, message.chat.id)), item2=_item(tr("common.chat_id"), f'<code>{message.chat.id}</code>'), item3=_item(tr("federations.by"), user_ref(message.from_user.id, message.from_user.full_name))))


@router.message(cmd("leavefed"))
async def cmd_leavefed(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    fed = await federations.chat_fed(message.chat.id)
    if fed is None:
        await reply(message, tr("federations.this_chat_is_not_in_a_3"))
        return
    await federations.leave(message.chat.id)
    await reply(message, tr("federations.this_chat_left_the_federation_nobody", fed=_fed_name(fed)))
    await _event(message.bot, fed, lambda: tr("federations.un_fedchat", v=_own_line(message.bot, fed), item=_item(tr("federations.chat"), _chat_ref(message.chat.title, message.chat.username, message.chat.id)), item2=_item(tr("common.chat_id"), f'<code>{message.chat.id}</code>'), item3=_item(tr("federations.by"), user_ref(message.from_user.id, message.from_user.full_name))))


@router.message(cmd("quietfed"))
async def cmd_quietfed(message: Message, command: CommandObject, role=None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    argument = (command.args or "").strip().lower()
    if argument and argument not in _ON_OFF:
        await reply(message, tr("federations.use_quietfed_on_or_quietfed_off"))
        return
    right = RIGHT if argument else "can_manage_chat"
    if await moderation.begin_chat_command(message, role, right=right) is None:
        return
    if await federations.chat_fed(message.chat.id) is None:
        await reply(message, tr("federations.this_chat_is_not_in_a_3"))
        return
    if not argument:
        quiet = await federations.is_quiet(message.chat.id)
        await reply(message, tr("federations.quiet_mode_is_i_removals_of", opt=tr("common.on") if quiet else tr("common.off"), opt2=tr("federations.do_not_announce") if quiet else tr("federations.announce")))
        return
    await federations.set_quiet(message.chat.id, _ON_OFF[argument])
    await reply(message, tr("federations.quiet_mode_is_now", opt=tr("common.on") if _ON_OFF[argument] else tr("common.off")))


def _ban_line(row: dict[str, Any]) -> str:
    return f"• <b>{esc(row['name'])}</b>\n  <code>{row['fed_id']}</code>"


@router.message(cmd("fedstat", "fbanstat"))
async def cmd_fedstat(message: Message, command: CommandObject) -> None:
    words = (command.args or "").split()
    fed_id = words.pop().lower() if words and _FED_ID.match(words[-1]) else None
    me = message.from_user
    if words or (replied_message(message) is not None and fed_id is None):
        command = CommandObject(prefix=command.prefix, command=command.command, mention=command.mention, args=" ".join(words) or None)
        target = await _target(message, command, allow_bots=True)
        if target is None:
            return
        user_id, name = target.user_id, target.name
        mine = False
    else:
        if me is None:
            return
        user_id, name, mine = me.id, me.full_name, True
    who = tr("federations.you_are") if mine else tr("federations.is", user_id=user_link(user_id, name))
    if fed_id:
        fed = await federations.get(fed_id)
        if fed is None:
            await reply(message, tr("federations.i_can_t_find_a_federation"))
            return
        ban = await federations.get_ban(fed_id, user_id)
        if ban is None:
            await reply(message, tr("federations.not_banned_in", who=who, fed=_fed_name(fed)))
            return
        lines = [tr("federations.banned_in", who=who, fed=_fed_name(fed)), _item(tr("federations.reason"), f"<code>{esc(ban['reason'] or 'none')}</code>"), _item(tr("federations.banned_by"), esc(ban["banned_by_name"] or "unknown")), _item(tr("federations.date"), f"<code>{_stamp(ban)}</code>")]
        await reply(message, "\n".join(lines))
        return
    found = await federations.banned_in(user_id)
    if not found:
        await reply(message, tr("federations.not_banned_in_any_federation", who=who))
        return
    head = tr("federations.banned_in_federation", who=who, found=len(found), opt='s' if len(found) != 1 else '')
    tail = tr("federations.for_the_reason_use_fedstat_lt") if not mine else tr("federations.for_the_reason_use_fedstat_lt_2")
    text = head + "\n" + "\n".join(_ban_line(r) for r in found) + tail
    if len(text) <= MAX_LIST_CHARS:
        await reply(message, text)
        return
    # Too long for one message: the whole list goes in a file.
    lines = [f"{row['name']}\n  {row['fed_id']}" for row in found]
    await message.bot.send_document(
        message.chat.id,
        BufferedInputFile(("\n".join(lines) + "\n").encode(), filename=f"FedBans_{user_id}.txt"),
        caption=tr("federations.banned_in_federations_the_list_is", who=who, found=len(found)),
        message_thread_id=message.message_thread_id if message.is_topic_message else None,
    )


# --- Enforcement ------------------------------------------------------------------------------

def _first_recently(chat_id: int, user_id: int) -> bool:
    """True the first time a user is dealt with in a chat within a short while (updates arrive in parallel)."""
    now = time.monotonic()
    key = (chat_id, user_id)
    last = _recent.get(key)
    if last is not None and now - last < RECENT_SECONDS:
        return False
    if len(_recent) > 10_000:
        for old in [k for k, t in _recent.items() if now - t >= RECENT_SECONDS]:
            del _recent[old]
    _recent[key] = now
    return True


async def _ban_in(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Ban in one chat. False when Telegram refuses (usually: the bot is not an admin there)."""
    for attempt in (1, 2):
        try:
            await bot.ban_chat_member(chat_id, user_id)
        except TelegramRetryAfter as exc:
            if attempt == 2:
                return False
            await asyncio.sleep(exc.retry_after)
            continue
        except TelegramAPIError as exc:
            log.debug("Could not fedban %s in %s: %s", user_id, chat_id, exc)
            return False
        await federations.record_applied(chat_id, user_id)
        return True
    return False


async def _spared(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Whether federation bans leave this person alone in this chat (immune, staff, approved, admin; not the whitelist)."""
    return await banguard.spared(bot, chat_id, user_id)


async def _lift(bot: Bot, fed_id: str, user_id: int) -> None:
    """Unban where the bot banned because of this federation, unless another federation still bans them there."""
    try:
        reach = set(await federations.chats_of(await federations.reaches(fed_id)))
        # Every chat the ban reached, not only those where the bot remembers banning them.
        targets = dict.fromkeys(await federations.applied_in(user_id))
        for chat_id in reach:
            targets.setdefault(chat_id)
        for chat_id in targets:
            if chat_id not in reach or await federations.effective_ban(chat_id, user_id) is not None:
                continue
            with suppress(TelegramAPIError):
                await bot.unban_chat_member(chat_id, user_id, only_if_banned=True)
            await federations.forget_applied(chat_id, user_id)
            await asyncio.sleep(0.05)
    except Exception:
        log.exception("Lifting a federation ban failed")


async def _banned(bot: Bot, chat_id: int, user) -> dict[str, Any] | bool:
    found = await federations.effective_ban(chat_id, user.id)
    if found is None or await _spared(bot, chat_id, user.id):
        return False
    return {"fed_ban": found}


async def _enforce(bot: Bot, chat_id: int, user, found: tuple[dict[str, Any], dict[str, Any]], thread_id: int | None) -> bool:
    """Remove them and say so. False when that failed (the bot probably can't ban here)."""
    fed, ban = found
    if not _first_recently(chat_id, user.id):
        return True  # another update of theirs is dealing with it
    if not await _ban_in(bot, chat_id, user.id):
        _recent.pop((chat_id, user.id), None)  # try again with their next message
        await _leave_if_powerless(bot, chat_id)
        return False
    if await federations.is_quiet(chat_id):
        return True
    async with i18n.speaking(chat_id):
        lines = [
            tr("federations.is_banned_in_this_chat_s", id=user_ref(user.id, user.full_name), name=esc(fed['name']))
        ]
        if ban["reason"]:
            lines.append(f"{tr('common.reason_label')}: <code>{esc(ban['reason'])}</code>")
    with suppress(TelegramAPIError):
        await bot.send_message(chat_id, "\n".join(lines), message_thread_id=thread_id, link_preview_options=LinkPreviewOptions(is_disabled=True))
    return True


async def _banned_in_message(message: Message, bot: Bot) -> dict[str, Any] | bool:
    user = message.from_user
    if user is None or message.chat.type not in GROUP_TYPES or message.sender_chat is not None:
        return False
    if getattr(message, "new_chat_members", None) or getattr(message, "left_chat_member", None):
        return False  # joins and leaves are handled as member updates
    return await _banned(bot, message.chat.id, user)


@router.message(_banned_in_message)
@router.edited_message(_banned_in_message)
async def enforce_on_message(message: Message, bot: Bot, fed_ban: tuple) -> None:
    with suppress(TelegramAPIError):
        await message.delete()
    thread = message.message_thread_id if message.is_topic_message else None
    await _enforce(bot, message.chat.id, message.from_user, fed_ban, thread)


def _moved(event: ChatMemberUpdated) -> bool:
    """Joining or leaving, as in KryoRobot: a ban (by anyone) or another change is not acted on."""
    def value(member) -> str:
        return str(getattr(member.status, "value", member.status))

    before, after = value(event.old_chat_member), value(event.new_chat_member)
    return (after == "member" and before != "member") or (after == "left" and before == "member")


async def _banned_active(event: ChatMemberUpdated, bot: Bot) -> dict[str, Any] | bool:
    user = event.new_chat_member.user
    if event.chat.type not in GROUP_TYPES or user.id == bot.id or not _moved(event):
        return False
    return await _banned(bot, event.chat.id, user)


class JoinMiddleware(BaseMiddleware):
    """
    The "X joined" service message: newcomers banned in the chat's federation are removed from it too,
    so that a chat in which member updates do not reach the bot (they come only when the bot is an
    admin that can see them) still gets them removed. When nobody but banned users joined, the
    message goes no further, so there is no welcome or captcha for them.
    """

    async def __call__(self, handler: Callable[[Any, dict[str, Any]], Awaitable[Any]], event: Any, data: dict[str, Any]) -> Any:
        members = getattr(event, "new_chat_members", None)
        if members and getattr(event.chat, "type", None) in GROUP_TYPES:
            bot = data.get("bot") or event.bot
            removed = 0
            try:
                for user in members:
                    if user.id == bot.id:
                        continue
                    found = await _banned(bot, event.chat.id, user)
                    if not found:
                        continue
                    thread = event.message_thread_id if event.is_topic_message else None
                    if await _enforce(bot, event.chat.id, user, found["fed_ban"], thread):
                        removed += 1
            except Exception:
                log.exception("Handling a join for federation bans failed")
            if removed and removed == len(members):
                await banguard.clean_join_message(event)  # greetings would have, but it never sees this message
                return None
        return await handler(event, data)


router.message.outer_middleware(JoinMiddleware())


@router.chat_member(_banned_active)
async def enforce_on_member_change(event: ChatMemberUpdated, bot: Bot, fed_ban: tuple) -> None:
    await _enforce(bot, event.chat.id, event.new_chat_member.user, fed_ban, None)


async def _banned_reacting(reaction: MessageReactionUpdated, bot: Bot) -> dict[str, Any] | bool:
    if reaction.user is None or reaction.chat.type not in GROUP_TYPES:
        return False
    return await _banned(bot, reaction.chat.id, reaction.user)


@router.message_reaction(_banned_reacting)
async def enforce_on_reaction(reaction: MessageReactionUpdated, bot: Bot, fed_ban: tuple) -> None:
    await _enforce(bot, reaction.chat.id, reaction.user, fed_ban, None)


# --- Leaving the federation when I can no longer ban ----------------------------------------------

def _can_ban(member: Any) -> bool:
    """Whether this membership lets the bot remove people: the owner, or an admin with that right."""
    status = getattr(member, "status", None)
    if status == "creator":
        return True
    return status == "administrator" and bool(getattr(member, "can_restrict_members", False))


async def _leave_federation(bot: Bot, chat_id: int, title: str | None, username: str | None = None) -> bool:
    """Take the chat out of its federation because I can't act for it. False when it was in none."""
    fed = await federations.chat_fed(chat_id)
    if fed is None:
        return False
    await federations.leave(chat_id)
    if title is None:
        with suppress(Exception):  # a name for the log, if Telegram still tells me
            found = await bot.get_chat(chat_id)
            title, username = found.title, found.username
    with suppress(TelegramAPIError):
        async with i18n.speaking(chat_id):
            notice = tr("federations.i_can_t_remove_people_from", fed=_fed_name(fed))
        await bot.send_message(chat_id, notice)
    await _event(
        bot,
        fed,
        lambda: tr("federations.un_fedchat", v=_own_line(bot, fed), item=_item(tr("federations.chat"), _chat_ref(title, username, chat_id)), item2=_item(tr("common.chat_id"), f'<code>{chat_id}</code>'), item3=_item(tr("federations.reason"), tr("federations.i_lost_the_right_to_ban"))),
    )
    return True


async def _leave_if_powerless(bot: Bot, chat_id: int) -> None:
    """A ban just failed: if that is because I lost my rights here, the chat leaves its federation."""
    try:
        member = await moderation.get_member(bot, chat_id, bot.id)
        if member is None or _can_ban(member):
            return  # can't tell, or the failure was something else (flood limit, an admin, ...)
        await _leave_federation(bot, chat_id, None)
    except Exception:
        log.exception("Checking my rights in %s failed", chat_id)


@router.my_chat_member()
async def on_my_rights_changed(event: ChatMemberUpdated, bot: Bot) -> None:
    if event.chat.type not in GROUP_TYPES or event.new_chat_member.user.id != bot.id:
        return
    if _can_ban(event.new_chat_member):
        return
    try:
        await _leave_federation(bot, event.chat.id, event.chat.title, event.chat.username)
    except Exception:
        log.exception("Leaving the federation of %s failed", event.chat.id)


# --- /info ------------------------------------------------------------------------------------

async def _info_notes(bot: Bot, user_id: int, chat_id: int | None) -> list[str]:
    """What /info says about a person in a chat that belongs to a federation."""
    if chat_id is None:
        return []
    fed = await federations.chat_fed(chat_id)
    if fed is None:
        return []
    if await federations.is_admin(fed, user_id):
        return [tr("federations.this_user_is_a_fed_admin", fed=_fed_name(fed))]
    if await roles.get_role(user_id) is not None or await moderation.is_chat_admin(bot, chat_id, user_id):
        return []  # staff and admins are not touched by federation bans: nothing to say
    found = await federations.effective_ban(chat_id, user_id)
    if found is None:
        return [tr("federations.this_user_is_not_fbanned_in")]
    banned_in, ban = found
    text = tr("federations.this_user_has_been_fbanned_in", banned_in=_fed_name(banned_in))
    if ban["reason"]:
        text += tr("common.reason", reason=esc(ban['reason']))
    return [text]


info_hooks.register_notes("federations", _info_notes)
