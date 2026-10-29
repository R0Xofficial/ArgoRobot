"""
Extra lines for /info.

/info (modules/misc.py) shows what the bot knows about everyone: id, name, role
in the chat, role in the bot. Other modules add their own facts about a user
(Whitelisted, GBanned, federation roles ...) by registering a provider:

    from core import info_hooks

    async def _info_lines(bot, user_id, chat_id):
        if await is_gbanned(user_id):
            return ["<b>GBanned:</b> <code>Yes</code>"]
        return []

    info_hooks.register("gbans", _info_lines)

`chat_id` is the chat /info was used in (None in private chats), for facts that
belong to a chat, like approval. A provider returns ready-made HTML lines (escape anything that came from a user)
or an empty list. It is skipped while its module is disabled, and a provider that
fails never breaks /info.
"""

import logging
from typing import Awaitable, Callable

from aiogram import Bot

from core import registry

log = logging.getLogger(__name__)

Provider = Callable[[Bot, int, int | None], Awaitable[list[str]]]

_providers: list[tuple[str, Provider]] = []


def register(module_name: str, provider: Provider) -> None:
    """Add a provider; `module_name` is the module whose enabled state decides if it runs."""
    _providers.append((module_name, provider))


async def lines_for(bot: Bot, user_id: int, chat_id: int | None = None) -> list[str]:
    lines: list[str] = []
    for module_name, provider in _providers:
        info = registry.get(module_name)
        if info is not None and not info.enabled:
            continue
        try:
            lines.extend(await provider(bot, user_id, chat_id))
        except Exception:
            log.exception("Info provider of '%s' failed", module_name)
    return lines
