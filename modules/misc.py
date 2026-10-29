"""Everyday commands"""

import inspect

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.filters import CommandObject
from aiogram.types import (
    CallbackQuery,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
)

from config import settings
from core import help_index, info_hooks, registry, roles, users
from core.filters import cmd
from core.helpers import edit, reply
from core.roles import Role
from core.targets import (
    Target,
    TargetError,
    replied_message,
    resolve_target,
    sender_as_target,
)
from core.text import esc

# Commands shown in /help (see core/help_index.py).
ABOUT = """
The basics: get to know me, find your way around, and look people up.

/id and /info accept a user, a bot, a group or a channel. Reply to one of their messages, or give a @username or a numeric ID. Without a target they describe you.
"""

HELP = [
    ("/start", "Say hello. In a group I only point you to a private chat, where I can show you what I do."),
    ("/help", "Open this panel. In a group you get a button that opens it in a private chat."),
    ("/id [user]", "Show your Telegram ID, plus the chat's ID when you use it in a group. Give a user, group or channel to see their ID instead."),
    ("/info [user]", "Show what I know about a user, bot, group or channel: name, username, ID, and in a group also their status here (member, admin, banned, muted) and flags from other modules such as Approved or GBanned."),
]

router = Router(name="misc")

CB_START = "start:home"
CB_START_LEGACY = "root:start"  # buttons on messages sent before start moved here
CB_HELP = "help:home"
CB_HELP_MODULE = "help:module:"

NO_PREVIEW = LinkPreviewOptions(is_disabled=True)


# --- Texts and keyboards ----------------------------------------------------

def _display_name(message: Message) -> str:
    return message.from_user.full_name if message.from_user else "there"


def _start_text(name: str) -> str:
    return (
        f"Hello, <b>{esc(name)}</b>! I'm <b>Argo</b>.\n\n"
        "I'm a group administration bot: moderation, role management and general "
        "crowd control, delivered with a questionable amount of sass.\n\n"
        "Add me to your group, give me admin rights and I'll keep things functional. "
        "Tap <b>Help</b> below to see what I can do."
    )


def _group_start_text(name: str, username: str) -> str:
    return (
        f"Hello, <b>{esc(name)}</b>! I'm <b>Argo</b>.\n\n"
        "I'd rather not discuss the details in front of everyone. "
        f"Message me in private to see what I can do: @{esc(username)}"
    )


def _help_home_text(role: Role | None = None) -> str:
    prefixes = " ".join(f"<code>{esc(p)}</code>" for p in settings.COMMAND_PREFIXES)
    lines = ["<b>Help</b>", "", "Pick a category to see what it does and how to use its commands.", ""]
    for info in help_index.categories(role):
        lines.append(f"• <b>{esc(_help_title(info))}</b> - {esc(info.description)}" if info.description else f"• <b>{esc(_help_title(info))}</b>")
    lines += ["", f"Commands can start with: {prefixes}"]
    return "\n".join(lines)


def _help_title(info: registry.ModuleInfo) -> str:
    """Name of a module in /help: its HELP_TITLE if it has one (AFK), else the capitalized name."""
    return getattr(info.module, "HELP_TITLE", None) or info.name.capitalize()


def _paragraphs(text: str) -> str:
    """Plain text from a module (ABOUT, notes) made safe for HTML; blank lines stay."""
    return "\n".join(esc(line.rstrip()) for line in inspect.cleandoc(text).splitlines())


def _help_category_text(info: registry.ModuleInfo, role: Role | None) -> str:
    """The page of one module: what it is, then every command it offers to this person."""
    lines = [f"<b>{esc(_help_title(info))}</b>"]
    if info.description:
        lines.append(f"<i>{esc(info.description)}</i>")
    about = getattr(info.module, "ABOUT", None)
    if about:
        lines += ["", _paragraphs(about)]

    lines += ["", "<b>Commands</b>"]
    for usage, description, who in help_index.detailed(info.module, role):
        head = f"<code>{esc(usage)}</code>"
        if who:
            head += f"  <i>({esc(who)})</i>"
        lines += ["", head, esc(description)]

    for title, text in getattr(info.module, "HELP_NOTES", ()):
        lines += ["", f"<b>{esc(title)}</b>", _paragraphs(text)]
    return "\n".join(lines)


def _start_keyboard() -> InlineKeyboardMarkup:
    row = []
    if settings.GITHUB_URL:
        row.append(InlineKeyboardButton(text="GitHub", url=settings.GITHUB_URL))
    row.append(InlineKeyboardButton(text="Help", callback_data=CB_HELP))
    return InlineKeyboardMarkup(inline_keyboard=[row])


def _help_home_keyboard(role: Role | None) -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=_help_title(info), callback_data=f"{CB_HELP_MODULE}{info.name}")
        for info in help_index.categories(role)
    ]
    rows = [buttons[i : i + 2] for i in range(0, len(buttons), 2)]
    rows.append([InlineKeyboardButton(text="Back", callback_data=CB_START)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def _help_category_keyboard() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text="Back", callback_data=CB_HELP)]]
    )


# --- /start -----------------------------------------------------------------

@router.message(cmd("start"))
async def cmd_start(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type != "private":
        # Groups: greet and point to the private chat. No buttons.
        me = await message.bot.me()
        await reply(message, _group_start_text(_display_name(message), me.username))
        return

    # Deep link from the group's /help button: t.me/<bot>?start=help
    if (command.args or "").strip().lower() == "help":
        await reply(message, _help_home_text(role), reply_markup=_help_home_keyboard(role))
        return

    await reply(message, _start_text(_display_name(message)), reply_markup=_start_keyboard())


@router.callback_query(F.data.in_({CB_START, CB_START_LEGACY}))
async def cb_start(callback: CallbackQuery) -> None:
    await callback.answer()  # always answer first, otherwise the button keeps spinning
    if isinstance(callback.message, Message):
        await edit(
            callback.message,
            _start_text(callback.from_user.full_name),
            reply_markup=_start_keyboard(),
        )


# --- /help ------------------------------------------------------------------

@router.message(cmd("help"))
async def cmd_help(message: Message, role: Role | None = None) -> None:
    if message.chat.type != "private":
        # Groups: a button that opens the help panel in the private chat.
        me = await message.bot.me()
        keyboard = InlineKeyboardMarkup(
            inline_keyboard=[
                [InlineKeyboardButton(text="Help", url=f"https://t.me/{me.username}?start=help")]
            ]
        )
        await reply(
            message,
            "Click the button below to see the available commands.",
            reply_markup=keyboard,
        )
        return

    await reply(message, _help_home_text(role), reply_markup=_help_home_keyboard(role))


@router.callback_query(F.data == CB_HELP)
async def cb_help_home(callback: CallbackQuery, role: Role | None = None) -> None:
    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(callback.message, _help_home_text(role), reply_markup=_help_home_keyboard(role))


@router.callback_query(F.data.startswith(CB_HELP_MODULE))
async def cb_help_category(callback: CallbackQuery, role: Role | None = None) -> None:
    name = (callback.data or "")[len(CB_HELP_MODULE):]
    info = registry.get(name)

    # Buttons on old messages can point to a module that is disabled now.
    if info is None or not info.enabled or not help_index.entries(info.module, role):
        await callback.answer("This category is not available.", show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(
            callback.message,
            _help_category_text(info, role),
            reply_markup=_help_category_keyboard(),
        )


# --- /id and /info ----------------------------------------------------------
#
# Both take an optional target: reply to a message, or give a @username or id.
# A target can be a user, a bot, a group or a channel. Without one they describe
# whoever sent the command (a premium user writing as a channel is described as
# that channel).

KIND_LABELS = {
    "user": "User",
    "bot": "Bot",
    "group": "Group",
    "supergroup": "Supergroup",
    "channel": "Channel",
}


def _wants_target(message: Message, command: CommandObject) -> bool:
    return bool((command.args or "").strip()) or replied_message(message) is not None


async def _subject(message: Message, command: CommandObject) -> tuple[Target, bool]:
    """(who the command is about, whether they were named explicitly)."""
    if _wants_target(message, command):
        target = await resolve_target(message, command, allow_bots=True, allow_chats=True)
        return target, True
    return sender_as_target(message), False


def _line(label: str, value: str) -> str:
    return f"<b>{label}:</b> {value}"


@router.message(cmd("id"))
async def cmd_id(message: Message, command: CommandObject) -> None:
    try:
        target, explicit = await _subject(message, command)
    except TargetError as exc:
        await reply(message, esc(str(exc)))
        return

    kind = KIND_LABELS[target.kind]
    if explicit:
        label = f"{kind} ID"
    else:
        label = "Your ID" if not target.is_chat else f"{kind} ID"
    lines = [_line(label, f"<code>{target.user_id}</code>")]

    # Handy for filling in log chat settings.
    if not explicit and message.chat.type != "private":
        lines.append(_line("Chat ID", f"<code>{message.chat.id}</code>"))
        if message.is_topic_message and message.message_thread_id:
            lines.append(_line("Topic ID", f"<code>{message.message_thread_id}</code>"))

    await reply(message, "\n".join(lines))


async def _chat_member(bot, chat_id: int, user_id: int):
    """The user's membership in a chat; the string "gone" if Telegram has never seen them; None if unknown."""
    try:
        return await bot.get_chat_member(chat_id, user_id)
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if "user not found" in problem or "participant_id_invalid" in problem:
            return "gone"
        return None  # e.g. "member list is inaccessible": better to say nothing
    except TelegramAPIError:
        return None


def _chat_status(member) -> str:
    """The user's standing in a chat, from their membership."""
    if member == "gone":
        return "Not in chat"
    status = getattr(member.status, "value", member.status)
    if status == "creator":
        return "Owner"
    if status == "administrator":
        return "Admin"
    if status == "member":
        return "Member"
    if status == "restricted":
        return "Member" if getattr(member, "is_member", True) else "Not in chat"
    return "Not in chat"  # left or kicked


def _restriction_lines(member) -> list[str]:
    """Banned / Muted flags, shown only when they apply."""
    if member == "gone":
        return []
    status = getattr(member.status, "value", member.status)
    if status == "kicked":
        return [_line("Banned", "<code>Yes</code>")]
    if status == "restricted" and getattr(member, "is_member", True) and not getattr(member, "can_send_messages", True):
        return [_line("Muted", "<code>Yes</code>")]
    return []


async def _refresh_user(bot, target: Target) -> None:
    """Fill in the name parts for people stored before names were kept in two parts."""
    if target.first_name is not None:
        return
    try:
        chat = await bot.get_chat(target.user_id)
    except TelegramAPIError:
        # Not reachable: all we have is the stored full name.
        target.first_name = target.name
        return
    target.first_name = chat.first_name or chat.full_name
    target.last_name = chat.last_name
    target.username = chat.username or target.username
    try:
        await users.remember(chat)
    except Exception:
        pass


async def _user_info(bot, chat, target: Target) -> list[str]:
    await _refresh_user(bot, target)

    lines = [_line("ID", f"<code>{target.user_id}</code>")]
    if target.first_name:
        lines.append(_line("First Name", esc(target.first_name)))
    if target.last_name:
        lines.append(_line("Last Name", esc(target.last_name)))
    if target.username:
        lines.append(_line("Username", f"@{esc(target.username)}"))
    lines.append(_line("User link", f'<a href="tg://user?id={target.user_id}">link</a>'))

    if chat.type in ("group", "supergroup"):
        member = await _chat_member(bot, chat.id, target.user_id)
        if member is not None:
            lines.append(_line("Status", f"<code>{_chat_status(member)}</code>"))
            lines.extend(_restriction_lines(member))

    role = await roles.get_role(target.user_id)
    if role is not None:
        lines.append(_line("Bot role", f"<code>{roles.label(role)}</code>"))

    lines.extend(await info_hooks.lines_for(bot, target.user_id, chat.id if chat.type in ("group", "supergroup") else None))
    return lines


async def _chat_info(bot, target: Target) -> list[str]:
    lines = [_line("ID", f"<code>{target.user_id}</code>")]
    if target.title:
        lines.append(_line("Title", esc(target.title)))
    lines.append(_line("Type", f"<code>{KIND_LABELS[target.kind]}</code>"))
    if target.username:
        lines.append(_line("Username", f"@{esc(target.username)}"))
        lines.append(_line("Link", f'<a href="https://t.me/{esc(target.username)}">link</a>'))
    try:
        members = await bot.get_chat_member_count(target.user_id)
    except TelegramAPIError:
        members = None
    if members is not None:
        lines.append(_line("Members", f"<code>{members}</code>"))
    return lines


@router.message(cmd("info"))
async def cmd_info(message: Message, command: CommandObject) -> None:
    try:
        target, _ = await _subject(message, command)
    except TargetError as exc:
        await reply(message, esc(str(exc)))
        return

    if target.is_chat:
        lines = await _chat_info(message.bot, target)
    else:
        lines = await _user_info(message.bot, message.chat, target)
    await reply(message, "\n".join(lines), link_preview_options=NO_PREVIEW)
