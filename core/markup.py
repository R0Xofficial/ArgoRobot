"""
Formatted messages written by users: Markdown, fillings, buttons and random content.

Used by modules that save a message and send it later (filters, and welcomes or notes
when they come). The text is kept exactly as the person wrote it and turned into a
message only when it is sent.

Markdown
    *bold*  _italic_  __underline__  ~strike~  ||spoiler||  `code`  ```pre```
    [text](https://link)
    A backslash makes a character literal: \\* \\_ \\[ ...
    Markers only work at the edge of words, so snake_case_names are left alone.

Fillings (replaced by facts about whoever triggered the message)
    {first} {last} {fullname} {username} {mention} {id} {chatname} {count}

Options (removed from the text, they change how it is sent)
    {preview} link previews on   {protect} no forwarding or saving
    {nonotif} silent             {mediaspoiler} hide a photo or video behind a spoiler

Buttons (removed from the text, shown under the message)
    [Text](buttonurl://example.com)         a button on its own row
    [Text](buttonurl://example.com:same)    a button on the row of the previous one

Random content
    Variants separated by %%% - one of them is picked each time.

Nothing here talks to Telegram; it only builds text and data.
"""

import random
import re
from dataclasses import dataclass, field
from typing import Any, Callable
from urllib.parse import urlparse

from core.text import esc, esc_attr

ESCAPABLE = frozenset("\\*_~|`[]()%{}")
MAX_BUTTONS = 20
MAX_BUTTONS_PER_ROW = 8
MAX_VARIANTS = 20
MAX_BUTTON_TEXT = 64

FILLINGS = ("first", "last", "fullname", "username", "mention", "id", "chatname", "count")
OPTIONS = ("preview", "protect", "nonotif", "mediaspoiler")


class MarkupError(ValueError):
    """The text can't be turned into a message; the message says why."""


# --- Escaping -----------------------------------------------------------------------

def escape(text: str) -> str:
    """Make `text` literal, so none of it is read as formatting."""
    return re.sub(r"([\\*_~|`\[\]()%{}])", r"\\\1", text)


def unescape(text: str) -> str:
    return re.sub(r"\\(.)", lambda m: m.group(1) if m.group(1) in ESCAPABLE else m.group(0), text)


# --- Markdown to HTML -----------------------------------------------------------------

_MARKERS = (("||", "tg-spoiler"), ("__", "u"), ("*", "b"), ("_", "i"), ("~", "s"))
_LINK = re.compile(r"\[((?:\\.|[^\]\\])+)\]\(((?:\\.|[^)\\\s])+)\)")
_SAFE_SCHEMES = ("http", "https", "tg")
_HOST = re.compile(r"^(?:[\w.~-]+@)?[\w.-]+\.[A-Za-z\w-]{2,}(?::\d{1,5})?$")


def _web_url_ok(parsed) -> bool:
    return parsed.scheme.lower() in ("http", "https") and bool(_HOST.match(parsed.netloc))


def _safe_url(url: str) -> str | None:
    url = unescape(url)
    parsed = urlparse(url)
    if _web_url_ok(parsed) or (parsed.scheme.lower() == "tg" and (parsed.netloc or parsed.path)):
        return url
    return None


def to_html(text: str) -> str:
    """Telegram HTML for a text written in the Markdown above."""
    tokens: list[list[Any]] = []  # [kind, value, ...]
    buffer: list[str] = []

    def flush() -> None:
        if buffer:
            tokens.append(["text", "".join(buffer)])
            buffer.clear()

    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if c == "\\" and i + 1 < n and text[i + 1] in ESCAPABLE:
            buffer.append(text[i + 1])
            i += 2
            continue
        if text.startswith("```", i):
            end = text.find("```", i + 3)
            if end > i + 3:
                flush()
                tokens.append(["html", f"<pre>{esc(text[i + 3:end].strip(chr(10)))}</pre>"])
                i = end + 3
                continue
        if c == "`":
            end = text.find("`", i + 1)
            if end > i + 1 and "\n" not in text[i:end]:
                flush()
                tokens.append(["html", f"<code>{esc(text[i + 1:end])}</code>"])
                i = end + 1
                continue
        if c == "[":
            match = _LINK.match(text, i)
            url = _safe_url(match.group(2)) if match else None
            if match and url:
                flush()
                tokens.append(["html", f'<a href="{esc_attr(url)}">{to_html(match.group(1))}</a>'])
                i = match.end()
                continue
        for marker, tag in _MARKERS:
            if text.startswith(marker, i):
                before = text[i - 1] if i else ""
                after = text[i + len(marker)] if i + len(marker) < n else ""
                can_close = bool(before) and not before.isspace() and not after.isalnum()
                can_open = bool(after) and not after.isspace() and not before.isalnum()
                flush()
                tokens.append(["marker", marker, tag, can_open, can_close])
                i += len(marker)
                break
        else:
            buffer.append(c)
            i += 1
    flush()

    # Pair openers with closers; whatever is left over is plain text.
    stack: list[int] = []
    state = {}  # token index -> "open" / "close"
    for index, token in enumerate(tokens):
        if token[0] != "marker":
            continue
        _, marker, _tag, can_open, can_close = token
        position = next((k for k in range(len(stack) - 1, -1, -1) if tokens[stack[k]][1] == marker), None)
        if can_close and position is not None:
            opener = stack[position]
            if index - opener > 1 and any(t[0] != "marker" or state.get(k) for k, t in enumerate(tokens[opener + 1:index], opener + 1)):
                del stack[position:]  # markers opened inside and never closed stay plain
                state[opener], state[index] = "open", "close"
                continue
        if can_open:
            stack.append(index)

    out: list[str] = []
    for index, token in enumerate(tokens):
        kind = token[0]
        if kind == "text":
            out.append(esc(token[1]))
        elif kind == "html":
            out.append(token[1])
        elif state.get(index) == "open":
            out.append(f"<{token[2]}>")
        elif state.get(index) == "close":
            out.append(f"</{token[2]}>")
        else:
            out.append(esc(token[1]))
    return "".join(out)


# --- Message entities to Markdown --------------------------------------------------------

_ENTITY_MARKS = {
    "bold": "*",
    "italic": "_",
    "underline": "__",
    "strikethrough": "~",
    "spoiler": "||",
}


def from_entities(text: str, entities: list[Any] | None) -> str:
    """The Markdown for a message Telegram already formatted (a replied-to message)."""
    if not text:
        return ""
    units = [len(ch.encode("utf-16-le")) // 2 for ch in text]
    starts = {}
    position = 0
    for index, size in enumerate(units):
        starts[position] = index
        position += size
    starts[position] = len(text)

    def char_index(offset: int) -> int | None:
        return starts.get(offset)

    opens: dict[int, list[tuple[int, str]]] = {}
    closes: dict[int, list[tuple[int, str]]] = {}
    raw = [False] * len(text)  # inside code: nothing is escaped
    spans = []
    for entity in entities or []:
        kind = getattr(entity.type, "value", entity.type)
        start = char_index(entity.offset)
        end = char_index(entity.offset + entity.length)
        if start is None or end is None or end <= start:
            continue
        if kind in ("code", "pre"):
            body = text[start:end]
            if "`" in body:
                continue
            for k in range(start, end):
                raw[k] = True
            mark = "`" if kind == "code" else "```"
            spans.append((start, end, mark, mark))
        elif kind in _ENTITY_MARKS:
            spans.append((start, end, _ENTITY_MARKS[kind], _ENTITY_MARKS[kind]))
        elif kind == "text_link" and getattr(entity, "url", None):
            spans.append((start, end, "[", f"]({escape(entity.url)})"))
    # Outer spans first, so that they open first and close last.
    spans.sort(key=lambda s: (s[0], -s[1]))
    for start, end, open_mark, close_mark in spans:
        if open_mark in ("`", "```") or not any(raw[start:end]):
            opens.setdefault(start, []).append((end, open_mark))
            closes.setdefault(end, []).append((start, close_mark))
    out: list[str] = []
    for index, ch in enumerate(text + "\0"):
        for _start, mark in sorted(closes.get(index, []), key=lambda c: -c[0]):
            out.append(mark)
        if index == len(text):
            break
        for _end, mark in opens.get(index, []):
            out.append(mark)
        out.append(ch if raw[index] else escape(ch))
    return "".join(out)


# --- Random content, fillings, buttons ---------------------------------------------------

def variants(source: str) -> list[str]:
    """The texts one saved message can turn into (split on %%%)."""
    parts = [part.strip() for part in re.split(r"%%%", source)]
    found = [part for part in parts if part]
    return found or [""]


def pick(source: str) -> str:
    return random.choice(variants(source))


@dataclass
class Parsed:
    html: str
    buttons: list[list[tuple[str, str]]] = field(default_factory=list)
    options: set[str] = field(default_factory=set)


_OPTION = re.compile(r"(?<!\\)\{(" + "|".join(OPTIONS) + r")\}")
_FILLING = re.compile(r"(?<!\\)\{(" + "|".join(FILLINGS) + r")\}")
_BUTTON = re.compile(r"\[((?:\\.|[^\]\\])+)\]\(buttonurl://((?:\\.|[^)\\\s])+)\)", re.IGNORECASE)


def _button_url(raw: str) -> str:
    url = unescape(raw)
    if not re.match(r"^[a-z][a-z0-9+.-]*://", url, re.IGNORECASE):
        url = "https://" + url
    parsed = urlparse(url)
    scheme = parsed.scheme.lower()
    if not (_web_url_ok(parsed) or (scheme == "tg" and (parsed.netloc or parsed.path))):
        raise MarkupError(f"The button link \"{unescape(raw)}\" isn't valid. Use a link like https://example.com.")
    return url


def parse(source: str, values: dict[str, str] | None = None) -> Parsed:
    """
    Turn one variant into HTML, buttons and options.

    `values` maps fillings to their (plain) text. With None the fillings get stand-in
    text: that is how a message is checked when it is saved.
    """
    options = {match.group(1) for match in _OPTION.finditer(source)}
    text = _OPTION.sub("", source)

    def fill(match: re.Match) -> str:
        name = match.group(1)
        value = (values or {}).get(name, "x")
        user_id = (values or {}).get("id", "1")
        if name == "mention" or (name == "username" and not value):
            # Someone without a @username is mentioned by name instead.
            return f"[{escape(value or (values or {}).get('fullname') or 'x')}](tg://user?id={user_id})"
        return escape(value)

    text = _FILLING.sub(fill, text)

    rows: list[list[tuple[str, str]]] = []
    count = 0

    def take_button(match: re.Match) -> str:
        nonlocal count
        label = unescape(match.group(1)).strip()
        url_raw = match.group(2)
        same = url_raw.lower().endswith(":same")
        if same:
            url_raw = url_raw[: -len(":same")]
        if not label:
            raise MarkupError("A button needs a text.")
        if len(label) > MAX_BUTTON_TEXT:
            raise MarkupError(f"A button text can be at most {MAX_BUTTON_TEXT} characters.")
        url = _button_url(url_raw)
        count += 1
        if count > MAX_BUTTONS:
            raise MarkupError(f"At most {MAX_BUTTONS} buttons per message.")
        if same and rows and len(rows[-1]) < MAX_BUTTONS_PER_ROW:
            rows[-1].append((label, url))
        else:
            rows.append([(label, url)])
        return ""

    text = _BUTTON.sub(take_button, text)
    if "buttonurl://" in text.lower():
        raise MarkupError("A button is written like [Text](buttonurl://example.com).")
    return Parsed(to_html(text.strip()), rows, options)


def check(source: str) -> None:
    """Raise MarkupError when a saved message could not be sent (every variant is tried)."""
    parts = variants(source)
    if len(parts) > MAX_VARIANTS:
        raise MarkupError(f"At most {MAX_VARIANTS} random variants (separated by %%%).")
    for part in parts:
        parse(part)
