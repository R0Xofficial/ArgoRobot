"""
Extra flags for /info.

/info (modules/misc.py) shows what the bot knows about everyone: a "User Info" section
(id, name, standing in the chat) and a section about what the bot has on them (role in the
bot and a list of yes/no flags). Modules add their own flags (Globally Banned,
Whitelisted ...) by registering a provider. The "Approved" flag is special: /info writes
it into the person's Status ("Member (Approved)") instead of listing it.

    from core import info_hooks

    async def _info_flags(bot, user_id, chat_id):
        return [("Globally Banned", await is_gbanned(user_id))]

    info_hooks.register("gbans", _info_flags)

A provider returns (name, value) pairs, always, so that /info can say "No" as well
as "Yes". `chat_id` is the chat /info was used in (None in private chats); a flag that
belongs to a chat, like Approved or FedBanned, is simply left out when there is none. /info decides
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

_providers: list[tuple[str, Provider, bool]] = []


def register(module_name: str, provider: Provider, *, always: bool = False) -> None:
    """
    Add a provider; `module_name` is the module whose enabled state decides if it runs.

    Flags are normally left out for admins and the bot's staff. A provider registered with
    always=True is shown for everyone (for facts about the person that an admin can have too).
    """
    _providers.append((module_name, provider, always))


async def flags_for(bot: Bot, user_id: int, chat_id: int | None = None, *, always: bool = False) -> list[tuple[str, bool]]:
    """The flags of the providers registered with the same `always` value."""
    flags: list[tuple[str, bool]] = []
    for module_name, provider, provider_always in _providers:
        if provider_always != always:
            continue
        info = registry.get(module_name)
        if info is not None and not info.enabled:
            continue
        try:
            flags.extend(await provider(bot, user_id, chat_id))
        except Exception:
            log.exception("Info provider of '%s' failed", module_name)
    return flags
