"""
What a chat has switched off (modules/disabling.py). There are three levels:

  * /disable <command>: members who use it are ignored, the chat's admins can still use
    it. Stored under the command's first name, so every alias is covered.
  * /restrictmodule <module>: the same for every command of a module. Stored as
    "module:<name>".
  * /disablemodule <module>: the module is off in the chat completely, for admins as well:
    its commands stop and so does everything it does by itself (filters, locks, flood
    control, welcome messages ...). Stored as "off:<name>"; the gate in core/registry.py
    asks module_off() before a module's handlers run.

cmd() (core/filters.py) tells record() about every command when the modules are
imported. The CommandGuard is attached to every module's router by core/registry.py
and runs just before a matching handler is called.
"""

import logging
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware

from core import database
from core.users import CACHE_LIMIT

log = logging.getLogger(__name__)

GROUP_TYPES = ("group", "supergroup")
ANONYMOUS_ADMIN = 1087968824  # the user Telegram shows for anonymous admins

# Never switched off: they are how people get out of trouble.
PROTECTED = frozenset({
    "disable", "enable", "disabled", "restrictmodule", "unrestrictmodule", "disablemodule", "enablemodule",
    "help", "start", "connect", "disconnect", "connection",
})

# Modules a chat can never turn off, besides the protected ones: /help and /start live there.
ALWAYS_ON = frozenset({"misc"})
# What "/disablemodule all" leaves running: the chats that only want global bans keep them.
KEEP_BY_ALL = frozenset({"gbans"})

# every name a command answers to -> (its first name, the module it belongs to)
COMMANDS: dict[str, tuple[str, str]] = {}

_cache: dict[int, frozenset[str]] = {}


def record(names: tuple[str, ...], module: str) -> None:
    """Remember a command and its aliases (called by cmd() while modules are imported)."""
    if not names:
        return
    primary = names[0].lower()
    for name in names:
        COMMANDS.setdefault(name.lower(), (primary, module))


def lookup(name: str) -> tuple[str, str] | None:
    """(first name, module) of a command, or None when no module has it."""
    return COMMANDS.get(name.lstrip("/!").lower())


def module_key(module: str) -> str:
    return f"module:{module.lower()}"


def off_key(module: str) -> str:
    return f"off:{module.lower()}"


def commands_of(module: str) -> list[str]:
    """The first names of a module's commands that can be switched off, sorted."""
    return sorted({p for p, m in COMMANDS.values() if m == module.lower() and p not in PROTECTED})


def aliases_of(primary: str) -> list[str]:
    return sorted(name for name, (p, _m) in COMMANDS.items() if p == primary and name != primary)


async def get(chat_id: int) -> frozenset[str]:
    cached = _cache.get(chat_id)
    if cached is not None:
        return cached
    rows = await database.fetch_all("SELECT command FROM disabled_commands WHERE chat_id = $1", chat_id)
    found = frozenset(row["command"] for row in rows)
    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[chat_id] = found
    return found


async def add(chat_id: int, key: str) -> bool:
    """Switch something off. False when it already was."""
    current = await get(chat_id)
    if key in current:
        return False
    await database.execute(
        "INSERT INTO disabled_commands (chat_id, command) VALUES ($1, $2) ON CONFLICT DO NOTHING", chat_id, key
    )
    _cache[chat_id] = current | {key}
    return True


async def remove(chat_id: int, key: str) -> bool:
    """Switch something back on. False when it was not off."""
    current = await get(chat_id)
    if key not in current:
        return False
    await database.execute("DELETE FROM disabled_commands WHERE chat_id = $1 AND command = $2", chat_id, key)
    _cache[chat_id] = current - {key}
    return True


def forget(chat_id: int) -> None:
    """Drop the cached switches (a chat changed its id)."""
    _cache.pop(chat_id, None)


async def module_off(chat_id: int, module: str) -> bool:
    """Whether the chat turned this whole module off. Never raises: a broken lookup means "on"."""
    try:
        return off_key(module) in await get(chat_id)
    except Exception:
        log.exception("Looking up switched-off modules failed")
        return False


async def blocks(message: Any, name: str, module: str) -> bool:
    """Whether this member's use of /name in this chat is switched off."""
    entry = lookup(name)
    if entry is None or entry[0] in PROTECTED:
        return False
    primary = entry[0]
    off = await get(message.chat.id)
    if primary not in off and module_key(module) not in off:
        return False

    user = message.from_user
    if getattr(message, "sender_chat", None) is not None or (user is not None and user.id == ANONYMOUS_ADMIN):
        return False  # anonymous admins and channels posting as the chat
    if user is None:
        return True
    from core import moderation  # imported late: moderation needs a lot of the core

    return not await moderation.is_chat_admin(message.bot, message.chat.id, user.id)


class CommandGuard(BaseMiddleware):
    """
    Inner middleware of a module's router: runs after a command handler has matched and
    before it is called, and stops it when the chat switched that command off.
    """

    def __init__(self, module: str) -> None:
        self.module = module.lower()

    async def __call__(self, handler: Callable[[Any, dict[str, Any]], Awaitable[Any]], event: Any, data: dict[str, Any]) -> Any:
        command = data.get("command")
        chat = getattr(event, "chat", None)
        name = getattr(command, "command", None)
        if name and chat is not None and getattr(chat, "type", None) in GROUP_TYPES:
            try:
                if await blocks(event, name, self.module):
                    return None
            except Exception:
                log.exception("Checking switched-off commands failed")  # never lock a chat out because of a bug
        return await handler(event, data)
