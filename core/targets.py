"""
Finding out which user a command is aimed at.

Command syntax, for every command that acts on a user:

    /command <user> [extra data]

The user always comes first, whatever the command needs after it, e.g.
`/ban @Michalek123 spamming links`. The user can be given as:

  1. A text mention: the person's name as a clickable link (people without a
     @username have these); the link carries the account itself.
  2. A @username mention, or a @username / id typed by hand.
  3. Nothing at all, when the command is a reply to the person's message. Then
     everything after the command is extra data.

Only the first argument is read as the user, so mentions inside the extra data
(a reason, a note) are never mistaken for the target. A person's name is never
counted as the reason: the whole link is the user, and when Telegram links only
part of a name ("Jan" of "Jan Kowalski"), the rest of the name is skipped too.

@usernames and ids are looked up in the phone book (core/users.py) first and
only then asked from Telegram, which knows users only when the bot has met them.

Commands that also make sense for bots, groups and channels (/id, /info) call
resolve_target(..., allow_bots=True, allow_chats=True). Then a target can also be
a chat: a @username or a negative id, or a replied-to message that was sent on
behalf of a channel (what premium users can do), in which case Target.kind says
what it is. Commands that give roles keep the defaults and only ever get users.
"""

import logging
import re
from dataclasses import dataclass

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import users

log = logging.getLogger(__name__)

_USERNAME_RE = re.compile(r"^@?([A-Za-z][A-Za-z0-9_]{3,31})$")
_ID_RE = re.compile(r"^-?\d+$")

_HINT = "Reply to a user's message, mention them, or give a user id or @username."


class TargetError(Exception):
    """Raised with a message that is safe to show to the user (plain text)."""


@dataclass
class Target:
    user_id: int  # the id of the user, or of the chat when kind is a chat type
    name: str | None
    extra: str = ""  # everything after the user: a reason, a duration, ...
    kind: str = "user"  # user, bot, group, supergroup or channel
    first_name: str | None = None
    last_name: str | None = None
    username: str | None = None
    title: str | None = None

    @property
    def is_chat(self) -> bool:
        return self.kind in CHAT_KINDS


CHAT_KINDS = ("group", "supergroup", "channel")
_NOT_A_USER_MESSAGE = "That message was not sent by a regular user."


def _value(thing) -> str:
    return getattr(thing, "value", thing)


def _user_kind(is_bot: bool, username: str | None) -> str:
    # Only bots can have a username ending in "bot".
    return "bot" if is_bot or (username or "").lower().endswith("bot") else "user"


async def _learn(chat) -> None:
    """Add what Telegram told us about a user to the phone book."""
    try:
        await users.remember(chat)
    except Exception as exc:
        log.debug("Could not store user %s: %s", getattr(chat, "id", "?"), exc)


async def display_name(bot: Bot, user_id: int, stored: str | None = None) -> str | None:
    """Current name of a user: from Telegram, else the phone book, else `stored`."""
    try:
        chat = await bot.get_chat(user_id)
    except TelegramAPIError:
        pass
    else:
        await _learn(chat)
        if chat.full_name:
            return chat.full_name

    try:
        return await users.name_of(user_id) or stored
    except Exception:
        return stored


def _from_user(user, allow_bots: bool = False) -> Target:
    username = getattr(user, "username", None)
    kind = _user_kind(getattr(user, "is_bot", False), username)
    if kind == "bot" and not allow_bots:
        raise TargetError("Bots cannot hold roles.")
    return Target(
        user.id,
        user.full_name,
        kind=kind,
        first_name=getattr(user, "first_name", None),
        last_name=getattr(user, "last_name", None),
        username=username,
    )


def _from_row(row: dict, allow_bots: bool) -> Target:
    kind = _user_kind(row["is_bot"], row["username"])
    if kind == "bot" and not allow_bots:
        raise TargetError("Bots cannot hold roles.")
    return Target(
        row["user_id"],
        row["name"],
        kind=kind,
        first_name=row.get("first_name"),
        last_name=row.get("last_name"),
        username=row["username"],
    )


def _from_chat(chat, allow_bots: bool, allow_chats: bool) -> Target:
    """A Target from what Telegram returned for a chat (private chats are users)."""
    kind = _value(chat.type)
    if kind == "private":
        return _from_user(chat, allow_bots)
    if kind not in CHAT_KINDS or not allow_chats:
        raise TargetError("That is not a user.")
    return Target(chat.id, chat.title, kind=kind, username=chat.username, title=chat.title)


async def _by_id(bot: Bot, user_id: int, allow_bots: bool, allow_chats: bool) -> Target:
    if user_id < 0:
        # Groups and channels have negative ids.
        if not allow_chats:
            raise TargetError("That is not a valid user id.")
        try:
            chat = await bot.get_chat(user_id)
        except TelegramAPIError:
            raise TargetError("I don't know that chat.") from None
        return _from_chat(chat, allow_bots, allow_chats)

    row = await users.get(user_id)
    if row is not None:
        return _from_row(row, allow_bots)

    try:
        chat = await bot.get_chat(user_id)
    except TelegramAPIError:
        return Target(user_id, None)  # never met: only the id is known
    await _learn(chat)
    return _from_chat(chat, allow_bots, allow_chats)


async def _by_username(bot: Bot, username: str, allow_bots: bool, allow_chats: bool) -> Target:
    row = await users.find_by_username(username)
    if row is not None:
        return _from_row(row, allow_bots)

    try:
        chat = await bot.get_chat(f"@{username}")
    except TelegramAPIError:
        raise TargetError(
            f"I don't know @{username} yet. Reply to their message, mention them or use their id."
        ) from None
    if _value(chat.type) == "private":
        await _learn(chat)
    return _from_chat(chat, allow_bots, allow_chats)


def replied_message(message: Message):
    """
    The message a command replies to, or None.

    In forum topics every message "replies" to the service message that created
    the topic. That is not a person the command is aimed at.
    """
    reply = message.reply_to_message
    if reply is not None and getattr(reply, "forum_topic_created", None) is not None:
        return None
    return reply


def sender_as_target(message: Message) -> Target:
    """
    Whoever sent the command. A premium user (or an anonymous admin) can write on
    behalf of a channel or group; then the target is that chat, not the user.
    """
    chat = getattr(message, "sender_chat", None)
    user = message.from_user
    if chat is not None and (user is None or user.id in users.IGNORED_IDS):
        return _from_chat(chat, allow_bots=True, allow_chats=True)
    return _from_user(user, allow_bots=True)


def _first_argument_offset(text: str) -> int | None:
    """Where the first argument starts, in UTF-16 units (Telegram's entity offsets)."""
    match = re.match(r"\S+\s+(?=\S)", text)
    if match is None:
        return None
    return len(text[: match.end()].encode("utf-16-le")) // 2


def _utf16_slice(text: str, start: int, end: int | None = None) -> str:
    """Part of a text by UTF-16 offsets (what Telegram entities use)."""
    raw = text.encode("utf-16-le")
    return raw[start * 2 : None if end is None else end * 2].decode("utf-16-le")


def _strip_name_remainder(extra: str, linked_text: str, full_name: str | None) -> str:
    """
    Drop the rest of a person's name from the start of the extra data.

    Telegram often links only part of a name ("Jan" of "Jan Kowalski") and
    leaves the rest as plain text. Without this, "Kowalski" would end up as
    the first word of the reason.
    """
    if not full_name:
        return extra
    name_words = full_name.split()
    linked_words = linked_text.split()
    if linked_words != name_words[: len(linked_words)]:
        return extra  # the link is not the start of the name; nothing to drop

    def plain(word: str) -> str:
        return word.strip(",.:;").casefold()

    remaining = [plain(w) for w in name_words[len(linked_words):]]
    words = extra.split(maxsplit=len(remaining))
    if remaining and [plain(w) for w in words[: len(remaining)]] == remaining:
        return words[len(remaining)].strip() if len(words) > len(remaining) else ""
    return extra


def _looks_like_user(token: str) -> bool:
    """Whether a word is clearly meant as a user (an id or a @username)."""
    return _ID_RE.match(token) is not None or (
        token.startswith("@") and _USERNAME_RE.match(token) is not None
    )


async def resolve_target(
    message: Message,
    command: CommandObject,
    *,
    allow_bots: bool = False,
    allow_chats: bool = False,
) -> Target:
    """Return the user a command refers to (plus extra data), or raise TargetError."""
    bot = message.bot
    text = message.text or ""
    parts = (command.args or "").split(maxsplit=1)
    reply = replied_message(message)

    # 1. A mention standing in the first argument position.
    start = _first_argument_offset(text)
    if start is not None:
        for entity in message.entities or []:
            if entity.offset != start:
                continue
            kind = getattr(entity.type, "value", entity.type)
            linked = _utf16_slice(text, entity.offset, entity.offset + entity.length)
            extra = _utf16_slice(text, entity.offset + entity.length).strip(" \t\n,:")
            if kind == "text_mention" and entity.user is not None:
                target = _from_user(entity.user, allow_bots)
                extra = _strip_name_remainder(extra, linked, entity.user.full_name).strip(" \t\n,:")
            elif kind == "mention":
                target = await _by_username(bot, linked.lstrip("@"), allow_bots, allow_chats)
            else:
                continue
            target.extra = extra
            return target

    # 2. An id or @username typed as the first argument. With a reply, a plain
    #    word is extra data for the replied-to person, not a user name.
    if parts and (reply is None or _looks_like_user(parts[0])):
        first, extra = parts[0], (parts[1].strip() if len(parts) > 1 else "")
        if _ID_RE.match(first):
            user_id = int(first)
            if user_id == 0:
                raise TargetError("That is not a valid user id.")
            target = await _by_id(bot, user_id, allow_bots, allow_chats)
        else:
            match = _USERNAME_RE.match(first)
            if not match:
                raise TargetError(_HINT)
            target = await _by_username(bot, match.group(1), allow_bots, allow_chats)
        target.extra = extra
        return target

    # 3. The message being replied to; everything typed is extra data.
    if reply is None:
        raise TargetError(_HINT)

    sender = reply.from_user
    sender_chat = getattr(reply, "sender_chat", None)
    if sender is None and sender_chat is None:
        raise TargetError("I can't tell who wrote that message.")

    if sender is None or sender.id in users.IGNORED_IDS:
        # Written on behalf of a channel or group (or by a Telegram service account).
        if sender_chat is not None and allow_chats:
            target = _from_chat(sender_chat, allow_bots, allow_chats)
        elif not allow_chats or sender is None:
            raise TargetError(_NOT_A_USER_MESSAGE)
        else:
            target = _from_user(sender, allow_bots)
    else:
        target = _from_user(sender, allow_bots)
    target.extra = (command.args or "").strip()
    return target
