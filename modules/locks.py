"""Lock kinds of messages"""

import time
from contextlib import suppress
from types import SimpleNamespace
from typing import Any

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import approvals, immunity, locks, moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, ModerationError, perform
from core.roles import Role
from core.text import esc, user_link, SWITCH as _ON_OFF

ABOUT = """
Lock a kind of message and I delete every one of them: stickers, links, forwards, voice messages, a whole alphabet... Locks do not affect chat admins or approved users, and edited messages are checked too. /locktypes explains every kind.

Locks that catch links, forwards, commands, stickers and the like can be eased with the allowlist: whatever is on it stays allowed, so you can lock all links but let one site through.

By default I just delete. With /lockwarns on, whoever breaks a lock also gets a warn, and is banned at the warn limit.

Everything here is for chat admins with the right to change chat info.
"""

HELP = [
    ("/lock <item(s)>", "Lock one or more kinds of message. Now only admins can send them.", "admins"),
    ("/unlock <item(s)>", "Unlock kinds of message. Everyone can send them again.", "admins"),
    ("/locks", "Show what is locked in this chat.", "admins"),
    ("/locktypes", "Show every kind of message that can be locked, and what each one means.", "admins"),
    ("/lockwarns <on|off>", "Warn people who send a locked item, not only delete it.", "admins"),
    ("/allowlist <items>", "Allow a link, site, @name, id, command, $cashtag or sticker pack link even when its lock is on. Separate several with spaces. Without items, shows the allowlist.", "admins"),
    ("/rmallowlist <items>", "Remove items from the allowlist.", "admins"),
    ("/rmallowlistall", "Empty the allowlist. Asks for confirmation.", "chat owner"),
]

HELP_NOTES = [
    (
        "Good to start with",
        """
        /lock invitelink botlink - stops advertising of other groups and bots.
        /lock forwardchannel - stops forwarded channel posts, a favourite of spammers.
        /lock cyrillic cjk rtl - for chats that stay in one alphabet.
        /lock bot - only admins can add bots.
        """,
    ),
    (
        "The allowlist",
        """
        /allowlist example.com - links to that site and its subpages.
        /allowlist @somechannel - its links and forwards, and writing as that channel.
        /allowlist -1001234567890 - a chat by id.
        /allowlist /start - the command.
        /allowlist $BTC - the cashtag.
        /allowlist t.me/addstickers/Pack - that sticker pack.
        """,
    ),
]

router = Router(name="locks")

# After antiflood (-60), before blacklists (-55): a locked message should be gone first.
PRIORITY = -58

RIGHT = "can_change_info"
LOOK_RIGHT = "can_manage_chat"
CB_CLEAR = "locks:clear:"  # locks:clear:yes / locks:clear:no
FAILURE_COOLDOWN = 300.0
WARN_COOLDOWN = 5.0  # an album or a burst of messages earns one warn
MAX_TEXT = 3500

_complained: dict[int, float] = {}
_warned_at: dict[tuple[int, int], float] = {}
_SERVICE = (
    "new_chat_members", "left_chat_member", "new_chat_title", "new_chat_photo", "delete_chat_photo",
    "group_chat_created", "supergroup_chat_created", "channel_chat_created", "migrate_to_chat_id",
    "migrate_from_chat_id", "pinned_message", "forum_topic_created", "forum_topic_edited",
    "forum_topic_closed", "forum_topic_reopened", "general_forum_topic_hidden", "general_forum_topic_unhidden",
    "video_chat_started", "video_chat_ended", "video_chat_scheduled", "video_chat_participants_invited",
    "message_auto_delete_timer_changed", "boost_added",
)


# --- Helpers ----------------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None, *, change: bool) -> bool:
    right = RIGHT if change else LOOK_RIGHT
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=right) is not None


def _words(text: str | None) -> list[str]:
    seen: list[str] = []
    for word in (text or "").replace(",", " ").split():
        if word not in seen:
            seen.append(word)
    return seen


def _chunks(lines: list[str]) -> list[str]:
    """Join lines into messages that fit in one Telegram message."""
    out, current = [], ""
    for line in lines:
        if current and len(current) + len(line) + 1 > MAX_TEXT:
            out.append(current)
            current = ""
        current += ("\n" if current else "") + line
    if current:
        out.append(current)
    return out


def _yes_no(value: bool) -> str:
    return "Yes" if value else "No"


# --- Commands ---------------------------------------------------------------------------------

@router.message(cmd("lock"))
async def cmd_lock(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _change(message, command, role, lock=True)


@router.message(cmd("unlock"))
async def cmd_unlock(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _change(message, command, role, lock=False)


async def _change(message: Message, command: CommandObject, role: Role | None, *, lock: bool) -> None:
    verb = "lock" if lock else "unlock"
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role, change=True):
        return
    items = [word.lower() for word in _words(command.args)]
    if not items:
        await reply(message, f"Use /{verb} &lt;item&gt;. /locktypes lists them.")
        return
    unknown = [item for item in items if item not in locks.LOCKS]
    if unknown:
        shown = ", ".join(f"<code>{esc(item[:30])}</code>" for item in unknown[:10])
        await reply(message, f"I don't know {shown}. /locktypes lists everything that can be locked.")
        return
    if lock:
        await locks.lock(message.chat.id, items)
    else:
        await locks.unlock(message.chat.id, items)
    names = ", ".join(f"<code>{item}</code>" for item in items)
    await moderation.log_action(message, "LOCKED" if lock else "UNLOCKED", moderation.actor_of(message), None, "", ("Items", names))
    await reply(message, f"{'Locked' if lock else 'Unlocked'} {names}.")


@router.message(cmd("locks"))
async def cmd_locks(message: Message, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role, change=False):
        return
    cfg = await locks.config(message.chat.id)
    lines = [
        f"<b>Locks in {esc(message.chat.title or 'this chat')}</b>",
        f"• <b>Warn on lock:</b> <code>{_yes_no(cfg.warn)}</code>",
        f"• <b>Allowlisted:</b> <code>{len(cfg.allow)}</code>",
        "",
    ]
    ordered = [name for name in locks.LOCKS if name in cfg.locked]
    if ordered:
        lines.append("Locked:")
        lines += [f"• <code>{name}</code>" for name in ordered]
    else:
        lines.append("Nothing is locked. /locktypes lists what can be.")
    await reply(message, "\n".join(lines))


def _lock_types_pages() -> list[str]:
    lines = ["<b>Lockable items</b>", "Items marked * can be eased with /allowlist."]
    for group, items in locks.GROUPS.items():
        lines += ["", f"<b>{group}</b>"]
        for name, (text, allowable) in items.items():
            lines.append(f"• <code>{name}</code>{'*' if allowable else ''} - {esc(text)}")
    return _chunks(lines)


HELP_PAGES = [("Lock types", _lock_types_pages)]


@router.message(cmd("locktypes"))
async def cmd_locktypes(message: Message, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role, change=False):
        return
    for part in _lock_types_pages():
        await reply(message, part)


@router.message(cmd("lockwarns"))
async def cmd_lockwarns(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    argument = (command.args or "").strip().lower()
    if not argument:
        if not await _admin_only(message, role, change=False):
            return
        cfg = await locks.config(message.chat.id)
        await reply(message, f"Warning on locks is <b>{'on' if cfg.warn else 'off'}</b> in this chat.")
        return
    if argument not in _ON_OFF:
        await reply(message, "Use /lockwarns on or /lockwarns off.")
        return
    if not await _admin_only(message, role, change=True):
        return
    await locks.set_warn(message.chat.id, _ON_OFF[argument])
    await reply(message, f"People who send a locked item will {'now be warned' if _ON_OFF[argument] else 'only have it deleted'}.")


# --- The allowlist ----------------------------------------------------------------------------

def _entries(text: str | None) -> tuple[list[str], str | None]:
    """The normalized entries typed, and a sentence about the first bad one."""
    out: list[str] = []
    for word in _words(text):
        try:
            entry = locks.normalize_entry(word)
        except ValueError as exc:
            return out, str(exc)
        if entry not in out:
            out.append(entry)
    return out, None


@router.message(cmd("allowlist"))
async def cmd_allowlist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not (command.args or "").strip():
        if not await _admin_only(message, role, change=False):
            return
        cfg = await locks.config(message.chat.id)
        if not cfg.allow:
            await reply(message, "The allowlist is empty. Add to it with /allowlist &lt;items&gt;.")
            return
        lines = ["<b>Allowlist</b>"] + [f"• <code>{esc(entry)}</code>" for entry in sorted(cfg.allow)]
        for part in _chunks(lines):
            await reply(message, part)
        return
    if not await _admin_only(message, role, change=True):
        return
    entries, problem = _entries(command.args)
    if problem:
        await reply(message, esc(problem))
        return
    cfg = await locks.config(message.chat.id)
    if len(cfg.allow | set(entries)) > locks.MAX_ALLOWED:
        await reply(message, f"The allowlist is full ({locks.MAX_ALLOWED} items). Remove some with /rmallowlist.")
        return
    added = await locks.allow_add(message.chat.id, entries)
    await reply(message, f"Allowlisted <b>{added}</b> new item{'s' if added != 1 else ''}." if added else "Those were already allowlisted.")


@router.message(cmd("rmallowlist"))
async def cmd_rmallowlist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role, change=True):
        return
    if not (command.args or "").strip():
        await reply(message, "Use /rmallowlist &lt;items&gt;. /allowlist shows what is on it.")
        return
    entries, problem = _entries(command.args)
    if problem:
        await reply(message, esc(problem))
        return
    removed = await locks.allow_remove(message.chat.id, entries)
    await reply(message, f"Removed <b>{removed}</b> item{'s' if removed != 1 else ''} from the allowlist." if removed else "None of those were on the allowlist.")


@router.message(cmd("rmallowlistall"))
async def cmd_rmallowlistall(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len((await locks.config(message.chat.id)).allow)
    if not count:
        await reply(message, "The allowlist of this chat is empty.")
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Remove all", callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text="Cancel", callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, f"Remove all <b>{count}</b> items from the allowlist? This cannot be undone.", reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_rmallowlistall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer("Only the owner of this chat can do this.", show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await locks.allow_clear(message.chat.id)
        await callback.answer()
        await edit(message, f"Done. <b>{removed}</b> items removed.", reply_markup=None)
    else:
        await callback.answer()
        await edit(message, "Cancelled. The allowlist is unchanged.", reply_markup=None)


# --- Enforcement ------------------------------------------------------------------------------

async def _exempt(bot: Bot, chat_id: int, user_id: int) -> bool:
    return (
        immunity.is_immune(user_id)
        or await approvals.is_approved(chat_id, user_id)
        or await moderation.is_chat_admin(bot, chat_id, user_id)
    )


async def _locked(message: Message, bot: Bot) -> dict[str, Any] | bool:
    """A message of a locked kind whose sender is not exempt."""
    user = message.from_user
    if (
        user is None
        or message.chat.type not in GROUP_TYPES
        or getattr(message, "is_automatic_forward", None)
        or any(getattr(message, field, None) for field in _SERVICE)
    ):
        return False
    channel = message.sender_chat is not None and message.sender_chat.id != message.chat.id
    if message.sender_chat is not None and not channel:
        return False  # an anonymous admin
    if user.is_bot and not channel:
        return False
    cfg = await locks.config(message.chat.id)
    if not cfg.locked:
        return False
    kind = await locks.find(message, bot, cfg)
    if kind is None:
        return False
    if not channel and await _exempt(bot, message.chat.id, user.id):
        return False
    return {"lock_hit": SimpleNamespace(kind=kind, warn=cfg.warn and not channel)}


async def _warn(bot: Bot, message: Message, reason: str) -> str:
    """Warn the sender; at the chat's warn limit, ban them and start the count over."""
    from modules import warns

    user = message.from_user
    chat_id = message.chat.id
    target = SimpleNamespace(user_id=user.id, name=user.full_name)
    await warns.add_warn(chat_id, target, reason, moderation.Actor(bot.id, "Locks"))
    count = len(await warns.list_warns(chat_id, user.id))
    limit = await warns.get_limit(chat_id)
    if count < limit:
        return f"warned ({count}/{limit})"
    await perform(bot.ban_chat_member(chat_id, user.id))
    await warns.clear_warns(chat_id, user.id)
    return f"banned after reaching the warn limit ({count}/{limit})"


@router.message(_locked)
@router.edited_message(_locked)
async def on_locked(message: Message, bot: Bot, lock_hit: SimpleNamespace) -> None:
    with suppress(TelegramAPIError):
        await message.delete()
    if not lock_hit.warn:
        return
    user = message.from_user
    chat_id = message.chat.id
    now = time.monotonic()
    key = (chat_id, user.id)
    if now - _warned_at.get(key, -WARN_COOLDOWN) < WARN_COOLDOWN:
        return
    if len(_warned_at) > 5000:
        _warned_at.clear()
    _warned_at[key] = now
    try:
        done = await _warn(bot, message, f"Sent a locked item: {lock_hit.kind}")
    except ModerationError as exc:
        if now - _complained.get(chat_id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat_id] = now
            await reply(message, f"{user_link(user.id, user.full_name)} sent a locked item, but I can't act: {esc(exc)}", quote=False)
        return
    await reply(
        message,
        f"{user_link(user.id, user.full_name)} has been {done}.\nReason: sent a locked item (<code>{lock_hit.kind}</code>).",
        quote=False,
    )


async def _bots_added(message: Message, bot: Bot) -> dict[str, Any] | bool:
    """Someone who is not exempt added bots to a chat that locks them."""
    members = message.new_chat_members
    adder = message.from_user
    if not members or adder is None or message.chat.type not in GROUP_TYPES:
        return False
    bots = [m for m in members if m.is_bot and m.id != bot.id and m.id != adder.id]
    if not bots:
        return False
    if "bot" not in (await locks.config(message.chat.id)).locked:
        return False
    if await _exempt(bot, message.chat.id, adder.id):
        return False
    return {"added_bots": bots}


@router.message(_bots_added)
async def on_bots_added(message: Message, bot: Bot, added_bots: list) -> None:
    chat_id = message.chat.id
    try:
        for member in added_bots:
            await perform(bot.ban_chat_member(chat_id, member.id))
            with suppress(TelegramAPIError):
                await bot.unban_chat_member(chat_id, member.id, only_if_banned=True)
    except ModerationError as exc:
        now = time.monotonic()
        if now - _complained.get(chat_id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat_id] = now
            await reply(message, f"Bots are locked here, but I can't remove them: {esc(exc)}", quote=False)
        return
    names = ", ".join(esc(m.full_name) for m in added_bots)
    await reply(message, f"Bots are locked in this chat, so {names} was removed. Only admins can add bots.", quote=False)
