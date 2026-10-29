"""Group administration"""

from typing import Awaitable, Callable

from aiogram import Router
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.filters import CommandObject
from aiogram.types import LinkPreviewOptions, Message

from core import chatsettings, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.targets import replied_message
from core.text import esc, truncate, user_link

ABOUT = """
Tools for looking after the chat itself.

The settings /adminerror, /anonadmin and /staffhelp belong to the chat owner: any admin can see the current value by sending the command with no argument, but only the owner can change it. My staff never changes a chat's settings.
"""

HELP = [
    ("/link [request|count]", "Get the chat's invite link. /link request makes one where joining needs an admin's approval, /link <count> makes one that stops working after that many uses. Needs the right to invite users.", "admins"),
    ("/gtitle <new title>", "Change the chat's title (up to 128 characters). Needs the right to change chat info.", "admins"),
    ("/adminlist", "List the admins of this chat."),
    ("/pin [loud]", "Reply to a message to pin it. People are not notified unless you add loud (or notify). Needs the right to pin messages.", "admins"),
    ("/unpin", "Reply to a pinned message to unpin it. Without a reply, the most recent pinned message is unpinned. Needs the right to pin messages.", "admins"),
    ("/cleanbluetext [on|off]", "Delete commands after I have dealt with them, so only my answers stay in the chat. Off by default. I need the right to delete messages; commands meant for other bots are left alone. Needs the right to change chat info.", "admins"),
    ("/adminerror [on|off]", "Choose whether I answer people who try admin commands without being admins. On by default; off makes me ignore them silently.", "chat owner"),
    ("/anonadmin [yes|no|on|off]", "Let anonymous admins use every command without their rights being checked, because Telegram hides who they are. Off by default and not recommended: anyone who can post anonymously gets full use of my commands.", "chat owner"),
    ("/staffhelp [on|off|yes|no]", "Let my staff (Sudo and up) help moderate this chat, or not. On by default. They can never change the chat's settings either way.", "chat owner"),
]

router = Router(name="admin")

NO_PREVIEW = LinkPreviewOptions(is_disabled=True)
CLEAN_RIGHT = "can_change_info"  # the admin right needed for /cleanbluetext
MAX_TITLE = 128  # Telegram's limit for a chat title
MAX_LINK_USES = 99999
MAX_ADMINS_SHOWN = 80

_ANSWERS = {"on": True, "yes": True, "off": False, "no": False}


def _state(enabled: bool) -> str:
    return "on" if enabled else "off"


# --- Switches the chat's owner controls -------------------------------------------------

async def _switch(
    message: Message,
    command: CommandObject,
    role: Role | None,
    *,
    read: Callable[[int], Awaitable[bool]],
    write: Callable[[int, bool], Awaitable[None]],
    status_text: Callable[[bool], str],
    changed_text: Callable[[bool], str],
    usage: str,
) -> None:
    """
    A yes/no setting of the chat. Any admin can look at it; only the chat's owner
    can change it. The bot's staff never changes a chat's settings.
    """
    argument = (command.args or "").strip().lower()

    if not argument:
        # can_manage_chat is held by every admin: looking is for any of them.
        if await moderation.begin_chat_command(message, role, allow_staff=False, right="can_manage_chat") is None:
            return
        await reply(message, status_text(await read(message.chat.id)))
        return

    if argument not in _ANSWERS:
        await reply(message, usage)
        return

    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return

    enabled = _ANSWERS[argument]
    await write(message.chat.id, enabled)
    await reply(message, changed_text(enabled))


@router.message(cmd("staffhelp"))
async def cmd_staffhelp(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """
    Whether the bot's staff (Owner, Developers, Sudo) may help moderate this chat.

    On by default. Only the chat's owner can change it; any admin can look.
    When it is off, the staff's commands are treated like those of any other member.
    Either way the staff never changes the chat's settings.
    """

    def changed(enabled: bool) -> str:
        if enabled:
            return (
                "Help from the bot's staff is now <b>on</b>. Developers and Sudo users can help "
                "moderate users here. Their actions are marked as taken by the bot's staff, "
                "and they can't change this chat's settings."
            )
        return (
            "Help from the bot's staff is now <b>off</b>. Their commands are treated like "
            "those of any other member."
        )

    await _switch(
        message, command, role,
        read=chatsettings.staff_help,
        write=chatsettings.set_staff_help,
        status_text=lambda enabled: (
            f"Help from the bot's staff is <b>{_state(enabled)}</b> in this chat.\n"
            "The chat owner can change it with /staffhelp on or /staffhelp off."
        ),
        changed_text=changed,
        usage="Use /staffhelp on or /staffhelp off (yes and no work too).",
    )


@router.message(cmd("adminerror"))
async def cmd_adminerror(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """Whether people who are not admins are told so when they try an admin command. On by default."""
    await _switch(
        message, command, role,
        read=chatsettings.admin_errors,
        write=chatsettings.set_admin_errors,
        status_text=lambda enabled: (
            f"Error replies are <b>{_state(enabled)}</b> in this chat.\n"
            "The chat owner can change it with /adminerror on or /adminerror off."
        ),
        changed_text=lambda enabled: (
            "Error replies are now <b>on</b>: people who are not admins are told so when they try an admin command."
            if enabled
            else "Error replies are now <b>off</b>: admin commands from people who are not admins are ignored silently."
        ),
        usage="Use /adminerror on or /adminerror off.",
    )


@router.message(cmd("anonadmin"))
async def cmd_anonadmin(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """
    Whether anonymous admins may use every command without their rights being
    checked. Telegram hides which admin is behind an anonymous message, so their
    rights cannot be known. Off by default.
    """
    await _switch(
        message, command, role,
        read=chatsettings.anon_admin,
        write=chatsettings.set_anon_admin,
        status_text=lambda enabled: (
            f"Trust in anonymous admins is <b>{_state(enabled)}</b> in this chat.\n"
            "The chat owner can change it with /anonadmin on or /anonadmin off."
        ),
        changed_text=lambda enabled: (
            "Anonymous admins can now use all commands without their permissions being checked. "
            "This is not recommended: anyone who can post anonymously is trusted completely."
            if enabled
            else "Anonymous admins are no longer trusted: they have to send commands as themselves."
        ),
        usage="Use /anonadmin on or /anonadmin off (yes and no work too).",
    )


# --- /link ------------------------------------------------------------------------------------

def _parse_link_options(argument: str) -> tuple[bool, int | None]:
    """'' / 'request' / '<count>' -> (needs approval, use limit)."""
    argument = argument.strip().lower()
    if not argument:
        return False, None
    if argument == "request":
        return True, None
    if argument.isdigit() and 1 <= int(argument) <= MAX_LINK_USES:
        return False, int(argument)
    raise ModerationError(f"Use /link, /link request, or /link &lt;count&gt; with a number from 1 to {MAX_LINK_USES}.")


@router.message(cmd("link"))
async def cmd_link(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, allow_staff=False, right="can_invite_users")
    if actor is None:
        return
    bot = message.bot
    chat = message.chat

    try:
        needs_approval, limit = _parse_link_options(command.args or "")
        me = await moderation.get_member(bot, chat.id, (await bot.me()).id)
        if me is None or not (getattr(me, "status", None) == "creator" or getattr(me, "can_invite_users", False) is True):
            raise ModerationError("I need the admin right to invite users to do that.")
        rights = "I don't have the right to create invite links. Make me an admin with permission to invite users."

        if not needs_approval and limit is None:
            # The chat's usual link: public chats have one; otherwise use the existing
            # primary link, and only make one when there is none.
            if chat.username:
                link = f"https://t.me/{chat.username}"
            else:
                full = await perform(bot.get_chat(chat.id), rights)
                link = getattr(full, "invite_link", None) or await perform(bot.export_chat_invite_link(chat.id), rights)
            await reply(message, f"Invite link: {esc(link)}", link_preview_options=NO_PREVIEW)
            return

        created = await perform(
            bot.create_chat_invite_link(
                chat.id,
                name=truncate(f"By {actor.name}", 32),
                creates_join_request=needs_approval or None,
                member_limit=limit,
            ),
            rights,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return

    if needs_approval:
        kind = "Invite link that requires approval to join"
    else:
        kind = f"Invite link for {limit} {'person' if limit == 1 else 'people'}"
    await reply(
        message,
        f"{kind}: {esc(created.invite_link)}" + moderation.attribution(actor),
        link_preview_options=NO_PREVIEW,
    )


# --- /gtitle -----------------------------------------------------------------------------------

@router.message(cmd("gtitle"))
async def cmd_gtitle(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, allow_staff=False, right="can_change_info")
    if actor is None:
        return
    title = (command.args or "").strip()
    try:
        if not title:
            raise ModerationError("Give the new title, for example: /gtitle My Chat")
        if len(title) > MAX_TITLE:
            raise ModerationError(f"A chat title can have at most {MAX_TITLE} characters.")
        me = await moderation.get_member(message.bot, message.chat.id, (await message.bot.me()).id)
        if me is None or not (getattr(me, "status", None) == "creator" or getattr(me, "can_change_info", False) is True):
            raise ModerationError("I need the admin right to change the chat info to do that.")
        await perform(
            message.bot.set_chat_title(message.chat.id, title),
            "I can't change the title. Make me an admin with permission to change the chat info.",
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    await reply(message, f"The chat title is now <b>{esc(title)}</b>." + moderation.attribution(actor))


# --- /adminlist ---------------------------------------------------------------------------------

@router.message(cmd("adminlist"))
async def cmd_adminlist(message: Message) -> None:
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    try:
        admins = await message.bot.get_chat_administrators(message.chat.id)
    except TelegramAPIError:
        await reply(message, "I couldn't read the list of admins of this chat.")
        return

    def line(admin) -> str:
        text = f"• {user_link(admin.user.id, admin.user.full_name)}"
        if admin.user.is_bot:
            text += " (bot)"
        if getattr(admin, "is_anonymous", False):
            text += " (anonymous)"
        if getattr(admin, "custom_title", None):
            text += f" — {esc(admin.custom_title)}"
        return text

    owners = [a for a in admins if getattr(a.status, "value", a.status) == "creator"]
    others = [a for a in admins if a not in owners]
    lines = [f"<b>Admins in {esc(message.chat.title)}:</b>"]
    if owners:
        lines += ["", "<b>Owner</b>", *[line(a) for a in owners]]
    if others:
        lines += ["", "<b>Admins</b>", *[line(a) for a in others[:MAX_ADMINS_SHOWN]]]
        if len(others) > MAX_ADMINS_SHOWN:
            lines.append(f"…and {len(others) - MAX_ADMINS_SHOWN} more.")
    await reply(message, "\n".join(lines), link_preview_options=NO_PREVIEW)


# --- /cleanbluetext: the deleting itself is CleanCommandsMiddleware (core/middlewares.py) ---

async def _bot_can_delete(message: Message) -> bool:
    try:
        me = await message.bot.get_chat_member(message.chat.id, message.bot.id)
    except TelegramAPIError:
        return False
    return getattr(me, "status", None) == "creator" or bool(getattr(me, "can_delete_messages", False))


@router.message(cmd("cleanbluetext"))
async def cmd_cleanbluetext(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if await moderation.begin_chat_command(message, role, allow_staff=False, right=CLEAN_RIGHT) is None:
        return
    usage = "Use /cleanbluetext on or /cleanbluetext off."
    argument = (command.args or "").strip().lower()
    if not argument:
        current = await chatsettings.clean_commands(message.chat.id)
        await reply(message, f"Commands are {'<b>deleted</b> after they run' if current else '<b>kept</b>'}.\n{usage}")
        return
    if argument not in _ANSWERS:
        await reply(message, usage)
        return
    enabled = _ANSWERS[argument]
    await chatsettings.set_clean_commands(message.chat.id, enabled)
    if not enabled:
        await reply(message, "Commands will be <b>kept</b>.")
        return
    text = "Commands will be <b>deleted</b> after they run."
    if not await _bot_can_delete(message):
        text += "\nI can't delete messages here yet. Give me that admin right."
    await reply(message, text)


# --- /pin and /unpin --------------------------------------------------------------------------------

PIN_RIGHT = "can_pin_messages"
PIN_REFUSED = "I can't pin messages here. Make me an admin with permission to pin messages."
_LOUD = ("loud", "notify", "violent")


@router.message(cmd("pin"))
async def cmd_pin(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, allow_staff=False, right=PIN_RIGHT)
    if actor is None:
        return
    argument = (command.args or "").strip().lower()
    if argument and argument not in _LOUD:
        await reply(message, "Use /pin as a reply to a message. Add loud to notify everyone: /pin loud.")
        return
    target = await moderation.require_reply(message)
    if target is None:
        return
    try:
        await perform(
            message.bot.pin_chat_message(message.chat.id, target.message_id, disable_notification=not argument),
            PIN_REFUSED,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        await reply(message, esc(_pin_problem(exc, "pin")))
        return
    await reply(
        message,
        "Pinned the message" + (" and notified everyone." if argument else ".") + moderation.attribution(actor),
    )


@router.message(cmd("unpin"))
async def cmd_unpin(message: Message, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, allow_staff=False, right=PIN_RIGHT)
    if actor is None:
        return
    target = replied_message(message)
    try:
        await perform(
            message.bot.unpin_chat_message(message.chat.id, message_id=target.message_id if target else None),
            PIN_REFUSED,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        await reply(message, esc(_pin_problem(exc, "unpin")))
        return
    what = "the message" if target else "the most recent pinned message"
    await reply(message, f"Unpinned {what}." + moderation.attribution(actor))


def _pin_problem(exc: Exception, action: str) -> str:
    """A sentence for the failures Telegram reports as plain bad requests."""
    problem = str(exc).lower()
    if "not found" in problem or "message_id_invalid" in problem:
        return "That message isn't pinned." if action == "unpin" else "I can't find that message to pin."
    if "chat_not_modified" in problem or "not modified" in problem:
        return "That message is already pinned."
    return f"I couldn't {action} that message."
