"""
Join blacklists: words that keep people out when their profile contains them
(the commands and the action are in modules/joinblacklists.py).

There are four kinds, one for each part of a profile that is checked: the name, the
@username, the bio and the personal channel. A chat has at most MAX_PER_KIND triggers
of each kind. Triggers use the wildcards of core/blacklist.py and match whole words
without caring about case:

    ?    any one character except whitespace
    *    any run of characters except whitespace (great for URLs)
    **   any run of characters, whitespace included

Every join in a chat with triggers is checked, so a chat's triggers are compiled once
and kept in memory.
"""

import re

from core import blacklist, database
from core.blacklist import TriggerError
from core.users import CACHE_LIMIT

KINDS = ("name", "username", "bio", "channel")
MAX_PER_KIND = 10
MAX_CHECKED_TEXT = 1000  # a bio is at most 140 characters; this only bounds odd input

_cache: dict[int, dict[str, list[tuple[str, re.Pattern]]]] = {}


def check(kind: str, text: str) -> str:
    """Validate a trigger as typed and return its stored form. Raises TriggerError."""
    pattern = blacklist.normalize(text)
    if kind == "username":
        pattern = pattern.lstrip("@")  # people write @name; usernames are checked without the @
    if not pattern:
        raise TriggerError("The trigger is empty.")
    if len(pattern) > blacklist.MAX_PATTERN:
        raise TriggerError(f"A trigger can be at most {blacklist.MAX_PATTERN} characters.")
    blacklist.word_regex(pattern)  # raises when the wildcards are too many
    return pattern


async def _load(chat_id: int) -> dict[str, list[tuple[str, re.Pattern]]]:
    cached = _cache.get(chat_id)
    if cached is not None:
        return cached
    rows = await database.fetch_all(
        "SELECT kind, pattern FROM join_blacklist WHERE chat_id = $1 ORDER BY kind, pattern", chat_id
    )
    built: dict[str, list[tuple[str, re.Pattern]]] = {kind: [] for kind in KINDS}
    for row in rows:
        if row["kind"] in built:
            built[row["kind"]].append((row["pattern"], blacklist.word_regex(row["pattern"])))
    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[chat_id] = built
    return built


async def patterns(chat_id: int) -> dict[str, list[str]]:
    """The stored triggers of a chat, by kind."""
    loaded = await _load(chat_id)
    return {kind: [pattern for pattern, _ in loaded[kind]] for kind in KINDS}


async def has(chat_id: int, kind: str) -> bool:
    return bool((await _load(chat_id))[kind])


async def add(chat_id: int, kind: str, pattern: str) -> str:
    """Add a trigger (as returned by check). Returns "added", "exists" or "full"."""
    current = (await _load(chat_id))[kind]
    if any(known == pattern for known, _ in current):
        return "exists"
    if len(current) >= MAX_PER_KIND:
        return "full"
    await database.execute(
        "INSERT INTO join_blacklist (chat_id, kind, pattern) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
        chat_id, kind, pattern,
    )
    _cache.pop(chat_id, None)
    return "added"


async def remove(chat_id: int, kind: str, pattern: str) -> bool:
    if not any(known == pattern for known, _ in (await _load(chat_id))[kind]):
        return False
    await database.execute(
        "DELETE FROM join_blacklist WHERE chat_id = $1 AND kind = $2 AND pattern = $3", chat_id, kind, pattern
    )
    _cache.pop(chat_id, None)
    return True


async def match(chat_id: int, kind: str, text: str | None) -> str | None:
    """The first trigger of this kind that `text` matches, or None."""
    text = (text or "").strip()[:MAX_CHECKED_TEXT]
    if not text:
        return None
    for pattern, regex in (await _load(chat_id))[kind]:
        if regex.search(text):
            return pattern
    return None


def forget(chat_id: int) -> None:
    _cache.pop(chat_id, None)
