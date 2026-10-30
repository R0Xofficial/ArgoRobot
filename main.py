"""
main.py - application entry point.

Responsibilities:
  * configure console logging
  * build the Bot and Dispatcher
  * wire up the core layer (roles, usage logging, error handling and traceback reports)
  * auto-discover feature modules from the modules/ package
  * announce startup and shutdown
  * run long polling

Module contract (modules/<name>.py or modules/<name>/__init__.py):
  router: aiogram.Router   (required)

Optional: PRIORITY (int, lower = checked earlier, default 0), PROTECTED (bool).
Files starting with "_" are ignored. Enabling and disabling modules at runtime
is handled by core.registry, so dropping a new file into modules/ is all that
is needed to add a feature.
"""

import asyncio
import importlib
import logging
import pkgutil
import sys

from aiogram import Bot, Dispatcher
from aiogram.client.default import DefaultBotProperties
from aiogram.enums import ParseMode

import modules
from config import settings
from core import captchas, chats, database, logs, registry, users
from core.errors import setup_error_handling
from core.middlewares import CleanCommandsMiddleware, ConnectionMiddleware, LoggingMiddleware, RolesMiddleware, UsersMiddleware
from core.text import esc

log = logging.getLogger("main")


def setup_logging() -> None:
    """Configure console logging."""
    logging.basicConfig(
        level=getattr(logging, settings.LOG_LEVEL.upper(), logging.INFO),
        format="%(asctime)s | %(levelname)-8s | %(name)s | %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
        stream=sys.stdout,
    )
    # Usage logging is done by our own middleware, so silence the per-update
    # "Update id=... is handled" lines coming from aiogram.
    logging.getLogger("aiogram.event").setLevel(logging.WARNING)


def discover_modules(dp: Dispatcher) -> None:
    """
    Import every module found in modules/ and attach its router.

    A broken module is logged and skipped; it must never prevent the bot
    from starting.
    """
    loaded = 0
    failed = 0
    found = []

    for info in pkgutil.iter_modules(modules.__path__):
        if info.name.startswith("_"):
            continue

        try:
            module = importlib.import_module(f"{modules.__name__}.{info.name}")
            router = getattr(module, "router", None)
            if router is None:
                log.warning("Module '%s' has no 'router' attribute, skipping", info.name)
                failed += 1
                continue
            found.append((info.name, module, router))
        except Exception:
            failed += 1
            log.exception("Failed to load module '%s'", info.name)

    # Routers are asked in this order and the first handler that matches wins.
    # A module can ask to be asked earlier with PRIORITY (lower comes first, the
    # default is 0); modules/gbans.py uses it so that globally banned users are
    # dealt with before any command of theirs is looked at.
    found.sort(key=lambda item: (getattr(item[1], "PRIORITY", 0), item[0]))

    for name, module, router in found:
        try:
            # The registry remembers the module and gates its router, so it
            # can be toggled on and off without detaching anything.
            registry.register(name, module, router)
            dp.include_router(router)
            loaded += 1
            log.info("Loaded module: %s", name)
        except Exception:
            failed += 1
            log.exception("Failed to load module '%s'", name)

    log.info("Module discovery finished: %d loaded, %d failed", loaded, failed)


async def on_startup(bot: Bot) -> None:
    await database.init()
    await registry.load_state()

    # Updates that piled up while the bot was offline are discarded so old
    # commands are not executed after a restart.
    await bot.delete_webhook(drop_pending_updates=True)

    me = await bot.get_me()
    log.info("Authorized as @%s (id=%s)", me.username, me.id)

    await logs.dev_log(
        bot,
        f"Bot started (@{esc(me.username)}). "
        f"Modules enabled: {registry.count_enabled()}/{registry.count_loaded()}.",
    )

    # Periodically forget chats the bot has been removed from (see core/chats.py).
    chats.start_cleanup_schedule(bot, settings.CHAT_CLEANUP_HOURS)

    # Kicks and unmutes of people who did not solve their captcha in time (core/captchas.py).
    captchas.start_sweeper(bot)


async def on_shutdown(bot: Bot) -> None:
    log.info("Shutting down")
    await chats.stop_cleanup_schedule()
    await captchas.stop_sweeper()
    await logs.dev_log(bot, "Bot stopped.")
    try:
        await database.close()
    except Exception:
        log.exception("Error while closing the database")


async def main() -> None:
    setup_logging()

    bot = Bot(
        token=settings.BOT_TOKEN,
        default=DefaultBotProperties(
            parse_mode=ParseMode.HTML,
            link_preview_is_disabled=True,  # no link previews anywhere
        ),
    )
    dp = Dispatcher()

    # Core layer: always active, cannot be disabled. (The management commands live
    # in modules/root.py, which is a protected module.)
    dp.update.outer_middleware(LoggingMiddleware())
    dp.update.outer_middleware(RolesMiddleware())
    dp.update.outer_middleware(UsersMiddleware())
    dp.update.outer_middleware(CleanCommandsMiddleware())
    dp.update.outer_middleware(ConnectionMiddleware())
    setup_error_handling(dp, bot)

    # Feature modules: auto-discovered, toggleable.
    discover_modules(dp)

    dp.startup.register(on_startup)
    dp.shutdown.register(on_shutdown)

    # Must be called after all routers are attached. Telegram sends only the
    # update types that are asked for: the ones handlers use, plus everything
    # that carries users for the phone book (joins, leaves, reactions, ...).
    allowed_updates = sorted(set(dp.resolve_used_update_types()) | set(users.UPDATE_TYPES))
    log.info("Requesting update types: %s", ", ".join(allowed_updates))
    await dp.start_polling(bot, allowed_updates=allowed_updates)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except (KeyboardInterrupt, SystemExit):
        log.info("Bot stopped")
