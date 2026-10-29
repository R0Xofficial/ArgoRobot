"""
Help index: collects the commands that modules declare for /help.

A module describes its commands with a module-level HELP list. Each entry is
(usage, description) or (usage, description, minimum_role):

    HELP = [
        ("/ping", "Check how fast I respond"),
        ("/ban <user> [reason]", "Ban a user", Role.SUDO),
    ]

Entries a user is not allowed to use are hidden from them. A module with no
visible entries, or a disabled module, does not appear in /help at all.
The first line of the module docstring is its short description.
"""

from types import ModuleType

from core import registry
from core.roles import Role


def entries(module: ModuleType, role: Role | None) -> list[tuple[str, str]]:
    """Commands of `module` that a user with `role` is allowed to see."""
    visible: list[tuple[str, str]] = []
    for entry in getattr(module, "HELP", ()):
        usage, description, *rest = entry
        minimum = rest[0] if rest else None
        if minimum is not None and (role is None or role < minimum):
            continue
        visible.append((usage, description))
    return visible


def categories(role: Role | None) -> list[registry.ModuleInfo]:
    """Enabled modules that have at least one command visible to `role`."""
    found = [
        info
        for info in registry.all_modules()
        if info.enabled and entries(info.module, role)
    ]
    # Protected (core) modules first, the rest alphabetically.
    return sorted(found, key=lambda info: (not info.protected, info.name))
