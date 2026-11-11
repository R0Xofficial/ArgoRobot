"""
Per-chat settings that belong to the chat's owner.

Each setting is one yes/no switch with a default, read on many updates and
therefore cached in memory:

  staff_help    whether the bot's staff (Owner, Developers, Sudo) may use the chat's
                admin commands: moderation, pins, promoting, settings. On by default.
                The chat's owner turns it off with /staffhelp (modules/admin.py); then
                the staff's commands are treated like those of any other member. The
                owner-only switches (this one, /gbans, /adminerror, /anonadmin, clearing
                everything, /export, /import) are never changed by staff.
  gban_enforce  whether the chat removes globally banned users (modules/gbans.py).
                On by default; the owner turns it off with /gbans.
  admin_errors  whether people who are not admins get an answer when they try an
                admin command. On by default; /adminerror.
  anon_admin    whether anonymous admins may use every command without their rights
                being checked (Telegram hides who they are). Off by default; /anonadmin.
  clean_commands  whether the bot deletes the commands people send after answering them
                (core/middlewares.py). Off by default; /cleanbluetext.
  reports       whether people may report messages to the admins (modules/reporting.py).
                On by default; /reports.
"""

from core import database
from core.users import CACHE_LIMIT

DEFAULTS = {
    "staff_help": True,
    "gban_enforce": True,
    "admin_errors": True,
    "anon_admin": False,
    "clean_commands": False,
    "reports": True,
}

_cache: dict[str, dict[int, bool]] = {name: {} for name in DEFAULTS}


async def _get(name: str, chat_id: int) -> bool:
    cache = _cache[name]
    cached = cache.get(chat_id)
    if cached is not None:
        return cached
    row = await database.fetch_one(f"SELECT {name} FROM chat_settings WHERE chat_id = $1", chat_id)
    value = row[name] if row else DEFAULTS[name]
    if len(cache) >= CACHE_LIMIT:
        cache.clear()
    cache[chat_id] = value
    return value


async def _set(name: str, chat_id: int, enabled: bool) -> None:
    await database.execute(
        f"INSERT INTO chat_settings (chat_id, {name}) VALUES ($1, $2) "
        f"ON CONFLICT (chat_id) DO UPDATE SET {name} = EXCLUDED.{name}",
        chat_id,
        enabled,
    )
    _cache[name][chat_id] = enabled


async def staff_help(chat_id: int) -> bool:
    return await _get("staff_help", chat_id)


async def set_staff_help(chat_id: int, enabled: bool) -> None:
    await _set("staff_help", chat_id, enabled)


async def gban_enforce(chat_id: int) -> bool:
    return await _get("gban_enforce", chat_id)


async def set_gban_enforce(chat_id: int, enabled: bool) -> None:
    await _set("gban_enforce", chat_id, enabled)


async def admin_errors(chat_id: int) -> bool:
    return await _get("admin_errors", chat_id)


async def set_admin_errors(chat_id: int, enabled: bool) -> None:
    await _set("admin_errors", chat_id, enabled)


async def anon_admin(chat_id: int) -> bool:
    return await _get("anon_admin", chat_id)


async def set_anon_admin(chat_id: int, enabled: bool) -> None:
    await _set("anon_admin", chat_id, enabled)


async def clean_commands(chat_id: int) -> bool:
    return await _get("clean_commands", chat_id)


async def set_clean_commands(chat_id: int, enabled: bool) -> None:
    await _set("clean_commands", chat_id, enabled)


async def reports(chat_id: int) -> bool:
    return await _get("reports", chat_id)


async def set_reports(chat_id: int, enabled: bool) -> None:
    await _set("reports", chat_id, enabled)


def forget(chat_id: int) -> None:
    """Drop the cached values (used when a chat changes its id)."""
    for cache in _cache.values():
        cache.pop(chat_id, None)
