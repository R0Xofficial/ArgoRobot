"""Federations: one ban list for many chats"""

import asyncio
from types import SimpleNamespace
import logging
import re
import time
from contextlib import suppress
from typing import Any

from aiogram import Bot, F, Router
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
from core import approvals, federations, immunity, info_hooks, moderation, roles
from core.federations import FederationError
from core.help_index import Section
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, ModerationError
from core.targets import TargetError, replied_message, resolve_target
from core.text import esc, user_link, user_ref, SWITCH as _ON_OFF

log = logging.getLogger(__name__)

# After global bans (-100): a federation ban removes someone before anything else looks at them.
PRIORITY = -95

ABOUT = """
A federation links chats so that one ban works in all of them. Create one, let your chats join it, and everyone it bans is removed from every chat of the federation as soon as they show up there (write, edit, react, join or leave). A federation can also subscribe to others and inherit their bans.

A chat is in one federation at a time. Chat admins, approved users and my staff are never removed by federation bans, and federation admins can't be banned in their own federation. Federations are identified by an ID like 3f1c2a3e-...: share it with the owners of the chats that should join.
"""

OWNER = "federation owner"
FED_ADMINS = "fed admins"
S_OWNER, S_ADMIN, S_USER = Section("FedOwner Commands"), Section("FedAdmin Commands"), Section("Users Commands")

HELP = [
    ("/newfed <name>", "Create a federation. You can own one. Private chat only.", S_OWNER, OWNER),
    ("/renamefed <name>", "Rename your federation. Private chat only.", S_OWNER, OWNER),
    ("/delfed", "Delete your federation. Nobody is unbanned in their chats. Private chat only.", S_OWNER, OWNER),
    ("/fedtransfer <user>", "Give your federation to a user, who has to accept. Private chat only.", S_OWNER, OWNER),
    ("/fedpromote <user>", "Make a user a federation admin. They have to accept. Alias: /fpromote.", S_OWNER, OWNER),
    ("/feddemote <user>", "Take a federation admin's rights away. Alias: /fdemote.", S_OWNER, OWNER),
    ("/fednotif <on|off>", "Get a private message about every action.", S_OWNER, OWNER),
    ("/fedreason <on|off>", "Make federation bans require a reason. Private chat only.", S_OWNER, OWNER),
    ("/subfed <FedID>", "Inherit the bans of another federation.", S_OWNER, OWNER),
    ("/unsubfed <FedID>", "Stop inheriting its bans.", S_OWNER, OWNER),
    ("/fedexport [csv|minicsv|json|human]", "Get the ban list as a file. The default is csv.", S_OWNER, OWNER),
    ("/fedimport [overwrite|keep] [csv|minicsv|json|human]", "Reply to a file (or send it with the command) to import bans. Keep, the default, keeps existing ones; overwrite replaces the list. JSON files with one ban per line work too.", S_OWNER, OWNER),
    ("/setfedlog", "Use this chat as the log of your federation.", S_OWNER, OWNER),
    ("/unsetfedlog", "Stop logging your federation in a chat.", S_OWNER, OWNER),
    ("/fban <user> [reason]", "Ban a user in this chat's federation, and in those subscribed to it. If the user is already banned, you are asked whether to update the reason.", S_ADMIN, FED_ADMINS),
    ("/unfban <user>", "Lift a federation ban.", S_ADMIN, FED_ADMINS),
    ("/feddemoteme <FedID>", "Give up your admin rights in a federation.", S_ADMIN, FED_ADMINS),
    ("/myfeds", "List the federations you own or administer. Private chat only.", S_ADMIN, FED_ADMINS),
    ("/fedinfo [FedID]", "Show a federation: owner, admins, chats, bans. Without an ID it shows the one you own.", S_USER),
    ("/fedadmins [FedID]", "List the owner and admins of a federation.", S_USER),
    ("/fedsubs [FedID]", "List the federations a federation is subscribed to.", S_USER),
    ("/joinfed <FedID>", "Join this chat to a federation. A chat is in one federation at a time.", "chat owner", S_USER),
    ("/leavefed", "Take this chat out of its federation.", "chat owner", S_USER),
    ("/chatfed", "Show the federation this chat is in.", S_USER),
    ("/quietfed <on|off>", "Choose whether I announce removals of fedbanned users here.", "admins", S_USER),
    ("/fedstat [user] [FedID]", "The federations you or another user are banned in. Add a FedID to see the reason and details of that ban. Alias: /fbanstat.", S_USER),
]

HELP_NOTES = [
    (
        "Getting started",
        """
        1. Create it: /newfed My Network
        2. Add your chats: send /joinfed <FedID> in each one (the chat owner does this).
        3. Ban from any of them: reply to a spammer with /fban reason
        Other federations' bans can be inherited with /subfed <FedID>.
        """,
    ),
    (
        "Files",
        """
        csv - user_id,name,reason,banned_by,banned_at
        minicsv - user_id,reason
        json - a list of {"user_id": ..., "name": ..., "reason": ...}
        human - one line per user, like: Name [123456] - reason
        """,
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
    return "Yes" if value else "No"


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
        await reply(message, "Send this as yourself, not as an anonymous admin or a channel.")
        return None
    return user


async def _private(message: Message) -> bool:
    """Owner commands are only taken in a private chat with the bot (otherwise False, after saying so)."""
    if message.chat.type == "private":
        return True
    await reply(message, "This command only works in a private chat with me. Send it to me there.")
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
        await reply(message, "You don't own a federation. Create one with /newfed &lt;name&gt;.")
        return None
    return user, fed


def _fed_name(fed: dict[str, Any]) -> str:
    return f"<b>{esc(fed['name'])}</b>"


_tags: dict[str, tuple[float, str | None]] = {}
TAG_SECONDS = 600


async def _fed_tag(bot: Bot, fed: dict[str, Any] | None) -> str | None:
    """@username of a federation: that of its log chat, when the log is a public chat."""
    if not fed or not fed.get("log_chat_id"):
        return None
    key = str(fed.get("fed_id") or fed["log_chat_id"])
    cached = _tags.get(key)
    if cached is not None and time.monotonic() - cached[0] < TAG_SECONDS:
        return cached[1]
    tag = None
    try:
        username = (await bot.get_chat(fed["log_chat_id"])).username
        tag = f"@{username}" if username else None
    except Exception:
        pass
    _tags[key] = (time.monotonic(), tag)
    return tag


async def _own_line(bot: Bot, fed: dict[str, Any]) -> str:
    """FedName: Knox Federation | @KnoxFederation"""
    tag = await _fed_tag(bot, fed)
    return _item("FedName", f"{esc(fed['name'])} | {esc(tag)}" if tag else esc(fed["name"]))


async def _other_lines(bot: Bot, label: str, fed: dict[str, Any] | None, fed_id: str | None = None) -> str:
    """The other federation of a subscription, as two lines: <label>: ALT-F4 (@ALTF4Fed) and <label>ID: <code>id</code>."""
    tag = await _fed_tag(bot, fed)
    name = esc(fed["name"]) if fed else "that federation"
    shown = f"{name} ({esc(tag)})" if tag else name
    identifier = fed["fed_id"] if fed else fed_id
    return f"{_item(label, shown)}\n{_item(label + 'ID', f'<code>{esc(identifier)}</code>')}"


def _owner_link(fed: dict[str, Any]) -> str:
    return user_link(fed["owner_id"], fed["owner_name"])


async def _event(bot: Bot, fed: dict[str, Any], text: str) -> None:
    """Tell the log chat of the federation, and its owner when they asked for private messages."""
    sent = False
    if fed["log_chat_id"]:
        try:
            await bot.send_message(fed["log_chat_id"], text, link_preview_options=LinkPreviewOptions(is_disabled=True))
            sent = True
        except TelegramAPIError as exc:
            log.debug("Federation log %s unreachable: %s", fed["log_chat_id"], exc)
    if fed["notify"] and not (sent and fed["log_chat_id"] == fed["owner_id"]):
        with suppress(TelegramAPIError):
            await bot.send_message(fed["owner_id"], text, link_preview_options=LinkPreviewOptions(is_disabled=True))


async def _admin_event(bot: Bot, fed: dict[str, Any], promoted: bool, user_id: int, who: str, by: str | None = None) -> None:
    """Tell the federation's log that someone became or stopped being one of its admins (`by`: who did it)."""
    title, label = ("New Fed Promotion", "Promoted FedAdmin") if promoted else ("New Fed Demotion", "Demoted FedAdmin")
    text = f"<b>{title}</b>\n{await _own_line(bot, fed)}\n{_item(label, who)}\n{_item('User ID', f'<code>{user_id}</code>')}"
    if by:
        text += f"\n{_item('By', by)}"
    await _event(bot, fed, text)


async def _resolve_fed(message: Message, argument: str, user) -> dict[str, Any] | None:
    """The federation an info command is about: the ID given, else the chat's, else the person's own."""
    argument = argument.strip()
    if argument:
        if not _FED_ID.match(argument):
            await reply(message, "That is not a federation ID. They look like 3f1c2a3e-1111-2222-3333-444455556666.")
            return None
        fed = await federations.get(argument.lower())
        if fed is None:
            await reply(message, "I can't find a federation with that ID.")
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
            await reply(message, "You are in several federations. Give the ID of the one you mean.")
            return None
    await reply(message, "This chat is not in a federation. Give a federation ID.")
    return None


async def _fed_to_ban_in(message: Message, user) -> dict[str, Any] | None:
    """The federation a fed admin acts in: the chat's, or in private the only one they administer."""
    if message.chat.type in GROUP_TYPES:
        fed = await federations.chat_fed(message.chat.id)
        if fed is None:
            await reply(message, "This chat is not in a federation. The chat owner can join one with /joinfed.")
            return None
        if not await federations.is_admin(fed, user.id):
            await reply(message, "You are not an admin of this chat's federation.")
            return None
        return fed
    mine = await federations.feds_of(user.id)
    if not mine:
        await reply(message, "You are not an admin of any federation.")
        return None
    if len(mine) > 1:
        await reply(message, "You administer several federations. Use this command in a chat that is in the one you mean.")
        return None
    return mine[0]


async def _target(message: Message, command: CommandObject):
    try:
        return await resolve_target(message, command)
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
        await reply(message, "Use /newfed &lt;name&gt;.")
        return
    try:
        fed = await federations.create(command.args, user.id, user.full_name)
    except FederationError as exc:
        await reply(message, esc(exc))
        return
    await reply(
        message,
        "<b>Federation created</b>\n"
        + "\n".join([_item("Name", f"<code>{esc(fed['name'])}</code>"), _item("ID", f"<code>{fed['fed_id']}</code>")])
        + "\n\nJoin a chat to it with /joinfed &lt;ID&gt; sent by the chat's owner.",
    )


@router.message(cmd("renamefed"))
async def cmd_renamefed(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message, private=True)
    if mine is None:
        return
    _, fed = mine
    if not (command.args or "").strip():
        await reply(message, "Use /renamefed &lt;name&gt;.")
        return
    try:
        name = await federations.rename(fed["fed_id"], command.args)
    except FederationError as exc:
        await reply(message, esc(exc))
        return
    await reply(message, f"Your federation is now called <b>{esc(name)}</b>.")


@router.message(cmd("delfed"))
async def cmd_delfed(message: Message) -> None:
    mine = await _own_fed(message, private=True)
    if mine is None:
        return
    user, fed = mine
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[
            InlineKeyboardButton(text="Delete", callback_data=f"{CB_DELETE}y:{user.id}"),
            InlineKeyboardButton(text="Cancel", callback_data=f"{CB_DELETE}n:{user.id}"),
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
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    if callback.from_user.id != int(owner):
        await callback.answer("Only the owner of the federation can do this.", show_alert=True)
        return
    fed = await federations.owned_by(callback.from_user.id)
    await callback.answer()
    if answer != "y" or fed is None:
        await edit(message, "Cancelled. Nothing was deleted." if answer != "y" else "You no longer own a federation.", reply_markup=None)
        return
    await federations.delete(fed["fed_id"])
    await edit(message, f"The federation {_fed_name(fed)} is deleted.", reply_markup=None)


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
        await reply(message, "A federation can only belong to a person.")
        return
    if target.user_id == user.id:
        await reply(message, "You already own it.")
        return
    if await federations.owned_by(target.user_id) is not None:
        await reply(message, "That person already owns a federation.")
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[
            InlineKeyboardButton(text="Accept", callback_data=f"{CB_TRANSFER}y:{target.user_id}:{user.id}"),
            InlineKeyboardButton(text="Decline", callback_data=f"{CB_TRANSFER}n:{target.user_id}:{user.id}"),
        ]]
    )
    await reply(
        message,
        f"{user_link(target.user_id, target.name)}, {user_link(user.id, user.full_name)} wants to give you the federation {_fed_name(fed)}. Do you accept?",
        reply_markup=keyboard,
    )


@router.callback_query(F.data.startswith(CB_TRANSFER))
async def cb_fedtransfer(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, new_id, old_id = callback.data.split(":")
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    giver = callback.from_user.id == int(old_id)
    if callback.from_user.id != int(new_id) and not (giver and answer != "y"):
        await callback.answer("Only the person it is offered to can answer. The owner can only decline.", show_alert=True)
        return
    await callback.answer()
    fed = await federations.owned_by(int(old_id))
    if answer != "y":
        await edit(message, "The transfer was cancelled by the owner." if giver else "The transfer was declined.", reply_markup=None)
        return
    if fed is None:
        await edit(message, "That federation is no longer theirs to give.", reply_markup=None)
        return
    try:
        await federations.transfer(fed["fed_id"], callback.from_user.id, callback.from_user.full_name)
    except FederationError as exc:
        await edit(message, esc(exc), reply_markup=None)
        return
    await edit(message, f"The federation {_fed_name(fed)} now belongs to {user_link(callback.from_user.id, callback.from_user.full_name)}.", reply_markup=None)
    await _event(
        callback.bot,
        {**fed, "owner_id": callback.from_user.id},
        f"<b>New FedOwner</b>\n{await _own_line(callback.bot, fed)}\n{_item('Previous owner', user_ref(fed['owner_id'], fed['owner_name']))}\n{_item('New owner', user_ref(callback.from_user.id, callback.from_user.full_name))}",
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
        await reply(message, "Only people can be federation admins.")
        return
    if await federations.is_admin(fed, target.user_id):
        await reply(message, "That person already is an admin of your federation.")
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[
            InlineKeyboardButton(text="Accept", callback_data=f"{CB_PROMOTE}y:{target.user_id}:{fed['fed_id']}"),
            InlineKeyboardButton(text="Decline", callback_data=f"{CB_PROMOTE}n:{target.user_id}:{fed['fed_id']}"),
        ]]
    )
    await reply(
        message,
        f"{user_link(target.user_id, target.name)}, {user_link(user.id, user.full_name)} wants to make you an admin of the federation {_fed_name(fed)}. Do you accept?",
        reply_markup=keyboard,
    )


@router.callback_query(F.data.startswith(CB_PROMOTE))
async def cb_fedpromote(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, user_id, fed_id = callback.data.split(":")
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    fed = await federations.get(fed_id)
    giver = fed is not None and callback.from_user.id == fed["owner_id"]
    if callback.from_user.id != int(user_id) and not (giver and answer != "y"):
        await callback.answer("Only the person it is offered to can answer. The owner can only decline.", show_alert=True)
        return
    await callback.answer()
    if answer != "y":
        await edit(message, "The offer was cancelled by the owner." if giver else "The offer was declined.", reply_markup=None)
        return
    if fed is None:
        await edit(message, "That federation no longer exists.", reply_markup=None)
        return
    try:
        await federations.add_admin(fed_id, callback.from_user.id, callback.from_user.full_name)
    except FederationError as exc:
        await edit(message, esc(exc), reply_markup=None)
        return
    who = user_link(callback.from_user.id, callback.from_user.full_name)
    await edit(message, f"{who} is now an admin of the federation {_fed_name(fed)}.", reply_markup=None)
    await _admin_event(callback.bot, fed, True, callback.from_user.id, who, user_ref(fed["owner_id"], fed["owner_name"]))


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
        await reply(message, "That person is not an admin of your federation.")
        return
    who = user_link(target.user_id, target.name)
    await reply(message, f"{who} is no longer an admin of {_fed_name(fed)}.")
    await _admin_event(message.bot, fed, False, target.user_id, who, user_ref(message.from_user.id, message.from_user.full_name))


@router.message(cmd("feddemoteme"))
async def cmd_feddemoteme(message: Message, command: CommandObject) -> None:
    user = await _person(message)
    if user is None:
        return
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, "Use /feddemoteme &lt;FedID&gt;.")
        return
    fed = await federations.get(argument.lower())
    if fed is None:
        await reply(message, "I can't find a federation with that ID.")
        return
    if fed["owner_id"] == user.id:
        await reply(message, "You own that federation. Transfer it with /fedtransfer or delete it with /delfed.")
        return
    if not await federations.remove_admin(fed["fed_id"], user.id):
        await reply(message, "You are not an admin of that federation.")
        return
    await reply(message, f"You are no longer an admin of {_fed_name(fed)}.")
    await _admin_event(message.bot, fed, False, user.id, user_link(user.id, user.full_name), user_ref(user.id, user.full_name))


async def _toggle(message: Message, command: CommandObject, field: str, name: str) -> None:
    mine = await _own_fed(message, private=field == "require_reason")
    if mine is None:
        return
    _, fed = mine
    argument = (command.args or "").strip().lower()
    if not argument:
        await reply(message, f"{name} is <b>{'on' if fed[field] else 'off'}</b> for {_fed_name(fed)}.")
        return
    if argument not in _ON_OFF:
        await reply(message, "Use on or off.")
        return
    await federations.update(fed["fed_id"], **{field: _ON_OFF[argument]})
    await reply(message, f"{name} is now <b>{'on' if _ON_OFF[argument] else 'off'}</b> for {_fed_name(fed)}.")


@router.message(cmd("fednotif"))
async def cmd_fednotif(message: Message, command: CommandObject) -> None:
    await _toggle(message, command, "notify", "Private notifications")


@router.message(cmd("fedreason"))
async def cmd_fedreason(message: Message, command: CommandObject) -> None:
    await _toggle(message, command, "require_reason", "Requiring a reason")


@router.message(cmd("subfed"))
async def cmd_subfed(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, "Use /subfed &lt;FedID&gt;.")
        return
    other = await federations.get(argument.lower())
    if other is None:
        await reply(message, "I can't find a federation with that ID.")
        return
    try:
        await federations.subscribe(fed["fed_id"], other["fed_id"])
    except FederationError as exc:
        await reply(message, esc(exc))
        return
    await reply(message, f"{_fed_name(fed)} now inherits the bans of {_fed_name(other)}. Your own ban list is not changed.")
    by = _item("By", user_ref(message.from_user.id, message.from_user.full_name))
    bot = message.bot
    await _event(bot, fed, f"<b>New FedSubscribed</b>\n{await _own_line(bot, fed)}\n{await _other_lines(bot, 'SubbedFed', other)}\n{by}")
    await _event(bot, other, f"<b>New FedSubscriber</b>\n{await _own_line(bot, other)}\n{await _other_lines(bot, 'SubbedFed', fed)}\n{by}")


@router.message(cmd("unsubfed"))
async def cmd_unsubfed(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    argument = (command.args or "").strip()
    if not _FED_ID.match(argument):
        await reply(message, "Use /unsubfed &lt;FedID&gt;.")
        return
    other = await federations.get(argument.lower())
    if not await federations.unsubscribe(fed["fed_id"], argument.lower()):
        await reply(message, "Your federation is not subscribed to that one.")
        return
    name = _fed_name(other) if other else "that federation"
    await reply(message, f"{_fed_name(fed)} no longer inherits the bans of {name}.")
    by = _item("By", user_ref(message.from_user.id, message.from_user.full_name))
    bot = message.bot
    await _event(bot, fed, f"<b>Un-FedSubscribed</b>\n{await _own_line(bot, fed)}\n{await _other_lines(bot, 'UnsubbedFed', other, argument.lower())}\n{by}")
    if other is not None:
        await _event(bot, other, f"<b>Un-FedSubscriber</b>\n{await _own_line(bot, other)}\n{await _other_lines(bot, 'UnsubbedFed', fed)}\n{by}")


@router.message(cmd("setfedlog"))
async def cmd_setfedlog(message: Message) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "Send this in the group that should be the log.")
        return
    await federations.update(fed["fed_id"], log_chat_id=message.chat.id)
    await reply(message, f"This chat is now the log of {_fed_name(fed)}.")


@router.message(cmd("unsetfedlog"))
async def cmd_unsetfedlog(message: Message) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    _, fed = mine
    if not fed["log_chat_id"]:
        await reply(message, "Your federation has no log chat.")
        return
    await federations.update(fed["fed_id"], log_chat_id=None)
    await reply(message, f"{_fed_name(fed)} is no longer logged in a chat.")


# --- Export and import ------------------------------------------------------------------------

@router.message(cmd("fedexport"))
async def cmd_fedexport(message: Message, command: CommandObject) -> None:
    mine = await _own_fed(message)
    if mine is None:
        return
    user, fed = mine
    fmt = (command.args or "csv").strip().lower() or "csv"
    if fmt not in FORMATS:
        await reply(message, "Use /fedexport csv, minicsv, json or human.")
        return
    now = time.monotonic()
    if now - _exported.get(user.id, -EXPORT_COOLDOWN) < EXPORT_COOLDOWN:
        await reply(message, "Wait a little before exporting again.")
        return
    rows = await federations.bans(fed["fed_id"])
    if not rows:
        await reply(message, "Your federation has no bans to export.")
        return
    _exported[user.id] = now
    data, extension = render_export(rows, fmt)
    await message.bot.send_document(
        message.chat.id,
        BufferedInputFile(data, filename=f"fedbans_{fed['fed_id'][:8]}.{extension}"),
        caption=f"{len(rows)} bans of {esc(fed['name'])}.",
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
        await reply(message, "Use /fedimport [overwrite|keep] [csv|minicsv|json|human], as a reply to a file.")
        return
    document = getattr(message, "document", None) or getattr(replied_message(message), "document", None)
    if document is None:
        await reply(message, "Reply to a file with the bans to import, or send the file with the command as its caption.")
        return
    if (document.file_size or 0) > MAX_IMPORT_BYTES:
        await reply(message, "That file is too big: at most 20 MB.")
        return
    try:
        raw = (await message.bot.download(document.file_id)).read()
        rows = parse_import(raw.decode("utf-8", errors="replace"), fmt)
    except ValueError as exc:
        await reply(message, esc(exc))
        return
    except TelegramAPIError:
        await reply(message, "I couldn't download that file.")
        return
    status = await reply(message, f"Importing {len(rows)} bans... Please wait!") if len(rows) > 2000 else None
    admins = {a["user_id"] for a in await federations.admins(fed["fed_id"])} | {fed["owner_id"], message.bot.id}
    kept = [r for r in rows if r[0] not in admins and not immunity.is_immune(r[0])]
    added = await federations.import_bans(fed["fed_id"], kept, overwrite, user.id, user.full_name)
    skipped = len(rows) - added
    send = (lambda m, text: edit(status, text)) if status is not None else reply
    await send(
        message,
        f"Imported <b>{added}</b> ban{'s' if added != 1 else ''} into {_fed_name(fed)}."
        + (f" Skipped <b>{skipped}</b> (already banned, or admins)." if skipped else "")
        + "\nPeople on the list are removed from the chats when they write or join.",
    )
    await _event(message.bot, fed, f"<b>New FedBans</b>\n{await _own_line(message.bot, fed)}\n{_item('Added', f'<code>{added}</code>')}\n{_item('By', user_ref(user.id, user.full_name))}")


# --- Fed admin commands -----------------------------------------------------------------------

async def _protected(bot: Bot, fed: dict[str, Any], target) -> str | None:
    """Why this target can't be banned in the federation, or None."""
    if target.is_chat:
        return "Only people can be banned in a federation."
    if target.user_id == bot.id:
        return "I'm not banning myself."
    if immunity.is_immune(target.user_id) or await roles.get_role(target.user_id) is not None:
        return "That account is protected and can't be banned."
    if await federations.is_admin(fed, target.user_id):
        return "That person is an admin of the federation and can't be banned in it."
    return None


def _ban_text(title: str, fed: dict[str, Any], admin, target_id: int, target_name: str | None, reason: str | None, fed_line: str | None = None) -> str:
    lines = [
        f"<b>{title}</b>",
        fed_line or _item("FedName", esc(fed['name'])),
        _item("FedAdmin", user_ref(admin.id, admin.full_name)),
        _item("User", user_link(target_id, target_name)),
        _item("User ID", f"<code>{target_id}</code>"),
    ]
    if reason:
        lines.append(_item("Reason", f"<code>{esc(reason)}</code>"))
    return "\n".join(lines)


@router.message(cmd("fban"))
async def cmd_fban(message: Message, command: CommandObject) -> None:
    user = await _person(message)
    if user is None:
        return
    fed = await _fed_to_ban_in(message, user)
    if fed is None:
        return
    target = await _target(message, command)
    if target is None:
        return
    problem = await _protected(message.bot, fed, target)
    if problem:
        await reply(message, problem)
        return
    reason = target.extra.strip()
    if fed["require_reason"] and not reason:
        await reply(message, "This federation requires a reason: /fban &lt;user&gt; &lt;reason&gt;.")
        return
    existing = await federations.get_ban(fed["fed_id"], target.user_id)
    if existing is not None:
        who = user_link(target.user_id, target.name)
        if not reason or reason == (existing["reason"] or ""):
            await reply(message, f"User {who} is already fbanned in {_fed_name(fed)}" + (f", with reason:\n{esc(existing['reason'])}" if existing["reason"] else "."))
            return
        keyboard = InlineKeyboardMarkup(
            inline_keyboard=[[
                InlineKeyboardButton(text="Update reason", callback_data=f"{CB_REASON}y:{fed['fed_id']}:{target.user_id}"),
                InlineKeyboardButton(text="Cancel", callback_data=f"{CB_REASON}n:{fed['fed_id']}:{target.user_id}"),
            ]]
        )
        sent = await reply(
            message,
            f"User {who} is already fbanned in {_fed_name(fed)}"
            + (f", with reason:\n{esc(existing['reason'])}" if existing["reason"] else ", with no reason.")
            + f"\n\nWould you like to update this reason?\n\nNew reason:\n{esc(reason)}",
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
    text = _ban_text("New FedBan", fed, user, target.user_id, target.name, reason, await _own_line(message.bot, fed))
    await reply(message, text)
    await _event(message.bot, fed, text)


@router.callback_query(F.data.startswith(CB_REASON))
async def cb_fedreason_update(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    _, answer, fed_id, user_id = callback.data.split(":")
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    fed = await federations.get(fed_id)
    if fed is None or not await federations.is_admin(fed, callback.from_user.id):
        await callback.answer("Only the admins of this federation can answer.", show_alert=True)
        return
    key = (message.chat.id, message.message_id)
    pending = _pending_reasons.pop(key, None)
    await callback.answer()
    if answer != "y":
        await edit(message, "The reason was not changed.", reply_markup=None)
        return
    if pending is None or time.monotonic() - pending[4] > REASON_TTL:
        await edit(message, "This request has expired. Send /fban again.", reply_markup=None)
        return
    name, reason, by_id, by_name, _ = pending
    await federations.ban(fed_id, int(user_id), name, reason, by_id, by_name)
    text = _ban_text("FedBan Reason Update", fed, SimpleNamespace(id=by_id, full_name=by_name), int(user_id), name, reason, await _own_line(callback.bot, fed))
    await edit(message, text, reply_markup=None)
    await _event(callback.bot, fed, text)


@router.message(cmd("unfban"))
async def cmd_unfban(message: Message, command: CommandObject) -> None:
    user = await _person(message)
    if user is None:
        return
    fed = await _fed_to_ban_in(message, user)
    if fed is None:
        return
    target = await _target(message, command)
    if target is None:
        return
    if await federations.unban(fed["fed_id"], target.user_id) is None:
        await reply(message, f"{user_link(target.user_id, target.name)} is not banned in {_fed_name(fed)}.")
        return
    text = _ban_text("FedBan Revoked", fed, user, target.user_id, target.name, None, await _own_line(message.bot, fed))
    await reply(message, text)
    await _event(message.bot, fed, text)
    _spawn(_lift(message.bot, fed["fed_id"], target.user_id))


@router.message(cmd("myfeds"))
async def cmd_myfeds(message: Message) -> None:
    if not await _private(message):
        return
    user = await _person(message)
    if user is None:
        return
    mine = await federations.feds_of(user.id)
    if not mine:
        await reply(message, "You are not an admin of any federation.")
        return
    lines = ["<b>Your federations</b>"]
    for fed in mine:
        role = "Owner" if fed["owner_id"] == user.id else "Admin"
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
            await reply(message, "You don't own a federation. Give the ID of the one you mean.")
    if fed is None:
        return
    fed_id = fed["fed_id"]
    subs = await federations.subs_of(fed_id)
    lines = [
        "<b>Fed Info</b>",
        _item("FedID", f"<code>{fed_id}</code>"),
        _item("Name", esc(fed["name"])),
        _item("Owner", f"{_owner_link(fed)} [<code>{fed['owner_id']}</code>]"),
        _item("Admins", f"<code>{len(await federations.admins(fed_id))}</code>"),
        _item("Banned users", f"<code>{await federations.ban_count(fed_id)}</code>"),
        _item("Connected chats", f"<code>{await federations.chat_count(fed_id)}</code>"),
        _item("Subscribers", f"<code>{len(await federations.subscribers_of(fed_id))}</code>"),
        "",
    ]
    if subs:
        lines.append("<b>Subscribed to:</b>")
        for sub_id in subs:
            other = await federations.get(sub_id)
            lines.append(f"• {esc(other['name']) if other else 'Unknown'} (<code>{sub_id}</code>)")
    else:
        lines.append("Not subscribed to any federation.")
    await reply(message, "\n".join(lines))


@router.message(cmd("fedadmins"))
async def cmd_fedadmins(message: Message, command: CommandObject) -> None:
    fed = await _resolve_fed(message, command.args or "", message.from_user)
    if fed is None:
        return
    lines = [f"<b>Admins of {esc(fed['name'])}</b>", _item("Owner", _owner_link(fed))]
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
    lines = [f"Number of subscribed feds: {followers}", ""]
    if not subs:
        lines.append(f"{_fed_name(fed)} is not subscribed to any federation.")
    else:
        lines.append("Subscribed to the following feds:")
        for sub_id in subs:
            other = await federations.get(sub_id)
            lines.append(f"- {esc(other['name']) if other else 'Unknown'} (<code>{sub_id}</code>)")
    await reply(message, "\n".join(lines))


@router.message(cmd("chatfed"))
async def cmd_chatfed(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    fed = await federations.chat_fed(message.chat.id)
    if fed is None:
        await reply(message, "This chat is not in a federation.")
        return
    await reply(
        message,
        "\n".join([
            "<b>Chat Federation</b>",
            _item("Name", f"<code>{esc(fed['name'])}</code>"),
            _item("ID", f"<code>{fed['fed_id']}</code>"),
            _item("Owner", _owner_link(fed)),
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
        await reply(message, "Use /joinfed &lt;FedID&gt;.")
        return
    current = await federations.chat_fed(message.chat.id)
    if current is not None:
        await reply(message, f"This chat is already in {_fed_name(current)}. Leave it first with /leavefed.")
        return
    fed = await federations.get(argument.lower())
    if fed is None:
        await reply(message, "I can't find a federation with that ID.")
        return
    await federations.join(message.chat.id, fed["fed_id"])
    await reply(message, f"This chat joined the federation {_fed_name(fed)}. People it bans are removed from here.")
    await _event(message.bot, fed, f"<b>New FedChat</b>\n{await _own_line(message.bot, fed)}\n{_item('Chat', _chat_ref(message.chat.title, message.chat.username, message.chat.id))}\n{_item('Chat ID', f'<code>{message.chat.id}</code>')}\n{_item('By', user_ref(message.from_user.id, message.from_user.full_name))}")


@router.message(cmd("leavefed"))
async def cmd_leavefed(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    fed = await federations.chat_fed(message.chat.id)
    if fed is None:
        await reply(message, "This chat is not in a federation.")
        return
    await federations.leave(message.chat.id)
    await reply(message, f"This chat left the federation {_fed_name(fed)}. Nobody is unbanned.")
    await _event(message.bot, fed, f"<b>Un-FedChat</b>\n{await _own_line(message.bot, fed)}\n{_item('Chat', _chat_ref(message.chat.title, message.chat.username, message.chat.id))}\n{_item('Chat ID', f'<code>{message.chat.id}</code>')}\n{_item('By', user_ref(message.from_user.id, message.from_user.full_name))}")


@router.message(cmd("quietfed"))
async def cmd_quietfed(message: Message, command: CommandObject, role=None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    argument = (command.args or "").strip().lower()
    if argument and argument not in _ON_OFF:
        await reply(message, "Use /quietfed on or /quietfed off.")
        return
    right = RIGHT if argument else "can_manage_chat"
    if await moderation.begin_chat_command(message, role, allow_staff=False, right=right) is None:
        return
    if await federations.chat_fed(message.chat.id) is None:
        await reply(message, "This chat is not in a federation.")
        return
    if not argument:
        quiet = await federations.is_quiet(message.chat.id)
        await reply(message, f"Quiet mode is <b>{'on' if quiet else 'off'}</b>: I {'do not announce' if quiet else 'announce'} removals of federation-banned users here.")
        return
    await federations.set_quiet(message.chat.id, _ON_OFF[argument])
    await reply(message, f"Quiet mode is now <b>{'on' if _ON_OFF[argument] else 'off'}</b>.")


def _ban_line(row: dict[str, Any]) -> str:
    return f"• <b>{esc(row['name'])}</b>\n  <code>{row['fed_id']}</code>"


@router.message(cmd("fedstat", "fbanstat"))
async def cmd_fedstat(message: Message, command: CommandObject) -> None:
    words = (command.args or "").split()
    fed_id = words.pop().lower() if words and _FED_ID.match(words[-1]) else None
    me = message.from_user
    if words or (replied_message(message) is not None and fed_id is None):
        command = CommandObject(prefix=command.prefix, command=command.command, mention=command.mention, args=" ".join(words) or None)
        target = await _target(message, command)
        if target is None:
            return
        user_id, name = target.user_id, target.name
        mine = False
    else:
        if me is None:
            return
        user_id, name, mine = me.id, me.full_name, True
    who = "You are" if mine else f"{user_link(user_id, name)} is"
    if fed_id:
        fed = await federations.get(fed_id)
        if fed is None:
            await reply(message, "I can't find a federation with that ID.")
            return
        ban = await federations.get_ban(fed_id, user_id)
        if ban is None:
            await reply(message, f"{who} not banned in {_fed_name(fed)}.")
            return
        lines = [f"{who} banned in {_fed_name(fed)}.", _item("Reason", f"<code>{esc(ban['reason'] or 'none')}</code>"), _item("Banned by", esc(ban["banned_by_name"] or "unknown")), _item("Date", f"<code>{_stamp(ban)}</code>")]
        await reply(message, "\n".join(lines))
        return
    found = await federations.banned_in(user_id)
    if not found:
        await reply(message, f"{who} not banned in any federation.")
        return
    head = f"{who} banned in {len(found)} federation{'s' if len(found) != 1 else ''}:"
    tail = "\n\nFor the reason, use /fedstat &lt;user&gt; &lt;FedID&gt;." if not mine else "\n\nFor the reason, use /fedstat &lt;FedID&gt;."
    text = head + "\n" + "\n".join(_ban_line(r) for r in found) + tail
    if len(text) <= MAX_LIST_CHARS:
        await reply(message, text)
        return
    # Too long for one message: the whole list goes in a file.
    lines = [f"{row['name']}\n  {row['fed_id']}" for row in found]
    await message.bot.send_document(
        message.chat.id,
        BufferedInputFile(("\n".join(lines) + "\n").encode(), filename=f"FedBans_{user_id}.txt"),
        caption=f"{who} banned in {len(found)} federations. The list is too long for a message, so here it is as a file.",
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
    """Whether federation bans leave this person alone in this chat."""
    return (
        immunity.is_immune(user_id)
        or user_id == bot.id
        or await roles.get_role(user_id) is not None
        or await approvals.is_approved(chat_id, user_id)
        or await moderation.is_chat_admin(bot, chat_id, user_id)
    )


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


async def _enforce(bot: Bot, chat_id: int, user, found: tuple[dict[str, Any], dict[str, Any]], thread_id: int | None) -> None:
    fed, ban = found
    if not _first_recently(chat_id, user.id):
        return
    if not await _ban_in(bot, chat_id, user.id):
        _recent.pop((chat_id, user.id), None)  # try again with their next message
        await _leave_if_powerless(bot, chat_id)
        return
    if await federations.is_quiet(chat_id):
        return
    lines = [
        f"<b>Alert!</b> This user is banned in the federation <code>{esc(fed['name'])}</code>.",
        "<code>I banned them here!</code>",
        _item("User", user_ref(user.id, user.full_name)),
    ]
    if ban["reason"]:
        lines.append(_item("Reason", f"<code>{esc(ban['reason'])}</code>"))
    with suppress(TelegramAPIError):
        await bot.send_message(chat_id, "\n".join(lines), message_thread_id=thread_id, link_preview_options=LinkPreviewOptions(is_disabled=True))


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
        await bot.send_message(
            chat_id,
            f"I can't remove people from this chat any more (I need to be an admin who can ban users), "
            f"so it left the federation {_fed_name(fed)}. Nobody is unbanned.\n"
            "Give me that right again and use /joinfed to join back.",
        )
    await _event(
        bot,
        fed,
        f"<b>Un-FedChat</b>\n{await _own_line(bot, fed)}\n{_item('Chat', _chat_ref(title, username, chat_id))}\n"
        f"{_item('Chat ID', f'<code>{chat_id}</code>')}\n{_item('Reason', 'I lost the right to ban users there')}",
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

async def _info_banned(bot: Bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    if chat_id is None or await federations.chat_fed_id(chat_id) is None:
        return []
    return [("FedBanned", await federations.effective_ban(chat_id, user_id) is not None)]


async def _info_admin(bot: Bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    if chat_id is None:
        return []
    fed = await federations.chat_fed(chat_id)
    if fed is None:
        return []
    return [("FedAdmin", await federations.is_admin(fed, user_id))]


info_hooks.register("federations", _info_banned)
info_hooks.register("federations", _info_admin, always=True)
