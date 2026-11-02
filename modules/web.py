"""
Small HTTP helper for the lookup commands (/tr, /dict, /time, /weather, /cash).

Everything they use is a free public service that needs no API key. A service that
is down, slow or answers with nonsense turns into a WebError with a message that is
safe to show to people; handlers never see aiohttp's own exceptions.
"""

import asyncio
import logging
from typing import Any

import aiohttp

log = logging.getLogger(__name__)

TIMEOUT_SECONDS = 10
UNAVAILABLE = "The service behind this command is not answering right now. Try again in a moment."


class WebError(Exception):
    """A lookup failed. str(error) is a sentence that can be shown to the user."""


class NotFound(WebError):
    """The service answered, but it does not know what was asked for."""


async def get_json(url: str, params: dict[str, Any] | None = None) -> Any:
    timeout = aiohttp.ClientTimeout(total=TIMEOUT_SECONDS)
    try:
        async with aiohttp.ClientSession(timeout=timeout) as session:
            async with session.get(url, params=params) as response:
                if response.status == 404:
                    raise NotFound("Nothing found.")
                if response.status >= 400:
                    log.warning("GET %s answered %s", url, response.status)
                    raise WebError(UNAVAILABLE)
                return await response.json(content_type=None)
    except WebError:
        raise
    except (aiohttp.ClientError, asyncio.TimeoutError, ValueError):
        log.warning("GET %s failed", url, exc_info=True)
        raise WebError(UNAVAILABLE) from None
