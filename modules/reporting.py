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
from core.targets import Target, TargetError, replied_message, resolve_target
from core.text import esc, truncate, user_link, SWITCH as _ANSWERS
from core.i18n import tr

log = logging.getLogger(__name__)

HELP_TITLE = "reporting.title"

ABOUT = "reporting.about"

HELP = [
    ("/report [reason]", "reporting.help.reply_to_a_message_to"),
    ("/report <user> [reason]", "reporting.help.report_a_user_without_replying"),
    ("@admin", "reporting.help.reply_to_a_message_and"),
    ("/reports [on|off]", "reporting.help.in_a_group_turn_reporting", "common.admins"),
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


def _with_id(who: str, ident: int) -> str:
    """A person or chat as the reports show them: the link or name, then the id."""
    return f"{who} [<code>{ident}</code>]"


def _sender_link(message: Message) -> str:
    """Who wrote a message, as a link and an id, or the channel or group name and id."""
    sender_chat = getattr(message, "sender_chat", None)
    if sender_chat is not None:
        return _with_id(f"<b>{esc(sender_chat.title or tr("reporting.a_channel"))}</b>", sender_chat.id)
    user = message.from_user
    return _with_id(user_link(user.id, user.full_name), user.id) if user else tr("reporting.someone")


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

async def _report(
    message: Message, target: Message | None, reason: str, *, explicit: bool, command: CommandObject | None = None
) -> None:
    """
    Report `target` to the admins. `message` is what the reporter wrote. `explicit` is
    /report (which answers when something is wrong) as opposed to an @admin in passing.
    Without a message to report, /report can name the user instead: `command` is then read for
    `/report <user> [reason]`.
    """
    chat = message.chat
    if chat.type not in GROUP_TYPES:
        if explicit:
            await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if await _writes_as_admin(message):
        return  # admins can't report, and it is not worth an answer
    if not await chatsettings.reports(chat.id):
        if explicit:
            await reply(message, tr("reporting.reporting_is_turned_off_in_this"))
        return
    named: Target | None = None
    if target is None:
        if command is None or not (command.args or "").strip():
            await reply(message, tr("reporting.reply_to_the_message_you_want"))
            return
        try:
            named = await resolve_target(message, command, allow_bots=True)
        except TargetError as error:
            await reply(message, str(error))
            return
        reason = named.extra

    reporter = message.from_user
    if named is not None:
        problem = None
        if named.user_id == message.bot.id:
            problem = tr("reporting.report_me_i_m_the_one")
        elif named.user_id == reporter.id:
            problem = tr("reporting.you_can_t_report_yourself")
        elif await moderation.is_chat_admin(message.bot, chat.id, named.user_id):
            problem = tr("reporting.you_can_t_report_an_admin")
        elif await approvals.is_approved(chat.id, named.user_id):
            problem = tr("reporting.you_can_t_report_an_approved")
        if problem:
            await reply(message, problem)
            return
    elif target is not message:  # an @admin that is no reply calls the admins for the message itself
        sender_chat = getattr(target, "sender_chat", None)
        author = target.from_user
        problem = None
        if sender_chat is not None and sender_chat.id == chat.id:
            problem = tr("reporting.you_can_t_report_an_admin")
        elif author is not None and author.id == message.bot.id:
            problem = tr("reporting.report_me_i_m_the_one")
        elif author is not None and author.id == reporter.id:
            problem = tr("reporting.you_can_t_report_yourself")
        elif author is not None and not author.is_bot and await moderation.is_chat_admin(message.bot, chat.id, author.id):
            problem = tr("reporting.you_can_t_report_an_admin")
        elif author is not None and await approvals.is_approved(chat.id, author.id):
            problem = tr("reporting.you_can_t_report_an_approved")
        if problem:
            await reply(message, problem)
            return

    now = time.monotonic()
    key = (chat.id, reporter.id)
    if now - _reported_at.get(key, -COOLDOWN) < COOLDOWN:
        if explicit:
            await reply(message, tr("reporting.you_just_sent_a_report_give"))
        return
    if len(_reported_at) > 5000:
        _reported_at.clear()
    _reported_at[key] = now

    try:
        admins = await _admins(message.bot, chat.id)
    except TelegramAPIError:
        await reply(message, tr("reporting.i_couldn_t_read_the_admins"))
        return

    reason = truncate(" ".join(reason.split()), MAX_REASON)
    reported = _sender_link(target) if named is None else _with_id(user_link(named.user_id, named.name or named.first_name or named.username), named.user_id)
    notice = tr("reporting.reported_to_the_admins", reported=reported)
    if reason:
        notice += tr("reporting.reason", reason=esc(reason))
    # Invisible mentions: they notify the admins without cluttering the message.
    notice += "".join(f'<a href="tg://user?id={a.user.id}">​</a>' for a in admins[:MAX_MENTIONED])
    with suppress(TelegramAPIError):
        await reply(target or message, notice, link_preview_options=NO_PREVIEW)  # no message to report: answer the /report itself

    await _alert_admins(message, target, admins, reporter, reported, reason)


async def _alert_admins(message: Message, target: Message | None, admins: list[Any], reporter, reported: str, reason: str) -> None:
    """A private message to each admin who has alerts on (and has started a chat with the bot)."""
    chat = message.chat
    muted = await reports.opted_out(a.user.id for a in admins[:MAX_ALERTED])
    link = _message_link(chat, (target or message).message_id)  # the /report itself when no message is named
    lines = [
        tr("reporting.new_report_in", v=esc(chat.title or tr("common.a_chat"))),
        tr("reporting.reporter", id=_with_id(user_link(reporter.id, reporter.full_name), reporter.id)),
        tr("reporting.reported", reported=reported),
    ]
    if reason:
        lines.append(f"• <b>Reason:</b> <code>{esc(reason)}</code>")
    excerpt = _excerpt(target) if target is not None else ""
    if excerpt:
        lines.append(f"• <b>Message:</b> <code>{esc(excerpt)}</code>")
    keyboard = (
        InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text=tr("reporting.go_to_the_message"), url=link)]]) if link else None
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
    await _report(message, replied_message(message), command.args or "", explicit=True, command=command)


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
    usage = tr("reporting.use_reports_on_or_reports_off")
    if argument and argument not in _ANSWERS:
        await reply(message, usage)
        return

    if message.chat.type == "private":
        user = message.from_user
        if user is None:
            return
        if not argument:
            on = await reports.alerts_on(user.id)
            await reply(message, tr("reporting.your_report_alerts_are", opt=tr("common.on") if on else tr("common.off"), usage=usage))
            return
        await reports.set_alerts(user.id, _ANSWERS[argument])
        if _ANSWERS[argument]:
            await reply(message, tr("reporting.your_report_alerts_are_now_on"))
        else:
            await reply(message, tr("reporting.your_report_alerts_are_now_off"))
        return

    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("reporting.this_command_only_works_in_groups"))
        return
    if not argument:
        if await moderation.begin_chat_command(message, role, right=LOOK_RIGHT) is None:
            return
        on = await chatsettings.reports(message.chat.id)
        await reply(message, tr("reporting.reporting_is_in_this_chat", opt=tr("common.on") if on else tr("common.off"), usage=usage))
        return
    if await moderation.begin_chat_command(message, role, right=RIGHT) is None:
        return
    await chatsettings.set_reports(message.chat.id, _ANSWERS[argument])
    if _ANSWERS[argument]:
        await reply(message, tr("reporting.reporting_is_now_on_anyone_can"))
    else:
        await reply(message, tr("reporting.reporting_is_now_off_report_and"))
