"""
Full member lists through Telegram's MTProto interface (Telethon).

The Bot API cannot list the members of a chat. MTProto can, for a bot that is an
admin there, and that is what /zombies needs to find deleted accounts nobody has
ever seen write. It is optional: without API_ID and API_HASH (from my.telegram.org)
nothing here connects and members() returns None, and callers fall back to what
the bot knows by itself.

The client logs in with the bot token, only to read member lists; it receives no
updates, so it never competes with the normal polling. Its session is kept in
SESSION_FILE (mount a volume there, or every start is a new login, which Telegram
limits).
"""

import asyncio
import logging
import time
from pathlib import Path

from config import settings

log = logging.getLogger(__name__)

SESSION_FILE = "data/argo"
RETRY_AFTER = 300          # seconds to wait before trying to connect again after a failure
MAX_MEMBERS = 200_000

_client = None
_lock = asyncio.Lock()
_failed_at = 0.0


def configured() -> bool:
    return bool(getattr(settings, "API_ID", None) and getattr(settings, "API_HASH", None))


async def _connect():
    global _client, _failed_at
    if _client is not None:
        return _client
    if not configured() or (_failed_at and time.monotonic() - _failed_at < RETRY_AFTER):
        return None
    async with _lock:
        if _client is not None:
            return _client
        try:
            from telethon import TelegramClient

            Path(SESSION_FILE).parent.mkdir(parents=True, exist_ok=True)
            client = TelegramClient(SESSION_FILE, settings.API_ID, settings.API_HASH, receive_updates=False)
            await client.start(bot_token=settings.BOT_TOKEN)
            _client = client
            log.info("MTProto client connected")
        except Exception as exc:
            _failed_at = time.monotonic()
            log.warning("MTProto client could not connect: %r", exc)
    return _client


def _peer(chat_id: int):
    """The Telethon peer for a Bot API chat id (-100... is a supergroup or channel, other negatives are groups)."""
    from telethon.tl import types

    if chat_id <= -1_000_000_000_000:
        return types.InputPeerChannel(-chat_id - 1_000_000_000_000, 0)  # a bot may name a channel it belongs to without its hash
    return types.InputPeerChat(-chat_id)


async def members(chat_id: int) -> list[tuple[int, bool]] | None:
    """(user id, is a deleted account) of everyone in the chat, or None when that can't be had."""
    client = await _connect()
    if client is None:
        return None
    try:
        try:
            peer = await client.get_input_entity(chat_id)
        except ValueError:
            peer = _peer(chat_id)
        found: list[tuple[int, bool]] = []
        async for user in client.iter_participants(peer, limit=MAX_MEMBERS):
            found.append((user.id, bool(getattr(user, "deleted", False))))
        return found
    except Exception as exc:
        log.warning("MTProto member list of %s failed: %r", chat_id, exc)
        return None


async def stop() -> None:
    global _client
    client, _client = _client, None
    if client is not None:
        try:
            await client.disconnect()
        except Exception:
            log.exception("Closing the MTProto client failed")
