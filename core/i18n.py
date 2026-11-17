"""
Languages of the bot's texts.

Code never contains the text itself, only a tag: tr("bans.was_banned", who=name).
The texts live in locales/<code>.json, one file per language, as {"tag": "text"}
with {placeholders}. locales/en.json is the reference and has every tag; another
language may lack some, and a missing tag is shown in English, so a missing line
is never an error. A language is a single file and nothing else: the files in locales/
are found at start-up, so to add one, copy en.json to <code>.json and translate it. Besides
the texts the file holds its own settings: meta.name (its name), meta.flag (its flag),
meta.plural ("one-other" or "one-few-many") and the forms of the time units, time.day ...

The language belongs to a chat (a private chat is a chat too, its id is the user's id)
and is chosen with /setlang. A middleware (core/middlewares.py) sets it for the update
being handled; code that runs later on its own (sweepers, timers) calls use_chat().
Logs for developers, command names and tags like #GBANNED are not translated.
"""

import json
import logging
from contextlib import asynccontextmanager, contextmanager
from pathlib import Path
from contextvars import ContextVar

log = logging.getLogger(__name__)

CACHE_LIMIT = 20000
DEFAULT = "en"
LANGUAGES: dict[str, str] = {}   # code -> its own name, found in locales/
FLAGS: dict[str, str] = {}       # code -> flag


def label(code: str) -> str:
    """A language as shown to people: its flag and its own name."""
    return f"{FLAGS[code]} {LANGUAGES[code]}" if code in FLAGS else LANGUAGES[code]

_current: ContextVar[str] = ContextVar("language", default=DEFAULT)
_catalogs: dict[str, dict[str, str]] = {}
_cache: dict[int, str] = {}


LOCALES = Path(__file__).resolve().parent.parent / "locales"


def _catalog(code: str) -> dict[str, str]:
    found = _catalogs.get(code)
    if found is None:
        try:
            found = json.loads((LOCALES / f"{code}.json").read_text(encoding="utf8"))
        except Exception:
            log.exception("Could not load the language %s", code)
            found = {}
        _catalogs[code] = found
    return found


def discover() -> None:
    """Find every language in locales/: a <code>.json file is a language. English comes first, then by code."""
    LANGUAGES.clear()
    FLAGS.clear()
    _catalogs.clear()
    codes = sorted((path.stem for path in LOCALES.glob("*.json")), key=lambda code: (code != DEFAULT, code))
    for code in codes:
        found = _catalog(code)
        if not found:
            continue
        LANGUAGES[code] = found.get("meta.name") or code
        if found.get("meta.flag"):
            FLAGS[code] = found["meta.flag"]


def current() -> str:
    return _current.get()


def _find(tag: str) -> tuple[str | None, bool]:
    """The text of a tag in the current language, else in English. Second value: it came from the current language."""
    code = _current.get()
    if code != DEFAULT:
        text = _catalog(code).get(tag)
        if text is not None:
            return text, True
    return _catalog(DEFAULT).get(tag), False


def tr(tag: str, /, **values) -> str:
    """The text behind a tag, in the language of the update being handled. {name} placeholders are filled from `values`."""
    text, local = _find(tag)
    if text is None:
        text = tag  # not a known tag: show it as it is
    if not values:
        return text
    try:
        return text.format(**values)
    except (KeyError, IndexError, ValueError):
        log.warning("Broken text for %r (%s)", tag, _current.get())
        english = _catalog(DEFAULT).get(tag, tag) if local else tag
        return english.format(**values)


def tr_or(tag: str, default: str, /, **values) -> str:
    """Like tr(), but a tag with no text anywhere gives `default` (for names that are the same in every language)."""
    text, _ = _find(tag)
    return tr(tag, **values) if text is not None else (default.format(**values) if values else default)


def word(value: str) -> str:
    """A word from a fixed list (ban, kick, mute ...) in the current language; one that is not listed stays as it is."""
    return tr_or(f"word.{value}", value)


def plural(count: int, one: str, few: str, many: str) -> str:
    """Pick the form for a number by the language's meta.plural. "one-few-many" (Polish): 1 konto, 2 konta, 5 kont.
    "one-other" (the default, English): `one` for 1 and `many` otherwise."""
    if _catalog(_current.get()).get("meta.plural") != "one-few-many":
        return one if count == 1 else many
    if count == 1:
        return one
    if 2 <= count % 10 <= 4 and not 12 <= count % 100 <= 14:
        return few
    return many


def forms(tag: str) -> tuple[str, str, str]:
    """The three forms of a tag written as "one|few|many" (a language with two forms writes "one|other")."""
    parts = tr(tag).split("|")
    while len(parts) < 3:
        parts.append(parts[-1])
    return parts[0], parts[1], parts[2]


def plural_tag(tag: str, count: int) -> str:
    """The form of a "one|few|many" tag that fits the number."""
    return plural(count, *forms(tag))


# --- Which language a chat uses ---------------------------------------------------------------

async def chat_language(chat_id: int) -> str:
    from core import database
    code = _cache.get(chat_id)
    if code is None:
        row = await database.fetch_one("SELECT language FROM chat_settings WHERE chat_id = $1", chat_id)
        code = row["language"] if row and row["language"] in LANGUAGES else DEFAULT
        if len(_cache) >= CACHE_LIMIT:
            _cache.clear()
        _cache[chat_id] = code
    return code


async def set_chat_language(chat_id: int, code: str) -> None:
    from core import database

    await database.execute(
        "INSERT INTO chat_settings (chat_id, language) VALUES ($1, $2) "
        "ON CONFLICT (chat_id) DO UPDATE SET language = EXCLUDED.language",
        chat_id,
        code,
    )
    _cache[chat_id] = code


def forget_chat(chat_id: int) -> None:
    _cache.pop(chat_id, None)


async def use_chat(chat_id: int | None) -> str:
    """Speak the language of this chat from here on (in the current task)."""
    code = DEFAULT
    if chat_id is not None:
        try:
            code = await chat_language(chat_id)
        except Exception:
            log.exception("Could not read the language of %s", chat_id)
    _current.set(code)
    return code


@contextmanager
def english():
    """Inside this block texts are in English: for log chats, which are always English whatever the chat being handled speaks."""
    token = _current.set(DEFAULT)
    try:
        yield
    finally:
        _current.reset(token)


@asynccontextmanager
async def speaking(chat_id: int | None):
    """Inside this block texts are written in the language of `chat_id`: for a message sent to another chat than the one being handled."""
    token = _current.set(await chat_language_safe(chat_id))
    try:
        yield
    finally:
        _current.reset(token)


async def chat_language_safe(chat_id: int | None) -> str:
    if chat_id is None:
        return DEFAULT
    try:
        return await chat_language(chat_id)
    except Exception:
        log.exception("Could not read the language of %s", chat_id)
        return DEFAULT


discover()
