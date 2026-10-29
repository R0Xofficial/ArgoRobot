"""Core bot commands: /start, /help, /ping and bot management."""

import time

from aiogram import F, Router
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from config import settings
from core import registry
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.roles import Role
from core.text import esc

# Protected modules cannot be disabled with /module, so the commands in this
# file (including /module itself) are always available.
PROTECTED = True

router = Router(name="root")

CB_HELP = "root:help"
CB_START = "root:start"


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


def _group_text(name: str) -> str:
    return (
        f"Hello, <b>{esc(name)}</b>! I'm <b>Argo</b>.\n\n"
        "I'd rather not discuss the details in front of everyone. "
        "Message me in private and I'll tell you what I can do."
    )


def _help_text(role: Role | None) -> str:
    prefixes = ", ".join(f"<code>{esc(p)}</code>" for p in settings.COMMAND_PREFIXES)
    lines = [
        "<b>Argo - help</b>",
        "",
        "<b>General</b>",
        "/start - introduction",
        "/help - this message",
        "/ping - check how fast I respond",
    ]
    if role is not None and role >= Role.DEVELOPER:
        lines += [
            "",
            "<b>Bot management</b>",
            "/modules - list modules and their status",
            "/module &lt;enable|disable&gt; &lt;name&gt; - toggle a module",
        ]
    lines += ["", f"Commands can start with any of these characters: {prefixes}"]
    return "\n".join(lines)


def _start_keyboard() -> InlineKeyboardMarkup:
    row = []
    if settings.GITHUB_URL:
        row.append(InlineKeyboardButton(text="GitHub", url=settings.GITHUB_URL))
    row.append(InlineKeyboardButton(text="Help", callback_data=CB_HELP))
    return InlineKeyboardMarkup(inline_keyboard=[row])


def _back_keyboard() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text="Back", callback_data=CB_START)]]
    )


async def _send_group_nudge(message: Message) -> None:
    """In groups: greet and point the user to the private chat."""
    me = await message.bot.me()
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text="Message me", url=f"https://t.me/{me.username}?start=help")]
        ]
    )
    await reply(message, _group_text(_display_name(message)), reply_markup=keyboard)


# --- /start, /help ----------------------------------------------------------

@router.message(cmd("start"))
async def cmd_start(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type != "private":
        await _send_group_nudge(message)
        return

    # Deep link from the group button: t.me/<bot>?start=help
    if (command.args or "").strip().lower() == "help":
        await reply(message, _help_text(role), reply_markup=_back_keyboard())
        return

    await reply(message, _start_text(_display_name(message)), reply_markup=_start_keyboard())


@router.message(cmd("help"))
async def cmd_help(message: Message, role: Role | None = None) -> None:
    if message.chat.type != "private":
        await _send_group_nudge(message)
        return
    await reply(message, _help_text(role), reply_markup=_back_keyboard())


@router.callback_query(F.data == CB_HELP)
async def cb_help(callback: CallbackQuery, role: Role | None = None) -> None:
    await callback.answer()  # always answer first, otherwise the button keeps spinning
    if isinstance(callback.message, Message):
        await edit(callback.message, _help_text(role), reply_markup=_back_keyboard())


@router.callback_query(F.data == CB_START)
async def cb_start(callback: CallbackQuery) -> None:
    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(
            callback.message,
            _start_text(callback.from_user.full_name),
            reply_markup=_start_keyboard(),
        )


# --- /ping ------------------------------------------------------------------

@router.message(cmd("ping"))
async def cmd_ping(message: Message) -> None:
    # Latency is the round trip of sending a message to Telegram.
    started = time.perf_counter()
    sent = await reply(message, "Pong!")
    latency_ms = (time.perf_counter() - started) * 1000
    await edit(sent, f"Pong!\nLatency: <b>{latency_ms:.0f} ms</b>")


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
        if info.protected:
            status += ", protected"
        line = f"<code>{esc(info.name)}</code> - {status}"
        if info.description:
            line += f" ({esc(info.description)})"
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
