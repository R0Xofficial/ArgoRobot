"""
Notices for the developer log (DEV_LOG_CHAT): the bot added to or removed from a
chat, and what happens to federations (created, renamed, handed over, deleted).
Sending never raises and never holds up a command.
"""

import asyncio
import logging
from typing import Any

from aiogram import Bot
from aiogram.types import ChatMemberUpdated

from core import logs
from core.text import esc, user_ref

log = logging.getLogger(__name__)

CHAT_TYPES = ("group", "supergroup", "channel")
_OUT = ("left", "kicked")
_IN = ("member", "administrator", "creator", "restricted")

_tasks: set[asyncio.Task] = set()


def _value(status: Any) -> str:
    return str(getattr(status, "value", status))


def chat_ref(title: str | None, username: str | None, chat_id: int) -> str:
    """A chat's name for a log: a link to it when it is public."""
    name = esc(title or chat_id)
    return f'<a href="https://t.me/{esc(username)}">{name}</a>' if username else name


def _spawn(coroutine) -> None:
    task = asyncio.get_running_loop().create_task(coroutine)
    _tasks.add(task)
    task.add_done_callback(_tasks.discard)


def _line(label: str, value: object) -> str:
    return f"<b>{label}:</b> {value}"


def _text(tag: str, lines: list[str]) -> str:
    return "\n".join([f"<b>{tag}</b>", *lines, _line("Timestamp", logs.timestamp())])


async def _send(bot: Bot, text: str) -> None:
    try:
        await logs.dev_log(bot, text)
    except Exception:
        log.exception("Developer log notice failed")


def notify(bot: Bot, tag: str, lines: list[str]) -> None:
    """Send a notice in the background."""
    _spawn(_send(bot, _text(tag, lines)))


# --- Chats ------------------------------------------------------------------------------------

def _by(event: ChatMemberUpdated) -> str:
    user = event.from_user
    return user_ref(user.id, user.full_name)


def chat_event(bot: Bot, event: ChatMemberUpdated) -> None:
    """The bot was added to a chat or taken out of it (a change of its rights says nothing)."""
    chat = event.chat
    if _value(chat.type) not in CHAT_TYPES:
        return
    old = _value(getattr(event.old_chat_member, "status", None))
    new = _value(getattr(event.new_chat_member, "status", None))
    where = [
        _line("Chat", chat_ref(chat.title, chat.username, chat.id)),
        _line("Chat ID", f"<code>{chat.id}</code>"),
        _line("Type", _value(chat.type)),
    ]
    if old in _OUT and new in _IN:
        notify(bot, "#NEWGROUP", where + [_line("Added by", _by(event))])
    elif old in _IN and new in _OUT:
        notify(bot, "#LEFTGROUP", where + [_line("Removed by" if new == "kicked" else "Left by", _by(event))])


# --- Federations ------------------------------------------------------------------------------

def _fed(fed: dict[str, Any]) -> list[str]:
    return [_line("Federation", f"<code>{esc(fed['name'])}</code>"), _line("Fed ID", f"<code>{fed['fed_id']}</code>")]


def fed_created(bot: Bot, fed: dict[str, Any], user_id: int, name: str | None) -> None:
    notify(bot, "#NEWFED", _fed(fed) + [_line("By", user_ref(user_id, name))])


def fed_deleted(bot: Bot, fed: dict[str, Any], user_id: int, name: str | None) -> None:
    notify(bot, "#DELFED", _fed(fed) + [_line("By", user_ref(user_id, name))])


def fed_renamed(bot: Bot, fed: dict[str, Any], new_name: str, user_id: int, name: str | None) -> None:
    notify(
        bot,
        "#RENAMEFED",
        [
            _line("Old Name", f"<code>{esc(fed['name'])}</code>"),
            _line("New Name", f"<code>{esc(new_name)}</code>"),
            _line("Fed ID", f"<code>{fed['fed_id']}</code>"),
            _line("By", user_ref(user_id, name)),
        ],
    )


def fed_transferred(bot: Bot, fed: dict[str, Any], new_id: int, new_name: str | None) -> None:
    notify(
        bot,
        "#FEDTRANSFER",
        _fed(fed) + [_line("Previous owner", user_ref(fed["owner_id"], fed["owner_name"])), _line("New owner", user_ref(new_id, new_name))],
    )
