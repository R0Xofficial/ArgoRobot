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
from core.text import at_name, esc
from core.i18n import tr, tr_or

# Commands shown in /help (see core/help_index.py).
ABOUT = "misc.about"

HELP = [
    ("/start", "misc.help.say_hello_in_a_group"),
    ("/help [topic]", "misc.help.open_this_panel_or_go"),
    ("/id [user]", "misc.help.show_your_telegram_id_plus"),
    ("/info [user]", "misc.help.show_what_i_know_about"),
    ("/tr [language] <text>", "misc.help.translate_text_into_a_language"),
    ("/dict <word>", "misc.help.look_up_an_english_word"),
    ("/time <place or timezone>", "misc.help.show_the_current_time_and"),
    ("/weather <place>", "misc.help.show_the_current_weather_in"),
    ("/cash <amount> <from> <to>", "misc.help.convert_money_between_currencies_like"),
]

HELP_NOTES = [
    (
        "common.examples",
        "misc.note.tr_pl_good_morning_translate",
    ),
]

router = Router(name="misc")

CB_START = "start:home"
CB_START_LEGACY = "root:start"  # buttons on messages sent before start moved here
CB_HELP = "help:home"
CB_HELP_MODULE = "help:module:"
CB_HELP_SECTION = "help:section:"
CB_HELP_PAGE = "help:page:"
CB_HELP_MORE = "help:more:"  # help:more:<module>:<section number or ->:<page number>

# A module page up to this long stays one page; a longer one keeps its notes behind buttons,
# and when even its commands are too much they get pages of their own.
SINGLE_LIMIT = 2000
MAIN_LIMIT = 2400
PART_LIMIT = 1800

NO_PREVIEW = LinkPreviewOptions(is_disabled=True)


# --- Texts and keyboards ----------------------------------------------------

def _display_name(message: Message) -> str:
    return message.from_user.full_name if message.from_user else tr("misc.there")


def _start_text(name: str) -> str:
    text = (
        tr("misc.hello_i_m_argo_i_m", name=esc(name))
    )
    support = at_name(settings.SUPPORT_CHAT_USERNAME)
    if support:
        text += tr("misc.questions_bugs_or_appeals_join_the", support=esc(support))
    return text


def _group_start_text(name: str, username: str) -> str:
    return (
        tr("misc.hello_i_m_argo_i_d", name=esc(name), username=esc(username))
    )


def _support_line() -> str:
    name = at_name(settings.SUPPORT_CHAT_USERNAME)
    return tr("misc.found_a_bug_or_something_doesn", name=esc(name)) if name else ""


def _help_home_text(role: Role | None = None) -> str:
    prefixes = " ".join(f"<code>{esc(p)}</code>" for p in settings.COMMAND_PREFIXES)
    lines = [
        tr("misc.help"),
        "",
        tr("misc.pick_a_category_below_to_see"),
        "",
        tr("misc.useful_commands"),
        tr("misc.start_say_hello_in_a_private"),
        tr("misc.help_open_this_panel_help_notes"),
        "",
        tr("misc.settings_that_take_on_or_off"),
        tr("misc.commands_can_start_with", prefixes=prefixes),
    ]
    support = _support_line()
    if support:
        lines += ["", support]
    return "\n".join(lines)


def _help_title(info: registry.ModuleInfo) -> str:
    """Name of a module in /help: its HELP_TITLE if it has one (AFK), else the capitalized name."""
    title = getattr(info.module, "HELP_TITLE", None)
    return tr(title) if title else tr_or(f"help.title.{info.name}", info.name.capitalize())


def _paragraphs(text: str) -> str:
    """Plain text from a module (ABOUT, notes) made safe for HTML; blank lines stay."""
    return "\n".join(esc(line.rstrip()) for line in inspect.cleandoc(text).splitlines())


def _help_parts(info: registry.ModuleInfo, role: Role | None, section: int | None = None) -> tuple[str, list[tuple[str, str]]]:
    """
    The page of one module and the extra pages behind its buttons, as (text, [(label, html)]).
    The page says what the module is, then lists the commands this person may use. A module with
    sections shows the commands that belong to none here, and a page per section. A page that
    gets long keeps its notes behind buttons, and its commands too when they alone are too much.
    """
    names = help_index.sections(info.module)
    chosen = names[section] if section is not None and 0 <= section < len(names) else None
    title = _help_title(info)
    page_title = f"<b>{esc(title)}</b>" if chosen is None else f"<b>{esc(title)} - {esc(tr(chosen))}</b>"
    head = [page_title]
    if chosen is None:
        if info.description:
            head.append(f"<i>{esc(tr_or(f'help.description.{info.name}', info.description))}</i>")
        about = getattr(info.module, "ABOUT", None)
        if about:
            head += ["", _paragraphs(tr(about))]

    blocks = []
    for usage, description, who in help_index.detailed(info.module, role, chosen):
        line = f"<code>{esc(usage)}</code>" + (f"  <i>({esc(tr(who))})</i>" if who else "")
        blocks.append(f"{line}\n{esc(tr(description))}")
    hint = ["", tr("misc.pick_a_section_below_for_the")] if chosen is None and help_index.visible_sections(info.module, role) else []
    notes = [(tr(note[0]), tr(note[1])) for note in getattr(info.module, "HELP_NOTES", ()) if (str(note[2]) if len(note) > 2 else None) == chosen]

    def commands(items: list[str]) -> list[str]:
        return ["", f"<b>{tr("misc.commands")}</b>", ""] + "\n\n".join(items).split("\n") if items else []

    full = "\n".join(head + commands(blocks) + hint + [x for title_, text in notes for x in ("", f"<b>{esc(title_)}</b>", _paragraphs(text))])
    if len(full) <= SINGLE_LIMIT:
        return full, []

    extra = [(label, f"{page_title.replace('</b>', '')} - {esc(label)}</b>\n\n{_paragraphs(text)}") for label, text in notes]
    core = "\n".join(head + commands(blocks) + hint)
    if len(core) <= MAIN_LIMIT:
        return core, extra

    chunks, current = [], []
    for block in blocks:
        if current and len("\n\n".join(current + [block])) > PART_LIMIT:
            chunks.append(current)
            current = []
        current.append(block)
    if current:
        chunks.append(current)
    pages = []
    for number, chunk in enumerate(chunks, 1):
        label = tr("misc.commands") if len(chunks) == 1 else tr("misc.commands_2", number=number)
        pages.append((label, f"{page_title.replace('</b>', '')} - {label}</b>\n\n" + "\n\n".join(chunk)))
    return "\n".join(head + hint), pages + extra


def _help_category_text(info: registry.ModuleInfo, role: Role | None, section: int | None = None) -> str:
    return _help_parts(info, role, section)[0]


def _start_keyboard() -> InlineKeyboardMarkup:
    row = []
    if settings.GITHUB_URL:
        row.append(InlineKeyboardButton(text=tr("misc.github"), url=settings.GITHUB_URL))
    row.append(InlineKeyboardButton(text=tr("misc.help_2"), callback_data=CB_HELP))
    return InlineKeyboardMarkup(inline_keyboard=[row])


def _help_home_keyboard(role: Role | None) -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=_help_title(info), callback_data=f"{CB_HELP_MODULE}{info.name}")
        for info in help_index.categories(role)
    ]
    # Three to a row. A last row of two or one is stretched over the whole width by Telegram.
    rows = [buttons[i : i + 3] for i in range(0, len(buttons), 3)]
    rows.append([InlineKeyboardButton(text=tr("common.back"), callback_data=CB_START)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def _more_rows(info: registry.ModuleInfo, role: Role | None, section: int | None) -> list[list[InlineKeyboardButton]]:
    """The buttons of a page's extra pages (long command lists, notes), two to a row."""
    where = "-" if section is None else str(section)
    buttons = [
        InlineKeyboardButton(text=label, callback_data=f"{CB_HELP_MORE}{info.name}:{where}:{number}")
        for number, (label, _html) in enumerate(_help_parts(info, role, section)[1])
    ]
    return [buttons[i : i + 2] for i in range(0, len(buttons), 2)]


def _help_category_keyboard(info: registry.ModuleInfo | None = None, role: Role | None = None, section: int | None = None) -> InlineKeyboardMarkup:
    """Back to the list of categories, or to the module's page from one of its sections."""
    if info is not None and section is not None:
        rows = _more_rows(info, role, section)
        rows.append([InlineKeyboardButton(text=tr("common.back"), callback_data=f"{CB_HELP_MODULE}{info.name}")])
        return InlineKeyboardMarkup(inline_keyboard=rows)
    rows = []
    if info is not None:
        rows = [
            [InlineKeyboardButton(text=tr(name), callback_data=f"{CB_HELP_SECTION}{info.name}:{number}")]
            for number, name in help_index.visible_sections(info.module, role)
        ]
        rows += _more_rows(info, role, None)
        rows += [
            [InlineKeyboardButton(text=tr(label), callback_data=f"{CB_HELP_PAGE}{info.name}:{number}:0")]
            for number, label in help_index.pages(info.module)
        ]
    rows.append([InlineKeyboardButton(text=tr("common.back"), callback_data=CB_HELP)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def _help_page_keyboard(info: registry.ModuleInfo, number: int, part: int, total: int) -> InlineKeyboardMarkup:
    rows = []
    if total > 1:
        move = []
        if part > 0:
            move.append(InlineKeyboardButton(text=tr("misc.previous"), callback_data=f"{CB_HELP_PAGE}{info.name}:{number}:{part - 1}"))
        if part < total - 1:
            move.append(InlineKeyboardButton(text=tr("misc.next"), callback_data=f"{CB_HELP_PAGE}{info.name}:{number}:{part + 1}"))
        rows.append(move)
    rows.append([InlineKeyboardButton(text=tr("common.back"), callback_data=f"{CB_HELP_MODULE}{info.name}")])
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
        await reply(message, tr("misc.i_have_no_help_page_for", v=esc(topic[:40])))
        return

    if message.chat.type != "private":
        # Groups: a button that opens the help panel (or the asked-for page) in the private chat.
        me = await message.bot.me()
        suffix = f"help_{info.name}" if info is not None else "help"
        keyboard = InlineKeyboardMarkup(
            inline_keyboard=[
                [InlineKeyboardButton(text=tr("misc.help_2"), url=f"https://t.me/{me.username}?start={suffix}")]
            ]
        )
        await reply(
            message,
            tr("misc.click_the_button_below_to_see"),
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
        await callback.answer(tr("misc.this_category_is_not_available"), show_alert=True)
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
        await callback.answer(tr("misc.this_section_is_not_available"), show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(
            callback.message,
            _help_category_text(info, role, int(number)),
            reply_markup=_help_category_keyboard(info, role, int(number)),
        )


@router.callback_query(F.data.startswith(CB_HELP_MORE))
async def cb_help_more(callback: CallbackQuery, role: Role | None = None) -> None:
    name, _, rest = (callback.data or "")[len(CB_HELP_MORE):].partition(":")
    where, _, number = rest.partition(":")
    info = registry.get(name)
    section = int(where) if where.isdigit() else None
    found = None
    if info is not None and info.enabled and help_index.entries(info.module, role) and number.isdigit() and (where == "-" or section is not None):
        extra = _help_parts(info, role, section)[1]
        if int(number) < len(extra):
            found = extra[int(number)][1]
    if found is None:
        await callback.answer(tr("misc.this_page_is_not_available"), show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        back = f"{CB_HELP_SECTION}{info.name}:{section}" if section is not None else f"{CB_HELP_MODULE}{info.name}"
        await edit(
            callback.message,
            found,
            reply_markup=InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text=tr("common.back"), callback_data=back)]]),
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
        await callback.answer(tr("misc.this_page_is_not_available"), show_alert=True)
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
    "user": "common.user",
    "bot": "misc.kind_labels.bot",
    "group": "misc.kind_labels.group",
    "supergroup": "misc.kind_labels.supergroup",
    "channel": "common.channel",
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
    "misc.unknown_user"
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

    kind = tr(KIND_LABELS[target.kind])
    if explicit:
        label = tr("misc.id", kind=kind)
    else:
        label = tr("misc.your_id") if not target.is_chat else tr("misc.id", kind=kind)
    lines = [_line(label, f"<code>{target.user_id}</code>")]

    # Handy for filling in log chat settings.
    if not explicit and message.chat.type != "private":
        lines.append(_line(tr("common.chat_id"), f"<code>{message.chat.id}</code>"))
        if message.is_topic_message and message.message_thread_id:
            lines.append(_line(tr("misc.topic_id"), f"<code>{message.message_thread_id}</code>"))

    await reply(message, "\n".join(lines))


async def _chat_member(bot, chat_id: int, user_id: int):
    """The user's membership in a chat; the string "gone" if Telegram has never seen them; None if unknown."""
    try:
        return await bot.get_chat_member(chat_id, user_id)
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if "user not found" in problem or "participant_id_invalid" in problem:
            return tr("misc.gone")
        return None  # e.g. "member list is inaccessible": better to say nothing
    except TelegramAPIError:
        return None


def _chat_status(member) -> str:
    """The user's standing in a chat, from their membership."""
    if member == "gone":
        return tr("misc.not_in_chat")
    status = getattr(member.status, "value", member.status)
    if status == "creator":
        return tr("common.owner")
    if status == "administrator":
        return tr("common.admin")
    if status == "member":
        return tr("misc.member")
    if status == "restricted":
        return tr("misc.member") if getattr(member, "is_member", True) else tr("misc.not_in_chat")
    return tr("misc.not_in_chat")  # left or kicked


# The order of the yes/no flags of the bot's own section in /info. Flags from modules this list
# doesn't know come last.
FLAG_ORDER = ("Globally Banned", "Whitelisted", "Immune")


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
            return tr("misc.banned")
        if status == "restricted" and getattr(member, "is_member", True) and not getattr(member, "can_send_messages", True):
            text = tr("misc.muted")
    if approved and text in ("Member", "Muted"):
        text += tr("misc.approved")
    elif captcha and text == "Muted":
        text = tr("misc.muted_no_captcha")  # muted until the captcha is solved (modules/captcha.py)
    return text


def _flag_lines(flags: list[tuple[str, bool]]) -> list[str]:
    def rank(flag: tuple[str, bool]) -> int:
        return FLAG_ORDER.index(flag[0]) if flag[0] in FLAG_ORDER else len(FLAG_ORDER)

    return [_item(name, f"<code>{tr("common.yes") if value else tr("common.no")}</code>") for name, value in sorted(flags, key=rank)]


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

    lines = [tr("misc.user_info"), _item(tr("common.id"), f"<code>{target.user_id}</code>")]
    if target.first_name:
        lines.append(_item(tr("misc.first_name"), esc(target.first_name)))
    if target.last_name:
        lines.append(_item(tr("misc.last_name"), esc(target.last_name)))
    if target.username:
        lines.append(_item(tr("misc.username"), f"@{esc(target.username)}"))
    lines.append(_item(tr("misc.user_link"), f'<a href="tg://user?id={target.user_id}">link</a>'))

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
        lines.append(_item(tr("misc.status"), f"<code>{standing}</code>"))

    about = []
    if role is not None:
        about.append(_item(tr("misc.bot_role"), f"<code>{roles.label(role)}</code>"))
    about.extend(_flag_lines(flags))
    if about:
        lines += ["", tr("misc.argo_info")] + about
    # Sentences the modules write themselves (a federation ban with its reason ...) close the message.
    for note in await info_hooks.notes_for(bot, target.user_id, chat_id):
        lines += ["", note]
    return lines


async def _chat_info(bot, target: Target) -> list[str]:
    lines = [tr("misc.chat_info"), _item(tr("common.id"), f"<code>{target.user_id}</code>")]
    if target.title:
        lines.append(_item(tr("common.title"), esc(target.title)))
    lines.append(_item(tr("misc.type"), f"<code>{tr(KIND_LABELS[target.kind])}</code>"))
    if target.username:
        lines.append(_item(tr("misc.username"), f"@{esc(target.username)}"))
        lines.append(_item(tr("misc.link"), f'<a href="https://t.me/{esc(target.username)}">link</a>'))
    try:
        members = await bot.get_chat_member_count(target.user_id)
    except TelegramAPIError:
        members = None
    if members is not None:
        lines.append(_item(tr("misc.members"), f"<code>{members}</code>"))
    return lines


@router.message(cmd("info"))
async def cmd_info(message: Message, command: CommandObject) -> None:
    try:
        target, _ = await _subject(message, command)
    except UnknownUserError:
        await reply(message, tr(UNKNOWN_USER))
        return
    except TargetError as exc:
        await reply(message, esc(str(exc)))
        return

    if not target.is_known:
        await reply(message, tr(UNKNOWN_USER))
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
        await reply(message, tr("common.slow_down_a_little_and_try"))
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
        await reply(message, tr("misc.use_tr_language_lt_text_gt"))
        return
    await _lookup(message, lambda: lookup.translate(text, target))


@router.message(cmd("dict"))
async def cmd_dict(message: Message, command: CommandObject) -> None:
    word = (command.args or "").strip()
    if not word:
        await reply(message, tr("misc.use_dict_lt_word_gt"))
        return
    if len(word) > 60:
        await reply(message, tr("misc.that_is_too_long_for_a"))
        return
    await _lookup(message, lambda: lookup.dictionary(word))


@router.message(cmd("time"))
async def cmd_time(message: Message, command: CommandObject) -> None:
    query = (command.args or "").strip()
    if not query:
        await reply(message, tr("misc.use_time_lt_place_or_timezone"))
        return
    await _lookup(message, lambda: lookup.time_in(query[:100]))


@router.message(cmd("weather"))
async def cmd_weather(message: Message, command: CommandObject) -> None:
    query = (command.args or "").strip()
    if not query:
        await reply(message, tr("misc.use_weather_lt_place_gt_like"))
        return
    await _lookup(message, lambda: lookup.weather(query[:100]))


@router.message(cmd("cash"))
async def cmd_cash(message: Message, command: CommandObject) -> None:
    usage = tr("misc.use_cash_lt_amount_gt_lt")
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
