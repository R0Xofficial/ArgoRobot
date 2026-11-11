"""
Formatted messages written by users: HTML, fillings, buttons and random content.

Used by modules that save a message and send it later (filters, notes, rules, welcomes).
The text is kept as the person wrote it and turned into a message only when it is sent.

Formatting
    Telegram's own HTML, which is also what the formatting menu of the Telegram app produces:
    <b>bold</b>  <i>italic</i>  <u>underline</u>  <s>strike</s>  <tg-spoiler>spoiler</tg-spoiler>
    <code>code</code>  <pre>pre</pre>  <blockquote>quote</blockquote>  <a href="https://link">text</a>
    Anything else that looks like a tag, and every stray < > &, is shown as the text it is.

Fillings (replaced by facts about whoever triggered the message)
    {first} {last} {fullname} {username} {mention} {id} {chatname} {count}
    A backslash in front of the brace makes it literal: \\{first}

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

import html as html_lib
import random
import re
from dataclasses import dataclass, field
from typing import Any, Callable
from urllib.parse import urlparse

from core.text import esc, esc_attr

MAX_BUTTONS = 20
MAX_BUTTONS_PER_ROW = 8
MAX_VARIANTS = 20
MAX_BUTTON_TEXT = 64

FILLINGS = ("first", "last", "fullname", "username", "mention", "id", "chatname", "count")
OPTIONS = ("preview", "protect", "nonotif", "mediaspoiler")


class MarkupError(ValueError):
    """The text can't be turned into a message; the message says why."""


# --- Safe HTML ------------------------------------------------------------------------

_HOST = re.compile(r"^(?:[\w.~-]+@)?[\w.-]+\.[A-Za-z\w-]{2,}(?::\d{1,5})?$")
_TAGS = {
    "b": "b", "strong": "b", "i": "i", "em": "i", "u": "u", "ins": "u", "s": "s", "strike": "s", "del": "s",
    "tg-spoiler": "tg-spoiler", "code": "code", "pre": "pre", "a": "a", "blockquote": "blockquote",
}
_TAG = re.compile(r"<(/?)([A-Za-z][\w-]*)((?:\s[^<>]*)?)>")
_BARE_AMP = re.compile(r"&(?!(?:amp|lt|gt|quot|#\d{1,7}|#[xX][0-9a-fA-F]{1,6});)")
_HREF = re.compile(r"""href\s*=\s*(?:"([^"]*)"|'([^']*)')""", re.IGNORECASE)
_CLASS = re.compile(r"""class\s*=\s*(?:"([^"]*)"|'([^']*)')""", re.IGNORECASE)


def _web_url_ok(parsed) -> bool:
    return parsed.scheme.lower() in ("http", "https") and bool(_HOST.match(parsed.netloc))


def _safe_url(url: str) -> str | None:
    url = html_lib.unescape(url).strip()
    parsed = urlparse(url)
    if _web_url_ok(parsed) or (parsed.scheme.lower() == "tg" and (parsed.netloc or parsed.path)):
        return url
    return None


def _text(segment: str) -> str:
    return _BARE_AMP.sub("&amp;", segment).replace("<", "&lt;").replace(">", "&gt;")


def _attr(regex: re.Pattern, attrs: str) -> str | None:
    found = regex.search(attrs)
    return (found.group(1) if found.group(1) is not None else found.group(2)) if found else None


def to_html(source: str) -> str:
    """
    Telegram HTML for a text written in the HTML above: the tags Telegram knows are kept
    (and closed in the right order), everything else is shown as the text it is.
    """
    out: list[str] = []
    stack: list[tuple[str, str]] = []  # (tag, what to write when it closes)
    position = 0
    for match in _TAG.finditer(source):
        out.append(_text(source[position:match.start()]))
        position = match.end()
        closing, name, attrs = match.group(1) == "/", match.group(2).lower(), match.group(3)
        tag = _TAGS.get(name)
        if name == "span" and (closing and any(entry[0] == "tg-spoiler" for entry in stack) or (_attr(_CLASS, attrs) or "") == "tg-spoiler"):
            tag = "tg-spoiler"
        if tag is None:
            out.append(_text(match.group(0)))
            continue
        if closing:
            kinds = [entry[0] for entry in stack]
            if tag not in kinds:
                continue
            depth = len(kinds) - 1 - kinds[::-1].index(tag)
            for _, text in reversed(stack[depth:]):
                out.append(text)
            del stack[depth:]
            continue
        if tag == "a":
            url = _safe_url(_attr(_HREF, attrs) or "")
            if url is None:
                stack.append(("a", ""))  # the link is dropped, its text stays
                continue
            out.append(f'<a href="{esc_attr(url)}">')
            stack.append(("a", "</a>"))
        elif tag == "code":
            language = _attr(_CLASS, attrs) or ""
            out.append(f'<code class="{language}">' if re.fullmatch(r"language-[\w+#.-]{1,30}", language) else "<code>")
            stack.append(("code", "</code>"))
        elif tag == "blockquote":
            out.append("<blockquote expandable>" if re.search(r"\bexpandable\b", attrs, re.IGNORECASE) else "<blockquote>")
            stack.append(("blockquote", "</blockquote>"))
        else:
            out.append(f"<{tag}>")
            stack.append((tag, f"</{tag}>"))
    out.append(_text(source[position:]))
    for _, text in reversed(stack):
        out.append(text)
    return "".join(out)


# --- Message entities to HTML ------------------------------------------------------------

_ENTITY_TAGS = {
    "bold": "b", "italic": "i", "underline": "u", "strikethrough": "s", "spoiler": "tg-spoiler",
    "code": "code", "pre": "pre", "blockquote": "blockquote", "expandable_blockquote": "blockquote",
}


def from_entities(text: str, entities: list[Any] | None, *, literal: bool = True, skip: int = 0) -> str:
    """
    The HTML for a message Telegram already formatted.

    literal: write the plain text as it is, so that what it shows stays what it shows (a
    replied-to message). Without it the text is left as typed, so tags written by hand
    still work next to the formatting made with the app's menu (a command's arguments).
    skip: UTF-16 units to leave out at the start (the command itself).
    """
    if not text:
        return ""
    units = [len(ch.encode("utf-16-le")) // 2 for ch in text]
    starts: dict[int, int] = {}
    position = 0
    for index, size in enumerate(units):
        starts[position] = index
        position += size
    starts[position] = len(text)

    spans = []  # (start, end, open, close)
    for entity in entities or []:
        kind = getattr(entity.type, "value", entity.type)
        start, end = starts.get(entity.offset - skip), starts.get(entity.offset + entity.length - skip)
        if entity.offset < skip or start is None or end is None or end <= start:
            continue
        if kind == "text_link" and getattr(entity, "url", None):
            spans.append((start, end, f'<a href="{esc_attr(entity.url)}">', "</a>"))
        elif kind == "pre":
            language = getattr(entity, "language", None)
            if language and re.fullmatch(r"[\w+#.-]{1,30}", language):
                spans.append((start, end, f'<pre><code class="language-{language}">', "</code></pre>"))
            else:
                spans.append((start, end, "<pre>", "</pre>"))
        elif kind in _ENTITY_TAGS:
            tag = _ENTITY_TAGS[kind]
            opening = "<blockquote expandable>" if kind == "expandable_blockquote" else f"<{tag}>"
            spans.append((start, end, opening, f"</{tag}>"))

    out: list[str] = []
    stack: list[tuple[int, int, str, str]] = []
    for index, char in enumerate(text + "\0"):
        ending = [span for span in stack if span[1] == index]
        if ending:
            low = min(stack.index(span) for span in ending)
            above = stack[low:]
            out.extend(span[3] for span in reversed(above))
            stack = stack[:low]
            for span in above:  # spans that go on are closed and opened again around it
                if span[1] != index:
                    out.append(span[2])
                    stack.append(span)
        for span in sorted((s for s in spans if s[0] == index), key=lambda s: -s[1]):
            out.append(span[2])
            stack.append(span)
        if index < len(text):
            out.append(esc(char) if literal else char)
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
_BUTTON = re.compile(r"\[([^\]]+)\]\(buttonurl://([^)\s]+)\)", re.IGNORECASE)


def _button_url(raw: str) -> str:
    url = html_lib.unescape(raw)
    if not re.match(r"^[a-z][a-z0-9+.-]*://", url, re.IGNORECASE):
        url = "https://" + url
    parsed = urlparse(url)
    scheme = parsed.scheme.lower()
    if not (_web_url_ok(parsed) or (scheme == "tg" and (parsed.netloc or parsed.path))):
        raise MarkupError(f"The button link \"{html_lib.unescape(raw)}\" isn't valid. Use a link like https://example.com.")
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
            name = value or (values or {}).get("fullname") or "x"
            return f'<a href="tg://user?id={esc_attr(user_id)}">{esc(name)}</a>'
        return esc(value)

    text = _FILLING.sub(fill, text)

    rows: list[list[tuple[str, str]]] = []
    count = 0

    def take_button(match: re.Match) -> str:
        nonlocal count
        label = html_lib.unescape(re.sub(r"<[^>]*>", "", match.group(1))).strip()
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
    text = re.sub(r"\\(\{(?:%s)\})" % "|".join(FILLINGS + OPTIONS), r"\1", text)  # \{first} is the text {first}
    return Parsed(to_html(text.strip()), rows, options)


def check(source: str) -> None:
    """Raise MarkupError when a saved message could not be sent (every variant is tried)."""
    parts = variants(source)
    if len(parts) > MAX_VARIANTS:
        raise MarkupError(f"At most {MAX_VARIANTS} random variants (separated by %%%).")
    for part in parts:
        parse(part)
