"""
Help index: collects the commands that modules declare for /help.

A module describes its commands with a module-level HELP list. Each entry is
(usage, description) or (usage, description, minimum_role):

    HELP = [
        ("/ping", "Check how fast I respond"),
        ("/ban <user> [reason]", "Ban a user", Role.SUDO),
    ]

An entry may also carry a label saying who can use it, as a string anywhere after
the description (ADMINS, CHAT_OWNER, or your own text). A module can set HELP_WHO to
give the same label to every entry that has none. A role in the entry hides it from
people below that role and labels it "Sudo and up", unless there is a string label.

A module is explained on its /help page by:

    ABOUT       a few sentences on what the module is for and how its commands work
                (plain text; a blank line starts a new paragraph)
    HELP_NOTES  extra sections, a list of (title, text), e.g. syntax that applies to
                several commands

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
        minimum = next((item for item in rest if isinstance(item, Role)), None)
        if minimum is not None and (role is None or role < minimum):
            continue
        visible.append((usage, description))
    return visible


ADMINS = "chat admins"
CHAT_OWNER = "chat owner"


def detailed(module: ModuleType, role: Role | None) -> list[tuple[str, str, str | None]]:
    """Like entries(), with who may use each command: (usage, description, label or None)."""
    visible: list[tuple[str, str, str | None]] = []
    default_label = getattr(module, "HELP_WHO", None)
    for entry in getattr(module, "HELP", ()):
        usage, description, *rest = entry
        minimum = next((item for item in rest if isinstance(item, Role)), None)
        if minimum is not None and (role is None or role < minimum):
            continue
        label = next((item for item in rest if isinstance(item, str)), None)
        if label is None and minimum is not None:
            label = f"{minimum.name.title()} and up"
        visible.append((usage, description, label or default_label))
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
