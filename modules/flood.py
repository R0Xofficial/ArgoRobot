"""
Antiflood: settings of a chat and the counting of messages (the commands and the
action taken are in modules/antiflood.py).

Two things can trigger flood control, and a chat can use either or both:

  * the limit   more than N messages in a row from one person (nobody else wrote
                in between). On by default, N = 10.
  * the timer   N messages from one person within some seconds, whoever else writes.
                Off by default.

Counting happens in memory only: it is about the last few seconds, and losing it
on a restart costs nothing. The settings are in the database and cached.
"""

from collections import deque
from typing import Any

from core import database
from core.enforce import MODES, TIMED_MODES  # noqa: F401  (used by modules/antiflood.py)
from core.users import CACHE_LIMIT

DEFAULTS: dict[str, Any] = {
    "flood_limit": 10,
    "timer_count": 0,
    "timer_seconds": 0,
    "mode": "mute",
    "mode_seconds": 0,
    "clear_flood": False,
    "antichannel": False,
}
COLUMNS = frozenset(DEFAULTS)


MIN_COUNT, MAX_COUNT = 2, 100
MIN_SECONDS, MAX_SECONDS = 1, 24 * 3600
MAX_TRACKED_IDS = 100  # messages remembered for /clearflood (delete_messages takes at most 100)

_settings: dict[int, dict[str, Any]] = {}


# --- Settings -------------------------------------------------------------------

async def get(chat_id: int) -> dict[str, Any]:
    cached = _settings.get(chat_id)
    if cached is not None:
        return cached
    row = await database.fetch_one(
        "SELECT flood_limit, timer_count, timer_seconds, mode, mode_seconds, clear_flood, antichannel "
        "FROM flood_settings WHERE chat_id = $1",
        chat_id,
    )
    value = {**DEFAULTS, **(row or {})}
    if len(_settings) >= CACHE_LIMIT:
        _settings.clear()
    _settings[chat_id] = value
    return value


async def update(chat_id: int, **fields: Any) -> dict[str, Any]:
    """Change some settings of a chat. Returns all of them."""
    if not fields or not set(fields) <= COLUMNS:
        raise ValueError(f"unknown flood setting in {sorted(fields)}")
    names = list(fields)
    await database.execute(
        f"INSERT INTO flood_settings (chat_id, {', '.join(names)}) "
        f"VALUES ($1, {', '.join(f'${i + 2}' for i in range(len(names)))}) "
        f"ON CONFLICT (chat_id) DO UPDATE SET {', '.join(f'{n} = EXCLUDED.{n}' for n in names)}",
        chat_id,
        *fields.values(),
    )
    merged = {**await get(chat_id), **fields}
    _settings[chat_id] = merged
    return merged


def active(settings: dict[str, Any]) -> bool:
    """Whether the chat counts messages at all."""
    return bool(settings["flood_limit"] or settings["timer_count"])


# --- Counting -------------------------------------------------------------------

class _Run:
    """The latest unbroken series of messages in a chat from one person."""

    __slots__ = ("user_id", "count", "ids", "group")

    def __init__(self, user_id: int) -> None:
        self.user_id = user_id
        self.count = 0
        self.ids: list[int] = []
        self.group: str | None = None  # the album the last message belonged to


_runs: dict[int, _Run] = {}
_timed: dict[tuple[int, int], deque] = {}


def record(
    settings: dict[str, Any],
    chat_id: int,
    user_id: int,
    message_id: int,
    now: float,
    media_group: str | None = None,
) -> list[int] | None:
    """
    Count a message. Returns the ids of the messages that make up the flood when a
    limit was reached (and starts counting again), otherwise None.

    An album counts as one message, however many photos it holds.
    """
    run = _runs.get(chat_id)
    if run is None or run.user_id != user_id:
        if len(_runs) >= CACHE_LIMIT:
            _runs.clear()
        run = _runs[chat_id] = _Run(user_id)
    same_album = media_group is not None and run.group == media_group
    run.group = media_group
    if not same_album:
        run.count += 1
    run.ids.append(message_id)
    del run.ids[:-MAX_TRACKED_IDS]

    ids: list[int] | None = None
    if settings["flood_limit"] and run.count > settings["flood_limit"]:
        ids = list(run.ids)

    if settings["timer_count"]:
        key = (chat_id, user_id)
        seen = _timed.get(key)
        if seen is None:
            if len(_timed) >= CACHE_LIMIT:
                _timed.clear()
            seen = _timed[key] = deque(maxlen=MAX_TRACKED_IDS)
        seen.append((now, message_id, media_group))  # an album's photos share one group key
        while seen and now - seen[0][0] > settings["timer_seconds"]:
            seen.popleft()
        counted = len({item[2] if item[2] is not None else item[1] for item in seen})
        if counted >= settings["timer_count"] and ids is None:
            ids = [item[1] for item in seen]

    if ids is not None:
        reset(chat_id, user_id)
    return ids


def reset(chat_id: int, user_id: int) -> None:
    """Start counting a person again (after an action, or when they are exempt)."""
    run = _runs.get(chat_id)
    if run is not None and run.user_id == user_id:
        del _runs[chat_id]
    _timed.pop((chat_id, user_id), None)


def forget(chat_id: int) -> None:
    _settings.pop(chat_id, None)
    _runs.pop(chat_id, None)
    for key in [key for key in _timed if key[0] == chat_id]:
        del _timed[key]
