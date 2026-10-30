"""Everyday commands"""

import inspect
import time

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
from core import help_index, info_hooks, lookup, registry, roles, users, web
from core.filters import cmd
from core.helpers import edit, reply
from core.roles import Role
from core.targets import (
    Target,
    TargetError,
    UnknownUserError,
    replied_message,
    resolve_target,
    sender_as_target,
)
from core.text import esc

# Commands shown in /help (see core/help_index.py).
ABOUT = """
The basics: get to know me, find your way around, and look people up.

/id and /info accept a user, a bot, a group or a channel. Reply to one of their messages, or give a @username or a numeric ID. Without a target they describe you.

/tr, /dict, /time, /weather and /cash look things up on free public services, so they can be slow or unavailable now and then. Each person can use them once every few seconds.
"""

HELP = [
    ("/start", "Say hello. In a group I only point you to a private chat, where I can show you what I do."),
    ("/help [topic]", "Open this panel, or go straight to a page: /help notes, or the name of any command. In a group you get a button that opens it in a private chat."),
    ("/id [user]", "Show your Telegram ID, plus the chat's ID when you use it in a group. Give a user, group or channel to see their ID instead."),
    ("/info [user]", "Show what I know about a user, bot, group or channel. Users get a User Info section (name, username, ID and, in a group, their status: Owner, Admin, Member, Muted or Banned, with Approved added, or Muted (No Captcha) while a captcha is unsolved) and an Argo Info section (bot role and Yes/No flags like Globally Banned, FedBanned, Whitelisted, Immune and AFK). People with a role show no flags. If I have no data on someone, I say so: they have to message me first, or I have to see them in a chat I am in."),
    ("/tr [language] <text>", "Translate text into a language (default: English). Give a language code like pl, de or es first, or reply to a message to translate it."),
    ("/dict <word>", "Look up an English word: how it is pronounced and what it means."),
    ("/time <place or timezone>", "Show the current time and date in a city or a timezone, like Tokyo or Europe/Warsaw."),
    ("/weather <place>", "Show the current weather in a city."),
    ("/cash <amount> <from> <to>", "Convert money between currencies, like /cash 100 usd pln."),
]

HELP_NOTES = [
    (
        "Examples",
        """
        /tr pl good morning - translate into Polish.
        /tr - reply to a message to get it in English.
        /tr en no way - a language code at the start always counts as the target, so to translate a text that starts with one (no, it, is, hi...) name the target first.
        /time Tokyo - the time in Tokyo.
        /weather Warsaw, Poland - the weather in Warsaw.
        /cash 100 usd pln - one hundred US dollars in zloty.
        """,
    ),
]

router = Router(name="misc")

CB_START = "start:home"
CB_START_LEGACY = "root:start"  # buttons on messages sent before start moved here
CB_HELP = "help:home"
CB_HELP_MODULE = "help:module:"
CB_HELP_SECTION = "help:section:"
CB_HELP_PAGE = "help:page:"

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
    lines = ["<b>Help</b>", "", "Pick a category to see what it does and how to use its commands.", "Settings that take on or off also understand yes and no.", ""]
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


def _help_category_text(info: registry.ModuleInfo, role: Role | None, section: int | None = None) -> str:
    """
    The page of one module: what it is, then every command it offers to this person. A module
    with sections shows the commands that belong to none here, and a page per section.
    """
    names = help_index.sections(info.module)
    chosen = names[section] if section is not None and 0 <= section < len(names) else None
    lines = [f"<b>{esc(_help_title(info))}</b>" if chosen is None else f"<b>{esc(_help_title(info))} - {esc(chosen)}</b>"]
    if chosen is None:
        if info.description:
            lines.append(f"<i>{esc(info.description)}</i>")
        about = getattr(info.module, "ABOUT", None)
        if about:
            lines += ["", _paragraphs(about)]

    commands = help_index.detailed(info.module, role, chosen)
    if commands:
        lines += ["", "<b>Commands</b>"]
    for usage, description, who in commands:
        head = f"<code>{esc(usage)}</code>"
        if who:
            head += f"  <i>({esc(who)})</i>"
        lines += ["", head, esc(description)]
    if chosen is None and help_index.visible_sections(info.module, role):
        lines += ["", "Pick a section below for the commands."]

    for title, text, *where in getattr(info.module, "HELP_NOTES", ()):
        if (str(where[0]) if where else None) == chosen:
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
    # Three to a row. A last row of two or one is stretched over the whole width by Telegram.
    rows = [buttons[i : i + 3] for i in range(0, len(buttons), 3)]
    rows.append([InlineKeyboardButton(text="Back", callback_data=CB_START)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def _help_category_keyboard(info: registry.ModuleInfo | None = None, role: Role | None = None, section: int | None = None) -> InlineKeyboardMarkup:
    """Back to the list of categories, or to the module's page from one of its sections."""
    if info is not None and section is not None:
        return InlineKeyboardMarkup(
            inline_keyboard=[[InlineKeyboardButton(text="Back", callback_data=f"{CB_HELP_MODULE}{info.name}")]]
        )
    rows = []
    if info is not None:
        rows = [
            [InlineKeyboardButton(text=name, callback_data=f"{CB_HELP_SECTION}{info.name}:{number}")]
            for number, name in help_index.visible_sections(info.module, role)
        ]
        rows += [
            [InlineKeyboardButton(text=label, callback_data=f"{CB_HELP_PAGE}{info.name}:{number}:0")]
            for number, label in help_index.pages(info.module)
        ]
    rows.append([InlineKeyboardButton(text="Back", callback_data=CB_HELP)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def _help_page_keyboard(info: registry.ModuleInfo, number: int, part: int, total: int) -> InlineKeyboardMarkup:
    rows = []
    if total > 1:
        move = []
        if part > 0:
            move.append(InlineKeyboardButton(text="Previous", callback_data=f"{CB_HELP_PAGE}{info.name}:{number}:{part - 1}"))
        if part < total - 1:
            move.append(InlineKeyboardButton(text="Next", callback_data=f"{CB_HELP_PAGE}{info.name}:{number}:{part + 1}"))
        rows.append(move)
    rows.append([InlineKeyboardButton(text="Back", callback_data=f"{CB_HELP_MODULE}{info.name}")])
    return InlineKeyboardMarkup(inline_keyboard=rows)


# --- /start -----------------------------------------------------------------

@router.message(cmd("start"))
async def cmd_start(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type != "private":
        # Groups: greet and point to the private chat. No buttons.
        me = await message.bot.me()
        await reply(message, _group_start_text(_display_name(message), me.username))
        return

    # Deep link from the group's /help button: t.me/<bot>?start=help
    payload = (command.args or "").strip().lower()
    if payload == "help":
        await reply(message, _help_home_text(role), reply_markup=_help_home_keyboard(role))
        return
    if payload.startswith("connect_"):
        # t.me/<bot>?start=connect_<chat id>: the button /connect sends in a group
        from modules.connection import connect_and_tell

        chat_id = payload[len("connect_"):]
        if chat_id.lstrip("-").isdigit():
            await connect_and_tell(message, int(chat_id))
            return
    if payload.startswith("cap_") and payload[4:].lstrip("-").isdigit():
        # t.me/<bot>?start=cap_<id>: the captcha button of a welcome message
        from core import captchas

        problem = await captchas.open_in_private(message.bot, message.from_user, int(payload[4:]), message.chat.id)
        if problem:
            await reply(message, problem)
        return
    if payload.startswith("help_"):
        info = registry.get(payload[len("help_"):])
        if info is not None and info.enabled and help_index.entries(info.module, role):
            await reply(message, _help_category_text(info, role), reply_markup=_help_category_keyboard(info, role))
            return
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

def _help_topic(topic: str, role: Role | None) -> registry.ModuleInfo | None:
    """
    The help page a typed word stands for: a category by its name or title, else the category
    that has a command of that name (/help save opens the notes).
    """
    word = " ".join(topic.lower().lstrip("/!").split())
    if not word:
        return None
    found = help_index.categories(role)
    for info in found:
        if word in (info.name.lower(), _help_title(info).lower()):
            return info
    starts = [info for info in found if info.name.lower().startswith(word) or _help_title(info).lower().startswith(word)]
    if len(starts) == 1:
        return starts[0]
    command = word.split()[0]
    for info in found:
        for usage, _description in help_index.entries(info.module, role):
            names = {part.lstrip("/!").split("[")[0].split("<")[0].lower() for part in usage.split()[:1]}
            if command in names:
                return info
    return None


@router.message(cmd("help"))
async def cmd_help(message: Message, command: CommandObject, role: Role | None = None) -> None:
    topic = (command.args or "").strip()
    info = _help_topic(topic, role) if topic else None
    if topic and info is None:
        await reply(message, f"I have no help page for <b>{esc(topic[:40])}</b>. Use /help to see the categories.")
        return

    if message.chat.type != "private":
        # Groups: a button that opens the help panel (or the asked-for page) in the private chat.
        me = await message.bot.me()
        suffix = f"help_{info.name}" if info is not None else "help"
        keyboard = InlineKeyboardMarkup(
            inline_keyboard=[
                [InlineKeyboardButton(text="Help", url=f"https://t.me/{me.username}?start={suffix}")]
            ]
        )
        await reply(
            message,
            "Click the button below to see the available commands.",
            reply_markup=keyboard,
        )
        return

    if info is not None:
        await reply(message, _help_category_text(info, role), reply_markup=_help_category_keyboard(info, role))
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
            reply_markup=_help_category_keyboard(info, role),
        )


@router.callback_query(F.data.startswith(CB_HELP_SECTION))
async def cb_help_section(callback: CallbackQuery, role: Role | None = None) -> None:
    name, _, number = (callback.data or "")[len(CB_HELP_SECTION):].rpartition(":")
    info = registry.get(name)
    shown = dict(help_index.visible_sections(info.module, role)) if info is not None and info.enabled else {}
    if not number.isdigit() or int(number) not in shown:
        await callback.answer("This section is not available.", show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(
            callback.message,
            _help_category_text(info, role, int(number)),
            reply_markup=_help_category_keyboard(info, role, int(number)),
        )


@router.callback_query(F.data.startswith(CB_HELP_PAGE))
async def cb_help_page(callback: CallbackQuery, role: Role | None = None) -> None:
    name, _, rest = (callback.data or "")[len(CB_HELP_PAGE):].partition(":")
    number, _, part = rest.partition(":")
    info = registry.get(name)
    texts = None
    if info is not None and info.enabled and help_index.entries(info.module, role) and number.isdigit() and part.isdigit():
        texts = help_index.page_texts(info.module, int(number))
    if not texts or int(part) >= len(texts):
        await callback.answer("This page is not available.", show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(
            callback.message,
            texts[int(part)],
            reply_markup=_help_page_keyboard(info, int(number), int(part), len(texts)),
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


UNKNOWN_USER = (
    "I don't know this user. They have to message me first, "
    "or I have to see them in a chat I'm in."
)


def _line(label: str, value: str) -> str:
    return f"<b>{label}:</b> {value}"


def _item(label: str, value: str) -> str:
    """One bulleted 'Label: value' line, as used in /info."""
    return f"• {_line(label, value)}"


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


# The order of the yes/no flags of the bot's own section in /info. Flags from modules this list
# doesn't know come last.
FLAG_ORDER = ("Globally Banned", "FedBanned", "Whitelisted", "Immune", "AFK", "FedAdmin")


def _standing(member, approved: bool, captcha: bool = False) -> str | None:
    """
    The "Status" of a person in a chat: Owner, Admin, Banned, Muted, Member (Approved) ...
    None when Telegram won't say.
    """
    if member is None:
        return None
    text = _chat_status(member)
    if member != "gone":
        status = getattr(member.status, "value", member.status)
        if status == "kicked":
            return "Banned"
        if status == "restricted" and getattr(member, "is_member", True) and not getattr(member, "can_send_messages", True):
            text = "Muted"
    if approved and text in ("Member", "Muted"):
        text += " (Approved)"
    elif captcha and text == "Muted":
        text = "Muted (No Captcha)"  # muted until the captcha is solved (modules/captcha.py)
    return text


def _flag_lines(flags: list[tuple[str, bool]]) -> list[str]:
    def rank(flag: tuple[str, bool]) -> int:
        return FLAG_ORDER.index(flag[0]) if flag[0] in FLAG_ORDER else len(FLAG_ORDER)

    return [_item(name, f"<code>{'Yes' if value else 'No'}</code>") for name, value in sorted(flags, key=rank)]


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

    lines = ["<b>User Info</b>", _item("ID", f"<code>{target.user_id}</code>")]
    if target.first_name:
        lines.append(_item("First Name", esc(target.first_name)))
    if target.last_name:
        lines.append(_item("Last Name", esc(target.last_name)))
    if target.username:
        lines.append(_item("Username", f"@{esc(target.username)}"))
    lines.append(_item("User link", f'<a href="tg://user?id={target.user_id}">link</a>'))

    in_group = chat.type in ("group", "supergroup")
    chat_id = chat.id if in_group else None
    member = await _chat_member(bot, chat.id, target.user_id) if in_group else None
    role = await roles.get_role(target.user_id)

    # A role settles it: admins and the bot's staff have no flags to list.
    has_role = role is not None or (member not in (None, "gone") and _chat_status(member) in ("Owner", "Admin"))
    flags: list[tuple[str, bool]] = []
    approved = captcha = False
    if not has_role:
        found = await info_hooks.flags_for(bot, target.user_id, chat_id)
        approved = any(name == "Approved" and value for name, value in found)  # shown in the Status
        captcha = any(name == "Captcha Pending" and value for name, value in found)  # shown in the Status too
        flags = [flag for flag in found if flag[0] not in ("Approved", "Captcha Pending")]
    # Facts that hold for admins and staff as well.
    flags += await info_hooks.flags_for(bot, target.user_id, chat_id, always=True)

    standing = _standing(member, approved, captcha)
    if standing is not None:
        lines.append(_item("Status", f"<code>{standing}</code>"))

    about = []
    if role is not None:
        about.append(_item("Bot Role", f"<code>{roles.label(role)}</code>"))
    about.extend(_flag_lines(flags))
    if about:
        lines += ["", "<b>Argo Info</b>"] + about
    return lines


async def _chat_info(bot, target: Target) -> list[str]:
    lines = ["<b>Chat Info</b>", _item("ID", f"<code>{target.user_id}</code>")]
    if target.title:
        lines.append(_item("Title", esc(target.title)))
    lines.append(_item("Type", f"<code>{KIND_LABELS[target.kind]}</code>"))
    if target.username:
        lines.append(_item("Username", f"@{esc(target.username)}"))
        lines.append(_item("Link", f'<a href="https://t.me/{esc(target.username)}">link</a>'))
    try:
        members = await bot.get_chat_member_count(target.user_id)
    except TelegramAPIError:
        members = None
    if members is not None:
        lines.append(_item("Members", f"<code>{members}</code>"))
    return lines


@router.message(cmd("info"))
async def cmd_info(message: Message, command: CommandObject) -> None:
    try:
        target, _ = await _subject(message, command)
    except UnknownUserError:
        await reply(message, UNKNOWN_USER)
        return
    except TargetError as exc:
        await reply(message, esc(str(exc)))
        return

    if not target.is_known:
        await reply(message, UNKNOWN_USER)
        return

    if target.is_chat:
        lines = await _chat_info(message.bot, target)
    else:
        lines = await _user_info(message.bot, message.chat, target)
    await reply(message, "\n".join(lines), link_preview_options=NO_PREVIEW)


# --- Lookups: /tr, /dict, /time, /weather, /cash -------------------------------------

LOOKUP_COOLDOWN = 3.0  # seconds between lookups of one person, to be kind to the free services
_last_lookup: dict[int, float] = {}


def _too_fast(message: Message) -> bool:
    user = message.from_user
    if user is None:
        return False
    now = time.monotonic()
    if now - _last_lookup.get(user.id, 0.0) < LOOKUP_COOLDOWN:
        return True
    if len(_last_lookup) >= 5000:
        _last_lookup.clear()
    _last_lookup[user.id] = now
    return False


async def _lookup(message: Message, work) -> None:
    """Run one lookup (a coroutine function) and send its text, or the reason it failed."""
    if _too_fast(message):
        await reply(message, "Slow down a little and try again in a few seconds.")
        return
    try:
        text = await work()
    except web.WebError as exc:
        text = str(exc)  # always a ready-made sentence, already safe for HTML
    await reply(message, text, link_preview_options=NO_PREVIEW)


@router.message(cmd("tr"))
async def cmd_tr(message: Message, command: CommandObject) -> None:
    words = (command.args or "").split(maxsplit=1)
    target = "en"
    rest = command.args or ""
    if words and lookup.is_language(words[0]):
        target = words[0].lower()
        rest = words[1] if len(words) > 1 else ""
    text = rest.strip()
    if not text:
        replied = replied_message(message)
        text = ((replied.text or replied.caption or "") if replied else "").strip()
    if not text:
        await reply(message, "Use /tr [language] &lt;text&gt;, or reply to a message with /tr.")
        return
    await _lookup(message, lambda: lookup.translate(text, target))


@router.message(cmd("dict"))
async def cmd_dict(message: Message, command: CommandObject) -> None:
    word = (command.args or "").strip()
    if not word:
        await reply(message, "Use /dict &lt;word&gt;.")
        return
    if len(word) > 60:
        await reply(message, "That is too long for a word.")
        return
    await _lookup(message, lambda: lookup.dictionary(word))


@router.message(cmd("time"))
async def cmd_time(message: Message, command: CommandObject) -> None:
    query = (command.args or "").strip()
    if not query:
        await reply(message, "Use /time &lt;place or timezone&gt;, like /time Tokyo or /time Europe/Warsaw.")
        return
    await _lookup(message, lambda: lookup.time_in(query[:100]))


@router.message(cmd("weather"))
async def cmd_weather(message: Message, command: CommandObject) -> None:
    query = (command.args or "").strip()
    if not query:
        await reply(message, "Use /weather &lt;place&gt;, like /weather Warsaw.")
        return
    await _lookup(message, lambda: lookup.weather(query[:100]))


@router.message(cmd("cash"))
async def cmd_cash(message: Message, command: CommandObject) -> None:
    usage = "Use /cash &lt;amount&gt; &lt;from&gt; &lt;to&gt;, like /cash 100 usd pln."
    words = (command.args or "").split()
    if len(words) == 2 and all(w.isalpha() for w in words):
        words = ["1"] + words  # /cash usd pln
    if len(words) != 3:
        await reply(message, usage)
        return
    amount = lookup.parse_amount(words[0])
    source, target = words[1], words[2]
    if amount is None or not (len(source) == 3 and len(target) == 3 and source.isalpha() and target.isalpha()):
        await reply(message, usage)
        return
    await _lookup(message, lambda: lookup.currency(amount, source, target))
