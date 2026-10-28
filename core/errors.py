"""
Global error handling.

Telegram regularly produces errors that are harmless for a bot: an edited or
deleted command, a message that is already gone, an expired callback query, a
closed topic. These are logged as one short line (or only at DEBUG) instead of
a full traceback. Only genuinely unexpected exceptions get a traceback.
"""

import logging

from aiogram import Dispatcher
from aiogram.exceptions import (
    TelegramAPIError,
    TelegramBadRequest,
    TelegramForbiddenError,
    TelegramNetworkError,
    TelegramRetryAfter,
)
from aiogram.types import ErrorEvent

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


async def on_error(event: ErrorEvent) -> bool:
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

    return True  # mark as handled so aiogram does not log it a second time


def setup_error_handling(dp: Dispatcher) -> None:
    dp.errors.register(on_error)
