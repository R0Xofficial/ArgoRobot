"""Core bot commands"""

import asyncio
import platform
import time

from aiogram import BaseMiddleware, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from config import settings
from core import chats, database, federations, gbans, immunity, info_hooks, logs, registry, roles, speedtest, whitelist
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.roles import Role
from core.targets import TargetError, display_name, resolve_target
from core.text import duration_text, esc, user_link, user_ref
from core import i18n
from core.i18n import tr

# Protected modules cannot be disabled with /module, so the commands in this
# file (including /module itself) are always available.
PROTECTED = True

# Commands shown in /help (see core/help_index.py).
ABOUT = "root.about"

HELP = [
    ("/ping", "root.help.check_that_i_m_alive"),
    ("/modules", "root.help.list_every_module_and_whether", Role.DEVELOPER),
    ("/module <enable|disable> <name>", "root.help.turn_a_module_on_or", Role.DEVELOPER),
    ("/cleanchats", "root.help.forget_the_chats_i_have", Role.DEVELOPER),
    ("/stats", "root.help.show_what_i_know_users", Role.DEVELOPER),
    ("/status", "root.help.show_how_i_am_doing", Role.DEVELOPER),
    ("/leave", "root.help.make_me_leave_the_chat", Role.DEVELOPER),
    ("/speedtest", "root.help.measure_the_ping_download_and", Role.DEVELOPER),
    ("/staff", "root.help.show_who_holds_each_staff", Role.SUPPORT),
    ("/adddev <user>", "root.help.grant_the_developer_role_not", Role.OWNER),
    ("/rmdev <user>", "root.help.remove_the_developer_role", Role.OWNER),
    ("/addsudo <user>", "root.help.grant_the_sudo_role", Role.DEVELOPER),
    ("/rmsudo <user>", "root.help.remove_the_sudo_role", Role.DEVELOPER),
    ("/addsupport <user>", "root.help.grant_the_support_role", Role.DEVELOPER),
    ("/rmsupport <user>", "root.help.remove_the_support_role", Role.DEVELOPER),
    ("/whitelist <user>", "root.help.protect_a_user_from_global", Role.SUDO),
    ("/unwhitelist <user>", "root.help.remove_a_user_s_protection", Role.SUDO),
]

router = Router(name="root")


class _English(BaseMiddleware):
    """The root commands (for the people who run the bot) are English only, whatever language the chat speaks."""

    async def __call__(self, handler, event, data):
        with i18n.english():
            return await handler(event, data)


router.message.middleware(_English())
router.callback_query.middleware(_English())

CB_STAFF_HOME = "staff:home"
CB_STAFF_ROLE = "staff:role:"

# <user> in the commands below: reply to the person, or give an id or @username.
GRANT_COMMANDS = {"adddev": Role.DEVELOPER, "addsudo": Role.SUDO, "addsupport": Role.SUPPORT}
REVOKE_COMMANDS = {"rmdev": Role.DEVELOPER, "rmsudo": Role.SUDO, "rmsupport": Role.SUPPORT}

# /staff buttons, in display order.
STAFF_BUTTONS = [
    (Role.OWNER, "Owner"),
    (Role.DEVELOPER, "Developers"),
    (Role.SUDO, "Sudos"),
    (Role.SUPPORT, "Supports"),
]
STAFF_TEXT_LIMIT = 3800  # Telegram allows 4096 characters per message


# --- /ping ------------------------------------------------------------------

@router.message(cmd("ping"))
async def cmd_ping(message: Message) -> None:
    # Latency is the round trip of sending a message to Telegram.
    started = time.perf_counter()
    sent = await reply(message, tr("root.pong"))
    latency_ms = (time.perf_counter() - started) * 1000
    await edit(sent, tr("root.pong_latency_ms", latency_ms=latency_ms))


# --- Bot management ---------------------------------------------------------

@router.message(cmd("modules"), HasRole(Role.DEVELOPER))
async def cmd_modules(message: Message) -> None:
    modules = registry.all_modules()
    if not modules:
        await reply(message, tr("root.no_modules_loaded"))
        return

    lines = [tr("root.modules")]
    for info in modules:
        status = tr("common.enabled") if info.enabled else tr("common.disabled")
        line = f"<code>{esc(info.name)}</code> - <b>{status}</b>"
        if info.protected:
            line += tr("root.protected")
        lines.append(line)
        if info.description:
            lines.append(f"<i>Description: {esc(info.description)}</i>")

    await reply(message, "\n".join(lines))


@router.message(cmd("module"), HasRole(Role.DEVELOPER))
async def cmd_module(message: Message, command: CommandObject) -> None:
    args = (command.args or "").split()
    action = args[0].lower() if args else ""

    if len(args) != 2 or action not in ("enable", "disable"):
        await reply(message, tr("root.usage_module_lt_enable_disable_gt"))
        return

    name = args[1]
    try:
        found = await registry.set_enabled(name, action == "enable")
    except registry.ProtectedModuleError:
        await reply(message, tr("root.module_is_protected_and_can_t", lower=esc(name.lower())))
        return

    if not found:
        await reply(message, tr("root.module_not_found", name=esc(name)))
        return

    past = tr("common.enabled") if action == "enable" else tr("common.disabled")
    ended = ""
    if action == "disable" and name.lower() == "connection":
        ended = await _end_all_connections(message)
    await reply(message, tr("root.module", lower=esc(name.lower()), past=past, ended=ended))


@router.message(cmd("cleanchats"), HasRole(Role.DEVELOPER))
async def cmd_cleanchats(message: Message) -> None:
    """Check every stored chat and forget the ones the bot is no longer in."""
    if chats.is_cleaning():
        await reply(message, tr("root.a_chat_cleanup_is_already_running"))
        return

    sent = await reply(message, tr("root.checking_chats_this_can_take_a"))
    result = await chats.cleanup(message.bot)
    if result is None:  # someone else started one in the meantime
        await edit(sent, tr("root.a_chat_cleanup_is_already_running"))
        return
    await edit(sent, chats.report(result, tr("root.chat_cleanup_finished")))


@router.message(cmd("stats"), HasRole(Role.DEVELOPER))
async def cmd_stats(message: Message) -> None:
    """What the bot has recorded, in numbers."""
    people = await database.fetch_one("SELECT count(*) AS total, count(*) FILTER (WHERE is_bot) AS bots FROM users")
    places = await database.fetch_all("SELECT type, count(*) AS total FROM chats GROUP BY type")
    by_type = {row["type"]: row["total"] for row in places}
    groups = by_type.get("group", 0) + by_type.get("supergroup", 0)
    staff = await database.fetch_one("SELECT count(*) AS total FROM roles")
    approved = await database.fetch_one("SELECT count(*) AS total FROM approvals")
    feds = await database.fetch_one("SELECT count(*) AS total FROM federations")

    lines = [
        tr("root.bot_statistics"),
        "",
        tr("root.known_users_bots", total=people['total'], bots=people['bots']),
        tr("root.chats_groups_channels", sum=sum(by_type.values()), groups=groups, get=by_type.get('channel', 0)),
        tr("root.global_bans", v=await gbans.count()),
        tr("root.federations", total=feds['total']),
        tr("root.whitelisted", v=len(await whitelist.list_all())),
        tr("root.approved_users", total=approved['total']),
        tr("root.staff_including_the_owner", v=staff['total'] + 1),
        tr("root.modules_of_enabled", count_enabled=registry.count_enabled(), count_loaded=registry.count_loaded()),
    ]
    await reply(message, "\n".join(lines))


_STARTED = time.monotonic()  # when this module was loaded: the bot's start


def _memory_mb() -> float | None:
    """The memory the bot process uses, from /proc (Linux); None elsewhere."""
    try:
        with open("/proc/self/status", encoding="utf-8") as status:
            for line in status:
                if line.startswith("VmRSS:"):
                    return int(line.split()[1]) / 1024
    except (OSError, ValueError, IndexError):
        pass
    return None


@router.message(cmd("status"), HasRole(Role.DEVELOPER))
async def cmd_status(message: Message) -> None:
    """How the bot is doing right now."""
    started = time.perf_counter()
    telegram = None
    try:
        await message.bot.get_me()
        telegram = (time.perf_counter() - started) * 1000
    except TelegramAPIError:
        pass

    started = time.perf_counter()
    try:
        await database.fetch_one("SELECT 1 AS ok")
        db_text = tr("root.ms", v=(time.perf_counter() - started) * 1000)
    except Exception:
        db_text = tr("root.not_reachable")

    try:
        import aiogram

        aiogram_version = aiogram.__version__
    except Exception:
        aiogram_version = "unknown"

    try:
        from importlib.metadata import version as package_version

        from telethon.tl.alltlobjects import LAYER

        mtproto_version = f"Telethon {package_version('telethon')} (layer {LAYER})"
    except Exception:
        mtproto_version = "unknown"

    memory = _memory_mb()
    lines = [
        tr("root.bot_status"),
        "",
        tr("root.uptime", duration_text=duration_text(int(time.monotonic() - _STARTED))),
        tr("root.telegram") + (tr("root.ms_2", telegram=telegram) if telegram is not None else tr("root.not_reachable")),
        tr("root.database", db_text=db_text),
        tr("root.memory") + (tr("root.mb", memory=memory) if memory is not None else tr("root.unknown")),
        tr("root.modules_of_enabled", count_enabled=registry.count_enabled(), count_loaded=registry.count_loaded()),
        tr("root.python", python_version=platform.python_version()),
        tr("root.aiogram", aiogram_version=esc(aiogram_version)),
        f"• <b>MTProto:</b> <code>{esc(mtproto_version)}</code>",
    ]
    await reply(message, "\n".join(lines))


@router.message(cmd("leave"), HasRole(Role.DEVELOPER))
async def cmd_leave(message: Message) -> None:
    """Leave the chat the command is used in."""
    if message.chat.type == "private":
        await reply(message, tr("root.i_can_only_leave_groups_and"))
        return

    chat_id = message.chat.id
    await reply(message, tr("root.farewell_my_duties_here_are_finished"))
    try:
        await message.bot.leave_chat(chat_id)
    except TelegramAPIError as exc:
        await reply(message, tr("root.i_couldn_t_leave_this_chat", getattr=esc(getattr(exc, 'message', exc))))
        return
    await chats.remove_chat(chat_id)


_speedtest_running = False


@router.message(cmd("speedtest"), HasRole(Role.DEVELOPER))
async def cmd_speedtest(message: Message) -> None:
    global _speedtest_running
    if _speedtest_running:
        await reply(message, tr("root.a_speedtest_is_already_running"))
        return

    _speedtest_running = True
    sent = await reply(message, tr("root.running_a_speedtest_this_takes_a"))
    try:
        ping, download, upload = await speedtest.run()
        text = (
            tr("root.speedtest_ping_ms_download_mbps_upload", ping=ping, download=download, upload=upload)
        )
    except Exception as exc:  # network trouble of any kind: report it, don't crash
        text = tr("root.speedtest_failed", v=esc(str(exc) or type(exc).__name__))
    finally:
        _speedtest_running = False
    await edit(sent, text)


# --- Staff roles ------------------------------------------------------------

# Shared with the other modules that write to the user log (see core/logs.py).
_timestamp = logs.timestamp
_role_log_text = logs.audit_text


@router.message(cmd(*GRANT_COMMANDS, *REVOKE_COMMANDS), HasRole(Role.SUPPORT))
async def cmd_staff_role(message: Message, command: CommandObject, role: Role | None = None) -> None:
    granting = command.command in GRANT_COMMANDS
    target_role = (GRANT_COMMANDS if granting else REVOKE_COMMANDS)[command.command]
    actor = message.from_user

    if not roles.can_manage(role, target_role):
        await reply(message, tr("root.you_don_t_have_permission_to", label=roles.label(target_role, plural=True)))
        return

    try:
        target = await resolve_target(message, command)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    if target.user_id == actor.id:
        await reply(message, tr("root.you_can_t_change_your_own"))
        return
    if target.user_id == settings.OWNER_ID:
        await reply(message, tr("common.the_owner_s_role_can_t"))
        return

    who = user_link(target.user_id, target.name)
    current = await roles.get_role(target.user_id)
    label = roles.label(target_role)

    if granting:
        if current is target_role:
            await reply(message, tr("root.is_already", who=who, label=label))
            return
        if current is not None:
            await reply(
                message,
                tr("root.is_already_remove_that_role_first", who=who, label=roles.label(current)),
            )
            return
        if await gbans.is_gbanned(target.user_id):
            await reply(message, tr("root.is_globally_banned_and_can_t", who=who))
            return
        fed_bans = await federations.banned_in(target.user_id)
        if fed_bans:
            names = ", ".join(esc(row["name"]) for row in fed_bans[:5])
            more = tr("root.and_more", v=len(fed_bans) - 5) if len(fed_bans) > 5 else ""
            await reply(
                message,
                tr("root.is_banned_in_a_federation_and", who=who, names=names, more=more),
            )
            return
        await roles.set_role(target.user_id, target_role, actor.id, target.name, actor.full_name)
        await reply(message, tr("root.has_been_promoted_to", who=who, label=label))
    else:
        if current is not target_role:
            await reply(message, tr("root.is_not", who=who, label=label))
            return
        await roles.remove_role(target.user_id)
        await reply(message, tr("root.has_been_removed_from", who=who, label=label))

    granted_tag, revoked_tag = roles.LOG_TAGS[target_role]
    await logs.role_log(
        message.bot,
        _role_log_text(granted_tag if granting else revoked_tag, target.user_id, target.name, actor),
    )


def _staff_home_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=title, callback_data=f"{CB_STAFF_ROLE}{int(role)}")
        for role, title in STAFF_BUTTONS
    ]
    return InlineKeyboardMarkup(inline_keyboard=[buttons[:2], buttons[2:]])


def _staff_back_keyboard() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text=tr("common.back"), callback_data=CB_STAFF_HOME)]]
    )


def _staff_home_text() -> str:
    return tr("root.staff_choose_a_role_to_see")


async def _staff_list_text(bot, role: Role) -> str:
    title = dict(STAFF_BUTTONS)[role]

    if role is Role.OWNER:
        rows = [{"user_id": settings.OWNER_ID, "name": None, "added_at": None, "added_by": None, "added_by_name": None}]
    else:
        rows = await roles.list_role(role)

    if not rows:
        return tr("root.nobody_holds_this_role_yet", title=title)

    # Look every person up once, in parallel; fall back to the stored name.
    stored: dict[int, str | None] = {}
    for row in rows:
        stored.setdefault(row["user_id"], row["name"])
        if row["added_by"] is not None:
            stored.setdefault(row["added_by"], row["added_by_name"])
    ids = list(stored)
    found = await asyncio.gather(*(display_name(bot, uid, stored[uid]) for uid in ids))
    names = dict(zip(ids, found))

    lines = [f"<b>{title}</b> ({len(rows)})"]
    shown = 0
    for number, row in enumerate(rows, start=1):
        entry = [f"{number}. {user_ref(row['user_id'], names[row['user_id']])}"]
        if row["added_at"] is not None:
            entry.append(f"   Added: {_timestamp(row['added_at'])}")
        if row["added_by"] is not None:
            entry.append(f"   By: {user_ref(row['added_by'], names.get(row['added_by']))}")
        block = "\n".join(entry)

        if sum(len(line) + 1 for line in lines) + len(block) > STAFF_TEXT_LIMIT:
            lines.append(f"\n...and {len(rows) - shown} more.")
            break
        lines.append("\n" + block)
        shown += 1

    return "\n".join(lines)


@router.message(cmd("staff"), HasRole(Role.SUPPORT))
async def cmd_staff(message: Message) -> None:
    await reply(message, _staff_home_text(), reply_markup=_staff_home_keyboard())


@router.callback_query(F.data == CB_STAFF_HOME, HasRole(Role.SUPPORT))
async def cb_staff_home(callback: CallbackQuery) -> None:
    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(callback.message, _staff_home_text(), reply_markup=_staff_home_keyboard())


@router.callback_query(F.data.startswith(CB_STAFF_ROLE), HasRole(Role.SUPPORT))
async def cb_staff_role(callback: CallbackQuery) -> None:
    try:
        role = Role(int((callback.data or "")[len(CB_STAFF_ROLE):]))
    except ValueError:
        await callback.answer(tr("root.unknown_role"), show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        text = await _staff_list_text(callback.bot, role)
        await edit(callback.message, text, reply_markup=_staff_back_keyboard())


# --- Whitelist ------------------------------------------------------------------

# Sudo users and up (Developers, the Owner) manage the list.
WHITELIST_ROLE = Role.SUDO


@router.message(cmd("whitelist", "unwhitelist"), HasRole(WHITELIST_ROLE))
async def cmd_whitelist(message: Message, command: CommandObject) -> None:
    adding = command.command == "whitelist"
    actor = message.from_user

    try:
        target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    if target.user_id == actor.id:
        await reply(message, tr("root.you_can_t_change_your_own_2"))
        return

    who = user_link(target.user_id, target.name)
    if immunity.is_immune(target.user_id):
        await reply(message, tr("root.is_immune_to_all_bot_actions", who=who))
        return

    if adding:
        if await gbans.is_gbanned(target.user_id):
            await reply(message, tr("root.is_globally_banned_and_can_t_2", who=who))
            return
        if not await whitelist.add(target.user_id, actor.id, target.name, actor.full_name):
            await reply(message, tr("root.is_already_whitelisted", who=who))
            return
        tag = "#WHITELIST"
        await reply(message, tr("root.has_been_whitelisted", who=who))
    else:
        if not await whitelist.remove(target.user_id):
            await reply(message, tr("root.is_not_whitelisted", who=who))
            return
        tag = "#UNWHITELIST"
        await reply(message, tr("root.has_been_removed_from_the_whitelist", who=who))

    await logs.role_log(message.bot, logs.audit_text(tag, target.user_id, target.name, actor))


async def _info_flags(bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    return [(tr("root.immune"), immunity.is_immune(user_id)), (tr("root.whitelisted_2"), await whitelist.is_whitelisted(user_id))]


info_hooks.register("root", _info_flags)


async def _end_all_connections(message: Message) -> str:
    """Connections can't outlive the module: end them all and tell the people on them."""
    from contextlib import suppress

    from aiogram.exceptions import TelegramAPIError

    from core import connection

    try:
        open_ones = await connection.drop_all()
    except Exception:
        return tr("root.ending_the_open_connections_failed_they")
    for item in open_ones:
        with suppress(TelegramAPIError):
            await message.bot.send_message(
                chat_id=item["user_id"],
                text=tr("root.your_connection_to_has_ended_connections", v=esc(item['title'] or str(item['chat_id']))),
            )
    n = len(open_ones)
    return tr("root.connection_ended", n=n, opt='s' if n != 1 else '') if n else ""

