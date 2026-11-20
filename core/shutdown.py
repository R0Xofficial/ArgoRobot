"""What the bot does when it is stopped (docker stop, Ctrl+C)."""
from __future__ import annotations

import asyncio
import logging

from aiogram import Bot

from core import captchas, chats, database, logreport, logs, mtproto, nightmode

log = logging.getLogger(__name__)

STOP_STEP_TIMEOUT = 8  # seconds a step that talks to Telegram may take while the bot stops


async def stop(bot: Bot) -> None:
    log.info("Shutting down")
    # The notice goes first, and every step that talks to Telegram has a time limit: the container is killed
    # when stopping takes too long (stop_grace_period), and a slow Telegram must not eat that time.
    try:
        await asyncio.wait_for(logs.dev_log(bot, "Bot stopped."), timeout=STOP_STEP_TIMEOUT)
    except Exception:
        log.warning("The 'Bot stopped.' notice could not be sent in time")
    try:
        await asyncio.wait_for(logreport.stop_schedule(bot), timeout=STOP_STEP_TIMEOUT)  # the last report, with everything since the previous one
    except Exception:
        log.warning("The last log report could not be sent in time")
    await chats.stop_cleanup_schedule()
    await captchas.stop_sweeper()
    await nightmode.stop_sweeper()
    try:
        await asyncio.wait_for(mtproto.stop(), timeout=5)
    except Exception:
        log.warning("Closing the MTProto client took too long")
    try:
        await database.close()
    except Exception:
        log.exception("Error while closing the database")
