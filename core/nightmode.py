"""
Nightmode: the chat is closed for the night and opens again in the morning.

A chat's admins choose the hours (and the time zone they are in). When the night starts, the
bot saves the chat's permissions and takes away everybody's right to send messages; when it ends,
it puts the saved permissions back. Admins can always write, nothing else changes for them.

A sweep every half minute starts and ends nights. The chat's saved permissions are kept in the
database, so a restart in the middle of a night does not lose them (modules/nightmode.py has the
commands; main.py starts the sweep).
"""

import asyncio
import json
import logging
import re
import time
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone, tzinfo
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from aiogram import Bot
from aiogram.types import ChatPermissions

from core import database

log = logging.getLogger(__name__)

SWEEP_EVERY = 30.0
FAILURE_COOLDOWN = 300.0  # after a failure, leave the chat alone for this long

DEFAULT_START = 23 * 60  # minutes after midnight
DEFAULT_END = 7 * 60
DEFAULT_ZONE = "UTC"

SEND_RIGHTS = (
    "can_send_messages",
    "can_send_audios",
    "can_send_documents",
    "can_send_photos",
    "can_send_videos",
    "can_send_video_notes",
    "can_send_voice_notes",
    "can_send_polls",
    "can_send_other_messages",
    "can_add_web_page_previews",
)


@dataclass(frozen=True)
class Settings:
    enabled: bool = False
    start: int = DEFAULT_START
    end: int = DEFAULT_END
    zone: str = DEFAULT_ZONE
    active: bool = False  # the bot has closed the chat and not opened it yet
    saved: dict | None = None  # the chat's permissions from before the night


# --- Time ---------------------------------------------------------------------------------

_OFFSET = re.compile(r"^(?:utc|gmt)?\s*([+-])\s*(\d{1,2})(?::?(\d{2}))?$", re.IGNORECASE)


def parse_zone(text: str) -> tuple[str, tzinfo] | None:
    """'Europe/Warsaw', 'UTC+2', '+5:30' -> (name to keep, tzinfo); None when it is neither."""
    text = text.strip()
    if not text:
        return None
    if text.upper() in ("UTC", "GMT", "Z"):
        return "UTC", timezone.utc
    match = _OFFSET.match(text)
    if match:
        hours, minutes = int(match.group(2)), int(match.group(3) or 0)
        if hours > 14 or minutes > 59:
            return None
        delta = timedelta(hours=hours, minutes=minutes)
        sign = match.group(1)
        return f"UTC{sign}{hours:02d}:{minutes:02d}", timezone(delta if sign == "+" else -delta)
    if re.fullmatch(r"[A-Za-z_]+(?:/[A-Za-z0-9_+\-]+){1,2}", text):
        try:
            return text, ZoneInfo(text)
        except (ZoneInfoNotFoundError, ValueError, OSError):
            return None
    return None


def zone_of(name: str) -> tzinfo:
    parsed = parse_zone(name)
    return parsed[1] if parsed else timezone.utc


def parse_clock(text: str) -> int | None:
    """'23:00', '7', '07:30' -> minutes after midnight."""
    match = re.fullmatch(r"(\d{1,2})(?::(\d{2}))?", text.strip())
    if match is None:
        return None
    hours, minutes = int(match.group(1)), int(match.group(2) or 0)
    if hours > 23 or minutes > 59:
        return None
    return hours * 60 + minutes


def clock_text(minutes: int) -> str:
    return f"{minutes // 60:02d}:{minutes % 60:02d}"


def is_night(settings: Settings, now: datetime | None = None) -> bool:
    """Whether `now` is inside the night. A night may pass midnight (23:00-07:00); equal hours mean no night."""
    now = now or datetime.now(timezone.utc)
    local = now.astimezone(zone_of(settings.zone))
    minute = local.hour * 60 + local.minute
    start, end = settings.start, settings.end
    if start == end:
        return False
    if start < end:
        return start <= minute < end
    return minute >= start or minute < end


# --- Storage ------------------------------------------------------------------------------

async def get(chat_id: int) -> Settings:
    row = await database.fetch_one(
        "SELECT enabled, start_minute, end_minute, zone, active, saved FROM nightmode WHERE chat_id = $1", chat_id
    )
    return _from_row(row)


def _from_row(row) -> Settings:
    if row is None:
        return Settings()
    saved = row["saved"]
    if isinstance(saved, str):
        try:
            saved = json.loads(saved)
        except ValueError:
            saved = None
    return Settings(bool(row["enabled"]), row["start_minute"], row["end_minute"], row["zone"], bool(row["active"]), saved)


async def update(chat_id: int, **values) -> None:
    """Change settings: enabled, start, end, zone."""
    current = await get(chat_id)
    merged = {name: values.get(name, getattr(current, name)) for name in ("enabled", "start", "end", "zone")}
    await database.execute(
        "INSERT INTO nightmode (chat_id, enabled, start_minute, end_minute, zone) VALUES ($1, $2, $3, $4, $5) "
        "ON CONFLICT (chat_id) DO UPDATE SET enabled = EXCLUDED.enabled, start_minute = EXCLUDED.start_minute, "
        "end_minute = EXCLUDED.end_minute, zone = EXCLUDED.zone",
        chat_id, merged["enabled"], merged["start"], merged["end"], merged["zone"],
    )


async def _set_state(chat_id: int, active: bool, saved: dict | None) -> None:
    await database.execute(
        "UPDATE nightmode SET active = $2, saved = $3 WHERE chat_id = $1",
        chat_id, active, json.dumps(saved) if saved is not None else None,
    )


# --- Closing and opening the chat -------------------------------------------------------------

def _closed(saved: dict) -> ChatPermissions:
    rights = {name: False for name in SEND_RIGHTS}
    rest = {name: value for name, value in saved.items() if name not in SEND_RIGHTS}
    return ChatPermissions(**rest, **rights)


def _reopened(saved: dict | None) -> ChatPermissions:
    if saved:
        return ChatPermissions(**saved)
    return ChatPermissions(**{name: True for name in SEND_RIGHTS})


async def close(bot: Bot, chat_id: int) -> None:
    """Start the night: save the permissions, then take the sending rights away."""
    chat = await bot.get_chat(chat_id)
    permissions = getattr(chat, "permissions", None)
    saved = permissions.model_dump(exclude_none=True) if permissions is not None else {}
    # Saved first: if closing fails nothing is left behind, if the bot dies in between the chat is still restored.
    await _set_state(chat_id, True, saved)
    try:
        await bot.set_chat_permissions(chat_id, _closed(saved), use_independent_chat_permissions=True)
    except Exception:
        await _set_state(chat_id, False, None)
        raise


async def reopen(bot: Bot, chat_id: int, saved: dict | None) -> None:
    """End the night: put the saved permissions back."""
    await bot.set_chat_permissions(chat_id, _reopened(saved), use_independent_chat_permissions=True)
    await _set_state(chat_id, False, None)


_failed_until: dict[int, float] = {}


async def reconcile(bot: Bot, chat_id: int, settings: Settings | None = None) -> str | None:
    """
    Bring a chat in line with the clock. Returns "closed" or "opened" when it changed, else None.
    Raises when Telegram refuses (no right to change the permissions).
    """
    settings = settings or await get(chat_id)
    night = settings.enabled and is_night(settings)
    if night and not settings.active:
        await close(bot, chat_id)
        return "closed"
    if not night and settings.active:
        await reopen(bot, chat_id, settings.saved)
        return "opened"
    return None


async def announce(bot: Bot, chat_id: int, what: str, settings: Settings) -> None:
    """Say in the chat that the night began or ended (or that it could not be started)."""
    if what == "closed":
        text = f"<b>Night mode is on.</b> The chat is closed until {clock_text(settings.end)} ({settings.zone}). Good night!"
    elif what == "opened":
        text = "<b>Night mode is over.</b> The chat is open again. Good morning!"
    else:
        text = "Night mode is on, but I can't change the permissions of this chat. I need the right to restrict members."
    try:
        await bot.send_message(chat_id, text)
    except Exception:
        log.warning("Nightmode could not write to chat %s", chat_id)


async def sweep(bot: Bot) -> None:
    """One round over every chat that has nightmode on or is closed by it."""
    rows = await database.fetch_all(
        "SELECT chat_id, enabled, start_minute, end_minute, zone, active, saved FROM nightmode WHERE enabled OR active"
    )
    for row in rows:
        chat_id = row["chat_id"]
        if time.monotonic() < _failed_until.get(chat_id, 0.0):
            continue
        settings = _from_row(row)
        try:
            what = await reconcile(bot, chat_id, settings)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            _failed_until[chat_id] = time.monotonic() + FAILURE_COOLDOWN
            log.warning("Nightmode could not change the permissions of chat %s: %s", chat_id, exc)
            await announce(bot, chat_id, "failed", settings)
            continue
        if what:
            await announce(bot, chat_id, what, settings)


# --- The sweep ---------------------------------------------------------------------------------

_task: asyncio.Task | None = None


async def _loop(bot: Bot) -> None:
    while True:
        try:
            await sweep(bot)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("Nightmode sweep failed")
        await asyncio.sleep(SWEEP_EVERY)


def start_sweeper(bot: Bot) -> None:
    global _task
    if _task is None:
        _task = asyncio.get_running_loop().create_task(_loop(bot), name="nightmode-sweeper")


async def stop_sweeper() -> None:
    global _task
    if _task is None:
        return
    _task.cancel()
    try:
        await _task
    except asyncio.CancelledError:
        pass
    _task = None
