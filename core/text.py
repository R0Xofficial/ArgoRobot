"""
Helpers for building Telegram HTML text safely.

The bot sends messages in HTML parse mode, so anything that is not written by
us (user names, chat titles, module descriptions, exception messages) must be
escaped before it is placed in a message.

    await reply(message, f"Hello, <b>{esc(user.full_name)}</b>!")

Never use html.escape() directly: by default it also converts quotes to
numeric entities (&#x27; for an apostrophe), which is exactly the kind of
unreadable output this module exists to prevent.
"""

import html
import re

_TAG_RE = re.compile(r"<[^>]*>")


def esc(value: object) -> str:
    """
    Escape text for use between HTML tags.

    Only &, < and > have a special meaning there. Quotes are left untouched,
    so "it's" and "say "hi"" stay readable.
    """
    return html.escape(str(value), quote=False)


def esc_attr(value: object) -> str:
    """Escape text for use inside a double-quoted HTML attribute (e.g. href)."""
    return esc(value).replace('"', "&quot;")


def strip_tags(text: str) -> str:
    """
    Plain-text version of an HTML message.

    Used as a fallback when Telegram rejects the markup ("can't parse
    entities"), so the user still gets the content instead of an error.
    """
    return html.unescape(_TAG_RE.sub("", text))


def truncate(text: str, limit: int, suffix: str = "...") -> str:
    """Shorten text to at most `limit` characters."""
    if len(text) <= limit:
        return text
    return text[: max(0, limit - len(suffix))] + suffix


def user_link(user_id: int, name: str | None) -> str:
    """Clickable mention that opens the user's profile."""
    return f'<a href="tg://user?id={int(user_id)}">{esc(name or "User")}</a>'


def user_ref(user_id: int, name: str | None) -> str:
    """Profile link followed by the id: Name [123456789]."""
    return f"{user_link(user_id, name)} [<code>{int(user_id)}</code>]"


def duration_text(seconds: int) -> str:
    """90 -> '1 minute 30 seconds'."""
    parts = []
    for name, size in (("day", 86400), ("hour", 3600), ("minute", 60), ("second", 1)):
        count, seconds = divmod(seconds, size)
        if count:
            parts.append(f"{count} {name}{'' if count == 1 else 's'}")
    return " ".join(parts)


# Every on/off setting of the bot understands the same words.
SWITCH = {"on": True, "yes": True, "off": False, "no": False}


def parse_switch(value: str | None) -> bool | None:
    """True for on/yes, False for off/no, None for anything else. Case does not matter."""
    return SWITCH.get((value or "").strip().lower())
