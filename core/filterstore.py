"""
Filters: saved replies to keywords (the commands and the replying are in modules/filters.py).

A chat has any number of filters. Each one is a trigger and a saved message (text in the
Markdown of core/markup.py, optionally with a photo, GIF, sticker ...). A trigger is:

    word                  the word anywhere in a message, as a whole word
    "a long sentence"     a phrase, in double quotes
    prefix:word           messages that start with word
    exact:word            messages that are exactly word
    (a, b, "c d")         any of several triggers sharing one reply

Matching ignores case. Every message of every chat is checked, so each chat's triggers
are compiled once and kept in memory; the saved messages stay in the database until a
trigger matches.
"""

import re
from dataclasses import dataclass
from typing import Any

from core import database
from core.users import CACHE_LIMIT

MAX_FILTERS = 300
MAX_PATTERN = 100
MAX_GROUP = 20
MAX_CONTENT = 4000

MODIFIERS = ("prefix", "exact")
_OPENING = '"“'
_CLOSING = {'"': '"', "“": "”"}


class FilterError(ValueError):
    """The command can't be understood; the message says why."""


# --- Reading triggers -------------------------------------------------------------------

def _normalize(text: str) -> str:
    return " ".join(text.split()).lower()


def _read_one(text: str) -> tuple[str, str]:
    """One trigger at the start of `text`: (stored pattern, the rest of the text)."""
    text = text.lstrip()
    modifier = ""
    lowered = text.lower()
    for name in MODIFIERS:
        if lowered.startswith(name + ":"):
            modifier = name + ":"
            text = text[len(modifier):]
            break
    if text[:1] in _OPENING and text[:1]:
        closing = _CLOSING[text[0]]
        end = text.find(closing, 1)
        if end == -1:
            raise FilterError("A quote is not closed. Close the phrase with another double quote.")
        body, rest = text[1:end], text[end + 1:]
    else:
        parts = text.split(None, 1)
        body = parts[0] if parts else ""
        rest = text[len(body):]
    body = _normalize(body)
    if not body:
        raise FilterError("The keyword is empty.")
    pattern = modifier + body
    if len(pattern) > MAX_PATTERN:
        raise FilterError(f"A keyword can be at most {MAX_PATTERN} characters.")
    return pattern, rest


def _split_group(inner: str) -> list[str]:
    items, current, quote = [], [], None
    for ch in inner:
        if quote:
            current.append(ch)
            if ch == quote:
                quote = None
        elif ch in _OPENING:
            quote = _CLOSING[ch]
            current.append(ch)
        elif ch == ",":
            items.append("".join(current))
            current = []
        else:
            current.append(ch)
    if quote:
        raise FilterError("A quote is not closed. Close the phrase with another double quote.")
    items.append("".join(current))
    return items


def parse_triggers(args: str) -> tuple[list[str], str]:
    """
    The triggers at the start of a command's arguments, and the rest (the reply text).

        bot Hello                        -> ["bot"], "Hello"
        "a long sentence" Hello          -> ["a long sentence"], "Hello"
        (bad, "not good") Stay positive  -> ["bad", "not good"], "Stay positive"
    """
    text = (args or "").lstrip()
    if not text:
        raise FilterError("Give me a keyword.")
    if text[0] != "(":
        pattern, rest = _read_one(text)
        return [pattern], rest.strip()

    quote, end = None, None
    for index, ch in enumerate(text[1:], 1):
        if quote:
            if ch == quote:
                quote = None
        elif ch in _OPENING:
            quote = _CLOSING[ch]
        elif ch == ")":
            end = index
            break
    if end is None:
        raise FilterError("A parenthesis is not closed. Finish the list of keywords with ).")
    patterns: list[str] = []
    for item in _split_group(text[1:end]):
        if not item.strip():
            continue
        pattern, rest = _read_one(item)
        if rest.strip():
            raise FilterError(f"Put keywords with spaces in double quotes: \"{item.strip()}\".")
        if pattern not in patterns:
            patterns.append(pattern)
    if not patterns:
        raise FilterError("The list of keywords is empty.")
    if len(patterns) > MAX_GROUP:
        raise FilterError(f"At most {MAX_GROUP} keywords in one filter.")
    return patterns, text[end + 1:].strip()


# --- Matching -------------------------------------------------------------------------------

@dataclass(frozen=True)
class Trigger:
    pattern: str
    regex: re.Pattern


def _compile(pattern: str) -> Trigger:
    kind, sep, body = pattern.partition(":")
    if not (sep and kind in MODIFIERS):
        kind, body = "plain", pattern
    literal = r"\s+".join(re.escape(word) for word in body.split(" "))
    if kind == "prefix":
        source = r"\A" + literal
    elif kind == "exact":
        source = r"\A" + literal + r"\Z"
    else:
        source = r"(?<!\w)" + literal + r"(?!\w)"
    return Trigger(pattern, re.compile(source, re.IGNORECASE))


_triggers: dict[int, list[Trigger]] = {}


async def triggers(chat_id: int) -> list[Trigger]:
    cached = _triggers.get(chat_id)
    if cached is not None:
        return cached
    rows = await database.fetch_all("SELECT pattern FROM filters WHERE chat_id = $1 ORDER BY id", chat_id)
    built = [_compile(row["pattern"]) for row in rows]
    if len(_triggers) >= CACHE_LIMIT:
        _triggers.clear()
    _triggers[chat_id] = built
    return built


async def match(chat_id: int, text: str) -> str | None:
    """The first trigger (by age) that the text matches."""
    text = text.strip()
    for trigger in await triggers(chat_id):
        if trigger.regex.search(text):
            return trigger.pattern
    return None


# --- Storage ----------------------------------------------------------------------------------

async def get(chat_id: int, pattern: str) -> dict[str, Any] | None:
    return await database.fetch_one(
        "SELECT pattern, content, media_type, media_file_id FROM filters WHERE chat_id = $1 AND pattern = $2",
        chat_id,
        pattern,
    )


async def patterns(chat_id: int) -> list[str]:
    return [t.pattern for t in await triggers(chat_id)]


async def save(
    chat_id: int,
    wanted: list[str],
    content: str,
    media_type: str | None,
    media_file_id: str | None,
    created_by: int | None,
) -> tuple[list[str], list[str], list[str]]:
    """Save a reply under every trigger. Returns (added, updated, refused because the chat is full)."""
    current = set(await patterns(chat_id))
    added: list[str] = []
    updated: list[str] = []
    full: list[str] = []
    for pattern in wanted:
        if pattern in current:
            updated.append(pattern)
        elif len(current) + len(added) >= MAX_FILTERS:
            full.append(pattern)
            continue
        else:
            added.append(pattern)
        await database.execute(
            "INSERT INTO filters (chat_id, pattern, content, media_type, media_file_id, created_by) "
            "VALUES ($1, $2, $3, $4, $5, $6) "
            "ON CONFLICT (chat_id, pattern) DO UPDATE SET content = EXCLUDED.content, "
            "media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id",
            chat_id, pattern, content, media_type, media_file_id, created_by,
        )
    _triggers.pop(chat_id, None)
    return added, updated, full


async def remove(chat_id: int, pattern: str) -> bool:
    row = await database.fetch_one(
        "DELETE FROM filters WHERE chat_id = $1 AND pattern = $2 RETURNING pattern", chat_id, pattern
    )
    _triggers.pop(chat_id, None)
    return row is not None


async def clear(chat_id: int) -> int:
    rows = await database.fetch_all("DELETE FROM filters WHERE chat_id = $1 RETURNING pattern", chat_id)
    _triggers.pop(chat_id, None)
    return len(rows)


def forget(chat_id: int) -> None:
    _triggers.pop(chat_id, None)
