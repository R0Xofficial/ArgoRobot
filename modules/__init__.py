"""
Feature modules. Every .py file (or package) in this folder that exposes a
`router` is loaded automatically by main.py. Files starting with "_" are ignored.

Conventions for a module:
  * The first line of the module docstring is its short description. It is shown
    in /modules and /help, so keep it to a few words, e.g. Core bot commands.
  * HELP is a list of (usage, description[, minimum_role][, label]) entries. It feeds
    the /help panel (see core/help_index.py). Modules without HELP are not listed there.
    Write each description as a full sentence: it is what a person reads to learn
    what the command does. The label says who can use it ("admins", "chat owner");
    a module-level HELP_WHO gives one to every entry that has none.
  * ABOUT explains the module on its /help page (what it is for, rules that apply to
    all its commands); HELP_NOTES is a list of (title, text) extra sections for syntax
    and examples. The first docstring line is the page's subtitle, so don't repeat
    the module's name in it.
  * Commands that act on a user use the syntax `/command <user> [extra data]`:
    the user first (reply, mention, @username or id), then whatever the command
    needs, e.g. `/ban @name spamming`. Use core.targets.resolve_target(); the
    extra data comes back in `target.extra`.
  * Commands that also work on bots, groups and channels (like /info) call
    resolve_target(message, command, allow_bots=True, allow_chats=True).
  * A module can add lines to /info with core.info_hooks.register(name, provider).
  * PROTECTED = True makes the module impossible to disable (only for core
    bot-management modules such as root).
"""
