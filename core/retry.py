"""Retrying a call that failed because Telegram could not be reached for a moment."""
from __future__ import annotations

import asyncio
import logging
from collections.abc import Awaitable, Callable
from typing import TypeVar

from aiogram.exceptions import TelegramNetworkError, TelegramRetryAfter

log = logging.getLogger(__name__)

T = TypeVar("T")

# Seconds to wait before the 2nd, 3rd ... attempt. After the last one the error is raised as it is.
DELAYS = (2, 5, 10, 20, 30, 60)


async def network_retry(name: str, call: Callable[[], Awaitable[T]], delays: tuple[float, ...] = DELAYS) -> T:
    """
    Await call() again while Telegram cannot be reached (timeout, no connection) or asks to wait.
    Used at start-up, where one bad moment of the network would otherwise stop the whole bot.
    """
    attempt = 0
    while True:
        try:
            return await call()
        except (TelegramNetworkError, TelegramRetryAfter) as exc:
            if attempt >= len(delays):
                raise
            wait = max(delays[attempt], getattr(exc, "retry_after", 0))
            attempt += 1
            log.warning("%s failed (%s), attempt %d of %d, trying again in %ss", name, exc, attempt, len(delays) + 1, wait)
            await asyncio.sleep(wait)
