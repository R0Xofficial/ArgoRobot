"""
Shared pieces of the moderation modules (bans, mutes, warns).

Who may use the commands: admins of the group (with the right to restrict
users), and the bot's Owner, Developers and Sudo users, who are trusted to help
keep order in any group without being admins there. Anonymous admins count as
admins. Support users and regular members may not.

A command handler starts with `begin()`: it checks the person, finds the target
user (see core/targets.py) and refuses targets that must never be touched
(yourself, the bot, immune accounts, chat admins). Everything it refuses is
answered with a short plain message, so handlers only continue on success.

Calls to Telegram go through `perform()`, which turns the usual refusals
("not enough rights", "user is an administrator", ...) into messages meant for
people. Unexpected errors are left to the normal error reporting.
"""

import logging
import re
from dataclasses import dataclass
from types import SimpleNamespace
from datetime import timedelta
from typing import Any, Awaitable

from aiogram.exceptions import TelegramAPIError, TelegramBadRequest, TelegramForbiddenError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import chatlog, chatsettings, immunity, roles
from core.helpers import reply
from core.roles import Role
from core.targets import Target, TargetError, replied_message, resolve_target
from core.text import esc, truncate, user_link, user_ref

log = logging.getLogger(__name__)

GROUP_TYPES = ("group", "supergroup")

# Staff who may moderate in any group, without being admins there.
STAFF_ROLE = Role.SUDO

MAX_REASON_LENGTH = 300

RIGHTS_MESSAGE = "I don't have the rights to do that. Make me an admin with permission to restrict users."


class ModerationError(Exception):
    """Raised with a message that is safe to show as it is (plain text)."""


class NotAllowed(ModerationError):
    """The person may not use the command here. Chats can choose not to answer these (/adminerror)."""


async def deny(message: Message, exc: Exception) -> None:
    """Answer a refusal, unless it is a "not allowed" and the chat asked not to hear those."""
    if isinstance(exc, NotAllowed) and not await chatsettings.admin_errors(message.chat.id):
        return
    await reply(message, esc(exc))


@dataclass
class Actor:
    """Whoever gave the command. Anonymous admins have no id."""

    id: int | None
    name: str
    anonymous: bool = False
    staff: bool = False  # acting as the bot's staff, not as an admin of the chat

    @property
    def link(self) -> str:
        return "Anonymous admin" if self.anonymous else user_link(self.id, self.name)


def actor_of(message: Message) -> Actor:
    chat = message.chat
    sender_chat = getattr(message, "sender_chat", None)
    if sender_chat is not None and sender_chat.id == chat.id:
        # An admin writing as the group itself: Telegram hides who it is.
        return Actor(None, "Anonymous admin", anonymous=True)
    return Actor(message.from_user.id, message.from_user.full_name)


def _status(member: Any) -> str:
    return getattr(member.status, "value", member.status)


RIGHT_TEXT = {
    "can_restrict_members": "ban users",
    "can_invite_users": "invite users",
    "can_change_info": "change the chat info",
    "can_promote_members": "add new admins",
    "can_manage_chat": "manage the chat",
}


async def authorize(
    bot,
    chat,
    user,
    role: Role | None,
    anonymous: bool = False,
    *,
    allow_staff: bool = True,
    staff_min: Role = STAFF_ROLE,
    right: str = "can_restrict_members",
) -> str:
    """
    Check that `user` may moderate in `chat`. Returns "admin" or "staff": the
    capacity they act in. Raises ModerationError otherwise.

    A chat admin (with the right to ban users) acts as an admin, even when they
    also hold a role in the bot. Bot staff who are not admins of the chat act as
    "staff", but only for commands that moderate users (`allow_staff`) and only
    while the chat's owner has not turned their help off (core/chatsettings.py).
    `staff_min` is the lowest role that counts as staff for the command, `right` the
    admin right a chat admin needs for it.

    Anonymous admins can't be checked (Telegram hides who they are), so they are
    refused unless the chat's owner trusts them all with /anonadmin.
    """
    if chat.type not in GROUP_TYPES:
        raise ModerationError("This command only works in groups.")
    if anonymous:
        # Only admins can write as the group, but which admin and with which rights is hidden.
        if await chatsettings.anon_admin(chat.id):
            return "admin"
        raise NotAllowed(
            "I can't check the rights of anonymous admins. Send the command as yourself, "
            "or let the chat owner trust anonymous admins with /anonadmin on."
        )

    def staff_may_help() -> bool:
        return allow_staff and role is not None and role >= staff_min

    try:
        member = await bot.get_chat_member(chat.id, user.id)
    except TelegramAPIError:
        if staff_may_help() and await chatsettings.staff_help(chat.id):
            return "staff"
        raise ModerationError("I couldn't check your rights in this chat.") from None

    status = _status(member)
    if status == "creator":
        return "admin"
    if status == "administrator" and getattr(member, right, False):
        return "admin"
    if staff_may_help() and await chatsettings.staff_help(chat.id):
        return "staff"
    if status == "administrator":
        raise NotAllowed(f"You need the admin right to {RIGHT_TEXT.get(right, right)} to use this command.")
    raise NotAllowed("Only chat admins can use this command.")


async def member_status(bot, chat_id: int, user_id: int) -> str | None:
    """
    The user's status in the chat (creator, administrator, member, restricted,
    left, kicked), or None when Telegram can't tell.
    """
    member = await get_member(bot, chat_id, user_id)
    return None if member is None else _status(member)


async def get_member(bot, chat_id: int, user_id: int) -> Any | None:
    try:
        return await bot.get_chat_member(chat_id, user_id)
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if "user not found" in problem or "participant_id_invalid" in problem:
            return SimpleNamespace(status="left")  # Telegram has never seen them in this chat
        return None
    except TelegramAPIError:
        return None


async def is_chat_admin(bot, chat_id: int, user_id: int) -> bool:
    return await member_status(bot, chat_id, user_id) in ("creator", "administrator")


async def check_target(
    bot, chat, target: Target, actor: Actor, actor_role: Role | None, punitive: bool
) -> None:
    """
    Raise ModerationError if the target must not be touched.

    `punitive` is for commands that restrict someone (ban, mute, warn). They also
    refuse chat admins (admins can't restrict each other) and keep the bot's staff
    from acting against each other. Undoing a punishment is never blocked that way.
    """
    if target.is_chat:
        # Only channels writing into the chat can be banned; never the group itself.
        if target.kind != "channel" or target.user_id == chat.id:
            raise ModerationError("Only channels can be banned that way.")
        return

    if actor.id is not None and actor.id == target.user_id:
        raise ModerationError("You can't do that to yourself.")
    if target.user_id == (await bot.me()).id:
        raise ModerationError("I can't take action against myself.")
    if immunity.is_immune(target.user_id):
        raise ModerationError("I can't take action against that account.")
    if not punitive:
        return

    # Admins of a group decide who stays in it, staff included. But someone with
    # a role in the bot may not act against themselves or another member of the staff.
    if actor_role is not None and await roles.get_role(target.user_id) is not None:
        raise ModerationError("Members of the bot's staff can't take action against each other.")
    if await is_chat_admin(bot, chat.id, target.user_id):
        raise ModerationError("I can't take action against a chat admin.")


async def begin(
    message: Message,
    command: CommandObject,
    role: Role | None,
    *,
    allow_chats: bool = False,
    punitive: bool = True,
) -> tuple[Target, Actor] | None:
    """
    The start of every moderation command: check the person and the target.

    Returns (target, actor), or None after answering why the command can't run.
    """
    bot = message.bot
    actor = actor_of(message)
    try:
        actor.staff = await authorize(bot, message.chat, message.from_user, role, actor.anonymous) == "staff"
        try:
            target = await resolve_target(message, command, allow_bots=True, allow_chats=allow_chats)
        except TargetError as exc:
            raise ModerationError(str(exc)) from None
        await check_target(bot, message.chat, target, actor, role, punitive)
    except ModerationError as exc:
        await deny(message, exc)
        return None
    return target, actor


async def begin_chat_command(
    message: Message,
    role: Role | None,
    *,
    allow_staff: bool = True,
    staff_min: Role = STAFF_ROLE,
    right: str = "can_restrict_members",
) -> Actor | None:
    """
    Like begin(), for commands about the chat itself (no target user).

    Commands that change or reveal the chat's settings pass allow_staff=False:
    the bot's staff helps moderate users, it does not run other people's chats.
    """
    actor = actor_of(message)
    try:
        capacity = await authorize(
            message.bot,
            message.chat,
            message.from_user,
            role,
            actor.anonymous,
            allow_staff=allow_staff,
            staff_min=staff_min,
            right=right,
        )
        actor.staff = capacity == "staff"
    except ModerationError as exc:
        await deny(message, exc)
        return None
    return actor


async def require_chat_owner(message: Message) -> None:
    """Raise ModerationError unless the command was given by the owner of the chat."""
    if message.chat.type not in GROUP_TYPES:
        raise ModerationError("This command only works in groups.")
    if actor_of(message).anonymous:
        raise ModerationError("Send this command as yourself, not as an anonymous admin.")
    status = await member_status(message.bot, message.chat.id, message.from_user.id)
    if status is None:
        raise ModerationError("I couldn't check your rights in this chat.")
    if status != "creator":
        raise NotAllowed("Only the owner of this chat can change this.")


async def require_reply(message: Message) -> Message | None:
    """The message a command replies to; answers with a hint when there is none."""
    found = replied_message(message)
    if found is None:
        await reply(message, "Reply to the message you want to act on.")
    return found


async def perform(call: Awaitable[Any], rights_message: str = RIGHTS_MESSAGE) -> Any:
    """Await a Telegram call, turning the usual refusals into ModerationError."""
    try:
        return await call
    except TelegramForbiddenError:
        raise ModerationError(rights_message) from None
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if any(key in problem for key in ("not enough rights", "need administrator rights", "chat_admin_required", "have no rights")):
            raise ModerationError(rights_message) from None
        if any(key in problem for key in ("can't remove chat owner", "can't restrict self", "user is an administrator", "administrator of the chat")):
            raise ModerationError("I can't take action against a chat admin.") from None
        if any(key in problem for key in ("user not found", "participant_id_invalid", "user_not_participant", "member not found")):
            raise ModerationError("That user isn't in this chat.") from None
        if "supergroup" in problem and "only" in problem:
            raise ModerationError("This only works in supergroups.") from None
        raise


# --- Wording ------------------------------------------------------------------

def who(target: Target) -> str:
    """The target as a link: a user by account, a channel by its public link."""
    if target.is_chat:
        title = esc(target.title or target.name or "Channel")
        if target.username:
            return f'<a href="https://t.me/{esc(target.username)}">{title}</a>'
        return f"<b>{title}</b>"
    return user_link(target.user_id, target.name)


STAFF_MARK = " (Bot Staff)"


def attribution(actor: Actor) -> str:
    """Who did it, shown under every moderation message. Staff are marked as such."""
    return f"\nBy: {actor.link}{STAFF_MARK if actor.staff else ''}"


def by(actor: Actor) -> str:
    """" by Name": who did it, written into the sentence ("King was banned by Jey"). No ids."""
    return f" by {actor.link}{STAFF_MARK if actor.staff else ''}"


def staff_note(actor: Actor) -> str:
    """Nothing any more: staff are marked next to their name (kept so call sites stay simple)."""
    return ""


def admin_ref(actor: Actor) -> str:
    """Who did it, for a log entry: a link, marked when it is the bot's staff."""
    return f"{actor.link}{STAFF_MARK if actor.staff else ''}"


def _user_ref(target: Target) -> str:
    if target.is_chat:
        return who(target)
    return f"{user_link(target.user_id, target.name)} [<code>{target.user_id}</code>]"


async def log_action(
    message: Message,
    tag: str,
    actor: Actor,
    target: Target | None = None,
    reason: str = "",
    *details: tuple[str, str],
) -> None:
    """Copy a moderation action to the chat's log (/setlog), if it has one. Never raises."""
    try:
        await chatlog.record(
            message.bot,
            message.chat,
            tag,
            admin_ref(actor),
            _user_ref(target) if target is not None else None,
            reason,
            details,
        )
    except Exception:
        log.exception("Could not log %s", tag)


def reason_line(reason: str) -> str:
    reason = reason.strip()
    return f"\nReason: {esc(truncate(reason, MAX_REASON_LENGTH))}" if reason else ""


# --- Durations ----------------------------------------------------------------

_DURATION_RE = re.compile(r"^(\d{1,6})([mhdw])$", re.IGNORECASE)
_UNITS = {"m": timedelta(minutes=1), "h": timedelta(hours=1), "d": timedelta(days=1), "w": timedelta(weeks=1)}

MIN_DURATION = timedelta(minutes=1)
MAX_DURATION = timedelta(days=365)  # Telegram treats anything longer as "forever"

DURATION_HELP = "Give a duration like 30m, 2h, 3d or 1w (from 1 minute to 365 days)."


def parse_duration(text: str) -> timedelta | None:
    """30m, 2h, 3d, 1w -> timedelta; None when the text is not a duration."""
    match = _DURATION_RE.match(text.strip())
    if match is None:
        return None
    return int(match.group(1)) * _UNITS[match.group(2).lower()]


def split_duration(extra: str) -> tuple[timedelta, str]:
    """The extra data of a timed command: a duration, then the reason."""
    first, _, rest = extra.strip().partition(" ")
    duration = parse_duration(first) if first else None
    if duration is None or not MIN_DURATION <= duration <= MAX_DURATION:
        raise ModerationError(DURATION_HELP)
    return duration, rest.strip()


def format_duration(duration: timedelta) -> str:
    """90 minutes -> '1 hour 30 minutes'."""
    minutes = int(duration.total_seconds() // 60)
    parts = []
    for name, size in (("day", 1440), ("hour", 60), ("minute", 1)):
        count, minutes = divmod(minutes, size)
        if count:
            parts.append(f"{count} {name}{'' if count == 1 else 's'}")
    return " ".join(parts)
