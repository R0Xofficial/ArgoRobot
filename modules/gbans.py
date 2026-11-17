"""Global bans"""

import asyncio
import io
import json
import logging
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
    ReplyParameters,
)

from config import settings
from core import approvals, banfiles, banguard, chats, chatsettings, gbans, immunity, info_hooks, logs, moderation, roles, users, whitelist
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.targets import Target, TargetError, display_name, replied_message, resolve_target, sender_as_target
from core.text import at_name, esc, user_ref, SWITCH as _ANSWERS
from core import i18n
from core.i18n import tr

log = logging.getLogger(__name__)

# Looked at before every other module: a globally banned user is removed before
# any command of theirs could run (see main.discover_modules).
PRIORITY = -100

ABOUT = "gbans.about"

HELP = [
    ("/gban <user> <reason>", "gbans.help.ban_a_user_in_every", Role.SUPPORT),
    ("/dgban <reason>", "gbans.help.reply_to_a_message_delete", Role.SUPPORT),
    ("/ungban <user> <reason>", "gbans.help.lift_a_global_ban_support", Role.SUPPORT),
    ("/gstat [user]", "gbans.help.show_whether_you_are_globally"),
    ("/gbans [on|off]", "gbans.help.choose_whether_this_chat_removes", "common.chat_owner"),
    ("/gimport [csv|minicsv|json|human]", "gbans.help.reply_to_a_file_to", Role.OWNER),
    ("/gexport [csv|minicsv|json|human]", "gbans.help.get_the_list_of_global", Role.OWNER),
]

router = Router(name="gbans")

MAX_REASON_LENGTH = 500
CB_PREFIX = "gban:"  # gban:apr:<request id> / gban:dec:<request id>
IMPORT_CHUNK = 5000

SUDO = Role.SUDO


NO_REASON = "gbans.no_reason"
IMMUNE_MESSAGE = "common.i_can_t_take_action_against"
PRIVILEGED_MESSAGE = "gbans.privileged_message"


# --- Wording -------------------------------------------------------------------

def _thread_of(message: Message) -> int | None:
    return message.message_thread_id if message.is_topic_message else None


def _reply_place(message: Message) -> tuple[int, int | None, int | None]:
    """Where to report back to: (chat, message to answer, topic). Private chats need no quote."""
    if message.chat.type == "private":
        return message.chat.id, None, None
    return message.chat.id, message.message_id, _thread_of(message)


def _origin_html(message: Message) -> str:
    """The "Initiated From" line: where the command was given, with a link only for public chats."""
    chat = message.chat
    if chat.type == "private":
        place = tr("gbans.pm_with", first_name=esc(message.from_user.first_name))
    elif chat.username:
        place = f'<a href="https://t.me/{esc(chat.username)}/{message.message_id}">{esc(chat.title)}</a>'
    else:
        place = esc(chat.title)
    return f"{place} [<code>{chat.id}</code>]"


def _log_text(
    tag: str,
    origin: str,
    target: str,
    reason: str,
    admin: str,
    *,
    old_reason: str | None = None,
    approved_by: str | None = None,
    timestamp: bool = True,
) -> str:
    lines = [
        f"<b>{tag}</b>",
        tr("gbans.initiated_from", origin=origin),
        "",
        tr("gbans.user", target=target),
        tr("gbans.reason", reason=esc(reason)),
    ]
    if old_reason:
        lines.append(f"<b>Old Reason:</b> <code>{esc(old_reason)}</code>")
    if timestamp:
        lines.append(f"<b>Timestamp:</b> {logs.timestamp()}")
    lines.append(f"<b>Admin:</b> {admin}")
    if approved_by:
        lines.append(f"<b>Approved by:</b> {approved_by}")
    return "\n".join(lines)


def _tag_for(old: dict | None) -> str:
    return "#GBANUPDATE" if old else "#GBANNED"


def _done_text(old: dict | None) -> str:
    if old:
        return tr("gbans.done_gban_reason_updated_old_reason", reason=esc(old['reason']))
    return tr("gbans.done_gbanned")


async def _notify(bot: Bot, chat_id: int, text, msg_id: int | None = None, thread_id: int | None = None) -> None:
    """Say something in the chat a command came from, in its topic, never failing."""
    if callable(text):  # written in the language of the chat it goes to
        async with i18n.speaking(chat_id):
            text = text()
    params: dict[str, Any] = {
        "chat_id": chat_id,
        "text": text,
        "message_thread_id": thread_id,
        "link_preview_options": LinkPreviewOptions(is_disabled=True),
    }
    if msg_id:
        params["reply_parameters"] = ReplyParameters(message_id=msg_id, allow_sending_without_reply=True)
    try:
        await bot.send_message(**params)
    except TelegramAPIError as exc:
        log.debug("Could not answer in %s: %s", chat_id, exc)


# --- Who may be globally banned ---------------------------------------------------

async def _protection(bot: Bot, user_id: int) -> str | None:
    """Why this user can never be globally banned, or None. Message is HTML."""
    if immunity.is_immune(user_id):
        return tr(IMMUNE_MESSAGE)
    if user_id == bot.id or await roles.get_role(user_id) is not None:
        return tr(PRIVILEGED_MESSAGE)
    if await whitelist.is_whitelisted(user_id):
        return tr("gbans.that_user_is_whitelisted_and_can")
    return None


# --- Removing people ----------------------------------------------------------------

_recent: dict[tuple[int, int], float] = {}
RECENT_SECONDS = 30
_tasks: set[asyncio.Task] = set()


def _first_recently(chat_id: int, user_id: int) -> bool:
    """
    True the first time a user is dealt with in a chat within a short while.
    Updates arrive in parallel, so a banned user who sends five messages at once
    would otherwise be banned, and announced, five times.
    """
    now = time.monotonic()
    key = (chat_id, user_id)
    last = _recent.get(key)
    if last is not None and now - last < RECENT_SECONDS:
        return False
    if len(_recent) > 10_000:
        for old_key in [k for k, t in _recent.items() if now - t >= RECENT_SECONDS]:
            del _recent[old_key]
    _recent[key] = now
    return True


async def _ban(bot: Bot, chat_id: int, user_id: int) -> bool:
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
            log.warning("Could not ban %s in %s: %s", user_id, chat_id, exc)
            return False
        await gbans.record_ban(user_id, chat_id)
        return True
    return False


async def _enforceable(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Whether a globally banned user is to be removed from this chat right now."""
    return await banguard.removable(bot, chat_id, user_id)


async def _remove_from(bot: Bot, chat_id: int, user_id: int) -> bool:
    return await _enforceable(bot, chat_id, user_id) and await _ban(bot, chat_id, user_id)


async def _alert(bot: Bot, chat_id: int, ban: dict, thread_id: int | None, name: str | None = None) -> None:
    # The name they have now, else the one the bot registered when it saw them, else the one from the ban.
    name = name or await users.name_of(ban["user_id"]) or ban["name"]
    lines = [
        tr("gbans.alert_detected_globally_banned_user"),
        tr("gbans.i_banned_them_here"),
        tr("gbans.user_2", user_id=user_ref(ban['user_id'], name)),
        tr("gbans.reason", reason=esc(ban['reason'])),
    ]
    if at_name(settings.SUPPORT_CHAT_USERNAME):
        lines.append(f"<b>Appeal Chat:</b> {esc(at_name(settings.SUPPORT_CHAT_USERNAME))}")
    await _notify(bot, chat_id, "\n".join(lines), thread_id=thread_id)


async def _enforce(bot: Bot, chat_id: int, user_id: int, thread_id: int | None, name: str | None = None) -> bool:
    """Remove them and say so. False when that failed (the bot is probably not allowed to ban here)."""
    ban = await gbans.get(user_id)
    if ban is None:
        return False
    if not _first_recently(chat_id, user_id):
        return True  # another update of theirs is dealing with it
    if not await _ban(bot, chat_id, user_id):
        _recent.pop((chat_id, user_id), None)  # try again with their next message
        return False
    await _alert(bot, chat_id, ban, thread_id, name)
    return True


def _spawn(coroutine) -> None:
    task = asyncio.get_running_loop().create_task(coroutine)
    _tasks.add(task)
    task.add_done_callback(_tasks.discard)


async def _propagate_unban(bot: Bot, user_id: int, origin: tuple[int, int | None, int | None] | None) -> None:
    """Unban them everywhere the bot knows of, then report to the chat the command came from."""
    started = time.monotonic()
    try:
        # Where the bot removed them, and every group it has seen them in (ban or not: only_if_banned).
        targets = dict.fromkeys(await gbans.banned_chats(user_id))
        for chat in await chats.chats_of_user(user_id):
            if chat["type"] in GROUP_TYPES and chat.get("bot_admin") is not False:
                targets.setdefault(chat["chat_id"])
        if origin is not None and origin[0] < 0:
            targets.setdefault(origin[0])  # the chat the command came from, always
        for chat_id in targets:
            try:
                await bot.unban_chat_member(chat_id, user_id, only_if_banned=True)
            except TelegramRetryAfter as exc:
                await asyncio.sleep(exc.retry_after)
            except TelegramAPIError as exc:
                log.debug("Could not unban %s in %s: %s", user_id, chat_id, exc)
            await asyncio.sleep(0.05)
        await gbans.forget_bans(user_id)
    except Exception:
        log.exception("Could not lift the bans of %s", user_id)
    if origin is not None:
        chat_id, msg_id, thread_id = origin
        seconds = round(time.monotonic() - started, 2)
        await _notify(
            bot, chat_id, lambda: tr("gbans.user_has_been_un_gbanned_time", seconds=seconds), msg_id, thread_id
        )


# --- Applying and lifting a ban ---------------------------------------------------------

async def _apply_ban(
    bot: Bot,
    *,
    target_id: int,
    target_name: str | None,
    reason: str,
    admin_id: int,
    admin_name: str | None,
    approved_by: tuple[int, str] | None,
    origin_chat: tuple[int, str] | None,
    delete: tuple[int, int] | None,
) -> dict | None:
    """Record the ban, remove the user where the command was given, and from the rest in the background."""
    old = await gbans.add(
        target_id,
        target_name,
        reason,
        admin_id,
        admin_name,
        approved_by[0] if approved_by else None,
        approved_by[1] if approved_by else None,
    )
    if delete is not None:
        with suppress(TelegramAPIError):
            await bot.delete_message(*delete)
    origin_id = None
    if origin_chat is not None and origin_chat[1] in GROUP_TYPES:
        origin_id = origin_chat[0]
        # Only when they are in the chat: a ban in a chat they never joined (an administrative
        # group, say) would only add a row to its ban list. Elsewhere it happens when they act.
        if await moderation.is_in_chat(bot, origin_id, target_id):
            await _remove_from(bot, origin_id, target_id)
    return old


# --- /gban, /dgban, /ungban --------------------------------------------------------------

def _request_keyboard(request_id: int) -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text=tr("gbans.approve"), callback_data=f"{CB_PREFIX}apr:{request_id}"),
                InlineKeyboardButton(text=tr("common.decline"), callback_data=f"{CB_PREFIX}dec:{request_id}"),
            ]
        ]
    )


async def _ask_sudo(
    message: Message,
    *,
    kind: str,
    tag: str,
    target: Target,
    reason: str,
    old: dict | None,
    target_msg_id: int | None,
) -> None:
    """A Support user's request: log it with Approve / Decline buttons for Sudo users."""
    actor = message.from_user
    origin = await logs.render(lambda: _origin_html(message))
    request_id = await gbans.create_request(
        target_id=target.user_id,
        target_name=target.name,
        kind=kind,
        reason=reason,
        requested_by=actor.id,
        requested_by_name=actor.full_name,
        chat_id=message.chat.id,
        msg_id=message.message_id,
        thread_id=_thread_of(message),
        target_msg_id=target_msg_id,
        origin_html=origin,
    )
    if request_id is None:
        await reply(message, tr("gbans.there_is_already_a_pending_request", user_id=user_ref(target.user_id, target.name)))
        return

    text = await logs.render(lambda: _log_text(
        tag,
        origin,
        user_ref(target.user_id, target.name),
        reason,
        user_ref(actor.id, actor.full_name),
        old_reason=old["reason"] if old else None,
        timestamp=False,
    ))
    if not await logs.role_log(message.bot, text, await logs.render(lambda: _request_keyboard(request_id))):
        await gbans.drop_request(request_id)
        await reply(message, tr("gbans.i_couldn_t_deliver_your_request"))
        return
    await reply(message, tr("gbans.request_sent"))


@router.message(cmd("gban", "dgban"), HasRole(Role.SUPPORT))
async def cmd_gban(message: Message, command: CommandObject, role: Role) -> None:
    bot = message.bot
    deleting = command.command == "dgban"
    replied = replied_message(message)

    try:
        if deleting:
            if replied is None:
                raise TargetError(tr("common.reply_to_the_message_of_the"))
            target = sender_as_target(replied)
            if target.is_chat or target.user_id in users.IGNORED_IDS:
                raise TargetError(tr("common.that_message_was_not_sent_by"))
            target.extra = (command.args or "").strip()
        else:
            target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    who = user_ref(target.user_id, target.name)
    reason = target.extra.strip()

    if immunity.is_immune(target.user_id):
        await reply(message, tr(IMMUNE_MESSAGE))
        return
    if not reason:
        await reply(message, tr(NO_REASON))
        return
    if len(reason) > MAX_REASON_LENGTH:
        await reply(message, tr("gbans.the_reason_is_too_long_at", MAX_REASON_LENGTH=MAX_REASON_LENGTH))
        return
    problem = await _protection(bot, target.user_id)
    if problem is not None:
        await reply(message, problem)
        return
    if await gbans.has_pending(target.user_id):
        await reply(
            message,
            tr("gbans.there_is_already_a_pending_request_2", who=who),
        )
        return
    old = await gbans.get(target.user_id)
    if old and old["reason"].strip() == reason:
        await reply(message, tr("gbans.is_already_globally_banned_for_the", who=who))
        return

    target_msg_id = replied.message_id if deleting else None
    if role < SUDO:
        await _ask_sudo(
            message,
            kind="dgban" if deleting else "gban",
            tag="#GBANUPDATEREQUEST" if old else "#GBANREQUEST",
            target=target,
            reason=reason,
            old=old,
            target_msg_id=target_msg_id,
        )
        return

    admin = message.from_user
    old = await _apply_ban(
        bot,
        target_id=target.user_id,
        target_name=target.name,
        reason=reason,
        admin_id=admin.id,
        admin_name=admin.full_name,
        approved_by=None,
        origin_chat=(message.chat.id, message.chat.type),
        delete=(message.chat.id, target_msg_id) if deleting else None,
    )
    await logs.role_log(
        bot,
        await logs.render(lambda: _log_text(
            _tag_for(old),
            _origin_html(message),
            who,
            reason,
            user_ref(admin.id, admin.full_name),
            old_reason=old["reason"] if old else None,
        )),
    )
    await reply(message, _done_text(old))


@router.message(cmd("ungban"), HasRole(Role.SUPPORT))
async def cmd_ungban(message: Message, command: CommandObject, role: Role) -> None:
    bot = message.bot
    try:
        target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    who = user_ref(target.user_id, target.name)
    reason = target.extra.strip()

    if immunity.is_immune(target.user_id):
        await reply(message, tr(IMMUNE_MESSAGE))
        return
    if not reason:
        await reply(message, tr(NO_REASON))
        return
    if len(reason) > MAX_REASON_LENGTH:
        await reply(message, tr("gbans.the_reason_is_too_long_at", MAX_REASON_LENGTH=MAX_REASON_LENGTH))
        return
    if target.user_id == bot.id or await roles.get_role(target.user_id) is not None:
        await reply(message, tr("gbans.privileged_users_are_never_gbanned"))
        return
    if not await gbans.is_gbanned(target.user_id):
        await reply(message, tr("gbans.is_not_globally_banned", who=who))
        return
    if await gbans.has_pending(target.user_id):
        await reply(
            message,
            tr("gbans.there_is_already_a_pending_request_2", who=who),
        )
        return

    if role < SUDO:
        await _ask_sudo(
            message,
            kind="ungban",
            tag="#UNGBANREQUEST",
            target=target,
            reason=reason,
            old=None,
            target_msg_id=None,
        )
        return

    admin = message.from_user
    if await gbans.remove(target.user_id) is None:
        await reply(message, tr("gbans.is_not_globally_banned", who=who))
        return
    await logs.role_log(
        bot,
        await logs.render(lambda: _log_text("#UNGBANNED", _origin_html(message), who, reason, user_ref(admin.id, admin.full_name))),
    )
    await reply(message, tr("gbans.let_s_give_them_another_chance"))
    _spawn(_propagate_unban(bot, target.user_id, _reply_place(message)))


# --- The buttons under a request -------------------------------------------------------------

@router.callback_query(F.data.startswith(CB_PREFIX))
async def cb_request(callback: CallbackQuery, role: Role | None = None) -> None:
    bot = callback.bot
    if role is None or role < SUDO:
        await callback.answer(tr("gbans.only_sudo_users_and_above_can"), show_alert=True)
        return

    parts = (callback.data or "").split(":")
    if len(parts) != 3 or parts[1] not in ("apr", "dec") or not parts[2].isdigit():
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    approving = parts[1] == "apr"

    message = callback.message if isinstance(callback.message, Message) else None
    request = await gbans.claim_request(int(parts[2]))
    if request is None:
        await callback.answer(tr("gbans.request_not_found_likely_already_processed"), show_alert=True)
        if message is not None:
            with suppress(TelegramAPIError):
                await message.edit_reply_markup(reply_markup=None)
        return

    approver = callback.from_user
    chat_id, msg_id, thread_id = request["chat_id"], request["msg_id"], request["thread_id"]
    target_id = request["target_id"]
    who = user_ref(target_id, request["target_name"])
    requester = user_ref(request["requested_by"], request["requested_by_name"])
    origin = request["origin_html"]
    reason = request["reason"]

    async def finish(text: str) -> None:
        if message is not None:
            await edit(message, text, reply_markup=None, link_preview_options=LinkPreviewOptions(is_disabled=True))

    if not approving:
        await callback.answer(tr("gbans.request_declined"))
        if message is not None:
            await finish(await logs.render(lambda: tr("gbans.declined_by", html_text=message.html_text, id=user_ref(approver.id, approver.full_name))))
        await _notify(bot, chat_id, lambda: tr("gbans.request_declined_2"), msg_id, thread_id)
        return

    if request["kind"] in ("gban", "dgban"):
        problem = await _protection(bot, target_id)
        if problem is not None:
            await callback.answer(tr("gbans.this_user_can_no_longer_be"), show_alert=True)
            await finish(await logs.render(lambda: tr("gbans.cancelled", opt=message.html_text if message else '', problem=problem)))
            await _notify(bot, chat_id, lambda: tr("gbans.request_cancelled", problem=problem), msg_id, thread_id)
            return
        await callback.answer(tr("gbans.approved"))
        chat_type = "private" if chat_id > 0 else "supergroup"  # only groups matter for banning
        old = await _apply_ban(
            bot,
            target_id=target_id,
            target_name=request["target_name"],
            reason=reason,
            admin_id=request["requested_by"],
            admin_name=request["requested_by_name"],
            approved_by=(approver.id, approver.full_name),
            origin_chat=(chat_id, chat_type),
            delete=(chat_id, request["target_msg_id"])
            if request["kind"] == "dgban" and request["target_msg_id"]
            else None,
        )
        await finish(await logs.render(lambda: _log_text(
            _tag_for(old),
            origin,
            who,
            reason,
            requester,
            old_reason=old["reason"] if old else None,
            approved_by=user_ref(approver.id, approver.full_name),
        )))
        await _notify(bot, chat_id, lambda: _done_text(old), msg_id, thread_id)
        return

    # ungban
    removed = await gbans.remove(target_id)
    if removed is None:
        await callback.answer(tr("gbans.this_user_is_not_globally_banned"), show_alert=True)
        await finish(await logs.render(lambda: tr("gbans.cancelled_not_globally_banned_any_more", opt=message.html_text if message else '')))
        return
    await callback.answer(tr("gbans.approved"))
    await finish(await logs.render(lambda: _log_text(
        "#UNGBANNED",
        origin,
        who,
        reason,
        requester,
        approved_by=user_ref(approver.id, approver.full_name),
    )))
    _spawn(_propagate_unban(bot, target_id, (chat_id, msg_id if chat_id < 0 else None, thread_id)))


# --- /gstat -------------------------------------------------------------------------------------

@router.message(cmd("gstat", "gbanstat"))
async def cmd_gstat(message: Message, command: CommandObject, role: Role | None = None) -> None:
    bot = message.bot
    viewer = message.from_user
    if viewer is None or viewer.id in users.IGNORED_IDS:
        await reply(message, tr("gbans.i_can_t_tell_who_you"))
        return

    if role is not None and (command.args or replied_message(message)):
        try:
            target = await resolve_target(message, command, allow_bots=True)
        except TargetError as exc:
            await reply(message, esc(exc))
            return
        target_id, stored_name = target.user_id, target.name
    else:
        target_id, stored_name = viewer.id, viewer.full_name

    if immunity.is_immune(target_id):
        await reply(message, tr("gbans.this_account_is_immune_to_all"))
        return

    ban = await gbans.get(target_id)
    name = await display_name(bot, target_id, (ban or {}).get("name") or stored_name)
    title = tr("gbans.your_global_ban_status") if target_id == viewer.id else tr("gbans.global_ban_status")
    lines = [f"<b>{title}</b>", tr("gbans.user_3", target_id=user_ref(target_id, name)), ""]

    target_role = await roles.get_role(target_id)
    if target_role is not None:
        lines.append(f"• <b>Status:</b> <code>{roles.label(target_role)}</code>")
    elif ban:
        lines.append("• <b>Status:</b> <code>Banned</code>")
        lines.append(f"• <b>Reason:</b> <code>{esc(ban['reason'])}</code>")
        lines.append(f"• <b>Timestamp:</b> {logs.timestamp(ban['created_at'])}")
        if await whitelist.is_whitelisted(target_id):
            lines.append("• <b>Whitelisted:</b> <code>Yes</code> (not removed from chats)")
        if role is not None and role >= SUDO:
            if ban["admin_id"] is None:
                lines.append("• <b>Admin:</b> This ban is imported.")
            else:
                lines.append(f"• <b>Admin:</b> {user_ref(ban['admin_id'], ban['admin_name'])}")
    else:
        lines.append("• <b>Status:</b> <code>Not Banned</code>")

    await reply(message, "\n".join(lines), link_preview_options=LinkPreviewOptions(is_disabled=True))


# --- /gbans: enforcement in a chat ---------------------------------------------------------------

@router.message(cmd("gbans", "enforcegban"))
async def cmd_gbans(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """
    Whether this chat removes globally banned users. On by default. Chat admins can
    look at the setting, only the chat's owner can change it, and the bot's staff
    can't change it either (see /staffhelp).
    """
    argument = (command.args or "").strip().lower()

    if not argument:
        if await moderation.begin_chat_command(message, role) is None:
            return
        enabled = await chatsettings.gban_enforce(message.chat.id)
        await reply(
            message,
            tr("gbans.global_ban_enforcement_current_status_for", opt=tr("gbans.enabled") if enabled else tr("gbans.disabled")),
        )
        return

    if argument not in _ANSWERS:
        await reply(
            message,
            tr("gbans.invalid_choice_use_gbans_on_or"),
        )
        return

    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return

    enabled = _ANSWERS[argument]
    await chatsettings.set_gban_enforce(message.chat.id, enabled)
    if enabled:
        await reply(message, tr("gbans.global_ban_enforcement_has_been_enabled"))
    else:
        await reply(
            message,
            tr("gbans.global_ban_enforcement_has_been_disabled"),
        )


# --- /gimport and /gexport -----------------------------------------------------------------------------

EXPORT_COOLDOWN = 30.0
_exported_at = 0.0


@router.message(cmd("gimport"), HasRole(Role.OWNER))
async def cmd_gimport(message: Message, command: CommandObject) -> None:
    fmt = (command.args or "").strip().lower() or None
    if fmt is not None and fmt not in banfiles.FORMATS:
        await reply(message, tr("gbans.use_gimport_csv_minicsv_json_human"))
        return
    document = message.document or (message.reply_to_message.document if message.reply_to_message else None)
    if document is None:
        await reply(message, tr("gbans.reply_to_a_file_with_the"))
        return
    if (document.file_size or 0) > banfiles.MAX_IMPORT_BYTES:
        await reply(message, tr("common.that_file_is_too_big_the"))
        return

    status = await reply(message, tr("gbans.importing_bans_please_wait"))
    try:
        buffer = io.BytesIO()
        await message.bot.download(document, destination=buffer)
        try:
            parsed = banfiles.parse_import(buffer.getvalue().decode("utf-8", errors="replace"), fmt)
        except ValueError as exc:
            await edit(status, esc(exc))
            return

        found: dict[int, tuple[str, str | None]] = {}
        for user_id, name, reason in parsed:
            reason = " ".join((reason or "").split())[:MAX_REASON_LENGTH] or tr("gbans.no_reason_given")
            found.setdefault(user_id, (reason, name))
        rows: list[tuple[int, str, str | None]] = []
        protected = 0
        for user_id, (reason, name) in found.items():
            if await whitelist.is_protected(user_id) or await roles.get_role(user_id) is not None:
                protected += 1
            else:
                rows.append((user_id, reason, name))

        added = 0
        for start in range(0, len(rows), IMPORT_CHUNK):
            added += await gbans.import_many(rows[start : start + IMPORT_CHUNK])
    except Exception:
        log.exception("Importing bans failed")
        await edit(status, tr("gbans.the_import_failed_nothing_more_was"))
        raise

    skipped = len(rows) - added + protected
    await edit(
        status,
        tr("gbans.imported_ban", added=added, opt='s' if added != 1 else '')
        + (tr("common.skipped_already_banned_or_protected", skipped=skipped) if skipped else ""),
    )
    admin = message.from_user
    await logs.role_log(
        message.bot,
        await logs.render(lambda: tr("gbans.gbanimport_added_timestamp_admin", added=added, timestamp=logs.timestamp(), id=user_ref(admin.id, admin.full_name))),
    )


@router.message(cmd("gexport"), HasRole(Role.OWNER))
async def cmd_gexport(message: Message, command: CommandObject) -> None:
    global _exported_at
    fmt = (command.args or "csv").strip().lower() or "csv"
    if fmt not in banfiles.FORMATS:
        await reply(message, tr("gbans.use_gexport_csv_minicsv_json_or"))
        return
    now = time.monotonic()
    if now - _exported_at < EXPORT_COOLDOWN:
        await reply(message, tr("common.wait_a_little_before_exporting_again"))
        return
    rows = await gbans.all_bans()
    if not rows:
        await reply(message, tr("gbans.there_are_no_global_bans_to"))
        return
    _exported_at = now
    data, extension = banfiles.render_export(rows, fmt)
    await message.bot.send_document(
        message.chat.id,
        BufferedInputFile(data, filename=f"gbans.{extension}"),
        caption=tr("gbans.global_bans", rows=len(rows)),
        message_thread_id=message.message_thread_id if message.is_topic_message else None,
    )


# --- Enforcement ---------------------------------------------------------------------------------------------

async def _banned_in_message(message: Message, bot: Bot) -> bool:
    """A message from a globally banned user in a chat that removes them."""
    user = message.from_user
    if user is None or message.chat.type not in GROUP_TYPES:
        return False
    if getattr(message, "new_chat_members", None) or getattr(message, "left_chat_member", None):
        return False  # joins and leaves are handled as member updates
    if not await gbans.is_gbanned(user.id):
        return False
    return await _enforceable(bot, message.chat.id, user.id)


@router.message(_banned_in_message)
@router.edited_message(_banned_in_message)
async def enforce_on_message(message: Message, bot: Bot) -> None:
    with suppress(TelegramAPIError):
        await message.delete()
    await _enforce(bot, message.chat.id, message.from_user.id, _thread_of(message), message.from_user.full_name)


def _moved(event: ChatMemberUpdated) -> bool:
    """Joining or leaving, as in KryoRobot: a ban (by anyone) or another change is not acted on."""
    def value(member) -> str:
        return str(getattr(member.status, "value", member.status))

    before, after = value(event.old_chat_member), value(event.new_chat_member)
    return (after == "member" and before != "member") or (after == "left" and before == "member")


async def _banned_active(event: ChatMemberUpdated, bot: Bot) -> bool:
    user = event.new_chat_member.user
    if event.chat.type not in GROUP_TYPES or user.id == bot.id or not _moved(event):
        return False
    if not await gbans.is_gbanned(user.id):
        return False
    return await _enforceable(bot, event.chat.id, user.id)


class JoinMiddleware(BaseMiddleware):
    """
    The "X joined" service message: globally banned newcomers are removed from it too, so that a
    chat in which member updates do not reach the bot (they come only when the bot is an admin
    that can see them) still gets them removed. When nobody but banned users joined, the message
    goes no further, so there is no welcome or captcha for them.
    """

    async def __call__(self, handler: Callable[[Any, dict[str, Any]], Awaitable[Any]], event: Any, data: dict[str, Any]) -> Any:
        members = getattr(event, "new_chat_members", None)
        if members and getattr(event.chat, "type", None) in GROUP_TYPES:
            bot = data.get("bot") or event.bot
            removed = 0
            try:
                for user in members:
                    if user.id == bot.id or not await gbans.is_gbanned(user.id):
                        continue
                    if await _enforceable(bot, event.chat.id, user.id):
                        if await _enforce(bot, event.chat.id, user.id, _thread_of(event), user.full_name):
                            removed += 1
                    else:
                        log.info("Globally banned user %s joined %s and was left alone", user.id, event.chat.id)
            except Exception:
                log.exception("Handling a join for global bans failed")
            if removed and removed == len(members):
                await banguard.clean_join_message(event)  # greetings would have, but it never sees this message
                return None
        return await handler(event, data)


router.message.outer_middleware(JoinMiddleware())


@router.chat_member(_banned_active)
async def enforce_on_member_change(event: ChatMemberUpdated, bot: Bot) -> None:
    await _enforce(bot, event.chat.id, event.new_chat_member.user.id, None, event.new_chat_member.user.full_name)


async def _banned_reacting(reaction: MessageReactionUpdated, bot: Bot) -> bool:
    user = reaction.user
    if user is None or reaction.chat.type not in GROUP_TYPES or not await gbans.is_gbanned(user.id):
        return False
    return await _enforceable(bot, reaction.chat.id, user.id)


@router.message_reaction(_banned_reacting)
async def enforce_on_reaction(reaction: MessageReactionUpdated, bot: Bot) -> None:
    await _enforce(bot, reaction.chat.id, reaction.user.id, None, reaction.user.full_name)


# --- /info ----------------------------------------------------------------------------------------------------

async def _info_flags(bot: Bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    return [(tr("gbans.globally_banned"), await gbans.is_gbanned(user_id))]


info_hooks.register("gbans", _info_flags)
