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


class Section(str):
    """
    Marks a command of a module's HELP with the section it belongs to. A module whose
    commands have sections gets a help page with one button per section instead of one
    long list. Use it like the "who may use it" label:

        HELP = [("/newfed <name>", "Create a federation.", Section("Owner commands"), "federation owner")]
    """


def sections(module: ModuleType) -> list[str]:
    """The sections of a module in the order they first appear."""
    found: list[str] = []
    for entry in getattr(module, "HELP", ()):
        name = next((item for item in entry[2:] if isinstance(item, Section)), None)
        if name is not None and name not in found:
            found.append(str(name))
    return found


def visible_sections(module: ModuleType, role: Role | None) -> list[tuple[int, str]]:
    """(number, name) of the sections that have a command this role may see. The number is stable."""
    names = sections(module)
    shown = {entry_section for entry_section in (_section(e) for e in _visible(module, role)) if entry_section}
    return [(i, name) for i, name in enumerate(names) if name in shown]


def _section(entry: tuple) -> str | None:
    name = next((item for item in entry[2:] if isinstance(item, Section)), None)
    return str(name) if name is not None else None


def _visible(module: ModuleType, role: Role | None) -> list[tuple]:
    out = []
    for entry in getattr(module, "HELP", ()):
        minimum = next((item for item in entry[2:] if isinstance(item, Role)), None)
        if minimum is None or (role is not None and role >= minimum):
            out.append(entry)
    return out


def pages(module: ModuleType) -> list[tuple[int, str]]:
    """
    (number, label) of the extra pages of a module: reference text that is too long for the
    module's page and gets a button of its own. A module declares them with

        HELP_PAGES = [("Lock types", function)]

    where the function takes no arguments and returns the HTML of the page: a string, or a
    list of strings when it needs more than one message (the page then has Previous/Next).
    """
    return [(number, label) for number, (label, _) in enumerate(getattr(module, "HELP_PAGES", ()))]


def page_texts(module: ModuleType, number: int) -> list[str] | None:
    found = getattr(module, "HELP_PAGES", ())
    if not 0 <= number < len(found):
        return None
    content = found[number][1]()
    return [content] if isinstance(content, str) else list(content)


ADMINS = "chat admins"
CHAT_OWNER = "chat owner"


def detailed(module: ModuleType, role: Role | None, section: str | None = None) -> list[tuple[str, str, str | None]]:
    """
    Like entries(), with who may use each command: (usage, description, label or None).
    With a section, only the commands of that section; without, only those that have none
    (all of them, for a module that has no sections).
    """
    visible: list[tuple[str, str, str | None]] = []
    default_label = getattr(module, "HELP_WHO", None)
    for entry in getattr(module, "HELP", ()):
        usage, description, *rest = entry
        minimum = next((item for item in rest if isinstance(item, Role)), None)
        if minimum is not None and (role is None or role < minimum):
            continue
        if _section(entry) != section:
            continue
        label = next((item for item in rest if isinstance(item, str) and not isinstance(item, Section)), None)
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
