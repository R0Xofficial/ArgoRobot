"""
Scheduled log report for the developer log chat.

Everything the bot writes to its console log (INFO and above: commands that were
used, warnings, errors with their tracebacks) is also kept in memory. Every
LOG_REPORT_HOURS hours, and once more when the bot stops, the collected lines
are sent to the developer log (DEV_LOG_CHAT) as one .txt file:

    Log report
    From: 2026-11-11 14:00:03 UTC
    To:   2026-11-11 20:00:01 UTC
    Entries: 412 (3 warnings, 1 error)

    [argo-log_20261111-1400_20261111-2000.txt]

Inside the file every line is a time (UTC), a level, the part of the bot that
wrote it and what happened:

    14:00:03  INFO     main     Authorized as @ArgoRobot (id=123)
    14:02:17  INFO     usage    command=/ban user=1 (@x) chat=-100 [supergroup]

A report that could not be delivered is kept and sent together with the next
one. The buffer is limited (MAX_ENTRIES); when it overflows the oldest entries
are dropped and the report says how many.
"""

import asyncio
import logging
from collections import deque
from datetime import datetime, timezone

from aiogram import Bot

from core import logs

log = logging.getLogger(__name__)

MAX_ENTRIES = 20000
MAX_ENTRY_CHARS = 4000       # a single entry (a long traceback, say) is cut to this

# (time, level number, level name, logger name, text)
_Entry = tuple[datetime, int, str, str, str]


class BufferHandler(logging.Handler):
    """Keeps log records in memory until they are sent."""

    def __init__(self, level: int = logging.INFO) -> None:
        super().__init__(level)
        self.entries: deque[_Entry] = deque(maxlen=MAX_ENTRIES)
        self.dropped = 0
        self._formatter = logging.Formatter()

    def emit(self, record: logging.LogRecord) -> None:
        try:
            text = record.getMessage()
            if record.exc_info:
                text += "\n" + self._formatter.formatException(record.exc_info)
            if len(text) > MAX_ENTRY_CHARS:
                text = text[:MAX_ENTRY_CHARS] + " ... (cut)"
            if len(self.entries) == self.entries.maxlen:
                self.dropped += 1
            self.entries.append((
                datetime.fromtimestamp(record.created, timezone.utc),
                record.levelno,
                record.levelname,
                record.name.split(".")[-1],
                text,
            ))
        except Exception:
            self.handleError(record)

    def take(self) -> tuple[list[_Entry], int]:
        """Hand over everything collected so far and start empty."""
        batch, dropped = list(self.entries), self.dropped
        self.entries.clear()
        self.dropped = 0
        return batch, dropped

    def put_back(self, batch: list[_Entry], dropped: int) -> None:
        """Return a batch that could not be sent, in front of what came in meanwhile."""
        newer = list(self.entries)
        self.entries.clear()
        self.entries.extend(batch + newer)          # the oldest fall out first
        overflow = max(0, len(batch) + len(newer) - MAX_ENTRIES)
        self.dropped += dropped + overflow


_handler: BufferHandler | None = None
_task: asyncio.Task | None = None


def install() -> BufferHandler:
    """Start collecting log records (call once, after the console logging is set up)."""
    global _handler
    if _handler is None:
        _handler = BufferHandler()
        logging.getLogger().addHandler(_handler)
    return _handler


# --- Building the report ----------------------------------------------------

def render(batch: list[_Entry], dropped: int = 0) -> tuple[str, str, bytes]:
    """(caption in HTML, file name, file content) for a batch of entries. The batch must not be empty."""
    first, last = batch[0][0], batch[-1][0]
    warnings = sum(1 for e in batch if e[1] == logging.WARNING)
    errors = sum(1 for e in batch if e[1] >= logging.ERROR)

    summary = f"{len(batch)}"
    parts = []
    if warnings:
        parts.append(f"{warnings} warning{'s' if warnings != 1 else ''}")
    if errors:
        parts.append(f"{errors} error{'s' if errors != 1 else ''}")
    if parts:
        summary += f" ({', '.join(parts)})"

    caption = (
        "<b>Log report</b>\n"
        f"<b>From:</b> {logs.timestamp(first)}\n"
        f"<b>To:</b> {logs.timestamp(last)}\n"
        f"<b>Entries:</b> {summary}"
    )
    if dropped:
        caption += f"\n<i>{dropped} older entries were dropped to save memory.</i>"

    lines = [
        "Argo log report (all times are UTC)",
        f"From: {first:%Y-%m-%d %H:%M:%S}",
        f"To:   {last:%Y-%m-%d %H:%M:%S}",
        f"Entries: {summary}",
    ]
    if dropped:
        lines.append(f"{dropped} older entries were dropped to save memory.")
    lines.append("")

    day = None
    for when, _, level, name, text in batch:
        if when.date() != day:
            day = when.date()
            lines.append(f"--- {day:%Y-%m-%d} ---")
        first_line, *rest = text.split("\n")
        lines.append(f"{when:%H:%M:%S}  {level:<8} {name:<10} {first_line}")
        lines.extend(f"    {extra}" for extra in rest)

    filename = f"argo-log_{first:%Y%m%d-%H%M}_{last:%Y%m%d-%H%M}.txt"
    return caption, filename, ("\n".join(lines) + "\n").encode("utf-8")


async def send_report(bot: Bot) -> bool:
    """
    Send what has been collected to the developer log. Nothing collected: nothing is sent
    (returns True). When the sending fails, the entries are kept for the next report.
    """
    if _handler is None:
        return True
    batch, dropped = _handler.take()
    if not batch:
        return True
    caption, filename, content = render(batch, dropped)
    if await logs.dev_file(bot, filename, content, caption):
        return True
    _handler.put_back(batch, dropped)
    return False


# --- Schedule -----------------------------------------------------------------

async def _loop(bot: Bot, hours: float) -> None:
    while True:
        await asyncio.sleep(hours * 3600)
        try:
            await send_report(bot)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("Scheduled log report failed")


def start_schedule(bot: Bot, hours: float) -> None:
    """Start sending reports every `hours` hours. `hours` <= 0 turns them off."""
    global _task
    if hours <= 0 or (_task and not _task.done()):
        return
    _task = asyncio.create_task(_loop(bot, hours), name="log-report")
    log.info("Log report every %s hours", f"{hours:g}")


async def stop_schedule(bot: Bot | None = None) -> None:
    """Stop the schedule and, when given a bot, send the last report."""
    global _task
    if _task:
        _task.cancel()
        try:
            await _task
        except asyncio.CancelledError:
            pass
        _task = None
    if bot is not None and _handler is not None:
        try:
            await send_report(bot)
        except Exception:
            log.exception("Final log report failed")
