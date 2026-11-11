"""Report messages to the admins"""

import logging
import re
import time
from contextlib import suppress
from typing import Any

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup, LinkPreviewOptions, Message

from config import settings
from core import approvals, chatsettings, moderation, reports
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.targets import replied_message
from core.text import esc, truncate, user_link, SWITCH as _ANSWERS

log = logging.getLogger(__name__)

HELP_TITLE = "Reporting"

ABOUT = """
Anyone can call the admins to a message that needs attention: reply to it with /report, or write @admin. I answer in the chat and mention the admins, and I also message the ones who have their report alerts on, with a link to the message. For that an admin has to have started a chat with me first.

Admins can't report, and nobody can report an admin or me. When an admin uses /report or @admin, nothing happens. Without a reply, @admin points at the message that contains it.

Reporting is on by default. A chat admin with the right to change chat info can turn it off for the chat. In a private chat with me, /reports switches your own alerts.
"""

HELP = [
    ("/report [reason]", "Reply to a message to report it to the admins, with an optional reason."),
    ("@admin", "Reply to a message and write @admin to call the admins. It does nothing when an admin writes it."),
    ("/reports [on|off]", "In a group, turn reporting on or off for the chat. In a private chat with me, turn your own report alerts on or off. Without an argument it shows the setting.", "admins"),
]

router = Router(name="reporting")

RIGHT = "can_change_info"
LOOK_RIGHT = "can_manage_chat"
COOLDOWN = 20.0         # seconds between reports of one person in one chat
MAX_REASON = 200
MAX_EXCERPT = 200
MAX_MENTIONED = 20      # admins pinged in the chat
MAX_ALERTED = 30        # admins messaged in private
NO_PREVIEW = LinkPreviewOptions(is_disabled=True)

_CALL = re.compile(r"(?<!\w)@admins?(?!\w)", re.IGNORECASE)
_reported_at: dict[tuple[int, int], float] = {}


# --- Helpers ----------------------------------------------------------------------------------

def _message_link(chat, message_id: int) -> str | None:
    """A link that opens the message, when Telegram has one for this kind of chat."""
    if getattr(chat, "username", None):
        return f"https://t.me/{chat.username}/{message_id}"
    chat_id = str(chat.id)
    if chat_id.startswith("-100"):
        return f"https://t.me/c/{chat_id[4:]}/{message_id}"
    return None


def _sender_link(message: Message) -> str:
    """Who wrote a message, as a link, or the channel or group name."""
    sender_chat = getattr(message, "sender_chat", None)
    if sender_chat is not None:
        return f"<b>{esc(sender_chat.title or 'a channel')}</b>"
    user = message.from_user
    return user_link(user.id, user.full_name) if user else "<b>someone</b>"


async def _writes_as_admin(message: Message) -> bool:
    """An admin, an anonymous admin, or no one we can look at (then there is nothing to report)."""
    sender_chat = getattr(message, "sender_chat", None)
    if sender_chat is not None and sender_chat.id == message.chat.id:
        return True  # an admin writing as the group
    user = message.from_user
    if user is None or user.is_bot:
        return True
    return await moderation.is_chat_admin(message.bot, message.chat.id, user.id)


async def _admins(bot, chat_id: int) -> list[Any]:
    """The human admins of a chat. Raises TelegramAPIError when they can't be read."""
    return [a for a in await bot.get_chat_administrators(chat_id) if not a.user.is_bot]


def _excerpt(target: Message) -> str:
    text = (getattr(target, "text", None) or getattr(target, "caption", None) or "").strip()
    return truncate(" ".join(text.split()), MAX_EXCERPT) if text else ""


# --- Filing a report ------------------------------------------------------------------------------

async def _report(message: Message, target: Message | None, reason: str, *, explicit: bool) -> None:
    """
    Report `target` to the admins. `message` is what the reporter wrote. `explicit` is
    /report (which answers when something is wrong) as opposed to an @admin in passing.
    """
    chat = message.chat
    if chat.type not in GROUP_TYPES:
        if explicit:
            await reply(message, "This command only works in groups.")
        return
    if await _writes_as_admin(message):
        return  # admins can't report, and it is not worth an answer
    if not await chatsettings.reports(chat.id):
        if explicit:
            await reply(message, "Reporting is turned off in this chat.")
        return
    if target is None:
        await reply(message, "Reply to the message you want to report.")
        return

    reporter = message.from_user
    if target is not message:  # an @admin that is no reply calls the admins for the message itself
        sender_chat = getattr(target, "sender_chat", None)
        author = target.from_user
        problem = None
        if sender_chat is not None and sender_chat.id == chat.id:
            problem = "You can't report an admin."
        elif author is not None and author.id == message.bot.id:
            problem = "Report me? I'm the one who does the reporting. 😏"
        elif author is not None and author.id == reporter.id:
            problem = "You can't report yourself."
        elif author is not None and not author.is_bot and await moderation.is_chat_admin(message.bot, chat.id, author.id):
            problem = "You can't report an admin."
        elif author is not None and await approvals.is_approved(chat.id, author.id):
            problem = "You can't report an approved user."
        if problem:
            await reply(message, problem)
            return

    now = time.monotonic()
    key = (chat.id, reporter.id)
    if now - _reported_at.get(key, -COOLDOWN) < COOLDOWN:
        if explicit:
            await reply(message, "You just sent a report. Give the admins a moment.")
        return
    if len(_reported_at) > 5000:
        _reported_at.clear()
    _reported_at[key] = now

    try:
        admins = await _admins(message.bot, chat.id)
    except TelegramAPIError:
        await reply(message, "I couldn't read the admins of this chat.")
        return

    reason = truncate(" ".join(reason.split()), MAX_REASON)
    reported = _sender_link(target)
    notice = f"Reported {reported} to the admins."
    if reason:
        notice += f"\n• <b>Reason:</b> <code>{esc(reason)}</code>"
    # Invisible mentions: they notify the admins without cluttering the message.
    notice += "".join(f'<a href="tg://user?id={a.user.id}">​</a>' for a in admins[:MAX_MENTIONED])
    with suppress(TelegramAPIError):
        await reply(target, notice, link_preview_options=NO_PREVIEW)

    await _alert_admins(message, target, admins, reporter, reported, reason)


async def _alert_admins(message: Message, target: Message, admins: list[Any], reporter, reported: str, reason: str) -> None:
    """A private message to each admin who has alerts on (and has started a chat with the bot)."""
    chat = message.chat
    muted = await reports.opted_out(a.user.id for a in admins[:MAX_ALERTED])
    link = _message_link(chat, target.message_id)
    lines = [
        f"<b>New report in {esc(chat.title or 'a chat')}</b>",
        f"• <b>Reporter:</b> {user_link(reporter.id, reporter.full_name)}",
        f"• <b>Reported:</b> {reported}",
    ]
    if reason:
        lines.append(f"• <b>Reason:</b> <code>{esc(reason)}</code>")
    excerpt = _excerpt(target)
    if excerpt:
        lines.append(f"• <b>Message:</b> <code>{esc(excerpt)}</code>")
    keyboard = (
        InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text="Go to the message", url=link)]]) if link else None
    )
    for admin in admins[:MAX_ALERTED]:
        if admin.user.id in muted or admin.user.id == reporter.id:
            continue
        with suppress(TelegramAPIError):  # an admin who never started the bot can't be messaged
            await message.bot.send_message(
                admin.user.id, "\n".join(lines), reply_markup=keyboard, link_preview_options=NO_PREVIEW
            )


# --- /report and @admin -----------------------------------------------------------------------

@router.message(cmd("report"))
async def cmd_report(message: Message, command: CommandObject) -> None:
    await _report(message, replied_message(message), command.args or "", explicit=True)


def _is_call(message: Message) -> bool:
    text = message.text or message.caption or ""
    if not text or text[0] in settings.COMMAND_PREFIXES:
        return False
    return message.chat.type in GROUP_TYPES and message.from_user is not None and _CALL.search(text) is not None


async def _calls_admins(message: Message) -> bool:
    """A message that writes @admin, from someone who is not an admin and where reporting is on."""
    if not _is_call(message):
        return False
    if not await chatsettings.reports(message.chat.id):
        return False
    return not await _writes_as_admin(message)


@router.message(_calls_admins)
async def on_call(message: Message) -> None:
    text = message.text or message.caption or ""
    await _report(message, replied_message(message) or message, _CALL.sub("", text), explicit=False)


# --- /reports ------------------------------------------------------------------------------------

@router.message(cmd("reports"))
async def cmd_reports(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = "Use /reports on or /reports off."
    if argument and argument not in _ANSWERS:
        await reply(message, usage)
        return

    if message.chat.type == "private":
        user = message.from_user
        if user is None:
            return
        if not argument:
            on = await reports.alerts_on(user.id)
            await reply(message, f"Your report alerts are <b>{'on' if on else 'off'}</b>.\n{usage}")
            return
        await reports.set_alerts(user.id, _ANSWERS[argument])
        if _ANSWERS[argument]:
            await reply(message, "Your report alerts are now <b>on</b>. I'll message you when someone reports something in a chat where you are an admin.")
        else:
            await reply(message, "Your report alerts are now <b>off</b>. Reports still mention you in the chat.")
        return

    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups and in private.")
        return
    if not argument:
        if await moderation.begin_chat_command(message, role, right=LOOK_RIGHT) is None:
            return
        on = await chatsettings.reports(message.chat.id)
        await reply(message, f"Reporting is <b>{'on' if on else 'off'}</b> in this chat.\n{usage}")
        return
    if await moderation.begin_chat_command(message, role, right=RIGHT) is None:
        return
    await chatsettings.set_reports(message.chat.id, _ANSWERS[argument])
    if _ANSWERS[argument]:
        await reply(message, "Reporting is now <b>on</b>: anyone can report a message with /report or @admin.")
    else:
        await reply(message, "Reporting is now <b>off</b>: /report and @admin do nothing.")
