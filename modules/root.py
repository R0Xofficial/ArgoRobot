"""Core bot commands"""

import time

from aiogram import F, Router
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from config import settings
from core import help_index, registry
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.roles import Role
from core.text import esc

# Protected modules cannot be disabled with /module, so the commands in this
# file (including /module itself) are always available.
PROTECTED = True

# Commands shown in /help (see core/help_index.py).
HELP = [
    ("/start", "Introduction"),
    ("/help", "Show the help panel"),
    ("/ping", "Check how fast I respond"),
    ("/modules", "List modules and their status", Role.DEVELOPER),
    ("/module <enable|disable> <name>", "Enable or disable a module", Role.DEVELOPER),
]

router = Router(name="root")

CB_START = "root:start"
CB_HELP = "help:home"
CB_HELP_MODULE = "help:module:"


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


def _help_home_text() -> str:
    prefixes = " ".join(f"<code>{esc(p)}</code>" for p in settings.COMMAND_PREFIXES)
    return (
        "<b>Help</b>\n\n"
        "Choose a category to see its commands.\n\n"
        f"Commands can start with: {prefixes}"
    )


def _help_category_text(info: registry.ModuleInfo, role: Role | None) -> str:
    title = f"<b>{esc(info.name)}</b>"
    if info.description:
        title += f" - {esc(info.description)}"

    lines = [title, ""]
    for usage, description in help_index.entries(info.module, role):
        lines.append(f"{esc(usage)} - {esc(description)}")
    return "\n".join(lines)


def _start_keyboard() -> InlineKeyboardMarkup:
    row = []
    if settings.GITHUB_URL:
        row.append(InlineKeyboardButton(text="GitHub", url=settings.GITHUB_URL))
    row.append(InlineKeyboardButton(text="Help", callback_data=CB_HELP))
    return InlineKeyboardMarkup(inline_keyboard=[row])


def _help_home_keyboard(role: Role | None) -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=info.name, callback_data=f"{CB_HELP_MODULE}{info.name}")
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
        await reply(message, _help_home_text(), reply_markup=_help_home_keyboard(role))
        return

    await reply(message, _start_text(_display_name(message)), reply_markup=_start_keyboard())


@router.callback_query(F.data == CB_START)
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

    await reply(message, _help_home_text(), reply_markup=_help_home_keyboard(role))


@router.callback_query(F.data == CB_HELP)
async def cb_help_home(callback: CallbackQuery, role: Role | None = None) -> None:
    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(callback.message, _help_home_text(), reply_markup=_help_home_keyboard(role))


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


# --- /ping ------------------------------------------------------------------

@router.message(cmd("ping"))
async def cmd_ping(message: Message) -> None:
    # Latency is the round trip of sending a message to Telegram.
    started = time.perf_counter()
    sent = await reply(message, "<b>Pong!</b>")
    latency_ms = (time.perf_counter() - started) * 1000
    await edit(sent, f"<b>Pong!</b>\nLatency: <code>{latency_ms:.0f}</code> ms.")


# --- Bot management ---------------------------------------------------------

@router.message(cmd("modules"), HasRole(Role.DEVELOPER))
async def cmd_modules(message: Message) -> None:
    modules = registry.all_modules()
    if not modules:
        await reply(message, "No modules loaded.")
        return

    lines = ["<b>Modules</b>"]
    for info in modules:
        status = "enabled" if info.enabled else "disabled"
        line = f"<code>{esc(info.name)}</code> - <b>{status}</b>"
        if info.protected:
            line += " [protected]"
        if info.description:
            line += f" <i>{esc(info.description)}</i>"
        lines.append(line)

    await reply(message, "\n".join(lines))


@router.message(cmd("module"), HasRole(Role.DEVELOPER))
async def cmd_module(message: Message, command: CommandObject) -> None:
    args = (command.args or "").split()
    action = args[0].lower() if args else ""

    if len(args) != 2 or action not in ("enable", "disable"):
        await reply(message, "Usage: /module &lt;enable|disable&gt; &lt;name&gt;")
        return

    name = args[1]
    try:
        found = await registry.set_enabled(name, action == "enable")
    except registry.ProtectedModuleError:
        await reply(message, f"Module <code>{esc(name.lower())}</code> is protected and cannot be disabled.")
        return

    if not found:
        await reply(message, f"Module <code>{esc(name)}</code> not found.")
        return

    past = "enabled" if action == "enable" else "disabled"
    await reply(message, f"Module <code>{esc(name.lower())}</code> {past}.")
