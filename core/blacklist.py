"""
Blacklists: words and phrases a chat does not allow (the commands and the action taken
are in modules/blacklists.py).

A trigger is text with an optional modifier and wildcards:

    word              the word or phrase anywhere in a message, as whole words
    exact:word        the whole message is word
    prefix:word       the message starts with word
    sticker:😀         a sticker with this emoji
    stickerpack:name  any sticker of this pack

    ?    any one character except whitespace
    *    any run of characters except whitespace (great for URLs)
    **   any run of characters, whitespace included
    \\*  \\?  a literal * or ?

Matching ignores case. Every message of every chat is checked, so each chat's
triggers are compiled once and kept in memory; a chat without triggers costs nothing.
"""

import re
from dataclasses import dataclass
from typing import Any, Iterator

from core import database
from core.users import CACHE_LIMIT

MAX_TRIGGERS = 500
MAX_PATTERN = 100
MAX_WILDCARDS = 4        # wildcard runs in one trigger
MAX_LONG_WILDCARDS = 2   # of which ** (they can match anything, so they are the slow ones)
MAX_MATCHED_TEXT = 4096

ACTIONS = ("del", "warn", "ban", "kick", "mute", "tban", "tmute")
TIMED = ("tban", "tmute")
ALIASES = {"delete": "del", "off": "off"}
MODIFIERS = ("exact", "prefix", "sticker", "stickerpack")

DEFAULT_SETTINGS: dict[str, Any] = {"mode": "del", "mode_seconds": 0, "delete_matches": True}


class TriggerError(ValueError):
    """The trigger text is not acceptable; the message says why."""


@dataclass(frozen=True)
class Trigger:
    pattern: str            # as stored
    kind: str               # plain / exact / prefix / sticker / stickerpack
    body: str
    regex: re.Pattern | None
    action: str | None
    seconds: int
    reason: str | None


# --- Building triggers --------------------------------------------------------

def normalize(text: str) -> str:
    """Trim, lowercase, single spaces. The stored form, and what removal compares."""
    return " ".join(text.split()).lower()


def split_modifier(pattern: str) -> tuple[str, str]:
    head, sep, rest = pattern.partition(":")
    if sep and head in MODIFIERS:
        return head, rest.strip()
    return "plain", pattern


def check(text: str) -> str:
    """Validate a trigger as typed and return its stored form. Raises TriggerError."""
    pattern = normalize(text)
    if not pattern:
        raise TriggerError("The trigger is empty.")
    if len(pattern) > MAX_PATTERN:
        raise TriggerError(f"A trigger can be at most {MAX_PATTERN} characters.")
    kind, body = split_modifier(pattern)
    if not body:
        raise TriggerError(f"{kind}: needs something after it.")
    if kind == "stickerpack":
        body = body.rstrip("/").rsplit("/", 1)[-1]  # accept t.me/addstickers/name
        if not re.fullmatch(r"\w{1,64}", body):
            raise TriggerError("A sticker pack name has only letters, digits and underscores.")
        return f"stickerpack:{body}"
    if kind == "sticker":
        if any(ch.isspace() for ch in body):
            raise TriggerError("sticker: takes one emoji.")
        return f"sticker:{body}"
    _glob(body)  # raises if the wildcards are too many
    return f"{kind}:{body}" if kind != "plain" else body


def _glob(body: str) -> str:
    """Wildcard text to a regular expression."""
    out: list[str] = []
    runs = long_runs = 0
    i = 0
    while i < len(body):
        ch = body[i]
        if ch == "\\" and i + 1 < len(body) and body[i + 1] in "*?":
            out.append(re.escape(body[i + 1]))
            i += 2
        elif body.startswith("**", i):
            out.append(".*")
            runs += 1
            long_runs += 1
            i += 2
        elif ch == "*":
            out.append(r"\S*")
            runs += 1
            i += 1
        elif ch == "?":
            out.append(r"\S")
            runs += 1
            i += 1
        elif ch.isspace():
            out.append(r"\s+")
            i += 1
        else:
            out.append(re.escape(ch))
            i += 1
    if runs > MAX_WILDCARDS or long_runs > MAX_LONG_WILDCARDS:
        raise TriggerError(
            f"Too many wildcards: at most {MAX_WILDCARDS}, and at most {MAX_LONG_WILDCARDS} of them **."
        )
    return "".join(out)


def escape_wildcards(text: str) -> str:
    """Text to be matched literally (a replied message): its * and ? lose their meaning."""
    return re.sub(r"([*?])", r"\\\1", text)


def build(pattern: str, action: str | None, seconds: int, reason: str | None) -> Trigger:
    kind, body = split_modifier(pattern)
    regex = None
    if kind == "plain":
        regex = re.compile(r"(?<!\w)" + _glob(body) + r"(?!\w)", re.IGNORECASE | re.DOTALL)
    elif kind == "exact":
        regex = re.compile(r"\A" + _glob(body) + r"\Z", re.IGNORECASE | re.DOTALL)
    elif kind == "prefix":
        regex = re.compile(r"\A" + _glob(body), re.IGNORECASE | re.DOTALL)
    return Trigger(pattern, kind, body, regex, action, seconds, reason)


def iter_matches(triggers: list[Trigger], text: str | None, sticker: Any = None) -> Iterator[Trigger]:
    """The triggers that a message (its text or caption, or its sticker) matches, in order."""
    text = (text or "").strip()[:MAX_MATCHED_TEXT]
    emoji = getattr(sticker, "emoji", None) if sticker is not None else None
    pack = (getattr(sticker, "set_name", None) or "").lower() if sticker is not None else ""
    for trigger in triggers:
        if trigger.kind == "sticker":
            matched = sticker is not None and emoji == trigger.body
        elif trigger.kind == "stickerpack":
            matched = sticker is not None and bool(pack) and pack == trigger.body
        else:
            matched = bool(text) and trigger.regex.search(text) is not None
        if matched:
            yield trigger


def find(triggers: list[Trigger], text: str | None, sticker: Any = None) -> Trigger | None:
    return next(iter_matches(triggers, text, sticker), None)


# --- Storage --------------------------------------------------------------------

_triggers: dict[int, list[Trigger]] = {}
_settings: dict[int, dict[str, Any]] = {}


async def triggers(chat_id: int) -> list[Trigger]:
    cached = _triggers.get(chat_id)
    if cached is not None:
        return cached
    rows = await database.fetch_all(
        "SELECT pattern, action, action_seconds, reason FROM blacklist WHERE chat_id = $1 ORDER BY id", chat_id
    )
    built = [build(r["pattern"], r["action"], r["action_seconds"], r["reason"]) for r in rows]
    if len(_triggers) >= CACHE_LIMIT:
        _triggers.clear()
    _triggers[chat_id] = built
    return built


async def add(
    chat_id: int, pattern: str, action: str | None, seconds: int, reason: str | None, added_by: int | None
) -> bool | None:
    """
    Add a trigger, or change the action and reason of an existing one.
    Returns True if it is new, False if it was updated, None if the chat is full.
    """
    current = await triggers(chat_id)
    known = any(t.pattern == pattern for t in current)
    if not known and len(current) >= MAX_TRIGGERS:
        return None
    build(pattern, action, seconds, reason)  # refuse what can't be compiled before saving
    await database.execute(
        "INSERT INTO blacklist (chat_id, pattern, action, action_seconds, reason, added_by) "
        "VALUES ($1, $2, $3, $4, $5, $6) "
        "ON CONFLICT (chat_id, pattern) DO UPDATE SET action = EXCLUDED.action, "
        "action_seconds = EXCLUDED.action_seconds, reason = EXCLUDED.reason",
        chat_id, pattern, action, seconds, reason, added_by,
    )
    _triggers.pop(chat_id, None)
    return not known


async def remove(chat_id: int, pattern: str) -> bool:
    row = await database.fetch_one(
        "DELETE FROM blacklist WHERE chat_id = $1 AND pattern = $2 RETURNING pattern", chat_id, pattern
    )
    _triggers.pop(chat_id, None)
    return row is not None


async def clear(chat_id: int) -> int:
    rows = await database.fetch_all("DELETE FROM blacklist WHERE chat_id = $1 RETURNING pattern", chat_id)
    _triggers.pop(chat_id, None)
    return len(rows)


async def settings(chat_id: int) -> dict[str, Any]:
    cached = _settings.get(chat_id)
    if cached is not None:
        return cached
    row = await database.fetch_one(
        "SELECT mode, mode_seconds, delete_matches FROM blacklist_settings WHERE chat_id = $1", chat_id
    )
    value = {**DEFAULT_SETTINGS, **(row or {})}
    if len(_settings) >= CACHE_LIMIT:
        _settings.clear()
    _settings[chat_id] = value
    return value


async def update_settings(chat_id: int, **fields: Any) -> dict[str, Any]:
    if not fields or not set(fields) <= set(DEFAULT_SETTINGS):
        raise ValueError(f"unknown blacklist setting in {sorted(fields)}")
    names = list(fields)
    await database.execute(
        f"INSERT INTO blacklist_settings (chat_id, {', '.join(names)}) "
        f"VALUES ($1, {', '.join(f'${i + 2}' for i in range(len(names)))}) "
        f"ON CONFLICT (chat_id) DO UPDATE SET {', '.join(f'{n} = EXCLUDED.{n}' for n in names)}",
        chat_id, *fields.values(),
    )
    merged = {**await settings(chat_id), **fields}
    _settings[chat_id] = merged
    return merged


def forget(chat_id: int) -> None:
    _triggers.pop(chat_id, None)
    _settings.pop(chat_id, None)
