"""
Telling a join or a leave that arrived as a service message from one that did not.

Telegram announces who joined or left with a service message in the chat ("X joined the group"),
and, to bots that are admins, with a chat_member update as well. In a group whose member list is
hidden, or in a big one, the service messages are not sent at all, so only the update tells the
bot. modules/greetings.py handles both: the service message at once, and the update after a short
wait, only when no service message came for the same person.
"""

import asyncio
import time

WAIT = 3.0  # seconds the update waits for the service message of the same event
WINDOW = 4.0  # a service message this long before the update still counts as the same event
_MAX = 5000

_marks: dict[tuple[str, int, int], float] = {}


def mark(kind: str, chat_id: int, user_id: int) -> None:
    """A service message about `user` ("join" or "leave") was handled."""
    if len(_marks) > _MAX:
        _marks.clear()
    _marks[(kind, chat_id, user_id)] = time.monotonic()


async def no_service_message(kind: str, chat_id: int, user_id: int) -> bool:
    """
    For a chat_member update: wait a moment and say whether the service message of the same
    event never came, so that the update has to do the greeting itself.
    """
    arrived = time.monotonic()
    if WAIT > 0:
        await asyncio.sleep(WAIT)
    return _marks.get((kind, chat_id, user_id), 0.0) < arrived - WINDOW
