"""
Global error handling and traceback reporting.

Telegram regularly produces errors that are harmless for a bot: an edited or
deleted command, a message that is already gone, an expired callback query, a
closed topic. These are logged as one short line (or only at DEBUG).

Genuinely unexpected exceptions are different: they are logged with a
traceback AND sent to the log chat as a message with the full traceback
attached as a .txt file:

    Bot Error Detected!

    Error: httpx.ConnectError:
    Chat: N/A
    User: N/A

    Full traceback is attached as a file.

Two places are covered: exceptions raised inside update handlers (the
dispatcher error handler, which also knows the chat and user) and exceptions
that escape anywhere else, e.g. in a background task (the asyncio loop
exception handler).

Telegram API and network errors are not reported this way, because the report
itself is sent through Telegram and would most likely fail for the same reason.
"""

import asyncio
import logging
import time
import traceback
from datetime import datetime, timezone
from typing import Any

from aiogram import Bot, Dispatcher
from aiogram.exceptions import (
    TelegramAPIError,
    TelegramBadRequest,
    TelegramForbiddenError,
    TelegramNetworkError,
    TelegramRetryAfter,
)
from aiogram.types import BufferedInputFile, ErrorEvent, Update

from config import settings
from core.text import esc, truncate

log = logging.getLogger("errors")

# Fragments of Bad Request messages that are expected and not worth a warning.
BENIGN_FRAGMENTS = (
    "message is not modified",
    "message to edit not found",
    "message to delete not found",
    "message can't be deleted",
    "message can't be edited",
    "message to be replied not found",
    "message to reply not found",
    "query is too old",
    "message thread not found",
    "topic_closed",
    "topic_deleted",
)

# The same error is reported at most once per this many seconds, so a failing
# loop cannot flood the log chat.
REPORT_COOLDOWN = 60

_last_reported: dict[str, float] = {}
_background_tasks: set[asyncio.Task] = set()

_EVENT_FIELDS = (
    "message",
    "edited_message",
    "channel_post",
    "edited_channel_post",
    "callback_query",
    "inline_query",
    "chosen_inline_result",
    "my_chat_member",
    "chat_member",
    "chat_join_request",
)


# --- Building the report ----------------------------------------------------

def _exception_name(exc: BaseException) -> str:
    cls = type(exc)
    if cls.__module__ == "builtins":
        return cls.__qualname__
    return f"{cls.__module__}.{cls.__qualname__}"


def _describe_context(update: Any) -> tuple[str, str]:
    """Return (chat, user) descriptions for an update, "N/A" when unknown."""
    if update is None:
        return "N/A", "N/A"

    event = next(
        (getattr(update, name) for name in _EVENT_FIELDS if getattr(update, name, None) is not None),
        None,
    )
    if event is None:
        return "N/A", "N/A"

    chat = getattr(event, "chat", None) or getattr(getattr(event, "message", None), "chat", None)
    user = getattr(event, "from_user", None)

    chat_text = "N/A"
    if chat is not None:
        chat_kind = getattr(chat.type, "value", chat.type)
        chat_text = f"{getattr(chat, 'title', None) or chat_kind} ({chat.id})"

    user_text = "N/A"
    if user is not None:
        username = getattr(user, "username", None)
        label = f"@{username}" if username else getattr(user, "full_name", None) or "user"
        user_text = f"{label} ({user.id})"

    return chat_text, user_text


def build_report(exc: BaseException, update: Any = None) -> tuple[str, str, bytes]:
    """Return (caption in HTML, file name, file content) for an error report."""
    now = datetime.now(timezone.utc)
    chat, user = _describe_context(update)

    message = str(exc).strip().splitlines()[0] if str(exc).strip() else ""
    error_line = f"{_exception_name(exc)}: {message}".rstrip()

    caption = (
        "<b>Bot Error Detected!</b>\n\n"
        f"Error: {esc(truncate(error_line, 300))}\n"
        f"Chat: {esc(truncate(chat, 100))}\n"
        f"User: {esc(truncate(user, 100))}\n\n"
        "Full traceback is attached as a file."
    )

    update_id = getattr(update, "update_id", None)
    header = (
        f"Time (UTC): {now:%Y-%m-%d %H:%M:%S}\n"
        f"Error: {error_line}\n"
        f"Update id: {update_id if update_id is not None else 'N/A'}\n"
        f"Chat: {chat}\n"
        f"User: {user}\n\n"
    )
    trace = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__))
    content = (header + trace).encode("utf-8", errors="replace")

    return caption, f"traceback_{now:%Y%m%d_%H%M%S}.txt", content


def _is_throttled(exc: BaseException) -> bool:
    key = f"{_exception_name(exc)}:{str(exc)[:100]}"
    now = time.monotonic()

    if len(_last_reported) > 100:
        for old_key in [k for k, t in _last_reported.items() if now - t > REPORT_COOLDOWN]:
            del _last_reported[old_key]

    last = _last_reported.get(key)
    if last is not None and now - last < REPORT_COOLDOWN:
        return True
    _last_reported[key] = now
    return False


async def report_exception(bot: Bot, exc: BaseException, update: Any = None) -> None:
    """Send the traceback as a .txt file to the log chat. Never raises."""
    try:
        if _is_throttled(exc):
            return

        caption, filename, content = build_report(exc, update)
        await bot.send_document(
            chat_id=settings.LOG_CHAT_ID or settings.OWNER_ID,
            document=BufferedInputFile(content, filename=filename),
            caption=caption,
            message_thread_id=settings.LOG_THREAD_ID,
        )
    except Exception as send_error:
        log.warning("Could not deliver the error report: %s", send_error)


# --- Dispatcher error handler -----------------------------------------------

async def on_error(event: ErrorEvent, bot: Bot) -> bool:
    exc = event.exception
    update_id = event.update.update_id
    text = getattr(exc, "message", None) or str(exc)

    if isinstance(exc, TelegramRetryAfter):
        log.warning("Rate limited (update %s): retry after %ss", update_id, exc.retry_after)
    elif isinstance(exc, TelegramForbiddenError):
        log.warning("Forbidden (update %s): %s", update_id, text)
    elif isinstance(exc, TelegramBadRequest):
        benign = any(fragment in text.lower() for fragment in BENIGN_FRAGMENTS)
        log.log(
            logging.DEBUG if benign else logging.WARNING,
            "Bad request (update %s): %s",
            update_id,
            text,
        )
    elif isinstance(exc, TelegramNetworkError):
        log.warning("Network error (update %s): %s", update_id, text)
    elif isinstance(exc, TelegramAPIError):
        log.warning("Telegram API error (update %s): %s", update_id, text)
    else:
        log.error("Unhandled exception while processing update %s", update_id, exc_info=exc)
        await report_exception(bot, exc, event.update)

    return True  # mark as handled so aiogram does not log it a second time


# --- Exceptions outside of handlers -----------------------------------------

def _make_loop_handler(bot: Bot):
    def handler(loop: asyncio.AbstractEventLoop, context: dict[str, Any]) -> None:
        # Keep the normal console output (message plus traceback).
        loop.default_exception_handler(context)

        exc = context.get("exception")
        if exc is None or isinstance(
            exc, (asyncio.CancelledError, KeyboardInterrupt, SystemExit, TelegramAPIError)
        ):
            return

        try:
            task = loop.create_task(report_exception(bot, exc))
        except RuntimeError:  # loop is shutting down
            return
        _background_tasks.add(task)
        task.add_done_callback(_background_tasks.discard)

    return handler


def setup_error_handling(dp: Dispatcher, bot: Bot) -> None:
    """Register the dispatcher error handler. Must be called from a running loop."""
    dp.errors.register(on_error)
    asyncio.get_running_loop().set_exception_handler(_make_loop_handler(bot))
