"""Global bans"""

import asyncio
import io
import json
import logging
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
    ReplyParameters,
)

from config import settings
from core import approvals, banfiles, chats, chatsettings, gbans, immunity, info_hooks, logs, moderation, roles, users, whitelist
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.targets import Target, TargetError, display_name, replied_message, resolve_target, sender_as_target
from core.text import esc, user_ref, SWITCH as _ANSWERS

log = logging.getLogger(__name__)

# Looked at before every other module: a globally banned user is removed before
# any command of theirs could run (see main.discover_modules).
PRIORITY = -100

ABOUT = """
A global ban works in every chat I'm in: I remove the user as soon as they show up there (write, edit, react, join or leave). They are handled by my staff: Sudo and up ban immediately, while Support users send a request that a Sudo user approves or declines with buttons.

Banned users are removed when they write or join. Each chat can switch this off with /gbans. Whitelisted and approved users are skipped, and lifting a global ban only undoes the bans I made because of it. The ban message names the support chat to appeal in, when one is configured.
"""

HELP = [
    ("/gban <user> <reason>", "Ban a user in every chat. Support users send a request instead.", Role.SUPPORT),
    ("/dgban <reason>", "Reply to a message: delete it and ban its author globally.", Role.SUPPORT),
    ("/ungban <user> <reason>", "Lift a global ban. Support users send a request instead.", Role.SUPPORT),
    ("/gstat [user]", "Show whether you are globally banned, and why. Support users can look up anyone. Alias: /gbanstat."),
    ("/gbans [on|off]", "Choose whether this chat removes globally banned users. On by default; any admin can look, only the chat owner can change it. Alias: /enforcegban.", "chat owner"),
    ("/gimport [csv|minicsv|json|human]", "Reply to a file to import global bans from it (or send the file with the command). The format is detected if you leave it out. JSON files with one ban per line work too.", Role.OWNER),
    ("/gexport [csv|minicsv|json|human]", "Get the list of global bans as a file. The default is csv.", Role.OWNER),
]

router = Router(name="gbans")

MAX_REASON_LENGTH = 500
CB_PREFIX = "gban:"  # gban:apr:<request id> / gban:dec:<request id>
IMPORT_CHUNK = 5000

SUDO = Role.SUDO


NO_REASON = "Give a reason!"
IMMUNE_MESSAGE = "I can't take action against that account."
PRIVILEGED_MESSAGE = "LoL, looks like... Someone tried gban privileged user. Nice Try."


# --- Wording -------------------------------------------------------------------

def _thread_of(message: Message) -> int | None:
    return message.message_thread_id if message.is_topic_message else None


def _reply_place(message: Message) -> tuple[int, int | None, int | None]:
    """Where to report back to: (chat, message to answer, topic). Private chats need no quote."""
    if message.chat.type == "private":
        return message.chat.id, None, None
    return message.chat.id, message.message_id, _thread_of(message)


def _origin_html(message: Message) -> str:
    """The "Initiated From" line: where the command was given, with a link when there is one."""
    chat = message.chat
    if chat.type == "private":
        place = f"PM with {esc(message.from_user.first_name)}"
    elif chat.username:
        place = f'<a href="https://t.me/{esc(chat.username)}/{message.message_id}">{esc(chat.title)}</a>'
    elif str(chat.id).startswith("-100"):
        place = f'<a href="https://t.me/c/{str(chat.id)[4:]}/{message.message_id}">{esc(chat.title)}</a>'
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
        f"<b>Initiated From:</b> {origin}",
        "",
        f"<b>User:</b> {target}",
        f"<b>Reason:</b> <code>{esc(reason)}</code>",
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
        return f"Done! Gban reason updated.\n<b>Old Reason:</b> <code>{esc(old['reason'])}</code>"
    return "Done! Gbanned."


async def _notify(bot: Bot, chat_id: int, text: str, msg_id: int | None = None, thread_id: int | None = None) -> None:
    """Say something in the chat a command came from, in its topic, never failing."""
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
        return IMMUNE_MESSAGE
    if user_id == bot.id or await roles.get_role(user_id) is not None:
        return PRIVILEGED_MESSAGE
    if await whitelist.is_whitelisted(user_id):
        return "That user is whitelisted and can't be globally banned."
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
            log.debug("Could not ban %s in %s: %s", user_id, chat_id, exc)
            return False
        await gbans.record_ban(user_id, chat_id)
        return True
    return False


async def _enforceable(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Whether a globally banned user is to be removed from this chat right now."""
    if not await chatsettings.gban_enforce(chat_id):
        return False
    if await whitelist.is_protected(user_id) or await roles.get_role(user_id) is not None:
        return False
    if await approvals.is_approved(chat_id, user_id):
        return False
    return not await moderation.is_chat_admin(bot, chat_id, user_id)


async def _remove_from(bot: Bot, chat_id: int, user_id: int) -> bool:
    return await _enforceable(bot, chat_id, user_id) and await _ban(bot, chat_id, user_id)


async def _alert(bot: Bot, chat_id: int, ban: dict, thread_id: int | None) -> None:
    lines = [
        "<b>Alert!</b> Detected globally banned user.",
        "<code>I banned them here!</code>",
        f"<b>User:</b> {user_ref(ban['user_id'], ban['name'])}",
        f"<b>Reason:</b> <code>{esc(ban['reason'])}</code>",
    ]
    if settings.APPEAL_CHAT_USERNAME:
        lines.append(f"<b>Appeal Chat:</b> {esc(settings.APPEAL_CHAT_USERNAME)}")
    await _notify(bot, chat_id, "\n".join(lines), thread_id=thread_id)


async def _enforce(bot: Bot, chat_id: int, user_id: int, thread_id: int | None) -> None:
    ban = await gbans.get(user_id)
    if ban is None or not _first_recently(chat_id, user_id):
        return
    if not await _ban(bot, chat_id, user_id):
        _recent.pop((chat_id, user_id), None)  # try again with their next message
        return
    await _alert(bot, chat_id, ban, thread_id)


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
            bot, chat_id, f"User has been un-gbanned.\nTime taken: <code>{seconds}s</code>", msg_id, thread_id
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
        await _remove_from(bot, origin_id, target_id)
    return old


# --- /gban, /dgban, /ungban --------------------------------------------------------------

def _request_keyboard(request_id: int) -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Approve", callback_data=f"{CB_PREFIX}apr:{request_id}"),
                InlineKeyboardButton(text="Decline", callback_data=f"{CB_PREFIX}dec:{request_id}"),
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
    origin = _origin_html(message)
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
        await reply(message, f"There is already a pending request for {user_ref(target.user_id, target.name)}.")
        return

    text = _log_text(
        tag,
        origin,
        user_ref(target.user_id, target.name),
        reason,
        user_ref(actor.id, actor.full_name),
        old_reason=old["reason"] if old else None,
        timestamp=False,
    )
    if not await logs.role_log(message.bot, text, _request_keyboard(request_id)):
        await gbans.drop_request(request_id)
        await reply(message, "I couldn't deliver your request to the log chat. Try again later.")
        return
    await reply(message, "Request sent.")


@router.message(cmd("gban", "dgban"), HasRole(Role.SUPPORT))
async def cmd_gban(message: Message, command: CommandObject, role: Role) -> None:
    bot = message.bot
    deleting = command.command == "dgban"
    replied = replied_message(message)

    try:
        if deleting:
            if replied is None:
                raise TargetError("Reply to the message of the user you want to delete and ban.")
            target = sender_as_target(replied)
            if target.is_chat or target.user_id in users.IGNORED_IDS:
                raise TargetError("That message was not sent by a regular user.")
            target.extra = (command.args or "").strip()
        else:
            target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    who = user_ref(target.user_id, target.name)
    reason = target.extra.strip()

    if immunity.is_immune(target.user_id):
        await reply(message, IMMUNE_MESSAGE)
        return
    if not reason:
        await reply(message, NO_REASON)
        return
    if len(reason) > MAX_REASON_LENGTH:
        await reply(message, f"The reason is too long (at most {MAX_REASON_LENGTH} characters).")
        return
    problem = await _protection(bot, target.user_id)
    if problem is not None:
        await reply(message, problem)
        return
    if await gbans.has_pending(target.user_id):
        await reply(
            message,
            f"There is already a pending request for {who} waiting for approval in the log channel.",
        )
        return
    old = await gbans.get(target.user_id)
    if old and old["reason"].strip() == reason:
        await reply(message, f"{who} is already globally banned for the same reason. <b>No changes made.</b>")
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
        _log_text(
            _tag_for(old),
            _origin_html(message),
            who,
            reason,
            user_ref(admin.id, admin.full_name),
            old_reason=old["reason"] if old else None,
        ),
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
        await reply(message, IMMUNE_MESSAGE)
        return
    if not reason:
        await reply(message, NO_REASON)
        return
    if len(reason) > MAX_REASON_LENGTH:
        await reply(message, f"The reason is too long (at most {MAX_REASON_LENGTH} characters).")
        return
    if target.user_id == bot.id or await roles.get_role(target.user_id) is not None:
        await reply(message, "Privileged users are never gbanned...")
        return
    if not await gbans.is_gbanned(target.user_id):
        await reply(message, f"{who} is not globally banned.")
        return
    if await gbans.has_pending(target.user_id):
        await reply(
            message,
            f"There is already a pending request for {who} waiting for approval in the log channel.",
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
        await reply(message, f"{who} is not globally banned.")
        return
    await logs.role_log(
        bot,
        _log_text("#UNGBANNED", _origin_html(message), who, reason, user_ref(admin.id, admin.full_name)),
    )
    await reply(message, "Let's give them another chance!")
    _spawn(_propagate_unban(bot, target.user_id, _reply_place(message)))


# --- The buttons under a request -------------------------------------------------------------

@router.callback_query(F.data.startswith(CB_PREFIX))
async def cb_request(callback: CallbackQuery, role: Role | None = None) -> None:
    bot = callback.bot
    if role is None or role < SUDO:
        await callback.answer("Only Sudo users and above can process requests.", show_alert=True)
        return

    parts = (callback.data or "").split(":")
    if len(parts) != 3 or parts[1] not in ("apr", "dec") or not parts[2].isdigit():
        await callback.answer("This button is not valid.", show_alert=True)
        return
    approving = parts[1] == "apr"

    message = callback.message if isinstance(callback.message, Message) else None
    request = await gbans.claim_request(int(parts[2]))
    if request is None:
        await callback.answer("Request not found (likely already processed).", show_alert=True)
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
        await callback.answer("Request declined.")
        if message is not None:
            await finish(f"{message.html_text}\n<b>Declined by:</b> {user_ref(approver.id, approver.full_name)}")
        await _notify(bot, chat_id, "Request Declined.", msg_id, thread_id)
        return

    if request["kind"] in ("gban", "dgban"):
        problem = await _protection(bot, target_id)
        if problem is not None:
            await callback.answer("This user can no longer be globally banned.", show_alert=True)
            await finish(f"{message.html_text if message else ''}\n<b>Cancelled:</b> {problem}")
            await _notify(bot, chat_id, f"Request cancelled. {problem}", msg_id, thread_id)
            return
        await callback.answer("Approved.")
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
        await finish(
            _log_text(
                _tag_for(old),
                origin,
                who,
                reason,
                requester,
                old_reason=old["reason"] if old else None,
                approved_by=user_ref(approver.id, approver.full_name),
            )
        )
        await _notify(bot, chat_id, _done_text(old), msg_id, thread_id)
        return

    # ungban
    removed = await gbans.remove(target_id)
    if removed is None:
        await callback.answer("This user is not globally banned any more.", show_alert=True)
        await finish(f"{message.html_text if message else ''}\n<b>Cancelled:</b> not globally banned any more.")
        return
    await callback.answer("Approved.")
    await finish(
        _log_text(
            "#UNGBANNED",
            origin,
            who,
            reason,
            requester,
            approved_by=user_ref(approver.id, approver.full_name),
        )
    )
    _spawn(_propagate_unban(bot, target_id, (chat_id, msg_id if chat_id < 0 else None, thread_id)))


# --- /gstat -------------------------------------------------------------------------------------

@router.message(cmd("gstat", "gbanstat"))
async def cmd_gstat(message: Message, command: CommandObject, role: Role | None = None) -> None:
    bot = message.bot
    viewer = message.from_user
    if viewer is None or viewer.id in users.IGNORED_IDS:
        await reply(message, "I can't tell who you are. Send this as yourself, not anonymously.")
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
        await reply(message, "This account is immune to all bot actions.")
        return

    ban = await gbans.get(target_id)
    name = await display_name(bot, target_id, (ban or {}).get("name") or stored_name)
    title = "Your Global Ban Status:" if target_id == viewer.id else "Global Ban Status:"
    lines = [f"<b>{title}</b>", f"• <b>User:</b> {user_ref(target_id, name)}", ""]

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
    never changes it (see /staffhelp).
    """
    argument = (command.args or "").strip().lower()

    if not argument:
        if await moderation.begin_chat_command(message, role, allow_staff=False) is None:
            return
        enabled = await chatsettings.gban_enforce(message.chat.id)
        await reply(
            message,
            "<b>Global Ban Enforcement</b>\n\n"
            f"Current status for this chat: <b>{'ENABLED' if enabled else 'DISABLED'}</b>\n"
            "<b>Usage:</b> <code>/gbans &lt;on/off&gt;</code>",
        )
        return

    if argument not in _ANSWERS:
        await reply(
            message,
            "<b>Invalid choice!</b>\n\nUse: <code>/gbans on</code> or <code>/gbans off</code>",
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
        await reply(message, "<b>Global Ban enforcement has been ENABLED.</b>")
    else:
        await reply(
            message,
            "<b>Global Ban enforcement has been DISABLED.</b>\n"
            "<i>Warning: Gbanned users will no longer be removed automatically.</i>",
        )


# --- /gimport and /gexport -----------------------------------------------------------------------------

EXPORT_COOLDOWN = 30.0
_exported_at = 0.0


@router.message(cmd("gimport"), HasRole(Role.OWNER))
async def cmd_gimport(message: Message, command: CommandObject) -> None:
    fmt = (command.args or "").strip().lower() or None
    if fmt is not None and fmt not in banfiles.FORMATS:
        await reply(message, "Use /gimport [csv|minicsv|json|human], as a reply to a file or with the file attached.")
        return
    document = message.document or (message.reply_to_message.document if message.reply_to_message else None)
    if document is None:
        await reply(message, "Reply to a file with the bans (csv, minicsv, json or human), or send it with the command.")
        return
    if (document.file_size or 0) > banfiles.MAX_IMPORT_BYTES:
        await reply(message, "That file is too big (the limit is 20 MB).")
        return

    status = await reply(message, "Importing bans... Please wait!")
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
            reason = " ".join((reason or "").split())[:MAX_REASON_LENGTH] or "No reason given."
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
        await edit(status, "The import failed. Nothing more was imported; see the developer log.")
        raise

    await edit(
        status,
        "<b>Import Bans Successful!</b>\n"
        f"Added <code>{added}</code> new bans to the database.\n"
        f"Already banned: <code>{len(rows) - added}</code>, "
        f"protected: <code>{protected}</code>.",
    )
    admin = message.from_user
    await logs.role_log(
        message.bot,
        "<b>#GBANIMPORT</b>\n"
        f"<b>Added:</b> <code>{added}</code>\n"
        f"<b>Timestamp:</b> {logs.timestamp()}\n"
        f"<b>Admin:</b> {user_ref(admin.id, admin.full_name)}",
    )


@router.message(cmd("gexport"), HasRole(Role.OWNER))
async def cmd_gexport(message: Message, command: CommandObject) -> None:
    global _exported_at
    fmt = (command.args or "csv").strip().lower() or "csv"
    if fmt not in banfiles.FORMATS:
        await reply(message, "Use /gexport csv, minicsv, json or human.")
        return
    now = time.monotonic()
    if now - _exported_at < EXPORT_COOLDOWN:
        await reply(message, "Wait a little before exporting again.")
        return
    rows = await gbans.all_bans()
    if not rows:
        await reply(message, "There are no global bans to export.")
        return
    _exported_at = now
    data, extension = banfiles.render_export(rows, fmt)
    await message.bot.send_document(
        message.chat.id,
        BufferedInputFile(data, filename=f"gbans.{extension}"),
        caption=f"{len(rows)} global bans.",
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
    await _enforce(bot, message.chat.id, message.from_user.id, _thread_of(message))


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


@router.chat_member(_banned_active)
async def enforce_on_member_change(event: ChatMemberUpdated, bot: Bot) -> None:
    await _enforce(bot, event.chat.id, event.new_chat_member.user.id, None)


async def _banned_reacting(reaction: MessageReactionUpdated, bot: Bot) -> bool:
    user = reaction.user
    if user is None or reaction.chat.type not in GROUP_TYPES or not await gbans.is_gbanned(user.id):
        return False
    return await _enforceable(bot, reaction.chat.id, user.id)


@router.message_reaction(_banned_reacting)
async def enforce_on_reaction(reaction: MessageReactionUpdated, bot: Bot) -> None:
    await _enforce(bot, reaction.chat.id, reaction.user.id, None)


# --- /info ----------------------------------------------------------------------------------------------------

async def _info_flags(bot: Bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    return [("Globally Banned", await gbans.is_gbanned(user_id))]


info_hooks.register("gbans", _info_flags)
