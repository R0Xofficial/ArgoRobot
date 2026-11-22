"""
Outer middlewares attached to dp.update.

Order matters: aiogram's own user-context middleware runs first and puts
`event_from_user` into the data dict, which RolesMiddleware relies on.
"""

import asyncio
import logging
from contextlib import suppress
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware
from aiogram.dispatcher.event.bases import UNHANDLED
from aiogram.exceptions import TelegramAPIError
from aiogram.types import Update

from config import settings
from core.text import esc
from core import chatsettings, chats, connection, devnotify, disabled, greetings, i18n, registry, roles, users
from core.i18n import tr

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
        if message and message.text and message.text.startswith(tuple(settings.COMMAND_PREFIXES)):
            user = message.from_user
            thread = f" thread={message.message_thread_id}" if message.is_topic_message else ""
            # The log line carries the module that owns the command: "... | modules.gbans | command=/fban ..."
            word = message.text.split()[0]
            found = disabled.lookup(word.split("@")[0])
            logger = logging.getLogger(f"modules.{found[1]}") if found and found[1] else log
            logger.info(
                "command=%s user=%s (@%s) chat=%s [%s]%s",
                word,
                user.id if user else "?",
                user.username if user else "-",
                message.chat.id,
                getattr(message.chat.type, "value", message.chat.type),
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


class LanguageMiddleware(BaseMiddleware):
    """Sets the language of the bot's texts (core/i18n.py) to that of the chat the update comes from."""

    @staticmethod
    def chat_id(event: Update) -> int | None:
        for name in ("message", "edited_message", "my_chat_member", "chat_member", "chat_join_request", "message_reaction"):
            found = getattr(event, name, None)
            chat = getattr(found, "chat", None)
            if chat is not None:
                return chat.id
        callback = event.callback_query
        if callback is not None:
            chat = getattr(callback.message, "chat", None)
            return chat.id if chat is not None else callback.from_user.id
        return None

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        user = data.get("event_from_user")
        chat_id = self.chat_id(event)
        await i18n.use_chat(chat_id if chat_id is not None else (user.id if user else None))
        return await handler(event, data)


class UsersMiddleware(BaseMiddleware):
    """Keeps the user phone book (core/users.py) up to date."""

    TIMEOUT = 5  # seconds; a slow database must not hold up the update

    @staticmethod
    async def _record(event: Update) -> None:
        await users.remember_all(users.collect(event))
        await chats.record(event)  # chats the bot is in, and who was seen where

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        try:
            await asyncio.wait_for(self._record(event), self.TIMEOUT)
        except Exception as exc:
            # The book is a convenience, never a reason to drop an update.
            users.log.warning("Could not update the user registry: %r", exc)

        bot = data.get("bot")
        if bot is not None:
            # Just added to (or promoted in) a group: look at its admins right away.
            change = getattr(event, "my_chat_member", None)
            joined = change is not None
            if change is not None:
                try:
                    devnotify.chat_event(bot, change)
                except Exception:
                    users.log.exception("Could not announce a chat change")
            for chat_id in users.group_chats(event):
                users.schedule_admin_refresh(bot, chat_id, force=joined)
        return await handler(event, data)


class CleanCommandsMiddleware(BaseMiddleware):
    """
    Deletes the commands people sent once the bot has dealt with them, in chats whose
    admins turned that on with /cleanbluetext (modules/admin.py).

    Only commands the bot handled are removed, and only those meant for this bot:
    /cmd@otherbot is left alone.
    """

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        result = await handler(event, data)
        message = event.message
        if result is UNHANDLED or message is None:
            return result
        try:
            if await self._should_delete(message, data.get("bot")):
                await message.delete()
        except TelegramAPIError:
            pass  # no right to delete, or already gone: nothing to do
        except Exception:
            log.debug("Failed to clean a command", exc_info=True)
        return result

    @staticmethod
    async def _should_delete(message, bot) -> bool:
        info = registry.get("admin")
        if info is not None and not info.enabled:
            return False
        if message.chat.type not in ("group", "supergroup") or not message.text:
            return False
        word = message.text.split(maxsplit=1)[0]
        if not word.startswith(tuple(settings.COMMAND_PREFIXES)) or len(word) < 2:
            return False
        _, _, addressed = word.partition("@")
        if addressed and bot is not None and addressed.lower() != ((await bot.me()).username or "").lower():
            return False
        return await chatsettings.clean_commands(message.chat.id)


class CleanServiceMiddleware(BaseMiddleware):
    """
    Deletes the service messages ("X joined", "X pinned a message", a new chat photo...) of the kinds
    a chat chose with /cleanservice (modules/greetings.py). It runs after the other handlers, so that
    a welcome or a ban still sees the message it answers.
    """

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        try:
            return await handler(event, data)
        finally:
            message = event.message
            if message is not None:
                try:
                    if await self._should_delete(message):
                        await message.delete()
                except TelegramAPIError:
                    pass  # no right to delete, or already gone: nothing to do
                except Exception:
                    log.debug("Failed to clean a service message", exc_info=True)

    @staticmethod
    async def _should_delete(message) -> bool:
        if message.chat.type not in ("group", "supergroup"):
            return False
        kind = greetings.service_kind(message)
        if kind is None:
            return False
        info = registry.get("greetings")
        if info is not None and not info.enabled:
            return False
        if await disabled.module_off(message.chat.id, "greetings"):
            return False
        return kind in greetings.clean_types(await greetings.get(message.chat.id))


class ConnectionMiddleware(BaseMiddleware):
    """
    Runs a connected admin's group commands, sent in a private chat, for the group
    (see core/connection.py). Everything else passes through untouched.
    """

    async def __call__(
        self,
        handler: Callable[[Update, dict[str, Any]], Awaitable[Any]],
        event: Update,
        data: dict[str, Any],
    ) -> Any:
        message = event.message
        try:
            record = await self._record(message, data.get("bot"))
        except Exception:
            log.debug("Looking up a connection failed", exc_info=True)
            record = None
        if record is None:
            return await handler(event, data)

        token = connection.origin.set(message.chat.id)
        try:
            return await handler(event.model_copy(update={"message": connection.rewrite(message, record)}), data)
        finally:
            connection.origin.reset(token)

    @staticmethod
    async def _record(message, bot) -> dict[str, Any] | None:
        if message is None or message.chat.type != "private" or message.from_user is None or not message.text:
            return None
        username = (await bot.me()).username if bot is not None else None
        name = connection.command_name(message.text, settings.COMMAND_PREFIXES, username)
        if name is None or not connection.is_remote(name):
            return None
        record = await connection.get(message.from_user.id)
        if record is None:
            return None
        why = await connection.switched_off(record["chat_id"])
        if why:
            # Connections were turned off while this one was open: it ends by itself.
            await connection.clear(message.from_user.id)
            with suppress(TelegramAPIError):
                await bot.send_message(
                    chat_id=message.chat.id,
                    text=tr("middlewares.your_connection_to_has_ended", why=why, v=esc(record['title'] or str(record['chat_id']))),
                )
            return None
        return record
