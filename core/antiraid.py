"""
Antiraid: when many accounts join a chat within a short time, the chat is in a raid for a while
and every newcomer is dealt with (muted until the raid is over, kicked or banned).

Off by default: it is up to the chat's admins. Settings per chat (modules/antiraid.py has the
commands): how many joins in how many seconds make a raid, how long the raid lasts, and what
happens to the newcomers. The people who joined in the window that started the raid are dealt
with as well, they are the raid.

Joins are counted in memory (a restart forgets the last minute, which costs nothing); the end
of a running raid is kept in the database so a restart does not end it early.
"""

import time
from collections import deque
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any

from core import approvals, database, enforce, immunity, roles
from core.users import CACHE_LIMIT

DEFAULTS = {"joins": 10, "window": 60, "duration": 3600, "mode": "mute"}
MODES = ("mute", "kick", "ban")

MIN_JOINS, MAX_JOINS = 3, 100
MIN_WINDOW, MAX_WINDOW = 10, 3600
MIN_DURATION, MAX_DURATION = 60, 7 * 86400


@dataclass(frozen=True)
class Settings:
    enabled: bool = False
    joins: int = DEFAULTS["joins"]
    window: int = DEFAULTS["window"]
    duration: int = DEFAULTS["duration"]
    mode: str = DEFAULTS["mode"]
    until: float = 0.0  # unix time when the running raid ends, 0 when there is none


_cache: dict[int, Settings] = {}
_recent: dict[int, deque] = {}  # chat -> (time, user id) of the latest joins
_seen: dict[tuple[int, int], float] = {}  # the same join can arrive twice (service message and update)
_removed: dict[tuple[int, int], float] = {}  # who the raid took care of, for the modules that greet or test newcomers
SEEN_FOR = 60.0
REMOVED_FOR = 30.0


# --- Storage -------------------------------------------------------------------------------

async def get(chat_id: int) -> Settings:
    cached = _cache.get(chat_id)
    if cached is not None:
        return cached
    row = await database.fetch_one(
        "SELECT enabled, joins, window_seconds, duration_seconds, mode, EXTRACT(EPOCH FROM raid_until) AS until "
        "FROM antiraid WHERE chat_id = $1",
        chat_id,
    )
    built = Settings() if row is None else Settings(
        bool(row["enabled"]), row["joins"], row["window_seconds"], row["duration_seconds"], row["mode"], float(row["until"] or 0.0)
    )
    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[chat_id] = built
    return built


def forget(chat_id: int) -> None:
    _cache.pop(chat_id, None)


_COLUMNS = {"enabled": "enabled", "joins": "joins", "window": "window_seconds", "duration": "duration_seconds", "mode": "mode"}


async def update(chat_id: int, **values: Any) -> None:
    """Change settings: enabled, joins, window, duration, mode."""
    current = await get(chat_id)
    merged = {name: values.get(name, getattr(current, name)) for name in _COLUMNS}
    await database.execute(
        "INSERT INTO antiraid (chat_id, enabled, joins, window_seconds, duration_seconds, mode) "
        "VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (chat_id) DO UPDATE SET enabled = EXCLUDED.enabled, "
        "joins = EXCLUDED.joins, window_seconds = EXCLUDED.window_seconds, "
        "duration_seconds = EXCLUDED.duration_seconds, mode = EXCLUDED.mode",
        chat_id, merged["enabled"], merged["joins"], merged["window"], merged["duration"], merged["mode"],
    )
    forget(chat_id)
    if not merged["enabled"]:
        _recent.pop(chat_id, None)


async def set_until(chat_id: int, until: float) -> None:
    """Start (or, with 0, end) a raid."""
    await database.execute(
        "INSERT INTO antiraid (chat_id, raid_until) VALUES ($1, $2) ON CONFLICT (chat_id) DO UPDATE SET raid_until = EXCLUDED.raid_until",
        chat_id,
        datetime.fromtimestamp(until, timezone.utc) if until else None,
    )
    forget(chat_id)


def raging(settings: Settings, now: float | None = None) -> bool:
    return settings.enabled and settings.until > (time.time() if now is None else now)


# --- Joins -------------------------------------------------------------------------------------

def was_removed(chat_id: int, user_id: int) -> bool:
    """Whether the raid has just dealt with this newcomer (there is nobody left to greet or test)."""
    stamp = _removed.get((chat_id, user_id))
    return stamp is not None and time.monotonic() - stamp < REMOVED_FOR


def _mark_removed(chat_id: int, user_id: int) -> None:
    if len(_removed) > 5000:
        _removed.clear()
    _removed[(chat_id, user_id)] = time.monotonic()


def _first_sight(chat_id: int, user_id: int) -> bool:
    now = time.monotonic()
    if len(_seen) > 5000:
        _seen.clear()
    last = _seen.get((chat_id, user_id))
    _seen[(chat_id, user_id)] = now
    return last is None or now - last >= SEEN_FOR


async def _exempt(bot, chat_id: int, user) -> bool:
    from core import gbanguard, moderation

    return (
        user.is_bot
        or user.id == bot.id
        or immunity.is_immune(user.id)
        or await roles.get_role(user.id) is not None
        or await approvals.is_approved(chat_id, user.id)
        or await moderation.is_chat_admin(bot, chat_id, user.id)
        or await gbanguard.applies(bot, chat_id, user.id)  # being banned: a mute now would undo the ban
    )


async def _deal_with(bot, chat_id: int, user_id: int, settings: Settings) -> str:
    """What the raid does to a newcomer. Returns what was done ('muted for ...')."""
    if settings.mode == "mute":
        remaining = max(int(settings.until - time.time()), 60)
        done = await enforce.punish(bot, chat_id, user_id, "tmute", remaining)
    else:
        done = await enforce.punish(bot, chat_id, user_id, settings.mode)
    _mark_removed(chat_id, user_id)
    return done


async def joined(bot, chat, users: list) -> dict[str, Any] | None:
    """
    Newcomers arrived. Counts them, starts a raid when there are too many, and deals with the
    newcomers of a raid. Returns None when nothing happened, otherwise what happened:
    {"started": bool, "people": [user ids dealt with], "settings": Settings}.
    Errors of Telegram (no right to restrict) are raised to the caller.
    """
    settings = await get(chat.id)
    if not settings.enabled:
        return None
    people = [u for u in users if _first_sight(chat.id, u.id) and not await _exempt(bot, chat.id, u)]
    if not people:
        return None

    now = time.time()
    started = False
    victims = [u.id for u in people]
    if not raging(settings, now):
        recent = _recent.setdefault(chat.id, deque())
        while recent and now - recent[0][0] > settings.window:
            recent.popleft()
        for user in people:
            recent.append((now, user.id))
        if len(recent) < settings.joins:
            return None
        started = True
        victims = list(dict.fromkeys(uid for _, uid in recent))
        recent.clear()
        await set_until(chat.id, now + settings.duration)
        settings = await get(chat.id)

    done = []
    for user_id in victims:
        await _deal_with(bot, chat.id, user_id, settings)
        done.append(user_id)
    return {"started": started, "people": done, "settings": settings}


async def end(bot, chat_id: int) -> bool:
    """End a running raid. True when there was one."""
    settings = await get(chat_id)
    if not raging(settings):
        return False
    await set_until(chat_id, 0.0)
    return True
