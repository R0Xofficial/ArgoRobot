"""
Outer middlewares attached to dp.update.

Order matters: aiogram's own user-context middleware runs first and puts
`event_from_user` into the data dict, which RolesMiddleware relies on.
"""

import logging
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware
from aiogram.types import Update

from core import roles

log = logging.getLogger("usage")


class LoggingMiddleware(BaseMiddleware):
    """Logs one console line per command or button press (never message text)."""

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        try:
            self._log(event)
        except Exception:
            # Logging must never break update processing.
            log.debug("Failed to log update", exc_info=True)
        return await handler(event, data)

    @staticmethod
    def _log(event: Update) -> None:
        message = event.message
        if message and message.text and message.text.startswith("/"):
            user = message.from_user
            thread = f" thread={message.message_thread_id}" if message.is_topic_message else ""
            log.info(
                "command=%s user=%s (@%s) chat=%s [%s]%s",
                message.text.split()[0],
                user.id if user else "?",
                user.username if user else "-",
                message.chat.id,
                message.chat.type,
                thread,
            )
            return

        callback = event.callback_query
        if callback:
            log.info(
                "callback=%s user=%s (@%s)",
                callback.data,
                callback.from_user.id,
                callback.from_user.username,
            )


class RolesMiddleware(BaseMiddleware):
    """Adds `role` (Role | None) to the handler data."""

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        user = data.get("event_from_user")
        data["role"] = await roles.get_role(user.id) if user else None
        return await handler(event, data)
