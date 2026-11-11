"""Keep spammers out by their profile"""

import logging
from typing import Any

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import approvals, enforce, greetings, immunity, joinblacklist, moderation, roles
from core.blacklist import TriggerError
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.text import esc, truncate, user_link

log = logging.getLogger(__name__)

HELP_TITLE = "Join Blacklists"

ABOUT = """
Block people from joining when their profile contains certain words, which stops spammers before they post a single message. There are four lists, one for each part of a profile that is checked: the name, the @username, the bio and the personal channel (its name and its username). Each chat can have up to 10 triggers per list.

Someone who matches is banned and the "joined" message is removed. Bots, chat admins, approved users and the bot's staff are never blocked, and neither is anyone who was added by a chat admin.

Triggers match whole words and ignore case, so "casino" catches "Best Casino!" but not "casinos". To catch a word inside a longer one, use a wildcard: *casino* matches @best_casino_bot. Write one trigger per line to add several at once.

The commands that change the lists are for chat admins with the right to change chat info. Anyone can look at them.
"""

HELP = [
    ("/joinBlacklists", "List the join blacklists of this chat. Alias: /joinblacklist."),
    ("/blacklistname <trigger>", "Block people whose name matches the trigger.", "admins"),
    ("/blacklistusername <trigger>", "Block people whose @username matches the trigger.", "admins"),
    ("/blacklistbio <trigger>", "Block people whose bio matches the trigger.", "admins"),
    ("/blacklistchannel <trigger>", "Block people whose personal channel (its name or its username) matches the trigger.", "admins"),
    ("/unblacklistname <trigger>", "Remove a name trigger.", "admins"),
    ("/unblacklistusername <trigger>", "Remove a username trigger.", "admins"),
    ("/unblacklistbio <trigger>", "Remove a bio trigger.", "admins"),
    ("/unblacklistchannel <trigger>", "Remove a channel trigger.", "admins"),
]

HELP_NOTES = [
    (
        "Wildcards",
        """
        ? - any single character except whitespace.
        * - any run of characters except whitespace (great for URLs).
        ** - any run of characters, whitespace included.
        A backslash makes them literal: \\* and \\?
        """,
    ),
    (
        "Examples",
        """
        /blacklistname free crypto - names with the words "free crypto".
        /blacklistusername *casino* - any username with casino inside.
        /blacklistbio t.me/* - bios that link to a Telegram chat or channel.
        /unblacklistbio t.me/*
        """,
    ),
]

router = Router(name="joinblacklists")

# Before the greetings (which would welcome a spammer), after the global bans.
PRIORITY = -40

RIGHT = "can_change_info"
MAX_SHOWN = 20
LABELS = {"name": "Names", "username": "Usernames", "bio": "Bios", "channel": "Channels"}


# --- Commands -----------------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """Chat business: admins only, never the bot's staff."""
    return await moderation.begin_chat_command(message, role, right=RIGHT) is not None


@router.message(cmd("joinblacklists", "joinblacklist"))
async def cmd_joinblacklists(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    found = await joinblacklist.patterns(message.chat.id)
    title = f"<b>Join blacklists in {esc(message.chat.title or 'this chat')}</b>"
    if not any(found.values()):
        await reply(
            message,
            f"{title}\n\nThere are none yet. Add one with /blacklistname, /blacklistusername, "
            "/blacklistbio or /blacklistchannel.",
        )
        return
    lines = [title]
    for kind in joinblacklist.KINDS:
        if found[kind]:
            lines += ["", f"<b>{LABELS[kind]}</b>"] + [f"• <code>{esc(p)}</code>" for p in found[kind]]
    await reply(message, "\n".join(lines))


async def _add(message: Message, command: CommandObject, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role):
        return
    texts = [line for line in (command.args or "").splitlines() if line.strip()]
    if not texts:
        await reply(message, f"Give me triggers, one per line. Example: /blacklist{kind} spam word")
        return
    added: list[str] = []
    problems: list[str] = []
    for text in texts:
        try:
            pattern = joinblacklist.check(kind, text)
        except TriggerError as exc:
            problems.append(f"<code>{esc(truncate(text, 40))}</code>: {esc(exc)}")
            continue
        outcome = await joinblacklist.add(message.chat.id, kind, pattern)
        if outcome == "added":
            added.append(f"<code>{esc(pattern)}</code>")
        elif outcome == "exists":
            problems.append(f"<code>{esc(pattern)}</code>: already in the list.")
        else:
            note = f"The {kind} list is full ({joinblacklist.MAX_PER_KIND} triggers). Remove one first."
            if note not in problems:
                problems.append(note)
    lines: list[str] = []
    if added:
        lines.append(f"<b>Added {len(added)} {kind} trigger{'' if len(added) == 1 else 's'}:</b>")
        lines += [f"• {item}" for item in added[:MAX_SHOWN]]
    if problems:
        lines.append("<b>Skipped:</b>")
        lines += [f"• {p}" for p in problems[:MAX_SHOWN]]
    await reply(message, "\n".join(lines))


async def _remove(message: Message, command: CommandObject, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role):
        return
    texts = [line for line in (command.args or "").splitlines() if line.strip()]
    if not texts:
        await reply(message, f"Give me the triggers to remove, one per line. Example: /unblacklist{kind} spam word")
        return
    removed: list[str] = []
    missing: list[str] = []
    for text in texts:
        try:
            pattern = joinblacklist.check(kind, text)
        except TriggerError:
            missing.append(f"<code>{esc(truncate(text, 40))}</code>")
            continue
        shown = f"<code>{esc(pattern)}</code>"
        (removed if await joinblacklist.remove(message.chat.id, kind, pattern) else missing).append(shown)
    lines: list[str] = []
    if removed:
        lines.append(f"<b>Removed {len(removed)} {kind} trigger{'' if len(removed) == 1 else 's'}:</b>")
        lines += [f"• {item}" for item in removed[:MAX_SHOWN]]
    if missing:
        lines.append(f"<b>Not in the {kind} list:</b>")
        lines += [f"• {item}" for item in missing[:MAX_SHOWN]]
    await reply(message, "\n".join(lines))


@router.message(cmd("blacklistname"))
async def cmd_blacklistname(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _add(message, command, role, "name")


@router.message(cmd("blacklistusername"))
async def cmd_blacklistusername(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _add(message, command, role, "username")


@router.message(cmd("blacklistbio"))
async def cmd_blacklistbio(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _add(message, command, role, "bio")


@router.message(cmd("blacklistchannel"))
async def cmd_blacklistchannel(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _add(message, command, role, "channel")


@router.message(cmd("unblacklistname"))
async def cmd_unblacklistname(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _remove(message, command, role, "name")


@router.message(cmd("unblacklistusername"))
async def cmd_unblacklistusername(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _remove(message, command, role, "username")


@router.message(cmd("unblacklistbio"))
async def cmd_unblacklistbio(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _remove(message, command, role, "bio")


@router.message(cmd("unblacklistchannel"))
async def cmd_unblacklistchannel(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _remove(message, command, role, "channel")


# --- Checking who joins -------------------------------------------------------------------------

async def _profile_match(bot: Bot, chat_id: int, user) -> tuple[str, str] | None:
    """(kind, trigger) of the first part of this person's profile that is blacklisted, or None."""
    hit = await joinblacklist.match(chat_id, "name", user.full_name)
    if hit is not None:
        return "name", hit
    hit = await joinblacklist.match(chat_id, "username", user.username)
    if hit is not None:
        return "username", hit

    wants_bio = await joinblacklist.has(chat_id, "bio")
    wants_channel = await joinblacklist.has(chat_id, "channel")
    if not (wants_bio or wants_channel):
        return None
    try:
        full = await bot.get_chat(user.id)
    except TelegramAPIError:
        return None  # a profile that can't be read is not a reason to ban anyone
    if wants_bio:
        hit = await joinblacklist.match(chat_id, "bio", getattr(full, "bio", None))
        if hit is not None:
            return "bio", hit
    if wants_channel:
        channel = getattr(full, "personal_chat", None)
        if channel is not None:
            for text in (getattr(channel, "title", None), getattr(channel, "username", None)):
                hit = await joinblacklist.match(chat_id, "channel", text)
                if hit is not None:
                    return "channel", hit
    return None


async def _exempt(bot: Bot, message: Message, user) -> bool:
    """Bots, immune accounts, the bot's staff, approved users, chat admins and people an admin added."""
    chat_id = message.chat.id
    if user.is_bot or immunity.is_immune(user.id) or await roles.get_role(user.id) is not None or await approvals.is_approved(chat_id, user.id):
        return True
    if await moderation.is_chat_admin(bot, chat_id, user.id):
        return True
    adder = message.from_user
    if adder is not None and adder.id != user.id and await moderation.is_chat_admin(bot, chat_id, adder.id):
        return True
    return False


async def _blocked(message: Message, bot: Bot) -> dict[str, Any] | bool:
    if message.chat.type not in GROUP_TYPES or not message.new_chat_members:
        return False
    chat_id = message.chat.id
    if not any((await joinblacklist.patterns(chat_id)).values()):
        return False
    found = []
    for user in message.new_chat_members:
        if user.is_bot or user.id == bot.id:
            continue
        hit = await _profile_match(bot, chat_id, user)
        if hit is not None and not await _exempt(bot, message, user):
            found.append((user, *hit))
    return {"join_hits": found} if found else False


@router.message(F.new_chat_members, _blocked)
async def on_blocked_join(message: Message, bot: Bot, join_hits: list[tuple[Any, str, str]]) -> None:
    banned = False
    for user, kind, trigger in join_hits:
        try:
            await enforce.punish(bot, message.chat.id, user.id, "ban")
        except (ModerationError, TelegramAPIError) as exc:
            log.info("Join blacklist: could not ban %s in chat %s: %s", user.id, message.chat.id, exc)
            continue
        banned = True
        log.info("Join blacklist: banned %s in chat %s (%s trigger %r)", user.id, message.chat.id, kind, trigger)
    if banned or (await greetings.get(message.chat.id))["clean_service"]:  # the greetings handler never sees this message
        try:
            await message.delete()
        except TelegramAPIError:
            pass
