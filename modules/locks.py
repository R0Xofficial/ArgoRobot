"""
Locks: kinds of messages a chat does not allow (the commands and the action taken are
in modules/locks.py).

A lock is a name from LOCKS. While it is on, the bot deletes the messages of that kind
sent by anyone who is not an admin or approved. Some locks can be eased with the
allowlist: links, forwards, commands, stickers... that are named in it stay allowed.

Every message of every chat passes through find(), so a chat's settings are kept in
memory and a chat without locks costs nothing.
"""

import re
import unicodedata
from dataclasses import dataclass
from typing import Any
from urllib.parse import urlsplit

from core import database
from core.users import CACHE_LIMIT

MAX_ALLOWED = 200
MAX_ENTRY = 200

# name -> (what it catches, can be allowlisted). Grouped for /locktypes.
GROUPS: dict[str, dict[str, tuple[str, bool]]] = {
    "Everything": {
        "all": ("Every message of everyone who is not an admin or approved. Turns the chat into read-only.", False),
    },
    "Media": {
        "photo": ("Photos.", False),
        "video": ("Videos.", False),
        "videonote": ("Round video messages.", False),
        "gif": ("GIFs.", False),
        "audio": ("Music and other audio files.", False),
        "voice": ("Voice messages.", False),
        "document": ("Files, including photos and videos sent uncompressed.", False),
        "album": ("Photos or files sent together as an album.", False),
        "sticker": ("All stickers. Allowlist a sticker pack to keep it.", True),
        "stickeranimated": ("Animated and video stickers.", True),
        "stickerpremium": ("Premium stickers, the ones with a full-screen effect.", True),
    },
    "Content": {
        "text": ("Anything with text, captions included.", False),
        "poll": ("Polls and quizzes.", False),
        "contact": ("Shared contacts.", False),
        "location": ("Shared locations and venues.", False),
        "game": ("Games from bots.", False),
        "emojigame": ("Dice, darts, bowling and the other emoji games.", False),
        "checklist": ("Telegram checklists.", False),
        "spoiler": ("Text hidden behind a spoiler.", False),
        "emojicustom": ("Custom (animated) emoji.", False),
        "button": ("Messages with buttons under them, usually from inline bots.", False),
    },
    "Links and mentions": {
        "url": ("Any link. Allowlist a site to keep it.", True),
        "email": ("Email addresses.", False),
        "phone": ("Phone numbers.", False),
        "invitelink": ("Links and @names that lead to other groups and channels: the classic advertising spam.", True),
        "botlink": ("Links and @names that lead to bots.", True),
        "cashtag": ("Stock and crypto tags in capitals, like $BTC.", True),
        "command": ("Messages that start with a /command.", True),
    },
    "Where it came from": {
        "forward": ("Every forwarded message.", True),
        "forwarduser": ("Messages forwarded from a person.", True),
        "forwardbot": ("Messages forwarded from a bot.", True),
        "forwardchannel": ("Messages forwarded from a channel.", True),
        "inline": ("Messages sent through an inline bot, like @gif or @vid.", True),
        "anonchannel": ("People writing as a channel instead of as themselves. Posts from the chat's linked channel are not touched.", True),
        "externalreply": ("Replies to a message from another chat.", True),
        "bot": ("Anyone but an admin adding a bot to the chat. The bot is removed.", False),
    },
    "Writing": {
        "cjk": ("Chinese, Japanese and Korean characters.", False),
        "cyrillic": ("Cyrillic characters: Russian, Ukrainian, Serbian...", False),
        "rtl": ("Right-to-left writing: Arabic, Persian, Hebrew...", False),
        "zalgo": ("Text with piles of stacked marks, Z̷a̶l̸g̵o̷ style, that spills over other messages.", False),
    },
}
LOCKS: dict[str, tuple[str, bool]] = {name: info for group in GROUPS.values() for name, info in group.items()}

_CJK = re.compile("[぀-ヿ㄰-㆏㐀-䶿一-鿿가-힯豈-﫿]")
_CYRILLIC = re.compile("[Ѐ-ԯ]")
_RTL = re.compile("[֐-ࣿיִ-﷿ﹰ-﻿]")
_STACKED = 4  # combining marks in a row that make text "zalgo"

_TELEGRAM_HOSTS = {"t.me", "telegram.me", "telegram.dog"}
_NOT_CHATS = {
    "addstickers", "addemoji", "addlist", "addtheme", "share", "proxy", "socks", "setlanguage",
    "boost", "invoice", "giftcode", "m", "login", "contact", "iv", "bg", "confirmphone",
}


@dataclass(frozen=True)
class Config:
    locked: frozenset[str]
    warn: bool
    allow: frozenset[str]


_cache: dict[int, Config] = {}
_chat_kinds: dict[str, bool] = {}  # "@name" -> leads to a group or channel


# --- Storage ----------------------------------------------------------------------------------

async def config(chat_id: int) -> Config:
    cached = _cache.get(chat_id)
    if cached is not None:
        return cached
    locked = await database.fetch_all("SELECT item FROM locks WHERE chat_id = $1", chat_id)
    row = await database.fetch_one("SELECT warn FROM lock_settings WHERE chat_id = $1", chat_id)
    allow = await database.fetch_all("SELECT entry FROM lock_allowlist WHERE chat_id = $1 ORDER BY entry", chat_id)
    built = Config(
        frozenset(r["item"] for r in locked),
        bool(row["warn"]) if row else False,
        frozenset(r["entry"] for r in allow),
    )
    if len(_cache) >= CACHE_LIMIT:
        _cache.clear()
    _cache[chat_id] = built
    return built


def forget(chat_id: int) -> None:
    _cache.pop(chat_id, None)


async def lock(chat_id: int, items: list[str]) -> None:
    for item in items:
        await database.execute("INSERT INTO locks (chat_id, item) VALUES ($1, $2) ON CONFLICT DO NOTHING", chat_id, item)
    forget(chat_id)


async def unlock(chat_id: int, items: list[str]) -> None:
    for item in items:
        await database.execute("DELETE FROM locks WHERE chat_id = $1 AND item = $2", chat_id, item)
    forget(chat_id)


async def set_warn(chat_id: int, enabled: bool) -> None:
    await database.execute(
        "INSERT INTO lock_settings (chat_id, warn) VALUES ($1, $2) ON CONFLICT (chat_id) DO UPDATE SET warn = EXCLUDED.warn",
        chat_id, enabled,
    )
    forget(chat_id)


async def allow_add(chat_id: int, entries: list[str]) -> int:
    """Add entries to the allowlist. Returns how many were new."""
    current = (await config(chat_id)).allow
    added = 0
    for entry in entries:
        if entry in current:
            continue
        await database.execute("INSERT INTO lock_allowlist (chat_id, entry) VALUES ($1, $2) ON CONFLICT DO NOTHING", chat_id, entry)
        added += 1
    forget(chat_id)
    return added


async def allow_remove(chat_id: int, entries: list[str]) -> int:
    """Remove entries from the allowlist. Returns how many were there."""
    current = (await config(chat_id)).allow
    removed = 0
    for entry in entries:
        if entry in current:
            await database.execute("DELETE FROM lock_allowlist WHERE chat_id = $1 AND entry = $2", chat_id, entry)
            removed += 1
    forget(chat_id)
    return removed


async def allow_clear(chat_id: int) -> int:
    rows = await database.fetch_all("DELETE FROM lock_allowlist WHERE chat_id = $1 RETURNING entry", chat_id)
    forget(chat_id)
    return len(rows)


# --- Allowlist entries ------------------------------------------------------------------------

def _split_url(text: str):
    return urlsplit(text if "://" in text else "//" + text)


def _telegram_target(url: str) -> tuple[str, str] | None:
    """('chat', 'name'), ('pack', 'name') or ('invite', '') for a Telegram link, else None."""
    parts = _split_url(url)
    host = (parts.hostname or "").lower()
    if host not in _TELEGRAM_HOSTS:
        return None
    segments = [s for s in parts.path.split("/") if s]
    if not segments:
        return None
    first = segments[0].lower()
    if first in ("addstickers", "addemoji") and len(segments) > 1:
        return "pack", segments[1].lower()
    if first.startswith("+") or first in ("joinchat", "c"):
        return "invite", ""
    if first in _NOT_CHATS:
        return None
    return "chat", first


def _plain_url(url: str) -> str:
    """host/path in lowercase, without scheme, www., query or trailing slash."""
    parts = _split_url(url.strip())
    host = (parts.hostname or "").lower()
    if host.startswith("www."):
        host = host[4:]
    return (host + parts.path.rstrip("/")).lower()


def normalize_entry(token: str) -> str:
    """The stored form of an allowlist entry typed by a person. Raises ValueError with a sentence."""
    token = token.strip()
    if not token or len(token) > MAX_ENTRY:
        raise ValueError(f"\"{token[:30]}\" is not a valid entry.")
    if re.fullmatch(r"-?\d+", token):
        return str(int(token))
    if token.startswith("@"):
        name = token[1:].lower()
        if not re.fullmatch(r"[a-z0-9_]{3,32}", name):
            raise ValueError(f"\"{token}\" is not a valid @name.")
        return "@" + name
    if token.startswith("/"):
        name = token[1:].split("@", 1)[0].lower()
        if not re.fullmatch(r"\w{1,32}", name):
            raise ValueError(f"\"{token}\" is not a valid command.")
        return "/" + name
    if token.startswith("$"):
        tag = token[1:].upper()
        if not re.fullmatch(r"[A-Z]{1,8}", tag):
            raise ValueError(f"\"{token}\" is not a valid cashtag.")
        return "$" + tag
    target = _telegram_target(token)
    if target and target[0] == "pack":
        return "stickerpack:" + target[1]
    if target and target[0] == "chat":
        return "@" + target[1]
    plain = _plain_url(token)
    if "." not in plain.split("/")[0]:
        raise ValueError(f"\"{token[:30]}\" is not a link, id, @name, command or cashtag.")
    return plain


def url_allowed(allow: frozenset[str], url: str) -> bool:
    if not allow:
        return False
    target = _telegram_target(url)
    if target:
        kind, name = target
        if kind == "pack":
            return "stickerpack:" + name in allow
        if kind == "chat":
            return "@" + name in allow or _plain_allowed(allow, url)
    return _plain_allowed(allow, url)


def _plain_allowed(allow: frozenset[str], url: str) -> bool:
    plain = _plain_url(url)
    host = plain.split("/", 1)[0]
    for entry in allow:
        if entry[0] in "@/$-0123456789" or entry.startswith("stickerpack:"):
            continue
        if plain == entry or plain.startswith(entry + "/") or host == entry or host.endswith("." + entry):
            return True
    return False


# --- Looking at a message ---------------------------------------------------------------------

def _utf16(text: str, start: int, length: int) -> str:
    raw = text.encode("utf-16-le")
    return raw[start * 2 : (start + length) * 2].decode("utf-16-le")


def _entities(message) -> tuple[str, list[Any]]:
    if message.text:
        return message.text, message.entities or []
    return message.caption or "", message.caption_entities or []


def _origin_keys(origin) -> set[str]:
    keys: set[str] = set()
    for field in ("sender_user", "sender_chat", "chat"):
        who = getattr(origin, field, None)
        if who is not None:
            keys.add(str(who.id))
            if getattr(who, "username", None):
                keys.add("@" + who.username.lower())
    return keys


def _stacked_marks(text: str) -> bool:
    run = 0
    for char in text:
        if unicodedata.category(char) in ("Mn", "Me"):
            run += 1
            if run >= _STACKED:
                return True
        else:
            run = 0
    return False


async def _leads_to_chat(bot, name: str) -> bool:
    """Whether the public @name belongs to a group or a channel. Remembered for a while."""
    key = "@" + name
    known = _chat_kinds.get(key)
    if known is not None:
        return known
    try:
        found = (await bot.get_chat(key)).type in ("group", "supergroup", "channel")
    except Exception:
        found = False
    if len(_chat_kinds) >= CACHE_LIMIT:
        _chat_kinds.clear()
    _chat_kinds[key] = found
    return found


async def find(message, bot, cfg: Config) -> str | None:
    """The first locked kind this message belongs to, or None."""
    locked, allow = cfg.locked, cfg.allow
    if not locked:
        return None
    if "all" in locked:
        return "all"

    text, entities = _entities(message)
    kinds: dict[str, list[str]] = {}
    for entity in entities:
        kinds.setdefault(entity.type, []).append(_utf16(text, entity.offset, entity.length))
    links = kinds.get("url", []) + [e.url for e in entities if e.type == "text_link" and e.url]
    sticker = message.sticker

    def has(name: str) -> bool:
        return name in locked

    if has("text") and text:
        return "text"
    for name in ("photo", "video", "audio", "voice", "poll", "contact", "game", "checklist"):
        if has(name) and getattr(message, name, None):
            return name
    if has("videonote") and message.video_note:
        return "videonote"
    if has("gif") and message.animation:
        return "gif"
    if has("document") and message.document and not message.animation:
        return "document"
    if has("album") and message.media_group_id:
        return "album"
    if has("location") and (message.location or message.venue):
        return "location"
    if has("emojigame") and message.dice:
        return "emojigame"
    if sticker is not None:
        pack = ("stickerpack:" + sticker.set_name.lower()) if getattr(sticker, "set_name", None) else None
        if not (pack and pack in allow):
            if has("sticker"):
                return "sticker"
            if has("stickeranimated") and (sticker.is_animated or sticker.is_video):
                return "stickeranimated"
            if has("stickerpremium") and getattr(sticker, "premium_animation", None):
                return "stickerpremium"
    if has("spoiler") and "spoiler" in kinds:
        return "spoiler"
    if has("emojicustom") and "custom_emoji" in kinds:
        return "emojicustom"
    if has("email") and "email" in kinds:
        return "email"
    if has("phone") and "phone_number" in kinds:
        return "phone"
    if has("button") and message.reply_markup and message.reply_markup.inline_keyboard:
        return "button"
    if has("cashtag") and any(("$" + c.lstrip("$").upper()) not in allow for c in kinds.get("cashtag", [])):
        return "cashtag"
    if has("command"):
        for entity, value in ((e, _utf16(text, e.offset, e.length)) for e in entities if e.type == "bot_command"):
            if entity.offset == 0 and "/" + value[1:].split("@", 1)[0].lower() not in allow:
                return "command"
    if has("url") and any(not url_allowed(allow, link) for link in links):
        return "url"
    if has("invitelink") or has("botlink"):
        found = await _telegram_links(bot, links, kinds.get("mention", []), allow, has("invitelink"))
        for name in ("invitelink", "botlink"):
            if has(name) and name in found:
                return name

    if message.forward_origin is not None and not message.is_automatic_forward:
        origin = message.forward_origin
        if not (_origin_keys(origin) & allow):
            if has("forward"):
                return "forward"
            if has("forwardbot") and origin.type == "user" and origin.sender_user.is_bot:
                return "forwardbot"
            if has("forwardchannel") and origin.type == "channel":
                return "forwardchannel"
            if has("forwarduser") and (origin.type == "hidden_user" or (origin.type == "user" and not origin.sender_user.is_bot)):
                return "forwarduser"
    if has("inline") and message.via_bot is not None:
        if not (_origin_keys(_Bag(sender_user=message.via_bot)) & allow):
            return "inline"
    if has("anonchannel") and message.sender_chat is not None and message.sender_chat.id != message.chat.id:
        if not (_origin_keys(_Bag(sender_chat=message.sender_chat)) & allow):
            return "anonchannel"
    if has("externalreply") and message.external_reply is not None:
        if not (_origin_keys(_Bag(chat=message.external_reply.chat)) & allow if message.external_reply.chat else False):
            return "externalreply"

    if text:
        if has("cjk") and _CJK.search(text):
            return "cjk"
        if has("cyrillic") and _CYRILLIC.search(text):
            return "cyrillic"
        if has("rtl") and _RTL.search(text):
            return "rtl"
        if has("zalgo") and _stacked_marks(text):
            return "zalgo"
    return None


class _Bag:
    """A tiny stand-in for the objects _origin_keys reads."""

    def __init__(self, **fields: Any) -> None:
        self.__dict__.update(fields)


async def _telegram_links(bot, links: list[str], mentions: list[str], allow: frozenset[str], look_up: bool) -> set[str]:
    """Which of 'invitelink' and 'botlink' the links and @names of a message contain."""
    found: set[str] = set()
    for link in links:
        target = _telegram_target(link)
        if target is None or url_allowed(allow, link):
            continue
        kind, name = target
        if kind == "invite" or (kind == "chat" and not name.endswith("bot")):
            found.add("invitelink")
        elif kind == "chat":
            found.add("botlink")
    for mention in mentions:
        name = mention.lstrip("@").lower()
        if "@" + name in allow:
            continue
        if name.endswith("bot"):
            found.add("botlink")
        elif look_up and "invitelink" not in found and await _leads_to_chat(bot, name):
            found.add("invitelink")
    return found
