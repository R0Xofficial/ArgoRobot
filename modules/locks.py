"""Lock kinds of messages"""

import time
from contextlib import suppress
from types import SimpleNamespace
from typing import Any

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import approvals, blacklist, enforce, greetings, immunity, locks, moderation, roles
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, ModerationError, perform
from core.roles import Role
from core.text import duration_text, esc, user_link, SWITCH as _ON_OFF
from core.i18n import tr, word

ABOUT = "locks.about"

HELP = [
    ("/lock <item(s)>", "locks.help.lock_one_or_more_kinds", "common.admins"),
    ("/unlock <item(s)>", "locks.help.unlock_kinds_of_message_everyone", "common.admins"),
    ("/locks", "locks.help.show_what_is_locked_in", "common.admins"),
    ("/locktypes", "locks.help.show_every_kind_of_message", "common.admins"),
    ("/lockwarns <on|off>", "locks.help.warn_people_who_send_a", "common.admins"),
    ("/lockmode <del|warn|ban|kick|mute|tban|tmute> [time]", "locks.help.what_happens_to_someone_who", "common.admins"),
    ("/allowlist <items>", "locks.help.allow_a_link_site_name", "common.admins"),
    ("/rmallowlist <items>", "locks.help.remove_items_from_the_allowlist", "common.admins"),
    ("/rmallowlistall", "locks.help.empty_the_allowlist_asks_for", "common.chat_owner"),
]

HELP_NOTES = [
    (
        "locks.note.good_to_start_with",
        "locks.note.lock_invitelink_botlink_stops_advertising",
    ),
    (
        "locks.note.the_allowlist",
        "locks.note.allowlist_example_com_links_to",
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
    return await moderation.begin_chat_command(message, role, right=right) is not None


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
    return tr("common.yes") if value else tr("common.no")


# --- Commands ---------------------------------------------------------------------------------

@router.message(cmd("lock"))
async def cmd_lock(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _change(message, command, role, lock=True)


@router.message(cmd("unlock"))
async def cmd_unlock(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _change(message, command, role, lock=False)


async def _change(message: Message, command: CommandObject, role: Role | None, *, lock: bool) -> None:
    verb = tr("locks.lock") if lock else tr("locks.unlock")
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role, change=True):
        return
    items = [word.lower() for word in _words(command.args)]
    if not items:
        await reply(message, tr("locks.use_lt_item_gt_locktypes_lists", verb=verb))
        return
    unknown = [item for item in items if item not in locks.LOCKS]
    if unknown:
        shown = ", ".join(f"<code>{esc(item[:30])}</code>" for item in unknown[:10])
        await reply(message, tr("locks.i_don_t_know_locktypes_lists", shown=shown))
        return
    if lock:
        await locks.lock(message.chat.id, items)
    else:
        await locks.unlock(message.chat.id, items)
    names = ", ".join(f"<code>{item}</code>" for item in items)
    await moderation.log_action(message, "LOCKED" if lock else "UNLOCKED", moderation.actor_of(message), None, "", (tr("common.items"), names))
    await reply(message, f"{tr("locks.locked") if lock else tr("locks.unlocked")} {names}.")


@router.message(cmd("locks"))
async def cmd_locks(message: Message, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role, change=False):
        return
    cfg = await locks.config(message.chat.id)
    lines = [
        tr("locks.locks_in", v=esc(message.chat.title or tr("common.this_chat"))),
        tr("locks.warn_on_lock", yes_no=_yes_no(cfg.warn)),
        tr("locks.allowlisted", allow=len(cfg.allow)),
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
    lines = [tr("locks.lockable_items"), tr("locks.items_marked_can_be_eased_with")]
    for group, items in locks.GROUPS.items():
        lines += ["", f"<b>{group}</b>"]
        for name, (text, allowable) in items.items():
            lines.append(f"• <code>{name}</code>{'*' if allowable else ''} - {esc(text)}")
    return _chunks(lines)


HELP_PAGES = [("locks.page.lock_types", _lock_types_pages)]


@router.message(cmd("locktypes"))
async def cmd_locktypes(message: Message, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role, change=False):
        return
    for part in _lock_types_pages():
        await reply(message, part)


@router.message(cmd("lockwarns"))
async def cmd_lockwarns(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    argument = (command.args or "").strip().lower()
    if not argument:
        if not await _admin_only(message, role, change=False):
            return
        cfg = await locks.config(message.chat.id)
        await reply(message, tr("locks.warning_on_locks_is_in_this", opt=tr("common.on") if cfg.warn else tr("common.off")))
        return
    if argument not in _ON_OFF:
        await reply(message, tr("locks.use_lockwarns_on_or_lockwarns_off"))
        return
    if not await _admin_only(message, role, change=True):
        return
    await locks.set_warn(message.chat.id, _ON_OFF[argument])
    await reply(message, tr("locks.people_who_send_a_locked_item", opt=tr("locks.now_be_warned") if _ON_OFF[argument] else tr("locks.only_have_it_deleted")))


@router.message(cmd("lockmode"))
async def cmd_lockmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    spec = (command.args or "").strip()
    usage = tr("locks.use_lockmode_lt_del_warn_ban")
    if not spec:
        if not await _admin_only(message, role, change=False):
            return
        cfg = await locks.config(message.chat.id)
        await reply(message, tr("locks.someone_who_sends_a_locked_item", mode_text=esc(_mode_text(cfg.action, cfg.seconds)), usage=usage))
        return
    if not await _admin_only(message, role, change=True):
        return
    try:
        mode, seconds = blacklist.parse_action(spec)
    except blacklist.TriggerError as exc:
        await reply(message, f"{esc(exc)}\n{usage}")
        return
    await locks.set_mode(message.chat.id, mode, seconds)
    await reply(message, tr("locks.someone_who_sends_a_locked_item_2", mode_text=esc(_mode_text(mode, seconds))))


def _mode_text(mode: str, seconds: int) -> str:
    if mode == "del":
        return tr("locks.the_message_deleted")
    return tr("common.for", mode=word(mode), duration_text=duration_text(seconds)) if mode in enforce.TIMED_MODES else word(mode)


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
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not (command.args or "").strip():
        if not await _admin_only(message, role, change=False):
            return
        cfg = await locks.config(message.chat.id)
        if not cfg.allow:
            await reply(message, tr("locks.the_allowlist_is_empty_add_to"))
            return
        lines = [tr("locks.allowlist")] + [f"• <code>{esc(entry)}</code>" for entry in sorted(cfg.allow)]
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
        await reply(message, tr("locks.the_allowlist_is_full_items_remove", MAX_ALLOWED=locks.MAX_ALLOWED))
        return
    added = await locks.allow_add(message.chat.id, entries)
    await reply(message, tr("locks.allowlisted_new_item", added=added, opt='s' if added != 1 else '') if added else tr("locks.those_were_already_allowlisted"))


@router.message(cmd("rmallowlist"))
async def cmd_rmallowlist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role, change=True):
        return
    if not (command.args or "").strip():
        await reply(message, tr("locks.use_rmallowlist_lt_items_gt_allowlist"))
        return
    entries, problem = _entries(command.args)
    if problem:
        await reply(message, esc(problem))
        return
    removed = await locks.allow_remove(message.chat.id, entries)
    await reply(message, tr("locks.removed_item_from_the_allowlist", removed=removed, opt='s' if removed != 1 else '') if removed else tr("locks.none_of_those_were_on_the"))


@router.message(cmd("rmallowlistall"))
async def cmd_rmallowlistall(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len((await locks.config(message.chat.id)).allow)
    if not count:
        await reply(message, tr("locks.the_allowlist_of_this_chat_is"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text=tr("common.remove_all"), callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, tr("locks.remove_all_items_from_the_allowlist", count=count), reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_rmallowlistall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer(tr("common.only_the_owner_of_this_chat"), show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await locks.allow_clear(message.chat.id)
        await callback.answer()
        await edit(message, tr("locks.done_items_removed", removed=removed), reply_markup=None)
    else:
        await callback.answer()
        await edit(message, tr("locks.cancelled_the_allowlist_is_unchanged"), reply_markup=None)


# --- Enforcement ------------------------------------------------------------------------------

async def _exempt(bot: Bot, chat_id: int, user_id: int) -> bool:
    return (
        immunity.is_immune(user_id)
        or await roles.get_role(user_id) is not None  # anybody with a role in the bot
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
    action = "del" if channel else cfg.action  # a channel can't be warned or muted as a user
    return {"lock_hit": SimpleNamespace(kind=kind, action=action, seconds=cfg.seconds, warn=action != "del")}


async def _warn(bot: Bot, message: Message, reason: str) -> str:
    """Warn the sender; at the chat's warn limit, ban them and start the count over."""
    from modules import warns

    user = message.from_user
    chat_id = message.chat.id
    target = SimpleNamespace(user_id=user.id, name=user.full_name)
    await warns.add_warn(chat_id, target, reason, moderation.Actor(bot.id, tr("locks.locks")))
    count = len(await warns.list_warns(chat_id, user.id))
    limit = await warns.get_limit(chat_id)
    if count < limit:
        return tr("common.warned", count=count, limit=limit)
    done = await warns.limit_action(bot, chat_id, user.id)
    await warns.clear_warns(chat_id, user.id)
    return tr("common.after_reaching_the_warn_limit", done=done, count=count, limit=limit)


@router.message(_locked)
@router.edited_message(_locked)
async def on_locked(message: Message, bot: Bot, lock_hit: SimpleNamespace) -> None:
    with suppress(TelegramAPIError):
        await message.delete()
    if lock_hit.action == "del":
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
        if lock_hit.action == "warn":
            done = await _warn(bot, message, tr("locks.sent_a_locked_item", kind=lock_hit.kind))
        else:
            done = await enforce.punish(bot, chat_id, user.id, lock_hit.action, lock_hit.seconds)
    except ModerationError as exc:
        if now - _complained.get(chat_id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat_id] = now
            await reply(message, tr("locks.sent_a_locked_item_but_i", id=user_link(user.id, user.full_name), exc=esc(exc)), quote=False)
        return
    await reply(
        message,
        tr("locks.has_been_reason_sent_a_locked", id=user_link(user.id, user.full_name), done=done, kind=lock_hit.kind),
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
            await reply(message, tr("locks.bots_are_locked_here_but_i", exc=esc(exc)), quote=False)
        return
    names = ", ".join(esc(m.full_name) for m in added_bots)
    await reply(message, tr("locks.bots_are_locked_in_this_chat", names=names), quote=False)
    if (await greetings.get(chat_id))["clean_service"]:  # this handler takes the message away from the greetings one
        with suppress(TelegramAPIError):
            await message.delete()
