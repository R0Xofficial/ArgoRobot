"""
Extra flags for /info.

/info (modules/misc.py) shows what the bot knows about everyone: id, name, standing
in the chat, role in the bot, and a list of yes/no flags. Modules add their own flags
(GBanned, Whitelisted, Approved ...) by registering a provider:

    from core import info_hooks

    async def _info_flags(bot, user_id, chat_id):
        return [("GBanned", await is_gbanned(user_id))]

    info_hooks.register("gbans", _info_flags)

A provider returns (name, value) pairs, always, so that /info can say "No" as well
as "Yes". `chat_id` is the chat /info was used in (None in private chats); a flag that
belongs to a chat, like Approved, is simply left out when there is none. /info decides
whether to show flags at all (people with a role show none) and in which order
(misc.FLAG_ORDER). A provider is skipped while its module is disabled, and one that
fails never breaks /info.
"""

import logging
from typing import Awaitable, Callable

from aiogram import Bot

from core import registry

log = logging.getLogger(__name__)

Provider = Callable[[Bot, int, int | None], Awaitable[list[tuple[str, bool]]]]

_providers: list[tuple[str, Provider]] = []


def register(module_name: str, provider: Provider) -> None:
    """Add a provider; `module_name` is the module whose enabled state decides if it runs."""
    _providers.append((module_name, provider))


async def flags_for(bot: Bot, user_id: int, chat_id: int | None = None) -> list[tuple[str, bool]]:
    flags: list[tuple[str, bool]] = []
    for module_name, provider in _providers:
        info = registry.get(module_name)
        if info is not None and not info.enabled:
            continue
        try:
            flags.extend(await provider(bot, user_id, chat_id))
        except Exception:
            log.exception("Info provider of '%s' failed", module_name)
    return flags
