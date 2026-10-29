"""
Registry of auto-discovered modules.

aiogram cannot cleanly detach a router at runtime, so toggling works through a
gate: every router of a module gets a filter that checks the module's `enabled`
flag. A disabled module simply does not match any event, and the event falls
through to the next router. The state is persisted in the database.

A module can protect itself from being disabled by defining `PROTECTED = True`
(modules/root.py does this, so management commands can never be switched off).
"""

import logging
from dataclasses import dataclass
from types import ModuleType

from aiogram import Router

from core import database

log = logging.getLogger(__name__)


class ProtectedModuleError(ValueError):
    """Raised when someone tries to disable a protected module."""


@dataclass
class ModuleInfo:
    name: str
    module: ModuleType
    router: Router
    enabled: bool = True

    @property
    def protected(self) -> bool:
        return bool(getattr(self.module, "PROTECTED", False))

    @property
    def description(self) -> str:
        doc = (self.module.__doc__ or "").strip()
        return doc.splitlines()[0] if doc else ""


_modules: dict[str, ModuleInfo] = {}


def _make_gate(info: ModuleInfo):
    # Must be a coroutine function: aiogram runs sync filters in a thread.
    async def gate(*_args, **_kwargs) -> bool:
        return info.enabled

    return gate


def register(name: str, module: ModuleType, router: Router) -> None:
    info = ModuleInfo(name=name.lower(), module=module, router=router)
    _modules[info.name] = info

    if info.protected:
        return

    gate = _make_gate(info)
    for event_name, observer in router.observers.items():
        if event_name == "error":
            continue
        observer.filter(gate)


async def load_state() -> None:
    """Apply the enabled/disabled flags stored in the database."""
    rows = await database.fetch_all("SELECT name, enabled FROM modules")
    for row in rows:
        info = _modules.get(row["name"])
        if info is not None and not info.protected:
            info.enabled = bool(row["enabled"])
            if not info.enabled:
                log.info("Module '%s' is disabled (stored state)", info.name)


async def set_enabled(name: str, enabled: bool) -> bool:
    """
    Enable or disable a module. Returns False if it does not exist.
    Raises ProtectedModuleError when asked to disable a protected module.
    """
    info = _modules.get(name.lower())
    if info is None:
        return False

    if info.protected and not enabled:
        raise ProtectedModuleError(f"Module '{info.name}' is protected and cannot be disabled.")

    info.enabled = enabled
    await database.execute(
        "INSERT INTO modules (name, enabled) VALUES ($1, $2) "
        "ON CONFLICT (name) DO UPDATE SET enabled = EXCLUDED.enabled",
        info.name,
        enabled,
    )
    log.info("Module '%s' %s", info.name, "enabled" if enabled else "disabled")
    return True


def get(name: str) -> ModuleInfo | None:
    return _modules.get(name.lower())


def all_modules() -> list[ModuleInfo]:
    return sorted(_modules.values(), key=lambda m: m.name)


def count_loaded() -> int:
    return len(_modules)


def count_enabled() -> int:
    return sum(1 for m in _modules.values() if m.enabled)
